package controller

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// ReverseConfig drives the 反向代理 (Xray VLESS simple reverse) feature: a
// bridge machine behind NAT dials the panel and its network becomes an egress
// (or a reachable target) for the clients the operator picks.
type ReverseConfig struct {
	Enable      bool     `json:"enable"`
	InboundId   int      `json:"inboundId"`   // VLESS inbound the bridge connects to
	ClientEmail string   `json:"clientEmail"` // client on that inbound carrying the reverse tag
	Tag         string   `json:"tag"`         // reverse/portal tag (both sides must match)
	ServerAddr  string   `json:"serverAddr"`  // host the bridge dials
	Scope       string   `json:"scope"`       // "all" | "emails"
	Emails      []string `json:"emails"`      // clients that egress via the bridge
}

type ReverseController struct {
	settingService     service.SettingService
	xraySettingService service.XraySettingService
	xrayService        service.XrayService
}

func NewReverseController(g *gin.RouterGroup) *ReverseController {
	a := &ReverseController{}
	g.GET("/config", a.getConfig)
	g.POST("/config", a.saveConfig)
	g.GET("/bridge", a.getBridge)
	return a
}

func (a *ReverseController) getConfig(c *gin.Context) {
	cfg := ReverseConfig{Tag: "ui3344rev", Scope: "all"}
	if raw, err := a.settingService.GetReverseConfig(); err == nil && strings.TrimSpace(raw) != "" {
		if uerr := json.Unmarshal([]byte(raw), &cfg); uerr != nil {
			jsonMsg(c, "加载反向代理配置失败", uerr)
			return
		}
	}
	jsonObj(c, cfg, nil)
}

func (a *ReverseController) saveConfig(c *gin.Context) {
	var cfg ReverseConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		jsonMsg(c, "参数错误", err)
		return
	}
	cfg.ClientEmail = strings.TrimSpace(cfg.ClientEmail)
	cfg.Tag = sanitizeRelayID(cfg.Tag)
	if cfg.Tag == "" {
		cfg.Tag = "ui3344rev"
	}
	cfg.ServerAddr = strings.TrimSpace(cfg.ServerAddr)
	cfg.Emails = cleanRelayList(cfg.Emails)
	if cfg.Scope != "emails" {
		cfg.Scope = "all"
	}
	if cfg.Enable {
		if cfg.InboundId <= 0 || cfg.ClientEmail == "" {
			jsonMsg(c, "请选择隧道入站与客户端", nil)
			return
		}
		if cfg.Scope == "emails" && len(cfg.Emails) == 0 {
			jsonMsg(c, "请至少选择一个走家里的客户端", nil)
			return
		}
	}
	if err := setClientReverseTag(cfg.InboundId, cfg.ClientEmail, cfg.Tag, cfg.Enable); err != nil {
		jsonMsg(c, "设置反向标签失败", err)
		return
	}
	template, err := a.settingService.GetXrayConfigTemplate()
	if err != nil {
		jsonMsg(c, "读取 Xray 配置失败", err)
		return
	}
	updated, err := applyReverseToTemplate(template, cfg)
	if err != nil {
		jsonMsg(c, "生成 Xray 配置失败", err)
		return
	}
	if err := a.xraySettingService.SaveXraySetting(updated); err != nil {
		jsonMsg(c, "保存 Xray 配置失败", err)
		return
	}
	b, _ := json.Marshal(cfg)
	if err := a.settingService.SetReverseConfig(string(b)); err != nil {
		jsonMsg(c, "保存反向代理设置失败", err)
		return
	}
	a.xrayService.SetToNeedRestart()
	if a.xrayService.IsXrayRunning() {
		_ = a.xrayService.RestartXray(false)
	}
	jsonMsg(c, "已保存", nil)
}

// setClientReverseTag writes the reverse tag onto the client row and mirrors it
// into the inbound's settings JSON (where the vless account is built from).
func setClientReverseTag(inboundId int, email, tag string, enable bool) error {
	db := database.GetDB()
	reverseStr := ""
	if enable {
		b, _ := json.Marshal(map[string]string{"tag": tag})
		reverseStr = string(b)
	}
	if err := db.Exec("update clients set reverse = ? where email = ?", reverseStr, email).Error; err != nil {
		return err
	}
	// mirror into the inbound settings so the running config picks it up
	var ib model.Inbound
	if err := db.Where("id = ?", inboundId).First(&ib).Error; err != nil {
		return err
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(ib.Settings), &settings); err != nil {
		return err
	}
	clients, _ := settings["clients"].([]any)
	for _, raw := range clients {
		obj, ok := raw.(map[string]any)
		if !ok || obj["email"] != email {
			continue
		}
		if enable {
			obj["reverse"] = map[string]any{"tag": tag}
		} else {
			delete(obj, "reverse")
		}
	}
	settings["clients"] = clients
	ns, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	return db.Model(&model.Inbound{}).Where("id = ?", inboundId).Update("settings", string(ns)).Error
}

// applyReverseToTemplate re-adds the reverse egress routing rule for this tag.
func applyReverseToTemplate(template string, cfg ReverseConfig) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(template), &doc); err != nil {
		return "", err
	}
	routing, _ := doc["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{"domainStrategy": "AsIs"}
	}
	rules, _ := routing["rules"].([]any)
	rules = removeRulesByOutboundPrefix(rules, cfg.Tag)
	if cfg.Enable {
		rule := map[string]any{"type": "field", "outboundTag": cfg.Tag}
		if cfg.Scope == "emails" {
			rule["user"] = toAnyStrings(cfg.Emails)
		} else {
			// A rule needs at least one matcher; network is the catch-all the core accepts.
			rule["network"] = "tcp,udp"
		}
		rules = append(rules, rule)
	}
	routing["rules"] = rules
	doc["routing"] = routing
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// getBridge returns a ready-to-run Xray config for the home/bridge machine.
func (a *ReverseController) getBridge(c *gin.Context) {
	raw, _ := a.settingService.GetReverseConfig()
	var cfg ReverseConfig
	_ = json.Unmarshal([]byte(raw), &cfg)
	if cfg.InboundId <= 0 || cfg.ClientEmail == "" {
		jsonMsg(c, "请先保存反向代理配置", nil)
		return
	}
	bridge, err := buildBridgeConfig(cfg)
	if err != nil {
		jsonMsg(c, "生成家里端配置失败", err)
		return
	}
	jsonObj(c, map[string]any{"config": bridge}, nil)
}

func buildBridgeConfig(cfg ReverseConfig) (string, error) {
	db := database.GetDB()
	var ib model.Inbound
	if err := db.Where("id = ?", cfg.InboundId).First(&ib).Error; err != nil {
		return "", err
	}
	var row struct {
		UUID string
		Flow string
	}
	if err := db.Raw("select uuid, flow from clients where email = ?", cfg.ClientEmail).Scan(&row).Error; err != nil {
		return "", err
	}
	if row.UUID == "" {
		return "", errors.New("client not found")
	}
	stream := map[string]any{}
	if strings.TrimSpace(ib.StreamSettings) != "" {
		_ = json.Unmarshal([]byte(ib.StreamSettings), &stream)
	}
	clientStream := bridgeStream(stream, cfg.ServerAddr)
	user := map[string]any{
		"address":    cfg.ServerAddr,
		"port":       ib.Port,
		"id":         row.UUID,
		"encryption": "none",
		"reverse":    map[string]any{"tag": cfg.Tag},
	}
	// xtls-rprx-vision only makes sense on TCP with TLS/Reality.
	if net, _ := stream["network"].(string); net == "tcp" {
		if sec, _ := stream["security"].(string); sec == "tls" || sec == "reality" {
			if row.Flow != "" {
				user["flow"] = row.Flow
			}
		}
	}
	bridge := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{
			map[string]any{"tag": "socks-in", "listen": "127.0.0.1", "port": 2080, "protocol": "socks", "settings": map[string]any{"auth": "noauth", "udp": true}},
		},
		"outbounds": []any{
			map[string]any{"tag": "interconn", "protocol": "vless", "settings": user, "streamSettings": clientStream},
			map[string]any{"tag": "direct", "protocol": "freedom", "settings": map[string]any{}},
		},
		"routing": map[string]any{"rules": []any{
			map[string]any{"type": "field", "inboundTag": []any{cfg.Tag}, "outboundTag": "direct"},
		}},
	}
	out, err := json.MarshalIndent(bridge, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// bridgeStream converts a server inbound stream into the matching client-side
// streamSettings (only the parts a client needs).
func bridgeStream(stream map[string]any, addr string) map[string]any {
	out := map[string]any{}
	if v, ok := stream["network"].(string); ok && v != "" {
		out["network"] = v
	}
	sec, _ := stream["security"].(string)
	if sec == "" {
		sec = "none"
	}
	out["security"] = sec
	switch sec {
	case "tls":
		ts, _ := stream["tlsSettings"].(map[string]any)
		cs := map[string]any{}
		if ts != nil {
			if v, ok := ts["serverName"]; ok {
				cs["serverName"] = v
			}
		}
		if _, has := cs["serverName"]; !has && addr != "" {
			// Behind Cloudflare / a domain, the SNI must be the public domain.
			cs["serverName"] = addr
		}
		if ts != nil {
			if v, ok := ts["alpn"]; ok {
				cs["alpn"] = v
			}
			if v, ok := ts["allowInsecure"]; ok {
				cs["allowInsecure"] = v
			}
		}
		out["tlsSettings"] = cs
	case "reality":
		rs, _ := stream["realitySettings"].(map[string]any)
		cs := map[string]any{}
		if rs != nil {
			if names, ok := rs["serverNames"].([]any); ok && len(names) > 0 {
				cs["serverName"] = names[0]
			}
			if settings, ok := rs["settings"].(map[string]any); ok {
				if v, ok := settings["publicKey"]; ok {
					cs["publicKey"] = v
				}
				if v, ok := settings["fingerprint"]; ok {
					cs["fingerprint"] = v
				}
				if v, ok := settings["spiderX"]; ok {
					cs["spiderX"] = v
				}
			}
			if sids, ok := rs["shortIds"].([]any); ok && len(sids) > 0 {
				cs["shortId"] = sids[0]
			}
		}
		out["realitySettings"] = cs
	}
	if net, _ := stream["network"].(string); net == "ws" {
		ws, _ := stream["wsSettings"].(map[string]any)
		cs := map[string]any{}
		if ws != nil {
			if v, ok := ws["path"]; ok {
				cs["path"] = v
			}
			if v, ok := ws["host"]; ok {
				cs["host"] = v
			}
		}
		out["wsSettings"] = cs
	}
	return out
}
