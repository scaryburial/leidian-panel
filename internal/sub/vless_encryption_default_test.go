package sub

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// VLESS 入站的 encryption 在保存时会被 model.StripVlessInboundEncryption 删除
// （VLESS 入站该字段应使用 decryption，多带 encryption 会让内核拒绝入站配置），
// 所以订阅生成阶段它**总是缺失**。
//
// JSON 订阅必须把缺失/空串补成 "none"：Xray 26.x 加载缺少该字段的 VLESS 出站会
// 直接拒绝启动整个配置 ——
//
//	Failed to start: infra/conf: VLESS users: please add/set
//	"encryption":"none" for every user
//
// 这是用真实 Xray 内核加载本面板生成的 JSON 订阅时实测到的（修复前 0/1 通过）。
func TestJsonVlessEncryptionNeverEmpty(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		want     string
	}{
		{"字段缺失_Strip后的真实状态", `{"clients":[],"decryption":"none"}`, "none"},
		{"空串_预设创建的形态", `{"clients":[],"decryption":"none","encryption":""}`, "none"},
		{"显式none", `{"clients":[],"decryption":"none","encryption":"none"}`, "none"},
		{"仅空白", `{"clients":[],"encryption":"   "}`, "none"},
		{"ML-KEM必须保留", `{"clients":[],"encryption":"mlkem768x25519plus.native.0rtt"}`, "mlkem768x25519plus.native.0rtt"},
	}

	svc := NewSubJsonService("", "", "", "", nil)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ib := &model.Inbound{
				Listen:   "203.0.113.1",
				Port:     443,
				Protocol: model.VLESS,
				Remark:   "vless-test",
				Settings: c.settings,
			}
			raw := svc.genVless(&SubService{}, ib, nil,
				model.Client{ID: "11111111-2222-4333-8444-555555555555"}, "")

			var out struct {
				Protocol string         `json:"protocol"`
				Settings map[string]any `json:"settings"`
			}
			if err := json.Unmarshal(raw, &out); err != nil {
				t.Fatalf("生成的出站不是合法 JSON: %v\n%s", err, raw)
			}
			if out.Protocol != "vless" {
				t.Fatalf("protocol = %q, want vless", out.Protocol)
			}
			got := out.Settings["encryption"]
			if got != c.want {
				t.Fatalf("encryption = %#v, want %q\n完整输出: %s", got, c.want, raw)
			}
		})
	}
}

// 出站里绝不能出现空的 encryption —— 这正是让真实内核启动失败的值。
func TestJsonVlessEncryptionEmptyIsRejectedByCore(t *testing.T) {
	svc := NewSubJsonService("", "", "", "", nil)
	ib := &model.Inbound{
		Listen:   "203.0.113.1",
		Port:     443,
		Protocol: model.VLESS,
		Settings: `{"clients":[],"decryption":"none"}`,
	}
	raw := svc.genVless(&SubService{}, ib, nil, model.Client{ID: "u"}, "")
	var out struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("JSON 解析失败: %v", err)
	}
	if v, _ := out.Settings["encryption"].(string); v == "" {
		t.Fatalf("encryption 不能为空串（Xray 会拒绝启动），got %#v", out.Settings["encryption"])
	}
}
