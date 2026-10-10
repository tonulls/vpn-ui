package service

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/database"
	"github.com/mhsanaei/3x-ui/v2/database/model"
	"github.com/mhsanaei/3x-ui/v2/xray"
)

// The account-centric view behind the Clients page.
//
// Everything else in the panel is organised by INBOUND: an account is found by
// opening the inbound that serves it. That made sense while an account lived on
// exactly one, and stopped making sense the moment one account could be on
// several, because there was then no page that showed the account itself.
//
// This is a read model. Ordinary client mutations still use the established inbound
// routes; super-admin account transfers use a separate transactional service because
// they replace ownership, memberships and reseller accounting together.

// AccountMembershipView is one inbound an account is served on, named the way an
// operator recognises it rather than by id alone.
type AccountMembershipView struct {
	InboundId    int    `json:"inboundId"`
	Protocol     string `json:"protocol"`
	ProtocolName string `json:"protocolName,omitempty"`
	Remark       string `json:"remark"`
	Port         int    `json:"port"`
	Enable       bool   `json:"enable"`
	Slot         *int   `json:"slot"`
	// Method is the shadowsocks cipher, and empty for every other protocol. It is
	// reported because a 2022-blake3 cipher refuses a user password that is not
	// base64 of its exact key length, so a form offering ONE password for an
	// account spanning several protocols has to generate it in the strict shape
	// whenever one of these is in the set. Every other protocol takes any string,
	// so the strict shape satisfies all of them at once.
	Method string `json:"method,omitempty"`
	// ClientId is the value THIS protocol addresses the account by, which is the
	// path parameter /updateClient/:clientId and /delClient/:clientId take.
	//
	// It is protocol-dependent (a uuid for vmess and vless, the password for
	// trojan and the credential VPNs, the auth for hysteria, the email only for
	// shadowsocks), so a caller cannot derive it from the email. Without it the
	// Clients page sent the email and every edit came back "empty client ID" for
	// all but one protocol.
	ClientId string `json:"clientId"`
}

// AccountCredentials is the account's own credential columns, reported ONLY for an
// account no inbound serves.
//
// Every other row leaves them out and the page reads them off the stored client
// entries instead (see loadCredentialsFrom in modals/client_membership_modal.html),
// because those entries are what the daemon actually authenticates and the two can
// legitimately differ - a 2022-blake3 membership holds a PSK minted for itself
// alone. An account on no inbound has no stored entry anywhere, so the columns are
// the only copy there is, and without them the edit form opens with every credential
// box blank. That is not cosmetic: ticking an inbound then mints a fresh credential
// for a field that only LOOKED empty, and every config the customer already
// installed stops authenticating.
//
// No new exposure. The same page already loads every inbound's settings blob to
// render the credentials in its expander, so this is the same data for the one kind
// of account that is in no blob.
type AccountCredentials struct {
	UUID        string `json:"uuid,omitempty"`
	Password    string `json:"password,omitempty"`
	VpnUsername string `json:"vpnUsername,omitempty"`
	Auth        string `json:"auth,omitempty"`
	Secret      string `json:"secret,omitempty"`
	NaiveUser   string `json:"naiveUsername,omitempty"`
	Security    string `json:"security,omitempty"`
}

// AccountCreator is the authenticated actor recorded when a new account is created
// through an admin, reseller, or super-admin action.
type AccountCreator struct {
	UserID int
	Role   string
	Name   string
}

func AccountCreatorFromUser(user *model.User) AccountCreator {
	if user == nil {
		return AccountCreator{}
	}
	if user.IsSuperAdmin {
		return AccountCreator{UserID: user.Id, Role: "superadmin", Name: user.DisplayUsername()}
	}
	role := "admin"
	if user.IsReseller {
		role = "reseller"
	}
	return AccountCreator{UserID: user.Id, Role: role, Name: user.DisplayUsername()}
}

func isAccountCreatorRole(role string) bool {
	switch role {
	case "admin", "reseller", "superadmin":
		return true
	default:
		return false
	}
}

// AccountRow is one line of the Clients table.
type AccountRow struct {
	Id         int    `json:"id"`
	Email      string `json:"email"`
	Enable     bool   `json:"enable"`
	SubID      string `json:"subId"`
	Comment    string `json:"comment"`
	TotalGB    int64  `json:"totalGB"`    // bytes, despite the name
	ExpiryTime int64  `json:"expiryTime"` // ms; negative = delayed start
	Reset      int    `json:"reset"`
	LimitIP    int    `json:"limitIp"`
	// TgID is the Telegram chat the bot notifies. Reported so the Clients form can
	// edit it without a second read; 0 means the account is not linked.
	TgID int64 `json:"tgId"`
	// The three per-account limit OVERRIDES, reported so the client form can show
	// what is actually set without a second read.
	//
	// Pointers all the way out to the browser, and NOT omitempty. The form has to
	// tell "this account overrides the inbound with 0, meaning unlimited" from "this
	// account inherits whatever the inbound says", and those are the same value once
	// a nil becomes a 0. omitempty would collapse an explicit 0 into an absent key
	// and lose exactly that distinction; null on the wire is what the empty box in
	// the Limits tab reads back as.
	//
	// LimitIP above is the IP-limit override and is deliberately NOT one of these: it
	// is a plain int that predates the feature, where 0 already means "inherit" (see
	// resolveIPLimit).
	SpeedLimitDown    *int                    `json:"speedLimitDown"`
	SpeedLimitUp      *int                    `json:"speedLimitUp"`
	UserLimitOverride *int                    `json:"userLimitOverride"`
	Up                int64                   `json:"up"`
	Down              int64                   `json:"down"`
	Memberships       []AccountMembershipView `json:"memberships"`
	// Credentials is set only when Memberships is empty. See AccountCredentials.
	Credentials *AccountCredentials `json:"credentials,omitempty"`
	// OwnedByReseller is the reseller's user id, or 0 for a non-reseller owner.
	OwnedByReseller    int    `json:"ownedByReseller"`
	OwnerRole          string `json:"ownerRole,omitempty"`
	OwnerName          string `json:"ownerName,omitempty"`
	ResellerInboundIds []int  `json:"resellerInboundIds,omitempty"`
}

// AccountListResult is one page of the Clients table.
type AccountListResult struct {
	Rows  []AccountRow `json:"rows"`
	Total int          `json:"total"` // rows matching the search, before paging
	Page  int          `json:"page"`
	Size  int          `json:"size"`
	// Sort echoes back the ordering that was actually applied, normalised. The menu
	// ticks its selected item from THIS rather than from what it asked for, so a key
	// the server does not know falls back visibly instead of leaving the menu
	// pointing at an ordering the list is not in.
	Sort string `json:"sort"`
}

// The orderings the Clients table offers. Each one is a COMPLETE ordering, not a
// field to be combined with a direction: "newest" already says which way it runs.
// That is why there is no dir parameter and no ascending/descending toggle - an
// operator picks an answer, not a column plus an arrow.
//
// Named constants because they cross the wire; clients.html uses these exact
// strings as its menu keys.
const (
	AccountSortNewest   = "newest"
	AccountSortOldest   = "oldest"
	AccountSortOnline   = "online"
	AccountSortEnabled  = "enable"
	AccountSortDisabled = "disable"
)

// CountAccounts returns the unique account count visible to a caller.
func (s *AccountService) CountAccounts(user *model.User) (int, error) {
	result, err := s.ListAccounts(user, 1, 1, "", "newest")
	if err != nil {
		return 0, err
	}
	return result.Total, nil
}

// ListAccounts returns the accounts the caller may see, filtered and paged.
//
// SCOPING IS THE WHOLE SECURITY SURFACE OF THIS ENDPOINT, and it fails closed.
// The underlying tables are panel-wide, so an unscoped list would hand every
// admin every other admin's customers and every reseller every other seller's:
//
//   - a super admin sees everything;
//   - a reseller sees ONLY the accounts they own (the ledger's OwnedEmails), which
//     is narrower than their inbound grants, because a shared inbound carries
//     other sellers' customers too;
//   - an ordinary admin sees accounts with at least one membership on an inbound
//     they hold, which is exactly what the Inbounds page already shows them.
func (s *AccountService) ListAccounts(user *model.User, page, size int, search, sortKey string) (*AccountListResult, error) {
	if user == nil {
		// No identity, no rows. Never an unscoped list.
		return &AccountListResult{Rows: []AccountRow{}, Sort: AccountSortNewest}, nil
	}
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 500 {
		size = 50
	}

	db := database.GetDB()

	var accounts []model.Account
	if err := db.Order("email ASC").Find(&accounts).Error; err != nil {
		return nil, err
	}

	memberships, err := s.membershipViews()
	if err != nil {
		return nil, err
	}

	// Usage comes from client_traffics, which is one row per account panel-wide.
	usage := map[string]*xray.ClientTraffic{}
	var traffics []xray.ClientTraffic
	if err := db.Find(&traffics).Error; err != nil {
		return nil, err
	}
	for i := range traffics {
		usage[accountKey(traffics[i].Email)] = &traffics[i]
	}

	owner := map[string]int{}
	var ledger []model.ResellerClient
	if err := db.Find(&ledger).Error; err != nil {
		return nil, err
	}
	for _, rc := range ledger {
		owner[accountKey(rc.Email)] = rc.UserId
	}

	ownerIDsSet := map[int]bool{}
	for i := range accounts {
		if accounts[i].OwnerUserId > 0 {
			ownerIDsSet[accounts[i].OwnerUserId] = true
		} else if accounts[i].CreatorUserId > 0 {
			ownerIDsSet[accounts[i].CreatorUserId] = true
		}
	}
	for _, ownerID := range owner {
		if ownerID > 0 {
			ownerIDsSet[ownerID] = true
		}
	}
	ownerNames := map[int]string{}
	if len(ownerIDsSet) > 0 {
		ownerIDs := make([]int, 0, len(ownerIDsSet))
		for id := range ownerIDsSet {
			ownerIDs = append(ownerIDs, id)
		}
		var owners []model.User
		if err := db.Where("id IN ?", ownerIDs).Find(&owners).Error; err != nil {
			return nil, err
		}
		for i := range owners {
			ownerNames[owners[i].Id] = owners[i].DisplayUsername()
		}
	}

	visible, visibleInboundIDs, err := s.visibilityFilter(user)
	if err != nil {
		return nil, err
	}
	resellerInboundIDs := make(map[int][]int)
	if len(ownerIDsSet) > 0 {
		ownerIDs := make([]int, 0, len(ownerIDsSet))
		for id := range ownerIDsSet {
			ownerIDs = append(ownerIDs, id)
		}
		var grants []model.InboundAccess
		if err := db.Where("user_id IN ?", ownerIDs).Find(&grants).Error; err != nil {
			return nil, err
		}
		for _, grant := range grants {
			resellerInboundIDs[grant.UserId] = append(resellerInboundIDs[grant.UserId], grant.InboundId)
		}
	}

	needle := strings.ToLower(strings.TrimSpace(search))
	rows := make([]AccountRow, 0, len(accounts))
	// When each account was created, for the newest/oldest orderings. Kept beside
	// the rows rather than added to AccountRow because nothing on the page renders
	// it: the table has no created column, and a field the browser never reads is a
	// field that goes stale without anyone noticing.
	createdAt := make(map[int]int64, len(accounts))
	for i := range accounts {
		account := &accounts[i]
		key := accountKey(account.Email)
		allMemberships := memberships[account.Id]
		if !visible(account, allMemberships) {
			continue
		}
		// An account can be shared across inbounds with different grants. The row
		// may be visible because of one granted membership, but the others can carry
		// protocol credentials in ClientId (for example, a Trojan password). Never
		// include hidden memberships in the response or in search matching.
		visibleMemberships := allMemberships
		if visibleInboundIDs != nil {
			visibleMemberships = make([]AccountMembershipView, 0, len(allMemberships))
			for _, membership := range allMemberships {
				if visibleInboundIDs[membership.InboundId] {
					visibleMemberships = append(visibleMemberships, membership)
				}
			}
		}
		ownerUserID, ownerRole, ownerName := account.OwnerUserId, account.OwnerRole, account.OwnerName
		if owner[key] > 0 {
			ownerUserID, ownerRole = owner[key], "reseller"
			if account.OwnerUserId != ownerUserID || account.OwnerRole != "reseller" {
				ownerName = ""
			}
		}
		if ownerRole == "" && isAccountCreatorRole(account.CreatorRole) {
			ownerUserID, ownerRole, ownerName = account.CreatorUserId, account.CreatorRole, account.CreatorName
		}
		if currentName := ownerNames[ownerUserID]; currentName != "" {
			ownerName = currentName
		}
		var ownerGrantIDs []int
		if ownerRole == "reseller" {
			for _, inboundID := range resellerInboundIDs[ownerUserID] {
				if visibleInboundIDs == nil || visibleInboundIDs[inboundID] {
					ownerGrantIDs = append(ownerGrantIDs, inboundID)
				}
			}
		}
		if needle != "" && !accountMatches(account, visibleMemberships, ownerName, needle) {
			continue
		}
		row := AccountRow{
			Id: account.Id, Email: account.Email, Enable: account.Enable,
			SubID: account.SubID, Comment: account.Comment,
			TotalGB: account.TotalGB, ExpiryTime: account.ExpiryTime,
			Reset: account.Reset, LimitIP: account.LimitIP, TgID: account.TgID,
			SpeedLimitDown: account.SpeedLimitDown, SpeedLimitUp: account.SpeedLimitUp,
			UserLimitOverride: account.UserLimitOverride,
			Memberships:       visibleMemberships, OwnedByReseller: owner[key],
			OwnerRole: ownerRole, OwnerName: ownerName, ResellerInboundIds: ownerGrantIDs,
		}
		if len(allMemberships) == 0 {
			// Nothing serves it, so no settings blob carries its credentials and this
			// row is the only place the edit form can read them from.
			row.Credentials = &AccountCredentials{
				UUID: account.UUID, Password: account.Password,
				VpnUsername: account.VpnUsername, Auth: account.Auth,
				Secret: account.Secret, NaiveUser: account.NaiveUser,
				Security: account.Security,
			}
		}
		// client_traffics is one row per account panel-wide, and it is what the
		// enforcement paths actually read, so it wins over the account row for the
		// three fields both carry.
		//
		// Not belt-and-braces: several paths write settings.clients and
		// client_traffics without going through the accounts layer at all, the
		// depletion sweep in disableInvalidClients most of all. Reading enable off
		// the account row showed a depleted account as still on, days after the
		// data plane had cut it off.
		if t := usage[key]; t != nil {
			row.Up, row.Down = t.Up, t.Down
			row.Enable = t.Enable
			row.TotalGB = t.Total
			row.ExpiryTime = t.ExpiryTime
		}
		createdAt[account.Id] = account.CreatedAt
		rows = append(rows, row)
	}

	// Ordered BEFORE paging, which is the whole reason this is server-side. The
	// page only ever holds one slice of the list, so sorting in the browser would
	// order fifty rows out of two hundred and call it a sort.
	// The online set is fetched only when it is the ordering being asked for. It
	// reads the running core's in-memory session list, which is cheap but not free
	// of meaning: on a box where Xray never started it is empty, and every account
	// then sorts as offline rather than the call failing.
	online := map[string]bool{}
	if sortKey == AccountSortOnline {
		var inboundService InboundService
		for _, email := range inboundService.GetOnlineClients() {
			online[accountKey(email)] = true
		}
	}
	sortKey = sortAccountRows(rows, createdAt, online, sortKey)

	total := len(rows)
	start := (page - 1) * size
	if start > total {
		start = total
	}
	end := start + size
	if end > total {
		end = total
	}
	return &AccountListResult{
		Rows: rows[start:end], Total: total, Page: page, Size: size,
		Sort: sortKey,
	}, nil
}

// sortAccountRows orders the whole filtered list in place and returns the ordering
// it actually applied, which is what the caller echoes back.
//
// An unknown or empty key falls back to "newest" rather than erroring. A sort is a
// view preference, and refusing the request would leave an operator with an error
// toast instead of a list; the echoed key is how the menu learns which item to tick.
//
// EVERY comparison falls through to a unique tie-break, and that is load-bearing
// rather than tidy. Paging is applied to this slice immediately afterwards, so with
// an unstable order two accounts comparing equal could swap between the request for
// page 1 and the request for page 2, and the operator would see one of them twice
// and the other not at all. The three status orderings tie-break on email because
// that is the useful reading inside a group; the two age orderings tie-break on id,
// which is monotonic, so accounts created in the same millisecond still come back in
// the order they were made.
func sortAccountRows(rows []AccountRow, createdAt map[int]int64, online map[string]bool, sortKey string) string {
	switch sortKey {
	case AccountSortNewest, AccountSortOldest, AccountSortOnline,
		AccountSortEnabled, AccountSortDisabled:
	default:
		sortKey = AccountSortNewest
	}

	// Each case returns "is a before b" outright. Written as a full comparison per
	// ordering rather than a shared key function plus a direction flag, because these
	// are five different questions and only two of them are about the same value.
	less := func(a, b *AccountRow) bool {
		switch sortKey {
		case AccountSortOldest:
			if createdAt[a.Id] != createdAt[b.Id] {
				return createdAt[a.Id] < createdAt[b.Id]
			}
			return a.Id < b.Id
		case AccountSortOnline:
			// Connected right now first. Not a health question: an account can be
			// online AND nearly out of data, and this ordering answers only the
			// first half.
			ao, bo := online[accountKey(a.Email)], online[accountKey(b.Email)]
			if ao != bo {
				return ao
			}
			return accountKey(a.Email) < accountKey(b.Email)
		case AccountSortEnabled:
			if a.Enable != b.Enable {
				return a.Enable
			}
			return accountKey(a.Email) < accountKey(b.Email)
		case AccountSortDisabled:
			// The mirror of the one above, and the more useful of the pair: a
			// disabled account is the one an operator is looking for, because the
			// panel switches accounts off by itself when they expire or run out.
			if a.Enable != b.Enable {
				return b.Enable
			}
			return accountKey(a.Email) < accountKey(b.Email)
		default: // AccountSortNewest
			if createdAt[a.Id] != createdAt[b.Id] {
				return createdAt[a.Id] > createdAt[b.Id]
			}
			return a.Id > b.Id
		}
	}

	sort.SliceStable(rows, func(i, j int) bool { return less(&rows[i], &rows[j]) })
	return sortKey
}

// membershipViews maps every account id to the inbounds serving it, named.
func (s *AccountService) membershipViews() (map[int][]AccountMembershipView, error) {
	db := database.GetDB()

	var inbounds []model.Inbound
	if err := db.Model(&model.Inbound{}).
		Select("id", "protocol", "remark", "port", "enable", "settings").Find(&inbounds).Error; err != nil {
		return nil, err
	}
	byId := make(map[int]*model.Inbound, len(inbounds))
	for i := range inbounds {
		(&DynamicSubscriptionService{}).SetInboundProtocolName(&inbounds[i])
		byId[inbounds[i].Id] = &inbounds[i]
	}

	// The settings blob of each inbound, so the protocol-correct identity can be
	// read off the stored entry rather than guessed.
	var full []model.Inbound
	if err := db.Model(&model.Inbound{}).Select("id", "protocol", "settings").Find(&full).Error; err != nil {
		return nil, err
	}
	identityByInbound := make(map[int]map[string]string, len(full))
	for i := range full {
		clients, ok := parseSettingsClients(full[i].Settings)
		if !ok {
			continue
		}
		m := map[string]string{}
		for _, entry := range clients {
			email, _ := entry["email"].(string)
			if accountKey(email) == "" {
				continue
			}
			key := clientIdentityKey(full[i].Protocol)
			if v, isStr := entry[key].(string); isStr {
				m[accountKey(email)] = v
			}
		}
		identityByInbound[full[i].Id] = m
	}

	var rows []model.AccountInbound
	if err := db.Order("inbound_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	emailById := map[int]string{}
	var accts []model.Account
	if err := db.Select("id", "email").Find(&accts).Error; err != nil {
		return nil, err
	}
	for i := range accts {
		emailById[accts[i].Id] = accts[i].Email
	}
	out := make(map[int][]AccountMembershipView, len(rows))
	for i := range rows {
		m := &rows[i]
		inbound, ok := byId[m.InboundId]
		if !ok {
			// A membership whose inbound is gone. Skipped rather than shown as a
			// blank row: nothing serves it, so it is not somewhere the account is.
			continue
		}
		view := AccountMembershipView{
			InboundId: inbound.Id, Protocol: string(inbound.Protocol), ProtocolName: inbound.ProtocolName,
			Remark: inbound.Remark, Port: inbound.Port, Enable: inbound.Enable,
			Slot: m.Slot,
		}
		if ids := identityByInbound[inbound.Id]; ids != nil {
			view.ClientId = ids[accountKey(emailById[m.AccountId])]
		}
		out[m.AccountId] = append(out[m.AccountId], view)
	}
	return out, nil
}

// visibilityFilter returns the account predicate and the caller's inbound grant set.
// The latter also scopes membership details in the response; nil means unrestricted
// only for a super admin. Both are built once per request rather than per row.
func (s *AccountService) visibilityFilter(user *model.User) (func(*model.Account, []AccountMembershipView) bool, map[int]bool, error) {
	if user.IsSuperAdmin {
		// nil grants mean unrestricted visibility, but only on this role-checked path.
		return func(*model.Account, []AccountMembershipView) bool { return true }, nil, nil
	}

	var adminService AdminService
	ids, err := adminService.AccessibleInboundIds(user.Id)
	if err != nil {
		return nil, nil, err
	}
	granted := make(map[int]bool, len(ids))
	for _, id := range ids {
		granted[id] = true
	}

	if user.IsReseller {
		// Narrower than their inbound grants on purpose: a reseller is legitimately
		// given a SHARED inbound, and that inbound carries other sellers' customers.
		// Scoping account visibility by grant would show them those. Membership
		// details are separately narrowed by the grant set before being returned.
		var resellerService ResellerService
		owned, err := resellerService.OwnedEmails(user.Id)
		if err != nil {
			return nil, nil, err
		}
		return func(a *model.Account, _ []AccountMembershipView) bool {
			return owned[accountKey(a.Email)]
		}, granted, nil
	}

	return func(account *model.Account, memberships []AccountMembershipView) bool {
		if account.OwnerRole == "admin" && account.OwnerUserId == user.Id {
			return true
		}
		// Accounts created before current-owner fields existed still belong to their
		// creator until the first transfer writes OwnerRole/OwnerUserId.
		if account.OwnerRole == "" && account.CreatorRole == "admin" && account.CreatorUserId == user.Id {
			return true
		}
		for _, m := range memberships {
			if granted[m.InboundId] {
				return true
			}
		}
		return false
	}, granted, nil
}

// accountMatches is the search predicate: account fields, current owner name, or
// the name and protocol of any inbound it is served on.
func accountMatches(account *model.Account, memberships []AccountMembershipView, ownerName, needle string) bool {
	if strings.Contains(strings.ToLower(account.Email), needle) ||
		strings.Contains(strings.ToLower(account.Comment), needle) ||
		strings.Contains(strings.ToLower(account.SubID), needle) ||
		strings.Contains(strings.ToLower(ownerName), needle) {
		return true
	}
	for _, m := range memberships {
		if strings.Contains(strings.ToLower(m.Remark), needle) ||
			strings.Contains(strings.ToLower(m.Protocol), needle) {
			return true
		}
	}
	return false
}

// AssignableInboundsFor returns the inbounds the caller may put an account on,
// for the page's inbound picker. Same grant the write path enforces, so the
// picker cannot offer something the save would then refuse.
// settingsHoldClients reports whether an inbound's settings carry a clients list
// at all. An explicit null is treated as an empty list for compatibility with older
// bulk deletes that marshalled an empty nil slice as null. dokodemo-door, socks, http
// and single-user shadowsocks legitimately omit the key, and parseSettingsClients
// cannot answer this: it returns ok=true for them, because "no clients array" and
// "unparseable" have to stay distinguishable there (treating the first as the second
// would delete every membership).
// inboundMethod reads settings.method, which only shadowsocks carries.
func inboundMethod(settings string) string {
	var root map[string]any
	if err := json.Unmarshal([]byte(settings), &root); err != nil || root == nil {
		return ""
	}
	m, _ := root["method"].(string)
	return m
}

func settingsHoldClients(protocol model.Protocol, settings string) bool {
	// The Xray-native wireguard inbound stores `peers`, not `clients`, so on this
	// test alone it looked like an inbound nothing could be a member of: the picker
	// never offered it, and an operator could create one and then had no way at all
	// to put an account on it. It holds clients now (see web/service/wgxray.go); the
	// array is created by the reconcile, so answer for the protocol rather than
	// waiting for the first reconcile to make the inbound assignable.
	if protocol == model.WireGuard {
		return true
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(settings), &root); err != nil || root == nil {
		return false
	}
	clients, exists := root["clients"]
	if !exists {
		return false
	}
	if clients == nil {
		return true
	}
	_, ok := clients.([]any)
	return ok
}

func (s *AccountService) AssignableInboundsFor(user *model.User) ([]AccountMembershipView, error) {
	if user == nil {
		return nil, nil
	}
	db := database.GetDB()
	var inbounds []model.Inbound
	if err := db.Model(&model.Inbound{}).
		Select("id", "protocol", "remark", "port", "enable", "settings").
		Order("id ASC").Find(&inbounds).Error; err != nil {
		return nil, err
	}

	allowed := func(int) bool { return true }
	if !user.IsSuperAdmin {
		var adminService AdminService
		ids, err := adminService.AccessibleInboundIds(user.Id)
		if err != nil {
			return nil, err
		}
		granted := make(map[int]bool, len(ids))
		for _, id := range ids {
			granted[id] = true
		}
		allowed = func(id int) bool { return granted[id] }
	}

	out := make([]AccountMembershipView, 0, len(inbounds))
	for i := range inbounds {
		in := &inbounds[i]
		if !allowed(in.Id) {
			continue
		}
		(&DynamicSubscriptionService{}).SetInboundProtocolName(in)
		// An inbound with no client list has nothing to be a member OF, and
		// offering it only produces a save the server refuses. Same rule the
		// client form's own checklist applies (modals/client_modal.html).
		if !settingsHoldClients(in.Protocol, in.Settings) {
			continue
		}
		out = append(out, AccountMembershipView{
			InboundId: in.Id, Protocol: string(in.Protocol), ProtocolName: in.ProtocolName,
			Remark: in.Remark, Port: in.Port, Enable: in.Enable,
			Method: inboundMethod(in.Settings),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].InboundId < out[j].InboundId })
	return out, nil
}
