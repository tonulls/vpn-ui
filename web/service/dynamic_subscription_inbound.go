package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"gorm.io/gorm"
)

// CreateInbound creates a logical connection for one external subscription slot. The
// row exists only to reuse the panel's account membership and inbound-access rules;
// it has no listener, port, Xray inbound, or daemon lifecycle.
func (s *DynamicSubscriptionService) CreateInbound(userID, slotID int, remark string) (*model.Inbound, error) {
	if !externalSelectorInstalled() {
		return nil, errors.New("сначала установите модуль External Selector в /panel/core")
	}
	remark = strings.TrimSpace(remark)
	if userID < 1 || slotID < 1 || remark == "" || len([]rune(remark)) > 128 || strings.ContainsAny(remark, "\r\n\x00") {
		return nil, errors.New("нужно указать имя подключения и существующий подмодуль")
	}
	var slot model.ExternalSubscriptionSlot
	if err := database.GetDB().First(&slot, slotID).Error; err != nil {
		return nil, errors.New("подмодуль не найден")
	}
	if !slot.Enabled {
		return nil, errors.New("подмодуль отключён")
	}
	inbound := &model.Inbound{
		UserId:         userID,
		Remark:         remark,
		Enable:         true,
		TrafficReset:   "never",
		Listen:         "",
		Port:           0,
		Protocol:       model.ExternalSubscription,
		Settings:       ExternalSubscriptionInboundSettings(slotID),
		StreamSettings: "{}",
	}
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(inbound).Error; err != nil {
			return err
		}
		inbound.Tag = fmt.Sprintf("external-subscription-%d", inbound.Id)
		return tx.Model(inbound).Update("tag", inbound.Tag).Error
	})
	if err != nil {
		return nil, err
	}
	s.SetInboundProtocolName(inbound)
	return inbound, nil
}

// UpdateInbound only changes the label and selected slot; it preserves all account
// memberships stored in settings.clients. Runtime configuration is never touched.
func (s *DynamicSubscriptionService) UpdateInbound(inboundID, slotID int, remark string, enabled *bool) (*model.Inbound, error) {
	remark = strings.TrimSpace(remark)
	if inboundID < 1 || slotID < 1 || remark == "" || len([]rune(remark)) > 128 || strings.ContainsAny(remark, "\r\n\x00") {
		return nil, errors.New("нужно указать имя подключения и существующий подмодуль")
	}
	var slot model.ExternalSubscriptionSlot
	if err := database.GetDB().First(&slot, slotID).Error; err != nil {
		return nil, errors.New("подмодуль не найден")
	}
	var inbound model.Inbound
	if err := database.GetDB().First(&inbound, inboundID).Error; err != nil {
		return nil, err
	}
	if inbound.Protocol != model.ExternalSubscription {
		return nil, errors.New("подключение не является внешним")
	}
	if !slot.Enabled && slot.ID != ExternalSubscriptionSlotID(&inbound) {
		return nil, errors.New("подмодуль отключён")
	}
	var settings externalSlotSettings
	if err := jsonUnmarshalInboundSettings(inbound.Settings, &settings); err != nil {
		return nil, errors.New("настройки подключения повреждены")
	}
	settings.SlotID = slotID
	inbound.Settings = marshalExternalSlotSettings(settings)
	inbound.Remark = remark
	if enabled != nil {
		inbound.Enable = *enabled
	}
	if err := database.GetDB().Save(&inbound).Error; err != nil {
		return nil, err
	}
	s.SetInboundProtocolName(&inbound)
	return &inbound, nil
}

func (s *DynamicSubscriptionService) SetInboundProtocolName(inbound *model.Inbound) {
	if !IsExternalSubscriptionInbound(inbound) {
		return
	}
	inbound.ProtocolName = ExternalSubscriptionDefaultName
	settings, err := s.Settings()
	if err == nil && settings.DisplayName != "" {
		inbound.ProtocolName = settings.DisplayName
	}
}

type ExternalSubscriptionSlotOption struct {
	ID      int    `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Ready   bool   `json:"ready"`
}

func (s *DynamicSubscriptionService) ExternalSelectorInstalled() bool {
	return externalSelectorInstalled()
}

// ExternalSubscriptionDedupGroup identifies slots with the same country-filter
// semantics. The source URL is deliberately excluded: equal filter mode and flag set
// share one de-duplication scope for a single rendered user subscription.
func (s *DynamicSubscriptionService) ExternalSubscriptionDedupGroup(slotID int) string {
	if slotID < 1 {
		return ""
	}
	var slot model.ExternalSubscriptionSlot
	if err := database.GetDB().Select("filter_mode", "country_flags_json").First(&slot, slotID).Error; err != nil {
		return ""
	}
	mode := strings.ToLower(strings.TrimSpace(slot.FilterMode))
	if mode == "" {
		mode = "include"
	}
	var flags []string
	_ = json.Unmarshal([]byte(slot.CountryFlagsJSON), &flags)
	flags, err := normalizeCountryFlags(flags)
	if err != nil {
		flags = nil
	}
	encodedFlags, _ := json.Marshal(flags)
	return mode + "\x00" + string(encodedFlags)
}

func (s *DynamicSubscriptionService) SlotOptions() ([]ExternalSubscriptionSlotOption, error) {
	moduleInstalled := externalSelectorEnabled()
	var rows []model.ExternalSubscriptionSlot
	if err := database.GetDB().Select("id", "name", "enabled", "status").Order("CASE WHEN sort_order > 0 THEN 0 ELSE 1 END, sort_order, id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	options := make([]ExternalSubscriptionSlotOption, 0, len(rows))
	for _, row := range rows {
		options = append(options, ExternalSubscriptionSlotOption{
			ID: row.ID, Name: row.Name, Enabled: row.Enabled,
			Ready: moduleInstalled && row.Enabled && row.Status == "healthy",
		})
	}
	return options, nil
}

// ReorderSlots persists the display order for all submodules. This changes neither
// their active keys nor any inbound; new slots with sort_order=0 naturally append.
func (s *DynamicSubscriptionService) ReorderSlots(ids []int) error {
	if len(ids) < 2 {
		return nil
	}
	db := database.GetDB()
	var rows []model.ExternalSubscriptionSlot
	if err := db.Select("id").Order("CASE WHEN sort_order > 0 THEN 0 ELSE 1 END, sort_order, id ASC").Find(&rows).Error; err != nil {
		return err
	}
	if len(rows) != len(ids) {
		return errors.New("список подмодулей изменился; обновите страницу")
	}
	known := make(map[int]bool, len(rows))
	for _, row := range rows {
		known[row.ID] = true
	}
	seen := make(map[int]bool, len(ids))
	for _, id := range ids {
		if !known[id] || seen[id] {
			return errors.New("неверный порядок подмодулей")
		}
		seen[id] = true
	}
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	for i, id := range ids {
		if err := tx.Model(&model.ExternalSubscriptionSlot{}).Where("id = ?", id).Update("sort_order", i+1).Error; err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit().Error
}

func (s *DynamicSubscriptionService) DecorateInbounds(inbounds []*model.Inbound) {
	name := ExternalSubscriptionDefaultName
	if settings, err := s.Settings(); err == nil && settings.DisplayName != "" {
		name = settings.DisplayName
	}
	for _, inbound := range inbounds {
		if IsExternalSubscriptionInbound(inbound) {
			inbound.ProtocolName = name
		}
	}
}

func jsonUnmarshalInboundSettings(raw string, target *externalSlotSettings) error {
	return json.Unmarshal([]byte(raw), target)
}

func marshalExternalSlotSettings(settings externalSlotSettings) string {
	encoded, err := json.Marshal(settings)
	if err != nil {
		return `{"slotId":0,"clients":[]}`
	}
	return string(encoded)
}
