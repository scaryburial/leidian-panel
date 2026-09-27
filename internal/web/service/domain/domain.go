// Package domain implements the panel's 域名功能: after the operator authorises
// with a TOTP code, it points a subdomain of the configured Cloudflare zone at
// this server (proxied / 橙云), issues a certificate for it, and switches the
// WebSocket presets over to the domain. Every external call is made *before*
// anything local is changed, and any failure rolls the partial work back, so a
// mid-way failure never leaves the panel or its inbounds broken.
package domain

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/totp"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	cfAPIBase = "https://api.cloudflare.com/client/v4"
	certRoot  = "/root/cert"

	// ws inbounds the domain feature rewrites to WS+TLS on the subdomain.
	wsVLESSRemark = "VLESS-综合-WS-2087"
	wsVMessRemark = "VMess-综合-WS-2052"
)

// Result is the panel-facing snapshot of the domain feature.
type Result struct {
	Enabled      bool     `json:"enabled"`
	FQDN         string   `json:"fqdn"`
	RootDomain   string   `json:"rootDomain"`
	ServerIP     string   `json:"serverIp"`
	CertMode     string   `json:"certMode"` // "" | "origin-ca" | "self-signed"
	CertPath     string   `json:"certPath"`
	Proxied      bool     `json:"proxied"`
	CFConfigured bool     `json:"cfConfigured"`
	Steps        []string `json:"steps,omitempty"`

	// 优选域名：已内置的优选域名数量与内容，以及本次操作的提示/警告。
	PreferredCount    int              `json:"preferredCount"`
	PreferredDomains  []string         `json:"preferredDomains,omitempty"`
	PreferredRejected []RejectedDomain `json:"preferredRejected,omitempty"`
	Warnings          []string         `json:"warnings,omitempty"`

	// 面板自身的登录入口。开启域名后面板会搬到 CF 支持的端口（8443），
	// 否则 `http://<IP>:33441/` 这条老路仍是唯一的登录方式——因为 33441
	// 不在 Cloudflare 代理的端口列表里，用域名根本登不进来。
	PanelPort int    `json:"panelPort"`
	PanelURL  string `json:"panelUrl,omitempty"`
}

// Manager drives the whole feature. It is stateless; everything it needs lives
// in panel settings and the database.
type Manager struct {
	settingService     service.SettingService
	xraySettingService service.XraySettingService
	xrayService        service.XrayService
}

// persisted state, stored as JSON under the panel's `domainConfig` setting.
type storedState struct {
	Enabled  bool   `json:"enabled"`
	FQDN     string `json:"fqdn"`
	IP       string `json:"ip"`
	RecordID string `json:"recordId"`
	CertMode string `json:"certMode"`
	CertPath string `json:"certPath"`
	Proxied  bool   `json:"proxied"`

	// 优选域名生成的产物（用于状态展示与「只挂加密端口」的可追溯性）。
	PreferredDomains       []string `json:"preferredDomains,omitempty"`
	PreferredRejectedCount int      `json:"preferredRejectedCount,omitempty"`

	// 面板端口搬迁的原值，停用时据此还原。
	Panel panelPortState `json:"panel,omitempty"`
}

func (m *Manager) rootDomain() string {
	root := m.settingService.GetCFRootDomain()
	if root == "" {
		root = "578272.xyz"
	}
	return root
}

func (m *Manager) state() storedState {
	var st storedState
	if raw, err := m.settingService.GetDomainConfig(); err == nil && strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	return st
}

func (m *Manager) saveState(st storedState) error {
	b, _ := json.Marshal(st)
	return m.settingService.SetDomainConfig(string(b))
}

func (m *Manager) snapshot(st storedState) Result {
	return Result{
		Enabled:          st.Enabled,
		FQDN:             st.FQDN,
		RootDomain:       m.rootDomain(),
		ServerIP:         st.IP,
		CertMode:         st.CertMode,
		CertPath:         st.CertPath,
		Proxied:          st.Proxied,
		CFConfigured:     strings.TrimSpace(m.settingService.GetCFApiToken()) != "",
		PreferredCount:   len(st.PreferredDomains),
		PreferredDomains: st.PreferredDomains,
		PanelPort:        m.panelPortForDisplay(st),
		PanelURL:         panelLoginURL(st.FQDN, m.currentBasePath()),
	}
}

// panelPortForDisplay 报告面板当前实际监听的端口（读取设置，读不到就退回常量）。
func (m *Manager) panelPortForDisplay(st storedState) int {
	if p, err := m.settingService.GetPort(); err == nil && p > 0 {
		return p
	}
	if st.Panel.Moved {
		return cfPanelPort
	}
	return 0
}

// Status reports the current state without touching the network.
func (m *Manager) Status() Result {
	return m.snapshot(m.state())
}

// Preview returns the flat list of steps Enable would run, so the UI and the CLI
// can tell the operator exactly what is about to happen before it does.
func (m *Manager) Preview(subdomain string) Result {
	res := m.snapshot(m.state())
	steps := []string{
		"1) 校验你的 TOTP 动态码",
		"2) 选择子域名：" + m.resolveSubdomain(subdomain),
		"3) 探测本机公网 IP（多源保底）",
		"4) 在 Cloudflare 为 578272.xyz 创建/更新 A 记录（橙云代理）",
		"5) 申请证书：优先 ACME/CF Origin，失败回退自签",
		"6) 把 " + wsVLESSRemark + " / " + wsVMessRemark + " 切到 WS+TLS 并使用该域名",
		"   · 其中位于 CF 纯 HTTP 端口（如 2052）的入站会跳过：该端口不支持 TLS，",
		"     强行切换会让节点在订阅里显示 tls 却连不上；跳过则它继续作为明文节点可用",
		"7) 自动生成优选域名组（备注 " + preferredGroupRemark + "）：",
		"   · 只挂「WS 传输 + 源站 TLS + CF HTTPS 端口(443/2053/2083/2087/2096/8443)」的入站",
		"   · 候选域名逐个做 DNS + Cloudflare 官方网段校验，失效或已不指向 CF 的会被剔除",
		"   · 组内 port/path/security 留默认值，表示逐行继承各自入站，因此客户端链接自动正确",
		"8) 把面板自身搬到端口 " + fmt.Sprint(cfPanelPort) + " 并使用该域名证书：",
		"   · 面板默认的 33441 不在 Cloudflare 代理的端口列表里，不搬的话开了域名也只能",
		"     用 http://<IP>:33441/ 登录，域名等于只服务了订阅",
		"   · 若 8443 已被入站占用（预设的 VLESS-速度-8443），会临时停用它，停用时自动恢复",
		"   · 面板会重启以在新端口生效，当前页面会断开",
		"9) 订阅域名设为该子域名；成功后记录状态",
		"失败任一步：回滚本次已做的改动，保持原状（优选生成失败除外，它只提示不回滚）",
	}
	res.Steps = steps
	return res
}

func (m *Manager) resolveSubdomain(sub string) string {
	sub = strings.ToLower(strings.TrimSpace(sub))
	root := m.rootDomain()
	if sub == "" {
		sub = randLabel(8)
	}
	// a user may type the full fqdn; keep only the left-most label
	sub = strings.TrimSuffix(sub, "."+root)
	sub = strings.Trim(sub, ".")
	if !validLabel(sub) {
		sub = randLabel(8)
	}
	return sub + "." + root
}

// Authorize reports whether the operator-supplied TOTP code is valid for the
// domain feature.
func (m *Manager) Authorize(code string) bool {
	return totp.Verify(m.settingService.GetDomainOtpSecret(), code)
}

// OtpConfigured reports whether a TOTP secret is already stored. It lets the
// secret be set once without a code (nothing to verify yet) while every later
// replacement must present the current code — otherwise a stolen session could
// install its own second factor and the TOTP gate would protect nothing.
func (m *Manager) OtpConfigured() bool {
	return strings.TrimSpace(m.settingService.GetDomainOtpSecret()) != ""
}

// Enable performs the full flow. totpCode gates the operation; subdomain is
// optional (empty → a random one).
func (m *Manager) Enable(ctx context.Context, subdomain, totpCode string) (Result, error) {
	if !m.Authorize(totpCode) {
		return m.snapshot(m.state()), errors.New("动态码校验失败（TOTP）")
	}
	token := strings.TrimSpace(m.settingService.GetCFApiToken())
	if token == "" {
		return m.snapshot(m.state()), errors.New("未配置 Cloudflare API Token")
	}
	cf := newCFClient(token)
	if err := cf.verify(ctx); err != nil {
		return m.snapshot(m.state()), fmt.Errorf("Cloudflare Token 校验失败：%w", err)
	}
	root := m.rootDomain()
	zoneID, err := cf.findZone(ctx, root)
	if err != nil {
		return m.snapshot(m.state()), fmt.Errorf("找不到域名 %s：%w", root, err)
	}
	fqdn := m.resolveSubdomain(subdomain)
	ip, err := detectPublicIP(ctx)
	if err != nil {
		return m.snapshot(m.state()), fmt.Errorf("探测公网 IP 失败：%w", err)
	}

	// --- external work first (nothing local touched yet) ---
	recordID, err := cf.upsertARecord(ctx, zoneID, fqdn, ip, true)
	if err != nil {
		return m.snapshot(m.state()), fmt.Errorf("写入 DNS 失败：%w", err)
	}
	certDir := filepath.Join(certRoot, fqdn)
	certMode := "self-signed"
	if err := cf.mintOriginCert(ctx, fqdn, certDir); err != nil {
		// fallback: self-signed (still valid behind CF proxy)
		if err2 := generateSelfSigned(fqdn, certDir); err2 != nil {
			// both failed → roll back the DNS record we just made
			_ = cf.deleteRecord(ctx, zoneID, recordID)
			return m.snapshot(m.state()), fmt.Errorf("证书申请失败（Origin:%v / 自签:%v）", err, err2)
		}
	} else {
		certMode = "origin-ca"
	}

	// --- local changes ---
	prevState := m.state()

	// 先把面板搬到 CF 支持的端口：33441 不在 Cloudflare 代理的端口列表里，
	// 不搬的话「开了域名」只惠及订阅，面板本身仍然只能用 IP:33441 登录。
	// 失败要连 DNS 记录一起回滚，不能留下半截状态。
	panelPrev, panelWarn, err := m.movePanelToCFPort(certDir)
	if err != nil {
		_ = cf.deleteRecord(ctx, zoneID, recordID)
		return m.snapshot(prevState), fmt.Errorf("迁移面板端口失败：%w", err)
	}

	skipped, err := m.applyInbounds(fqdn, certDir)
	if err != nil {
		_ = cf.deleteRecord(ctx, zoneID, recordID)
		_ = m.restoreInbounds()
		m.restorePanelPort(panelPrev)
		return m.snapshot(prevState), fmt.Errorf("切换入站失败：%w", err)
	}
	_ = m.settingService.SetDomainFqdn(fqdn)

	// 自动生成优选域名组：只挂「WS + TLS + CF HTTPS 端口」的加密入站。
	//
	// 这一步是**尽力而为**的：优选域名依赖外部 DNS，任何一个环节失败都不应该
	// 让已经成功的域名功能回滚。失败只作为警告返回给操作者。
	warnings := append([]string{}, panelWarn...)
	warnings = append(warnings, skipped...)
	prefDomains, prefRejected, prefWarn, prefErr := SyncPreferredHosts(ctx, fqdn, PreferredDomainCandidates)
	warnings = append(warnings, prefWarn...)
	if prefErr != nil {
		warnings = append(warnings, "优选域名生成失败："+prefErr.Error())
	}
	if n := len(prefRejected); n > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"已剔除 %d 个未通过 Cloudflare 网段校验的候选域名（失效或已不指向 CF）", n))
	}

	st := storedState{
		Enabled: true, FQDN: fqdn, IP: ip, RecordID: recordID,
		CertMode: certMode, CertPath: certDir, Proxied: true,
		PreferredDomains:       prefDomains,
		PreferredRejectedCount: len(prefRejected),
		Panel:                  panelPrev,
	}
	if err := m.saveState(st); err != nil {
		m.restorePanelPort(panelPrev)
		return m.snapshot(prevState), err
	}
	_ = m.settingService.SetDomainEnabled(true)

	// 面板端口的改动要重启进程才生效，放在最后：先让本次 HTTP 响应写出去，
	// 否则用户在切换瞬间拿到的是断掉的响应。延迟 3 秒。
	if panelPrev.Moved {
		restartPanelSoon()
	}

	res := m.snapshot(st)
	res.Warnings = warnings
	res.PreferredRejected = prefRejected
	if u := panelLoginURL(fqdn, m.currentBasePath()); u != "" {
		res.PanelURL = u
		res.Warnings = append(res.Warnings, "面板新登录地址："+u)
	}
	return res, nil
}

// Disable removes the DNS record, restores the WS inbounds to plain WebSocket
// and clears all state. The certificate files are left on disk. It is gated by
// the same TOTP code as Enable.
func (m *Manager) Disable(ctx context.Context, totpCode string) (Result, error) {
	if !m.Authorize(totpCode) {
		return m.snapshot(m.state()), errors.New("动态码校验失败（TOTP）")
	}
	st := m.state()
	if token := strings.TrimSpace(m.settingService.GetCFApiToken()); token != "" && st.RecordID != "" {
		cf := newCFClient(token)
		if zoneID, err := cf.findZone(ctx, m.rootDomain()); err == nil {
			_ = cf.deleteRecord(ctx, zoneID, st.RecordID)
		}
	}
	_ = m.restoreInbounds()
	// 优选组是本功能生成的，停用时一并清掉，避免订阅里留下指向失效域名的节点。
	_ = RemovePreferredHosts()
	// 面板端口/证书还原回启用前的值，并为「腾端口」而临时停用的入站恢复启用。
	// 顺序放在清状态之前——restorePanelPort 需要读 st.Panel。
	m.restorePanelPort(st.Panel)
	if st.Panel.Moved {
		restartPanelSoon()
	}
	_ = m.settingService.SetDomainFqdn("")
	_ = m.settingService.SetDomainEnabled(false)
	clear := storedState{Enabled: false}
	_ = m.saveState(clear)
	res := m.Status()
	if st.Panel.Moved && st.Panel.PrevPort > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"面板端口已还原为 %d；面板将重启，请改用原地址登录", st.Panel.PrevPort))
	}
	return res, nil
}

//
// ------------------------- inbound rewrite -------------------------
//

// applyInbounds 把域名功能管理的 WS 入站切到 WS+TLS。
//
// 这里必须按 Cloudflare 的端口语义分情况处理（实测结论见 preferred.go 顶部说明）：
//
//   - 入站位于 CF 的 **HTTPS 端口**（443/2053/2083/2087/2096/8443）：
//     客户端到 CF 是 TLS，CF 回源也是 TLS —— 可以且应当切成 TLS，切换后全程加密。
//
//   - 入站位于 CF 的 **纯 HTTP 端口**（2052 等）：CF 在这类端口上**不接受客户端的
//     TLS**，回源也是明文。若强行切成 TLS，订阅里会显示 tls 但客户端根本连不上
//     （边缘拒绝 TLS 握手），节点直接废掉。所以保持它原本的明文 WS 配置不动，
//     让它继续作为明文节点可用。
//
// 返回被跳过的入站说明，供上层提示操作者。
func (m *Manager) applyInbounds(fqdn, certDir string) (skipped []string, err error) {
	for _, remark := range []string{wsVLESSRemark, wsVMessRemark} {
		port, err := inboundPortByRemark(remark)
		if err != nil {
			return skipped, err
		}
		if IsCFHTTPOnlyPort(port) {
			skipped = append(skipped, fmt.Sprintf(
				"%s 位于 Cloudflare 的纯 HTTP 端口 %d：该端口不支持 TLS，已保持明文以免节点失效",
				remark, port))
			continue
		}
		if err := rewriteWSInbound(remark, fqdn, certDir); err != nil {
			return skipped, err
		}
	}
	m.restartXray()
	return skipped, nil
}

// inboundPortByRemark 查出入站的监听端口。
func inboundPortByRemark(remark string) (int, error) {
	var ib model.Inbound
	if err := database.GetDB().Where("remark = ?", remark).First(&ib).Error; err != nil {
		return 0, fmt.Errorf("找不到入站 %s", remark)
	}
	return ib.Port, nil
}

func (m *Manager) restoreInbounds() error {
	var errs []string
	for _, remark := range []string{wsVLESSRemark, wsVMessRemark} {
		if err := rewriteWSInbound(remark, "", ""); err != nil {
			errs = append(errs, err.Error())
		}
	}
	m.restartXray()
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (m *Manager) restartXray() {
	m.xrayService.SetToNeedRestart()
	if m.xrayService.IsXrayRunning() {
		_ = m.xrayService.RestartXray(false)
	}
}

// rewriteWSInbound turns a WebSocket inbound into WS+TLS bound to fqdn using
// certDir, or back to plain WebSocket when fqdn is empty.
func rewriteWSInbound(remark, fqdn, certDir string) error {
	db := database.GetDB()
	var ib model.Inbound
	if err := db.Where("remark = ?", remark).First(&ib).Error; err != nil {
		return fmt.Errorf("找不到入站 %s", remark)
	}
	var stream map[string]any
	if err := json.Unmarshal([]byte(ib.StreamSettings), &stream); err != nil {
		return err
	}
	if fqdn == "" {
		stream["security"] = "none"
		delete(stream, "tlsSettings")
		if ws, ok := stream["wsSettings"].(map[string]any); ok {
			ws["host"] = ""
		}
	} else {
		stream["security"] = "tls"
		stream["tlsSettings"] = map[string]any{
			"serverName": fqdn, "minVersion": "1.2", "maxVersion": "1.3",
			"rejectUnknownSni": false, "allowInsecure": false,
			"certificates": []any{map[string]any{
				"certificateFile": filepath.Join(certDir, "fullchain.pem"),
				"keyFile":         filepath.Join(certDir, "privkey.pem"),
				"ocspStapling":    0, "oneTimeLoading": false, "usage": "encipherment", "buildChain": false,
			}},
			// 只声明 http/1.1 —— 这里**不能**带上 h2。
			//
			// WebSocket 无法在 HTTP/2 上运行。若声明 ["h2","http/1.1"]，
			// 客户端与 Cloudflare 握手时 CF 会协商出 h2，客户端随即失败：
			//
			//   websocket: protocol "h2" was given but is not supported
			//   malformed HTTP response "\x00\x00\x12\x04..."  (HTTP/2 SETTINGS 帧)
			//
			// 实测：同一节点 alpn=http/1.1 时 generate_204 返回 204；
			// alpn=h2,http/1.1 时直接 000 连不上。不要改回两个都写。
			"alpn": []any{"http/1.1"},
		}
		if ws, ok := stream["wsSettings"].(map[string]any); ok {
			ws["host"] = fqdn
		}
	}
	out, err := json.Marshal(stream)
	if err != nil {
		return err
	}
	return db.Model(&model.Inbound{}).Where("id = ?", ib.Id).Update("stream_settings", string(out)).Error
}

//
// ------------------------- Cloudflare -------------------------
//

type cfClient struct {
	token string
	http  *http.Client
}

type cfEnvelope struct {
	Success bool            `json:"success"`
	Errors  []cfMessage     `json:"errors"`
	Result  json.RawMessage `json:"result"`
}

type cfMessage struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newCFClient(token string) *cfClient {
	return &cfClient{token: token, http: &http.Client{Timeout: 20 * time.Second}}
}

func (c *cfClient) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfAPIBase+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env cfEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if !env.Success {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if len(env.Errors) > 0 && env.Errors[0].Message != "" {
			msg = env.Errors[0].Message
		}
		return nil, errors.New(msg)
	}
	return env.Result, nil
}

// verify 仅用于确认令牌可用。注意：域名级(zone-scoped)令牌会被
// /user/tokens/verify 判为 Invalid API Token，因此改用令牌真正需要的
// zones 列表接口来校验。
func (c *cfClient) verify(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/zones?per_page=1", nil)
	return err
}

func (c *cfClient) findZone(ctx context.Context, name string) (string, error) {
	res, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(name), nil)
	if err != nil {
		return "", err
	}
	var zones []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(res, &zones); err != nil {
		return "", err
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("zone %s not found", name)
	}
	return zones[0].ID, nil
}

func (c *cfClient) upsertARecord(ctx context.Context, zoneID, fqdn, ip string, proxied bool) (string, error) {
	res, err := c.do(ctx, http.MethodGet, "/zones/"+zoneID+"/dns_records?type=A&name="+url.QueryEscape(fqdn), nil)
	if err != nil {
		return "", err
	}
	var existing []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(res, &existing)
	payload := map[string]any{"type": "A", "name": fqdn, "content": ip, "proxied": proxied, "ttl": 1}
	if len(existing) > 0 {
		out, err := c.do(ctx, http.MethodPatch, "/zones/"+zoneID+"/dns_records/"+existing[0].ID, payload)
		if err != nil {
			return "", err
		}
		return recordID(out, existing[0].ID), nil
	}
	out, err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", payload)
	if err != nil {
		return "", err
	}
	return recordID(out, ""), nil
}

func recordID(raw json.RawMessage, fallback string) string {
	var r struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &r); err == nil && r.ID != "" {
		return r.ID
	}
	return fallback
}

func (c *cfClient) deleteRecord(ctx context.Context, zoneID, recordID string) error {
	if recordID == "" {
		return nil
	}
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+recordID, nil)
	return err
}

// mintOriginCert asks Cloudflare for a 15-year Origin CA certificate for fqdn
// and writes it to certDir. The private key never leaves the host: only a CSR
// is sent.
func (c *cfClient) mintOriginCert(ctx context.Context, fqdn, certDir string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: fqdn},
		DNSNames: []string{fqdn},
	}, key)
	if err != nil {
		return err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	res, err := c.do(ctx, http.MethodPost, "/certificates", map[string]any{
		"hostnames":          []string{fqdn},
		"requested_validity": 5475,
		"request_type":       "origin-ecc",
		"csr":                string(csrPEM),
	})
	if err != nil {
		return err
	}
	var out struct {
		Certificate string `json:"certificate"`
	}
	if err := json.Unmarshal(res, &out); err != nil || strings.TrimSpace(out.Certificate) == "" {
		return errors.New("origin cert response empty")
	}
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"), []byte(out.Certificate), 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(certDir, "privkey.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
}

//
// ------------------------- helpers -------------------------
//

// detectPublicIP tries several endpoints so a single blocked source can't stop
// the feature.
func detectPublicIP(ctx context.Context) (string, error) {
	endpoints := []string{
		"https://api.ipify.org",
		"https://ipv4.icanhazip.com",
		"https://ipinfo.io/ip",
		"https://1.1.1.1/cdn-cgi/trace",
		"https://ifconfig.me/ip",
	}
	client := &http.Client{Timeout: 8 * time.Second}
	var lastErr error
	for _, ep := range endpoints {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep, nil)
		if err != nil {
			lastErr = err
			continue
		}
		req.Header.Set("User-Agent", "curl/8")
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		text := strings.TrimSpace(string(body))
		if strings.Contains(ep, "trace") {
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "ip=") {
					text = strings.TrimPrefix(line, "ip=")
					break
				}
			}
		}
		text = strings.TrimSpace(text)
		// 只接受 IPv4：DNS 写的是 A 记录，若误取 IPv6 会导致创建失败。
		if ip := net.ParseIP(text); ip != nil && ip.To4() != nil {
			return ip.To4().String(), nil
		}
		lastErr = fmt.Errorf("%s: %q", ep, text)
	}
	if lastErr == nil {
		lastErr = errors.New("no endpoint")
	}
	return "", lastErr
}

func generateSelfSigned(fqdn, certDir string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: fqdn},
		DNSNames:              []string{fqdn},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(certDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(certDir, "fullchain.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(certDir, "privkey.pem"),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
}

func validLabel(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return !strings.HasPrefix(s, "-") && !strings.HasSuffix(s, "-")
}

func randLabel(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "ui3344"
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}
