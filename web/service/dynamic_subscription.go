package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"gorm.io/gorm"
)

const (
	ExternalSubscriptionSettingsID                      = 1
	ExternalSubscriptionDefaultName                     = "External Selector"
	ExternalSubscriptionDefaultProbeURL                 = "https://www.google.com/generate_204"
	ExternalSubscriptionDefaultProbeURL2                = "https://cp.cloudflare.com/generate_204"
	ExternalSubscriptionDefaultProbeURL3                = "https://www.msftconnecttest.com/connecttest.txt"
	ExternalSubscriptionProbeSuccessTarget              = 2
	ExternalSubscriptionMaxProbeURLs                    = 10
	ExternalSubscriptionMaxKeysPerSlot                  = 10
	ExternalSubscriptionMaxForceRotationIntervalMinutes = 10080
	ExternalSubscriptionProbeTimeout                    = 20 * time.Second
	ExternalSubscriptionMaxCandidatesPerRun             = 3
	ExternalSubscriptionCandidateBatchDelay             = 10 * time.Second
)

var (
	ErrExternalSubscriptionProbeBusy = errors.New("external subscription probe is busy")
	externalSubscriptionRunMu        sync.Mutex
)

// DynamicSubscriptionService manages remote lists and verified URI selections per slot.
// Credential-bearing URIs are returned only by subscription getters, called after the
// normal account/membership checks.
type DynamicSubscriptionService struct {
	fetchSource func(context.Context, string, string, string) (externalSourceResult, error)
	probe       func(context.Context, ExternalSubscriptionCandidate, string) error
	now         func() time.Time
}

type ExternalSubscriptionSettingsView struct {
	DisplayName string   `json:"displayName" form:"displayName"`
	ProbeURL    string   `json:"probeUrl" form:"probeUrl"` // Deprecated alias for ProbeURLs[0].
	ProbeURLs   []string `json:"probeUrls" form:"probeUrls"`
	Enabled     bool     `json:"enabled" form:"enabled"`
}

type ExternalSubscriptionSlotInput struct {
	Name                         string   `json:"name" form:"name"`
	SourceURL                    string   `json:"sourceUrl" form:"sourceUrl"`
	RefreshIntervalMinutes       int      `json:"refreshIntervalMinutes" form:"refreshIntervalMinutes"`
	CheckIntervalMinutes         int      `json:"checkIntervalMinutes" form:"checkIntervalMinutes"`
	SubscriptionKeyCount         int      `json:"subscriptionKeyCount" form:"subscriptionKeyCount"`
	ForceRotationIntervalMinutes int      `json:"forceRotationIntervalMinutes" form:"forceRotationIntervalMinutes"`
	FilterMode                   string   `json:"filterMode" form:"filterMode"`
	SelectionMode                string   `json:"selectionMode" form:"selectionMode"`
	CountryFlags                 []string `json:"countryFlags" form:"countryFlags"`
	Enabled                      *bool    `json:"enabled" form:"enabled"`
}

type ExternalSubscriptionSlotView struct {
	ID                           int      `json:"id"`
	Name                         string   `json:"name"`
	SourceURL                    string   `json:"sourceUrl"`
	RefreshIntervalMinutes       int      `json:"refreshIntervalMinutes"`
	CheckIntervalMinutes         int      `json:"checkIntervalMinutes"`
	SubscriptionKeyCount         int      `json:"subscriptionKeyCount"`
	ForceRotationIntervalMinutes int      `json:"forceRotationIntervalMinutes"`
	HealthyKeyCount              int      `json:"healthyKeyCount"`
	ActiveFlags                  []string `json:"activeFlags"`
	FilterMode                   string   `json:"filterMode"`
	SelectionMode                string   `json:"selectionMode"`
	CountryFlags                 []string `json:"countryFlags"`
	AvailableFlags               []string `json:"availableFlags"`
	Enabled                      bool     `json:"enabled"`
	ActiveName                   string   `json:"activeName"`
	ActiveFlag                   string   `json:"activeFlag"`
	Status                       string   `json:"status"`
	LastError                    string   `json:"lastError"`
	CandidateCount               int      `json:"candidateCount"`
	MatchingCount                int      `json:"matchingCount"`
	CheckedCount                 int      `json:"checkedCount"`
	RotationPending              bool     `json:"rotationPending"`
	RotationTotal                int      `json:"rotationTotal"`
	LastFetchedAt                int64    `json:"lastFetchedAt"`
	LastCheckedAt                int64    `json:"lastCheckedAt"`
}

type ExternalSubscriptionModuleView struct {
	Installed bool                             `json:"installed"`
	Settings  ExternalSubscriptionSettingsView `json:"settings"`
	Slots     []ExternalSubscriptionSlotView   `json:"slots"`
}

type externalSourceResult struct {
	Body         []byte
	ETag         string
	LastModified string
	NotModified  bool
}

type externalSlotSettings struct {
	SlotID  int            `json:"slotId"`
	Clients []model.Client `json:"clients"`
}

func IsExternalSubscriptionInbound(inbound *model.Inbound) bool {
	return inbound != nil && inbound.Protocol == model.ExternalSubscription
}

func ExternalSubscriptionSlotID(inbound *model.Inbound) int {
	if !IsExternalSubscriptionInbound(inbound) {
		return 0
	}
	var settings externalSlotSettings
	if json.Unmarshal([]byte(inbound.Settings), &settings) != nil {
		return 0
	}
	return settings.SlotID
}

func ExternalSubscriptionInboundSettings(slotID int) string {
	data, _ := json.Marshal(externalSlotSettings{SlotID: slotID, Clients: []model.Client{}})
	return string(data)
}

func externalSelectorInstalled() bool {
	var cores CoreService
	return cores.provisionedProtocolSet()["external-selector"]
}

func externalSelectorEnabled() bool {
	if !externalSelectorInstalled() {
		return false
	}
	settings, err := (&DynamicSubscriptionService{}).Settings()
	return err == nil && settings.Enabled
}

func (s *DynamicSubscriptionService) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *DynamicSubscriptionService) Settings() (ExternalSubscriptionSettingsView, error) {
	db := database.GetDB()
	settings := model.ExternalSubscriptionSettings{ID: ExternalSubscriptionSettingsID}
	err := db.Where("id = ?", ExternalSubscriptionSettingsID).First(&settings).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		settings = model.ExternalSubscriptionSettings{
			ID: ExternalSubscriptionSettingsID, DisplayName: ExternalSubscriptionDefaultName,
			ProbeURLsJSON: "[]", Enabled: true,
		}
		if err = db.Create(&settings).Error; err != nil {
			err = db.Where("id = ?", ExternalSubscriptionSettingsID).First(&settings).Error
		}
	}
	if err != nil {
		return ExternalSubscriptionSettingsView{}, err
	}
	if settings.DisplayName == "" || settings.DisplayName == "Внешние узлы" {
		// Rename only the old built-in default; preserve any administrator-chosen
		// display name across upgrades.
		if settings.DisplayName != "" {
			if err := db.Model(&settings).UpdateColumn("display_name", ExternalSubscriptionDefaultName).Error; err != nil {
				return ExternalSubscriptionSettingsView{}, err
			}
		}
		settings.DisplayName = ExternalSubscriptionDefaultName
	}
	return externalSubscriptionSettingsView(settings), nil
}

func externalSubscriptionProbeURLs(settings model.ExternalSubscriptionSettings) []string {
	var urls []string
	parsed := settings.ProbeURLsJSON != "" && json.Unmarshal([]byte(settings.ProbeURLsJSON), &urls) == nil
	if !parsed || len(urls) == 0 {
		legacyURLs := []string{settings.ProbeURL, settings.ProbeURL2, settings.ProbeURL3}
		for _, raw := range legacyURLs {
			if strings.TrimSpace(raw) != "" {
				urls = append(urls, raw)
			}
		}
	}
	clean := make([]string, 0, len(urls))
	for _, raw := range urls {
		if url := strings.TrimSpace(raw); url != "" {
			clean = append(clean, url)
		}
	}
	return clean
}

func equalExternalProbeURLs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func externalSubscriptionSettingsView(settings model.ExternalSubscriptionSettings) ExternalSubscriptionSettingsView {
	urls := externalSubscriptionProbeURLs(settings)
	firstURL := ""
	if len(urls) > 0 {
		firstURL = urls[0]
	}
	return ExternalSubscriptionSettingsView{
		DisplayName: settings.DisplayName,
		ProbeURL:    firstURL,
		ProbeURLs:   urls,
		Enabled:     settings.Enabled,
	}
}

func (s *DynamicSubscriptionService) UpdateSettings(view ExternalSubscriptionSettingsView) (ExternalSubscriptionSettingsView, error) {
	name := strings.TrimSpace(view.DisplayName)
	if name == "" || len([]rune(name)) > 64 || strings.ContainsAny(name, "\r\n\x00") {
		return ExternalSubscriptionSettingsView{}, errors.New("нужно указать название модуля длиной до 64 символов")
	}
	settings, err := s.settingsRow()
	if err != nil {
		return ExternalSubscriptionSettingsView{}, err
	}
	probeURLsProvided := view.ProbeURLs != nil
	probeURLs := make([]string, 0, len(view.ProbeURLs))
	for _, raw := range view.ProbeURLs {
		if value := strings.TrimSpace(raw); value != "" {
			probeURLs = append(probeURLs, value)
		}
	}
	if len(probeURLs) == 0 && !probeURLsProvided {
		// Keep compatibility with older clients that still submit the former single-URL field.
		if legacyURL := strings.TrimSpace(view.ProbeURL); legacyURL != "" {
			probeURLs = []string{legacyURL}
		} else {
			probeURLs = externalSubscriptionProbeURLs(settings)
		}
	}
	if len(probeURLs) > ExternalSubscriptionMaxProbeURLs {
		return ExternalSubscriptionSettingsView{}, fmt.Errorf("можно указать не более %d адресов проверки", ExternalSubscriptionMaxProbeURLs)
	}
	seenProbeURLs := make(map[string]struct{}, len(probeURLs))
	for i := range probeURLs {
		if err := validateExternalHTTPSURL(probeURLs[i]); err != nil {
			return ExternalSubscriptionSettingsView{}, errors.New("каждый адрес проверки должен быть публичным HTTPS URL без логина и пароля")
		}
		normalized := strings.TrimRight(strings.ToLower(probeURLs[i]), "/")
		if _, exists := seenProbeURLs[normalized]; exists {
			return ExternalSubscriptionSettingsView{}, errors.New("адреса проверки должны отличаться друг от друга")
		}
		seenProbeURLs[normalized] = struct{}{}
	}
	oldProbeURLs := externalSubscriptionProbeURLs(settings)
	probeURLsJSON, _ := json.Marshal(probeURLs)
	settings.DisplayName = name
	settings.ProbeURLsJSON = string(probeURLsJSON)
	settings.ProbeURL, settings.ProbeURL2, settings.ProbeURL3 = "", "", ""
	for i, probeURL := range probeURLs {
		switch i {
		case 0:
			settings.ProbeURL = probeURL
		case 1:
			settings.ProbeURL2 = probeURL
		case 2:
			settings.ProbeURL3 = probeURL
		}
	}
	if err := database.GetDB().Save(&settings).Error; err != nil {
		return ExternalSubscriptionSettingsView{}, err
	}
	if !equalExternalProbeURLs(oldProbeURLs, probeURLs) {
		now := s.clock().UnixMilli()
		_ = database.GetDB().Model(&model.ExternalSubscriptionSlot{}).Where("enabled = ?", true).Updates(map[string]any{
			"status": "checking", "next_check_at": now, "candidate_cursor": 0,
			"manual_rotate": false, "last_error": "",
		}).Error
	}
	return externalSubscriptionSettingsView(settings), nil
}

func (s *DynamicSubscriptionService) settingsRow() (model.ExternalSubscriptionSettings, error) {
	view, err := s.Settings()
	if err != nil {
		return model.ExternalSubscriptionSettings{}, err
	}
	row := model.ExternalSubscriptionSettings{ID: ExternalSubscriptionSettingsID}
	if err := database.GetDB().First(&row, ExternalSubscriptionSettingsID).Error; err != nil {
		return model.ExternalSubscriptionSettings{}, err
	}
	if row.DisplayName == "" {
		row.DisplayName = view.DisplayName
	}
	if row.ProbeURL == "" {
		row.ProbeURL = view.ProbeURL
	}
	if row.ProbeURL2 == "" && len(view.ProbeURLs) > 1 {
		row.ProbeURL2 = view.ProbeURLs[1]
	}
	if row.ProbeURL3 == "" && len(view.ProbeURLs) > 2 {
		row.ProbeURL3 = view.ProbeURLs[2]
	}
	if row.ProbeURLsJSON == "" || row.ProbeURLsJSON == "[]" {
		encoded, _ := json.Marshal(view.ProbeURLs)
		row.ProbeURLsJSON = string(encoded)
	}
	return row, nil
}

func (s *DynamicSubscriptionService) Module() (ExternalSubscriptionModuleView, error) {
	settings, err := s.Settings()
	if err != nil {
		return ExternalSubscriptionModuleView{}, err
	}
	var rows []model.ExternalSubscriptionSlot
	if err := database.GetDB().Order("CASE WHEN sort_order > 0 THEN 0 ELSE 1 END, sort_order, id ASC").Find(&rows).Error; err != nil {
		return ExternalSubscriptionModuleView{}, err
	}
	view := ExternalSubscriptionModuleView{Installed: externalSelectorInstalled(), Settings: settings, Slots: make([]ExternalSubscriptionSlotView, 0, len(rows))}
	for i := range rows {
		slotView := externalSlotView(&rows[i])
		view.Slots = append(view.Slots, slotView)
	}
	return view, nil
}

func (s *DynamicSubscriptionService) StopModule() error {
	if !externalSelectorInstalled() {
		return errors.New("External Selector не установлен")
	}
	if !externalSubscriptionRunMu.TryLock() {
		return ErrExternalSubscriptionProbeBusy
	}
	defer externalSubscriptionRunMu.Unlock()
	settings, err := s.settingsRow()
	if err != nil {
		return err
	}
	settings.Enabled = false
	return database.GetDB().Save(&settings).Error
}

// RestartModule resumes the feature and schedules every enabled slot for a fresh
// source fetch and probe. The scheduler performs network work asynchronously.
func (s *DynamicSubscriptionService) RestartModule() error {
	if !externalSelectorInstalled() {
		return errors.New("External Selector не установлен")
	}
	if !externalSubscriptionRunMu.TryLock() {
		return ErrExternalSubscriptionProbeBusy
	}
	defer externalSubscriptionRunMu.Unlock()
	settings, err := s.settingsRow()
	if err != nil {
		return err
	}
	settings.Enabled = true
	if err := database.GetDB().Save(&settings).Error; err != nil {
		return err
	}
	now := s.clock().UnixMilli()
	return database.GetDB().Model(&model.ExternalSubscriptionSlot{}).
		Where("enabled = ?", true).
		Updates(map[string]any{
			"next_fetch_at": now, "next_check_at": now, "status": "pending",
			"active_uri": "", "active_fingerprint": "", "active_name": "",
			"active_flag": "", "candidate_cursor": 0, "manual_rotate": false, "last_error": "",
		}).Error
}

// ModuleLogSummary returns a credential-free operational snapshot. There is no
// module daemon, so historical process logs do not exist.
func (s *DynamicSubscriptionService) ModuleLogSummary() (string, error) {
	module, err := s.Module()
	if err != nil {
		return "", err
	}
	state := "не установлен"
	if module.Installed {
		state = "остановлен"
		if module.Settings.Enabled {
			state = "работает"
		}
	}
	lines := []string{"External Selector: " + state, "Исторический журнал процесса отсутствует; ниже — текущее состояние слотов."}
	for _, slot := range module.Slots {
		lines = append(lines, fmt.Sprintf("Подмодуль #%d: %s; подходящих узлов %d из %d; последняя загрузка %s; последняя проверка %s",
			slot.ID, slot.Status, slot.MatchingCount, slot.CandidateCount,
			formatExternalTimestamp(slot.LastFetchedAt), formatExternalTimestamp(slot.LastCheckedAt)))
		if slot.LastError != "" {
			lines = append(lines, "  Последняя ошибка: "+slot.LastError)
		}
	}
	if len(module.Slots) == 0 {
		lines = append(lines, "Подмодули ещё не созданы.")
	}
	return strings.Join(lines, "\n"), nil
}

func formatExternalTimestamp(milliseconds int64) string {
	if milliseconds <= 0 {
		return "—"
	}
	return time.UnixMilli(milliseconds).Format(time.RFC3339)
}

func (s *DynamicSubscriptionService) CreateSlot(input ExternalSubscriptionSlotInput) (ExternalSubscriptionSlotView, error) {
	row, err := normalizeExternalSlotInput(input)
	if err != nil {
		return ExternalSubscriptionSlotView{}, err
	}
	now := s.clock().UnixMilli()
	enabled := row.Enabled
	row.CandidateDataJSON = "[]"
	row.Status = "pending"
	row.NextFetchAt = now
	row.NextCheckAt = now
	if row.ForceRotationIntervalMinutes > 0 {
		row.NextForcedRotationAt = s.clock().Add(time.Duration(row.ForceRotationIntervalMinutes) * time.Minute).UnixMilli()
	}
	row.LastError = ""
	if !enabled {
		row.Status = "disabled"
		row.NextForcedRotationAt = 0
	}
	db := database.GetDB()
	if err := db.Create(&row).Error; err != nil {
		return ExternalSubscriptionSlotView{}, err
	}
	// GORM applies the model's `default:true` tag to false on INSERT. Restore an
	// explicit disabled value after insertion so the API and scheduler agree.
	if !enabled {
		if err := db.Model(&row).UpdateColumn("enabled", false).Error; err != nil {
			return ExternalSubscriptionSlotView{}, err
		}
		row.Enabled = false
	}
	return externalSlotView(&row), nil
}

func (s *DynamicSubscriptionService) UpdateSlot(id int, input ExternalSubscriptionSlotInput) (ExternalSubscriptionSlotView, error) {
	if id < 1 {
		return ExternalSubscriptionSlotView{}, errors.New("неверный идентификатор подмодуля")
	}
	var row model.ExternalSubscriptionSlot
	if err := database.GetDB().First(&row, id).Error; err != nil {
		return ExternalSubscriptionSlotView{}, err
	}
	if input.SubscriptionKeyCount == 0 {
		input.SubscriptionKeyCount = row.SubscriptionKeyCount
		if input.SubscriptionKeyCount == 0 {
			input.SubscriptionKeyCount = 1
		}
	}
	updated, err := normalizeExternalSlotInput(input)
	if err != nil {
		return ExternalSubscriptionSlotView{}, err
	}
	wasEnabled := row.Enabled
	changedSelection := row.SourceURL != updated.SourceURL || row.FilterMode != updated.FilterMode || row.CountryFlagsJSON != updated.CountryFlagsJSON
	changedKeyCount := row.SubscriptionKeyCount != updated.SubscriptionKeyCount
	changedForceRotation := row.ForceRotationIntervalMinutes != updated.ForceRotationIntervalMinutes
	row.Name = updated.Name
	row.SourceURL = updated.SourceURL
	row.RefreshIntervalMinutes = updated.RefreshIntervalMinutes
	row.CheckIntervalMinutes = updated.CheckIntervalMinutes
	row.SubscriptionKeyCount = updated.SubscriptionKeyCount
	row.ForceRotationIntervalMinutes = updated.ForceRotationIntervalMinutes
	row.FilterMode = updated.FilterMode
	row.SelectionMode = updated.SelectionMode
	row.CountryFlagsJSON = updated.CountryFlagsJSON
	row.Enabled = updated.Enabled
	now := s.clock().UnixMilli()
	if changedForceRotation {
		row.NextForcedRotationAt = 0
		if row.Enabled && row.ForceRotationIntervalMinutes > 0 {
			row.NextForcedRotationAt = s.clock().Add(time.Duration(row.ForceRotationIntervalMinutes) * time.Minute).UnixMilli()
		}
	}
	if changedSelection {
		s.clearActive(&row)
		row.RotationFingerprintsJSON = "[]"
		row.Status = "pending"
		row.CandidateCursor = 0
		row.ManualRotate = false
		row.NextFetchAt = now
		row.NextCheckAt = now
		row.LastError = ""
	} else if changedKeyCount {
		row.RotationFingerprintsJSON = "[]"
		selected := slotSelectedCandidates(&row, filterExternalCandidates(
			&slotCandidateConfig{Mode: row.FilterMode, FlagsJSON: row.CountryFlagsJSON},
			parseStoredCandidates(row.CandidateDataJSON),
		))
		if len(selected) > row.SubscriptionKeyCount {
			selected = selected[:row.SubscriptionKeyCount]
		}
		setSlotSelectedCandidates(&row, selected)
		row.CandidateCursor = 0
		row.ManualRotate = false
		row.Status = "checking"
		row.NextCheckAt = now
		row.LastError = ""
	}
	if !row.Enabled {
		row.ManualRotate = false
		row.RotationFingerprintsJSON = "[]"
		row.NextForcedRotationAt = 0
		s.clearActive(&row)
		row.Status = "disabled"
	} else if !wasEnabled {
		row.Status = "pending"
		row.NextFetchAt = now
		row.NextCheckAt = now
		row.LastError = ""
		if row.ForceRotationIntervalMinutes > 0 {
			row.NextForcedRotationAt = s.clock().Add(time.Duration(row.ForceRotationIntervalMinutes) * time.Minute).UnixMilli()
		}
	}
	if err := database.GetDB().Save(&row).Error; err != nil {
		return ExternalSubscriptionSlotView{}, err
	}
	return externalSlotView(&row), nil
}

func (s *DynamicSubscriptionService) DeleteSlot(id int) error {
	if id < 1 {
		return errors.New("неверный идентификатор подмодуля")
	}
	var inbounds []model.Inbound
	if err := database.GetDB().Where("protocol = ?", model.ExternalSubscription).Find(&inbounds).Error; err != nil {
		return err
	}
	for i := range inbounds {
		if ExternalSubscriptionSlotID(&inbounds[i]) == id {
			return errors.New("подмодуль используется подключением; сначала удалите это подключение")
		}
	}
	return database.GetDB().Delete(&model.ExternalSubscriptionSlot{}, id).Error
}

func (s *DynamicSubscriptionService) RequestRefresh(id int) error {
	if id < 1 {
		return errors.New("неверный идентификатор подмодуля")
	}
	db := database.GetDB()
	var slot model.ExternalSubscriptionSlot
	if err := db.First(&slot, id).Error; err != nil {
		return err
	}
	now := s.clock().UnixMilli()
	return db.Model(&slot).
		Updates(map[string]any{"next_fetch_at": now, "next_check_at": now, "last_error": ""}).Error
}

// RequestNextSlotCandidate starts a bounded background scan for a complete replacement pool.
// The current set remains active until the replacement set passes the configured probes.
func (s *DynamicSubscriptionService) RequestNextSlotCandidate(id int) error {
	if id < 1 {
		return errors.New("неверный идентификатор подмодуля")
	}
	if !externalSelectorEnabled() {
		return errors.New("External Selector не установлен или остановлен")
	}
	externalSubscriptionRunMu.Lock()
	defer externalSubscriptionRunMu.Unlock()

	var slot model.ExternalSubscriptionSlot
	if err := database.GetDB().First(&slot, id).Error; err != nil {
		return err
	}
	if !slot.Enabled {
		return errors.New("подмодуль отключён")
	}
	if slot.ManualRotate {
		return errors.New("поиск следующего узла уже выполняется")
	}
	candidates := filterExternalCandidates(
		&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON},
		parseStoredCandidates(slot.CandidateDataJSON),
	)
	selected := slotSelectedCandidates(&slot, candidates)
	selectedSet := make(map[string]struct{}, len(selected))
	for _, candidate := range selected {
		selectedSet[candidate.Fingerprint] = struct{}{}
	}
	hasReplacement := false
	for _, candidate := range candidates {
		if _, exists := selectedSet[candidate.Fingerprint]; !exists {
			hasReplacement = true
			break
		}
	}
	if !hasReplacement {
		return errors.New("нет другого подходящего набора ключей для переключения")
	}
	now := s.clock().UnixMilli()
	slot.CandidateCursor = 0
	slot.RotationFingerprintsJSON = "[]"
	slot.ManualRotate = true
	slot.Status = "checking"
	slot.LastError = "Ищется новый проверенный набор ключей; текущий набор останется до полной проверки."
	slot.NextCheckAt = now
	return database.GetDB().Save(&slot).Error
}

// ActiveURIForSubscription retains the legacy singular accessor. New renderers should
// use ActiveURIsForSubscription so a configured pool is not silently truncated.
func (s *DynamicSubscriptionService) ActiveURIForSubscription(slotID int) (string, bool) {
	uris, ok := s.ActiveURIsForSubscription(slotID)
	if !ok || len(uris) == 0 {
		return "", false
	}
	return uris[0], true
}

// ActiveURIsForSubscription is intentionally the only manager API returning
// third-party URIs. It prefixes each displayed fragment with the slot name while
// leaving the stored URI and all connection parameters unchanged. Callers must invoke
// it only after their usual subscription token, membership, enable and quota checks.
func (s *DynamicSubscriptionService) ActiveURIsForSubscription(slotID int) ([]string, bool) {
	if slotID < 1 || !externalSelectorEnabled() {
		return nil, false
	}
	settings, err := s.settingsRow()
	if err != nil || len(externalSubscriptionProbeURLs(settings)) == 0 {
		return nil, false
	}
	var row model.ExternalSubscriptionSlot
	if err := database.GetDB().First(&row, slotID).Error; err != nil {
		return nil, false
	}
	if !row.Enabled {
		return nil, false
	}
	filtered := filterExternalCandidates(
		&slotCandidateConfig{Mode: row.FilterMode, FlagsJSON: row.CountryFlagsJSON},
		parseStoredCandidates(row.CandidateDataJSON),
	)
	selected := slotSelectedCandidates(&row, filtered)
	if len(selected) == 0 && row.ActiveURI != "" {
		selected = []ExternalSubscriptionCandidate{{URI: row.ActiveURI}}
	}
	if len(selected) == 0 || (row.Status != "healthy" && row.Status != "partial" && row.Status != "checking" && !row.ManualRotate) {
		return nil, false
	}
	if row.SubscriptionKeyCount > 0 && len(selected) > row.SubscriptionKeyCount {
		selected = selected[:row.SubscriptionKeyCount]
	}
	switch strings.ToLower(strings.TrimSpace(row.SelectionMode)) {
	case "a-z":
		sort.SliceStable(selected, func(i, j int) bool {
			return indexCandidate(filtered, selected[i].Fingerprint) < indexCandidate(filtered, selected[j].Fingerprint)
		})
	case "z-a":
		sort.SliceStable(selected, func(i, j int) bool {
			return indexCandidate(filtered, selected[i].Fingerprint) > indexCandidate(filtered, selected[j].Fingerprint)
		})
	default: // AUTO, including rows created before this setting existed.
		rand.Shuffle(len(selected), func(i, j int) { selected[i], selected[j] = selected[j], selected[i] })
	}
	uris := make([]string, 0, len(selected))
	for _, candidate := range selected {
		if candidate.URI != "" {
			uris = append(uris, prefixExternalSubscriptionURIName(candidate.URI, row.Name))
		}
	}
	return uris, len(uris) > 0
}

// prefixExternalSubscriptionURIName adds the slot's label to a VLESS URI fragment.
// Only the fragment prefix is encoded and changed; source query parameters and the
// stored source URI remain byte-for-byte untouched.
func prefixExternalSubscriptionURIName(rawURI, slotName string) string {
	slotName = strings.TrimSpace(slotName)
	fragmentAt := strings.IndexByte(rawURI, '#')
	if slotName == "" || fragmentAt < 0 {
		return rawURI
	}
	return rawURI[:fragmentAt+1] + url.PathEscape(slotName+" · ") + rawURI[fragmentAt+1:]
}

func (s *DynamicSubscriptionService) RunDue(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !externalSelectorEnabled() {
		return
	}
	if !externalSubscriptionRunMu.TryLock() {
		return
	}
	defer externalSubscriptionRunMu.Unlock()
	if !externalSelectorEnabled() {
		return
	}
	var slots []model.ExternalSubscriptionSlot
	if err := database.GetDB().WithContext(ctx).Order("id ASC").Find(&slots).Error; err != nil {
		return
	}
	for i := range slots {
		if ctx.Err() != nil {
			return
		}
		slot := &slots[i]
		if !slot.Enabled {
			continue
		}
		now := s.clock().UnixMilli()
		if slot.NextFetchAt == 0 || slot.NextFetchAt <= now {
			s.refreshSlot(ctx, slot)
		}
		if ctx.Err() != nil {
			return
		}
		now = s.clock().UnixMilli()
		if slot.ForceRotationIntervalMinutes > 0 && (slot.NextForcedRotationAt == 0 || slot.NextForcedRotationAt <= now) {
			if !slot.ManualRotate {
				slot.ManualRotate = true
				slot.RotationFingerprintsJSON = "[]"
				slot.CandidateCursor = 0
				slot.Status = "checking"
				slot.LastError = "По расписанию собирается новый набор; текущие ключи останутся до проверки замены."
				slot.NextCheckAt = now
			}
			slot.NextForcedRotationAt = s.clock().Add(time.Duration(slot.ForceRotationIntervalMinutes) * time.Minute).UnixMilli()
			_ = database.GetDB().Save(slot).Error
		}
		if slot.NextCheckAt == 0 || slot.NextCheckAt <= now {
			s.checkSlot(ctx, slot)
		}
	}
}

func (s *DynamicSubscriptionService) refreshSlot(ctx context.Context, slot *model.ExternalSubscriptionSlot) {
	fetch := s.fetchSource
	if fetch == nil {
		fetch = fetchExternalSubscriptionSource
	}
	result, err := fetch(ctx, slot.SourceURL, slot.SourceETag, slot.SourceLastModified)
	if err != nil {
		slot.LastError = "Не удалось загрузить источник"
		if slot.ActiveURI == "" {
			slot.Status = "source_error"
		}
		slot.NextFetchAt = s.clock().Add(time.Duration(slot.RefreshIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	now := s.clock()
	slot.LastFetchedAt = now.UnixMilli()
	slot.NextFetchAt = now.Add(time.Duration(slot.RefreshIntervalMinutes) * time.Minute).UnixMilli()
	slot.LastError = ""
	if result.NotModified {
		if result.ETag != "" {
			slot.SourceETag = result.ETag
		}
		if result.LastModified != "" {
			slot.SourceLastModified = result.LastModified
		}
		if slot.ActiveURI == "" && slot.MatchingCount > 0 {
			slot.NextCheckAt = now.UnixMilli()
		}
		_ = database.GetDB().Save(slot).Error
		return
	}
	candidates, stats, err := ParseExternalVLESSList(result.Body)
	if err != nil {
		slot.SourceETag = ""
		slot.SourceLastModified = ""
		slot.LastError = "Источник содержит слишком много данных или некорректный формат"
		if slot.ActiveURI == "" {
			slot.Status = "source_error"
		}
		_ = database.GetDB().Save(slot).Error
		return
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		slot.SourceETag = ""
		slot.SourceLastModified = ""
		slot.LastError = "Не удалось сохранить разобранный источник"
		if slot.ActiveURI == "" {
			slot.Status = "source_error"
		}
		_ = database.GetDB().Save(slot).Error
		return
	}
	// A full successful response replaces both validators, including clearing
	// stale values when the source stops sending them.
	slot.SourceETag = result.ETag
	slot.SourceLastModified = result.LastModified
	manualRotationPending := slot.ManualRotate
	filtered := filterExternalCandidates(&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON}, candidates)
	slot.CandidateDataJSON = string(encoded)
	slot.CandidateCount = stats.Accepted
	slot.MatchingCount = len(filtered)
	slot.CandidateCursor = 0
	selected := slotSelectedCandidates(slot, filtered)
	if len(selected) > slot.SubscriptionKeyCount && slot.SubscriptionKeyCount > 0 {
		selected = selected[:slot.SubscriptionKeyCount]
	}
	setSlotSelectedCandidates(slot, selected)
	if len(filtered) == 0 {
		slot.ManualRotate = false
		slot.RotationFingerprintsJSON = "[]"
		s.clearActive(slot)
		slot.Status = "no_candidates"
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
	} else {
		if manualRotationPending {
			setSlotRotationCandidates(slot, slotRotationCandidates(slot, filtered))
		} else {
			slot.RotationFingerprintsJSON = "[]"
		}
		slot.ManualRotate = manualRotationPending
		slot.Status = "checking"
		slot.NextCheckAt = now.UnixMilli()
	}
	_ = database.GetDB().Save(slot).Error
}

// slotCandidateConfig keeps the common filter helper independent of a DB row.
type slotCandidateConfig struct {
	Mode      string
	FlagsJSON string
}

func (s *DynamicSubscriptionService) checkSlot(ctx context.Context, slot *model.ExternalSubscriptionSlot) {
	if !slot.ManualRotate && slot.SubscriptionKeyCount <= 1 {
		s.checkSlotSingle(ctx, slot)
		return
	}
	s.checkSlotMultiple(ctx, slot)
}

func (s *DynamicSubscriptionService) checkSlotMultiple(ctx context.Context, slot *model.ExternalSubscriptionSlot) {
	candidates := parseStoredCandidates(slot.CandidateDataJSON)
	filtered := filterExternalCandidates(&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON}, candidates)
	slot.MatchingCount = len(filtered)
	now := s.clock()
	if len(filtered) == 0 {
		slot.ManualRotate = false
		slot.RotationFingerprintsJSON = "[]"
		s.clearActive(slot)
		slot.Status = "no_candidates"
		slot.LastError = ""
		slot.LastCheckedAt = now.UnixMilli()
		slot.CandidateCursor = 0
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	keyCount := slot.SubscriptionKeyCount
	if keyCount < 1 {
		keyCount = 1
	}
	if keyCount > ExternalSubscriptionMaxKeysPerSlot {
		keyCount = ExternalSubscriptionMaxKeysPerSlot
	}
	if slot.SubscriptionKeyCount != keyCount {
		slot.SubscriptionKeyCount = keyCount
	}
	selected := slotSelectedCandidates(slot, filtered)
	if len(selected) > keyCount {
		selected = selected[:keyCount]
	}
	settings, err := s.settingsRow()
	if err != nil {
		slot.Status = "probe_error"
		slot.LastError = "Не удалось прочитать настройки проверки"
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	probe := s.probe
	if probe == nil {
		probe = probeExternalVLESSCandidate
	}
	probeURLs := externalSubscriptionProbeURLs(settings)
	if len(probeURLs) == 0 {
		slot.ManualRotate = false
		slot.RotationFingerprintsJSON = "[]"
		s.clearActive(slot)
		slot.Status = "probe_error"
		slot.LastError = "Добавьте хотя бы один публичный HTTPS-адрес проверки."
		slot.CandidateCursor = 0
		slot.LastCheckedAt = now.UnixMilli()
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	probeCandidate := func(candidate ExternalSubscriptionCandidate) error {
		probeCtx, cancel := context.WithTimeout(ctx, ExternalSubscriptionProbeTimeout)
		defer cancel()
		return probeExternalVLESSCandidateSet(probeCtx, candidate, probeURLs, probe)
	}

	if slot.ManualRotate {
		s.checkSlotFullRotation(ctx, slot, filtered, selected, keyCount, probeCandidate)
		return
	}

	// Recheck a stable selected pool on each normal interval. While an incomplete
	// pool is being scanned in batches, retain its already verified members and
	// resume from CandidateCursor without probing them again.
	if slot.Status != "checking" || slot.CandidateCursor == 0 {
		verified := make([]ExternalSubscriptionCandidate, 0, len(selected))
		for index, candidate := range selected {
			if ctx.Err() != nil {
				verified = append(verified, selected[index:]...)
				setSlotSelectedCandidates(slot, verified)
				slot.Status = "checking"
				slot.CandidateCursor = 0
				slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			probeErr := probeCandidate(candidate)
			if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
				verified = append(verified, selected[index:]...)
				setSlotSelectedCandidates(slot, verified)
				slot.Status = "checking"
				slot.CandidateCursor = 0
				slot.NextCheckAt = s.clock().Add(30 * time.Second).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			if probeErr != nil {
				continue
			}
			verified = append(verified, candidate)
		}
		selected = verified
		setSlotSelectedCandidates(slot, selected)
	}

	if len(selected) >= keyCount {
		slot.Status = "healthy"
		slot.LastError = ""
		slot.CandidateCursor = 0
		slot.LastCheckedAt = s.clock().UnixMilli()
		slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	selectedSet := make(map[string]struct{}, len(selected))
	for _, candidate := range selected {
		selectedSet[candidate.Fingerprint] = struct{}{}
	}
	start := slot.CandidateCursor
	if start < 0 || start >= len(filtered) {
		start = 0
	}
	attempted := 0
	index := start
	var lastProbeError error
	for index < len(filtered) && attempted < ExternalSubscriptionMaxCandidatesPerRun {
		candidate := filtered[index]
		candidateIndex := index
		index++
		if _, exists := selectedSet[candidate.Fingerprint]; exists {
			continue
		}
		if ctx.Err() != nil {
			slot.Status = "checking"
			slot.CandidateCursor = candidateIndex
			slot.LastError = fmt.Sprintf("Выбрано %d из %d ключей; поиск продолжится.", len(selected), keyCount)
			slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
			setSlotSelectedCandidates(slot, selected)
			_ = database.GetDB().Save(slot).Error
			return
		}
		attempted++
		probeErr := probeCandidate(candidate)
		if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
			slot.Status = "checking"
			slot.CandidateCursor = candidateIndex
			slot.NextCheckAt = s.clock().Add(30 * time.Second).UnixMilli()
			setSlotSelectedCandidates(slot, selected)
			_ = database.GetDB().Save(slot).Error
			return
		}
		if probeErr != nil {
			lastProbeError = probeErr
			continue
		}
		selected = append(selected, candidate)
		selectedSet[candidate.Fingerprint] = struct{}{}
		setSlotSelectedCandidates(slot, selected)
		if len(selected) >= keyCount {
			slot.Status = "healthy"
			slot.LastError = ""
			slot.CandidateCursor = 0
			slot.ManualRotate = false
			slot.LastCheckedAt = s.clock().UnixMilli()
			slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
	}
	slot.LastCheckedAt = s.clock().UnixMilli()
	if index < len(filtered) {
		slot.Status = "checking"
		slot.CandidateCursor = index
		slot.LastError = fmt.Sprintf("Выбрано %d из %d ключей; поиск продолжается. %s", len(selected), keyCount, externalProbeFailureSummary(lastProbeError))
		slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
	} else {
		slot.CandidateCursor = 0
		slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		if len(selected) > 0 {
			slot.Status = "partial"
			slot.LastError = fmt.Sprintf("Проверены все кандидаты; здоровых ключей %d из %d. %s", len(selected), keyCount, externalProbeFailureSummary(lastProbeError))
		} else {
			slot.Status = "no_healthy_nodes"
			slot.LastError = fmt.Sprintf("Ни один из %d подходящих узлов не прошёл проверку. %s", len(filtered), externalProbeFailureSummary(lastProbeError))
		}
	}
	setSlotSelectedCandidates(slot, selected)
	_ = database.GetDB().Save(slot).Error
}

func (s *DynamicSubscriptionService) checkSlotFullRotation(
	ctx context.Context,
	slot *model.ExternalSubscriptionSlot,
	filtered []ExternalSubscriptionCandidate,
	current []ExternalSubscriptionCandidate,
	keyCount int,
	probeCandidate func(ExternalSubscriptionCandidate) error,
) {
	now := s.clock()
	setSlotSelectedCandidates(slot, current)
	currentSet := make(map[string]struct{}, len(current))
	for _, candidate := range current {
		currentSet[candidate.Fingerprint] = struct{}{}
	}
	rotation := slotRotationCandidates(slot, filtered)
	rotationSet := make(map[string]struct{}, len(rotation))
	for _, candidate := range rotation {
		rotationSet[candidate.Fingerprint] = struct{}{}
	}
	ordered := filtered
	if len(current) > 0 {
		pivot := indexCandidate(filtered, current[0].Fingerprint)
		if pivot >= 0 {
			ordered = append(append(make([]ExternalSubscriptionCandidate, 0, len(filtered)), filtered[pivot+1:]...), filtered[:pivot+1]...)
		}
	}
	index := slot.CandidateCursor
	if index < 0 || index >= len(ordered) {
		index = 0
	}
	attempted := 0
	var lastProbeError error
	for index < len(ordered) && attempted < ExternalSubscriptionMaxCandidatesPerRun {
		candidate := ordered[index]
		candidateIndex := index
		index++
		if _, exists := currentSet[candidate.Fingerprint]; exists {
			continue
		}
		if _, exists := rotationSet[candidate.Fingerprint]; exists {
			continue
		}
		if ctx.Err() != nil {
			slot.CandidateCursor = candidateIndex
			slot.Status = "checking"
			slot.LastError = fmt.Sprintf("Проверено %d из %d ключей нового набора; поиск продолжится.", len(rotation), keyCount)
			slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
			setSlotRotationCandidates(slot, rotation)
			_ = database.GetDB().Save(slot).Error
			return
		}
		attempted++
		probeErr := probeCandidate(candidate)
		if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
			slot.CandidateCursor = candidateIndex
			slot.Status = "checking"
			slot.LastError = fmt.Sprintf("Проверено %d из %d ключей нового набора; проверка продолжится.", len(rotation), keyCount)
			slot.NextCheckAt = now.Add(30 * time.Second).UnixMilli()
			setSlotRotationCandidates(slot, rotation)
			_ = database.GetDB().Save(slot).Error
			return
		}
		if probeErr != nil {
			lastProbeError = probeErr
			continue
		}
		rotation = append(rotation, candidate)
		rotationSet[candidate.Fingerprint] = struct{}{}
		if len(rotation) >= keyCount {
			setSlotSelectedCandidates(slot, rotation[:keyCount])
			slot.RotationFingerprintsJSON = "[]"
			slot.ManualRotate = false
			slot.CandidateCursor = 0
			slot.Status = "healthy"
			slot.LastError = ""
			slot.LastCheckedAt = s.clock().UnixMilli()
			slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
	}
	setSlotRotationCandidates(slot, rotation)
	slot.LastCheckedAt = s.clock().UnixMilli()
	if index < len(ordered) {
		slot.CandidateCursor = index
		slot.Status = "checking"
		slot.LastError = fmt.Sprintf("Проверено %d из %d ключей нового набора; поиск продолжается. %s", len(rotation), keyCount, externalProbeFailureSummary(lastProbeError))
		slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
	} else {
		slot.ManualRotate = false
		slot.CandidateCursor = 0
		slot.RotationFingerprintsJSON = "[]"
		slot.Status = "no_healthy_nodes"
		if len(current) > 0 {
			slot.Status = "partial"
			if len(current) >= keyCount {
				slot.Status = "healthy"
			}
		}
		slot.LastError = fmt.Sprintf("Не удалось собрать новый набор из %d проверенных ключей; прежний набор сохранён. %s", keyCount, externalProbeFailureSummary(lastProbeError))
		slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
	}
	_ = database.GetDB().Save(slot).Error
}

func (s *DynamicSubscriptionService) checkSlotSingle(ctx context.Context, slot *model.ExternalSubscriptionSlot) {
	candidates := parseStoredCandidates(slot.CandidateDataJSON)
	filtered := filterExternalCandidates(&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON}, candidates)
	slot.MatchingCount = len(filtered)
	if len(filtered) == 0 {
		slot.ManualRotate = false
		slot.RotationFingerprintsJSON = "[]"
		s.clearActive(slot)
		slot.Status = "no_candidates"
		slot.LastError = ""
		slot.LastCheckedAt = s.clock().UnixMilli()
		slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	scanCandidates := filtered
	if slot.ManualRotate {
		activeIndex := indexCandidate(filtered, slot.ActiveFingerprint)
		if activeIndex >= 0 {
			scanCandidates = make([]ExternalSubscriptionCandidate, 0, len(filtered)-1)
			scanCandidates = append(scanCandidates, filtered[activeIndex+1:]...)
			scanCandidates = append(scanCandidates, filtered[:activeIndex]...)
		}
		if len(scanCandidates) == 0 {
			slot.ManualRotate = false
			slot.CandidateCursor = 0
			slot.Status = "healthy"
			slot.LastError = "Нет другого подходящего узла для переключения; текущий оставлен."
			slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
	}
	settings, err := s.settingsRow()
	if err != nil {
		slot.Status = "probe_error"
		slot.LastError = "Не удалось прочитать настройки проверки"
		slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	probe := s.probe
	if probe == nil {
		probe = probeExternalVLESSCandidate
	}
	probeURLs := externalSubscriptionProbeURLs(settings)
	if len(probeURLs) == 0 {
		slot.ManualRotate = false
		slot.RotationFingerprintsJSON = "[]"
		s.clearActive(slot)
		slot.Status = "probe_error"
		slot.LastError = "Добавьте хотя бы один публичный HTTPS-адрес проверки."
		slot.CandidateCursor = 0
		slot.LastCheckedAt = s.clock().UnixMilli()
		slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	probeCandidate := func(ctx context.Context, candidate ExternalSubscriptionCandidate) error {
		return probeExternalVLESSCandidateSet(ctx, candidate, probeURLs, probe)
	}
	checkCtx, cancel := context.WithTimeout(ctx, ExternalSubscriptionProbeTimeout)
	defer cancel()
	var lastProbeError error

	if !slot.ManualRotate && slot.ActiveFingerprint != "" {
		if active := candidateByFingerprint(filtered, slot.ActiveFingerprint); active != nil {
			err := probeCandidate(checkCtx, *active)
			if errors.Is(err, ErrExternalSubscriptionProbeBusy) {
				slot.NextCheckAt = s.clock().Add(30 * time.Second).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			if err == nil {
				setSlotSelectedCandidates(slot, []ExternalSubscriptionCandidate{*active})
				slot.Status = "healthy"
				slot.LastError = ""
				slot.LastCheckedAt = s.clock().UnixMilli()
				slot.CandidateCursor = 0
				slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			lastProbeError = err
			activeIndex := indexCandidate(filtered, active.Fingerprint)
			s.clearActive(slot)
			slot.CandidateCursor = activeIndex + 1
		}
	}

	start := slot.CandidateCursor
	if start < 0 || start >= len(scanCandidates) {
		start = 0
	}
	end := start + ExternalSubscriptionMaxCandidatesPerRun
	if end > len(scanCandidates) {
		end = len(scanCandidates)
	}
	for index := start; index < end; index++ {
		if ctx.Err() != nil {
			slot.Status = "checking"
			slot.CandidateCursor = index
			slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
		probeCtx, probeCancel := context.WithTimeout(ctx, ExternalSubscriptionProbeTimeout)
		probeErr := probeCandidate(probeCtx, scanCandidates[index])
		probeCancel()
		if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
			slot.Status = "checking"
			slot.CandidateCursor = index
			slot.NextCheckAt = s.clock().Add(30 * time.Second).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
		if probeErr != nil {
			lastProbeError = probeErr
		}
		if probeErr == nil {
			setSlotSelectedCandidates(slot, []ExternalSubscriptionCandidate{scanCandidates[index]})
			slot.Status = "healthy"
			slot.LastError = ""
			slot.LastCheckedAt = s.clock().UnixMilli()
			slot.CandidateCursor = 0
			slot.ManualRotate = false
			slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
	}
	slot.LastCheckedAt = s.clock().UnixMilli()
	probeReason := externalProbeFailureSummary(lastProbeError)
	if slot.ManualRotate {
		if end < len(scanCandidates) {
			slot.LastError = "Следующая группа не прошла проверку; текущий узел сохранён, поиск продолжается. " + probeReason
			slot.Status = "checking"
			slot.CandidateCursor = end
			slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
		} else {
			slot.ManualRotate = false
			slot.CandidateCursor = 0
			slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
			if slot.ActiveFingerprint != "" && candidateByFingerprint(filtered, slot.ActiveFingerprint) != nil {
				slot.LastError = "Другой проверенный узел не найден; текущий оставлен. " + probeReason
				slot.Status = "healthy"
			} else {
				slot.LastError = fmt.Sprintf("Ни один из %d подходящих узлов не прошёл проверку. %s", len(filtered), probeReason)
				slot.Status = "no_healthy_nodes"
			}
		}
	} else if end < len(scanCandidates) {
		slot.LastError = "Текущая группа узлов не прошла проверку; поиск продолжается. " + probeReason
		slot.Status = "checking"
		slot.CandidateCursor = end
		slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
	} else {
		slot.LastError = fmt.Sprintf("Ни один из %d подходящих узлов не прошёл проверку. %s", len(filtered), probeReason)
		slot.Status = "no_healthy_nodes"
		slot.CandidateCursor = 0
		slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
	}
	_ = database.GetDB().Save(slot).Error
}

func (s *DynamicSubscriptionService) RefreshNow(ctx context.Context, id int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !externalSelectorInstalled() {
		return errors.New("External Selector не установлен")
	}
	if !externalSelectorEnabled() {
		return errors.New("External Selector остановлен")
	}
	if !externalSubscriptionRunMu.TryLock() {
		return ErrExternalSubscriptionProbeBusy
	}
	defer externalSubscriptionRunMu.Unlock()
	var slot model.ExternalSubscriptionSlot
	if err := database.GetDB().WithContext(ctx).First(&slot, id).Error; err != nil {
		return err
	}
	if !slot.Enabled {
		return errors.New("подмодуль отключён")
	}
	slot.NextFetchAt = 0
	slot.NextCheckAt = 0
	s.refreshSlot(ctx, &slot)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if slot.NextCheckAt == 0 || slot.NextCheckAt <= s.clock().UnixMilli() {
		s.checkSlot(ctx, &slot)
	}
	return nil
}

func normalizeExternalSlotInput(input ExternalSubscriptionSlotInput) (model.ExternalSubscriptionSlot, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" || len([]rune(name)) > 64 || strings.ContainsAny(name, "\r\n\x00") {
		return model.ExternalSubscriptionSlot{}, errors.New("нужно указать название подмодуля длиной до 64 символов")
	}
	sourceURL := strings.TrimSpace(input.SourceURL)
	if err := validateExternalHTTPSURL(sourceURL); err != nil {
		return model.ExternalSubscriptionSlot{}, errors.New("источник должен быть публичным HTTPS URL без логина и пароля")
	}
	if input.RefreshIntervalMinutes < 1 || input.RefreshIntervalMinutes > 1440 {
		return model.ExternalSubscriptionSlot{}, errors.New("интервал обновления источника должен быть от 1 до 1440 минут")
	}
	if input.CheckIntervalMinutes < 1 || input.CheckIntervalMinutes > 1440 {
		return model.ExternalSubscriptionSlot{}, errors.New("интервал проверки ключа должен быть от 1 до 1440 минут")
	}
	keyCount := input.SubscriptionKeyCount
	if keyCount == 0 {
		keyCount = 1
	}
	if keyCount < 1 || keyCount > ExternalSubscriptionMaxKeysPerSlot {
		return model.ExternalSubscriptionSlot{}, fmt.Errorf("количество ключей должно быть от 1 до %d", ExternalSubscriptionMaxKeysPerSlot)
	}
	if input.ForceRotationIntervalMinutes < 0 || input.ForceRotationIntervalMinutes > ExternalSubscriptionMaxForceRotationIntervalMinutes {
		return model.ExternalSubscriptionSlot{}, fmt.Errorf("интервал принудительной смены должен быть пустым или от 1 до %d минут", ExternalSubscriptionMaxForceRotationIntervalMinutes)
	}
	mode := strings.ToLower(strings.TrimSpace(input.FilterMode))
	if mode != "include" && mode != "exclude" {
		return model.ExternalSubscriptionSlot{}, errors.New("режим стран должен быть include или exclude")
	}
	selectionMode := strings.ToLower(strings.TrimSpace(input.SelectionMode))
	if selectionMode == "" {
		selectionMode = "auto"
	}
	if selectionMode != "auto" && selectionMode != "a-z" && selectionMode != "z-a" {
		return model.ExternalSubscriptionSlot{}, errors.New("состояние должно быть AUTO, A-Z или Z-A")
	}
	flags, err := normalizeCountryFlags(input.CountryFlags)
	if err != nil {
		return model.ExternalSubscriptionSlot{}, err
	}
	flagsJSON, _ := json.Marshal(flags)
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return model.ExternalSubscriptionSlot{
		Name:                         name,
		SourceURL:                    sourceURL,
		RefreshIntervalMinutes:       input.RefreshIntervalMinutes,
		CheckIntervalMinutes:         input.CheckIntervalMinutes,
		SubscriptionKeyCount:         keyCount,
		ForceRotationIntervalMinutes: input.ForceRotationIntervalMinutes,
		FilterMode:                   mode,
		SelectionMode:                selectionMode,
		CountryFlagsJSON:             string(flagsJSON),
		Enabled:                      enabled,
	}, nil
}

func validateExternalHTTPSURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !strings.EqualFold(u.Scheme, "https") || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Hostname() == "" {
		return errors.New("invalid URL")
	}
	if port := u.Port(); port != "" && port != "443" {
		return errors.New("HTTPS URL must use port 443")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return errors.New("non-public host")
	}
	if net.ParseIP(host) != nil && !externalPublicIP(host) {
		return errors.New("non-public host")
	}
	return nil
}

func normalizeCountryFlags(input []string) ([]string, error) {
	seen := make(map[string]struct{}, len(input))
	flags := make([]string, 0, len(input))
	for _, raw := range input {
		flag := strings.TrimSpace(raw)
		runes := []rune(flag)
		if len(runes) != 2 || firstCountryFlag(flag) != flag {
			return nil, errors.New("Укажите флаг страны одним эмодзи-символом, например 🇷🇺; код ru не подходит")
		}
		if _, exists := seen[flag]; exists {
			continue
		}
		seen[flag] = struct{}{}
		flags = append(flags, flag)
	}
	sort.Strings(flags)
	return flags, nil
}

func externalSlotView(slot *model.ExternalSubscriptionSlot) ExternalSubscriptionSlotView {
	flags := make([]string, 0)
	_ = json.Unmarshal([]byte(slot.CountryFlagsJSON), &flags)
	available := make(map[string]struct{})
	candidates := parseStoredCandidates(slot.CandidateDataJSON)
	for _, candidate := range candidates {
		if candidate.Flag != "" {
			available[candidate.Flag] = struct{}{}
		}
	}
	availableFlags := make([]string, 0, len(available))
	for flag := range available {
		availableFlags = append(availableFlags, flag)
	}
	sort.Strings(availableFlags)
	filtered := filterExternalCandidates(&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON}, candidates)
	selected := slotSelectedCandidates(slot, filtered)
	activeFlags := make([]string, 0, len(selected))
	for _, candidate := range selected {
		if candidate.Flag != "" {
			activeFlags = append(activeFlags, candidate.Flag)
		}
	}
	firstActiveFlag := ""
	if len(activeFlags) > 0 {
		firstActiveFlag = activeFlags[0]
	}
	keyCount := slot.SubscriptionKeyCount
	if keyCount < 1 {
		keyCount = 1
	}
	rotationTotal := slot.MatchingCount - len(selected)
	if rotationTotal < 0 {
		rotationTotal = 0
	}
	checkedCount := slot.CandidateCursor
	if slot.Status == "no_healthy_nodes" && !slot.ManualRotate {
		checkedCount = slot.MatchingCount
	}
	if slot.ManualRotate {
		checkedCount = len(slotRotationCandidates(slot, filtered))
		rotationTotal = keyCount
	}
	if checkedCount < 0 {
		checkedCount = 0
	}
	if !slot.ManualRotate && checkedCount > slot.MatchingCount {
		checkedCount = slot.MatchingCount
	}
	return ExternalSubscriptionSlotView{
		ID:                           slot.ID,
		Name:                         slot.Name,
		SourceURL:                    slot.SourceURL,
		RefreshIntervalMinutes:       slot.RefreshIntervalMinutes,
		CheckIntervalMinutes:         slot.CheckIntervalMinutes,
		SubscriptionKeyCount:         keyCount,
		ForceRotationIntervalMinutes: slot.ForceRotationIntervalMinutes,
		HealthyKeyCount:              len(selected),
		ActiveFlags:                  activeFlags,
		FilterMode:                   slot.FilterMode,
		SelectionMode:                slot.SelectionMode,
		CountryFlags:                 flags,
		AvailableFlags:               availableFlags,
		Enabled:                      slot.Enabled,
		// The source fragment is preserved inside ActiveURI for authorized
		// subscription output. Admin APIs receive only the country flag, never an
		// untrusted fragment that could itself contain credential material.
		ActiveName:      firstActiveFlag,
		ActiveFlag:      firstActiveFlag,
		Status:          slot.Status,
		LastError:       slot.LastError,
		CandidateCount:  slot.CandidateCount,
		MatchingCount:   slot.MatchingCount,
		CheckedCount:    checkedCount,
		RotationPending: slot.ManualRotate,
		RotationTotal:   rotationTotal,
		LastFetchedAt:   slot.LastFetchedAt,
		LastCheckedAt:   slot.LastCheckedAt,
	}
}

func parseStoredCandidates(raw string) []ExternalSubscriptionCandidate {
	var candidates []ExternalSubscriptionCandidate
	if raw == "" || json.Unmarshal([]byte(raw), &candidates) != nil {
		return nil
	}
	return candidates
}

func slotSelectedFingerprints(slot *model.ExternalSubscriptionSlot) []string {
	if slot == nil {
		return nil
	}
	var fingerprints []string
	_ = json.Unmarshal([]byte(slot.SelectedFingerprintsJSON), &fingerprints)
	if len(fingerprints) == 0 && slot.ActiveFingerprint != "" {
		fingerprints = append(fingerprints, slot.ActiveFingerprint)
	}
	seen := make(map[string]struct{}, len(fingerprints))
	unique := make([]string, 0, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint == "" {
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		unique = append(unique, fingerprint)
	}
	return unique
}

func slotSelectedCandidates(slot *model.ExternalSubscriptionSlot, candidates []ExternalSubscriptionCandidate) []ExternalSubscriptionCandidate {
	selected := make([]ExternalSubscriptionCandidate, 0)
	for _, fingerprint := range slotSelectedFingerprints(slot) {
		if candidate := candidateByFingerprint(candidates, fingerprint); candidate != nil {
			selected = append(selected, *candidate)
		}
	}
	return selected
}

func setSlotSelectedCandidates(slot *model.ExternalSubscriptionSlot, selected []ExternalSubscriptionCandidate) {
	fingerprints := make([]string, 0, len(selected))
	seen := make(map[string]struct{}, len(selected))
	for _, candidate := range selected {
		if candidate.Fingerprint == "" {
			continue
		}
		if _, exists := seen[candidate.Fingerprint]; exists {
			continue
		}
		seen[candidate.Fingerprint] = struct{}{}
		fingerprints = append(fingerprints, candidate.Fingerprint)
	}
	encoded, _ := json.Marshal(fingerprints)
	slot.SelectedFingerprintsJSON = string(encoded)
	slot.ActiveURI = ""
	slot.ActiveFingerprint = ""
	slot.ActiveName = ""
	slot.ActiveFlag = ""
	if len(selected) > 0 {
		slot.ActiveURI = selected[0].URI
		slot.ActiveFingerprint = selected[0].Fingerprint
		slot.ActiveName = selected[0].Name
		slot.ActiveFlag = selected[0].Flag
	}
}

func slotRotationCandidates(slot *model.ExternalSubscriptionSlot, candidates []ExternalSubscriptionCandidate) []ExternalSubscriptionCandidate {
	if slot == nil {
		return nil
	}
	var fingerprints []string
	_ = json.Unmarshal([]byte(slot.RotationFingerprintsJSON), &fingerprints)
	rotation := make([]ExternalSubscriptionCandidate, 0, len(fingerprints))
	seen := make(map[string]struct{}, len(fingerprints))
	for _, fingerprint := range fingerprints {
		if fingerprint == "" {
			continue
		}
		if _, exists := seen[fingerprint]; exists {
			continue
		}
		seen[fingerprint] = struct{}{}
		if candidate := candidateByFingerprint(candidates, fingerprint); candidate != nil {
			rotation = append(rotation, *candidate)
		}
	}
	return rotation
}

func setSlotRotationCandidates(slot *model.ExternalSubscriptionSlot, rotation []ExternalSubscriptionCandidate) {
	fingerprints := make([]string, 0, len(rotation))
	seen := make(map[string]struct{}, len(rotation))
	for _, candidate := range rotation {
		if candidate.Fingerprint == "" {
			continue
		}
		if _, exists := seen[candidate.Fingerprint]; exists {
			continue
		}
		seen[candidate.Fingerprint] = struct{}{}
		fingerprints = append(fingerprints, candidate.Fingerprint)
	}
	encoded, _ := json.Marshal(fingerprints)
	slot.RotationFingerprintsJSON = string(encoded)
}

func filterExternalCandidates(slot *slotCandidateConfig, candidates []ExternalSubscriptionCandidate) []ExternalSubscriptionCandidate {
	flags := make(map[string]struct{})
	if slot != nil {
		var parsed []string
		_ = json.Unmarshal([]byte(slot.FlagsJSON), &parsed)
		for _, flag := range parsed {
			flags[flag] = struct{}{}
		}
	}
	mode := "include"
	if slot != nil && slot.Mode != "" {
		mode = slot.Mode
	}
	filtered := make([]ExternalSubscriptionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		_, selected := flags[candidate.Flag]
		if mode == "include" && selected || mode == "exclude" && !selected {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func candidateByFingerprint(candidates []ExternalSubscriptionCandidate, fingerprint string) *ExternalSubscriptionCandidate {
	for i := range candidates {
		if candidates[i].Fingerprint == fingerprint {
			return &candidates[i]
		}
	}
	return nil
}

func indexCandidate(candidates []ExternalSubscriptionCandidate, fingerprint string) int {
	for i := range candidates {
		if candidates[i].Fingerprint == fingerprint {
			return i
		}
	}
	return -1
}

func (s *DynamicSubscriptionService) clearActive(slot *model.ExternalSubscriptionSlot) {
	slot.SelectedFingerprintsJSON = "[]"
	slot.ActiveURI = ""
	slot.ActiveFingerprint = ""
	slot.ActiveName = ""
	slot.ActiveFlag = ""
}

func fetchExternalSubscriptionSource(ctx context.Context, sourceURL, etag, modified string) (externalSourceResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateExternalHTTPSURL(sourceURL); err != nil {
		return externalSourceResult{}, errors.New("invalid external subscription URL")
	}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		IdleConnTimeout:       5 * time.Second,
		DisableKeepAlives:     true,
		DialContext:           dialPublicHTTPS,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 || validateExternalHTTPSURL(req.URL.String()) != nil {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sourceURL, nil)
	if err != nil {
		return externalSourceResult{}, errors.New("invalid external subscription request")
	}
	req.Header.Set("Accept", "text/plain, */*;q=0.1")
	req.Header.Set("User-Agent", "vpn-ui-external-subscription/1")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if modified != "" {
		req.Header.Set("If-Modified-Since", modified)
	}
	resp, err := client.Do(req)
	if err != nil {
		return externalSourceResult{}, errors.New("external subscription request failed")
	}
	defer resp.Body.Close()
	result := externalSourceResult{ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
	if resp.StatusCode == http.StatusNotModified {
		result.NotModified = true
		return result, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return externalSourceResult{}, fmt.Errorf("external source returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxExternalSubscriptionBytes+1))
	if err != nil || len(body) > maxExternalSubscriptionBytes {
		return externalSourceResult{}, errors.New("external source response is too large")
	}
	result.Body = body
	return result, nil
}

func dialPublicHTTPS(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" {
		return nil, errors.New("source target is not public HTTPS")
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return nil, errors.New("source host could not be resolved")
	}
	for _, candidate := range ips {
		if !externalPublicIP(candidate.IP.String()) {
			return nil, errors.New("source host resolved to a non-public address")
		}
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	var lastErr error
	for _, candidate := range ips {
		remote := net.JoinHostPort(candidate.IP.String(), port)
		conn, dialErr := dialer.DialContext(ctx, network, remote)
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr == nil {
		lastErr = errors.New("public endpoint unavailable")
	}
	return nil, lastErr
}

func resolvePublicExternalHost(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !externalPublicIP(ip.String()) {
			return "", errors.New("node endpoint is not public")
		}
		return ip.String(), nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(ips) == 0 {
		return "", errors.New("node endpoint could not be resolved")
	}
	for _, candidate := range ips {
		if !externalPublicIP(candidate.IP.String()) {
			return "", errors.New("node endpoint resolved to a non-public address")
		}
	}
	return ips[0].IP.String(), nil
}

func probeExternalVLESSCandidateSet(
	ctx context.Context,
	candidate ExternalSubscriptionCandidate,
	probeURLs []string,
	probe func(context.Context, ExternalSubscriptionCandidate, string) error,
) error {
	if len(probeURLs) == 0 || len(probeURLs) > ExternalSubscriptionMaxProbeURLs {
		return errors.New("probe URL count is outside the allowed range")
	}
	requiredSuccesses := ExternalSubscriptionProbeSuccessTarget
	if len(probeURLs) < requiredSuccesses {
		requiredSuccesses = len(probeURLs)
	}
	successes := 0
	var lastErr error
	for i, probeURL := range probeURLs {
		err := probe(ctx, candidate, probeURL)
		if errors.Is(err, ErrExternalSubscriptionProbeBusy) {
			return err
		}
		if err == nil {
			successes++
			if successes >= requiredSuccesses {
				return nil
			}
		} else {
			lastErr = err
		}
		remaining := len(probeURLs) - i - 1
		if successes+remaining < requiredSuccesses {
			break
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("candidate did not pass enough HTTPS probes")
}

func externalProbeFailureSummary(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "истёк тайм-аут проверки"
	}
	if err == nil {
		return "не удалось подтвердить HTTPS-передачу данных через VLESS"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "unsuccessful status"), strings.Contains(message, "http status"):
		return "HTTPS-сервер вернул ответ вне диапазона 2xx"
	case strings.Contains(message, "could not be resolved"), strings.Contains(message, "resolve"):
		return "не удалось разрешить адрес узла или проверки"
	case strings.Contains(message, "temporary xray probe"):
		return "временный процесс Xray не запустился"
	case strings.Contains(message, "probe did not become ready"):
		return "временный процесс Xray не открыл локальный порт проверки"
	case strings.Contains(message, "external http probe failed"), strings.Contains(message, "data-plane"):
		return "не прошёл HTTPS-запрос через VLESS"
	case strings.Contains(message, "not public"):
		return "адрес узла или проверки не является публичным"
	default:
		return "не удалось подтвердить HTTPS-передачу данных через VLESS"
	}
}

func probeExternalVLESSCandidate(ctx context.Context, candidate ExternalSubscriptionCandidate, probeURL string) error {
	if err := validateExternalHTTPSURL(probeURL); err != nil {
		return errors.New("invalid configured probe URL")
	}
	parsed, err := url.Parse(candidate.URI)
	if err != nil {
		return errors.New("invalid candidate")
	}
	probeAddress, err := resolvePublicExternalHost(ctx, parsed.Hostname())
	if err != nil {
		return err
	}
	outbound, err := ExternalVLESSOutboundJSON(candidate, probeAddress, "external-subscription-probe")
	if err != nil {
		return err
	}
	result, err := (&OutboundService{}).TestExternalOutbound(ctx, string(outbound), probeURL)
	if err != nil {
		return err
	}
	if !result.Success {
		if strings.Contains(strings.ToLower(result.Error), "another outbound test is already running") {
			return ErrExternalSubscriptionProbeBusy
		}
		if result.StatusCode > 0 {
			return fmt.Errorf("external HTTP probe returned status %d", result.StatusCode)
		}
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return errors.New("VLESS data-plane probe failed")
	}
	return nil
}
