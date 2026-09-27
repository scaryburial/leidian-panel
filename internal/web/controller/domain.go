package controller

import (
	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/util/totp"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/domain"
)

// DomainController exposes the panel's 域名功能 (automatic Cloudflare subdomain
// + certificate + WS inbound switchover). Every state-changing call that touches
// Cloudflare or the inbounds is gated by the operator's TOTP code.
type DomainController struct {
	settingService service.SettingService
	domainService  domain.Manager
}

func NewDomainController(g *gin.RouterGroup) *DomainController {
	a := &DomainController{}
	g.GET("/config", a.getConfig)
	g.POST("/preview", a.preview)
	g.POST("/enable", a.enable)
	g.POST("/disable", a.disable)
	g.POST("/token", a.setToken)
	g.POST("/otp", a.setOtp)
	return a
}

func (a *DomainController) getConfig(c *gin.Context) {
	jsonObj(c, a.domainService.Status(), nil)
}

type domainPreviewReq struct {
	Subdomain string `json:"subdomain"`
}

func (a *DomainController) preview(c *gin.Context) {
	var req domainPreviewReq
	_ = c.ShouldBindJSON(&req)
	jsonObj(c, a.domainService.Preview(req.Subdomain), nil)
}

type domainEnableReq struct {
	Subdomain string `json:"subdomain"`
	Totp      string `json:"totp"`
}

func (a *DomainController) enable(c *gin.Context) {
	var req domainEnableReq
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, "参数错误："+err.Error(), err)
		return
	}
	res, err := a.domainService.Enable(c.Request.Context(), req.Subdomain, req.Totp)
	if err != nil {
		jsonMsg(c, err.Error(), nil)
		return
	}
	jsonObj(c, res, nil)
}

type domainTotpReq struct {
	Totp string `json:"totp"`
}

func (a *DomainController) disable(c *gin.Context) {
	var req domainTotpReq
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, "参数错误："+err.Error(), err)
		return
	}
	if _, err := a.domainService.Disable(c.Request.Context(), req.Totp); err != nil {
		jsonMsg(c, err.Error(), nil)
		return
	}
	jsonObj(c, a.domainService.Status(), nil)
}

type domainTokenReq struct {
	Token string `json:"token"`
	Totp  string `json:"totp"`
}

func (a *DomainController) setToken(c *gin.Context) {
	var req domainTokenReq
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, "参数错误："+err.Error(), err)
		return
	}
	if !a.domainService.Authorize(req.Totp) {
		jsonMsg(c, "动态码校验失败（TOTP）", nil)
		return
	}
	if err := a.settingService.SetCFApiToken(req.Token); err != nil {
		jsonMsg(c, "保存失败", err)
		return
	}
	jsonMsg(c, "已保存", nil)
}

type domainOtpReq struct {
	Secret string `json:"secret"`
	Totp   string `json:"totp"`
}

func (a *DomainController) setOtp(c *gin.Context) {
	var req domainOtpReq
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, "参数错误："+err.Error(), err)
		return
	}
	// 已配置密钥时，替换必须先出示当前动态码。
	//
	// 否则任何持有会话的人都能把自己的 base32 写进来，再用自己的动态码合法调用
	// /domain/token、/domain/enable、/domain/disable —— 这个端点就成了绕过第二
	// 因子的后门，而 setToken/Enable/Disable 全都依赖这道门。
	//
	// 首次设置（尚无密钥）没有可用动态码可验，只能放行引导路径；线上构建已通过
	// -ldflags 注入默认密钥，因此正常情况下走的始终是校验分支。
	if a.domainService.OtpConfigured() && !a.domainService.Authorize(req.Totp) {
		jsonMsg(c, "动态码校验失败（TOTP）", nil)
		return
	}
	if !totp.ValidSecret(req.Secret) {
		jsonMsg(c, "密钥格式不正确（需要 base32，例如 JBSWY3DPEHPK3PXP）", nil)
		return
	}
	if err := a.settingService.SetDomainOtpSecret(req.Secret); err != nil {
		jsonMsg(c, "保存失败", err)
		return
	}
	jsonMsg(c, "已保存", nil)
}
