// 优选域名（Cloudflare 优选）自动生成。
//
// 背景：本文件解决的问题是「面板启用域名后，怎样自动带上一批优选域名」。
//
// # Cloudflare 端口语义（实测结论，非推测）
//
// Cloudflare 代理只支持有限的端口，且明确分成两类：
//
//	HTTPS 端口  443 / 2053 / 2083 / 2087 / 2096 / 8443
//	    客户端→CF 是 TLS；CF→源站也是 TLS。
//	HTTP  端口  80 / 8080 / 8880 / 2052 / 2082 / 2086 / 2095
//	    CF 在这类端口上**不接受客户端的 TLS**；CF→源站是**明文 HTTP**。
//
// 这一点是在真实源站上抓包确认的（把原始字节监听器绑在对应端口，让 CF 打过来）：
//
//	端口 2083 收到 16 03 01 02 00 01 ..        → TLS ClientHello
//	端口 2082 收到 "GET / HTTP/1.1" + cf-ray    → 明文 HTTP
//
// 直接推论（决定了本文件的全部设计）：
//
//  1. 同一份「源站 TLS」入站配置放到 HTTP 端口上必然失败：CF 会发明文过去，
//     而源站等着 TLS 握手。
//  2. 客户端在 HTTP 端口上也无法使用 TLS（CF 边缘不支持），所以这类端口上的
//     节点只能是明文；VLESS 脱离 TLS 没有任何加密，绝不能用于明文端口。
//  3. 因此「优选域名只挂加密端口」不是偏好，而是唯一正确的做法：
//     只有 WS + TLS 且监听在 CF HTTPS 端口上的入站，才会被挂上优选域名。
//
// # 为什么必须逐个校验优选域名
//
// 网上流传的「优选域名」列表过期极其严重。实测抽样：
//
//	shopify.com               → 23.227.38.33    已迁出 Cloudflare
//	acjp2.cloudflarest.link   → 104.247.81.99   不是 Cloudflare 的 IP
//	bestcf.onecf.eu.org       → 解析失败
//
// 名字里带 "cloudflare" 却解析到非 CF 地址的域名，如果被内置进订阅，客户端会把
// TLS 握手发给一台**不属于 Cloudflare 的服务器**。虽然证书校验会让连接失败
// （allowInsecure=false 时不会明文泄露），但这类域名绝不能内置。
//
// 因此这里在生成前用 Cloudflare 官方 IP 段逐个做 DNS 校验，只保留**当前解析
// 结果确实落在 CF 段内**的域名。域名失效或被回收时会自动被剔除。
package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	// preferredGroupRemark 既是订阅里显示的组名，也是本功能做幂等清理的标识：
	// 每次同步都先删掉同名的旧组再重建，所以重复启用不会堆积重复节点。
	preferredGroupRemark = "CF优选域名"

	// 校验优选域名时的并发度与总体时间预算。列表不长（几十个），
	// 8 并发足以在 1~3 秒内跑完；预算用尽则只用已解析出的部分，
	// 并且**不会**因此让整个 Enable 失败。
	preferredResolveWorkers = 8
	preferredResolveBudget  = 12 * time.Second

	// 单次最多内置多少个优选域名，避免订阅被撑爆。
	preferredMaxDomains = 64
)

// cfHTTPSPorts：Cloudflare 代理支持的 HTTPS 端口。只有这些端口上
// 客户端→CF 与 CF→源站两段都是 TLS，优选节点才是全程加密的。
var cfHTTPSPorts = map[int]bool{
	443: true, 2053: true, 2083: true, 2087: true, 2096: true, 8443: true,
}

// cfHTTPOnlyPorts：Cloudflare 代理支持的纯 HTTP 端口。
// CF 在这类端口上不接受 TLS，回源也是明文——所以源站必须用明文，
// 且协议必须是自带加密的（VMess / SS2022），不能用 VLESS。
var cfHTTPOnlyPorts = map[int]bool{
	80: true, 8080: true, 8880: true, 2052: true, 2082: true, 2086: true, 2095: true,
}

// IsCFHTTPSPort 报告端口是否为 Cloudflare 的 HTTPS 端口。
func IsCFHTTPSPort(port int) bool { return cfHTTPSPorts[port] }

// IsCFHTTPOnlyPort 报告端口是否为 Cloudflare 的纯 HTTP 端口。
func IsCFHTTPOnlyPort(port int) bool { return cfHTTPOnlyPorts[port] }

// cfIPv4Ranges 是 Cloudflare 官方公布的 IPv4 段（https://www.cloudflare.com/ips-v4）。
// 用于判断一个优选域名当前是否真的指向 Cloudflare。
var cfIPv4Ranges = []string{
	"173.245.48.0/20",
	"103.21.244.0/22",
	"103.22.200.0/22",
	"103.31.4.0/22",
	"141.101.64.0/18",
	"108.162.192.0/18",
	"190.93.240.0/20",
	"188.114.96.0/20",
	"197.234.240.0/22",
	"198.41.128.0/17",
	"162.158.0.0/15",
	"104.16.0.0/13",
	"104.24.0.0/14",
	"172.64.0.0/13",
	"131.0.72.0/22",
}

var cfIPv4Nets = func() []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cfIPv4Ranges))
	for _, c := range cfIPv4Ranges {
		if _, n, err := net.ParseCIDR(c); err == nil {
			out = append(out, n)
		}
	}
	return out
}()

// IsCloudflareIPv4 报告 IPv4 是否落在 Cloudflare 官方网段内。
func IsCloudflareIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	for _, n := range cfIPv4Nets {
		if n.Contains(v4) {
			return true
		}
	}
	return false
}

// PreferredDomainCandidates 是内置候选列表。
//
// 这份列表**故意包含已知失效项**（shopify.com、acjp2.cloudflarest.link 等）：
// 它们会在运行时校验阶段被剔除并给出原因，这既能让操作者看到「为什么某个域名
// 没被采用」，也保证列表本身不需要随域名兴衰频繁改动。
//
// 导出为变量是为了让测试可以替换成固定列表。
var PreferredDomainCandidates = []string{
	// 常用官方域名（解析到 CF 段内才保留）
	"time.is",
	"icook.hk",
	"icook.tw",
	"ip.sb",
	"japan.com",
	"malaysia.com",
	"russia.com",
	"singapore.com",
	"skk.moe",
	"shopify.com", // 已迁出 CF，预期被剔除
	"www.visa.com.sg",
	"www.visa.com.hk",
	"www.visa.com.tw",
	"www.visa.co.jp",
	"www.visakorea.com",
	"www.gco.gov.qa",
	"www.gov.se",
	"www.gov.ua",
	// 三方维护的优选域名
	"1.cf.cname.vvhan.com",
	"1.cf.959923.xyz",
	"bestcf.onecf.eu.org",
	"cf.zhetengsha.eu.org",
	"acjp2.cloudflarest.link", // 解析到非 CF 地址，预期被剔除
	"achk.cloudflarest.link",  // 同上
	"xn--b6gac.eu.org",
	"yx.887141.xyz",
	"8.889288.xyz",
	"cfip.1323123.xyz",
	"cf.515188.xyz",
	"cf-st.annoy.eu.org",
	"cf.0sm.com",
	"cf.877771.xyz",
	"cf.345673.xyz",
	"cfip.xxxxxxxx.tk",
}

// lookupIPv4 是解析候选域名的注入点。默认走系统解析器只查 A 记录
// （DNS 写的是 A 记录，IPv6 对优选没有意义）。测试通过替换它来脱离网络。
var lookupIPv4 = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip4", host)
}

// RejectedDomain 记录一个被剔除的候选域名及原因。
type RejectedDomain struct {
	Domain string `json:"domain"`
	Reason string `json:"reason"`
}

// ResolvePreferredDomains 并发解析候选域名，只保留当前解析结果落在
// Cloudflare 官方网段内的那些。ctx 用尽时返回已完成的部分（best effort）。
//
// 返回值 domains 已去重并保持候选列表的原始顺序，便于输出稳定、可对比。
func ResolvePreferredDomains(ctx context.Context, candidates []string) (domains []string, rejected []RejectedDomain) {
	// 去重但保留顺序
	seen := make(map[string]bool, len(candidates))
	uniq := make([]string, 0, len(candidates))
	for _, d := range candidates {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		uniq = append(uniq, d)
	}
	if len(uniq) == 0 {
		return nil, nil
	}

	type result struct {
		domain string
		ips    []string
		err    error
	}

	resolveCtx, cancel := context.WithTimeout(ctx, preferredResolveBudget)
	defer cancel()

	jobs := make(chan string)
	results := make(chan result, len(uniq))
	var wg sync.WaitGroup

	workers := preferredResolveWorkers
	if workers > len(uniq) {
		workers = len(uniq)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for d := range jobs {
				addrs, err := lookupIPv4(resolveCtx, d)
				if err != nil {
					results <- result{domain: d, err: err}
					continue
				}
				ips := make([]string, 0, len(addrs))
				for _, a := range addrs {
					ips = append(ips, a.String())
				}
				results <- result{domain: d, ips: ips}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, d := range uniq {
			select {
			case jobs <- d:
			case <-resolveCtx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	byName := make(map[string]result, len(uniq))
	for r := range results {
		byName[r.domain] = r
	}

	for _, d := range uniq {
		r, ok := byName[d]
		switch {
		case !ok:
			rejected = append(rejected, RejectedDomain{d, "解析超时"})
		case r.err != nil:
			rejected = append(rejected, RejectedDomain{d, "解析失败"})
		default:
			hit := ""
			for _, ip := range r.ips {
				if IsCloudflareIPv4(net.ParseIP(ip)) {
					hit = ip
					break
				}
			}
			if hit == "" {
				first := "无 A 记录"
				if len(r.ips) > 0 {
					first = r.ips[0]
				}
				rejected = append(rejected, RejectedDomain{d, "非 Cloudflare 地址（" + first + "）"})
				continue
			}
			domains = append(domains, d)
			if len(domains) >= preferredMaxDomains {
				break
			}
		}
	}
	return domains, rejected
}

// parseStream 解析入站的 stream_settings JSON。空串视为空对象。
func parseStream(raw string) (map[string]any, error) {
	m := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// asString 把 JSON 里的值安全地当成字符串取用：非字符串一律视为空。
func asString(v any) string {
	s, _ := v.(string)
	return s
}

// PreferredInbound 描述一个可以挂优选域名的入站。
type PreferredInbound struct {
	ID       int
	Port     int
	Protocol string
	Remark   string
	Path     string
}

// EligiblePreferredInbounds 返回**全程加密**的入站：WS 传输 + 源站 TLS +
// 监听在 Cloudflare 的 HTTPS 端口上。
//
// 三个条件缺一不可：
//   - 不是 WS 的（例如裸 TCP/TLS），Cloudflare 无法代理（CF 只转发 HTTP/WS）；
//   - 源站不是 TLS 的，客户端在 HTTPS 端口上用 TLS 会与明文源站握手失败；
//   - 端口不在 CF HTTPS 列表里的，客户端根本没法用 TLS 连上 CF。
func EligiblePreferredInbounds() ([]PreferredInbound, error) {
	var inbounds []*model.Inbound
	if err := database.GetDB().Find(&inbounds).Error; err != nil {
		return nil, err
	}
	out := make([]PreferredInbound, 0, len(inbounds))
	for _, ib := range inbounds {
		if ib == nil || !ib.Enable {
			continue
		}
		if !IsCFHTTPSPort(ib.Port) {
			continue
		}
		stream, err := parseStream(ib.StreamSettings)
		if err != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(asString(stream["network"]))) != "ws" {
			continue
		}
		if strings.ToLower(strings.TrimSpace(asString(stream["security"]))) != "tls" {
			continue
		}
		path := ""
		if ws, ok := stream["wsSettings"].(map[string]any); ok {
			path = asString(ws["path"])
		}
		out = append(out, PreferredInbound{
			ID: ib.Id, Port: ib.Port, Protocol: string(ib.Protocol), Remark: ib.Remark, Path: path,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}

// preferredGroupIDs 找出所有备注为 preferredGroupRemark 的优选组。
func preferredGroupIDs() ([]string, error) {
	var ids []string
	err := database.GetDB().Model(&model.Host{}).
		Where("remark = ?", preferredGroupRemark).
		Distinct().Pluck("group_id", &ids).Error
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// RemovePreferredHosts 删除本功能生成的全部优选组（幂等）。
func RemovePreferredHosts() error {
	groups, err := preferredGroupIDs()
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}
	return (&service.HostService{}).DeleteHostsGroup(groups)
}

// SyncPreferredHosts 按当前入站状态重建优选组，返回生成的域名、被剔除的域名
// 以及提示信息。
//
// 幂等：先删除同名旧组再重建，因此重复调用不会产生重复节点，也会自动跟随
// 入站的变化（比如后来新增了一个 2083 端口上的 WS+TLS 入站）。
//
// 失败不会影响域名功能本身：调用方把 err 当作警告处理即可。
func SyncPreferredHosts(ctx context.Context, fqdn string, candidates []string) (domains []string, rejected []RejectedDomain, warnings []string, err error) {
	inbounds, err := EligiblePreferredInbounds()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("读取入站失败：%w", err)
	}
	if len(inbounds) == 0 {
		return nil, nil, []string{
			"没有可用于优选域名的入站：需要「WS 传输 + 源站 TLS + 监听 CF HTTPS 端口(443/2053/2083/2087/2096/8443)」三者同时满足",
		}, nil
	}

	domains, rejected = ResolvePreferredDomains(ctx, candidates)
	if len(domains) == 0 {
		return nil, rejected, []string{"候选优选域名全部未通过 Cloudflare 网段校验，已跳过优选生成"}, nil
	}

	ids := make([]int, 0, len(inbounds))
	for _, ib := range inbounds {
		ids = append(ids, ib.ID)
	}

	if err := RemovePreferredHosts(); err != nil {
		return nil, rejected, nil, fmt.Errorf("清理旧优选组失败：%w", err)
	}

	// Port=0 / Path="" / Security="same" 表示**逐行继承各自入站**的端口、路径
	// 与安全模式。这样同一个组就能同时正确覆盖端口和路径都不同的多个入站，
	// 而且订阅里的客户端链接会自动带上正确的 tls/明文模式与 ws 路径。
	group := &entity.HostGroup{
		Remark:     preferredGroupRemark,
		InboundIds: ids,
		Hosts:      domains,
		Port:       0,
		Path:       "",
		Security:   "same",
		Sni:        fqdn,
		HostHeader: fqdn,
		SortOrder:  0,
		IsDisabled: false,
		IsHidden:   false,
	}
	if _, err := (&service.HostService{}).AddHostGroup(group); err != nil {
		return nil, rejected, nil, fmt.Errorf("写入优选组失败：%w", err)
	}

	ports := make([]string, 0, len(inbounds))
	for _, ib := range inbounds {
		ports = append(ports, fmt.Sprintf("%d", ib.Port))
	}
	warnings = append(warnings, fmt.Sprintf(
		"优选域名已生成：%d 个域名 × %d 个加密入站（端口 %s）",
		len(domains), len(inbounds), strings.Join(ports, "/")))

	return domains, rejected, warnings, nil
}

// PreferredStatus 供面板/CLI 查询当前优选组状态（不触网）。
func PreferredStatus() (domains []string, inboundIDs []int, err error) {
	groups, err := preferredGroupIDs()
	if err != nil || len(groups) == 0 {
		return nil, nil, err
	}
	var hosts []*model.Host
	if err := database.GetDB().Where("group_id IN ?", groups).Find(&hosts).Error; err != nil {
		return nil, nil, err
	}
	seenDomain := make(map[string]bool)
	seenInbound := make(map[int]bool)
	for _, h := range hosts {
		if h.Address != "" && !seenDomain[h.Address] {
			seenDomain[h.Address] = true
			domains = append(domains, h.Address)
		}
		if !seenInbound[h.InboundId] {
			seenInbound[h.InboundId] = true
			inboundIDs = append(inboundIDs, h.InboundId)
		}
	}
	sort.Strings(domains)
	sort.Ints(inboundIDs)
	return domains, inboundIDs, nil
}
