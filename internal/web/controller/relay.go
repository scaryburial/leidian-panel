package controller

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	relayOutboundTag = "ui3344-relay"
	relayTestURL     = "https://api.ipify.org"
)

// RelayConfig is the persisted 出口中转 configuration: the panel relays every
// (or the selected) client's egress through an upstream SOCKS5/HTTP proxy.
type RelayConfig struct {
	Enable   bool     `json:"enable"`
	Type     string   `json:"type"` // "socks" | "http"
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	User     string   `json:"user"`
	Pass     string   `json:"pass"`
	Scope    string   `json:"scope"` // "all" | "emails" | "inbounds"
	Emails   []string `json:"emails"`
	Inbounds []string `json:"inbounds"` // inbound tags
}

// RelayController exposes a simplified outbound-relay (中转) API on top of the
// Xray config template, so operators do not have to hand-edit outbounds/rules.
type RelayController struct {
	settingService     service.SettingService
	xraySettingService service.XraySettingService
	xrayService        service.XrayService
}

func NewRelayController(g *gin.RouterGroup) *RelayController {
	a := &RelayController{}
	g.GET("/config", a.getConfig)
	g.POST("/config", a.saveConfig)
	g.POST("/test", a.test)
	return a
}

func (a *RelayController) getConfig(c *gin.Context) {
	cfg := RelayConfig{Type: "socks", Scope: "all", Port: 1080}
	if raw, err := a.settingService.GetRelayConfig(); err == nil && strings.TrimSpace(raw) != "" {
		if uerr := json.Unmarshal([]byte(raw), &cfg); uerr != nil {
			jsonMsg(c, "加载中转配置失败", uerr)
			return
		}
	}
	jsonObj(c, cfg, nil)
}

func (a *RelayController) saveConfig(c *gin.Context) {
	var cfg RelayConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		jsonMsg(c, "参数错误", err)
		return
	}
	cfg.Type = strings.ToLower(strings.TrimSpace(cfg.Type))
	if cfg.Type != "socks" && cfg.Type != "http" {
		cfg.Type = "socks"
	}
	switch cfg.Scope {
	case "emails", "inbounds":
	default:
		cfg.Scope = "all"
	}
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.Emails = cleanRelayList(cfg.Emails)
	cfg.Inbounds = cleanRelayList(cfg.Inbounds)
	if cfg.Enable {
		if cfg.Host == "" || cfg.Port <= 0 || cfg.Port > 65535 {
			jsonMsg(c, "请填写正确的上游地址与端口", nil)
			return
		}
		if cfg.Scope == "emails" && len(cfg.Emails) == 0 {
			jsonMsg(c, "请至少选择一个客户端", nil)
			return
		}
		if cfg.Scope == "inbounds" && len(cfg.Inbounds) == 0 {
			jsonMsg(c, "请至少选择一个入站", nil)
			return
		}
	}
	template, err := a.settingService.GetXrayConfigTemplate()
	if err != nil {
		jsonMsg(c, "读取 Xray 配置失败", err)
		return
	}
	updated, err := applyRelayToTemplate(template, cfg)
	if err != nil {
		jsonMsg(c, "生成 Xray 配置失败", err)
		return
	}
	if err := a.xraySettingService.SaveXraySetting(updated); err != nil {
		jsonMsg(c, "保存 Xray 配置失败", err)
		return
	}
	b, _ := json.Marshal(cfg)
	if err := a.settingService.SetRelayConfig(string(b)); err != nil {
		jsonMsg(c, "保存中转设置失败", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	if a.xrayService.IsXrayRunning() {
		_ = a.xrayService.RestartXray(false)
	}
	jsonMsg(c, "已保存", nil)
}

func (a *RelayController) test(c *gin.Context) {
	var cfg RelayConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		jsonMsg(c, "参数错误", err)
		return
	}
	ip, err := testRelay(cfg)
	if err != nil {
		jsonMsg(c, "测试失败", err)
		return
	}
	jsonObj(c, map[string]any{"egressIp": ip}, nil)
}

// applyRelayToTemplate removes any previous relay outbound/rule and, when
// enabled, injects the current one. The relay outbound is always tagged so it
// can be found again regardless of other edits to the template.
func applyRelayToTemplate(template string, cfg RelayConfig) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(template), &doc); err != nil {
		return "", err
	}
	outbounds, _ := doc["outbounds"].([]any)
	outbounds = removeOutboundByTag(outbounds, relayOutboundTag)
	if cfg.Enable {
		outbounds = append(outbounds, relayOutbound(cfg))
	}
	doc["outbounds"] = outbounds

	routing, _ := doc["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{"domainStrategy": "AsIs"}
	}
	rules, _ := routing["rules"].([]any)
	rules = removeRulesByOutbound(rules, relayOutboundTag)
	if cfg.Enable {
		rules = append(rules, relayRule(cfg))
	}
	routing["rules"] = rules
	doc["routing"] = routing

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func relayOutbound(cfg RelayConfig) map[string]any {
	server := map[string]any{"address": cfg.Host, "port": cfg.Port}
	if cfg.User != "" || cfg.Pass != "" {
		server["users"] = []any{map[string]any{"user": cfg.User, "pass": cfg.Pass}}
	}
	return map[string]any{
		"tag":      relayOutboundTag,
		"protocol": cfg.Type,
		"settings": map[string]any{"servers": []any{server}},
	}
}

func relayRule(cfg RelayConfig) map[string]any {
	rule := map[string]any{"type": "field", "outboundTag": relayOutboundTag}
	switch cfg.Scope {
	case "emails":
		rule["user"] = toAnyStrings(cfg.Emails)
	case "inbounds":
		rule["inboundTag"] = toAnyStrings(cfg.Inbounds)
	}
	return rule
}

func removeOutboundByTag(outbounds []any, tag string) []any {
	out := make([]any, 0, len(outbounds))
	for _, o := range outbounds {
		if m, ok := o.(map[string]any); ok {
			if t, _ := m["tag"].(string); t == tag {
				continue
			}
		}
		out = append(out, o)
	}
	return out
}

func removeRulesByOutbound(rules []any, tag string) []any {
	out := make([]any, 0, len(rules))
	for _, r := range rules {
		if m, ok := r.(map[string]any); ok {
			if t, _ := m["outboundTag"].(string); t == tag {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

func toAnyStrings(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func cleanRelayList(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		for _, part := range strings.Split(s, ",") {
			v := strings.TrimSpace(part)
			if v == "" || seen[v] {
				continue
			}
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func testRelay(cfg RelayConfig) (string, error) {
	addr := net.JoinHostPort(strings.TrimSpace(cfg.Host), strconv.Itoa(cfg.Port))
	transport := &http.Transport{}
	if cfg.Type == "http" {
		u := &url.URL{Scheme: "http", Host: addr}
		if cfg.User != "" {
			u.User = url.UserPassword(cfg.User, cfg.Pass)
		}
		transport.Proxy = http.ProxyURL(u)
	} else {
		var auth *proxy.Auth
		if cfg.User != "" {
			auth = &proxy.Auth{User: cfg.User, Password: cfg.Pass}
		}
		dialer, err := proxy.SOCKS5("tcp", addr, auth, proxy.Direct)
		if err != nil {
			return "", err
		}
		transport.DialContext = func(_ context.Context, network, address string) (net.Conn, error) {
			return dialer.Dial(network, address)
		}
	}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	resp, err := client.Get(relayTestURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	return strings.TrimSpace(string(body)), nil
}
