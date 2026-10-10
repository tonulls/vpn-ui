package service

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	maxExternalSubscriptionBytes = 5 << 20
	maxExternalSubscriptionLine  = 32 << 10
	maxExternalSubscriptionItems = 5000
)

// ExternalSubscriptionCandidate is stored inside a secret-bearing JSON column. Do not
// serialize this type directly to an API response: URI contains a third-party credential.
type ExternalSubscriptionCandidate struct {
	URI         string `json:"uri"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name"`
	Flag        string `json:"flag"`
}

type ExternalSubscriptionParseStats struct {
	Accepted    int
	Duplicates  int
	Rejected    int
	Unsupported int
}

// ExternalSubscriptionURIDedupKey identifies the credential/configuration represented
// by a VLESS URI while ignoring its display fragment. The returned value is a hash so
// callers can compare identities without retaining another credential-bearing string.
func ExternalSubscriptionURIDedupKey(rawURI string) string {
	identity := rawURI
	parsed, err := url.Parse(rawURI)
	if err == nil && parsed != nil {
		parsed.Scheme = strings.ToLower(parsed.Scheme)
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Fragment = ""
		parsed.RawFragment = ""
		parsed.RawQuery = parsed.Query().Encode()
		identity = parsed.String()
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

// ParseExternalVLESSURI validates the shape required by the VLESS subscription slot.
// It retains the original URI byte-for-byte after outer whitespace is trimmed, so
// emitted links do not lose source-specific parameters or their encoded Unicode name.
func ParseExternalVLESSURI(raw string) (ExternalSubscriptionCandidate, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxExternalSubscriptionLine || strings.ContainsAny(raw, "\r\n\x00") {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS URI")
	}
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "vless") || u.Opaque != "" || u.User == nil {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS URI")
	}
	if _, hasPassword := u.User.Password(); hasPassword {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS credentials")
	}
	id := u.User.Username()
	if _, err := uuid.Parse(id); err != nil {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS credentials")
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, " \t\r\n\x00") {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS endpoint")
	}
	portText := u.Port()
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS endpoint")
	}
	if !utf8.ValidString(u.Fragment) {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS name")
	}
	flag := firstCountryFlag(u.Fragment)
	if flag == "" {
		return ExternalSubscriptionCandidate{}, fmt.Errorf("country flag is missing")
	}
	if _, err := externalVLESSNetwork(u.Query()); err != nil {
		return ExternalSubscriptionCandidate{}, err
	}
	if _, err := externalVLESSSecurity(u.Query()); err != nil {
		return ExternalSubscriptionCandidate{}, err
	}
	if strings.EqualFold(u.Query().Get("type"), "xhttp") {
		if extra := u.Query().Get("extra"); extra != "" && extra != "null" {
			var value map[string]any
			if err := json.Unmarshal([]byte(extra), &value); err != nil || value == nil {
				return ExternalSubscriptionCandidate{}, fmt.Errorf("invalid VLESS transport settings")
			}
		}
	}
	sum := sha256.Sum256([]byte(raw))
	return ExternalSubscriptionCandidate{
		URI:         raw,
		Fingerprint: hex.EncodeToString(sum[:]),
		Name:        u.Fragment,
		Flag:        flag,
	}, nil
}

// ParseExternalVLESSList reads a line-oriented source. Unknown URI schemes and invalid
// entries are counted but never copied into returned errors or logs. Exact URI duplicates
// are removed; different transport/security variants remain distinct.
func ParseExternalVLESSList(body []byte) ([]ExternalSubscriptionCandidate, ExternalSubscriptionParseStats, error) {
	if len(body) > maxExternalSubscriptionBytes {
		return nil, ExternalSubscriptionParseStats{}, fmt.Errorf("source exceeds the size limit")
	}
	stats := ExternalSubscriptionParseStats{}
	seen := make(map[string]struct{})
	result := make([]ExternalSubscriptionCandidate, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	scanner.Buffer(make([]byte, 1024), maxExternalSubscriptionLine)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(result)+stats.Rejected+stats.Unsupported >= maxExternalSubscriptionItems {
			return nil, stats, fmt.Errorf("source contains too many entries")
		}
		if !strings.HasPrefix(strings.ToLower(line), "vless://") {
			stats.Unsupported++
			continue
		}
		candidate, err := ParseExternalVLESSURI(line)
		if err != nil {
			stats.Rejected++
			continue
		}
		if _, duplicate := seen[candidate.Fingerprint]; duplicate {
			stats.Duplicates++
			continue
		}
		seen[candidate.Fingerprint] = struct{}{}
		result = append(result, candidate)
	}
	if err := scanner.Err(); err != nil {
		return nil, stats, fmt.Errorf("source contains an invalid or oversized line")
	}
	stats.Accepted = len(result)
	return result, stats, nil
}

func firstCountryFlag(name string) string {
	runes := []rune(name)
	for i := 0; i+1 < len(runes); i++ {
		if isRegionalIndicator(runes[i]) && isRegionalIndicator(runes[i+1]) {
			return string(runes[i : i+2])
		}
	}
	return ""
}

func isRegionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

func externalVLESSNetwork(query url.Values) (string, error) {
	network := strings.ToLower(strings.TrimSpace(query.Get("type")))
	if network == "" {
		network = "tcp"
	}
	switch network {
	case "tcp", "raw", "ws", "grpc", "httpupgrade", "xhttp":
		return network, nil
	default:
		return "", fmt.Errorf("unsupported VLESS transport")
	}
}

func externalVLESSSecurity(query url.Values) (string, error) {
	security := strings.ToLower(strings.TrimSpace(query.Get("security")))
	if security == "" {
		security = "none"
	}
	switch security {
	case "none", "tls", "reality":
	default:
		return "", fmt.Errorf("unsupported VLESS security")
	}
	if security == "reality" && (query.Get("pbk") == "" || query.Get("sni") == "") {
		return "", fmt.Errorf("incomplete VLESS reality settings")
	}
	return security, nil
}

// ExternalVLESSOutboundJSON converts one validated URI to the outbound shape consumed by
// the pinned Xray binary. probeAddress is an already-validated public IP, avoiding a
// second hostname lookup (and DNS-rebinding SSRF) inside the temporary probe process.
func ExternalVLESSOutboundJSON(candidate ExternalSubscriptionCandidate, probeAddress, tag string) ([]byte, error) {
	u, err := url.Parse(candidate.URI)
	if err != nil || !strings.EqualFold(u.Scheme, "vless") {
		return nil, fmt.Errorf("invalid VLESS URI")
	}
	query := u.Query()
	id := u.User.Username()
	if _, err := uuid.Parse(id); err != nil {
		return nil, fmt.Errorf("invalid VLESS credentials")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid VLESS endpoint")
	}
	if probeAddress == "" {
		probeAddress = u.Hostname()
	}
	if strings.ContainsAny(probeAddress, " \t\r\n\x00") {
		return nil, fmt.Errorf("invalid VLESS endpoint")
	}
	network, err := externalVLESSNetwork(query)
	if err != nil {
		return nil, err
	}
	security, err := externalVLESSSecurity(query)
	if err != nil {
		return nil, err
	}
	encryption := query.Get("encryption")
	if encryption == "" {
		encryption = "none"
	}
	if encryption != "none" {
		return nil, fmt.Errorf("unsupported VLESS encryption")
	}

	stream := map[string]any{"network": network, "security": security}
	switch network {
	case "ws":
		settings := map[string]any{"path": query.Get("path")}
		if host := query.Get("host"); host != "" {
			settings["headers"] = map[string]string{"Host": host}
		}
		stream["wsSettings"] = settings
	case "grpc":
		settings := map[string]any{"serviceName": query.Get("serviceName")}
		if authority := query.Get("authority"); authority != "" {
			settings["authority"] = authority
		}
		if strings.EqualFold(query.Get("mode"), "multi") {
			settings["multiMode"] = true
		}
		stream["grpcSettings"] = settings
	case "httpupgrade":
		settings := map[string]any{"path": query.Get("path")}
		if host := query.Get("host"); host != "" {
			settings["host"] = host
		}
		stream["httpupgradeSettings"] = settings
	case "xhttp":
		settings := map[string]any{"path": query.Get("path")}
		if host := query.Get("host"); host != "" {
			settings["host"] = host
		}
		if mode := query.Get("mode"); mode != "" {
			settings["mode"] = mode
		}
		if extra := query.Get("extra"); extra != "" && extra != "null" {
			var value map[string]any
			if err := json.Unmarshal([]byte(extra), &value); err != nil || value == nil {
				return nil, fmt.Errorf("invalid VLESS transport settings")
			}
			settings["extra"] = value
		}
		stream["xhttpSettings"] = settings
	case "tcp", "raw":
		headerType := strings.ToLower(strings.TrimSpace(query.Get("headerType")))
		if headerType == "http" {
			hosts := splitNonEmpty(query.Get("host"), ",")
			paths := splitNonEmpty(query.Get("path"), ",")
			request := map[string]any{"method": "GET", "path": paths}
			if len(hosts) > 0 {
				request["headers"] = map[string]any{"Host": hosts}
			}
			stream["tcpSettings"] = map[string]any{
				"header": map[string]any{"type": "http", "request": request},
			}
		} else if headerType != "" && headerType != "none" {
			return nil, fmt.Errorf("unsupported VLESS TCP header")
		}
	}

	if security == "tls" {
		tls := map[string]any{}
		serverName := query.Get("sni")
		if serverName == "" {
			serverName = u.Hostname()
		}
		if serverName != "" {
			tls["serverName"] = serverName
		}
		if fingerprint := query.Get("fp"); fingerprint != "" {
			tls["fingerprint"] = fingerprint
		}
		if alpn := splitNonEmpty(query.Get("alpn"), ","); len(alpn) > 0 {
			tls["alpn"] = alpn
		}
		if insecure := query.Get("insecure"); insecure == "1" || strings.EqualFold(insecure, "true") {
			tls["allowInsecure"] = true
		}
		if ech := query.Get("ech"); ech != "" {
			tls["echConfigList"] = ech
		}
		stream["tlsSettings"] = tls
	} else if security == "reality" {
		reality := map[string]any{
			"serverName":  query.Get("sni"),
			"fingerprint": query.Get("fp"),
			"publicKey":   query.Get("pbk"),
			"shortId":     query.Get("sid"),
			"spiderX":     query.Get("spx"),
			"show":        false,
		}
		if pqv := query.Get("pqv"); pqv != "" {
			reality["mldsa65Verify"] = pqv
		}
		stream["realitySettings"] = reality
	}

	user := map[string]any{"id": id, "encryption": encryption, "level": 8}
	if flow := query.Get("flow"); flow != "" {
		user["flow"] = flow
	}
	outbound := map[string]any{
		"protocol": "vless",
		"tag":      tag,
		"settings": map[string]any{
			"vnext": []any{map[string]any{
				"address": probeAddress,
				"port":    port,
				"users":   []any{user},
			}},
		},
		"streamSettings": stream,
	}
	return json.Marshal(outbound)
}

func splitNonEmpty(value, separator string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, separator)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// externalPublicIP rejects addresses that would let an imported URI probe the panel,
// private LANs, link-local services, or documentation/special-use ranges.
func externalPublicIP(raw string) bool {
	ip := net.ParseIP(raw)
	if ip == nil {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{
		"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32",
	} {
		block, err := netip.ParsePrefix(cidr)
		if err == nil && block.Contains(addr) {
			return false
		}
	}
	return true
}
