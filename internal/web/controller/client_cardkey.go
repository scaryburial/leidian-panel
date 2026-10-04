package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// Card key ("卡密") endpoints. Both are ordinary admin API routes: they are not
// in the monitor or node-sync token allowlists, so only a session login or an
// admin API token can reach them.

// generateCardKeys creates a batch of clients on one inbound and gives each of
// them a card key. It goes through ClientService.GenerateCardKeys, which uses
// the same bulk-create path as POST /clients/bulkCreate — inbounds, the
// normalised client tables, hot reload and node fan-out all behave as they do
// for a normal create.
func (a *ClientController) generateCardKeys(c *gin.Context) {
	var params service.CardKeyGenerateParams
	if err := c.ShouldBindJSON(&params); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	result, needRestart, err := a.clientService.GenerateCardKeys(&a.inboundService, params)
	// Flagged before the error check, like create/bulkCreate: a partly applied
	// batch committed real clients and those still need the restart.
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	if needRestart || err == nil {
		notifyClientsChanged()
	}
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
}

// exportCardKeys streams the card keys as a CSV download. Every client is
// listed, including the ones that have no card key yet (rendered as 未生成),
// because the point of the export is to see what still needs generating.
func (a *ClientController) exportCardKeys(c *gin.Context) {
	inboundID := 0
	if raw := strings.TrimSpace(c.Query("inboundId")); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id < 0 {
			jsonMsg(c, I18nWeb(c, "somethingWentWrong"), fmt.Errorf("invalid inboundId %q", raw))
			return
		}
		inboundID = id
	}
	rows, err := a.clientService.ExportCardKeyRows(inboundID)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	var buf bytes.Buffer
	if err := service.WriteCardKeyCSV(&buf, rows); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	name := "cardkeys"
	if inboundID > 0 {
		name = fmt.Sprintf("cardkeys-inbound-%d", inboundID)
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	c.Data(http.StatusOK, "text/csv; charset=utf-8", buf.Bytes())
}
