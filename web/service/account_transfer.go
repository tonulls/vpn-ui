package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/xray"
	"gorm.io/gorm"
)

// AccountTransferRequest задаёт выбранные аккаунты и получателя передачи.
type AccountTransferRequest struct {
	Emails       []string `json:"emails" form:"emails"`
	TargetRole   string   `json:"targetRole" form:"targetRole"`
	TargetUserId int      `json:"targetUserId" form:"targetUserId"`
}

// AccountTransferItem — результат обработки одного аккаунта.
type AccountTransferItem struct {
	Email   string `json:"email"`
	Success bool   `json:"success"`
	Skipped bool   `json:"skipped"`
	Error   string `json:"error,omitempty"`
}

// AccountTransferResult агрегирует независимые транзакции по аккаунтам.
type AccountTransferResult struct {
	Total             int                   `json:"total"`
	Transferred       int                   `json:"transferred"`
	Failed            int                   `json:"failed"`
	Skipped           int                   `json:"skipped"`
	Items             []AccountTransferItem `json:"items"`
	TouchedInboundIds []int                 `json:"-"`
}

var (
	errTransferAccountNotFound = errors.New("аккаунт не найден")
	errTransferTrafficNotFound = errors.New("не найдена запись статистики аккаунта")
	errTransferSameOwner       = errors.New("аккаунт уже принадлежит выбранному владельцу")
)

// AccountTransferService выполняет передачу аккаунтов отдельно и атомарно для
// каждого аккаунта. Исторический автор остаётся в accounts, а переходы владельцев
// записываются только во внутреннюю таблицу account_transfer_histories.
type AccountTransferService struct{}

// TransferAccounts передаёт уникальные аккаунты указанному активному владельцу.
// Вызов разрешён только суперадминистратору; эта проверка дублирует middleware.
func (s *AccountTransferService) TransferAccounts(actor *model.User, req AccountTransferRequest) (AccountTransferResult, error) {
	var result AccountTransferResult
	if actor == nil || !actor.IsSuperAdmin {
		return result, errors.New("передача доступна только суперадминистратору")
	}
	if req.TargetUserId <= 0 {
		return result, errors.New("не выбран получатель передачи")
	}

	emails := uniqueAccountEmails(req.Emails)
	if len(emails) == 0 {
		return result, errors.New("не выбраны аккаунты для передачи")
	}
	if len(emails) > 200 {
		return result, errors.New("за один запрос можно передать не более 200 аккаунтов")
	}

	db := database.GetDB()
	var target model.User
	if err := db.Where("id = ?", req.TargetUserId).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, errors.New("получатель не найден")
		}
		return result, err
	}
	if !target.Enable || ownerRoleForUser(&target) != req.TargetRole {
		return result, errors.New("получатель отключён или его роль не совпадает с выбранной")
	}
	if !target.Can(model.PermAccessInbounds) {
		return result, errors.New("получателю не разрешено управлять подключениями и клиентами")
	}

	var accountService AccountService
	assignable, err := accountService.AssignableInboundsFor(&target)
	if err != nil {
		return result, err
	}
	inboundIDs := make([]int, 0, len(assignable))
	for _, inbound := range assignable {
		inboundIDs = append(inboundIDs, inbound.InboundId)
	}

	// У набора подключений получателя проверяем те же ограничения, что и при
	// обычном добавлении членства. Нельзя молча оставить аккаунт лишь на части.
	var targetInbounds []*model.Inbound
	if len(inboundIDs) > 0 {
		if err := db.Where("id IN ?", inboundIDs).Order("id ASC").Find(&targetInbounds).Error; err != nil {
			return result, err
		}
	}
	if err := accountService.ValidateMembershipSet(targetInbounds); err != nil {
		return result, err
	}

	result.Total = len(emails)
	result.Items = make([]AccountTransferItem, 0, len(emails))
	touched := map[int]bool{}
	for _, email := range emails {
		item := AccountTransferItem{Email: email}
		var changed []int
		err := db.Transaction(func(tx *gorm.DB) error {
			var transferErr error
			changed, transferErr = transferOneAccount(tx, actor, &target, req.TargetRole, inboundIDs, email)
			return transferErr
		})
		if err != nil {
			item.Error = err.Error()
			result.Failed++
		} else if changed == nil {
			item.Skipped = true
			item.Error = errTransferSameOwner.Error()
			result.Skipped++
		} else {
			item.Success = true
			result.Transferred++
			for _, inboundID := range changed {
				touched[inboundID] = true
			}
		}
		result.Items = append(result.Items, item)
	}
	for id := range touched {
		result.TouchedInboundIds = append(result.TouchedInboundIds, id)
	}
	return result, nil
}

func transferOneAccount(tx *gorm.DB, actor, target *model.User, targetRole string, targetInboundIDs []int, email string) ([]int, error) {
	var accountService AccountService
	account, err := accountService.GetAccountByEmailTx(tx, email)
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, errTransferAccountNotFound
	}

	var traffic xray.ClientTraffic
	if err := tx.Where("LOWER(TRIM(email)) = ?", accountKey(account.Email)).First(&traffic).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errTransferTrafficNotFound
		}
		return nil, err
	}

	var oldLedger model.ResellerClient
	ledgerErr := tx.Where("LOWER(TRIM(email)) = ?", accountKey(account.Email)).First(&oldLedger).Error
	if ledgerErr != nil && !errors.Is(ledgerErr, gorm.ErrRecordNotFound) {
		return nil, ledgerErr
	}
	hasOldLedger := ledgerErr == nil
	fromUserID, fromRole, fromName := currentAccountOwner(account, &oldLedger, hasOldLedger)
	if fromRole == targetRole && fromUserID == target.Id {
		return nil, nil
	}
	// Capture creator identity before any owner update. Name snapshots remain even
	// if the originating user account is later deleted.
	if account.CreatorName == "" && account.CreatorUserId > 0 {
		var creator model.User
		if err := tx.Where("id = ?", account.CreatorUserId).First(&creator).Error; err == nil {
			account.CreatorName = creator.DisplayUsername()
		}
	}
	if fromName == "" && fromUserID > 0 {
		var oldUser model.User
		if err := tx.Where("id = ?", fromUserID).First(&oldUser).Error; err == nil {
			fromName = oldUser.DisplayUsername()
		}
	}

	var quote ChargeQuote
	var targetProfile *model.ResellerProfile
	if targetRole == "reseller" {
		var profile model.ResellerProfile
		if err := tx.Where("user_id = ?", target.Id).First(&profile).Error; err != nil {
			return nil, fmt.Errorf("не найден профиль принимающего реселлера: %w", err)
		}
		if profile.SubscriptionLimit > 0 {
			var count int64
			if err := tx.Model(&model.ResellerClient{}).Where("user_id = ?", target.Id).
				Select("COUNT(DISTINCT LOWER(TRIM(email)))").Scan(&count).Error; err != nil {
				return nil, err
			}
			if count >= int64(profile.SubscriptionLimit) {
				return nil, fmt.Errorf("реселлер достиг лимита подписок: максимум %d", profile.SubscriptionLimit)
			}
		}
		q, err := Quote(QuoteInput{
			Profile: profile, Create: true, NewTotal: account.TotalGB,
			NowMillis: time.Now().UnixMilli(),
		})
		if err != nil {
			return nil, err
		}
		quote, targetProfile = q, &profile
	}

	// Финансовые изменения включены в ту же транзакцию, что и смена владельца.
	// Поэтому недостаток баланса у получателя оставляет аккаунт и прежнее списание
	// нетронутыми.
	if hasOldLedger {
		consumed := traffic.AllTime - oldLedger.AllTimeBase
		if consumed < 0 {
			consumed = 0
		}
		refund := oldLedger.ChargedBytes - consumed
		if refund < 0 {
			refund = 0
		}
		if err := restoreSpent(tx, oldLedger.UserId, -refund); err != nil {
			return nil, err
		}
		if err := tx.Delete(&oldLedger).Error; err != nil {
			return nil, err
		}
	}

	account.OwnerUserId = target.Id
	account.OwnerRole = targetRole
	account.OwnerName = target.DisplayUsername()
	// Сохраняются только идентификатор аккаунта, subId, комментарий и Telegram ID;
	// квота и срок также остаются исходными, если профиль реселлера не принуждает
	// срок по daysPerGb. Все секреты и остальные ограничения пересоздаются ниже.
	account.Enable = true
	account.Reset = 0
	account.LimitIP = 0
	account.SpeedLimitDown = nil
	account.SpeedLimitUp = nil
	account.UserLimitOverride = nil
	account.UUID = ""
	account.VpnUsername = ""
	account.Password = ""
	account.Auth = ""
	account.Security = ""
	account.Secret = ""
	account.NaiveUser = ""
	if targetProfile != nil {
		if targetProfile.ClientLimitsEnabled {
			account.LimitIP = targetProfile.ClientLimitIP
			account.SpeedLimitDown = targetProfile.ClientLimitDown
			account.SpeedLimitUp = targetProfile.ClientLimitUp
			account.UserLimitOverride = targetProfile.ClientLimitDevices
		}
		if quote.ForceExpiry {
			account.ExpiryTime = quote.ExpiryTime
		}
		if err := addSpent(tx, target.Id, quote.DeltaSpent); err != nil {
			return nil, err
		}
	}
	if err := tx.Save(account).Error; err != nil {
		return nil, err
	}

	// Drop every old membership and its protocol-specific Extra first. The
	// projection removes the account from all old settings blobs; SetMemberships
	// then allocates fresh slots and credentials for exactly the recipient's grants.
	if err := tx.Where("account_id = ?", account.Id).Delete(&model.AccountInbound{}).Error; err != nil {
		return nil, err
	}
	oldTouched, err := accountService.ProjectAccount(tx, account.Id)
	if err != nil {
		return nil, err
	}
	if err := accountService.SetMemberships(tx, account.Id, targetInboundIDs); err != nil {
		return nil, err
	}
	newTouched, err := accountService.ProjectAccount(tx, account.Id)
	if err != nil {
		return nil, err
	}
	touched := uniqueTransferInboundIDs(oldTouched, newTouched)
	for _, inboundID := range touched {
		if err := accountService.SyncInboundAccounts(tx, inboundID); err != nil {
			return nil, err
		}
	}

	homeInboundID := 0
	if len(targetInboundIDs) > 0 {
		homeInboundID = targetInboundIDs[0]
	}
	if err := tx.Model(&xray.ClientTraffic{}).Where("id = ?", traffic.Id).Updates(map[string]any{
		"inbound_id":  homeInboundID,
		"enable":      true,
		"total":       account.TotalGB,
		"expiry_time": account.ExpiryTime,
		"reset":       0,
		"last_online": 0,
		"up":          0,
		"down":        0,
		"all_time":    0,
	}).Error; err != nil {
		return nil, err
	}

	if targetRole == "reseller" {
		if err := tx.Create(&model.ResellerClient{
			Email: account.Email, InboundId: homeInboundID, UserId: target.Id,
			ChargedBytes: quote.NewCharged, AllTimeBase: 0,
		}).Error; err != nil {
			return nil, err
		}
	}

	if err := tx.Create(&model.AccountTransferHistory{
		AccountId: account.Id, Email: account.Email,
		FromUserId: fromUserID, FromRole: fromRole, FromName: fromName,
		ToUserId: target.Id, ToRole: targetRole, ToName: target.DisplayUsername(),
		ActorUserId: actor.Id,
	}).Error; err != nil {
		return nil, err
	}
	return touched, nil
}

func currentAccountOwner(account *model.Account, ledger *model.ResellerClient, hasLedger bool) (int, string, string) {
	if hasLedger {
		name := account.OwnerName
		if account.OwnerUserId != ledger.UserId || account.OwnerRole != "reseller" {
			name = ""
		}
		return ledger.UserId, "reseller", name
	}
	if account.OwnerRole != "" {
		return account.OwnerUserId, account.OwnerRole, account.OwnerName
	}
	if isAccountCreatorRole(account.CreatorRole) {
		return account.CreatorUserId, account.CreatorRole, account.CreatorName
	}
	return 0, "", ""
}

func ownerRoleForUser(user *model.User) string {
	if user == nil {
		return ""
	}
	if user.IsSuperAdmin {
		return "superadmin"
	}
	if user.IsReseller {
		return "reseller"
	}
	return "admin"
}

func uniqueTransferInboundIDs(first, second []int) []int {
	seen := make(map[int]bool, len(first)+len(second))
	out := make([]int, 0, len(first)+len(second))
	for _, ids := range [][]int{first, second} {
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func uniqueAccountEmails(emails []string) []string {
	out := make([]string, 0, len(emails))
	seen := make(map[string]bool, len(emails))
	for _, email := range emails {
		email = strings.TrimSpace(email)
		key := accountKey(email)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, email)
	}
	return out
}
