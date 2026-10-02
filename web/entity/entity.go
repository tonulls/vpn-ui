// Package entity defines data structures and entities used by the web layer of the vpn-ui panel.
package entity

import (
	"crypto/tls"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v2/util/common"
	"github.com/mhsanaei/3x-ui/v2/web/proxyip"
)

// Msg represents a standard API response message with success status, message text, and optional data object.
type Msg struct {
	Success bool   `json:"success"` // Indicates if the operation was successful
	Msg     string `json:"msg"`     // Response message text
	Obj     any    `json:"obj"`     // Optional data object
}

// AllSetting contains all configuration settings for the vpn-ui panel including web server, Telegram bot, and subscription settings.
type AllSetting struct {
	// Web server settings
	WebListen          string `json:"webListen" form:"webListen"`                   // Web server listen IP address
	WebTrustedProxies  string `json:"webTrustedProxies" form:"webTrustedProxies"`   // Trusted reverse proxies for the panel
	XrayTrustedProxies string `json:"xrayTrustedProxies" form:"xrayTrustedProxies"` // Trusted reverse proxies for Xray HTTP transports
	WebDomain          string `json:"webDomain" form:"webDomain"`                   // Web server domain for domain validation
	WebPort            int    `json:"webPort" form:"webPort"`                       // Web server port number
	WebCertFile        string `json:"webCertFile" form:"webCertFile"`               // Path to SSL certificate file for web server
	WebKeyFile         string `json:"webKeyFile" form:"webKeyFile"`                 // Path to SSL private key file for web server
	WebBasePath        string `json:"webBasePath" form:"webBasePath"`               // Base path for web panel URLs
	SessionMaxAge      int    `json:"sessionMaxAge" form:"sessionMaxAge"`           // Session maximum age in minutes

	// UI settings
	PageSize    int    `json:"pageSize" form:"pageSize"`       // Number of items per page in lists
	ExpireDiff  int    `json:"expireDiff" form:"expireDiff"`   // Expiration warning threshold in days
	TrafficDiff int    `json:"trafficDiff" form:"trafficDiff"` // Traffic warning threshold percentage
	RemarkModel string `json:"remarkModel" form:"remarkModel"` // Remark model pattern for inbounds
	Datepicker  string `json:"datepicker" form:"datepicker"`   // Date picker format

	// Telegram bot settings
	TgBotEnable             bool   `json:"tgBotEnable" form:"tgBotEnable"`       // Enable Telegram bot
	TgBotToken              string `json:"tgBotToken" form:"tgBotToken"`         // Telegram bot token
	TgBotProxy              string `json:"tgBotProxy" form:"tgBotProxy"`         // Proxy URL for Telegram bot
	TgBotAPIServer          string `json:"tgBotAPIServer" form:"tgBotAPIServer"` // Custom API server for Telegram bot
	TgBotXrayRoutingEnabled bool   `json:"tgBotXrayRoutingEnabled" form:"tgBotXrayRoutingEnabled"`
	TgBotXrayInboundTag     string `json:"tgBotXrayInboundTag" form:"tgBotXrayInboundTag"`
	TgBotXrayOutboundTag    string `json:"tgBotXrayOutboundTag" form:"tgBotXrayOutboundTag"`
	TgBotXrayBalancerTag    string `json:"tgBotXrayBalancerTag" form:"tgBotXrayBalancerTag"`
	TgBotChatId             string `json:"tgBotChatId" form:"tgBotChatId"`                     // Main Telegram bot admin user IDs; required when bot is enabled
	TgBotAdditionalChatId   string `json:"tgBotAdditionalChatId" form:"tgBotAdditionalChatId"` // Optional additional Telegram bot admin user IDs
	TgRunTime               string `json:"tgRunTime" form:"tgRunTime"`                         // Legacy report schedule (retained for DB compatibility)
	TgLang                  string `json:"tgLang" form:"tgLang"`                               // Telegram bot language
	TgForumEnable           bool   `json:"tgForumEnable" form:"tgForumEnable"`                 // Enable Telegram forum destination
	TgForumChatId           string `json:"tgForumChatId" form:"tgForumChatId"`                 // Telegram forum supergroup chat ID
	TgNotifyDirect          bool   `json:"tgNotifyDirect" form:"tgNotifyDirect"`               // Send event notifications to admins privately
	TgNotifyForum           bool   `json:"tgNotifyForum" form:"tgNotifyForum"`                 // Send event notifications to forum
	TgNotifyLoginSuccess    bool   `json:"tgNotifyLoginSuccess" form:"tgNotifyLoginSuccess"`   // Notify successful logins
	TgNotifyLoginFailure    bool   `json:"tgNotifyLoginFailure" form:"tgNotifyLoginFailure"`   // Notify failed logins
	TgNotifyCPU             bool   `json:"tgNotifyCPU" form:"tgNotifyCPU"`                     // Notify CPU threshold crossings
	TgCpu                   int    `json:"tgCpu" form:"tgCpu"`                                 // CPU usage threshold for alerts
	TgTopicLoginSuccess     string `json:"tgTopicLoginSuccess" form:"tgTopicLoginSuccess"`     // Forum thread ID for successful logins
	TgTopicLoginFailure     string `json:"tgTopicLoginFailure" form:"tgTopicLoginFailure"`     // Forum thread ID for failed logins
	TgTopicCPU              string `json:"tgTopicCPU" form:"tgTopicCPU"`                       // Forum thread ID for CPU alerts
	TgBackupEnable          bool   `json:"tgBackupEnable" form:"tgBackupEnable"`               // Enable scheduled database backup
	TgBackupIntervalHours   int    `json:"tgBackupIntervalHours" form:"tgBackupIntervalHours"` // Backup interval in hours
	TgBackupTopicId         string `json:"tgBackupTopicId" form:"tgBackupTopicId"`             // Forum thread ID for backups
	TgBackupEncrypt         bool   `json:"tgBackupEncrypt" form:"tgBackupEncrypt"`             // Encrypt backup archive
	TgBackupPassword        string `json:"tgBackupPassword" form:"tgBackupPassword"`           // Write-only backup archive password

	// Security settings
	TimeLocation    string `json:"timeLocation" form:"timeLocation"`       // Time zone location
	TwoFactorEnable bool   `json:"twoFactorEnable" form:"twoFactorEnable"` // Enable two-factor authentication
	TwoFactorToken  string `json:"twoFactorToken" form:"twoFactorToken"`   // Two-factor authentication token

	// Subscription server settings
	SubEnable                   bool   `json:"subEnable" form:"subEnable"`                                     // Enable subscription server
	SubJsonEnable               bool   `json:"subJsonEnable" form:"subJsonEnable"`                             // Enable JSON subscription endpoint
	SubTitle                    string `json:"subTitle" form:"subTitle"`                                       // Subscription title for client configuration
	SubPageTitle                string `json:"subPageTitle" form:"subPageTitle"`                               // Browser title for the subscription page
	SubFaviconUrl               string `json:"subFaviconUrl" form:"subFaviconUrl"`                             // Favicon URL for the subscription page
	SubShowSupport              bool   `json:"subShowSupport" form:"subShowSupport"`                           // Show support link on the subscription page
	SubSupportUrl               string `json:"subSupportUrl" form:"subSupportUrl"`                             // Subscription support URL
	SubSupportButtonLabel       string `json:"subSupportButtonLabel" form:"subSupportButtonLabel"`             // Label for the support button on the subscription page
	SubShowProfileUrl           bool   `json:"subShowProfileUrl" form:"subShowProfileUrl"`                     // Show profile URL button on the subscription page
	SubProfileButtonLabel       string `json:"subProfileButtonLabel" form:"subProfileButtonLabel"`             // Label for the profile URL button on the subscription page
	SubProfileUrl               string `json:"subProfileUrl" form:"subProfileUrl"`                             // Subscription profile URL
	SubAnnounce                 string `json:"subAnnounce" form:"subAnnounce"`                                 // Subscription announce
	SubEnableRouting            bool   `json:"subEnableRouting" form:"subEnableRouting"`                       // Enable routing for subscription
	SubRoutingRules             string `json:"subRoutingRules" form:"subRoutingRules"`                         // Subscription global routing rules (Only for Happ)
	SubListen                   string `json:"subListen" form:"subListen"`                                     // Subscription server listen IP
	SubPort                     int    `json:"subPort" form:"subPort"`                                         // Subscription server port
	SubPath                     string `json:"subPath" form:"subPath"`                                         // Base path for subscription URLs
	SubDomain                   string `json:"subDomain" form:"subDomain"`                                     // Domain for subscription server validation
	SubCertFile                 string `json:"subCertFile" form:"subCertFile"`                                 // SSL certificate file for subscription server
	SubKeyFile                  string `json:"subKeyFile" form:"subKeyFile"`                                   // SSL private key file for subscription server
	SubUpdates                  int    `json:"subUpdates" form:"subUpdates"`                                   // Subscription update interval in minutes
	ExternalTrafficInformEnable bool   `json:"externalTrafficInformEnable" form:"externalTrafficInformEnable"` // Enable external traffic reporting
	ExternalTrafficInformURI    string `json:"externalTrafficInformURI" form:"externalTrafficInformURI"`       // URI for external traffic reporting
	SubEncrypt                  bool   `json:"subEncrypt" form:"subEncrypt"`                                   // Encrypt subscription responses
	SubShowInfo                 bool   `json:"subShowInfo" form:"subShowInfo"`                                 // Show client information in subscriptions
	SubURI                      string `json:"subURI" form:"subURI"`                                           // Subscription server URI
	SubJsonPath                 string `json:"subJsonPath" form:"subJsonPath"`                                 // Path for JSON subscription endpoint
	SubJsonURI                  string `json:"subJsonURI" form:"subJsonURI"`                                   // JSON subscription server URI
	SubClashEnable              bool   `json:"subClashEnable" form:"subClashEnable"`                           // Enable Clash/Mihomo subscription endpoint
	SubClashPath                string `json:"subClashPath" form:"subClashPath"`                               // Path for Clash/Mihomo subscription endpoint
	SubClashURI                 string `json:"subClashURI" form:"subClashURI"`                                 // Clash/Mihomo subscription server URI
	SubJsonFragment             string `json:"subJsonFragment" form:"subJsonFragment"`                         // JSON subscription fragment configuration
	SubJsonNoises               string `json:"subJsonNoises" form:"subJsonNoises"`                             // JSON subscription noise configuration
	SubJsonMux                  string `json:"subJsonMux" form:"subJsonMux"`                                   // JSON subscription mux configuration
	SubJsonRules                string `json:"subJsonRules" form:"subJsonRules"`

	// LDAP settings
	LdapEnable     bool   `json:"ldapEnable" form:"ldapEnable"`
	LdapHost       string `json:"ldapHost" form:"ldapHost"`
	LdapPort       int    `json:"ldapPort" form:"ldapPort"`
	LdapUseTLS     bool   `json:"ldapUseTLS" form:"ldapUseTLS"`
	LdapBindDN     string `json:"ldapBindDN" form:"ldapBindDN"`
	LdapPassword   string `json:"ldapPassword" form:"ldapPassword"`
	LdapBaseDN     string `json:"ldapBaseDN" form:"ldapBaseDN"`
	LdapUserFilter string `json:"ldapUserFilter" form:"ldapUserFilter"`
	LdapUserAttr   string `json:"ldapUserAttr" form:"ldapUserAttr"` // e.g., mail or uid
	LdapVlessField string `json:"ldapVlessField" form:"ldapVlessField"`
	LdapSyncCron   string `json:"ldapSyncCron" form:"ldapSyncCron"`
	// Generic flag configuration
	LdapFlagField         string `json:"ldapFlagField" form:"ldapFlagField"`
	LdapTruthyValues      string `json:"ldapTruthyValues" form:"ldapTruthyValues"`
	LdapInvertFlag        bool   `json:"ldapInvertFlag" form:"ldapInvertFlag"`
	LdapInboundTags       string `json:"ldapInboundTags" form:"ldapInboundTags"`
	LdapAutoCreate        bool   `json:"ldapAutoCreate" form:"ldapAutoCreate"`
	LdapAutoDelete        bool   `json:"ldapAutoDelete" form:"ldapAutoDelete"`
	LdapDefaultTotalGB    int    `json:"ldapDefaultTotalGB" form:"ldapDefaultTotalGB"`
	LdapDefaultExpiryDays int    `json:"ldapDefaultExpiryDays" form:"ldapDefaultExpiryDays"`
	LdapDefaultLimitIP    int    `json:"ldapDefaultLimitIP" form:"ldapDefaultLimitIP"`
	// JSON subscription routing rules
}

// normalizeForumChatID accepts Telegram supergroup IDs with or without the
// canonical minus sign and returns the negative -100… form.
func normalizeForumChatID(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	digits := strings.TrimPrefix(raw, "-")
	if strings.HasPrefix(raw, "+") || !strings.HasPrefix(digits, "100") {
		return "", common.NewError("forum chat ID must be a Telegram supergroup ID beginning with 100")
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", common.NewError("forum chat ID must contain only digits and an optional leading minus")
		}
	}
	if _, err := strconv.ParseInt(digits, 10, 64); err != nil {
		return "", common.NewError("forum chat ID is outside the supported numeric range")
	}
	return "-" + digits, nil
}

func normalizeTelegramUserIDs(raw, label string, required bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if required {
			return "", common.NewError(label, " is required when Telegram bot is enabled")
		}
		return "", nil
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, part := range strings.Split(raw, ",") {
		idText := strings.TrimSpace(part)
		if idText == "" {
			return "", common.NewError(label, " contains an empty ID")
		}
		id, err := strconv.ParseInt(idText, 10, 64)
		if err != nil || id <= 0 {
			return "", common.NewError(label, " must contain positive Telegram user IDs separated by commas")
		}
		canonical := strconv.FormatInt(id, 10)
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		ids = append(ids, canonical)
	}
	if required && len(ids) == 0 {
		return "", common.NewError(label, " is required when Telegram bot is enabled")
	}
	return strings.Join(ids, ","), nil
}

// CheckValid validates all settings in the AllSetting struct, checking IP addresses, ports, SSL certificates, and other configuration values.
func (s *AllSetting) CheckValid() error {
	s.TgBotToken = strings.TrimSpace(s.TgBotToken)
	if s.TgBotEnable && s.TgBotToken == "" {
		return common.NewError("Telegram bot token is required when Telegram bot is enabled")
	}
	mainAdminIDs, err := normalizeTelegramUserIDs(s.TgBotChatId, "main Telegram bot admin user ID", s.TgBotEnable)
	if err != nil {
		return err
	}
	s.TgBotChatId = mainAdminIDs
	additionalAdminIDs, err := normalizeTelegramUserIDs(s.TgBotAdditionalChatId, "additional Telegram bot admin user ID", false)
	if err != nil {
		return err
	}
	s.TgBotAdditionalChatId = additionalAdminIDs

	forumChatID, err := normalizeForumChatID(s.TgForumChatId)
	if err != nil {
		return err
	}
	s.TgForumChatId = forumChatID
	if s.TgForumEnable && forumChatID == "" {
		return common.NewError("forum chat ID is required when Telegram forum is enabled")
	}
	if s.TgNotifyForum && !s.TgForumEnable {
		return common.NewError("enable the Telegram forum before enabling forum notifications")
	}
	if s.TgBackupEnable {
		if !s.TgBotEnable {
			return common.NewError("enable the Telegram bot before enabling backups")
		}
		directBackupAvailable := s.TgNotifyDirect && mainAdminIDs != ""
		forumBackupAvailable := s.TgNotifyForum && s.TgForumEnable && forumChatID != ""
		if !directBackupAvailable && !forumBackupAvailable {
			return common.NewError("enable at least one backup delivery channel: private chat or forum")
		}
	}
	if s.TgBackupIntervalHours == 0 {
		s.TgBackupIntervalHours = 24
	}
	if s.TgBackupIntervalHours < 1 || s.TgBackupIntervalHours > 8760 {
		return common.NewError("backup interval must be between 1 and 8760 hours")
	}
	if s.TgCpu == 0 {
		s.TgCpu = 80
	}
	if s.TgCpu < 1 || s.TgCpu > 100 {
		return common.NewError("CPU threshold must be between 1 and 100 percent")
	}
	if s.TgBackupEncrypt && strings.TrimSpace(s.TgBackupPassword) == "" {
		return common.NewError("backup password is required when archive encryption is enabled")
	}
	for _, topicID := range []string{s.TgTopicLoginSuccess, s.TgTopicLoginFailure, s.TgTopicCPU, s.TgBackupTopicId} {
		if topicID == "" {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSpace(topicID), 10, 64)
		if err != nil || id <= 0 {
			return common.NewError("forum topic IDs must be positive integers")
		}
	}

	if s.WebListen != "" {
		ip := net.ParseIP(s.WebListen)
		if ip == nil {
			return common.NewError("web listen is not valid ip:", s.WebListen)
		}
	}

	trustedProxies, err := proxyip.NormalizeTrustedProxies(s.WebTrustedProxies)
	if err != nil {
		return common.NewError("trusted reverse proxies must be valid IP addresses or CIDRs:", err)
	}
	if trustedProxies == "" {
		trustedProxies = proxyip.DefaultTrustedProxyCIDRs
	}
	s.WebTrustedProxies = trustedProxies
	trustedXrayProxies, err := proxyip.NormalizeTrustedProxies(s.XrayTrustedProxies)
	if err != nil {
		return common.NewError("trusted Xray proxies must be valid IP addresses or CIDRs:", err)
	}
	if trustedXrayProxies == "" {
		trustedXrayProxies = proxyip.DefaultTrustedProxyCIDRs
	}
	s.XrayTrustedProxies = trustedXrayProxies

	if s.SubListen != "" {
		ip := net.ParseIP(s.SubListen)
		if ip == nil {
			return common.NewError("Sub listen is not valid ip:", s.SubListen)
		}
	}

	if s.WebPort <= 0 || s.WebPort > math.MaxUint16 {
		return common.NewError("web port is not a valid port:", s.WebPort)
	}

	if s.SubPort <= 0 || s.SubPort > math.MaxUint16 {
		return common.NewError("Sub port is not a valid port:", s.SubPort)
	}

	if (s.SubPort == s.WebPort) && (s.WebListen == s.SubListen) {
		return common.NewError("Sub and Web could not use same ip:port, ", s.SubListen, ":", s.SubPort, " & ", s.WebListen, ":", s.WebPort)
	}

	if s.WebCertFile != "" || s.WebKeyFile != "" {
		_, err := tls.LoadX509KeyPair(s.WebCertFile, s.WebKeyFile)
		if err != nil {
			return common.NewErrorf("cert file <%v> or key file <%v> invalid: %v", s.WebCertFile, s.WebKeyFile, err)
		}
	}

	if s.SubCertFile != "" || s.SubKeyFile != "" {
		_, err := tls.LoadX509KeyPair(s.SubCertFile, s.SubKeyFile)
		if err != nil {
			return common.NewErrorf("cert file <%v> or key file <%v> invalid: %v", s.SubCertFile, s.SubKeyFile, err)
		}
	}

	if !strings.HasPrefix(s.WebBasePath, "/") {
		s.WebBasePath = "/" + s.WebBasePath
	}
	if !strings.HasSuffix(s.WebBasePath, "/") {
		s.WebBasePath += "/"
	}
	if !strings.HasPrefix(s.SubPath, "/") {
		s.SubPath = "/" + s.SubPath
	}
	if !strings.HasSuffix(s.SubPath, "/") {
		s.SubPath += "/"
	}

	if !strings.HasPrefix(s.SubJsonPath, "/") {
		s.SubJsonPath = "/" + s.SubJsonPath
	}
	if !strings.HasSuffix(s.SubJsonPath, "/") {
		s.SubJsonPath += "/"
	}

	if !strings.HasPrefix(s.SubClashPath, "/") {
		s.SubClashPath = "/" + s.SubClashPath
	}
	if !strings.HasSuffix(s.SubClashPath, "/") {
		s.SubClashPath += "/"
	}

	_, err = time.LoadLocation(s.TimeLocation)
	if err != nil {
		return common.NewError("time location not exist:", s.TimeLocation)
	}

	return nil
}
