// Package proxyip resolves the original client address from direct and proxied HTTP
// connections. Forwarding headers are considered only when the TCP peer is trusted.
package proxyip

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
)

const DefaultTrustedProxyCIDRs = "127.0.0.1/32, ::1/128"

// NormalizeTrustedProxies validates a comma-, semicolon-, or whitespace-separated list
// of IP addresses and CIDRs and returns its canonical comma-separated representation.
func NormalizeTrustedProxies(value string) (string, error) {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	canonical := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		if ip := net.ParseIP(part); ip != nil {
			if v4 := ip.To4(); v4 != nil {
				part = v4.String() + "/32"
			} else {
				part = ip.String() + "/128"
			}
		} else {
			_, network, err := net.ParseCIDR(part)
			if err != nil {
				return "", fmt.Errorf("invalid trusted proxy address or CIDR %q", part)
			}
			part = network.String()
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		canonical = append(canonical, part)
	}
	return strings.Join(canonical, ", "), nil
}

// Resolve returns the client IP for a request. Untrusted peers cannot influence the
// result through Forwarded, X-Forwarded-For, or X-Real-IP headers.
func Resolve(remoteAddr string, headers http.Header, trustedProxyCIDRs string) string {
	peer := parseIP(remoteAddr)
	if peer == nil {
		return remoteHost(remoteAddr)
	}

	trusted, err := parseNetworks(trustedProxyCIDRs)
	if err != nil || !contains(trusted, peer) {
		return peer.String()
	}

	if client := resolveForwardedChain(peer, xForwardedFor(headers), trusted); client != nil {
		return client.String()
	}
	if client := resolveForwardedChain(peer, forwardedFor(headers), trusted); client != nil {
		return client.String()
	}
	if client := parseIP(headers.Get("X-Real-IP")); client != nil {
		return client.String()
	}
	return peer.String()
}

func parseNetworks(value string) ([]*net.IPNet, error) {
	normalized, err := NormalizeTrustedProxies(value)
	if err != nil {
		return nil, err
	}
	if normalized == "" {
		return nil, nil
	}
	parts := strings.Split(normalized, ", ")
	networks := make([]*net.IPNet, 0, len(parts))
	for _, part := range parts {
		_, network, err := net.ParseCIDR(part)
		if err != nil {
			return nil, err
		}
		networks = append(networks, network)
	}
	return networks, nil
}

func contains(networks []*net.IPNet, ip net.IP) bool {
	for _, network := range networks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func resolveForwardedChain(peer net.IP, chain []string, trusted []*net.IPNet) net.IP {
	if len(chain) == 0 {
		return nil
	}
	current := peer
	for i := len(chain) - 1; i >= 0; i-- {
		if !contains(trusted, current) {
			return current
		}
		next := parseIP(chain[i])
		if next == nil {
			return nil
		}
		current = next
	}
	return current
}

func xForwardedFor(headers http.Header) []string {
	var chain []string
	for _, value := range headers.Values("X-Forwarded-For") {
		for _, part := range strings.Split(value, ",") {
			if value := strings.TrimSpace(part); value != "" {
				chain = append(chain, value)
			}
		}
	}
	return chain
}

func forwardedFor(headers http.Header) []string {
	var chain []string
	for _, value := range headers.Values("Forwarded") {
		for _, element := range strings.Split(value, ",") {
			for _, parameter := range strings.Split(element, ";") {
				key, value, found := strings.Cut(strings.TrimSpace(parameter), "=")
				if found && strings.EqualFold(strings.TrimSpace(key), "for") {
					if value = unquote(strings.TrimSpace(value)); value != "" && !strings.EqualFold(value, "unknown") && !strings.HasPrefix(value, "_") {
						chain = append(chain, value)
					}
					break
				}
			}
		}
	}
	return chain
}

func unquote(value string) string {
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	}
	return value
}

func parseIP(value string) net.IP {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.TrimPrefix(strings.TrimSuffix(value, "]"), "[")
	return net.ParseIP(value)
}

func remoteHost(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}
