package sub

import (
	"net"
	"net/netip"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

var warnSuppressedForwardedOnce sync.Once

// forwardedHeaderNames lists the headers that only mean something when the
// request's peer is allowed to speak for the client. X-Forwarded-For is on the
// list because it names the client a CDN saw, and it is the header the card-key
// rate limiter keys on: seeing it is what makes the trustedProxyCIDRs boundary
// apply.
var forwardedHeaderNames = [...]string{"X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-IP", "X-Forwarded-For"}

// clientIPHeaders are the headers that carry the client's own address.
var clientIPHeaders = [...]string{"X-Forwarded-For", "X-Real-IP"}

func (s *SubService) forwardedHeadersTrusted(c *gin.Context) (trusted bool) {
	if !hasForwardedHeaders(c) {
		return true
	}
	_, trusted = s.forwardedTrust(c)
	return trusted
}

// forwardedTrust returns the configured proxy boundary together with the
// verdict for this request: forwarded identity headers are believed when no
// boundary is configured (the shipped default) or when the peer sits inside
// trustedProxyCIDRs. An empty boundary means "no boundary declared" — the
// shipped default, which keeps trusting forwarded headers, exactly as
// forwardedHeadersTrusted always has.
func (s *SubService) forwardedTrust(c *gin.Context) (boundary string, trusted bool) {
	trusted = true
	defer func() {
		_ = recover()
	}()

	configured, err := s.settingService.GetTrustedProxyCIDRs()
	if err != nil {
		return "", true
	}
	configured = strings.TrimSpace(configured)
	if configured == "" || configured == service.DefaultTrustedProxyCIDRs {
		return "", true
	}
	return configured, remoteAddrInCIDRs(c.Request.RemoteAddr, configured)
}

// clientIPForLimit resolves the source address a subscription request should be
// attributed to (the card-key rate limiter's per-caller key), reusing the
// trusted-proxy verdict above rather than inventing a second trust model:
//
//   - a peer that is not allowed to speak for the client (a direct connection
//     to the origin while trustedProxyCIDRs declares a boundary) is keyed by
//     its own socket address, so it cannot mint identities by sending headers;
//   - otherwise the address comes from the forwarded chain, read from the right
//     — the hop closest to us that is not one of our own proxies, which is what
//     a CDN that appends the visitor address to X-Forwarded-For produces.
//
// The result is always a parsed IP or the literal "unknown", never a
// caller-supplied string, so a forged header cannot inflate the limiter's keys.
func (s *SubService) clientIPForLimit(c *gin.Context) string {
	peer := ipFromHeader(c.Request.RemoteAddr)
	if peer == "" {
		peer = "unknown"
	}
	if !hasClientIPHeader(c) {
		return peer
	}

	boundary, trusted := s.forwardedTrust(c)
	if !trusted {
		return peer
	}
	if ip := forwardedHopIP(c.GetHeader("X-Forwarded-For"), boundary); ip != "" {
		return ip
	}
	if ip := ipFromHeader(c.GetHeader("X-Real-IP")); ip != "" {
		return ip
	}
	return peer
}

func hasClientIPHeader(c *gin.Context) bool {
	for _, name := range clientIPHeaders {
		if c.GetHeader(name) != "" {
			return true
		}
	}
	return false
}

// forwardedHopIP picks the client address out of an X-Forwarded-For value.
//
// With a declared boundary the chain is walked from the right and the first hop
// that is not one of our own proxies wins — the closest hop that is not
// infrastructure. Without a boundary the chain is walked the same way, skipping
// only local addresses (loopback/link-local/unspecified/private), because a
// proxy that appends itself leaves an unusable hop at the end. Anything that
// does not parse as an IP is skipped, and the chain is read at most 32 hops
// deep.
//
// The two modes differ in what happens when no hop qualifies. With a boundary,
// every hop was ours: the client is simply not in the header, so this returns
// "" and the caller keys on the peer (a configured proxy) rather than on an
// address the caller could have chosen. Without a boundary the rightmost hop is
// the address the outermost proxy appended, which is still the best available
// answer even when it looks local.
func forwardedHopIP(header, boundary string) string {
	header = strings.TrimSpace(header)
	if header == "" {
		return ""
	}
	hops := strings.Split(header, ",")
	if len(hops) > 32 {
		hops = hops[len(hops)-32:]
	}

	lastResort := ""
	for i := len(hops) - 1; i >= 0; i-- {
		ip := ipFromHeader(hops[i])
		if ip == "" {
			continue
		}
		if boundary != "" {
			if remoteAddrInCIDRs(ip, boundary) || isLoopbackAddress(ip) {
				continue
			}
			return ip
		}
		if lastResort == "" {
			lastResort = ip
		}
		if isLocalAddress(ip) {
			continue
		}
		return ip
	}
	return lastResort
}

// ipFromHeader parses a header value that may be a bare address, a host:port
// pair, or a bracketed IPv6 host:port. It returns the canonical address form so
// that the same client always produces the same limiter key.
func ipFromHeader(value string) string {
	candidate := strings.TrimSpace(value)
	if candidate == "" {
		return ""
	}
	if addr, err := netip.ParseAddr(candidate); err == nil {
		return addr.Unmap().String()
	}
	if host, _, err := net.SplitHostPort(candidate); err == nil {
		if addr, err := netip.ParseAddr(strings.TrimSpace(host)); err == nil {
			return addr.Unmap().String()
		}
	}
	return ""
}

func isLocalAddress(ip string) bool {
	if isLoopbackAddress(ip) {
		return true
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLinkLocalUnicast() || addr.IsPrivate()
}

// isLoopbackAddress reports whether a hop is this host: a loopback or
// unspecified hop is never a client, whatever boundary is configured.
func isLoopbackAddress(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsUnspecified()
}

func hasForwardedHeaders(c *gin.Context) bool {
	for _, name := range forwardedHeaderNames {
		if c.GetHeader(name) != "" {
			return true
		}
	}
	return false
}

func warnSuppressedForwardedHeaders(c *gin.Context) {
	present := make([]string, 0, 3)
	for _, name := range forwardedHeaderNames {
		if c.GetHeader(name) != "" {
			present = append(present, name)
		}
	}
	if len(present) == 0 {
		return
	}
	headers := strings.Join(present, ", ")
	logger.Debugf("sub: ignoring %s from %s, which is outside trustedProxyCIDRs", headers, c.Request.RemoteAddr)
	warnSuppressedForwardedOnce.Do(func() {
		logger.Warningf("sub: ignoring %s from %s because it is outside trustedProxyCIDRs; subscription URLs will use the request host. Add the proxy to that setting, or set subURI, if the generated links look wrong.", headers, c.Request.RemoteAddr)
	})
}

func remoteAddrInCIDRs(remoteAddr, cidrs string) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for value := range strings.SplitSeq(cidrs, ",") {
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
