package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/util/json_util"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

type managedSocksRoute struct {
	InboundTag string
	Port       int
	Outbound   string
	Balancer   string
	Feature    string
}

// applyManagedSocksRoute adds one private loopback SOCKS inbound and a single
// routing rule owned by a panel feature. It never rewrites operator rules.
func applyManagedSocksRoute(config *xray.Config, route managedSocksRoute) error {
	if config == nil {
		return errors.New("missing Xray config")
	}
	if route.InboundTag == "" || route.Port < 1 || route.Port > 65535 {
		return fmt.Errorf("invalid %s managed SOCKS inbound", route.Feature)
	}
	if (strings.TrimSpace(route.Outbound) == "") == (strings.TrimSpace(route.Balancer) == "") {
		return fmt.Errorf("select exactly one %s outboundTag or balancerTag", route.Feature)
	}

	var outbounds []map[string]any
	if err := json.Unmarshal(config.OutboundConfigs, &outbounds); err != nil {
		return fmt.Errorf("parse Xray outbounds for %s: %w", route.Feature, err)
	}
	outboundExists := false
	for _, outbound := range outbounds {
		if tag, _ := outbound["tag"].(string); tag == route.Outbound && tag != "" {
			outboundExists = true
		}
	}
	if route.Outbound != "" && !outboundExists {
		return fmt.Errorf("%s outboundTag %q does not exist", route.Feature, route.Outbound)
	}

	var routing map[string]any
	if len(config.RouterConfig) == 0 {
		routing = map[string]any{}
	} else if err := json.Unmarshal(config.RouterConfig, &routing); err != nil {
		return fmt.Errorf("parse Xray routing for %s: %w", route.Feature, err)
	}
	balancerExists := false
	if raw, ok := routing["balancers"].([]any); ok {
		for _, item := range raw {
			balancer, _ := item.(map[string]any)
			if balancer != nil {
				if tag, _ := balancer["tag"].(string); tag == route.Balancer && tag != "" {
					balancerExists = true
				}
			}
		}
	}
	if route.Balancer != "" && !balancerExists {
		return fmt.Errorf("%s balancerTag %q does not exist", route.Feature, route.Balancer)
	}

	for _, inbound := range config.InboundConfigs {
		if inbound.Tag == route.InboundTag {
			return fmt.Errorf("%s inboundTag %q conflicts with an existing inbound", route.Feature, route.InboundTag)
		}
		if inbound.Port == route.Port {
			return fmt.Errorf("%s SOCKS port %d conflicts with inbound %q", route.Feature, route.Port, inbound.Tag)
		}
	}

	var rules []any
	if raw, ok := routing["rules"].([]any); ok {
		rules = raw
	}
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		if rule == nil {
			continue
		}
		if tag, _ := rule["inboundTag"].(string); tag == route.InboundTag {
			return fmt.Errorf("%s inboundTag %q is already referenced by an operator rule", route.Feature, route.InboundTag)
		}
		if tags, _ := rule["inboundTag"].([]any); tags != nil {
			for _, item := range tags {
				if item == route.InboundTag {
					return fmt.Errorf("%s inboundTag %q is already referenced by an operator rule", route.Feature, route.InboundTag)
				}
			}
		}
	}

	rule := map[string]any{"type": "field", "inboundTag": []any{route.InboundTag}}
	if route.Outbound != "" {
		rule["outboundTag"] = route.Outbound
	} else {
		rule["balancerTag"] = route.Balancer
	}
	// Put the feature rule ahead of operator catch-alls. Its single inboundTag
	// predicate leaves all unrelated traffic on the original rule chain.
	routing["rules"] = append([]any{rule}, rules...)
	encodedRouting, err := json.Marshal(routing)
	if err != nil {
		return fmt.Errorf("encode Xray routing for %s: %w", route.Feature, err)
	}
	config.RouterConfig = encodedRouting
	config.InboundConfigs = append(config.InboundConfigs, xray.InboundConfig{
		Listen:   json_util.RawMessage(`"127.0.0.1"`),
		Port:     route.Port,
		Protocol: "socks",
		Settings: json_util.RawMessage(`{"auth":"noauth","udp":false}`),
		Tag:      route.InboundTag,
	})
	return nil
}
