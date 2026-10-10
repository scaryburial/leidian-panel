package controller

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// remoteIPContext builds a gin context whose peer is remoteAddr and whose
// headers are exactly the given ones — nothing else is set, so a stray trusted
// header can never leak in from the test process.
func remoteIPContext(remoteAddr string, headers map[string]string) *gin.Context {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/login", nil)
	c.Request.RemoteAddr = remoteAddr
	c.Request.Header = make(http.Header, len(headers))
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c
}

// TestCloudflareRangeTable pins the built-in Cloudflare prefix table (fetched
// from https://www.cloudflare.com/ips-v4 and /ips-v6 on 2026-10-05 UTC) against
// samples inside every published range plus look-alike addresses just outside
// the boundaries.
func TestCloudflareRangeTable(t *testing.T) {
	inside := []string{
		"173.245.48.1", "103.21.244.5", "103.22.200.9", "103.31.4.7",
		"141.101.64.3", "108.162.192.2", "190.93.240.6", "188.114.96.4",
		"197.234.240.8", "198.41.128.1", "162.158.179.156", "172.68.211.156",
		"104.16.0.1", "104.24.0.1", "131.0.72.1",
		"2400:cb00::1", "2606:4700:3034::ac43:a234", "2803:f800::1",
		"2405:b500::1", "2405:8100::1", "2a06:98c0::1", "2c0f:f248::1",
		"::ffff:162.158.1.1", // IPv4-mapped IPv6 must still match the v4 ranges
	}
	for _, ip := range inside {
		if !isCloudflareIP(ip) {
			t.Errorf("isCloudflareIP(%q) = false, want true (published Cloudflare range)", ip)
		}
	}

	outside := []string{
		"8.8.8.8", "154.12.82.50", "127.0.0.1", "203.0.113.10",
		"162.157.255.255", "162.160.0.1", // around 162.158.0.0/15
		"172.63.255.255", "172.72.0.1", // around 172.64.0.0/13
		"104.15.255.255", "104.28.0.1", // around 104.16.0.0/13 + 104.24.0.0/14
		"131.0.71.255", "131.0.76.1", // around 131.0.72.0/22
		"198.41.127.255", // just below 198.41.128.0/17
		"2606:46ff::1", "2400:cb01::1",
		"not-an-ip", "",
	}
	for _, ip := range outside {
		if isCloudflareIP(ip) {
			t.Errorf("isCloudflareIP(%q) = true, want false (outside every Cloudflare range)", ip)
		}
	}
}

func TestGetRemoteIpUsesCFConnectingIPFromCloudflarePeer(t *testing.T) {
	c := remoteIPContext("162.158.179.156:44321", map[string]string{
		cfConnectingIPHeader: "1.2.3.4",
		// Junk the attacker controls: it must lose against CF-Connecting-IP.
		"X-Forwarded-For": "9.9.9.9",
		"X-Real-IP":       "9.9.9.8",
	})

	if got := getRemoteIp(c); got != "1.2.3.4" {
		t.Fatalf("remote IP = %q, want the CF-Connecting-IP value 1.2.3.4", got)
	}
}

func TestGetRemoteIpFallsBackToRightmostNonCloudflareHop(t *testing.T) {
	// CF appends the real client to X-Forwarded-For; everything left of it can
	// be attacker-supplied, so the nearest usable hop has to win.
	c := remoteIPContext("172.68.211.156:5555", map[string]string{
		cfConnectingIPHeader: "not-an-ip",
		"X-Forwarded-For":    "9.9.9.9, 203.0.113.7, 162.158.179.156",
	})

	if got := getRemoteIp(c); got != "203.0.113.7" {
		t.Fatalf("remote IP = %q, want 203.0.113.7 (nearest non-Cloudflare hop)", got)
	}
}

func TestGetRemoteIpFallsBackToPeerWhenForwardedChainIsCloudflareOnly(t *testing.T) {
	c := remoteIPContext("104.16.0.5:1234", map[string]string{
		cfConnectingIPHeader: "   ",
		"X-Forwarded-For":    "162.158.1.1, 172.64.0.9",
	})

	if got := getRemoteIp(c); got != "104.16.0.5" {
		t.Fatalf("remote IP = %q, want the peer address 104.16.0.5", got)
	}
}

func TestGetRemoteIpRejectsForwardedHeadersFromUntrustedPeer(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{"public peer", "203.0.113.10:4444", "203.0.113.10"},
		{"just outside the Cloudflare ranges", "162.157.255.255:4444", "162.157.255.255"},
		{"look-alike CF address", "172.63.255.255:4444", "172.63.255.255"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := remoteIPContext(tc.remoteAddr, map[string]string{
				cfConnectingIPHeader: "1.2.3.4",
				"X-Forwarded-For":    "1.2.3.4",
				"X-Real-IP":          "1.2.3.4",
			})

			if got := getRemoteIp(c); got != tc.want {
				t.Fatalf("remote IP = %q, want the peer socket address %q (forged headers must be ignored)", got, tc.want)
			}
		})
	}
}

func TestGetRemoteIpHandlesIPv6CloudflarePeer(t *testing.T) {
	cases := []struct {
		name       string
		remoteAddr string
		header     string
		want       string
	}{
		{
			name:       "CF IPv6 peer with IPv6 client",
			remoteAddr: "[2606:4700:3034::ac43:a234]:44321",
			header:     "2001:db8::1",
			want:       "2001:db8::1",
		},
		{
			name:       "CF IPv6 peer with padded IPv4 client",
			remoteAddr: "[2400:cb00::1]:8443",
			header:     "  203.0.113.9  ",
			want:       "203.0.113.9",
		},
		{
			name:       "port suffix is tolerated",
			remoteAddr: "[2a06:98c0::1]:8443",
			header:     "1.2.3.4:5678",
			want:       "1.2.3.4",
		},
		{
			name:       "bracketed IPv6 with port is tolerated",
			remoteAddr: "[2c0f:f248::1]:8443",
			header:     "[2606:4700::1]:443",
			want:       "2606:4700::1",
		},
		{
			name:       "list-shaped value is not blindly split",
			remoteAddr: "162.158.1.1:8443",
			header:     "203.0.113.9, 198.51.100.1",
			want:       "162.158.1.1",
		},
		{
			name:       "mapped IPv4 client is unmapped",
			remoteAddr: "162.158.1.1:8443",
			header:     "::ffff:203.0.113.9",
			want:       "203.0.113.9",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := remoteIPContext(tc.remoteAddr, map[string]string{cfConnectingIPHeader: tc.header})
			if got := getRemoteIp(c); got != tc.want {
				t.Fatalf("remote IP = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestGetRemoteIpReportsUnknownWhenPeerIsUnparseable(t *testing.T) {
	c := remoteIPContext("not-an-address", map[string]string{cfConnectingIPHeader: "1.2.3.4"})
	if got := getRemoteIp(c); got != "unknown" {
		t.Fatalf("remote IP = %q, want %q", got, "unknown")
	}
}

func TestGetRemoteIpTrustedProxySkipsCloudflareEdges(t *testing.T) {
	// Origin behind a local reverse proxy which itself sits behind Cloudflare:
	// both X-Real-IP and the right-most X-Forwarded-For entry are CF edge IPs,
	// so the real client is the nearest non-Cloudflare, non-trusted hop.
	c := remoteIPContext("127.0.0.1:1234", map[string]string{
		"X-Real-IP":       "162.158.179.156",
		"X-Forwarded-For": "9.9.9.9, 203.0.113.7, 162.158.179.156",
	})

	if got := getRemoteIp(c); got != "203.0.113.7" {
		t.Fatalf("remote IP = %q, want 203.0.113.7 (real client behind the CF edge)", got)
	}
}

func TestGetRemoteIpTrustedProxyKeepsXRealIPWithoutCloudflareHops(t *testing.T) {
	c := remoteIPContext("127.0.0.1:1234", map[string]string{"X-Real-IP": "198.51.100.4"})
	if got := getRemoteIp(c); got != "198.51.100.4" {
		t.Fatalf("remote IP = %q, want the trusted proxy's X-Real-IP 198.51.100.4", got)
	}
}

func TestGetRemoteIpTrustedProxyKeepsLeftmostHopSemantics(t *testing.T) {
	// Pre-existing behaviour, unchanged by the Cloudflare work: with a trusted
	// loopback proxy the oldest hop in X-Forwarded-For is the original client.
	c := remoteIPContext("127.0.0.1:1234", map[string]string{
		"X-Forwarded-For": "198.51.100.8, 127.0.0.1",
	})
	if got := getRemoteIp(c); got != "198.51.100.8" {
		t.Fatalf("remote IP = %q, want 198.51.100.8", got)
	}
}