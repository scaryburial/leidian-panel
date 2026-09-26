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
		Enabled:      st.Enabled,
		FQDN:         st.FQDN,
		RootDomain:   m.rootDomain(),
		ServerIP:     st.IP,
		CertMode:     st.CertMode,
		CertPath:     st.CertPath,
		Proxied:      st.Proxied,
		CFConfigured: strings.TrimSpace(m.settingService.GetCFApiToken()) != "",
	}
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
		"7) 订阅域名设为该子域名；成功后记录状态",
		"失败任一步：回滚本次已做的改动，保持原状",
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
	if err := m.applyInbounds(fqdn, certDir); err != nil {
		_ = cf.deleteRecord(ctx, zoneID, recordID)
		_ = m.restoreInbounds()
		return m.snapshot(prevState), fmt.Errorf("切换入站失败：%w", err)
	}
	_ = m.settingService.SetDomainFqdn(fqdn)
	st := storedState{Enabled: true, FQDN: fqdn, IP: ip, RecordID: recordID, CertMode: certMode, CertPath: certDir, Proxied: true}
	if err := m.saveState(st); err != nil {
		return m.snapshot(prevState), err
	}
	_ = m.settingService.SetDomainEnabled(true)
	return m.snapshot(st), nil
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
	_ = m.settingService.SetDomainFqdn("")
	_ = m.settingService.SetDomainEnabled(false)
	clear := storedState{Enabled: false}
	_ = m.saveState(clear)
	return m.Status(), nil
}

//
// ------------------------- inbound rewrite -------------------------
//

func (m *Manager) applyInbounds(fqdn, certDir string) error {
	for _, remark := range []string{wsVLESSRemark, wsVMessRemark} {
		if err := rewriteWSInbound(remark, fqdn, certDir); err != nil {
			return err
		}
	}
	m.restartXray()
	return nil
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
			"alpn": []any{"h2", "http/1.1"},
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

func (c *cfClient) verify(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodGet, "/user/tokens/verify", nil)
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
		"https://ifconfig.me/ip",
		"https://1.1.1.1/cdn-cgi/trace",
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
		if net.ParseIP(text) != nil {
			return text, nil
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
