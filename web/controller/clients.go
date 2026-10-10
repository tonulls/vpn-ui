package controller

import (
	"strconv"

	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/mhsanaei/3x-ui/v2/web/session"

	"github.com/gin-gonic/gin"
)

// The Clients page: the account-centric view of who exists, as opposed to the
// Inbounds page's view of what serves them.
//
// This controller serves the account-centric Clients page. Ordinary account
// mutations still use the established inbound routes; only super-admin transfers
// have a dedicated endpoint because they atomically replace ownership, memberships,
// credentials and reseller accounting as one operation per account.
//
// The one account state those routes cannot express - an account NO inbound serves,
// which has no settings blob to be spliced into and no protocol to be addressed by -
// has two routes of its own, saveAccount and delAccount. They live on the same
// /panel/api/inbounds group and behind the same permission bits for exactly the
// reason above: the guards belong together, not on either side of a page boundary.
type ClientsController struct {
	BaseController
}

func NewClientsController(g *gin.RouterGroup) *ClientsController {
	a := &ClientsController{}
	a.initRouter(g)
	return a
}

func (a *ClientsController) initRouter(g *gin.RouterGroup) {
	// Inbound access or any client-action permission opens this account-centric view;
	// callers who may manage clients need its list even when they cannot administer
	// the inbounds themselves. Rows are narrowed per caller inside ListAccounts.
	g.GET("/list", a.list)
	g.GET("/assignable", a.assignable)
	g.POST("/transfer", requireSuperAdmin(), a.transfer)
}

// list returns one page of accounts the caller may see.
//
// The scoping lives in the service and is the whole security surface here: the
// accounts and memberships tables are panel-wide, so an unscoped read would hand
// every admin every other admin's customers and every reseller every other
// seller's. Passing the login user rather than a flag keeps that decision in one
// place.
// The sort is a query parameter and not a body field because this is a GET, and it
// is validated in the service rather than here: an unknown key falls back to
// "newest" there, so there is nothing for this handler to reject. There is no
// direction parameter on purpose - each ordering the page offers already says which
// way it runs. The result echoes back the ordering that was actually applied.
func (a *ClientsController) list(c *gin.Context) {
	page, _ := strconv.Atoi(c.Query("page"))
	size, _ := strconv.Atoi(c.Query("size"))
	result, err := accountService.ListAccounts(session.GetLoginUser(c), page, size,
		c.Query("search"), c.Query("sort"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
}

// assignable lists the inbounds the caller may put an account on, for the page's
// inbound picker.
//
// Filtered by the same grant the write path enforces, so the picker cannot offer
// an inbound the save would then refuse: an operator ticking a box and getting
// "not found" back with no explanation is the worst version of this.
func (a *ClientsController) assignable(c *gin.Context) {
	rows, err := accountService.AssignableInboundsFor(session.GetLoginUser(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, rows, nil)
}

// transfer performs one independent database transaction per selected account,
// then reconciles every inbound touched by accounts that committed successfully.
func (a *ClientsController) transfer(c *gin.Context) {
	var req service.AccountTransferRequest
	if err := c.ShouldBind(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	result, err := (&service.AccountTransferService{}).TransferAccounts(session.GetLoginUser(c), req)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if len(result.TouchedInboundIds) > 0 {
		(&InboundController{}).reconcileForInbounds(result.TouchedInboundIds, true)
	}
	jsonObj(c, result, nil)
}
