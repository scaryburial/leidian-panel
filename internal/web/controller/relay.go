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
	// relayTagPrefix scopes the outbound tags and routing rules this feature
	// owns, so a save can clean up everything it previously injected.
	relayTagPrefix = "ui3344-relay"
	relayTestURL   = "https://api.ipify.org"
)

// RelayRule is one upstream (SOCKS5/HTTP) plus the clients it applies to.
type RelayRule struct {
	ID       string   `json:"id"`
	Enable   bool     `json:"enable"`
	Type     string   `json:"type"` // "socks" | "http"
	Host     string   `json:"host"`
	Port     int      `json:"port"`
	User     string   `json:"user"`
	Pass     string   `json:"pass"`
	Scope    string   `json:"scope"` // "all" | "emails" | "inbounds"
	Emails   []string `json:"emails"`
	Inbounds []string `json:"inbounds"` // inbound tags
	Remark   string   `json:"remark"`   // operator note, panel-only
}

// RelayConfig persists the ordered list of relay rules.
type RelayConfig struct {
	Rules []RelayRule `json:"rules"`
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
	cfg := RelayConfig{Rules: []RelayRule{}}
	if raw, err := a.settingService.GetRelayConfig(); err == nil && strings.TrimSpace(raw) != "" {
		if uerr := json.Unmarshal([]byte(raw), &cfg); uerr != nil {
			jsonMsg(c, "加载中转配置失败", uerr)
			return
		}
	}
	if cfg.Rules == nil {
		cfg.Rules = []RelayRule{}
	}
	jsonObj(c, cfg, nil)
}

func (a *RelayController) saveConfig(c *gin.Context) {
	var cfg RelayConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		jsonMsg(c, "参数错误", err)
		return
	}
	normalized, err := normalizeRelayRules(cfg.Rules)
	if err != nil {
		jsonMsg(c, err.Error(), nil)
		return
	}
	cfg.Rules = normalized

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
	var rule RelayRule
	if err := c.ShouldBindJSON(&rule); err != nil {
		jsonMsg(c, "参数错误", err)
		return
	}
	ip, err := testRelay(rule)
	if err != nil {
		jsonMsg(c, "测试失败", err)
		return
	}
	jsonObj(c, map[string]any{"egressIp": ip}, nil)
}

func normalizeRelayRules(rules []RelayRule) ([]RelayRule, error) {
	out := make([]RelayRule, 0, len(rules))
	for i := range rules {
		r := rules[i]
		r.ID = sanitizeRelayID(r.ID)
		if r.ID == "" {
			r.ID = "r" + strconv.FormatInt(time.Now().UnixNano(), 36) + strconv.Itoa(i)
		}
		r.Type = strings.ToLower(strings.TrimSpace(r.Type))
		if r.Type != "socks" && r.Type != "http" {
			r.Type = "socks"
		}
		switch r.Scope {
		case "emails", "inbounds":
		default:
			r.Scope = "all"
		}
		r.Host = strings.TrimSpace(r.Host)
		r.Remark = strings.TrimSpace(r.Remark)
		r.Emails = cleanRelayList(r.Emails)
		r.Inbounds = cleanRelayList(r.Inbounds)
		if r.Enable {
			if r.Host == "" || r.Port <= 0 || r.Port > 65535 {
				return nil, errRelay("请为每条启用中的上游填写正确的地址与端口")
			}
			if r.Scope == "emails" && len(r.Emails) == 0 {
				return nil, errRelay("有一条上游选择了「指定客户端」但没选客户端")
			}
			if r.Scope == "inbounds" && len(r.Inbounds) == 0 {
				return nil, errRelay("有一条上游选择了「指定入站」但没选入站")
			}
		}
		out = append(out, r)
	}
	return out, nil
}

type relayError string

func (e relayError) Error() string { return string(e) }

func errRelay(msg string) error { return relayError(msg) }

func sanitizeRelayID(id string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(id) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func relayTag(id string) string { return relayTagPrefix + "-" + id }

// applyRelayToTemplate drops every previously-injected relay outbound/rule and
// re-adds the enabled ones. Catch-all ("all") rules are appended last so they
// never shadow a more specific rule.
func applyRelayToTemplate(template string, cfg RelayConfig) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(template), &doc); err != nil {
		return "", err
	}
	outbounds, _ := doc["outbounds"].([]any)
	outbounds = removeOutboundsByTagPrefix(outbounds, relayTagPrefix)

	routing, _ := doc["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{"domainStrategy": "AsIs"}
	}
	rules, _ := routing["rules"].([]any)
	rules = removeRulesByOutboundPrefix(rules, relayTagPrefix)

	enabled := make([]RelayRule, 0, len(cfg.Rules))
	for _, r := range cfg.Rules {
		if r.Enable {
			enabled = append(enabled, r)
		}
	}
	// specific rules first, catch-all last
	for pass := 0; pass < 2; pass++ {
		wantAll := pass == 1
		for _, r := range enabled {
			if (r.Scope == "all") != wantAll {
				continue
			}
			tag := relayTag(r.ID)
			outbounds = append(outbounds, relayOutbound(tag, r))
			rules = append(rules, relayRule(tag, r))
		}
	}

	doc["outbounds"] = outbounds
	routing["rules"] = rules
	doc["routing"] = routing

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func relayOutbound(tag string, r RelayRule) map[string]any {
	server := map[string]any{"address": r.Host, "port": r.Port}
	if r.User != "" || r.Pass != "" {
		server["users"] = []any{map[string]any{"user": r.User, "pass": r.Pass}}
	}
	return map[string]any{
		"tag":      tag,
		"protocol": r.Type,
		"settings": map[string]any{"servers": []any{server}},
	}
}

func relayRule(tag string, r RelayRule) map[string]any {
	rule := map[string]any{"type": "field", "outboundTag": tag}
	switch r.Scope {
	case "emails":
		rule["user"] = toAnyStrings(r.Emails)
	case "inbounds":
		rule["inboundTag"] = toAnyStrings(r.Inbounds)
	}
	return rule
}

func removeOutboundsByTagPrefix(outbounds []any, prefix string) []any {
	out := make([]any, 0, len(outbounds))
	for _, o := range outbounds {
		if m, ok := o.(map[string]any); ok {
			if t, _ := m["tag"].(string); strings.HasPrefix(t, prefix) {
				continue
			}
		}
		out = append(out, o)
	}
	return out
}

func removeRulesByOutboundPrefix(rules []any, prefix string) []any {
	out := make([]any, 0, len(rules))
	for _, r := range rules {
		if m, ok := r.(map[string]any); ok {
			if t, _ := m["outboundTag"].(string); strings.HasPrefix(t, prefix) {
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

func testRelay(r RelayRule) (string, error) {
	addr := net.JoinHostPort(strings.TrimSpace(r.Host), strconv.Itoa(r.Port))
	transport := &http.Transport{}
	if r.Type == "http" {
		u := &url.URL{Scheme: "http", Host: addr}
		if r.User != "" {
			u.User = url.UserPassword(r.User, r.Pass)
		}
		transport.Proxy = http.ProxyURL(u)
	} else {
		var auth *proxy.Auth
		if r.User != "" {
			auth = &proxy.Auth{User: r.User, Password: r.Pass}
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
