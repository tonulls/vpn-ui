package controller

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v2/web/service"
)

func (a *CoreController) externalSubscriptionList(c *gin.Context) {
	module, err := a.dynamicSubscriptionService.Module()
	jsonObj(c, module, err)
}

func (a *CoreController) updateExternalSubscriptionSettings(c *gin.Context) {
	var form service.ExternalSubscriptionSettingsView
	if err := c.ShouldBind(&form); err != nil {
		jsonObj(c, nil, err)
		return
	}
	updated, err := a.dynamicSubscriptionService.UpdateSettings(form)
	jsonObj(c, updated, err)
}

func (a *CoreController) createExternalSubscriptionSlot(c *gin.Context) {
	input, err := bindExternalSubscriptionSlotInput(c)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	created, err := a.dynamicSubscriptionService.CreateSlot(input)
	jsonObj(c, created, err)
}

func (a *CoreController) reorderExternalSubscriptionSlots(c *gin.Context) {
	var body struct {
		Data string `form:"data" json:"data"`
	}
	if err := c.ShouldBind(&body); err != nil {
		jsonObj(c, nil, err)
		return
	}
	var ids []int
	if err := json.Unmarshal([]byte(body.Data), &ids); err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonMsg(c, "Порядок подмодулей сохранён", a.dynamicSubscriptionService.ReorderSlots(ids))
}

func (a *CoreController) updateExternalSubscriptionSlot(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		jsonObj(c, nil, fmt.Errorf("неверный идентификатор подмодуля"))
		return
	}
	input, err := bindExternalSubscriptionSlotInput(c)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	updated, err := a.dynamicSubscriptionService.UpdateSlot(id, input)
	jsonObj(c, updated, err)
}

func (a *CoreController) deleteExternalSubscriptionSlot(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		jsonObj(c, nil, fmt.Errorf("неверный идентификатор подмодуля"))
		return
	}
	jsonMsg(c, "", a.dynamicSubscriptionService.DeleteSlot(id))
}

func (a *CoreController) refreshExternalSubscriptionSlot(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		jsonObj(c, nil, fmt.Errorf("неверный идентификатор подмодуля"))
		return
	}
	err = a.dynamicSubscriptionService.RequestRefresh(id)
	jsonObj(c, gin.H{"queued": err == nil}, err)
}

func (a *CoreController) nextExternalSubscriptionSlotCandidate(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		jsonObj(c, nil, fmt.Errorf("неверный идентификатор подмодуля"))
		return
	}
	err = a.dynamicSubscriptionService.RequestNextSlotCandidate(id)
	jsonObj(c, gin.H{"queued": err == nil}, err)
}

func bindExternalSubscriptionSlotInput(c *gin.Context) (service.ExternalSubscriptionSlotInput, error) {
	var input service.ExternalSubscriptionSlotInput
	if strings.Contains(strings.ToLower(c.GetHeader("Content-Type")), "application/json") {
		if err := c.ShouldBindJSON(&input); err != nil {
			return input, err
		}
		return input, nil
	}
	var form struct {
		Name                         string `form:"name"`
		ShowName                     *bool  `form:"showName"`
		SourceURL                    string `form:"sourceUrl"`
		RefreshIntervalMinutes       int    `form:"refreshIntervalMinutes"`
		CheckIntervalMinutes         int    `form:"checkIntervalMinutes"`
		SubscriptionKeyCount         int    `form:"subscriptionKeyCount"`
		ForceRotationIntervalMinutes int    `form:"forceRotationIntervalMinutes"`
		FilterMode                   string `form:"filterMode"`
		SelectionMode                string `form:"selectionMode"`
		Enabled                      *bool  `form:"enabled"`
	}
	if err := c.ShouldBind(&form); err != nil {
		return input, err
	}
	input.Name = form.Name
	input.ShowName = form.ShowName
	input.SourceURL = form.SourceURL
	input.RefreshIntervalMinutes = form.RefreshIntervalMinutes
	input.CheckIntervalMinutes = form.CheckIntervalMinutes
	input.SubscriptionKeyCount = form.SubscriptionKeyCount
	input.ForceRotationIntervalMinutes = form.ForceRotationIntervalMinutes
	input.FilterMode = form.FilterMode
	input.SelectionMode = form.SelectionMode
	input.Enabled = form.Enabled
	if err := c.Request.ParseForm(); err != nil {
		return input, err
	}
	for key, values := range c.Request.Form {
		if key != "countryFlags" && key != "countryFlags[]" && !strings.HasPrefix(key, "countryFlags[") {
			continue
		}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			var decoded []string
			if strings.HasPrefix(value, "[") && json.Unmarshal([]byte(value), &decoded) == nil {
				input.CountryFlags = append(input.CountryFlags, decoded...)
				continue
			}
			input.CountryFlags = append(input.CountryFlags, strings.Split(value, ",")...)
		}
	}
	return input, nil
}
