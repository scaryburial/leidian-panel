package domain

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// 面板默认端口是 33441，而它**不在 Cloudflare 代理支持的端口列表里**。
// 所以「启用域名」必须把面板搬到 8443，否则开了域名也只能用 IP:33441 登录。
func TestPanelLoginURL(t *testing.T) {
	cases := []struct {
		fqdn, base, want string
	}{
		{"3344.example.com", "/ui3344/", "https://3344.example.com:8443/ui3344/"},
		{"a.example.com", "/ui3344", "https://a.example.com:8443/ui3344/"},
		{"a.example.com", "ui3344", "https://a.example.com:8443/ui3344/"},
		{"a.example.com", "", "https://a.example.com:8443/"},
		{"", "/ui3344/", ""},
	}
	for _, c := range cases {
		if got := panelLoginURL(c.fqdn, c.base); got != c.want {
			t.Errorf("panelLoginURL(%q, %q) = %q, want %q", c.fqdn, c.base, got, c.want)
		}
	}
}

func enabledInboundOnPort(t *testing.T, port int, remark string) *model.Inbound {
	t.Helper()
	ib := &model.Inbound{
		Tag: "in-" + remark, Enable: true, Port: port,
		Protocol: model.VLESS, Remark: remark, Settings: `{"clients":[]}`,
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("create inbound on %d: %v", port, err)
	}
	return ib
}

// webPortOf 走产品自己的 GetPort()，而不是直读 settings 表：
// webPort 在全新库里**没有行**，GetPort() 从 defaultValueMap 回退到 33441，
// 直读 SQL 只会拿到空串——用它做断言会把「功能正确」误判成失败。
func webPortOf(t *testing.T, m *Manager) int {
	t.Helper()
	p, err := m.settingService.GetPort()
	if err != nil {
		t.Fatalf("GetPort: %v", err)
	}
	return p
}

func inboundEnabled(t *testing.T, id int) bool {
	t.Helper()
	var ib model.Inbound
	if err := database.GetDB().Where("id = ?", id).First(&ib).Error; err != nil {
		t.Fatalf("load inbound %d: %v", id, err)
	}
	return ib.Enable
}

// 搬迁 + 还原的完整往返。特别覆盖预设 VLESS-速度-8443 占着 8443 的情况：
// 面板要搬过去就必须先把它让出来，而且停用域名功能时必须恢复。
func TestMovePanelToCFPort_AndRestore(t *testing.T) {
	setupDomainDB(t)
	m := &Manager{}

	// 预设里真实存在的那个占位入站
	parked := enabledInboundOnPort(t, cfPanelPort, "VLESS-速度-8443")

	if got := webPortOf(t, m); got == cfPanelPort {
		t.Fatalf("前置条件不成立：初始 webPort 已经是 %d", cfPanelPort)
	}
	before := webPortOf(t, m)

	st, warnings, err := m.movePanelToCFPort("/root/cert/3344.example.com")
	if err != nil {
		t.Fatalf("movePanelToCFPort: %v", err)
	}
	if !st.Moved {
		t.Fatalf("Moved 应为 true")
	}
	if st.PrevPort != before {
		t.Errorf("PrevPort = %d, want %d（原端口必须被记下来才能还原）", st.PrevPort, before)
	}
	if got := webPortOf(t, m); got != cfPanelPort {
		t.Errorf("webPort = %d, want %d", got, cfPanelPort)
	}
	if inboundEnabled(t, parked.Id) {
		t.Errorf("占用 %d 的入站应被临时停用，否则与面板抢端口", cfPanelPort)
	}
	if st.ParkedInbound != parked.Id {
		t.Errorf("ParkedInbound = %d, want %d", st.ParkedInbound, parked.Id)
	}
	if len(warnings) == 0 {
		t.Errorf("应提示操作者端口变更与页面会断开")
	}
	// 证书必须同时被指到域名证书，否则面板不会启用 HTTPS（isDirectHTTPSConfigured
	// 要求 certFile 与 keyFile 同时非空且可加载）
	cert, _ := m.settingService.GetCertFile()
	key, _ := m.settingService.GetKeyFile()
	if cert == "" || key == "" {
		t.Errorf("证书未被设置：cert=%q key=%q", cert, key)
	}

	// 还原
	m.restorePanelPort(st)
	if got := webPortOf(t, m); got != before {
		t.Errorf("还原后 webPort = %d, want %d", got, before)
	}
	if !inboundEnabled(t, parked.Id) {
		t.Errorf("还原后入站 %d 应重新启用", parked.Id)
	}
}

// 已经在目标端口上时（用户手动改过），不应把当前端口当成「原值」记下来，
// 否则停用域名会把面板还原到一个从没打算使用的端口。
func TestMovePanelToCFPort_AlreadyOnTargetPort(t *testing.T) {
	setupDomainDB(t)
	m := &Manager{}
	if err := m.settingService.SetPort(cfPanelPort); err != nil {
		t.Fatalf("SetPort: %v", err)
	}

	st, _, err := m.movePanelToCFPort("/root/cert/x")
	if err != nil {
		t.Fatalf("movePanelToCFPort: %v", err)
	}
	if st.PrevPort != 0 {
		t.Errorf("PrevPort = %d, want 0（本来就在该端口上，不该记原值）", st.PrevPort)
	}
	if !st.Moved {
		t.Errorf("仍然要记 Moved=true，因为证书被改成了域名证书，停用时需要还原")
	}
}

// restorePanelPort 必须幂等：PrevPort 为 0 时不动端口。
func TestRestorePanelPort_NoopWhenPrevPortZero(t *testing.T) {
	setupDomainDB(t)
	m := &Manager{}
	if err := m.settingService.SetPort(33441); err != nil {
		t.Fatalf("SetPort: %v", err)
	}
	m.restorePanelPort(panelPortState{})
	if got := webPortOf(t, m); got != 33441 {
		t.Errorf("空状态不应改端口, got %d", got)
	}
}
