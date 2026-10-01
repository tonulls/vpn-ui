package service

import (
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/web/proxyip"
)

// applyTrustedXrayProxySettings tells Xray's HTTP-based inbound transports which
// trusted proxy hops may supply forwarded client addresses. Other transports use
// their socket peer address directly and are left unchanged.
func applyTrustedXrayProxySettings(stream map[string]any, trustedProxyCIDRs string) error {
	network, _ := stream["network"].(string)
	switch strings.ToLower(network) {
	case "xhttp", "splithttp", "ws", "websocket", "httpupgrade", "grpc":
	default:
		return nil
	}

	normalized, err := proxyip.NormalizeTrustedProxies(trustedProxyCIDRs)
	if err != nil {
		return fmt.Errorf("invalid trusted Xray proxy list: %w", err)
	}
	if normalized == "" {
		normalized = proxyip.DefaultTrustedProxyCIDRs
	}

	sockopt, ok := stream["sockopt"].(map[string]any)
	if !ok {
		if existing, exists := stream["sockopt"]; exists && existing != nil {
			return fmt.Errorf("streamSettings.sockopt must be a JSON object")
		}
		sockopt = make(map[string]any)
	}

	headers := make([]string, 0)
	switch existing := sockopt["trustedXForwardedFor"].(type) {
	case []any:
		for _, value := range existing {
			header, ok := value.(string)
			if !ok {
				return fmt.Errorf("streamSettings.sockopt.trustedXForwardedFor must contain strings")
			}
			if strings.TrimSpace(header) != "" {
				headers = append(headers, strings.TrimSpace(header))
			}
		}
	case []string:
		headers = append(headers, existing...)
	case nil:
	default:
		return fmt.Errorf("streamSettings.sockopt.trustedXForwardedFor must be an array of strings")
	}
	if !hasHeader(headers, "X-Forwarded-For") {
		headers = append(headers, "X-Forwarded-For")
	}
	if !hasHeader(headers, "X-Real-IP") {
		headers = append(headers, "X-Real-IP")
	}
	if !hasHeader(headers, "Forwarded") {
		headers = append(headers, "Forwarded")
	}

	var headerValues []any
	for _, header := range headers {
		headerValues = append(headerValues, header)
	}
	proxyCIDRs := strings.Split(normalized, ", ")
	switch existing := sockopt["trustedProxyCIDRs"].(type) {
	case []any:
		for _, value := range existing {
			cidr, ok := value.(string)
			if !ok {
				return fmt.Errorf("streamSettings.sockopt.trustedProxyCIDRs must contain strings")
			}
			proxyCIDRs = append(proxyCIDRs, cidr)
		}
	case []string:
		proxyCIDRs = append(proxyCIDRs, existing...)
	case nil:
	default:
		return fmt.Errorf("streamSettings.sockopt.trustedProxyCIDRs must be an array of strings")
	}
	normalized, err = proxyip.NormalizeTrustedProxies(strings.Join(proxyCIDRs, ","))
	if err != nil {
		return fmt.Errorf("invalid trusted Xray proxy list: %w", err)
	}
	var proxyValues []any
	for _, cidr := range strings.Split(normalized, ", ") {
		proxyValues = append(proxyValues, cidr)
	}
	sockopt["trustedXForwardedFor"] = headerValues
	sockopt["trustedProxyCIDRs"] = proxyValues
	stream["sockopt"] = sockopt
	return nil
}

func hasHeader(headers []string, want string) bool {
	for _, header := range headers {
		if strings.EqualFold(header, want) {
			return true
		}
	}
	return false
}
