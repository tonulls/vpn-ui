package service

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

func TestGetXrayConfigInjectsTrustedProxyPolicyForXHTTP(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "xray-trusted-proxy.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	settings := &model.Setting{Key: "xrayTrustedProxies", Value: "127.0.0.1/32, 10.20.0.0/16"}
	if err := database.GetDB().Create(settings).Error; err != nil {
		t.Fatalf("create trusted proxy setting: %v", err)
	}
	inbound := &model.Inbound{
		UserId:         1,
		Enable:         true,
		Port:           8443,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[]}`,
		StreamSettings: `{"network":"xhttp","security":"none"}`,
		Tag:            "proxy-aware-xhttp",
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatalf("create XHTTP inbound: %v", err)
	}

	config, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	var stream map[string]any
	for _, configured := range config.InboundConfigs {
		if configured.Tag != inbound.Tag {
			continue
		}
		if err := json.Unmarshal(configured.StreamSettings, &stream); err != nil {
			t.Fatalf("decode generated streamSettings: %v", err)
		}
		break
	}
	if stream == nil {
		t.Fatal("XHTTP inbound was not included in generated configuration")
	}
	sockopt, ok := stream["sockopt"].(map[string]any)
	if !ok {
		t.Fatalf("generated sockopt = %#v, want object", stream["sockopt"])
	}
	if got, want := sockopt["trustedProxyCIDRs"], []any{"127.0.0.1/32", "10.20.0.0/16"}; !equalJSONValues(got, want) {
		t.Fatalf("trustedProxyCIDRs = %#v, want %#v", got, want)
	}
	if got, want := sockopt["trustedXForwardedFor"], []any{"X-Forwarded-For", "X-Real-IP", "Forwarded"}; !equalJSONValues(got, want) {
		t.Fatalf("trustedXForwardedFor = %#v, want %#v", got, want)
	}
}

func equalJSONValues(a, b any) bool {
	encodedA, errA := json.Marshal(a)
	encodedB, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(encodedA) == string(encodedB)
}
