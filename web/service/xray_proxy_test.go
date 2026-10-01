package service

import (
	"reflect"
	"testing"
)

func TestApplyTrustedXrayProxySettingsForHTTPTransports(t *testing.T) {
	for _, network := range []string{"xhttp", "splithttp", "ws", "websocket", "httpupgrade", "grpc"} {
		t.Run(network, func(t *testing.T) {
			stream := map[string]any{
				"network": network,
				"sockopt": map[string]any{"trustedXForwardedFor": []any{"X-Custom-Forwarded-For"}},
			}
			if err := applyTrustedXrayProxySettings(stream, "127.0.0.1, 10.20.0.0/16"); err != nil {
				t.Fatalf("applyTrustedXrayProxySettings: %v", err)
			}
			sockopt := stream["sockopt"].(map[string]any)
			if got, want := sockopt["trustedProxyCIDRs"], []any{"127.0.0.1/32", "10.20.0.0/16"}; !reflect.DeepEqual(got, want) {
				t.Errorf("trustedProxyCIDRs = %#v, want %#v", got, want)
			}
			if got, want := sockopt["trustedXForwardedFor"], []any{"X-Custom-Forwarded-For", "X-Forwarded-For", "X-Real-IP", "Forwarded"}; !reflect.DeepEqual(got, want) {
				t.Errorf("trustedXForwardedFor = %#v, want %#v", got, want)
			}
		})
	}
}

func TestApplyTrustedXrayProxySettingsLeavesDirectTransportsAlone(t *testing.T) {
	stream := map[string]any{"network": "tcp"}
	if err := applyTrustedXrayProxySettings(stream, "127.0.0.1/32"); err != nil {
		t.Fatalf("applyTrustedXrayProxySettings: %v", err)
	}
	if _, ok := stream["sockopt"]; ok {
		t.Fatalf("direct TCP transport unexpectedly changed: %#v", stream)
	}
}
