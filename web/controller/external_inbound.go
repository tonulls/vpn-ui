package controller

import (
	"errors"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v2/logger"
	"github.com/mhsanaei/3x-ui/v2/web/service"
	"github.com/mhsanaei/3x-ui/v2/web/session"
)

type externalSubscriptionInboundForm struct {
	SlotID int    `json:"slotId" form:"slotId"`
	Remark string `json:"remark" form:"remark"`
	Enable *bool  `json:"enable" form:"enable"`
}

func (a *InboundController) externalSubscriptionSlotOptions(c *gin.Context) {
	dynamic := &service.DynamicSubscriptionService{}
	options, err := dynamic.SlotOptions()
	jsonObj(c, gin.H{
		"installed": dynamic.ExternalSelectorInstalled(),
		"slots":     options,
	}, err)
}

func (a *InboundController) createExternalSubscriptionInbound(c *gin.Context) {
	var form externalSubscriptionInboundForm
	if err := c.ShouldBind(&form); err != nil {
		jsonObj(c, nil, err)
		return
	}
	user := session.GetLoginUser(c)
	if user == nil {
		jsonObj(c, nil, errors.New("not logged in"))
		return
	}
	inbound, err := (&service.DynamicSubscriptionService{}).CreateInbound(user.Id, form.SlotID, form.Remark)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	if !user.IsSuperAdmin {
		var adminService service.AdminService
		if err := adminService.GrantInbound(user.Id, inbound.Id); err != nil {
			logger.Warning("granting the creator access to their external subscription inbound: ", err)
			var inboundService service.InboundService
			if _, cleanupErr := inboundService.DelInbound(inbound.Id); cleanupErr != nil {
				logger.Warning("cleaning up an external subscription inbound after access grant failure: ", cleanupErr)
			}
			jsonObj(c, nil, err)
			return
		}
	}
	jsonObj(c, inbound, nil)
}

func (a *InboundController) updateExternalSubscriptionInbound(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		jsonObj(c, nil, errors.New("неверный идентификатор подключения"))
		return
	}
	var form externalSubscriptionInboundForm
	if err := c.ShouldBind(&form); err != nil {
		jsonObj(c, nil, err)
		return
	}
	inbound, err := (&service.DynamicSubscriptionService{}).UpdateInbound(id, form.SlotID, form.Remark, form.Enable)
	jsonObj(c, inbound, err)
}
