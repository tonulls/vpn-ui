package service

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/xray"

	"gorm.io/gorm"
)

const (
	perInboundQuotaMB = int64(1024 * 1024)
	perInboundQuotaGB = 1024 * perInboundQuotaMB
	perInboundQuotaTB = 1024 * perInboundQuotaGB
)

// MembershipTrafficQuota — рассчитанное состояние квоты одного аккаунта на одном inbound.
type MembershipTrafficQuota struct {
	Enabled        bool
	Exhausted      bool
	LimitBytes     int64
	UsedBytes      int64
	RemainingBytes int64
	Unit           string
}

// TrafficQuotaUnitBytes возвращает размер выбранной двоичной единицы в байтах.
func TrafficQuotaUnitBytes(unit string) (int64, bool) {
	switch strings.ToUpper(strings.TrimSpace(unit)) {
	case "MB":
		return perInboundQuotaMB, true
	case "GB":
		return perInboundQuotaGB, true
	case "TB":
		return perInboundQuotaTB, true
	default:
		return 0, false
	}
}

// ResolveMembershipTrafficQuota считает остаток лимита, не смешивая его с общей квотой аккаунта.
func ResolveMembershipTrafficQuota(inbound *model.Inbound, membership *model.AccountInbound) MembershipTrafficQuota {
	result := MembershipTrafficQuota{}
	if inbound == nil || membership == nil || !inbound.PerUserTrafficLimitEnable || inbound.PerUserTrafficLimitBytes <= 0 {
		return result
	}
	if _, ok := TrafficQuotaUnitBytes(inbound.PerUserTrafficLimitUnit); !ok {
		return result
	}

	used := membership.QuotaUsedBytes
	if used < 0 {
		used = 0
	}
	remaining := inbound.PerUserTrafficLimitBytes - used
	if remaining < 0 {
		remaining = 0
	}
	result.Enabled = true
	result.Exhausted = used >= inbound.PerUserTrafficLimitBytes
	result.LimitBytes = inbound.PerUserTrafficLimitBytes
	result.UsedBytes = used
	result.RemainingBytes = remaining
	result.Unit = strings.ToUpper(strings.TrimSpace(inbound.PerUserTrafficLimitUnit))
	return result
}

func accountHasExhaustedLocalQuota(tx *gorm.DB, accountID int) (bool, error) {
	if tx == nil || accountID <= 0 {
		return false, nil
	}
	var memberships []model.AccountInbound
	if err := tx.Session(&gorm.Session{NewDB: true}).
		Where("account_id = ?", accountID).Find(&memberships).Error; err != nil {
		return false, err
	}
	if len(memberships) == 0 {
		return false, nil
	}
	inboundIDs := make([]int, 0, len(memberships))
	for _, membership := range memberships {
		inboundIDs = append(inboundIDs, membership.InboundId)
	}
	var inbounds []*model.Inbound
	if err := tx.Session(&gorm.Session{NewDB: true}).Where("id IN ?", inboundIDs).Find(&inbounds).Error; err != nil {
		return false, err
	}
	quotaByInbound := make(map[int]*model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		quotaByInbound[inbound.Id] = inbound
	}
	for _, membership := range memberships {
		if ResolveMembershipTrafficQuota(quotaByInbound[membership.InboundId], &membership).Exhausted {
			return true, nil
		}
	}
	return false, nil
}

// projectQuotaTransitionEnables пересчитывает только эффективные флаги, на которые
// влияет старый или новый локальный лимит. Учётные данные и явные изменения в
// остальных записях остаются такими, как отправила форма подключения.
func projectQuotaTransitionEnables(tx *gorm.DB, oldInbound, newInbound *model.Inbound) error {
	if tx == nil || oldInbound == nil || newInbound == nil {
		return nil
	}
	var oldSettings map[string]any
	if err := json.Unmarshal([]byte(oldInbound.Settings), &oldSettings); err != nil {
		return err
	}
	var newSettings map[string]any
	if err := json.Unmarshal([]byte(newInbound.Settings), &newSettings); err != nil {
		return err
	}
	oldClients, _ := oldSettings["clients"].([]any)
	newClients, ok := newSettings["clients"].([]any)
	if !ok {
		return nil
	}
	oldByEmail := make(map[string]map[string]any, len(oldClients))
	for _, raw := range oldClients {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if email, _ := entry["email"].(string); accountKey(email) != "" {
			oldByEmail[accountKey(email)] = entry
		}
	}
	accountService := AccountService{}
	changed := false
	affectedAccounts := map[int]model.Account{}
	for _, raw := range newClients {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		email, _ := entry["email"].(string)
		key := accountKey(email)
		oldEntry := oldByEmail[key]
		if key == "" {
			continue
		}
		account, err := accountService.GetAccountByEmailTx(tx, email)
		if err != nil {
			return err
		}
		if account == nil {
			continue
		}
		var membership model.AccountInbound
		membershipErr := tx.Session(&gorm.Session{NewDB: true}).
			Where("account_id = ? AND inbound_id = ?", account.Id, newInbound.Id).
			First(&membership).Error
		if membershipErr != nil && membershipErr != gorm.ErrRecordNotFound {
			return membershipErr
		}
		if membershipErr == gorm.ErrRecordNotFound {
			membership = model.AccountInbound{AccountId: account.Id, InboundId: newInbound.Id}
		}
		if oldEntry == nil {
			if enabled, exists := entry["enable"].(bool); exists && !enabled {
				sourceQuotaExhausted, quotaErr := accountHasExhaustedLocalQuota(tx, account.Id)
				if quotaErr != nil {
					return quotaErr
				}
				if sourceQuotaExhausted && !ResolveMembershipTrafficQuota(newInbound, &membership).Exhausted {
					entry["enable"] = account.Enable && MembershipEnabled(&membership)
					changed = true
				}
			}
			continue
		}
		oldExhausted := ResolveMembershipTrafficQuota(oldInbound, &membership).Exhausted
		newQuota := ResolveMembershipTrafficQuota(newInbound, &membership)
		if oldExhausted != newQuota.Exhausted {
			affectedAccounts[account.Id] = *account
		}
		if !oldExhausted && !newQuota.Exhausted {
			continue
		}
		entry["enable"] = account.Enable && MembershipEnabled(&membership) && !newQuota.Exhausted
		changed = true
	}
	if !changed {
		return nil
	}
	encoded, err := json.MarshalIndent(newSettings, "", "  ")
	if err != nil {
		return err
	}
	newInbound.Settings = string(encoded)
	for _, account := range affectedAccounts {
		if err := projectOtherMembershipEnableFlags(tx, &account, newInbound.Id); err != nil {
			return err
		}
	}
	return nil
}

func projectOtherMembershipEnableFlags(tx *gorm.DB, account *model.Account, skipInboundID int) error {
	if tx == nil || account == nil || account.Id <= 0 {
		return nil
	}
	var memberships []model.AccountInbound
	if err := tx.Session(&gorm.Session{NewDB: true}).Where("account_id = ?", account.Id).Find(&memberships).Error; err != nil {
		return err
	}
	inboundIDs := make([]int, 0, len(memberships))
	for _, membership := range memberships {
		if membership.InboundId != skipInboundID {
			inboundIDs = append(inboundIDs, membership.InboundId)
		}
	}
	if len(inboundIDs) == 0 {
		return nil
	}
	var inbounds []*model.Inbound
	if err := tx.Session(&gorm.Session{NewDB: true}).Where("id IN ?", inboundIDs).Find(&inbounds).Error; err != nil {
		return err
	}
	membershipByInbound := make(map[int]*model.AccountInbound, len(memberships))
	for i := range memberships {
		membershipByInbound[memberships[i].InboundId] = &memberships[i]
	}
	for _, inbound := range inbounds {
		var settings map[string]any
		if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
			return err
		}
		clients, ok := settings["clients"].([]any)
		if !ok {
			continue
		}
		membership := membershipByInbound[inbound.Id]
		enable := account.Enable && MembershipEnabled(membership) && !ResolveMembershipTrafficQuota(inbound, membership).Exhausted
		changed := false
		for _, raw := range clients {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			email, _ := entry["email"].(string)
			if accountKey(email) != accountKey(account.Email) {
				continue
			}
			if current, exists := entry["enable"].(bool); !exists || current != enable {
				entry["enable"] = enable
				changed = true
			}
		}
		if !changed {
			continue
		}
		encoded, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}
		if err := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).
			Update("settings", string(encoded)).Error; err != nil {
			return err
		}
	}
	return nil
}

// NextInboundTrafficReset возвращает следующий календарный запуск существующего reset-job.
// Границы соответствуют @hourly, @daily, @weekly (воскресенье) и @monthly из robfig/cron.
func NextInboundTrafficReset(period string, now time.Time) (time.Time, bool) {
	local := now.In(time.Local)
	switch strings.ToLower(strings.TrimSpace(period)) {
	case "hourly":
		return time.Date(local.Year(), local.Month(), local.Day(), local.Hour()+1, 0, 0, 0, local.Location()), true
	case "daily":
		return time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, local.Location()), true
	case "weekly":
		days := (7 - int(local.Weekday())) % 7
		if days == 0 {
			days = 7
		}
		return time.Date(local.Year(), local.Month(), local.Day()+days, 0, 0, 0, 0, local.Location()), true
	case "monthly":
		return time.Date(local.Year(), local.Month()+1, 1, 0, 0, 0, 0, local.Location()), true
	default:
		return time.Time{}, false
	}
}

type membershipQuotaResetCandidate struct {
	inboundID int
	email     string
}

// resetMembershipQuotaCounters очищает выбранные локальные счётчики и повторно
// проецирует затронутые аккаунты. Кандидаты после commit проверяются ещё раз по
// эффективному enable, чтобы не включить вручную отключённое членство.
func resetAccountMembershipQuotaCounters(tx *gorm.DB, email string) ([]membershipQuotaResetCandidate, error) {
	key := strings.ToLower(strings.TrimSpace(email))
	if key == "" {
		return nil, nil
	}
	var accountIDs []int
	if err := tx.Model(&model.Account{}).Where("LOWER(TRIM(email)) = ?", key).Pluck("id", &accountIDs).Error; err != nil {
		return nil, err
	}
	if len(accountIDs) == 0 {
		return nil, nil
	}
	return resetMembershipQuotaCounters(tx, "account_id IN ?", []any{accountIDs}, accountIDs)
}

func (s *InboundService) enabledMembershipRestoreCandidates(tx *gorm.DB, emails []string) ([]membershipQuotaResetCandidate, error) {
	rows, err := s.inboundsServingEmails(tx, emails)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	accountService := AccountService{}
	inboundByID := map[int]*model.Inbound{}
	seen := map[string]bool{}
	candidates := make([]membershipQuotaResetCandidate, 0, len(rows))
	for _, row := range rows {
		key := accountKey(row.Email) + "\x00" + strconv.Itoa(row.Id)
		if seen[key] {
			continue
		}
		seen[key] = true
		inbound := inboundByID[row.Id]
		if inbound == nil {
			var loaded model.Inbound
			if err := tx.Where("id = ?", row.Id).First(&loaded).Error; err != nil {
				return nil, err
			}
			inbound = &loaded
			inboundByID[row.Id] = inbound
		}
		account, err := accountService.GetAccountByEmailTx(tx, row.Email)
		if err != nil {
			return nil, err
		}
		if account != nil {
			if !account.Enable {
				continue
			}
			var membership model.AccountInbound
			membershipErr := tx.Where("account_id = ? AND inbound_id = ?", account.Id, inbound.Id).First(&membership).Error
			if membershipErr == nil {
				if !MembershipEnabled(&membership) || ResolveMembershipTrafficQuota(inbound, &membership).Exhausted {
					continue
				}
			} else if membershipErr != gorm.ErrRecordNotFound {
				return nil, membershipErr
			}
		}
		candidates = append(candidates, membershipQuotaResetCandidate{inboundID: inbound.Id, email: row.Email})
	}
	return candidates, nil
}

func resetMembershipQuotaCounters(tx *gorm.DB, where string, args []any, extraAccountIDs []int) ([]membershipQuotaResetCandidate, error) {
	var memberships []model.AccountInbound
	if err := tx.Where(where, args...).Find(&memberships).Error; err != nil {
		return nil, err
	}

	inboundIDs := make(map[int]bool, len(memberships))
	accountIDs := make(map[int]bool, len(memberships)+len(extraAccountIDs))
	for _, membership := range memberships {
		inboundIDs[membership.InboundId] = true
	}
	for _, accountID := range extraAccountIDs {
		if accountID > 0 {
			accountIDs[accountID] = true
		}
	}

	inboundIDList := make([]int, 0, len(inboundIDs))
	for id := range inboundIDs {
		inboundIDList = append(inboundIDList, id)
	}
	inbounds := map[int]*model.Inbound{}
	if len(inboundIDList) > 0 {
		var rows []*model.Inbound
		if err := tx.Where("id IN ?", inboundIDList).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, inbound := range rows {
			inbounds[inbound.Id] = inbound
		}
	}

	for _, membership := range memberships {
		if quota := ResolveMembershipTrafficQuota(inbounds[membership.InboundId], &membership); quota.Exhausted {
			accountIDs[membership.AccountId] = true
		}
		if err := tx.Model(&model.AccountInbound{}).
			Where("account_id = ? AND inbound_id = ?", membership.AccountId, membership.InboundId).
			Update("quota_used_bytes", 0).Error; err != nil {
			return nil, err
		}
	}

	accountIDList := make([]int, 0, len(accountIDs))
	for id := range accountIDs {
		accountIDList = append(accountIDList, id)
	}
	sort.Ints(accountIDList)
	if len(accountIDList) == 0 {
		return nil, nil
	}
	var accounts []model.Account
	if err := tx.Where("id IN ?", accountIDList).Order("id ASC").Find(&accounts).Error; err != nil {
		return nil, err
	}
	accountService := AccountService{}
	var candidates []membershipQuotaResetCandidate
	seen := map[string]bool{}
	for _, account := range accounts {
		touched, err := accountService.ProjectAccount(tx, account.Id)
		if err != nil {
			return nil, err
		}
		for _, inboundID := range touched {
			key := strings.ToLower(strings.TrimSpace(account.Email)) + "\x00" + strconv.Itoa(inboundID)
			if seen[key] {
				continue
			}
			seen[key] = true
			candidates = append(candidates, membershipQuotaResetCandidate{inboundID: inboundID, email: account.Email})
		}
	}
	return candidates, nil
}

// restoreMembershipUsers re-adds enabled native Xray users after a counter reset.
// WireGuard-Xray peers are config-derived and therefore need a full core restart.
func (s *InboundService) restoreMembershipUsers(candidates []membershipQuotaResetCandidate) bool {
	seen := map[string]bool{}
	needRestart := false
	for _, candidate := range candidates {
		key := strings.ToLower(strings.TrimSpace(candidate.email)) + "\x00" + strconv.Itoa(candidate.inboundID)
		if seen[key] {
			continue
		}
		seen[key] = true

		inbound, err := s.GetInbound(candidate.inboundID)
		if err != nil || inbound == nil || !inbound.Enable {
			continue
		}
		var client *model.Client
		clients, err := s.GetClients(inbound)
		if err != nil {
			logger.Warning("Не удалось прочитать клиента после сброса квоты:", candidate.email, err)
			continue
		}
		for i := range clients {
			if strings.EqualFold(strings.TrimSpace(clients[i].Email), strings.TrimSpace(candidate.email)) {
				client = &clients[i]
				break
			}
		}
		if client == nil || !client.Enable {
			continue
		}
		var globalTraffic xray.ClientTraffic
		trafficErr := database.GetDB().Where("LOWER(TRIM(email)) = ?", accountKey(candidate.email)).First(&globalTraffic).Error
		if trafficErr == nil && !globalTraffic.Enable {
			continue
		}
		if trafficErr != nil && trafficErr != gorm.ErrRecordNotFound {
			logger.Warning("Не удалось проверить общую квоту перед восстановлением пользователя:", candidate.email, trafficErr)
			continue
		}
		if xrayMembershipNeedsRestart(inbound.Protocol) {
			needRestart = true
			continue
		}
		if !supportsXrayUserAPI(inbound.Protocol) {
			continue
		}

		cipher := client.Method
		if inbound.Protocol == model.Shadowsocks && cipher == "" {
			var settings map[string]any
			if err := json.Unmarshal([]byte(inbound.Settings), &settings); err == nil {
				cipher, _ = settings["method"].(string)
			}
		}
		if err := s.xrayApi.Init(p.GetAPIPort()); err != nil {
			logger.Warning("Не удалось подключиться к Xray после сброса локальной квоты:", err)
			needRestart = true
			continue
		}
		err = s.xrayApi.AddUser(string(inbound.Protocol), inbound.Tag, map[string]any{
			"email": client.Email, "id": client.ID, "auth": client.Auth,
			"security": client.Security, "flow": client.Flow, "password": client.Password,
			"username": client.Username, "cipher": cipher,
		})
		s.xrayApi.Close()
		if err != nil {
			logger.Warning("Не удалось повторно включить клиента в Xray:", client.Email, err)
			needRestart = true
		}
	}
	if needRestart {
		(&XrayService{}).SetToNeedRestart()
	}
	return needRestart
}

func xrayMembershipNeedsRestart(protocol model.Protocol) bool {
	return protocol == model.WireGuard || protocol == model.Mixed || protocol == model.HTTP
}

func supportsXrayUserAPI(protocol model.Protocol) bool {
	switch protocol {
	case model.VMESS, model.VLESS, model.Trojan, model.Shadowsocks,
		model.Hysteria, model.Hysteria2, model.ANYTLS, model.TUIC, model.NAIVE:
		return true
	default:
		return false
	}
}

type membershipQuotaDisableCandidate struct {
	inboundID int
	email     string
}

type membershipQuotaRawDelta struct {
	up   int64
	down int64
}

// addMembershipQuotaTraffic сохраняет только однозначные source-aware записи и
// возвращает членства, которые именно этим тиком достигли своего локального лимита.
func (s *InboundService) addMembershipQuotaTraffic(tx *gorm.DB, accountTraffic []*xray.ClientTraffic, taggedTraffic []*xray.MembershipTraffic) ([]membershipQuotaDisableCandidate, error) {
	raw := map[membershipUsageKey]*membershipQuotaRawDelta{}
	add := func(inboundID int, email string, up, down int64) {
		if inboundID <= 0 || accountKey(email) == "" || up <= 0 && down <= 0 {
			return
		}
		key := membershipUsageKey{inboundId: inboundID, emailKey: accountKey(email)}
		delta := raw[key]
		if delta == nil {
			delta = &membershipQuotaRawDelta{}
			raw[key] = delta
		}
		if up > 0 {
			delta.up += up
		}
		if down > 0 {
			delta.down += down
		}
	}

	var taggedInbounds []*model.Inbound
	var tags []string
	for _, traffic := range taggedTraffic {
		if traffic != nil && strings.TrimSpace(traffic.InboundTag) != "" {
			tags = append(tags, traffic.InboundTag)
		}
	}
	if len(tags) > 0 {
		if err := tx.Where("tag IN ?", tags).Find(&taggedInbounds).Error; err != nil {
			return nil, err
		}
	}
	inboundByTag := make(map[string]*model.Inbound, len(taggedInbounds))
	inboundByID := make(map[int]*model.Inbound, len(taggedInbounds))
	for _, inbound := range taggedInbounds {
		inboundByTag[inbound.Tag] = inbound
		inboundByID[inbound.Id] = inbound
	}

	var taggedKeys = map[membershipUsageKey]bool{}
	for _, traffic := range taggedTraffic {
		if traffic == nil {
			continue
		}
		inbound := inboundByTag[traffic.InboundTag]
		if inbound == nil {
			continue
		}
		email := ""
		switch traffic.IdentityType {
		case "email":
			email = traffic.Identity
		case "wireguard-ip":
			if resolved, ok := WgxrayAccountForSourceIP(inbound, traffic.Identity); ok {
				email = resolved
			}
		default:
			continue
		}
		if accountKey(email) == "" || traffic.Up <= 0 && traffic.Down <= 0 {
			continue
		}
		key := membershipUsageKey{inboundId: inbound.Id, emailKey: accountKey(email)}
		taggedKeys[key] = true
		add(inbound.Id, email, traffic.Up, traffic.Down)
	}

	for _, traffic := range accountTraffic {
		if traffic == nil || traffic.CoreCounted || traffic.InboundId <= 0 {
			continue
		}
		key := membershipUsageKey{inboundId: traffic.InboundId, emailKey: accountKey(traffic.Email)}
		// Tagged Xray counters are authoritative when present. Relay-side counters
		// are then only a fallback, never a second charge for the same transfer.
		if taggedKeys[key] {
			continue
		}
		add(traffic.InboundId, traffic.Email, traffic.Up, traffic.Down)
	}
	if len(raw) == 0 {
		return nil, nil
	}

	ids := make([]int, 0, len(raw))
	for key := range raw {
		ids = append(ids, key.inboundId)
	}
	var inboundRows []*model.Inbound
	if err := tx.Where("id IN ?", ids).Find(&inboundRows).Error; err != nil {
		return nil, err
	}
	for _, inbound := range inboundRows {
		inboundByID[inbound.Id] = inbound
	}

	accountService := AccountService{}
	var crossed []membershipQuotaDisableCandidate
	changedAccounts := map[int]bool{}
	syncedInbounds := map[int]bool{}
	for key, delta := range raw {
		inbound := inboundByID[key.inboundId]
		if inbound == nil || !inbound.PerUserTrafficLimitEnable || inbound.PerUserTrafficLimitBytes <= 0 {
			continue
		}
		var account model.Account
		accountErr := tx.Where("LOWER(TRIM(email)) = ?", key.emailKey).First(&account).Error
		if accountErr == gorm.ErrRecordNotFound && !syncedInbounds[inbound.Id] {
			if err := accountService.SyncInboundAccounts(tx, inbound.Id); err != nil {
				return nil, err
			}
			syncedInbounds[inbound.Id] = true
			accountErr = tx.Where("LOWER(TRIM(email)) = ?", key.emailKey).First(&account).Error
		}
		if accountErr != nil {
			if accountErr == gorm.ErrRecordNotFound {
				logger.Warning("Не найден аккаунт для локальной квоты:", key.emailKey)
				continue
			}
			return nil, accountErr
		}
		var membership model.AccountInbound
		membershipErr := tx.Where("account_id = ? AND inbound_id = ?", account.Id, inbound.Id).First(&membership).Error
		if membershipErr == gorm.ErrRecordNotFound {
			// Ремонтируем неполную accounts-проекцию один раз на inbound, после чего
			// повторяем поиск. Обычный путь сюда не попадает: save и client add уже sync.
			if err := accountService.SyncInboundAccounts(tx, inbound.Id); err != nil {
				return nil, err
			}
			membershipErr = tx.Where("account_id = ? AND inbound_id = ?", account.Id, inbound.Id).First(&membership).Error
		}
		if membershipErr != nil {
			return nil, membershipErr
		}

		before := ResolveMembershipTrafficQuota(inbound, &membership)
		if !before.Enabled {
			continue
		}
		beforeAllowed := account.Enable && MembershipEnabled(&membership) && !before.Exhausted
		up, down := multiplyDelta(inbound, before.UsedBytes, delta.up, delta.down)
		added := up + down
		if added <= 0 {
			continue
		}
		newUsed := before.UsedBytes + added
		if newUsed < before.UsedBytes {
			newUsed = int64(^uint64(0) >> 1)
		}
		result := tx.Model(&model.AccountInbound{}).
			Where("account_id = ? AND inbound_id = ?", account.Id, inbound.Id).
			Update("quota_used_bytes", newUsed)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected != 1 {
			return nil, gorm.ErrRecordNotFound
		}
		membership.QuotaUsedBytes = newUsed
		after := ResolveMembershipTrafficQuota(inbound, &membership)
		afterAllowed := account.Enable && MembershipEnabled(&membership) && !after.Exhausted
		if beforeAllowed && !afterAllowed {
			changedAccounts[account.Id] = true
			crossed = append(crossed, membershipQuotaDisableCandidate{inboundID: inbound.Id, email: account.Email})
		}
	}

	for accountID := range changedAccounts {
		if _, err := accountService.ProjectAccount(tx, accountID); err != nil {
			return nil, err
		}
	}
	return crossed, nil
}

// disableMembershipInXray отключает только пользователя на указанном Xray inbound.
// Xray-WireGuard строит peer-список из конфигурации и требует перезапуска core.
func (s *InboundService) disableMembershipInXray(candidate membershipQuotaDisableCandidate) bool {
	inbound, err := s.GetInbound(candidate.inboundID)
	if err != nil || inbound == nil {
		return false
	}
	if xrayMembershipNeedsRestart(inbound.Protocol) {
		return true
	}
	if !supportsXrayUserAPI(inbound.Protocol) {
		return false
	}
	if err := s.xrayApi.Init(p.GetAPIPort()); err != nil {
		logger.Warning("Не удалось подключиться к Xray при отключении локальной квоты:", err)
		return true
	}
	err = s.xrayApi.RemoveUser(inbound.Tag, candidate.email)
	s.xrayApi.Close()
	if err != nil {
		logger.Warning("Не удалось отключить пользователя на Xray inbound:", candidate.email, err)
		return true
	}
	return false
}
