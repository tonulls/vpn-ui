package controller

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/web/proxyip"
	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/mhsanaei/3x-ui/v2/web/session"
	"github.com/mhsanaei/3x-ui/v2/web/websocket"

	"github.com/gin-gonic/gin"
	ws "github.com/gorilla/websocket"
)

const (
	// Time allowed to write a message to the peer
	writeWait = 10 * time.Second

	// Time allowed to read the next pong message from the peer
	pongWait = 60 * time.Second

	// Send pings to peer with this period (must be less than pongWait)
	pingPeriod = (pongWait * 9) / 10

	// Maximum message size allowed from peer
	maxMessageSize = 512
)

func websocketOriginAllowed(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		// Non-browser clients may omit Origin; browsers send it on WebSocket handshakes.
		return true
	}
	if len(origins) != 1 {
		return false
	}
	return sameWebSocketOrigin(origins[0], websocketRequestScheme(r), r.Host)
}

func websocketRequestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	trustedProxies, err := (&service.SettingService{}).GetWebTrustedProxies()
	if err == nil && proxyip.IsTrustedProxy(r.RemoteAddr, trustedProxies) {
		forwarded := r.Header.Values("X-Forwarded-Proto")
		if len(forwarded) == 1 {
			scheme := strings.ToLower(strings.TrimSpace(forwarded[0]))
			if scheme == "http" || scheme == "https" {
				return scheme
			}
		}
	}
	return "http"
}

func sameWebSocketOrigin(rawOrigin, expectedScheme, requestHost string) bool {
	if expectedScheme != "http" && expectedScheme != "https" {
		return false
	}
	if strings.TrimSpace(rawOrigin) != rawOrigin {
		return false
	}
	origin, err := url.Parse(rawOrigin)
	if err != nil || origin.User != nil || origin.Opaque != "" || origin.Host == "" ||
		origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	if !strings.EqualFold(origin.Scheme, expectedScheme) {
		return false
	}
	originAuthority, ok := normalizeWebSocketAuthority(origin.Host, origin.Scheme)
	if !ok {
		return false
	}
	requestAuthority, ok := normalizeWebSocketAuthority(requestHost, expectedScheme)
	return ok && originAuthority == requestAuthority
}

func normalizeWebSocketAuthority(authority, scheme string) (string, bool) {
	if authority == "" {
		return "", false
	}
	parsed, err := url.Parse("//" + authority)
	if err != nil || parsed.Host == "" || parsed.Host != authority || parsed.User != nil ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	host := parsed.Hostname()
	if host == "" {
		return "", false
	}
	port := parsed.Port()
	if port == "" {
		if strings.HasSuffix(authority, ":") {
			return "", false
		}
		switch strings.ToLower(scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", false
		}
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", false
	}
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	} else if strings.Contains(host, ":") {
		// A colon-bearing host must be a valid IPv6 literal, not an unbracketed
		// or malformed authority that could collapse under string splitting.
		return "", false
	} else {
		host = strings.ToLower(host)
	}
	return net.JoinHostPort(host, strconv.Itoa(portNumber)), true
}

var upgrader = ws.Upgrader{
	ReadBufferSize:    32768,
	WriteBufferSize:   32768,
	EnableCompression: true, // Negotiate permessage-deflate compression if the client supports it

	CheckOrigin: websocketOriginAllowed,
}

// WebSocketController handles WebSocket connections for real-time updates
type WebSocketController struct {
	BaseController
	hub *websocket.Hub
}

// NewWebSocketController creates a new WebSocket controller
func NewWebSocketController(hub *websocket.Hub) *WebSocketController {
	return &WebSocketController{
		hub: hub,
	}
}

// HandleWebSocket handles WebSocket connections
func (w *WebSocketController) HandleWebSocket(c *gin.Context) {
	// Check authentication. The user is captured rather than discarded: the socket
	// is tagged with their id so scoped payloads reach only them.
	user := session.GetLoginUser(c)
	clientIP := getRemoteIp(c)
	if user == nil {
		logger.Warningf("Unauthorized WebSocket connection attempt from %s", clientIP)
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	// Upgrade connection to WebSocket
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		logger.Error("Failed to upgrade WebSocket connection:", err)
		return
	}

	// Create client
	clientID := uuid.New().String()
	client := &websocket.Client{
		ID:     clientID,
		UserId: user.Id,
		Hub:    w.hub,
		Send:   make(chan []byte, 512), // Increased from 256 to 512 to prevent overflow
		Topics: make(map[websocket.MessageType]bool),
	}

	// Register client
	w.hub.Register(client)
	logger.Debugf("WebSocket client %s registered from %s", clientID, clientIP)

	// Start goroutines for reading and writing
	go w.writePump(client, conn)
	go w.readPump(client, conn)
}

// readPump pumps messages from the WebSocket connection to the hub
func (w *WebSocketController) readPump(client *websocket.Client, conn *ws.Conn) {
	defer func() {
		if r := common.Recover("WebSocket readPump panic"); r != nil {
			logger.Error("WebSocket readPump panic recovered:", r)
		}
		w.hub.Unregister(client)
		conn.Close()
	}()

	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	conn.SetReadLimit(maxMessageSize)

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if ws.IsUnexpectedCloseError(err, ws.CloseGoingAway, ws.CloseAbnormalClosure) {
				logger.Debugf("WebSocket read error for client %s: %v", client.ID, err)
			}
			break
		}

		// Validate message size
		if len(message) > maxMessageSize {
			logger.Warningf("WebSocket message from client %s exceeds max size: %d bytes", client.ID, len(message))
			continue
		}

		// Handle incoming messages (e.g., subscription requests)
		// For now, we'll just log them
		logger.Debugf("Received WebSocket message from client %s: %s", client.ID, string(message))
	}
}

// writePump pumps messages from the hub to the WebSocket connection
func (w *WebSocketController) writePump(client *websocket.Client, conn *ws.Conn) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		if r := common.Recover("WebSocket writePump panic"); r != nil {
			logger.Error("WebSocket writePump panic recovered:", r)
		}
		ticker.Stop()
		conn.Close()
	}()

	for {
		select {
		case message, ok := <-client.Send:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				// Hub closed the channel
				conn.WriteMessage(ws.CloseMessage, []byte{})
				return
			}

			// Send each message individually (no batching)
			// This ensures each JSON message is sent separately and can be parsed correctly
			if err := conn.WriteMessage(ws.TextMessage, message); err != nil {
				logger.Debugf("WebSocket write error for client %s: %v", client.ID, err)
				return
			}

		case <-ticker.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteMessage(ws.PingMessage, nil); err != nil {
				logger.Debugf("WebSocket ping error for client %s: %v", client.ID, err)
				return
			}
		}
	}
}
