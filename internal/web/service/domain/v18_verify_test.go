package domain

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// mkTLSInbound 造一个普通（非 WebSocket）TLS 入站，用于验证域名功能对
// VMess-安全-TLS 这类入站的证书替换与还原。
func mkTLSInbound(t *testing.T, remark string, port int, serverName, certDir string) *model.Inbound {
	t.Helper()
	stream := map[string]any{
		"network":  "tcp",
		"security": "tls",
		"tlsSettings": map[string]any{
			"serverName": serverName,
			"certificates": []any{map[string]any{
				"certificateFile": certDir + "/fullchain.pem",
				"keyFile":         certDir + "/privkey.pem",
				"usage":           "encipherment",
			}},
			"alpn": []any{"http/1.1"},
		},
	}
	b, err := json.Marshal(stream)
	if err != nil {
		t.Fatalf("marshal stream: %v", err)
	}
	ib := &model.Inbound{
		Tag:            "in-" + strconv.Itoa(port) + "-vmess",
		Enable:         true,
		Port:           port,
		Protocol:       model.Protocol("vmess"),
		Remark:         remark,
		Settings:       `{"clients":[{"id":"00000000-0000-0000-0000-000000000001","email":"t"}]}`,
		StreamSettings: string(b),
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("create inbound %d: %v", port, err)
	}
	return ib
}

func reloadStream(t *testing.T, id int) map[string]any {
	t.Helper()
	var ib model.Inbound
	if err := database.GetDB().First(&ib, id).Error; err != nil {
		t.Fatalf("reload inbound %d: %v", id, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(ib.StreamSettings), &m); err != nil {
		t.Fatalf("unmarshal stream: %v", err)
	}
	return m
}

// TestRewriteTLSCertificate_SwapAndRestore 锁定 v1.8 的行为：
// 预设入站 VMess-安全-TLS-2053 原本用 CN=ui3344、且**没有任何 SAN** 的自签证书，
// 客户端必然握手失败（分享链接里没有 allowInsecure/pin）。启用域名后必须换成
// 域名证书，停用后必须能原样还原回自签证书。
func TestRewriteTLSCertificate_SwapAndRestore(t *testing.T) {
	setupDomainDB(t)
	ib := mkTLSInbound(t, vmessTLSRemark, 2053, selfSignedSNI, selfSignedCertDir)

	const fqdn = "x.example.com"
	dir := filepath.Join("/root", "cert", "x.example.com")

	if err := rewriteTLSCertificate(vmessTLSRemark, fqdn, dir); err != nil {
		t.Fatalf("swap certificate: %v", err)
	}
	m := reloadStream(t, ib.Id)
	ts, ok := m["tlsSettings"].(map[string]any)
	if !ok {
		t.Fatalf("tlsSettings missing after swap: %v", m)
	}
	if got := ts["serverName"]; got != fqdn {
		t.Errorf("serverName = %v, want %v", got, fqdn)
	}
	certs, ok := ts["certificates"].([]any)
	if !ok || len(certs) == 0 {
		t.Fatalf("certificates missing after swap: %v", ts["certificates"])
	}
	first, _ := certs[0].(map[string]any)
	if got := first["certificateFile"]; got != filepath.Join(dir, "fullchain.pem") {
		t.Errorf("certificateFile = %v, want %v", got, filepath.Join(dir, "fullchain.pem"))
	}
	if got := first["keyFile"]; got != filepath.Join(dir, "privkey.pem") {
		t.Errorf("keyFile = %v, want %v", got, filepath.Join(dir, "privkey.pem"))
	}
	// 传输层与客户端列表不能被顺手改掉
	if m["network"] != "tcp" || m["security"] != "tls" {
		t.Errorf("transport changed: network=%v security=%v", m["network"], m["security"])
	}

	// 停用域名 → 还原
	if err := rewriteTLSCertificate(vmessTLSRemark, "", ""); err != nil {
		t.Fatalf("restore certificate: %v", err)
	}
	m = reloadStream(t, ib.Id)
	ts, _ = m["tlsSettings"].(map[string]any)
	if got := ts["serverName"]; got != selfSignedSNI {
		t.Errorf("restored serverName = %v, want %v", got, selfSignedSNI)
	}
	first, _ = ts["certificates"].([]any)[0].(map[string]any)
	if got := first["certificateFile"]; got != filepath.Join(selfSignedCertDir, "fullchain.pem") {
		t.Errorf("restored certificateFile = %v, want %v", got, filepath.Join(selfSignedCertDir, "fullchain.pem"))
	}
}

// TestRewriteTLSCertificate_MissingInbound 锁定「找不到入站」返回可识别的错误，
// 这样 applyInbounds/restoreInbounds 才能把它降级成「跳过」而不是让整个 Enable 失败。
func TestRewriteTLSCertificate_MissingInbound(t *testing.T) {
	setupDomainDB(t)
	err := rewriteTLSCertificate("这个 remark 不存在", "x.example.com", "/root/cert/x")
	if err == nil {
		t.Fatal("want an error for a missing inbound")
	}
	if !errors.Is(err, errInboundNotFound) {
		t.Errorf("want errInboundNotFound, got %v", err)
	}
}

// TestRewriteTLSCertificate_NonTLSUntouched 保证用户把该入站改成非 TLS 传输后，
// 我们不会凭空给它加一层 TLS。
func TestRewriteTLSCertificate_NonTLSUntouched(t *testing.T) {
	setupDomainDB(t)
	ib := mkWSInbound(t, 9999, model.Protocol("vmess"), "ws", "none")
	if err := rewriteTLSCertificate(ib.Remark, "x.example.com", "/root/cert/x"); err == nil {
		t.Error("want an error when the inbound is not TLS")
	}
	m := reloadStream(t, ib.Id)
	if _, hasTLS := m["tlsSettings"]; hasTLS {
		t.Error("tlsSettings must not be invented for a non-TLS inbound")
	}
}

// TestProbePreferredDomain_Live 是真的打公网的端到端探测，默认跳过。
//
// 它锁定的正是 v1.8 的核心修复依据：只按「A 记录落在 CF 网段」筛选是不够的，
// 大量第三方优选域名虽然解析到 Cloudflare，却不会把流量送回本机入站。
//
//	UI3344_LIVE_PROBE=1 UI3344_LIVE_FQDN=49pwzx78.578272.xyz \
//	UI3344_LIVE_PORT=2087 UI3344_LIVE_PATH=/ui3344ws \
//	go test ./internal/web/service/domain/ -run TestProbePreferredDomain_Live -v
func TestProbePreferredDomain_Live(t *testing.T) {
	if os.Getenv("UI3344_LIVE_PROBE") != "1" {
		t.Skip("set UI3344_LIVE_PROBE=1 to run the live end-to-end probe")
	}
	fqdn := os.Getenv("UI3344_LIVE_FQDN")
	if fqdn == "" {
		t.Skip("set UI3344_LIVE_FQDN to the panel's own domain")
	}
	port := 2087
	if v := os.Getenv("UI3344_LIVE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			port = p
		}
	}
	path := os.Getenv("UI3344_LIVE_PATH")
	if path == "" {
		path = "/"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if ok, reason := probePreferredDomain(ctx, fqdn, fqdn, port, path); !ok {
		t.Errorf("own domain %s must pass the probe, got: %s", fqdn, reason)
	} else {
		t.Logf("own domain %s -> 101 ok", fqdn)
	}

	for _, h := range []string{"time.is", "ip.sb", "japan.com", "icook.hk"} {
		if ok, reason := probePreferredDomain(ctx, h, fqdn, port, path); ok {
			t.Errorf("%s unexpectedly passed the probe (it must be rejected)", h)
		} else {
			t.Logf("%s -> rejected: %s", h, reason)
		}
	}

	if ok, reason := probePreferredDomain(ctx, "no-such-host.invalid", fqdn, port, path); ok {
		t.Errorf("a non-resolving host must not pass, got ok (%s)", reason)
	} else {
		t.Logf("no-such-host.invalid -> rejected: %s", reason)
	}
}
