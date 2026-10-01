package proxyip

import (
	"net/http"
	"testing"
)

func TestResolveUsesRemoteAddressWithoutTrustedProxy(t *testing.T) {
	headers := http.Header{"X-Forwarded-For": []string{"198.51.100.99"}, "X-Real-Ip": []string{"198.51.100.99"}}
	if got := Resolve("203.0.113.7:42100", headers, ""); got != "203.0.113.7" {
		t.Fatalf("Resolve() = %q, want direct peer IP", got)
	}
}

func TestResolveUsesForwardedHeadersOnlyFromTrustedPeer(t *testing.T) {
	headers := http.Header{"X-Forwarded-For": []string{"198.51.100.7"}}
	if got := Resolve("127.0.0.1:8080", headers, "127.0.0.1/32"); got != "198.51.100.7" {
		t.Fatalf("Resolve() = %q, want forwarded client IP", got)
	}
}

func TestResolveWalksTrustedProxyChainFromRightToLeft(t *testing.T) {
	headers := http.Header{"X-Forwarded-For": []string{"198.51.100.7, 10.0.0.2"}}
	trusted := "127.0.0.1/32, 10.0.0.0/8"
	if got := Resolve("127.0.0.1:8080", headers, trusted); got != "198.51.100.7" {
		t.Fatalf("Resolve() = %q, want first untrusted address", got)
	}
}

func TestResolveSupportsRFCForwardedAndXRealIP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers http.Header
		want    string
	}{
		{
			name:    "Forwarded IPv4 with port",
			headers: http.Header{"Forwarded": []string{"for=198.51.100.8:1234;proto=https"}},
			want:    "198.51.100.8",
		},
		{
			name:    "Forwarded quoted IPv6",
			headers: http.Header{"Forwarded": []string{"for=\"[2001:db8::8]:1234\";proto=https"}},
			want:    "2001:db8::8",
		},
		{
			name:    "X-Real-IP fallback",
			headers: http.Header{"X-Real-Ip": []string{"198.51.100.9"}},
			want:    "198.51.100.9",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Resolve("[::1]:8080", tc.headers, "::1/128"); got != tc.want {
				t.Fatalf("Resolve() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestResolveIgnoresMalformedForwardedValue(t *testing.T) {
	headers := http.Header{"X-Forwarded-For": []string{"not-an-ip"}}
	if got := Resolve("127.0.0.1:8080", headers, "127.0.0.1/32"); got != "127.0.0.1" {
		t.Fatalf("Resolve() = %q, want trusted peer fallback", got)
	}
}

func TestNormalizeTrustedProxies(t *testing.T) {
	got, err := NormalizeTrustedProxies("127.0.0.1, ::1/128; 10.0.0.0/8, 127.0.0.1/32")
	if err != nil {
		t.Fatalf("NormalizeTrustedProxies: %v", err)
	}
	want := "127.0.0.1/32, ::1/128, 10.0.0.0/8"
	if got != want {
		t.Fatalf("NormalizeTrustedProxies() = %q, want %q", got, want)
	}
	if _, err := NormalizeTrustedProxies("not-an-ip"); err == nil {
		t.Fatal("expected invalid trusted proxy to be rejected")
	}
}
