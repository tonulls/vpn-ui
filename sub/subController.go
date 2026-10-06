package sub

import (
	"encoding/base64"
	"fmt"
	"html/template"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v2/config"
	"github.com/mhsanaei/3x-ui/v2/web/entity"
	"github.com/mhsanaei/3x-ui/v2/web/service"

	"github.com/gin-gonic/gin"
)

// SUBController handles HTTP requests for subscription links and JSON configurations.
type SUBController struct {
	subTitle              string
	subSupportUrl         string
	subPageTitle          string
	subFaviconUrl         string
	subShowSupport        bool
	subSupportButtonLabel string
	subProfileUrl         string
	subShowProfileUrl     bool
	subProfileButtonLabel string
	subAnnounce           string
	subEnableRouting      bool
	subRoutingRules       string
	subPath               string
	subJsonPath           string
	subClashPath          string
	jsonEnabled           bool
	clashEnabled          bool
	subEncrypt            bool
	updateInterval        string

	subService      *SubService
	subJsonService  *SubJsonService
	subClashService *SubClashService
}

// NewSUBController creates a new subscription controller with the given configuration.
func NewSUBController(
	g *gin.RouterGroup,
	subPath string,
	jsonPath string,
	clashPath string,
	jsonEnabled bool,
	clashEnabled bool,
	encrypt bool,
	showInfo bool,
	rModel string,
	update string,
	jsonFragment string,
	jsonNoise string,
	jsonMux string,
	jsonRules string,
	subTitle string,
	subSupportUrl string,
	subPageTitle string,
	subFaviconUrl string,
	subShowSupport bool,
	subProfileUrl string,
	subAnnounce string,
	subEnableRouting bool,
	subRoutingRules string,
	subShowProfileUrl bool,
	subProfileButtonLabel string,
	subSupportButtonLabel string,
) *SUBController {
	sub := NewSubService(showInfo, rModel)
	a := &SUBController{
		subTitle:              subTitle,
		subSupportUrl:         subSupportUrl,
		subPageTitle:          subPageTitle,
		subFaviconUrl:         subFaviconUrl,
		subShowSupport:        subShowSupport,
		subSupportButtonLabel: subSupportButtonLabel,
		subProfileUrl:         subProfileUrl,
		subShowProfileUrl:     subShowProfileUrl,
		subProfileButtonLabel: subProfileButtonLabel,
		subAnnounce:           subAnnounce,
		subEnableRouting:      subEnableRouting,
		subRoutingRules:       subRoutingRules,
		subPath:               subPath,
		subJsonPath:           jsonPath,
		subClashPath:          clashPath,
		jsonEnabled:           jsonEnabled,
		clashEnabled:          clashEnabled,
		subEncrypt:            encrypt,
		updateInterval:        update,

		subService:      sub,
		subJsonService:  NewSubJsonService(jsonFragment, jsonNoise, jsonMux, jsonRules, sub),
		subClashService: NewSubClashService(sub),
	}
	a.initRouter(g)
	return a
}

// initRouter registers HTTP routes for subscription links and JSON endpoints
// on the provided router group.
func (a *SUBController) initRouter(g *gin.RouterGroup) {
	gLink := g.Group(a.subPath)
	gLink.Use(subscriptionPrivacyHeaders())
	gLink.GET(":subid", a.subs)
	// Client config downloads offered by the subscriber page (OpenVPN .ovpn, wg-c/awg
	// .conf). Under the raw sub path so it inherits the same host, port and base path,
	// and so the subId stays the only credential involved.
	gLink.GET(":subid/configs/:key", a.subConfig)
	if a.jsonEnabled {
		gJson := g.Group(a.subJsonPath)
		gJson.Use(subscriptionPrivacyHeaders())
		gJson.GET(":subid", a.subJsons)
	}
	if a.clashEnabled {
		gClash := g.Group(a.subClashPath)
		gClash.Use(subscriptionPrivacyHeaders())
		gClash.GET(":subid", a.subClashs)
	}
}

// subscriptionPrivacyHeaders reduce persistence and referrer leakage for URLs
// whose path itself is the bearer credential. Clients will simply refresh on their
// configured subscription interval rather than relying on a shared HTTP cache.
func subscriptionPrivacyHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "private, no-store")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	}
}

// subs handles HTTP requests for subscription links, returning either HTML page or base64-encoded subscription data.
func (a *SUBController) subs(c *gin.Context) {
	subId := c.Param("subid")
	scheme, host, hostWithPort, hostHeader := a.subService.ResolveRequest(c)
	subs, lastOnline, traffic, err := a.subService.GetSubs(subId, host)
	// An empty link list is NOT an error: an account whose only inbounds are wg-c/awg
	// has real usage and a real expiry to report, but no single-line raw form (its
	// config comes from the Clash sub). Erroring here would hide the traffic/days page
	// from exactly those accounts. GetSubs already errors when the subId matches nothing.
	if err != nil {
		c.String(400, "Error!")
	} else {
		result := ""
		for _, sub := range subs {
			result += sub + "\n"
		}

		// If the request expects HTML (e.g., browser) or explicitly asked (?html=1 or ?view=html), render the info page here
		accept := c.GetHeader("Accept")
		if strings.Contains(strings.ToLower(accept), "text/html") || c.Query("html") == "1" || strings.EqualFold(c.Query("view"), "html") {
			// Build page data in service
			subURL, subJsonURL, subClashURL := a.subService.BuildURLs(scheme, hostWithPort, a.subPath, a.subJsonPath, a.subClashPath, subId)
			if !a.jsonEnabled {
				subJsonURL = ""
			}
			if !a.clashEnabled {
				subClashURL = ""
			}
			// Keep static assets on the configured, token-free subscription path.
			// The subId authorizes the page/config requests; it must not be copied
			// into stylesheet, script, font or image request URLs.
			basePath, exists := c.Get("base_path")
			basePathStr, ok := basePath.(string)
			if !exists || !ok || basePathStr == "" {
				basePathStr = "/"
			}
			if !strings.HasSuffix(basePathStr, "/") {
				basePathStr += "/"
			}
			page := a.subService.BuildPageData(subId, hostHeader, traffic, lastOnline, subs, subURL, subJsonURL, subClashURL, basePathStr)
			// OpenVPN and WireGuard cannot be set up from a link: the page offers their
			// config files as downloads. Rendered only for the browser view, since a
			// subscription client has no use for them.
			page.Configs = a.subService.ConfigLinks(subId, host, scheme, hostWithPort, a.subPath)

			// The donation button is only exposed when the operator has enabled and
			// configured the list. Read it when rendering so settings changes take
			// effect without restarting the subscription listener.
			var donationEntries []service.DonationEntry
			var settingService service.SettingService
			if donations, err := settingService.GetDonationSettings(); err == nil && donations.Enabled {
				donationEntries = donations.Entries
			}
			faviconURL := a.subFaviconUrl
			if currentFaviconURL, err := settingService.GetSubFaviconUrl(); err == nil {
				faviconURL = currentFaviconURL
			}
			// Custom site buttons are shared by the browser and in-app HTML view.
			// Read them for every request so saved changes are live immediately.
			showSiteButtons, siteButtonsPerRow, siteButtons, err := settingService.GetSubSiteButtons()
			if err != nil {
				showSiteButtons, siteButtonsPerRow, siteButtons = false, 2, nil
			} else {
				visibleButtons := make([]service.SubscriptionSiteButton, 0, len(siteButtons))
				for _, button := range siteButtons {
					if strings.TrimSpace(button.URL) != "" {
						visibleButtons = append(visibleButtons, button)
					}
				}
				siteButtons = visibleButtons
				showSiteButtons = showSiteButtons && len(siteButtons) > 0
			}
			siteButtonsTextAlign, siteButtonsBold, siteButtonsItalic, siteButtonsUnderline, siteButtonsStrike, styleErr := settingService.GetSubSiteButtonsTextStyle()
			if styleErr != nil {
				siteButtonsTextAlign, siteButtonsBold, siteButtonsItalic, siteButtonsUnderline, siteButtonsStrike = "left", false, false, false, false
			}
			var faviconValue any = faviconURL
			if strings.HasPrefix(strings.ToLower(faviconURL), "data:") && entity.ValidateFaviconURL(faviconURL) == nil {
				faviconValue = template.URL(faviconURL)
			}

			c.HTML(200, "subpage.html", gin.H{
				"title":                   "subscription.title",
				"page_title":              a.subPageTitle,
				"subscription_page":       true,
				"favicon_url":             faviconValue,
				"site_buttons_enabled":    showSiteButtons,
				"site_buttons_per_row":    siteButtonsPerRow,
				"site_buttons":            siteButtons,
				"site_buttons_text_align": siteButtonsTextAlign,
				"site_buttons_bold":       siteButtonsBold,
				"site_buttons_italic":     siteButtonsItalic,
				"site_buttons_underline":  siteButtonsUnderline,
				"site_buttons_strike":     siteButtonsStrike,
				"cur_ver":                 config.GetVersion(),
				"asset_ver":               config.GetAssetVersion(),
				"host":                    page.Host,
				"base_path":               page.BasePath,
				"sId":                     page.SId,
				"download":                page.Download,
				"upload":                  page.Upload,
				"total":                   page.Total,
				"used":                    page.Used,
				"remained":                page.Remained,
				"expire":                  page.Expire,
				"lastOnline":              page.LastOnline,
				"datepicker":              page.Datepicker,
				"downloadByte":            page.DownloadByte,
				"uploadByte":              page.UploadByte,
				"totalByte":               page.TotalByte,
				"subUrl":                  page.SubUrl,
				"subJsonUrl":              page.SubJsonUrl,
				"subClashUrl":             page.SubClashUrl,
				"result":                  page.Result,
				"configs":                 page.Configs,
				"donate":                  donationEntries,
			})
			return
		}

		// The app-only button points to the subscription's HTML page. The list of
		// additional links is rendered inside that page, not as raw client metadata.
		header := fmt.Sprintf("upload=%d; download=%d; total=%d; expire=%d", traffic.Up, traffic.Down, traffic.Total, traffic.ExpiryTime/1000)
		profileURL, profileURLLabel := a.clientSubscriptionPageMetadata(scheme, hostWithPort, subId)
		a.ApplyCommonHeaders(c, header, a.updateInterval, a.subTitle, "", profileURL, profileURLLabel, a.subAnnounce, a.subEnableRouting, a.subRoutingRules)

		if a.subEncrypt {
			c.String(200, base64.StdEncoding.EncodeToString([]byte(result)))
		} else {
			c.String(200, result)
		}
	}
}

// clientSubscriptionPageMetadata supplies the optional page link understood by
// subscription clients. The legacy Profile-Web-Page-Url header is retained, but
// now points at this subscription's HTML page instead of a separate profile URL.
func (a *SUBController) clientSubscriptionPageMetadata(scheme, hostWithPort, subId string) (string, string) {
	subURL, _, _ := a.subService.BuildURLs(scheme, hostWithPort, a.subPath, a.subJsonPath, a.subClashPath, subId)
	var settingService service.SettingService
	showSubscriptionURL := true
	if current, err := settingService.GetSubShowSubscriptionUrl(); err == nil {
		showSubscriptionURL = current
	}
	label := "Подписка"
	if current, err := settingService.GetSubSubscriptionButtonLabel(); err == nil {
		if strings.TrimSpace(current) != "" {
			label = strings.TrimSpace(current)
		}
	} else if strings.TrimSpace(a.subProfileButtonLabel) != "" {
		label = strings.TrimSpace(a.subProfileButtonLabel)
	}
	if !showSubscriptionURL {
		return "", label
	}
	return subURL, label
}

// subConfig serves one client config file (an OpenVPN .ovpn or a WireGuard/AmneziaWG
// .conf) for the account behind the subId. The key names an inbound and variant; anything
// that does not resolve to a config this subscription owns is a flat 404, so the route
// says nothing about inbounds the caller has no subId for.
func (a *SUBController) subConfig(c *gin.Context) {
	subId := c.Param("subid")
	_, host, _, _ := a.subService.ResolveRequest(c)
	cfg, ok := a.subService.ConfigFile(subId, host, c.Param("key"))
	if !ok {
		c.String(404, "Not found")
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", cfg.Filename))
	c.Data(200, cfg.ContentType+"; charset=utf-8", []byte(cfg.Content))
}

// subJsons handles HTTP requests for JSON subscription configurations.
func (a *SUBController) subJsons(c *gin.Context) {
	subId := c.Param("subid")
	scheme, host, hostWithPort, _ := a.subService.ResolveRequest(c)
	jsonSub, header, err := a.subJsonService.GetJson(subId, host)
	if err != nil || len(jsonSub) == 0 {
		c.String(400, "Error!")
	} else {
		profileURL, profileURLLabel := a.clientSubscriptionPageMetadata(scheme, hostWithPort, subId)
		a.ApplyCommonHeaders(c, header, a.updateInterval, a.subTitle, "", profileURL, profileURLLabel, a.subAnnounce, a.subEnableRouting, a.subRoutingRules)

		c.String(200, jsonSub)
	}
}

func (a *SUBController) subClashs(c *gin.Context) {
	subId := c.Param("subid")
	scheme, host, hostWithPort, _ := a.subService.ResolveRequest(c)
	clashSub, header, err := a.subClashService.GetClash(subId, host)
	if err != nil || len(clashSub) == 0 {
		c.String(400, "Error!")
	} else {
		profileURL, profileURLLabel := a.clientSubscriptionPageMetadata(scheme, hostWithPort, subId)
		a.ApplyCommonHeaders(c, header, a.updateInterval, a.subTitle, "", profileURL, profileURLLabel, a.subAnnounce, a.subEnableRouting, a.subRoutingRules)
		c.Data(200, "application/yaml; charset=utf-8", []byte(clashSub))
	}
}

// ApplyCommonHeaders sets common HTTP headers for subscription responses including user info, update interval, and profile title.
func (a *SUBController) ApplyCommonHeaders(
	c *gin.Context,
	header,
	updateInterval,
	profileTitle string,
	profileSupportUrl string,
	profileUrl string,
	profileUrlLabel string,
	profileAnnounce string,
	profileEnableRouting bool,
	profileRoutingRules string,
) {
	c.Writer.Header().Set("Subscription-Userinfo", header)
	c.Writer.Header().Set("Profile-Update-Interval", updateInterval)

	//Basics
	if profileTitle != "" {
		c.Writer.Header().Set("Profile-Title", "base64:"+base64.StdEncoding.EncodeToString([]byte(profileTitle)))
	}
	if profileSupportUrl != "" {
		c.Writer.Header().Set("Support-Url", profileSupportUrl)
	}
	if profileUrl != "" {
		c.Writer.Header().Set("Profile-Web-Page-Url", profileUrl)
		// Best-effort display label for clients that support this extension. Clients
		// that only understand Profile-Web-Page-Url keep their built-in button text.
		if profileUrlLabel != "" {
			c.Writer.Header().Set("Profile-Web-Page-Title", profileUrlLabel)
		}
	}
	if profileAnnounce != "" {
		c.Writer.Header().Set("Announce", "base64:"+base64.StdEncoding.EncodeToString([]byte(profileAnnounce)))
	}

	//Advanced (Happ)
	c.Writer.Header().Set("Routing-Enable", strconv.FormatBool(profileEnableRouting))
	if profileRoutingRules != "" {
		c.Writer.Header().Set("Routing", profileRoutingRules)
	}
}
