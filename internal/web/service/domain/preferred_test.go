package domain

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func setupDomainDB(t *testing.T) {
	t.Helper()
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

// mkWSInbound 造一个入站，便于验证「哪些入站会被挂上优选域名」。
func mkWSInbound(t *testing.T, port int, proto model.Protocol, network, security string) *model.Inbound {
	t.Helper()
	stream := map[string]any{
		"network":    network,
		"security":   security,
		"wsSettings": map[string]any{"path": "/p" + strconv.Itoa(port), "host": "d.example.com"},
	}
	b, err := json.Marshal(stream)
	if err != nil {
		t.Fatalf("marshal stream: %v", err)
	}
	ib := &model.Inbound{
		Tag:            "in-" + strconv.Itoa(port) + "-" + security,
		Enable:         true,
		Port:           port,
		Protocol:       proto,
		Settings:       `{"clients":[]}`,
		StreamSettings: string(b),
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("create inbound %d: %v", port, err)
	}
	return ib
}

// stubLookup 替换 DNS 注入点，让测试不依赖网络。
func stubLookup(t *testing.T, table map[string][]string) {
	t.Helper()
	prev := lookupIPv4
	lookupIPv4 = func(_ context.Context, host string) ([]net.IP, error) {
		ips, ok := table[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		out := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			out = append(out, net.ParseIP(s))
		}
		return out, nil
	}
	t.Cleanup(func() { lookupIPv4 = prev })
}

// TestCFPortClassification 锁定 Cloudflare 的两类端口。
//
// 这个分类是整个「优选只挂加密端口」功能的地基：分错了就会把 TLS 入站挂到
// 不支持 TLS 的端口上，节点在订阅里显示 tls 却根本连不上。
func TestCFPortClassification(t *testing.T) {
	https := []int{443, 2053, 2083, 2087, 2096, 8443}
	httpOnly := []int{80, 8080, 8880, 2052, 2082, 2086, 2095}

	for _, p := range https {
		if !IsCFHTTPSPort(p) {
			t.Errorf("端口 %d 应是 CF HTTPS 端口", p)
		}
		if IsCFHTTPOnlyPort(p) {
			t.Errorf("端口 %d 不能同时是 HTTPS 与纯 HTTP 端口", p)
		}
	}
	for _, p := range httpOnly {
		if !IsCFHTTPOnlyPort(p) {
			t.Errorf("端口 %d 应是 CF 纯 HTTP 端口", p)
		}
		if IsCFHTTPSPort(p) {
			t.Errorf("端口 %d 不能同时是纯 HTTP 与 HTTPS 端口", p)
		}
	}
	// 集合必须与 CF 公布的一致：多一个会让不该挂的入站被挂上，
	// 少一个会让可用端口被白白排除。
	if len(cfHTTPSPorts) != len(https) {
		t.Errorf("HTTPS 端口数量 = %d, want %d", len(cfHTTPSPorts), len(https))
	}
	if len(cfHTTPOnlyPorts) != len(httpOnly) {
		t.Errorf("纯 HTTP 端口数量 = %d, want %d", len(cfHTTPOnlyPorts), len(httpOnly))
	}
	// 非 CF 端口两类都不属于
	for _, p := range []int{22, 8388, 33441, 2096 + 1} {
		if IsCFHTTPSPort(p) || IsCFHTTPOnlyPort(p) {
			t.Errorf("端口 %d 不是 CF 代理端口，不该被分类", p)
		}
	}
}

// TestIsCloudflareIPv4 覆盖实测遇到的真实地址，包含「名字像 CF 但不是 CF」的坑。
func TestIsCloudflareIPv4(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
		why  string
	}{
		{"104.16.0.1", true, "CF 官方段"},
		{"172.67.162.52", true, "3344.578272.xyz 实际解析结果"},
		{"104.21.90.228", true, "3344.578272.xyz 实际解析结果"},
		{"162.158.114.58", true, "抓包到的 CF 回源地址"},
		{"8.8.8.8", false, "Google DNS"},
		{"23.227.38.33", false, "shopify.com 已迁出 CF"},
		{"104.247.81.99", false, "acjp2.cloudflarest.link 实测非 CF（名字有 cloudflare 但地址不是）"},
		{"1.1.1.1", false, "CF 的 DNS 服务地址不在 CF 回源网段列表内"},
	}
	for _, c := range cases {
		got := IsCloudflareIPv4(net.ParseIP(c.ip))
		if got != c.want {
			t.Errorf("IsCloudflareIPv4(%s) = %v, want %v (%s)", c.ip, got, c.want, c.why)
		}
	}
	// IPv6 / 非法输入不能崩，且必须判为 false
	if IsCloudflareIPv4(net.ParseIP("2606:4700::1111")) {
		t.Errorf("IPv6 不应被判为 CF IPv4")
	}
	if IsCloudflareIPv4(nil) {
		t.Errorf("nil 不应被判为 CF")
	}
}

// TestResolvePreferredDomains 验证逐个校验：只有当前解析到 CF 段内的才保留。
func TestResolvePreferredDomains(t *testing.T) {
	stubLookup(t, map[string][]string{
		"good.test":  {"104.16.0.1", "172.67.0.1"},
		"mixed.test": {"8.8.8.8", "104.16.0.1"}, // 有一个落在 CF 段内即通过
		"bad.test":   {"8.8.8.8"},
		"alias.test": {"23.227.38.33"},
		// gone.test 不在表里 → 解析失败
	})

	domains, rejected := ResolvePreferredDomains(context.Background(), []string{
		"good.test", "bad.test", "gone.test", "mixed.test", "alias.test", "good.test", // 末项是重复项
	})

	want := []string{"good.test", "mixed.test"}
	if len(domains) != len(want) {
		t.Fatalf("domains = %v, want %v", domains, want)
	}
	for i := range want {
		if domains[i] != want[i] {
			t.Fatalf("domains = %v, want %v (顺序应保持候选顺序且去重)", domains, want)
		}
	}

	reasonOf := func(d string) string {
		for _, r := range rejected {
			if r.Domain == d {
				return r.Reason
			}
		}
		return ""
	}
	if r := reasonOf("bad.test"); !strings.Contains(r, "非 Cloudflare") {
		t.Errorf("bad.test 的剔除原因 = %q, 期望包含「非 Cloudflare」", r)
	}
	if r := reasonOf("alias.test"); !strings.Contains(r, "23.227.38.33") {
		t.Errorf("alias.test 的剔除原因应带上实际地址, got %q", r)
	}
	if r := reasonOf("gone.test"); r != "解析失败" {
		t.Errorf("gone.test 的剔除原因 = %q, want 解析失败", r)
	}
	if len(rejected) != 3 {
		t.Errorf("剔除数量 = %d, want 3 (%v)", len(rejected), rejected)
	}
}

// TestEligiblePreferredInbounds 是「只挂加密端口」的核心断言：
// 必须同时满足 WS + 源站 TLS + CF HTTPS 端口。
func TestEligiblePreferredInbounds(t *testing.T) {
	setupDomainDB(t)

	ok87 := mkWSInbound(t, 2087, model.VLESS, "ws", "tls")
	ok83 := mkWSInbound(t, 2083, model.VMESS, "ws", "tls")
	mkWSInbound(t, 2052, model.VMESS, "ws", "tls")  // CF 纯 HTTP 端口 → 必须排除
	mkWSInbound(t, 2096, model.VMESS, "ws", "none") // 源站明文 → 必须排除
	mkWSInbound(t, 2053, model.VMESS, "tcp", "tls") // 非 WS，CF 无法代理 → 必须排除
	mkWSInbound(t, 8388, model.Shadowsocks, "tcp", "none")

	// 关掉的入站也不该被挂
	off := mkWSInbound(t, 8443, model.VMESS, "ws", "tls")
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", off.Id).Update("enable", false).Error; err != nil {
		t.Fatalf("disable inbound: %v", err)
	}

	got, err := EligiblePreferredInbounds()
	if err != nil {
		t.Fatalf("EligiblePreferredInbounds: %v", err)
	}
	ids := make([]int, 0, len(got))
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	sort.Ints(ids)
	want := []int{ok83.Id, ok87.Id}
	sort.Ints(want)
	if len(ids) != len(want) || ids[0] != want[0] || ids[1] != want[1] {
		t.Fatalf("可用入站 = %v, want %v（只应包含 WS+源站TLS+CF HTTPS 端口的入站）", ids, want)
	}
	for _, g := range got {
		if !IsCFHTTPSPort(g.Port) {
			t.Errorf("选中的入站端口 %d 不是 CF HTTPS 端口", g.Port)
		}
	}
}

// TestSyncPreferredHosts_RealWriteAndIdempotent 走一遍真实的数据库写入：
// 建组、排除明文端口、重复调用不堆积、可清理。
func TestSyncPreferredHosts_RealWriteAndIdempotent(t *testing.T) {
	setupDomainDB(t)

	ib87 := mkWSInbound(t, 2087, model.VLESS, "ws", "tls")
	ib83 := mkWSInbound(t, 2083, model.VMESS, "ws", "tls")
	plain := mkWSInbound(t, 2052, model.VMESS, "ws", "tls") // CF 明文端口

	stubLookup(t, map[string][]string{
		"a.test": {"104.16.0.1"},
		"b.test": {"8.8.8.8"}, // 非 CF，应被剔除
	})

	ctx := context.Background()
	domains, rejected, warnings, err := SyncPreferredHosts(ctx, "d.example.com", []string{"a.test", "b.test"})
	if err != nil {
		t.Fatalf("SyncPreferredHosts: %v", err)
	}
	if len(domains) != 1 || domains[0] != "a.test" {
		t.Fatalf("domains = %v, want [a.test]", domains)
	}
	if len(rejected) != 1 || rejected[0].Domain != "b.test" {
		t.Fatalf("rejected = %v, want [b.test]", rejected)
	}
	if len(warnings) == 0 {
		t.Errorf("应给出生成结果提示")
	}

	rows := func() []model.Host {
		t.Helper()
		var hs []model.Host
		if err := database.GetDB().Where("remark = ?", preferredGroupRemark).Find(&hs).Error; err != nil {
			t.Fatalf("load hosts: %v", err)
		}
		return hs
	}

	hs := rows()
	// 1 个域名 × 2 个加密入站 = 2 行
	if len(hs) != 2 {
		t.Fatalf("hosts 行数 = %d, want 2 (%+v)", len(hs), hs)
	}
	for _, h := range hs {
		if h.InboundId == plain.Id {
			t.Fatalf("明文端口 %d 的入站被错误地挂上了优选域名", plain.Port)
		}
		if h.InboundId != ib87.Id && h.InboundId != ib83.Id {
			t.Fatalf("出现了预期外的入站 %d", h.InboundId)
		}
		if h.Address != "a.test" {
			t.Errorf("address = %q, want a.test", h.Address)
		}
		// 这三项必须保持「继承各自入站」的语义，否则订阅里的端口/路径/加密模式会错。
		if h.Port != 0 {
			t.Errorf("port = %d, want 0（0 = 继承入站端口）", h.Port)
		}
		if h.Path != "" {
			t.Errorf("path = %q, want 空（空 = 继承入站路径）", h.Path)
		}
		if h.Security != "same" {
			t.Errorf("security = %q, want same（继承入站的安全模式）", h.Security)
		}
		if h.Sni != "d.example.com" || h.HostHeader != "d.example.com" {
			t.Errorf("sni/hostHeader = %q/%q, want d.example.com", h.Sni, h.HostHeader)
		}
	}

	// 幂等：再跑一次不应堆积
	if _, _, _, err := SyncPreferredHosts(ctx, "d.example.com", []string{"a.test", "b.test"}); err != nil {
		t.Fatalf("第二次 SyncPreferredHosts: %v", err)
	}
	if n := len(rows()); n != 2 {
		t.Fatalf("重复调用后 hosts 行数 = %d, want 2（应幂等）", n)
	}

	// 状态查询
	statusDomains, statusInbounds, err := PreferredStatus()
	if err != nil {
		t.Fatalf("PreferredStatus: %v", err)
	}
	if len(statusDomains) != 1 || statusDomains[0] != "a.test" {
		t.Errorf("status domains = %v, want [a.test]", statusDomains)
	}
	if len(statusInbounds) != 2 {
		t.Errorf("status inbounds = %v, want 2 个", statusInbounds)
	}

	// 清理
	if err := RemovePreferredHosts(); err != nil {
		t.Fatalf("RemovePreferredHosts: %v", err)
	}
	if n := len(rows()); n != 0 {
		t.Fatalf("清理后仍有 %d 行", n)
	}
	// 再清理一次不应报错（幂等）
	if err := RemovePreferredHosts(); err != nil {
		t.Fatalf("二次 RemovePreferredHosts 应幂等: %v", err)
	}
}

// TestSyncPreferredHosts_NoEligibleInbound 覆盖「没有可用加密入站」的情况：
// 必须给出可操作的提示，而不是静默什么都不做。
func TestSyncPreferredHosts_NoEligibleInbound(t *testing.T) {
	setupDomainDB(t)
	mkWSInbound(t, 2052, model.VMESS, "ws", "tls") // 只有明文端口上的入站
	stubLookup(t, map[string][]string{"a.test": {"104.16.0.1"}})

	domains, _, warnings, err := SyncPreferredHosts(context.Background(), "d.example.com", []string{"a.test"})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(domains) != 0 {
		t.Errorf("不应生成域名, got %v", domains)
	}
	if len(warnings) == 0 || !strings.Contains(strings.Join(warnings, " "), "WS") {
		t.Errorf("应提示需要哪些条件, got %v", warnings)
	}
}

// TestBuiltinCandidatesAreWellFormed 守住内置候选列表的基本质量：
// 没有空项、没有重复、都是合法主机名。
func TestBuiltinCandidatesAreWellFormed(t *testing.T) {
	if len(PreferredDomainCandidates) == 0 {
		t.Fatal("内置候选列表为空")
	}
	seen := map[string]bool{}
	for _, d := range PreferredDomainCandidates {
		if strings.TrimSpace(d) == "" {
			t.Errorf("候选列表含空项")
			continue
		}
		if d != strings.ToLower(d) || strings.Contains(d, " ") {
			t.Errorf("候选 %q 应是小写且不含空格", d)
		}
		if !strings.Contains(d, ".") {
			t.Errorf("候选 %q 不像域名", d)
		}
		if seen[d] {
			t.Errorf("候选 %q 重复", d)
		}
		seen[d] = true
	}
}
