package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

const (
	geofileXrayInboundTag = "geofile-download-socks"
	geofileXrayPort       = 19655
	geofileXrayProxyURL   = "socks5://127.0.0.1:19655"
)

type GeofileXrayRoutingSettings struct {
	Enabled    bool   `json:"enabled"`
	InboundTag string `json:"inboundTag"`
	Outbound   string `json:"outboundTag"`
	Balancer   string `json:"balancerTag"`
}

type GeofileXrayRoutingOptions struct {
	InboundTags  []string `json:"inboundTags"`
	OutboundTags []string `json:"outboundTags"`
	BalancerTags []string `json:"balancerTags"`
}

func (s *SettingService) ValidateGeofileXrayRouting(settings GeofileXrayRoutingSettings) error {
	if !settings.Enabled {
		return nil
	}
	template, err := s.GetXrayConfigTemplate()
	if err != nil {
		return err
	}
	var config xray.Config
	if err := json.Unmarshal([]byte(template), &config); err != nil {
		return fmt.Errorf("parse Xray template: %w", err)
	}
	return applyGeofileXrayRouting(&config, settings)
}

func applyGeofileXrayRouting(config *xray.Config, settings GeofileXrayRoutingSettings) error {
	if !settings.Enabled {
		return nil
	}
	if settings.InboundTag != geofileXrayInboundTag {
		return fmt.Errorf("unsupported Geo-file Xray inboundTag %q", settings.InboundTag)
	}
	return applyManagedSocksRoute(config, managedSocksRoute{
		InboundTag: settings.InboundTag,
		Port:       geofileXrayPort,
		Outbound:   settings.Outbound,
		Balancer:   settings.Balancer,
		Feature:    "Geo-file Xray routing",
	})
}

func (s *SettingService) GetGeofileXrayRoutingOptions() (GeofileXrayRoutingOptions, error) {
	config, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		return GeofileXrayRoutingOptions{}, err
	}
	return geofileXrayRouteOptionsFromConfig(config)
}

func geofileXrayRouteOptions(template string) (GeofileXrayRoutingOptions, error) {
	var config xray.Config
	if err := json.Unmarshal([]byte(template), &config); err != nil {
		return GeofileXrayRoutingOptions{}, fmt.Errorf("parse Xray template: %w", err)
	}
	return geofileXrayRouteOptionsFromConfig(&config)
}

func geofileXrayRouteOptionsFromConfig(config *xray.Config) (GeofileXrayRoutingOptions, error) {
	if config == nil {
		return GeofileXrayRoutingOptions{}, fmt.Errorf("missing Xray config")
	}
	options := GeofileXrayRoutingOptions{InboundTags: []string{geofileXrayInboundTag}}
	var outbounds []map[string]any
	if len(config.OutboundConfigs) > 0 {
		if err := json.Unmarshal(config.OutboundConfigs, &outbounds); err != nil {
			return options, fmt.Errorf("parse Xray outbounds: %w", err)
		}
	}
	for _, outbound := range outbounds {
		if tag, ok := outbound["tag"].(string); ok && tag != "" {
			options.OutboundTags = append(options.OutboundTags, tag)
		}
	}

	var routing struct {
		Balancers []struct {
			Tag string `json:"tag"`
		} `json:"balancers"`
	}
	if len(config.RouterConfig) > 0 {
		if err := json.Unmarshal(config.RouterConfig, &routing); err != nil {
			return options, fmt.Errorf("parse Xray balancers: %w", err)
		}
	}
	for _, balancer := range routing.Balancers {
		if balancer.Tag != "" {
			options.BalancerTags = append(options.BalancerTags, balancer.Tag)
		}
	}
	sort.Strings(options.OutboundTags)
	sort.Strings(options.BalancerTags)
	return options, nil
}

var (
	geofileProxyFallbackMu sync.Mutex
	geofileProxyFallbackAt time.Time
)

// geofileDownloadProxy routes built-in downloads through the private SOCKS
// inbound when enabled and ready. If Xray has not started that inbound yet, the
// panel must still be able to fetch a missing geo file before core startup; use
// the same environment-proxy behavior as before and emit a rate-limited warning.
func geofileDownloadProxy(req *http.Request) (*url.URL, error) {
	settings, err := (&SettingService{}).GetGeofileXrayRouting()
	if err != nil {
		logger.Warning("geofiles: could not read Xray routing setting; using the default download route:", err)
		return http.ProxyFromEnvironment(req)
	}
	if !settings.Enabled {
		return http.ProxyFromEnvironment(req)
	}
	if err := probeGeofileXraySocks(); err != nil {
		geofileProxyFallbackMu.Lock()
		if time.Since(geofileProxyFallbackAt) >= time.Minute {
			logger.Warning("geofiles: Xray SOCKS inbound is not ready; using the default download route for bootstrap:", err)
			geofileProxyFallbackAt = time.Now()
		}
		geofileProxyFallbackMu.Unlock()
		return http.ProxyFromEnvironment(req)
	}
	return url.Parse(geofileXrayProxyURL)
}

func probeGeofileXraySocks() error {
	conn, err := net.DialTimeout("tcp", "127.0.0.1:19655", 250*time.Millisecond)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(250 * time.Millisecond))
	if _, err := conn.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}
	if response[0] != 5 || response[1] != 0 {
		return fmt.Errorf("unexpected SOCKS5 greeting response %v", response)
	}
	return nil
}
