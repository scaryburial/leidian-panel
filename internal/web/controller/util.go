package controller

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/entity"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"github.com/gin-gonic/gin"
)

// getRemoteIp extracts the real IP address from the request headers or remote address.
//
// 信任模型（顺序即优先级，改动前请先读完）：
//
//  1. 对端 socket 地址落在 Cloudflare 网段内 → 请求来自 CF 回源，采信
//     CF-Connecting-IP（由 CF 覆盖写入，客户端无法伪造）。它缺失或不是合法 IP 时，
//     退回 X-Forwarded-For 中「最靠近我们且不属于 CF 网段」的那一跳——CF 会把真实
//     客户端 IP 追加在 XFF 右侧，左侧可能是客户端自己伪造的内容，所以必须
//     从右往左找，绝不能取最左边那个。再不行才退回对端地址。
//
//  2. 对端落在 settings.trustedProxyCIDRs 内（未配置时默认只有回环）→ 沿用原有语义
//     采信 X-Real-IP / X-Forwarded-For，但会跳过 CF 边缘地址：源站前面如果还有一层
//     本地反代（nginx 等），它记录的“对端”同样是 CF 边缘，那不是真实客户端。
//
//  3. 其它情况（公网直连）→ 一律使用对端 socket 地址，不采信任何头部，防伪造。
//
// 注意：不要为了“支持 CF”而放宽 trustedProxyCIDRs 的默认值——那会变成信任一切，
// 让任何直连者都能用 X-Forwarded-For 伪造 IP 绕过登录限速。CF 网段是独立的一类
// 信任来源（见 cloudflare_ips.go）。
func getRemoteIp(c *gin.Context) string {
	remoteIP, ok := extractTrustedIP(c.Request.RemoteAddr)
	if !ok {
		return "unknown"
	}

	if isCloudflareIP(remoteIP) {
		if ip, ok := extractTrustedIP(c.GetHeader(cfConnectingIPHeader)); ok {
			return ip
		}
		if ip, ok := selectForwardedHop(c.GetHeader("X-Forwarded-For"), isCloudflareIP); ok {
			return ip
		}
		return remoteIP
	}

	if isTrustedProxy(remoteIP) {
		// CF 边缘地址出现在这里说明“真实客户端”还在更前面一跳，不能直接采用。
		if ip, ok := extractTrustedIP(c.GetHeader("X-Real-IP")); ok && !isCloudflareIP(ip) {
			return ip
		}

		if ip, ok := selectForwardedHop(c.GetHeader("X-Forwarded-For"), func(value string) bool {
			// 跳过 CF 边缘与已知信任代理：它们只是转发链上的中间节点。
			return isCloudflareIP(value) || isTrustedProxy(value)
		}); ok {
			return ip
		}
	}

	return remoteIP
}

// selectForwardedHop walks X-Forwarded-For from right to left (nearest hop first)
// and returns the first parseable hop that skip does not reject. Walking in this
// direction is what makes the result non-spoofable: an attacker can prepend
// arbitrary values to X-Forwarded-For, but cannot remove or reorder the hops
// appended by the proxies on the path (including Cloudflare).
func selectForwardedHop(xff string, skip func(string) bool) (string, bool) {
	if strings.TrimSpace(xff) == "" {
		return "", false
	}
	parts := strings.Split(xff, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip, ok := extractTrustedIP(parts[i])
		if !ok || skip(ip) {
			continue
		}
		return ip, true
	}
	return "", false
}

func isTrustedForwardedRequest(c *gin.Context) bool {
	remoteIP, ok := extractTrustedIP(c.Request.RemoteAddr)
	return ok && isTrustedProxy(remoteIP)
}

func isTrustedProxy(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}

	trusted := trustedProxyCIDRs()
	for value := range strings.SplitSeq(trusted, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(value); err == nil {
			if prefix.Contains(addr) {
				return true
			}
			continue
		}
		if proxyIP, err := netip.ParseAddr(value); err == nil && proxyIP.Unmap() == addr.Unmap() {
			return true
		}
	}
	return false
}

func trustedProxyCIDRs() (trusted string) {
	trusted = service.DefaultTrustedProxyCIDRs
	defer func() {
		_ = recover()
	}()
	settingService := service.SettingService{}
	if value, err := settingService.GetTrustedProxyCIDRs(); err == nil && strings.TrimSpace(value) != "" {
		trusted = value
	}
	return trusted
}

func extractTrustedIP(value string) (string, bool) {
	candidate := strings.TrimSpace(value)
	if candidate == "" {
		return "", false
	}

	if ip, ok := parseIPCandidate(candidate); ok {
		return ip.String(), true
	}

	if host, _, err := net.SplitHostPort(candidate); err == nil {
		if ip, ok := parseIPCandidate(host); ok {
			return ip.String(), true
		}
	}

	if strings.Count(candidate, ":") == 1 {
		if host, _, err := net.SplitHostPort(fmt.Sprintf("[%s]", candidate)); err == nil {
			if ip, ok := parseIPCandidate(host); ok {
				return ip.String(), true
			}
		}
	}

	return "", false
}

func parseIPCandidate(value string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(value))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// jsonMsg sends a JSON response with a message and error status.
func jsonMsg(c *gin.Context, msg string, err error) {
	jsonMsgObj(c, msg, nil, err)
}

// jsonObj sends a JSON response with an object and error status.
func jsonObj(c *gin.Context, obj any, err error) {
	jsonMsgObj(c, "", obj, err)
}

func requestErrorContext(c *gin.Context) string {
	handler, loc := callerOutsideUtil()
	return fmt.Sprintf("[%s %s handler=%s %s]", c.Request.Method, c.Request.URL.Path, handler, loc)
}

func callerOutsideUtil() (string, string) {
	var pcs [12]uintptr
	n := runtime.Callers(2, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		base := filepath.Base(frame.File)
		if base != "util.go" {
			name := frame.Function
			if idx := strings.LastIndex(name, "/"); idx >= 0 {
				name = name[idx+1:]
			}
			return name, fmt.Sprintf("%s:%d", base, frame.Line)
		}
		if !more {
			break
		}
	}
	return "unknown", "unknown"
}

// jsonMsgObj sends a JSON response with a message, object, and error status.
func jsonMsgObj(c *gin.Context, msg string, obj any, err error) {
	m := entity.Msg{
		Obj: obj,
	}
	if err == nil {
		m.Success = true
		if msg != "" {
			m.Msg = msg
		}
	} else {
		m.Success = false
		ctx := requestErrorContext(c)
		fail := I18nWeb(c, "fail")
		errStr := err.Error()
		if errStr != "" {
			m.Msg = msg + " (" + errStr + ")"
			logger.Warningf("%s %s %s: %v", ctx, msg, fail, err)
		} else if msg != "" {
			m.Msg = msg
			logger.Warningf("%s %s %s", ctx, msg, fail)
		} else {
			m.Msg = I18nWeb(c, "somethingWentWrong")
			logger.Warningf("%s %s %s", ctx, m.Msg, fail)
		}
	}
	c.JSON(http.StatusOK, m)
}

// pendingNodeObj returns a response object flagging that the save committed
// locally but a backing node was offline/disabled, so the change will be
// mirrored to the node once it reconnects. Returns nil when nothing is pending.
func pendingNodeObj(pending bool) any {
	if pending {
		return gin.H{"nodePending": true}
	}
	return nil
}

// pureJsonMsg sends a pure JSON message response with custom status code.
func pureJsonMsg(c *gin.Context, statusCode int, success bool, msg string) {
	c.JSON(statusCode, entity.Msg{
		Success: success,
		Msg:     msg,
	})
}

// isAjax checks if the request is an AJAX request.
func isAjax(c *gin.Context) bool {
	return c.GetHeader("X-Requested-With") == "XMLHttpRequest"
}
