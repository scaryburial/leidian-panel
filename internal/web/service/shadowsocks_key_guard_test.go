package service

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// v1.9 待办 1：Shadowsocks-2022「客户端 × 入站」密钥长度冲突。
//
// 核心事实（决定了全部设计）：clients 表里一个客户端身份只有**一列** password，
// 而 SS2022 的 PSK 长度由 method 决定：
//
//	2022-blake3-aes-128-gcm        → 16 字节
//	2022-blake3-aes-256-gcm        → 32 字节
//	2022-blake3-chacha20-poly1305  → 32 字节
//
// 同一个身份同时挂到两种长度上，**不存在**一份能满足两边的密码；xray 对长度不符
// 的 PSK 会以 `bad key` 拒绝启动，后果是全部节点一起下线。因此入口只有两条路：
// 能修的修（单种长度、长度不对），修不了的明确拒绝（两种长度共存）。

// ssKey 造一个恰好 n 字节的 base64 PSK。
func ssKey(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i + 1)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// ssInboundSettings 造一份 SS2022 入站 settings：入站级 PSK 与每个客户端 PSK
// 都用指定长度（真实配置里两者都必须与 method 匹配，否则同样是 bad key）。
func ssInboundSettings(method string, n int, clients ...string) string {
	cs := make([]map[string]any, 0, len(clients))
	for _, email := range clients {
		cs = append(cs, map[string]any{"email": email, "password": ssKey(n)})
	}
	b, _ := json.Marshal(map[string]any{
		"method":   method,
		"password": ssKey(n),
		"clients":  cs,
	})
	return string(b)
}

func mkSSInbound(t *testing.T, tag, remark string, port int, method string, keyBytes int, emails ...string) *model.Inbound {
	t.Helper()
	ib := &model.Inbound{
		Tag: tag, Remark: remark, Enable: true, Port: port,
		Protocol:       model.Shadowsocks,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Settings:       ssInboundSettings(method, keyBytes, emails...),
	}
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("create inbound %s: %v", tag, err)
	}
	return ib
}

// ssClientPasswords 读出某个入站 settings 里每个 email 的 PSK。
func ssClientPasswords(t *testing.T, inboundID int) map[string]string {
	t.Helper()
	var ib model.Inbound
	if err := database.GetDB().First(&ib, inboundID).Error; err != nil {
		t.Fatalf("load inbound %d: %v", inboundID, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(ib.Settings), &m); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	out := map[string]string{}
	for _, raw := range anySlice(m["clients"]) {
		cm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		email, _ := cm["email"].(string)
		pw, _ := cm["password"].(string)
		out[email] = pw
	}
	return out
}

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// ---------------------------------------------------------------------------
// 1) 两种长度共存 → 明确拒绝，且报错必须可执行
// ---------------------------------------------------------------------------

func TestShadowsocksKeyGuard_RejectsCipherSizeConflict(t *testing.T) {
	setupSettingTestDB(t)

	const email = "shared@ss.test"
	ib128 := mkSSInbound(t, "ss-128", "SS2022-速度-8388", 8388, "2022-blake3-aes-128-gcm", 16, email)
	// 目标入站此刻还没有这个客户端（否则入口的「邮箱重复」检查会先拦住，
	// 测不到我们要验的密钥长度冲突）。
	ib256 := mkSSInbound(t, "ss-256", "SS2022-安全-8389", 8389, "2022-blake3-aes-256-gcm", 32)

	svc := &ClientService{}
	// 该身份当前只在 aes-128 上（16 字节合法），现在要挂到 aes-256 上。
	if err := svc.SyncInbound(nil, ib128.Id, []model.Client{{
		Email: email, Password: ssKey(16), Enable: true,
	}}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	got, notes, err := svc.enforceShadowsocksKeyGuard(database.GetDB(),
		model.Client{Email: email, Password: ssKey(16)},
		[]int{ib256.Id},
		ssKeyGuardOptions{RejectOnConflict: true, RepairSingleSize: true})

	if err == nil {
		t.Fatalf("同一身份挂 aes-128 + aes-256 必须被拒绝")
	}
	if got.Password != ssKey(16) {
		t.Errorf("被拒绝时不得改动客户端密码")
	}
	if len(notes) != 0 {
		t.Errorf("被拒绝时不应产生修正提示: %v", notes)
	}

	msg := err.Error()
	// 报错必须同时说清「为什么」和「怎么做」——面板把它当成操作者的提示。
	for _, want := range []string{
		email,
		"SS2022-速度-8388", "2022-blake3-aes-128-gcm", "16 字节",
		"SS2022-安全-8389", "2022-blake3-aes-256-gcm", "32 字节",
		"bad key", "拒绝启动",
		"①", "②", "③",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("拒绝原因里缺少 %q\n实际: %s", want, msg)
		}
	}
}

// ---------------------------------------------------------------------------
// 2) 单种长度但长度不对 → 自动修正（优先沿用入站已下发的密钥）
// ---------------------------------------------------------------------------

func TestShadowsocksKeyGuard_AdoptsKeyAlreadyIssuedByInbound(t *testing.T) {
	setupSettingTestDB(t)

	const email = "legacy@ss.test"
	ib := mkSSInbound(t, "ss-256", "SS2022-安全-8389", 8389, "2022-blake3-aes-256-gcm", 32, email)
	svc := &ClientService{}
	// 入站 settings 里已经下发了一个合法的 32 字节密钥；clients 表里却是 16 字节
	// （历史脏数据：曾经在 aes-128 上，method 改过之后没同步过来）。
	issued := ssClientPasswords(t, ib.Id)[email]
	if err := svc.SyncInbound(nil, ib.Id, []model.Client{{
		Email: email, Password: ssKey(16), Enable: true,
	}}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	fixed, notes, err := svc.enforceShadowsocksKeyGuard(database.GetDB(),
		model.Client{Email: email, Password: ssKey(16)},
		[]int{ib.Id}, ssKeyGuardOptions{RejectOnConflict: true, RepairSingleSize: true})
	if err != nil {
		t.Fatalf("单种长度必须能自动修正，不该报错: %v", err)
	}
	if fixed.Password != issued {
		t.Errorf("应沿用入站已下发的密钥（客户端无需重新导入），got %q want %q", fixed.Password, issued)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "已自动修正") {
		t.Errorf("应给出修正提示，got %v", notes)
	}
	if !strings.Contains(notes[0], "32 字节") {
		t.Errorf("提示里应说明要求多少字节，got %q", notes[0])
	}
}

func TestShadowsocksKeyGuard_GeneratesKeyWhenNothingUsable(t *testing.T) {
	setupSettingTestDB(t)

	const email = "broken@ss.test"
	ib := mkSSInbound(t, "ss-256", "SS2022-安全-8389", 8389, "2022-blake3-aes-256-gcm", 32, email)
	// 把入站 settings 里那条也改成非法的，逼出「必须重新生成」分支。
	var m map[string]any
	var raw model.Inbound
	if err := database.GetDB().First(&raw, ib.Id).Error; err != nil {
		t.Fatalf("load: %v", err)
	}
	_ = json.Unmarshal([]byte(raw.Settings), &m)
	m["clients"] = []any{map[string]any{"email": email, "password": "not-base64!!"}}
	bs, _ := json.Marshal(m)
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", ib.Id).Update("settings", string(bs)).Error; err != nil {
		t.Fatalf("update settings: %v", err)
	}

	svc := &ClientService{}
	fixed, notes, err := svc.enforceShadowsocksKeyGuard(database.GetDB(),
		model.Client{Email: email, Password: "not-base64!!"},
		[]int{ib.Id}, ssKeyGuardOptions{RejectOnConflict: true, RepairSingleSize: true})
	if err != nil {
		t.Fatalf("应能生成新密钥: %v", err)
	}
	if !validShadowsocksClientKey("2022-blake3-aes-256-gcm", fixed.Password) {
		t.Errorf("新生成的密钥长度不合法: %q", fixed.Password)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "重新获取订阅") {
		t.Errorf("重新生成密钥时必须提示客户端要重新获取订阅，got %v", notes)
	}
}

// 合法数据绝不能被改动：这是「已经存在的合法数据不能被动坏」的底线。
func TestShadowsocksKeyGuard_LeavesValidDataUntouched(t *testing.T) {
	setupSettingTestDB(t)

	const email = "ok@ss.test"
	ibA := mkSSInbound(t, "ss-a", "SS2022-速度-8388", 8388, "2022-blake3-aes-256-gcm", 32, email)
	ibB := mkSSInbound(t, "ss-b", "SS2022-综合-8390", 8390, "2022-blake3-aes-256-gcm", 32, email)
	svc := &ClientService{}
	key := ssKey(32)
	for _, id := range []int{ibA.Id, ibB.Id} {
		if err := svc.SyncInbound(nil, id, []model.Client{{Email: email, Password: key, Enable: true}}); err != nil {
			t.Fatalf("SyncInbound: %v", err)
		}
	}

	fixed, notes, err := svc.enforceShadowsocksKeyGuard(database.GetDB(),
		model.Client{Email: email, Password: key},
		[]int{ibA.Id, ibB.Id}, ssKeyGuardOptions{RejectOnConflict: true, RepairSingleSize: true})
	if err != nil {
		t.Fatalf("两条同长度入站是合法的: %v", err)
	}
	if fixed.Password != key {
		t.Errorf("合法密钥被改动了: %q → %q", key, fixed.Password)
	}
	if len(notes) != 0 {
		t.Errorf("合法数据不该产生提示: %v", notes)
	}
}

// 非 2022 / 非 Shadowsocks 入站不产生长度约束，不能被误报。
func TestShadowsocksKeyGuard_IgnoresNonSS2022(t *testing.T) {
	setupSettingTestDB(t)

	legacy := &model.Inbound{
		Tag: "ss-legacy", Remark: "SS-legacy", Enable: true, Port: 8388,
		Protocol:       model.Shadowsocks,
		StreamSettings: `{"network":"tcp","security":"none"}`,
		Settings:       `{"method":"aes-256-gcm","password":"whatever","clients":[{"email":"a","password":"short"}]}`,
	}
	if err := database.GetDB().Create(legacy).Error; err != nil {
		t.Fatalf("create legacy ss: %v", err)
	}
	vless := &model.Inbound{
		Tag: "vless", Enable: true, Port: 8443, Protocol: model.VLESS,
		Settings: `{"clients":[{"id":"11111111-1111-1111-1111-111111111111","email":"a"}]}`,
	}
	if err := database.GetDB().Create(vless).Error; err != nil {
		t.Fatalf("create vless: %v", err)
	}

	svc := &ClientService{}
	fixed, notes, err := svc.enforceShadowsocksKeyGuard(database.GetDB(),
		model.Client{Email: "a", Password: "short"},
		[]int{legacy.Id, vless.Id}, ssKeyGuardOptions{RejectOnConflict: true, RepairSingleSize: true})
	if err != nil {
		t.Fatalf("非 2022 入站不该报错: %v", err)
	}
	if fixed.Password != "short" || len(notes) != 0 {
		t.Errorf("非 2022 入站不该改动任何东西: pw=%q notes=%v", fixed.Password, notes)
	}
}

// ---------------------------------------------------------------------------
// 3) 入口：挂载路径（ClientService.Attach）必须拦住，且不留半截写入
// ---------------------------------------------------------------------------

func TestAttach_RejectsCrossCipherSizeCombo(t *testing.T) {
	setupSettingTestDB(t)

	const email = "attach@ss.test"
	ib128 := mkSSInbound(t, "ss-128", "SS2022-速度-8388", 8388, "2022-blake3-aes-128-gcm", 16, email)
	ib256 := mkSSInbound(t, "ss-256", "SS2022-安全-8389", 8389, "2022-blake3-aes-256-gcm", 32)
	svc := &ClientService{}
	inboundSvc := &InboundService{}

	rec := &model.ClientRecord{Email: email, Password: ssKey(16), Enable: true, SubID: "sub-attach-1"}
	if err := database.GetDB().Create(rec).Error; err != nil {
		t.Fatalf("create client record: %v", err)
	}
	if err := svc.SyncInbound(nil, ib128.Id, []model.Client{{Email: email, Password: ssKey(16), Enable: true}}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	_, err := svc.Attach(inboundSvc, rec.Id, []int{ib256.Id})
	if err == nil {
		t.Fatalf("把已经在 aes-128 上的客户端挂到 aes-256 必须被拒绝")
	}
	if !strings.Contains(err.Error(), "bad key") {
		t.Errorf("报错应解释后果（bad key / 全部节点下线），got %v", err)
	}

	// 关键：拒绝之后不能留下半截状态——client_inbounds 里不能出现 aes-256 的链接。
	ids, err := svc.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		t.Fatalf("GetInboundIdsForRecord: %v", err)
	}
	for _, id := range ids {
		if id == ib256.Id {
			t.Fatalf("被拒绝的挂载却写进了 client_inbounds（半截写入）")
		}
	}
	// aes-256 入站的 settings 也不能被塞进这个客户端。
	if _, hit := ssClientPasswords(t, ib256.Id)[email]; hit {
		t.Errorf("被拒绝的挂载却改了 aes-256 入站的 settings")
	}
}

// ---------------------------------------------------------------------------
// 4) 入口：新增客户端到入站（AddInboundClient）
// ---------------------------------------------------------------------------

func TestAddInboundClient_RejectsCrossCipherSizeCombo(t *testing.T) {
	setupSettingTestDB(t)

	const email = "add@ss.test"
	ib128 := mkSSInbound(t, "ss-128", "SS2022-速度-8388", 8388, "2022-blake3-aes-128-gcm", 16, email)
	ib256 := mkSSInbound(t, "ss-256", "SS2022-安全-8389", 8389, "2022-blake3-aes-256-gcm", 32)
	svc := &ClientService{}
	inboundSvc := &InboundService{}
	if err := svc.SyncInbound(nil, ib128.Id, []model.Client{{
		Email: email, Password: ssKey(16), Enable: true, SubID: "a1b2c3d4e5f60718",
	}}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}
	// 用同一个 subId：这样才不会被入口的「邮箱重复」检查先拦住
	// （同一身份挂多条入站靠 subId 认定为同一个人），测到的才是密钥长度冲突。
	rec, err := svc.GetRecordByEmail(nil, email)
	if err != nil {
		t.Fatalf("GetRecordByEmail: %v", err)
	}

	payload, _ := json.Marshal(map[string][]model.Client{"clients": {{
		Email: email, Password: ssKey(16), Enable: true, SubID: rec.SubID,
	}}})
	_, err = svc.AddInboundClient(inboundSvc, &model.Inbound{Id: ib256.Id, Settings: string(payload)})
	if err == nil {
		t.Fatal("把已在 aes-128 上的身份加到 aes-256 入站必须被拒绝")
	}
	if !strings.Contains(err.Error(), "16 字节") || !strings.Contains(err.Error(), "32 字节") {
		t.Errorf("报错应写清两种长度，got %v", err)
	}
	if _, hit := ssClientPasswords(t, ib256.Id)[email]; hit {
		t.Errorf("被拒绝的新增却改了入站 settings")
	}
}

// ---------------------------------------------------------------------------
// 5) 入口：入站保存（method 在 aes-256 ↔ aes-128 之间切换）
// ---------------------------------------------------------------------------

func TestUpdateInbound_RejectsMethodChangeThatCreatesConflict(t *testing.T) {
	setupSettingTestDB(t)

	const email = "method@ss.test"
	ib256a := mkSSInbound(t, "ss-256-a", "SS2022-安全-8389", 8389, "2022-blake3-aes-256-gcm", 32, email)
	ib256b := mkSSInbound(t, "ss-256-b", "SS2022-综合-8390", 8390, "2022-blake3-aes-256-gcm", 32, email)
	svc := &ClientService{}
	inboundSvc := &InboundService{}
	for _, id := range []int{ib256a.Id, ib256b.Id} {
		if err := svc.SyncInbound(nil, id, []model.Client{{Email: email, Password: ssKey(32), Enable: true}}); err != nil {
			t.Fatalf("SyncInbound: %v", err)
		}
	}

	// 把 ib256b 的 method 改成 aes-128：同一个客户端立刻落进 16/32 两种长度。
	update := *ib256b
	update.Settings = ssInboundSettings("2022-blake3-aes-128-gcm", 16, email)
	if _, _, err := inboundSvc.UpdateInbound(&update); err == nil {
		t.Fatal("把共享客户端的入站从 aes-256 改成 aes-128 必须被拒绝")
	} else if !strings.Contains(err.Error(), "bad key") {
		t.Errorf("报错应解释后果，got %v", err)
	}

	// 拒绝之后 settings 必须原样（还是 aes-256），否则下次启动照样 bad key。
	fresh, err := inboundSvc.GetInbound(ib256b.Id)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	if shadowsocksMethodFromSettings(fresh.Settings) != "2022-blake3-aes-256-gcm" {
		t.Errorf("被拒绝的保存却改动了 method: %s", shadowsocksMethodFromSettings(fresh.Settings))
	}
}

// 改 method 但**该客户端只在这条入站上**：没有冲突，必须放行。
func TestUpdateInbound_AllowsMethodChangeWithoutConflict(t *testing.T) {
	setupSettingTestDB(t)

	const email = "solo@ss.test"
	ib := mkSSInbound(t, "ss-solo", "SS2022-单独-8391", 8391, "2022-blake3-aes-256-gcm", 32, email)
	svc := &ClientService{}
	inboundSvc := &InboundService{}
	if err := svc.SyncInbound(nil, ib.Id, []model.Client{{Email: email, Password: ssKey(32), Enable: true}}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	update := *ib
	update.Settings = ssInboundSettings("2022-blake3-aes-128-gcm", 16, email)
	if _, _, err := inboundSvc.UpdateInbound(&update); err != nil {
		t.Fatalf("只有一条入站时换 method 不该被拦: %v", err)
	}
	fresh, _ := inboundSvc.GetInbound(ib.Id)
	if shadowsocksMethodFromSettings(fresh.Settings) != "2022-blake3-aes-128-gcm" {
		t.Errorf("method 未被保存: %s", shadowsocksMethodFromSettings(fresh.Settings))
	}
	// normalizeShadowsocksClientKeys 必须把客户端 PSK 一起改成 16 字节，
	// 否则 xray 依旧 bad key。
	if pw := ssClientPasswords(t, ib.Id)[email]; !validShadowsocksClientKey("2022-blake3-aes-128-gcm", pw) {
		t.Errorf("换 method 后客户端 PSK 长度不合法: %q", pw)
	}
	rec, err := svc.GetRecordByEmail(nil, email)
	if err != nil {
		t.Fatalf("GetRecordByEmail: %v", err)
	}
	if !validShadowsocksClientKey("2022-blake3-aes-128-gcm", rec.Password) {
		t.Errorf("clients 表里的密码也必须跟着换成合法长度（否则订阅下发的是废密钥）: %q", rec.Password)
	}
}

// ---------------------------------------------------------------------------
// 6) GetXrayConfig 级别：真正的产物必须每条入站的 PSK 长度都对
// ---------------------------------------------------------------------------

// 合法的多入站情形：同一个客户端挂在两条 aes-256 入站上，生成的 xray 配置里
// 两条入站都必须带**同一个合法密钥**（这就是「两个入站都能认证」）。
func TestGetXrayConfig_SharedClientOnSameCipherAuthenticatesOnBoth(t *testing.T) {
	setupSettingTestDB(t)

	const email = "both@ss.test"
	ibA := mkSSInbound(t, "ss-a", "SS2022-速度-8388", 8388, "2022-blake3-aes-256-gcm", 32, email)
	ibB := mkSSInbound(t, "ss-b", "SS2022-综合-8390", 8390, "2022-blake3-aes-256-gcm", 32, email)
	svc := &ClientService{}
	key := ssKey(32)
	for _, id := range []int{ibA.Id, ibB.Id} {
		if err := svc.SyncInbound(nil, id, []model.Client{{Email: email, Password: key, Enable: true}}); err != nil {
			t.Fatalf("SyncInbound: %v", err)
		}
	}

	for _, tc := range []struct{ tag, method string }{
		{"ss-a", "2022-blake3-aes-256-gcm"},
		{"ss-b", "2022-blake3-aes-256-gcm"},
	} {
		settings := emittedInboundSettings(t, tc.tag)
		pw := emittedClientPassword(settings, email)
		if pw != key {
			t.Errorf("%s 下发的客户端密钥 = %q, want %q", tc.tag, pw, key)
		}
		if !validShadowsocksClientKey(tc.method, pw) {
			t.Errorf("%s 的客户端密钥长度与 method 不匹配 → xray 会 bad key 拒绝启动", tc.tag)
		}
	}
}

// 入口拦不住的历史脏数据（修复前就已经存在）不能把 xray 拖死：此刻 xray.go 的
// 就地救回必须保证**每条入站的客户端密钥长度都合法**，xray 才能启动。
// 这一条同时记录了产品层面的遗留：救回来的密钥只落在配置里，订阅下发的仍是
// clients 表那一份，所以其中一个入站客户端必然认证失败 —— 正是入口拦截存在的理由。
func TestGetXrayConfig_LegacyCipherConflictStillBoots(t *testing.T) {
	setupSettingTestDB(t)

	const email = "legacy-bad@ss.test"
	// 历史数据：同一个身份被挂到 aes-128 与 aes-256 两条入站上。
	ib128 := mkSSInbound(t, "ss-128", "SS2022-速度-8388", 8388, "2022-blake3-aes-128-gcm", 16, email)
	ib256 := mkSSInbound(t, "ss-256", "SS2022-安全-8389", 8389, "2022-blake3-aes-256-gcm", 32, email)
	svc := &ClientService{}
	// clients 表只有一份密码，32 字节（对 aes-256 合法、对 aes-128 非法）。
	shared := ssKey(32)
	for _, id := range []int{ib128.Id, ib256.Id} {
		if err := svc.SyncInbound(nil, id, []model.Client{{Email: email, Password: shared, Enable: true}}); err != nil {
			t.Fatalf("SyncInbound: %v", err)
		}
	}

	for _, tc := range []struct {
		tag     string
		method  string
		keyByte int
	}{
		{"ss-128", "2022-blake3-aes-128-gcm", 16},
		{"ss-256", "2022-blake3-aes-256-gcm", 32},
	} {
		settings := emittedInboundSettings(t, tc.tag)
		pw := emittedClientPassword(settings, email)
		if pw == "" {
			t.Fatalf("%s 没有下发客户端密钥 → xray 一样会 bad key", tc.tag)
		}
		if !validShadowsocksClientKey(tc.method, pw) {
			t.Errorf("%s 的客户端密钥 %q 长度不为 %d 字节 → xray 会以 bad key 拒绝启动，全部节点下线",
				tc.tag, pw, tc.keyByte)
		}
	}
	// aes-128 那条救回时沿用了它自己 settings 里已经下发的 16 字节密钥
	// （客户端此前导入的就是它），所以老客户端仍能认证。
	if got := emittedClientPassword(emittedInboundSettings(t, "ss-128"), email); got != ssClientPasswords(t, ib128.Id)[email] {
		t.Errorf("aes-128 一侧应沿用入站已下发的密钥，got %q want %q", got, ssClientPasswords(t, ib128.Id)[email])
	}
}

// emittedInboundSettings 从 GetXrayConfig 的真实产物里取出某条入站的 settings JSON。
func emittedInboundSettings(t *testing.T, tag string) map[string]any {
	t.Helper()
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag != tag {
			continue
		}
		var s map[string]any
		if err := json.Unmarshal([]byte(cfg.InboundConfigs[i].Settings), &s); err != nil {
			t.Fatalf("unmarshal emitted settings: %v", err)
		}
		return s
	}
	t.Fatalf("inbound %q not found in generated config", tag)
	return nil
}

func emittedClientPassword(settings map[string]any, email string) string {
	for _, raw := range anySlice(settings["clients"]) {
		cm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if e, _ := cm["email"].(string); strings.EqualFold(e, email) {
			pw, _ := cm["password"].(string)
			return pw
		}
	}
	return ""
}