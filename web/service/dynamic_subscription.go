package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

	"github.com/mhsanaei/3x-ui/v2/config"
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
	ExternalSubscriptionCandidateBatchDelay             = time.Minute
	ExternalSubscriptionSchedulerProbeBudget            = 1
)

var (
	ErrExternalSubscriptionProbeBusy     = errors.New("external subscription probe is busy")
	errExternalSubscriptionProbeDeferred = errors.New("external subscription probe deferred by scheduler budget")
	externalSubscriptionRunMu            sync.Mutex
	externalSubscriptionSchedulerCursor  int
)

// DynamicSubscriptionService manages remote lists and verified URI selections per slot.
// Credential-bearing URIs are returned only by subscription getters, called after the
// normal account/membership checks.
type DynamicSubscriptionService struct {
	fetchSource          func(context.Context, string, string, string) (externalSourceResult, error)
	probe                func(context.Context, ExternalSubscriptionCandidate, string) error
	now                  func() time.Time
	schedulerProbeBudget *int
}

type ExternalSubscriptionSettingsView struct {
	DisplayName string   `json:"displayName" form:"displayName"`
	ProbeURL    string   `json:"probeUrl" form:"probeUrl"` // Deprecated alias for ProbeURLs[0].
	ProbeURLs   []string `json:"probeUrls" form:"probeUrls"`
	Enabled     bool     `json:"enabled" form:"enabled"`
}

type ExternalSubscriptionSlotInput struct {
	Name                         string   `json:"name" form:"name"`
	ShowName                     *bool    `json:"showName" form:"showName"`
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
	ID       int    `json:"id"`
	Name     string `json:"name"`
	ShowName bool   `json:"showName"`
	// SourceURL is returned by the controller only to superadmins; source paths
	// and queries may contain bearer credentials.
	SourceURL                    string   `json:"sourceUrl,omitempty"`
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
			"replacement_candidates_json": "[]", "rotation_candidates_json": "[]",
			"rotation_fingerprints_json": "[]", "active_check_fingerprints_json": "[]",
			"active_check_failures_json": "[]", "active_check_cursor": 0,
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
	// Stop is a safety gate, not a mutation of the in-flight probe state. It must
	// remain available while a fetch/check holds externalSubscriptionRunMu: once
	// Enabled is false, subscription requests stop receiving these URIs, and a
	// later RestartModule resets the slots before they can be served again.
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
	// Wait for the current bounded fetch/probe to finish instead of surfacing a
	// transient "busy" error to an operator. StopModule remains non-blocking so
	// it can immediately gate subscription output during that same operation.
	if _, err := s.Settings(); err != nil {
		return err
	}
	externalSubscriptionRunMu.Lock()
	defer externalSubscriptionRunMu.Unlock()

	now := s.clock().UnixMilli()
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		settings := model.ExternalSubscriptionSettings{ID: ExternalSubscriptionSettingsID}
		if err := tx.First(&settings, ExternalSubscriptionSettingsID).Error; err != nil {
			return err
		}
		settings.Enabled = true
		if err := tx.Save(&settings).Error; err != nil {
			return err
		}
		return tx.Model(&model.ExternalSubscriptionSlot{}).
			Where("enabled = ?", true).
			Updates(map[string]any{
				"next_fetch_at": now, "next_check_at": now, "status": "checking",
				"candidate_cursor": 0, "manual_rotate": false, "last_error": "",
				"replacement_candidates_json": "[]", "rotation_candidates_json": "[]",
				"rotation_fingerprints_json": "[]", "active_check_fingerprints_json": "[]",
				"active_check_failures_json": "[]", "active_check_cursor": 0,
			}).Error
	})
}

// ModuleLogSummary reads the dedicated External Selector journal and appends a
// credential-free snapshot of current slots. Probe Xray output is written here,
// never to the shared Xray log.
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
	logPath := filepath.Join(config.GetLogFolder(), "external-selector.log")
	content, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	lines := []string{"External Selector: " + state}
	if strings.TrimSpace(string(content)) != "" {
		lines = append(lines, strings.TrimSpace(string(content)))
	} else {
		lines = append(lines, "Журнал пока пуст.")
	}
	lines = append(lines, "", "Текущее состояние подмодулей:")
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
	showName := row.ShowName
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
	if !showName {
		if err := db.Model(&row).UpdateColumn("show_name", false).Error; err != nil {
			return ExternalSubscriptionSlotView{}, err
		}
		row.ShowName = false
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
	if input.ShowName == nil {
		showName := row.ShowName
		input.ShowName = &showName
	}
	updated, err := normalizeExternalSlotInput(input)
	if err != nil {
		return ExternalSubscriptionSlotView{}, err
	}
	wasEnabled := row.Enabled
	changedFilter := row.FilterMode != updated.FilterMode || row.CountryFlagsJSON != updated.CountryFlagsJSON
	changedSource := row.SourceURL != updated.SourceURL
	changedKeyCount := row.SubscriptionKeyCount != updated.SubscriptionKeyCount
	changedForceRotation := row.ForceRotationIntervalMinutes != updated.ForceRotationIntervalMinutes
	row.Name = updated.Name
	row.ShowName = updated.ShowName
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
	if changedFilter {
		s.clearActive(&row)
		setSlotRotationCandidates(&row, nil)
		row.Status = "pending"
		row.CandidateCursor = 0
		row.ManualRotate = false
		row.NextFetchAt = now
		row.NextCheckAt = now
		row.LastError = ""
	} else {
		if changedSource {
			// Changing the source affects future candidates, not the already
			// verified active keys. Drop pending candidates from the old source.
			setSlotReplacementCandidates(&row, nil)
			setSlotRotationCandidates(&row, nil)
			clearSlotActiveCheck(&row)
			row.CandidateCursor = 0
			row.ManualRotate = false
			row.NextFetchAt = now
		}
		if changedKeyCount {
			setSlotRotationCandidates(&row, nil)
			setSlotReplacementCandidates(&row, nil)
			clearSlotActiveCheck(&row)
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
		} else if changedSource {
			row.Status = "checking"
			if len(slotSelectedCandidates(&row, nil)) == 0 {
				row.Status = "pending"
			}
			row.NextCheckAt = now
			row.LastError = ""
		}
	}
	if !row.Enabled {
		row.ManualRotate = false
		setSlotRotationCandidates(&row, nil)
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
	if !externalSelectorEnabled() {
		return errors.New("External Selector остановлен")
	}
	// Serialize the request with the running fetch/probe so its durable state
	// cannot be overwritten by a stale scheduler snapshot. The UI shows the
	// queued refresh state optimistically while this bounded operation finishes.
	externalSubscriptionRunMu.Lock()
	defer externalSubscriptionRunMu.Unlock()
	if !externalSelectorEnabled() {
		return errors.New("External Selector остановлен")
	}
	db := database.GetDB()
	var slot model.ExternalSubscriptionSlot
	if err := db.First(&slot, id).Error; err != nil {
		return err
	}
	if !slot.Enabled {
		return errors.New("подмодуль отключён")
	}
	now := s.clock().UnixMilli()
	return db.Model(&slot).Updates(map[string]any{
		"next_fetch_at": now, "next_check_at": now, "candidate_cursor": 0,
		"status": "refreshing_source", "last_error": "",
		"replacement_candidates_json": "[]", "active_check_fingerprints_json": "[]",
		"active_check_failures_json": "[]", "active_check_cursor": 0,
	}).Error
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
	selectedSet := externalCandidateIdentitySet(selected)
	hasReplacement := false
	for _, candidate := range candidates {
		if _, exists := selectedSet[externalCandidateIdentity(candidate)]; !exists {
			hasReplacement = true
			break
		}
	}
	if !hasReplacement {
		return errors.New("нет другого подходящего набора ключей для переключения")
	}
	now := s.clock().UnixMilli()
	slot.CandidateCursor = 0
	setSlotRotationCandidates(&slot, nil)
	setSlotReplacementCandidates(&slot, nil)
	clearSlotActiveCheck(&slot)
	slot.ManualRotate = true
	slot.Status = "checking"
	slot.LastError = "Ищется новый проверенный набор ключей; текущий набор останется до полной проверки."
	slot.NextCheckAt = now
	return database.GetDB().Save(&slot).Error
}

// ActiveURIForSubscription retains the legacy singular accessor for older callers.
func (s *DynamicSubscriptionService) ActiveURIForSubscription(slotID int) (string, bool) {
	uris, ok := s.ActiveURIsForSubscription(slotID)
	if !ok || len(uris) == 0 {
		return "", false
	}
	return uris[0], true
}

// ActiveURIsForSubscription returns only the configured active pool. Callers must
// invoke it after their usual subscription token, membership, enable and quota checks.
func (s *DynamicSubscriptionService) ActiveURIsForSubscription(slotID int) ([]string, bool) {
	uris, limit, ok := s.subscriptionURICandidates(slotID, false)
	if !ok || len(uris) == 0 {
		return nil, false
	}
	if len(uris) > limit {
		uris = uris[:limit]
	}
	return uris, len(uris) > 0
}

// SubscriptionURICandidatesForSubscription returns the active pool followed by
// previously verified replacement candidates, plus the slot's output limit. Renderers
// use the extra candidates only to replace duplicates in this subscriber's response;
// unprobed source-list candidates are never exposed. Call only after authorization.
func (s *DynamicSubscriptionService) SubscriptionURICandidatesForSubscription(slotID int) ([]string, int, bool) {
	return s.subscriptionURICandidates(slotID, true)
}

func (s *DynamicSubscriptionService) subscriptionURICandidates(slotID int, includeReplacements bool) ([]string, int, bool) {
	if slotID < 1 || !externalSelectorEnabled() {
		return nil, 0, false
	}
	var row model.ExternalSubscriptionSlot
	if err := database.GetDB().First(&row, slotID).Error; err != nil || !row.Enabled {
		return nil, 0, false
	}
	filtered := filterExternalCandidates(
		&slotCandidateConfig{Mode: row.FilterMode, FlagsJSON: row.CountryFlagsJSON},
		parseStoredCandidates(row.CandidateDataJSON),
	)
	active := slotSelectedCandidates(&row, filtered)
	if len(active) == 0 && row.ActiveURI != "" {
		active = []ExternalSubscriptionCandidate{{URI: row.ActiveURI}}
	}
	limit := row.SubscriptionKeyCount
	if limit < 1 {
		limit = 1
	}
	if limit > ExternalSubscriptionMaxKeysPerSlot {
		limit = ExternalSubscriptionMaxKeysPerSlot
	}
	if len(active) > limit {
		active = active[:limit]
	}

	// Keep the last verified pool available while refresh/check work is pending.
	// ReplacementCandidatesJSON contains only candidates which already passed the
	// configured HTTPS probe; CandidateDataJSON is deliberately never used as fallback.
	pool := append([]ExternalSubscriptionCandidate(nil), active...)
	if includeReplacements {
		pool = append(pool, slotReplacementCandidates(&row)...)
	}
	pool = uniqueExternalCandidates(pool)
	if len(pool) == 0 {
		return nil, limit, false
	}

	activeCount := min(len(uniqueExternalCandidates(active)), len(pool))
	order := func(candidates []ExternalSubscriptionCandidate) {
		switch strings.ToLower(strings.TrimSpace(row.SelectionMode)) {
		case "a-z":
			sort.SliceStable(candidates, func(i, j int) bool {
				return indexCandidate(filtered, candidates[i].Fingerprint) < indexCandidate(filtered, candidates[j].Fingerprint)
			})
		case "z-a":
			sort.SliceStable(candidates, func(i, j int) bool {
				return indexCandidate(filtered, candidates[i].Fingerprint) > indexCandidate(filtered, candidates[j].Fingerprint)
			})
		default: // AUTO, including rows created before this setting existed.
			rand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
		}
	}
	order(pool[:activeCount])
	order(pool[activeCount:])

	uris := make([]string, 0, len(pool))
	for _, candidate := range pool {
		if candidate.URI == "" {
			continue
		}
		uri := candidate.URI
		if row.ShowName {
			uri = prefixExternalSubscriptionURIName(uri, row.Name)
		}
		uris = append(uris, uri)
	}
	return uris, limit, len(uris) > 0
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
	if !externalSelectorEnabled() || !externalSubscriptionRunMu.TryLock() {
		return
	}
	defer externalSubscriptionRunMu.Unlock()
	if !externalSelectorEnabled() {
		return
	}
	budget := ExternalSubscriptionSchedulerProbeBudget
	s.schedulerProbeBudget = &budget
	defer func() { s.schedulerProbeBudget = nil }()

	var slots []model.ExternalSubscriptionSlot
	if err := database.GetDB().WithContext(ctx).Order("id ASC").Find(&slots).Error; err != nil || len(slots) == 0 {
		return
	}
	start := sort.Search(len(slots), func(i int) bool { return slots[i].ID > externalSubscriptionSchedulerCursor })
	if start == len(slots) {
		start = 0
	}
	for offset := 0; offset < len(slots); offset++ {
		if ctx.Err() != nil || !externalSelectorEnabled() {
			return
		}
		slot := &slots[(start+offset)%len(slots)]
		if !slot.Enabled {
			continue
		}
		now := s.clock().UnixMilli()
		if slot.NextFetchAt == 0 || slot.NextFetchAt <= now {
			// Source I/O is also limited to one slot per scheduler tick. A bad or
			// slow upstream can no longer delay every other slot in this pass.
			s.refreshSlot(ctx, slot)
			externalSubscriptionSchedulerCursor = slot.ID
			return
		}
		if slot.ForceRotationIntervalMinutes > 0 && (slot.NextForcedRotationAt == 0 || slot.NextForcedRotationAt <= now) {
			if !slot.ManualRotate {
				slot.ManualRotate = true
				setSlotRotationCandidates(slot, nil)
				setSlotReplacementCandidates(slot, nil)
				clearSlotActiveCheck(slot)
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
			if budget < ExternalSubscriptionSchedulerProbeBudget {
				externalSubscriptionSchedulerCursor = slot.ID
				return
			}
		}
	}
}

func (s *DynamicSubscriptionService) refreshSlot(ctx context.Context, slot *model.ExternalSubscriptionSlot) {
	previousStatus := slot.Status
	slot.Status = "refreshing_source"
	slot.LastError = ""
	_ = database.GetDB().Model(slot).Updates(map[string]any{
		"status": slot.Status, "last_error": "",
	}).Error
	fetch := s.fetchSource
	if fetch == nil {
		fetch = fetchExternalSubscriptionSource
	}
	result, err := fetch(ctx, slot.SourceURL, slot.SourceETag, slot.SourceLastModified)
	if err != nil {
		now := s.clock()
		slot.Status = "source_error"
		slot.LastError = "Не удалось загрузить источник"
		slot.NextFetchAt = now.Add(time.Duration(slot.RefreshIntervalMinutes) * time.Minute).UnixMilli()
		scheduleExternalSlotRetryCheck(slot, now)
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
		if previousStatus == "refreshing_source" {
			active := slotSelectedCandidates(slot, nil)
			if slot.MatchingCount == 0 && len(active) == 0 {
				slot.Status = "no_candidates"
				scheduleExternalSlotRetryCheck(slot, now)
			} else {
				slot.Status = "checking"
				slot.CandidateCursor = 0
				slot.NextCheckAt = now.UnixMilli()
			}
		} else if slot.ActiveURI == "" && slot.MatchingCount > 0 {
			slot.Status = "checking"
			slot.NextCheckAt = now.UnixMilli()
		} else {
			slot.Status = previousStatus
		}
		_ = database.GetDB().Save(slot).Error
		return
	}
	candidates, stats, err := ParseExternalVLESSList(result.Body)
	if err != nil {
		slot.SourceETag = ""
		slot.SourceLastModified = ""
		slot.Status = "source_error"
		slot.LastError = "Источник содержит слишком много данных или некорректный формат"
		scheduleExternalSlotRetryCheck(slot, now)
		_ = database.GetDB().Save(slot).Error
		return
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		slot.SourceETag = ""
		slot.SourceLastModified = ""
		slot.Status = "source_error"
		slot.LastError = "Не удалось сохранить разобранный источник"
		scheduleExternalSlotRetryCheck(slot, now)
		_ = database.GetDB().Save(slot).Error
		return
	}
	// A full successful response replaces both validators, including clearing
	// stale values when the source stops sending them.
	slot.SourceETag = result.ETag
	slot.SourceLastModified = result.LastModified
	manualRotationPending := slot.ManualRotate
	oldCandidates := filterExternalCandidates(
		&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON},
		parseStoredCandidates(slot.CandidateDataJSON),
	)
	selected := slotSelectedCandidates(slot, oldCandidates)
	rotation := slotRotationCandidates(slot, oldCandidates)
	filtered := filterExternalCandidates(&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON}, candidates)
	slot.CandidateDataJSON = string(encoded)
	// Replacement candidates are tied to the previously fetched source list. Keep
	// the active pool available during refresh, but rebuild verified reserves from
	// the newly parsed source before serving them as fallbacks.
	setSlotReplacementCandidates(slot, nil)
	slot.CandidateCount = stats.Accepted
	slot.MatchingCount = len(filtered)
	slot.CandidateCursor = 0
	if len(selected) > slot.SubscriptionKeyCount && slot.SubscriptionKeyCount > 0 {
		selected = selected[:slot.SubscriptionKeyCount]
	}
	setSlotSelectedCandidates(slot, selected)
	if manualRotationPending {
		setSlotRotationCandidates(slot, rotation)
	} else {
		setSlotRotationCandidates(slot, nil)
	}
	slot.ManualRotate = manualRotationPending
	if len(filtered) == 0 && len(selected) == 0 && len(slotReplacementCandidates(slot)) == 0 && !slot.ManualRotate {
		slot.Status = "no_candidates"
		slot.LastError = ""
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
	} else {
		// Keep the already verified active pool available while checks resume. The
		// replacement reserve was cleared above and will be rebuilt from this source.
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

func scheduleExternalSlotRetryCheck(slot *model.ExternalSubscriptionSlot, now time.Time) {
	delay := time.Duration(slot.CheckIntervalMinutes) * time.Minute
	if delay <= 0 {
		delay = ExternalSubscriptionCandidateBatchDelay
	}
	slot.NextCheckAt = now.Add(delay).UnixMilli()
}

func (s *DynamicSubscriptionService) checkSlot(ctx context.Context, slot *model.ExternalSubscriptionSlot) {
	s.checkSlotMultiple(ctx, slot)
}

func (s *DynamicSubscriptionService) schedulerBudgetExhausted() bool {
	return s.schedulerProbeBudget != nil && *s.schedulerProbeBudget <= 0
}

func (s *DynamicSubscriptionService) probeCandidateSet(
	ctx context.Context,
	candidate ExternalSubscriptionCandidate,
	probeURLs []string,
	probe func(context.Context, ExternalSubscriptionCandidate, string) error,
) error {
	if s.schedulerProbeBudget != nil {
		if *s.schedulerProbeBudget <= 0 {
			return errExternalSubscriptionProbeDeferred
		}
		(*s.schedulerProbeBudget)--
	}
	return probeExternalVLESSCandidateSet(ctx, candidate, probeURLs, probe)
}

func (s *DynamicSubscriptionService) checkSlotPersistentPool(
	ctx context.Context,
	slot *model.ExternalSubscriptionSlot,
	filtered []ExternalSubscriptionCandidate,
	now time.Time,
) {
	keyCount := slot.SubscriptionKeyCount
	if keyCount < 1 {
		keyCount = 1
	}
	if keyCount > ExternalSubscriptionMaxKeysPerSlot {
		keyCount = ExternalSubscriptionMaxKeysPerSlot
	}
	slot.SubscriptionKeyCount = keyCount
	active := slotSelectedCandidates(slot, filtered)
	if len(active) > keyCount {
		active = active[:keyCount]
	}
	setSlotSelectedCandidates(slot, active)
	activeSet := externalCandidateIdentitySet(active)
	replacements := make([]ExternalSubscriptionCandidate, 0)
	for _, candidate := range slotReplacementCandidates(slot) {
		if _, duplicate := activeSet[externalCandidateIdentity(candidate)]; duplicate {
			continue
		}
		replacements = append(replacements, candidate)
	}
	setSlotReplacementCandidates(slot, replacements)

	settings, err := s.settingsRow()
	if err != nil {
		s.deferSlotCheck(slot, now, "probe_error", "Не удалось прочитать настройки проверки", time.Duration(slot.CheckIntervalMinutes)*time.Minute)
		return
	}
	probe := s.probe
	if probe == nil {
		probe = probeExternalVLESSCandidate
	}
	probeURLs := externalSubscriptionProbeURLs(settings)
	if len(probeURLs) == 0 {
		s.deferSlotCheck(slot, now, "probe_error", "Добавьте хотя бы один публичный HTTPS-адрес проверки.", time.Duration(slot.CheckIntervalMinutes)*time.Minute)
		return
	}
	probeCandidate := func(candidate ExternalSubscriptionCandidate) error {
		probeCtx, cancel := context.WithTimeout(ctx, ExternalSubscriptionProbeTimeout)
		defer cancel()
		return s.probeCandidateSet(probeCtx, candidate, probeURLs, probe)
	}

	checkFingerprints, _ := slotActiveCheckState(slot)
	if len(checkFingerprints) > 0 {
		s.runActivePoolCheck(ctx, slot, active, replacements, probeCandidate, now)
		return
	}

	if len(active) < keyCount {
		s.fillInitialPool(ctx, slot, filtered, active, replacements, keyCount, probeCandidate, now)
		return
	}

	s.scanReplacementAndCheckPool(ctx, slot, filtered, active, replacements, probeCandidate, now)
}

func (s *DynamicSubscriptionService) deferSlotCheck(slot *model.ExternalSubscriptionSlot, now time.Time, status, message string, delay time.Duration) {
	if delay <= 0 {
		delay = time.Duration(slot.CheckIntervalMinutes) * time.Minute
	}
	slot.Status = status
	slot.LastError = message
	slot.NextCheckAt = now.Add(delay).UnixMilli()
	_ = database.GetDB().Save(slot).Error
}

func (s *DynamicSubscriptionService) fillInitialPool(
	ctx context.Context,
	slot *model.ExternalSubscriptionSlot,
	filtered, active, replacements []ExternalSubscriptionCandidate,
	keyCount int,
	probeCandidate func(ExternalSubscriptionCandidate) error,
	now time.Time,
) {
	pool := uniqueExternalCandidates(append(append([]ExternalSubscriptionCandidate(nil), active...), replacements...))
	active = pool
	if len(active) > keyCount {
		active = pool[:keyCount]
		setSlotReplacementCandidates(slot, pool[keyCount:])
	} else {
		setSlotReplacementCandidates(slot, nil)
	}
	setSlotSelectedCandidates(slot, active)
	if len(active) >= keyCount {
		slot.Status = "healthy"
		slot.LastError = ""
		slot.LastCheckedAt = now.UnixMilli()
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}

	activeSet := externalCandidateIdentitySet(active)
	index := slot.CandidateCursor
	if index < 0 || index >= len(filtered) {
		index = 0
	}
	attempted := 0
	var lastProbeError error
	for index < len(filtered) && len(active) < keyCount && attempted < ExternalSubscriptionMaxCandidatesPerRun {
		if ctx.Err() != nil {
			slot.CandidateCursor = index
			slot.Status = "checking"
			slot.LastError = fmt.Sprintf("Выбрано %d из %d ключей; поиск продолжится.", len(active), keyCount)
			slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
			setSlotSelectedCandidates(slot, active)
			_ = database.GetDB().Save(slot).Error
			return
		}
		candidateIndex := index
		candidate := filtered[index]
		index++
		identity := externalCandidateIdentity(candidate)
		if _, exists := activeSet[identity]; exists {
			continue
		}
		attempted++
		probeErr := probeCandidate(candidate)
		if !externalSelectorEnabled() {
			return
		}
		if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) || errors.Is(probeErr, errExternalSubscriptionProbeDeferred) {
			slot.CandidateCursor = candidateIndex
			slot.Status = "checking"
			slot.LastError = fmt.Sprintf("Выбрано %d из %d ключей; поиск продолжится.", len(active), keyCount)
			delay := ExternalSubscriptionCandidateBatchDelay
			if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
				delay = 30 * time.Second
			}
			slot.NextCheckAt = now.Add(delay).UnixMilli()
			setSlotSelectedCandidates(slot, active)
			_ = database.GetDB().Save(slot).Error
			return
		}
		if ctx.Err() != nil {
			slot.CandidateCursor = candidateIndex
			slot.Status = "checking"
			slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
			setSlotSelectedCandidates(slot, active)
			_ = database.GetDB().Save(slot).Error
			return
		}
		if probeErr != nil {
			lastProbeError = probeErr
		} else {
			active = append(active, candidate)
			activeSet[identity] = struct{}{}
			setSlotSelectedCandidates(slot, active)
			// Each successful initial key is durable and immediately available.
			if err := database.GetDB().Save(slot).Error; err != nil {
				return
			}
		}
		if s.schedulerBudgetExhausted() {
			break
		}
	}

	if len(active) >= keyCount {
		slot.CandidateCursor = index
		if slot.CandidateCursor >= len(filtered) {
			slot.CandidateCursor = 0
		}
		slot.Status = "healthy"
		slot.LastError = ""
		slot.LastCheckedAt = now.UnixMilli()
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
	} else if index < len(filtered) {
		slot.Status = "checking"
		slot.CandidateCursor = index
		slot.LastError = fmt.Sprintf("Выбрано %d из %d ключей; поиск продолжается. %s", len(active), keyCount, externalProbeFailureSummary(lastProbeError))
		slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
	} else {
		slot.CandidateCursor = 0
		slot.LastCheckedAt = now.UnixMilli()
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		if len(active) > 0 {
			slot.Status = "partial"
			slot.LastError = fmt.Sprintf("Проверены все кандидаты; здоровых ключей %d из %d. %s", len(active), keyCount, externalProbeFailureSummary(lastProbeError))
		} else if len(filtered) == 0 {
			slot.Status = "no_candidates"
			slot.LastError = ""
		} else {
			slot.Status = "no_healthy_nodes"
			slot.LastError = fmt.Sprintf("Ни один из %d подходящих узлов не прошёл проверку. %s", len(filtered), externalProbeFailureSummary(lastProbeError))
		}
	}
	setSlotSelectedCandidates(slot, active)
	_ = database.GetDB().Save(slot).Error
}

func (s *DynamicSubscriptionService) scanReplacementAndCheckPool(
	ctx context.Context,
	slot *model.ExternalSubscriptionSlot,
	filtered, active, replacements []ExternalSubscriptionCandidate,
	probeCandidate func(ExternalSubscriptionCandidate) error,
	now time.Time,
) {
	reserveTarget := slot.SubscriptionKeyCount
	if reserveTarget < 1 {
		reserveTarget = 1
	}
	if reserveTarget > ExternalSubscriptionMaxKeysPerSlot {
		reserveTarget = ExternalSubscriptionMaxKeysPerSlot
	}
	if len(replacements) < reserveTarget {
		knownSet := externalCandidateIdentitySet(append(append([]ExternalSubscriptionCandidate(nil), active...), replacements...))
		index := slot.CandidateCursor
		if index < 0 || index >= len(filtered) {
			index = 0
		}
		attempted := 0
		for index < len(filtered) && len(replacements) < reserveTarget && attempted < ExternalSubscriptionMaxCandidatesPerRun {
			if ctx.Err() != nil {
				slot.CandidateCursor = index
				slot.Status = "checking"
				slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			candidateIndex := index
			candidate := filtered[index]
			index++
			identity := externalCandidateIdentity(candidate)
			if _, exists := knownSet[identity]; exists {
				continue
			}
			attempted++
			probeErr := probeCandidate(candidate)
			if !externalSelectorEnabled() {
				return
			}
			if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) || errors.Is(probeErr, errExternalSubscriptionProbeDeferred) {
				slot.CandidateCursor = candidateIndex
				slot.Status = "checking"
				delay := ExternalSubscriptionCandidateBatchDelay
				if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
					delay = 30 * time.Second
				}
				slot.NextCheckAt = now.Add(delay).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			if ctx.Err() != nil {
				slot.CandidateCursor = candidateIndex
				slot.Status = "checking"
				slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			if probeErr == nil {
				replacements = append(replacements, candidate)
				knownSet[identity] = struct{}{}
				setSlotReplacementCandidates(slot, replacements)
				if err := database.GetDB().Save(slot).Error; err != nil {
					return
				}
			}
			slot.CandidateCursor = index
			if index >= len(filtered) {
				slot.CandidateCursor = 0
			}
			if s.schedulerBudgetExhausted() {
				break
			}
		}
		if len(replacements) > 0 {
			setSlotReplacementCandidates(slot, replacements)
		} else if index >= len(filtered) {
			slot.CandidateCursor = 0
		}
	}

	checkCandidates := append(append([]ExternalSubscriptionCandidate(nil), active...), replacements...)
	fingerprints := candidateFingerprints(checkCandidates)
	setSlotActiveCheckState(slot, fingerprints, nil, 0)
	slot.Status = "checking"
	setSlotSelectedCandidates(slot, active)
	_ = database.GetDB().Save(slot).Error
	s.runActivePoolCheck(ctx, slot, active, replacements, probeCandidate, now)
}

func (s *DynamicSubscriptionService) runActivePoolCheck(
	ctx context.Context,
	slot *model.ExternalSubscriptionSlot,
	active, replacements []ExternalSubscriptionCandidate,
	probeCandidate func(ExternalSubscriptionCandidate) error,
	now time.Time,
) {
	checkCandidates := uniqueExternalCandidates(append(append([]ExternalSubscriptionCandidate(nil), active...), replacements...))
	expectedFingerprints := candidateFingerprints(checkCandidates)
	fingerprints, failures := slotActiveCheckState(slot)
	if !equalStringSlices(fingerprints, expectedFingerprints) {
		fingerprints = expectedFingerprints
		failures = nil
		setSlotActiveCheckState(slot, fingerprints, failures, 0)
	}
	activeByFingerprint := make(map[string]ExternalSubscriptionCandidate, len(checkCandidates))
	for _, candidate := range checkCandidates {
		activeByFingerprint[candidate.Fingerprint] = candidate
	}
	cursor := slot.ActiveCheckCursor
	for cursor < len(fingerprints) {
		if ctx.Err() != nil {
			slot.Status = "checking"
			slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
			setSlotActiveCheckState(slot, fingerprints, failures, cursor)
			_ = database.GetDB().Save(slot).Error
			return
		}
		fingerprint := fingerprints[cursor]
		candidate, exists := activeByFingerprint[fingerprint]
		if exists {
			probeErr := probeCandidate(candidate)
			if !externalSelectorEnabled() {
				return
			}
			if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) || errors.Is(probeErr, errExternalSubscriptionProbeDeferred) {
				setSlotActiveCheckState(slot, fingerprints, failures, cursor)
				slot.Status = "checking"
				delay := ExternalSubscriptionCandidateBatchDelay
				if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
					delay = 30 * time.Second
				}
				slot.NextCheckAt = now.Add(delay).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			if ctx.Err() != nil {
				setSlotActiveCheckState(slot, fingerprints, failures, cursor)
				slot.Status = "checking"
				slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
				_ = database.GetDB().Save(slot).Error
				return
			}
			if probeErr != nil {
				failures = append(failures, fingerprint)
			}
		}
		cursor++
		setSlotActiveCheckState(slot, fingerprints, failures, cursor)
		if err := database.GetDB().Save(slot).Error; err != nil {
			return
		}
		if s.schedulerBudgetExhausted() && cursor < len(fingerprints) {
			slot.Status = "checking"
			slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
	}

	failedSet := make(map[string]struct{}, len(failures))
	for _, fingerprint := range failures {
		failedSet[fingerprint] = struct{}{}
	}
	keyCount := slot.SubscriptionKeyCount
	if keyCount < 1 {
		keyCount = 1
	}
	if keyCount > ExternalSubscriptionMaxKeysPerSlot {
		keyCount = ExternalSubscriptionMaxKeysPerSlot
	}
	healthy := make([]ExternalSubscriptionCandidate, 0, keyCount)
	for _, candidate := range active {
		if _, failed := failedSet[candidate.Fingerprint]; !failed {
			healthy = append(healthy, candidate)
		}
	}
	healthy = uniqueExternalCandidates(healthy)
	known := externalCandidateIdentitySet(healthy)
	healthyReserve := make([]ExternalSubscriptionCandidate, 0, keyCount)
	for _, replacement := range replacements {
		if _, failed := failedSet[replacement.Fingerprint]; failed {
			continue
		}
		identity := externalCandidateIdentity(replacement)
		if _, duplicate := known[identity]; duplicate {
			continue
		}
		known[identity] = struct{}{}
		if len(healthy) < keyCount {
			healthy = append(healthy, replacement)
			continue
		}
		if len(healthyReserve) < keyCount {
			healthyReserve = append(healthyReserve, replacement)
		}
	}
	setSlotSelectedCandidates(slot, healthy)
	setSlotReplacementCandidates(slot, healthyReserve)
	clearSlotActiveCheck(slot)
	slot.LastCheckedAt = now.UnixMilli()
	if len(healthy) >= keyCount {
		slot.Status = "healthy"
		slot.LastError = ""
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
	} else if len(healthy) > 0 {
		slot.Status = "partial"
		slot.LastError = fmt.Sprintf("Недоступные ключи удалены; в пуле %d из %d. Поиск замены продолжается.", len(healthy), keyCount)
		slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
	} else {
		slot.Status = "no_healthy_nodes"
		slot.LastError = "Все активные ключи недоступны; поиск замены продолжается."
		slot.NextCheckAt = now.Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
	}
	_ = database.GetDB().Save(slot).Error
}

func (s *DynamicSubscriptionService) checkSlotMultiple(ctx context.Context, slot *model.ExternalSubscriptionSlot) {
	candidates := parseStoredCandidates(slot.CandidateDataJSON)
	filtered := filterExternalCandidates(&slotCandidateConfig{Mode: slot.FilterMode, FlagsJSON: slot.CountryFlagsJSON}, candidates)
	slot.MatchingCount = len(filtered)
	now := s.clock()
	if !slot.ManualRotate {
		s.checkSlotPersistentPool(ctx, slot, filtered, now)
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
		slot.Status = "probe_error"
		slot.LastError = "Добавьте хотя бы один публичный HTTPS-адрес проверки."
		slot.NextCheckAt = now.Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
		_ = database.GetDB().Save(slot).Error
		return
	}
	probeCandidate := func(candidate ExternalSubscriptionCandidate) error {
		probeCtx, cancel := context.WithTimeout(ctx, ExternalSubscriptionProbeTimeout)
		defer cancel()
		return s.probeCandidateSet(probeCtx, candidate, probeURLs, probe)
	}

	s.checkSlotFullRotation(ctx, slot, filtered, selected, keyCount, probeCandidate)
	return

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
	currentSet := externalCandidateIdentitySet(current)
	rotation := slotRotationCandidates(slot, filtered)
	rotationSet := externalCandidateIdentitySet(rotation)
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
		identity := externalCandidateIdentity(candidate)
		if _, exists := currentSet[identity]; exists {
			continue
		}
		if _, exists := rotationSet[identity]; exists {
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
		if !externalSelectorEnabled() {
			return
		}
		if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) || errors.Is(probeErr, errExternalSubscriptionProbeDeferred) {
			slot.CandidateCursor = candidateIndex
			slot.Status = "checking"
			slot.LastError = fmt.Sprintf("Проверено %d из %d ключей нового набора; проверка продолжится.", len(rotation), keyCount)
			delay := ExternalSubscriptionCandidateBatchDelay
			if errors.Is(probeErr, ErrExternalSubscriptionProbeBusy) {
				delay = 30 * time.Second
			}
			slot.NextCheckAt = now.Add(delay).UnixMilli()
			setSlotRotationCandidates(slot, rotation)
			_ = database.GetDB().Save(slot).Error
			return
		}
		if probeErr != nil {
			lastProbeError = probeErr
			if s.schedulerBudgetExhausted() {
				break
			}
			continue
		}
		rotation = append(rotation, candidate)
		rotationSet[identity] = struct{}{}
		if len(rotation) >= keyCount {
			setSlotSelectedCandidates(slot, rotation[:keyCount])
			setSlotRotationCandidates(slot, nil)
			setSlotReplacementCandidates(slot, nil)
			clearSlotActiveCheck(slot)
			slot.ManualRotate = false
			slot.CandidateCursor = 0
			slot.Status = "healthy"
			slot.LastError = ""
			slot.LastCheckedAt = s.clock().UnixMilli()
			slot.NextCheckAt = s.clock().Add(time.Duration(slot.CheckIntervalMinutes) * time.Minute).UnixMilli()
			_ = database.GetDB().Save(slot).Error
			return
		}
		if s.schedulerBudgetExhausted() {
			setSlotRotationCandidates(slot, rotation)
			slot.CandidateCursor = index
			slot.Status = "checking"
			slot.LastError = fmt.Sprintf("Проверено %d из %d ключей нового набора; поиск продолжается.", len(rotation), keyCount)
			slot.NextCheckAt = s.clock().Add(ExternalSubscriptionCandidateBatchDelay).UnixMilli()
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
		setSlotRotationCandidates(slot, nil)
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
	budget := ExternalSubscriptionSchedulerProbeBudget
	s.schedulerProbeBudget = &budget
	defer func() { s.schedulerProbeBudget = nil }()
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
	showName := true
	if input.ShowName != nil {
		showName = *input.ShowName
	}
	return model.ExternalSubscriptionSlot{
		Name:                         name,
		ShowName:                     showName,
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
		ShowName:                     slot.ShowName,
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
	if slot == nil {
		return nil
	}
	stored := parseStoredCandidates(slot.ActiveCandidatesJSON)
	if len(stored) > 0 {
		return uniqueExternalCandidates(stored)
	}
	selected := make([]ExternalSubscriptionCandidate, 0)
	for _, fingerprint := range slotSelectedFingerprints(slot) {
		if candidate := candidateByFingerprint(candidates, fingerprint); candidate != nil {
			selected = append(selected, *candidate)
		}
	}
	if len(selected) == 0 && slot.ActiveURI != "" {
		candidate := ExternalSubscriptionCandidate{
			URI: slot.ActiveURI, Fingerprint: slot.ActiveFingerprint,
			Name: slot.ActiveName, Flag: slot.ActiveFlag,
		}
		if candidate.Fingerprint == "" {
			if parsed, err := ParseExternalVLESSURI(candidate.URI); err == nil {
				candidate.Fingerprint = parsed.Fingerprint
				if candidate.Name == "" {
					candidate.Name = parsed.Name
				}
				if candidate.Flag == "" {
					candidate.Flag = parsed.Flag
				}
			}
		}
		selected = append(selected, candidate)
	}
	return uniqueExternalCandidates(selected)
}

func setSlotSelectedCandidates(slot *model.ExternalSubscriptionSlot, selected []ExternalSubscriptionCandidate) {
	selected = uniqueExternalCandidates(selected)
	fingerprints := candidateFingerprints(selected)
	encodedFingerprints, _ := json.Marshal(fingerprints)
	encodedCandidates, _ := json.Marshal(selected)
	slot.SelectedFingerprintsJSON = string(encodedFingerprints)
	slot.ActiveCandidatesJSON = string(encodedCandidates)
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

func externalCandidateIdentity(candidate ExternalSubscriptionCandidate) string {
	if candidate.URI == "" {
		return ""
	}
	return ExternalSubscriptionURIDedupKey(candidate.URI)
}

func externalCandidateIdentitySet(candidates []ExternalSubscriptionCandidate) map[string]struct{} {
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if identity := externalCandidateIdentity(candidate); identity != "" {
			seen[identity] = struct{}{}
		}
	}
	return seen
}

func uniqueExternalCandidates(candidates []ExternalSubscriptionCandidate) []ExternalSubscriptionCandidate {
	seen := make(map[string]struct{}, len(candidates))
	unique := make([]ExternalSubscriptionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Fingerprint == "" || candidate.URI == "" {
			continue
		}
		identity := externalCandidateIdentity(candidate)
		if identity == "" {
			continue
		}
		if _, exists := seen[identity]; exists {
			continue
		}
		seen[identity] = struct{}{}
		unique = append(unique, candidate)
	}
	return unique
}

func candidateFingerprints(candidates []ExternalSubscriptionCandidate) []string {
	fingerprints := make([]string, 0, len(candidates))
	for _, candidate := range uniqueExternalCandidates(candidates) {
		fingerprints = append(fingerprints, candidate.Fingerprint)
	}
	return fingerprints
}

func slotReplacementCandidates(slot *model.ExternalSubscriptionSlot) []ExternalSubscriptionCandidate {
	if slot == nil {
		return nil
	}
	return uniqueExternalCandidates(parseStoredCandidates(slot.ReplacementCandidatesJSON))
}

func setSlotReplacementCandidates(slot *model.ExternalSubscriptionSlot, candidates []ExternalSubscriptionCandidate) {
	candidates = uniqueExternalCandidates(candidates)
	encoded, _ := json.Marshal(candidates)
	slot.ReplacementCandidatesJSON = string(encoded)
}

func slotActiveCheckState(slot *model.ExternalSubscriptionSlot) (fingerprints, failures []string) {
	if slot == nil {
		return nil, nil
	}
	_ = json.Unmarshal([]byte(slot.ActiveCheckFingerprintsJSON), &fingerprints)
	_ = json.Unmarshal([]byte(slot.ActiveCheckFailuresJSON), &failures)
	return uniqueStrings(fingerprints), uniqueStrings(failures)
}

func setSlotActiveCheckState(slot *model.ExternalSubscriptionSlot, fingerprints, failures []string, cursor int) {
	fingerprints = uniqueStrings(fingerprints)
	failures = uniqueStrings(failures)
	encodedFingerprints, _ := json.Marshal(fingerprints)
	encodedFailures, _ := json.Marshal(failures)
	slot.ActiveCheckFingerprintsJSON = string(encodedFingerprints)
	slot.ActiveCheckFailuresJSON = string(encodedFailures)
	if cursor < 0 || cursor > len(fingerprints) {
		cursor = len(fingerprints)
	}
	slot.ActiveCheckCursor = cursor
}

func clearSlotActiveCheck(slot *model.ExternalSubscriptionSlot) {
	if slot == nil {
		return
	}
	slot.ActiveCheckFingerprintsJSON = "[]"
	slot.ActiveCheckFailuresJSON = "[]"
	slot.ActiveCheckCursor = 0
}

func equalStringSlices(a, b []string) bool {
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

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func slotRotationCandidates(slot *model.ExternalSubscriptionSlot, candidates []ExternalSubscriptionCandidate) []ExternalSubscriptionCandidate {
	if slot == nil {
		return nil
	}
	stored := parseStoredCandidates(slot.RotationCandidatesJSON)
	if len(stored) > 0 {
		return uniqueExternalCandidates(stored)
	}
	var fingerprints []string
	_ = json.Unmarshal([]byte(slot.RotationFingerprintsJSON), &fingerprints)
	rotation := make([]ExternalSubscriptionCandidate, 0, len(fingerprints))
	for _, fingerprint := range uniqueStrings(fingerprints) {
		if candidate := candidateByFingerprint(candidates, fingerprint); candidate != nil {
			rotation = append(rotation, *candidate)
		}
	}
	return rotation
}

func setSlotRotationCandidates(slot *model.ExternalSubscriptionSlot, rotation []ExternalSubscriptionCandidate) {
	rotation = uniqueExternalCandidates(rotation)
	fingerprints := candidateFingerprints(rotation)
	encodedFingerprints, _ := json.Marshal(fingerprints)
	encodedCandidates, _ := json.Marshal(rotation)
	slot.RotationFingerprintsJSON = string(encodedFingerprints)
	slot.RotationCandidatesJSON = string(encodedCandidates)
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
	noCountryFilter := len(flags) == 0
	filtered := make([]ExternalSubscriptionCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		_, selected := flags[candidate.Flag]
		if noCountryFilter || mode == "include" && selected || mode == "exclude" && !selected {
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
	slot.ActiveCandidatesJSON = "[]"
	slot.ActiveURI = ""
	slot.ActiveFingerprint = ""
	slot.ActiveName = ""
	slot.ActiveFlag = ""
	setSlotReplacementCandidates(slot, nil)
	clearSlotActiveCheck(slot)
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
