package entity

import "testing"

func TestAllSettingNormalizesTrustedReverseProxies(t *testing.T) {
	settings := AllSetting{
		WebPort:            2083,
		WebTrustedProxies:  "127.0.0.1, 10.0.0.0/8; ::1/128",
		XrayTrustedProxies: "192.0.2.1, 192.0.2.0/24",
		SubPort:            2097,
		TimeLocation:       "UTC",
	}
	if err := settings.CheckValid(); err != nil {
		t.Fatalf("CheckValid: %v", err)
	}
	if want := "127.0.0.1/32, 10.0.0.0/8, ::1/128"; settings.WebTrustedProxies != want {
		t.Fatalf("trusted proxies = %q, want %q", settings.WebTrustedProxies, want)
	}
	if want := "192.0.2.1/32, 192.0.2.0/24"; settings.XrayTrustedProxies != want {
		t.Fatalf("Xray trusted proxies = %q, want %q", settings.XrayTrustedProxies, want)
	}
}

func TestAllSettingRejectsInvalidTrustedReverseProxy(t *testing.T) {
	settings := AllSetting{
		WebPort:           2083,
		WebTrustedProxies: "127.0.0.1, not-a-proxy",
		SubPort:           2097,
		TimeLocation:      "UTC",
	}
	if err := settings.CheckValid(); err == nil {
		t.Fatal("invalid trusted proxy entry was accepted")
	}
}
