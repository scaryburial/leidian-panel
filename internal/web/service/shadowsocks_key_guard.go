package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"

	"gorm.io/gorm"
)

// 本文件解决 v1.9 待办里「最危险的一条」：Shadowsocks-2022 的
// **客户端 × 入站密钥长度冲突**。
//
// 背景：clients 表里一个客户端身份只有**一列** password，而 SS2022 的 PSK 长度
// 由 method 决定（aes-128 → 16 字节，aes-256 / chacha20 → 32 字节）。把同一个
// 身份同时挂到两种长度不同的入站上，**不存在**一份能同时满足两边的密码。
//
// xray 对不合法长度的 PSK 不是「这条入站里的这个用户不可用」，而是**拒绝启动**：
//
//	Failed to start: main: failed to create server >
//	proxy/shadowsocks_2022: create service > bad key
//
// 后果是**整台服务器的全部节点一起下线**。所以必须在「产生这种组合的入口」就
// 拦住，而不是等 xray.go 里的就地救回（那只是最后一层兜底，代价是那个入站的
// 客户端必然连不上，因为订阅下发的还是 clients 表里那份密码）。
//
// 本文件给出两条策略，调用方按入口语义选择：
//
//	· 冲突（目标集合里出现两种长度）→ **明确拒绝**，中文报错直接显示在面板上，
//	  并写清三种可执行的做法。因为不存在自动修复方案：改哪一边都会破坏另一边
//	  已经分发的密钥。
//	· 单种长度但客户端密钥长度不对（历史脏数据、换过 method）→ **自动修正**，
//	  优先沿用该入站 settings 里已经下发给订阅的那个密钥（保连接不断），
//	  否则新生成一个合法长度的密钥；修正结果同时写回 clients 表，
//	  这样 xray 配置与订阅下发的密钥重新一致。

// ssKeyRequirement 描述一条 Shadowsocks-2022 入站对客户端 PSK 的长度要求。
type ssKeyRequirement struct {
	InboundID int
	Remark    string
	Port      int
	Method    string
	KeyBytes  int
}

// label 渲染成给操作者看的一段描述（含端口/method/要求字节数）。
func (r ssKeyRequirement) label() string {
	name := strings.TrimSpace(r.Remark)
	if name == "" {
		name = fmt.Sprintf("入站 #%d", r.InboundID)
	}
	return fmt.Sprintf("%s（端口 %d，%s，要求 %d 字节）", name, r.Port, r.Method, r.KeyBytes)
}

// shadowsocksKeyRequirements 收集给定入站里所有 Shadowsocks-2022 入站的长度要求。
//
// 非 Shadowsocks、legacy SS（aes-256-gcm 等非 2022 method）都不产生约束：
// 它们不按字节校验 PSK，把它们算进来只会误报。
func shadowsocksKeyRequirements(tx *gorm.DB, inboundIDs []int) ([]ssKeyRequirement, error) {
	if len(inboundIDs) == 0 {
		return nil, nil
	}
	if tx == nil {
		tx = database.GetDB()
	}
	var inbounds []*model.Inbound
	if err := tx.Where("id IN ?", inboundIDs).Find(&inbounds).Error; err != nil {
		return nil, err
	}
	out := make([]ssKeyRequirement, 0, len(inbounds))
	for _, ib := range inbounds {
		if ib == nil || ib.Protocol != model.Shadowsocks {
			continue
		}
		method := shadowsocksMethodFromSettings(ib.Settings)
		n := shadowsocksKeyBytes(method)
		if n == 0 {
			continue
		}
		out = append(out, ssKeyRequirement{
			InboundID: ib.Id, Remark: ib.Remark, Port: ib.Port, Method: method, KeyBytes: n,
		})
	}
	// 顺序稳定，报错信息才可复现（也便于测试断言）。
	sort.Slice(out, func(i, j int) bool {
		if out[i].KeyBytes != out[j].KeyBytes {
			return out[i].KeyBytes > out[j].KeyBytes
		}
		return out[i].InboundID < out[j].InboundID
	})
	return out, nil
}

// ssKeySizes 返回要求里出现过的不同密钥长度（升序）。
func ssKeySizes(reqs []ssKeyRequirement) []int {
	seen := map[int]bool{}
	var sizes []int
	for _, r := range reqs {
		if !seen[r.KeyBytes] {
			seen[r.KeyBytes] = true
			sizes = append(sizes, r.KeyBytes)
		}
	}
	sort.Ints(sizes)
	return sizes
}

// ssKeyConflictError 构造「无法自动修复」时的中文可执行报错。
//
// 面板会把 service 层返回的 error 直接显示成提示，所以这句话必须同时回答
// 「为什么不行」和「我该怎么做」——不然操作者只能看到一个 bad key 的后果。
func ssKeyConflictError(email string, reqs []ssKeyRequirement, sizes []int) error {
	var lines []string
	for _, r := range reqs {
		lines = append(lines, "  · "+r.label())
	}
	parts := make([]string, 0, len(sizes))
	for _, n := range sizes {
		parts = append(parts, fmt.Sprintf("%d 字节", n))
	}
	msg := fmt.Sprintf(
		"客户端 %q 不能同时挂到密钥长度不同的 Shadowsocks-2022 入站上：\n%s\n"+
			"同一个客户端只有一份密码（clients.password），无法同时满足 %s；"+
			"xray 遇到长度不符的 PSK 会以 bad key **拒绝启动**，整台服务器的所有节点都会下线。\n"+
			"请任选一种做法：\n"+
			"  ① 为其中一种 method 新建一个独立客户端（换一个邮箱），只挂对应的入站；\n"+
			"  ② 把这些入站统一改成同一种 method（aes-128 与 aes-256 任选其一）；\n"+
			"  ③ 取消该客户端在其中一种入站上的挂载。",
		email, strings.Join(lines, "\n"), strings.Join(parts, " 与 "))
	return common.NewError(msg)
}

// ssKeyGuardOptions 让调用方按入口语义选择策略。
type ssKeyGuardOptions struct {
	// RejectOnConflict：发现两种长度共存时返回可显示的报错（拒绝这次操作）。
	// false 只记录警告——用于「不涉及凭据变更的老数据编辑」，
	// 否则历史脏数据会让流量重置、改启用状态等无关操作全部失败。
	RejectOnConflict bool
	// RepairSingleSize：只有一种长度要求、而客户端 PSK 长度不符时自动修正。
	RepairSingleSize bool
}

// ssAdoptKeyFromInbounds 在目标入站的 settings JSON 里找这个 email 已经下发的密钥；
// 找到合法的就沿用它。这样修历史脏数据时不会把已经在用的客户端踢下线。
func ssAdoptKeyFromInbounds(tx *gorm.DB, inboundIDs []int, email, method string) string {
	if tx == nil {
		tx = database.GetDB()
	}
	if len(inboundIDs) == 0 || strings.TrimSpace(email) == "" {
		return ""
	}
	// 用 id 升序保证结果稳定：同一个 email 在多个同长度入站上有不同 settings 值时，
	// 永远取同一个（最小的 id），避免每次调用修出不同的密码。
	var inbounds []*model.Inbound
	if err := tx.Where("id IN ?", inboundIDs).Order("id ASC").Find(&inbounds).Error; err != nil {
		return ""
	}
	for _, ib := range inbounds {
		if ib == nil || ib.Protocol != model.Shadowsocks {
			continue
		}
		if shadowsocksMethodFromSettings(ib.Settings) != method {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(ib.Settings), &m); err != nil {
			continue
		}
		if pw := settingsClientPassword(m, email); validShadowsocksClientKey(method, pw) {
			return pw
		}
	}
	return ""
}

// enforceShadowsocksKeyGuard 是「同一客户端的 PSK 长度必须与它所在的每条
// Shadowsocks-2022 入站匹配」这条不变量的**唯一实施点**。
//
// targetInboundIDs 是本次操作要挂上去（或要改动的）入站；函数会把它与每个客户端
// **已有的**入站挂载合并后再判定，否则「先挂 aes-256、再挂 aes-128」这种分两步
// 走出来的冲突就漏掉了。
//
// 返回可能被修正过 Password 的客户端切片（与入参同序）与给操作者/日志的提示。
func (s *ClientService) enforceShadowsocksKeyGuard(
	tx *gorm.DB, client model.Client, targetInboundIDs []int, opts ssKeyGuardOptions,
) (model.Client, []string, error) {
	email := strings.TrimSpace(client.Email)
	if email == "" {
		return client, nil, nil
	}
	if tx == nil {
		tx = database.GetDB()
	}

	ids := make([]int, 0, len(targetInboundIDs)+4)
	seen := map[int]bool{}
	for _, id := range targetInboundIDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	// 已有的挂载必须算进来：冲突是「身份 × 全部入站」的属性，不是单次调用的。
	attached, err := s.GetInboundIdsForEmail(tx, email)
	if err != nil {
		return client, nil, err
	}
	for _, id := range attached {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}

	reqs, err := shadowsocksKeyRequirements(tx, ids)
	if err != nil {
		return client, nil, err
	}
	sizes := ssKeySizes(reqs)
	if len(sizes) == 0 {
		return client, nil, nil // 没有任何 2022 入站：无约束
	}

	if len(sizes) > 1 {
		if opts.RejectOnConflict {
			return client, nil, ssKeyConflictError(email, reqs, sizes)
		}
		msg := fmt.Sprintf(
			"客户端 %q 同时挂在密钥长度不同的 Shadowsocks-2022 入站上（%s）；"+
				"密码只可能满足一边，xray 启动时会用兜底密钥保住服务，但另一边无法认证。请拆分该客户端或统一 method",
			email, ssSizesText(sizes))
		logger.Warning(msg)
		return client, []string{msg}, nil
	}

	method := reqs[0].Method
	if validShadowsocksClientKey(method, client.Password) {
		return client, nil, nil // 已经合法：绝不改动已分发的密钥
	}
	if !opts.RepairSingleSize {
		msg := fmt.Sprintf(
			"客户端 %q 的密码长度与 Shadowsocks-2022 入站 %s 不符（%s 要求 %d 字节），需要重新生成密钥后客户端才能认证",
			email, reqs[0].label(), method, reqs[0].KeyBytes)
		logger.Warning(msg)
		return client, []string{msg}, nil
	}

	// 修正：优先沿用入站 settings 里已下发的密钥（订阅之前给出去的就是它，
	// 沿用它客户端不用重新导入），否则新生成一个长度合法的。
	adopted := ""
	key := ssAdoptKeyFromInbounds(tx, ids, email, method)
	if key != "" {
		adopted = "（沿用该入站已下发的密钥，客户端无需重新导入）"
	} else {
		key = randomShadowsocksClientKey(method)
		adopted = "（已生成新密钥，客户端需要重新获取订阅/分享链接）"
	}
	old := client.Password
	client.Password = key
	msg := fmt.Sprintf(
		"客户端 %q 的密码长度与 Shadowsocks-2022 入站 %s 不符（%s 要求 %d 字节，原密码解码后 %d 字节），已自动修正%s；"+
			"否则 xray 会以 bad key 拒绝启动，全部节点下线",
		email, reqs[0].label(), method, reqs[0].KeyBytes, ssDecodedKeyLen(old), adopted)
	logger.Warning(msg)
	return client, []string{msg}, nil
}

// ssSizesText 把长度集合渲染成「16 字节 / 32 字节」。
func ssSizesText(sizes []int) string {
	parts := make([]string, 0, len(sizes))
	for _, n := range sizes {
		parts = append(parts, fmt.Sprintf("%d 字节", n))
	}
	return strings.Join(parts, " / ")
}

// ssDecodedKeyLen 返回密钥解码后的字节数，解不出来返回 0（仅用于报错文本）。
func ssDecodedKeyLen(key string) int {
	if key == "" {
		return 0
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return 0
	}
	return len(decoded)
}

// enforceShadowsocksKeyGuardForClients 对一批客户端做同一套判定（批量创建路径）。
// 任一客户端冲突即整体拒绝，避免部分写入。
func (s *ClientService) enforceShadowsocksKeyGuardForClients(
	tx *gorm.DB, clients []model.Client, targetInboundIDs []int, opts ssKeyGuardOptions,
) ([]model.Client, []string, error) {
	out := make([]model.Client, len(clients))
	copy(out, clients)
	var notes []string
	for i := range out {
		fixed, n, err := s.enforceShadowsocksKeyGuard(tx, out[i], targetInboundIDs, opts)
		if err != nil {
			return clients, nil, err
		}
		out[i] = fixed
		notes = append(notes, n...)
	}
	return out, notes, nil
}

// ssInboundEmbeddedClientsConflict 守住「保存入站」这条路径。
//
// 这是实践中**最可能**造出冲突的入口：把一条 aes-256 入站的 method 改成
// aes-128，而它上面的某个客户端还被挂在另一条 aes-256 入站上。
//
// oldKeyBytes 是本次保存前该入站的密钥长度（0 表示新建）。只有当这次保存
// **引入**了冲突（长度发生了变化，或本来就是新入站）才拒绝；对历史上已经存在
// 的冲突只记警告——否则一个历史脏数据会让这条入站之后所有的编辑都失败。
func ssInboundEmbeddedClientsConflict(tx *gorm.DB, inbound *model.Inbound, oldKeyBytes int) error {
	if inbound == nil || inbound.Protocol != model.Shadowsocks {
		return nil
	}
	method := shadowsocksMethodFromSettings(inbound.Settings)
	size := shadowsocksKeyBytes(method)
	if size == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(inbound.Settings), &m); err != nil {
		return nil
	}
	clients, _ := m["clients"].([]any)
	if len(clients) == 0 {
		return nil
	}
	self := ssKeyRequirement{InboundID: inbound.Id, Remark: inbound.Remark, Port: inbound.Port, Method: method, KeyBytes: size}
	svc := &ClientService{}
	for _, raw := range clients {
		cm, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		email, _ := cm["email"].(string)
		email = strings.TrimSpace(email)
		if email == "" {
			continue
		}
		others, err := svc.GetInboundIdsForEmail(tx, email)
		if err != nil {
			return err
		}
		otherIDs := make([]int, 0, len(others))
		for _, id := range others {
			if id != inbound.Id {
				otherIDs = append(otherIDs, id)
			}
		}
		reqs, err := shadowsocksKeyRequirements(tx, otherIDs)
		if err != nil {
			return err
		}
		conflicting := make([]ssKeyRequirement, 0, len(reqs)+1)
		for _, r := range reqs {
			if r.KeyBytes != size {
				conflicting = append(conflicting, r)
			}
		}
		if len(conflicting) == 0 {
			continue
		}
		conflicting = append(conflicting, self)
		sizes := ssKeySizes(conflicting)
		if oldKeyBytes == size {
			// 冲突在这次保存之前就存在：不因为历史数据挡住无关的编辑。
			logger.Warningf("入站 %s 上的客户端 %q 与其它 Shadowsocks-2022 入站存在密钥长度冲突（已是历史状态）；"+
				"xray 会用兜底密钥保住启动，但请尽快拆分该客户端或统一 method", self.label(), email)
			continue
		}
		return ssKeyConflictError(email, conflicting, sizes)
	}
	return nil
}