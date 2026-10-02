package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/util/json_util"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

const (
	telegramBotXrayInboundTag = "telegram-bot-socks"
	telegramBotXrayPort       = 19654
	telegramBotXrayProxyURL   = "socks5://127.0.0.1:19654"
)

// TelegramBotXrayInboundTag is the single loopback-only inbound exposed by the
// Telegram routing controls.
func TelegramBotXrayInboundTag() string { return telegramBotXrayInboundTag }

type telegramBotXraySettings struct {
	Enabled    bool
	InboundTag string
	Outbound   string
	Balancer   string
}

// applyTelegramBotXrayRouting synthesizes the private loopback SOCKS inbound and
// its single routing rule. The operator's template is never edited in place.
func applyTelegramBotXrayRouting(config *xray.Config, settings telegramBotXraySettings) error {
	if config == nil {
		return errors.New("missing Xray config")
	}
	if !settings.Enabled {
		return nil
	}
	if settings.InboundTag != telegramBotXrayInboundTag {
		return fmt.Errorf("unsupported Telegram Xray inboundTag %q", settings.InboundTag)
	}
	if (strings.TrimSpace(settings.Outbound) == "") == (strings.TrimSpace(settings.Balancer) == "") {
		return errors.New("select exactly one Telegram Xray outboundTag or balancerTag")
	}

	var outbounds []map[string]any
	if err := json.Unmarshal(config.OutboundConfigs, &outbounds); err != nil {
		return fmt.Errorf("parse Xray outbounds: %w", err)
	}
	outboundExists := false
	for _, outbound := range outbounds {
		if tag, _ := outbound["tag"].(string); tag == settings.Outbound && tag != "" {
			outboundExists = true
		}
	}
	if settings.Outbound != "" && !outboundExists {
		return fmt.Errorf("Telegram Xray outboundTag %q does not exist", settings.Outbound)
	}

	var routing map[string]any
	if len(config.RouterConfig) == 0 {
		routing = map[string]any{}
	} else if err := json.Unmarshal(config.RouterConfig, &routing); err != nil {
		return fmt.Errorf("parse Xray routing: %w", err)
	}
	balancerExists := false
	if raw, ok := routing["balancers"].([]any); ok {
		for _, item := range raw {
			balancer, _ := item.(map[string]any)
			if balancer != nil {
				if tag, _ := balancer["tag"].(string); tag == settings.Balancer && tag != "" {
					balancerExists = true
				}
			}
		}
	}
	if settings.Balancer != "" && !balancerExists {
		return fmt.Errorf("Telegram Xray balancerTag %q does not exist", settings.Balancer)
	}

	for _, inbound := range config.InboundConfigs {
		if inbound.Tag == telegramBotXrayInboundTag {
			return fmt.Errorf("Telegram Xray inbound tag %q conflicts with an existing inbound", telegramBotXrayInboundTag)
		}
		if inbound.Port == telegramBotXrayPort {
			return fmt.Errorf("Telegram Xray port %d conflicts with inbound %q", telegramBotXrayPort, inbound.Tag)
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
		if tag, _ := rule["inboundTag"].(string); tag == telegramBotXrayInboundTag {
			return fmt.Errorf("Telegram Xray inboundTag %q is already referenced by an operator routing rule", telegramBotXrayInboundTag)
		}
		if tags, _ := rule["inboundTag"].([]any); tags != nil {
			for _, item := range tags {
				if item == telegramBotXrayInboundTag {
					return fmt.Errorf("Telegram Xray inboundTag %q is already referenced by an operator routing rule", telegramBotXrayInboundTag)
				}
			}
		}
	}

	newRule := map[string]any{"type": "field", "inboundTag": []any{telegramBotXrayInboundTag}}
	if settings.Outbound != "" {
		newRule["outboundTag"] = settings.Outbound
	} else {
		newRule["balancerTag"] = settings.Balancer
	}
	// First-match routing: insert at the front so catch-all rules cannot swallow
	// the bot. The rule only matches the private source tag, so other inbounds
	// retain their existing routing result.
	routing["rules"] = append([]any{newRule}, rules...)
	encodedRouting, err := json.Marshal(routing)
	if err != nil {
		return fmt.Errorf("encode Xray routing: %w", err)
	}
	config.RouterConfig = encodedRouting

	config.InboundConfigs = append(config.InboundConfigs, xray.InboundConfig{
		Listen:   json_util.RawMessage(`"127.0.0.1"`),
		Port:     telegramBotXrayPort,
		Protocol: "socks",
		Settings: json_util.RawMessage(`{"auth":"noauth","udp":false}`),
		Tag:      telegramBotXrayInboundTag,
	})
	return nil
}
