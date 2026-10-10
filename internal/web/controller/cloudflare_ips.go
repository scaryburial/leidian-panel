package controller

import (
	"net/netip"
	"strings"
)

// Cloudflare 官方出口（回源）网段。
//
// 来源：https://www.cloudflare.com/ips-v4 与 https://www.cloudflare.com/ips-v6
// 取数时间：2026-10-05（UTC，通过公网抓取核对，与官方列表逐条一致）。
//
// 为什么要内置这份表：面板通过 Cloudflare 暴露（CF 回源到源站 8443）时，
// c.Request.RemoteAddr 是 **CF 边缘节点** 的地址，不是真实客户端地址。
// 如果只信任 settings.trustedProxyCIDRs（默认仅 127.0.0.1/32,::1/128），
// 面板就会把 CF 边缘 IP 当成客户端 IP 记录下来并用于登录限速：
//   ① 攻击者换一个 CF PoP 就换了一个限速桶，限速形同虚设；
//   ② 与管理员落在同一 PoP 的攻击者，可以用管理员的用户名把管理员锁死。
// 因此把 CF 回源网段作为 **独立的一类信任来源**（而不是放宽
// trustedProxyCIDRs 的默认值）：只有对端确实落在 CF 网段内时，才采信
// CF 写入的 CF-Connecting-IP。
//
// 维护提醒：CF 会新增网段（历史上多次扩容），这份常量表需要人工定期比对
// 上面的两个官方地址；面板不联网，不能自动刷新。
const (
	cloudflareIPv4Ranges = "173.245.48.0/20," +
		"103.21.244.0/22," +
		"103.22.200.0/22," +
		"103.31.4.0/22," +
		"141.101.64.0/18," +
		"108.162.192.0/18," +
		"190.93.240.0/20," +
		"188.114.96.0/20," +
		"197.234.240.0/22," +
		"198.41.128.0/17," +
		"162.158.0.0/15," +
		"104.16.0.0/13," +
		"104.24.0.0/14," +
		"172.64.0.0/13," +
		"131.0.72.0/22"

	cloudflareIPv6Ranges = "2400:cb00::/32," +
		"2606:4700::/32," +
		"2803:f800::/32," +
		"2405:b500::/32," +
		"2405:8100::/32," +
		"2a06:98c0::/29," +
		"2c0f:f248::/32"
)

// cfConnectingIPHeader 是 Cloudflare 回源时写入真实客户端 IP 的头部。与
// X-Forwarded-For 不同，它由 CF 边缘覆盖写入，客户端无法伪造（也不是列表）。
const cfConnectingIPHeader = "CF-Connecting-IP"

// cloudflarePrefixes 在包初始化时解析一次，之后只读。
var cloudflarePrefixes = parseIPPrefixList(cloudflareIPv4Ranges + "," + cloudflareIPv6Ranges)

func parseIPPrefixList(list string) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0, 32)
	for value := range strings.SplitSeq(list, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	return prefixes
}

// isCloudflareIP reports whether ip falls inside a Cloudflare edge/origin-range
// prefix. Unparseable input is never treated as Cloudflare.
func isCloudflareIP(ip string) bool {
	candidate := strings.TrimSpace(ip)
	if candidate == "" {
		return false
	}
	addr, err := netip.ParseAddr(candidate)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, prefix := range cloudflarePrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}