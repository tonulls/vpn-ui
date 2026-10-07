package service

import (
	"encoding/json"
	"errors"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
)

const coreDisplayOrderSetting = "coreDisplayOrder"

var coreDisplayOrderNames = map[string]bool{
	"xray": true, "l2tp": true, "ipsec": true, "pptp": true,
	"openvpn": true, "openconnect": true, "sstp": true, "ikev2": true,
	"wgc": true, "awg": true, "gre": true, "mtproto": true,
	"ssh": true, "radius": true, "external-selector": true,
}

// ApplyCoreDisplayOrder puts the requested names first and leaves newly added
// statuses in their catalog order. A malformed or missing preference is harmless.
func (s *CoreService) ApplyCoreDisplayOrder(cores []CoreStatus) []CoreStatus {
	var setting model.Setting
	if err := database.GetDB().Where("key = ?", coreDisplayOrderSetting).First(&setting).Error; err != nil {
		return cores
	}
	var order []string
	if err := json.Unmarshal([]byte(setting.Value), &order); err != nil {
		return cores
	}
	byName := make(map[string]CoreStatus, len(cores))
	for _, core := range cores {
		byName[core.Name] = core
	}
	ordered := make([]CoreStatus, 0, len(cores))
	seen := make(map[string]bool, len(cores))
	for _, name := range order {
		if core, ok := byName[name]; ok && !seen[name] {
			ordered = append(ordered, core)
			seen[name] = true
		}
	}
	for _, core := range cores {
		if !seen[core.Name] {
			ordered = append(ordered, core)
		}
	}
	return ordered
}

// ReorderCores stores a presentation preference only; it never starts, stops, or
// reconfigures a daemon. The browser posts only installed cards, so names omitted
// here keep their default tail position until they are installed and rearranged.
func (s *CoreService) ReorderCores(names []string) error {
	if len(names) == 0 {
		return errors.New("порядок модулей пуст")
	}
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !coreDisplayOrderNames[name] || seen[name] {
			return errors.New("неверный порядок модулей")
		}
		seen[name] = true
	}
	encoded, err := json.Marshal(names)
	if err != nil {
		return err
	}
	db := database.GetDB()
	var setting model.Setting
	err = db.Where("key = ?", coreDisplayOrderSetting).First(&setting).Error
	if database.IsNotFound(err) {
		return db.Create(&model.Setting{Key: coreDisplayOrderSetting, Value: string(encoded)}).Error
	}
	if err != nil {
		return err
	}
	return db.Model(&setting).Update("value", string(encoded)).Error
}
