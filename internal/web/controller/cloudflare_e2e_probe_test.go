package controller

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// cfE2EProbe is one recorded Cloudflare round-trip: what the edge sent, what the
// production IP resolution made of it, and what the production login limiter
// answered for it.
type cfE2EProbe struct {
	Path         string `json:"path"`
	Peer         string `json:"peer_ip"`
	CFConnecting string `json:"cf_connecting_ip"`
	ForwardedFor string `json:"x_forwarded_for"`
	RealIP       string `json:"x_real_ip"`
	Resolved     string `json:"resolved_ip"`
	Username     string `json:"username"`
	Allowed      bool   `json:"allowed"`
	BlockedUntil string `json:"blocked_until"`
	Message      string `json:"message"`
	// Legacy* mirror the same limiter calls keyed by the peer socket address,
	// i.e. exactly what the panel did before this change. Running both over the
	// same real Cloudflare traffic shows the difference the fix makes.
	LegacyBlocked     bool   `json:"legacy_blocked"`
	LegacyBlockedFrom string `json:"legacy_blocked_until"`
}

// TestRealCloudflareForwardedIP runs a REAL Cloudflare round trip against the
// production code path instead of mocking it: it serves the gin handler on :443
// with the origin's own Cloudflare Origin CA certificate, so requests made to the
// public Cloudflare hostname reach this process exactly as they reach the panel
// (peer = CF edge, CF-Connecting-IP = the true client).
//
// Skipped unless LP_CF_E2E=1, because it needs root, a free :443 and the origin
// certificate. Intended invocation on the verification server:
//
//	LP_CF_E2E=1 LP_CF_E2E_REQUESTS=21 LP_CF_E2E_EXPECT_BLOCKED=1 \
//	LP_CF_E2E_CERT=/root/cert/49pwzx78.578272.xyz/fullchain.pem \
//	LP_CF_E2E_KEY=/root/cert/49pwzx78.578272.xyz/privkey.pem \
//	go test -run TestRealCloudflareForwardedIP -v -timeout 300s ./internal/web/controller/
//
// while, from another shell, the probes are issued through
// https://49pwzx78.578272.xyz/ (port 443 by default, no :8443).
func TestRealCloudflareForwardedIP(t *testing.T) {
	if os.Getenv("LP_CF_E2E") != "1" {
		t.Skip("real Cloudflare round-trip probe: set LP_CF_E2E=1 on the verification server to run it")
	}

	want := envInt(t, "LP_CF_E2E_REQUESTS", 1)
	certFile := os.Getenv("LP_CF_E2E_CERT")
	keyFile := os.Getenv("LP_CF_E2E_KEY")
	if certFile == "" || keyFile == "" {
		t.Fatal("LP_CF_E2E_CERT and LP_CF_E2E_KEY must point at the origin certificate/key")
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("loading the origin key pair: %v", err)
	}

	probes := make(chan cfE2EProbe, want+16)
	legacyLimiter := newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Any("/*any", func(c *gin.Context) {
		peer, _ := extractTrustedIP(c.Request.RemoteAddr)
		resolved := getRemoteIp(c)
		probe := cfE2EProbe{
			Path:         c.Request.URL.Path,
			Peer:         peer,
			CFConnecting: c.GetHeader(cfConnectingIPHeader),
			ForwardedFor: c.GetHeader("X-Forwarded-For"),
			RealIP:       c.GetHeader("X-Real-IP"),
			Resolved:     resolved,
			Username:     c.Query("user"),
		}

		switch {
		case probe.Username == "":
			probe.Allowed = true
			probe.Message = "ok"
		case c.Query("fail") == "1":
			blockedUntil, blocked := defaultLoginLimiter.registerFailure(resolved, probe.Username)
			probe.BlockedUntil = formatProbeDeadline(blockedUntil)
			probe.Allowed = false
			if blocked {
				probe.Message = "too many failed attempts"
				fmt.Printf("failed login: username=%q, IP=%q, peer=%q, reason=%q, blocked_until=%s\n",
					probe.Username, resolved, peer, "too many failed attempts", blockedUntil.Format(time.RFC3339))
			} else {
				probe.Message = "wrong username or password"
				fmt.Printf("failed login: username=%q, IP=%q, peer=%q, reason=%q\n",
					probe.Username, resolved, peer, "invalid credentials")
			}
		default:
			blockedUntil, ok := defaultLoginLimiter.allow(resolved, probe.Username)
			probe.Allowed = ok
			probe.BlockedUntil = formatProbeDeadline(blockedUntil)
			if ok {
				probe.Message = "allowed"
			} else {
				probe.Message = "too many failed attempts"
				fmt.Printf("failed login: username=%q, IP=%q, peer=%q, reason=%q, blocked_until=%s\n",
					probe.Username, resolved, peer, "too many failed attempts", blockedUntil.Format(time.RFC3339))
			}
		}

		// Same limiter code, keyed by the peer socket address instead of the
		// resolved client IP: that is precisely the pre-fix behaviour, run over
		// the same live Cloudflare requests for comparison.
		if probe.Username != "" {
			if c.Query("fail") == "1" {
				legacyUntil, legacyBlocked := legacyLimiter.registerFailure(probe.Peer, probe.Username)
				probe.LegacyBlocked = legacyBlocked
				probe.LegacyBlockedFrom = formatProbeDeadline(legacyUntil)
			} else {
				legacyUntil, legacyAllowed := legacyLimiter.allow(probe.Peer, probe.Username)
				probe.LegacyBlocked = !legacyAllowed
				probe.LegacyBlockedFrom = formatProbeDeadline(legacyUntil)
			}
		}

		c.JSON(http.StatusOK, gin.H{
			"resolved_ip": resolved,
			"peer_ip":     peer,
			"allowed":     probe.Allowed,
			"msg":         probe.Message,
		})
		select {
		case probes <- probe:
		default:
			t.Logf("probe channel full, dropping %s %s", probe.Peer, probe.Path)
		}
	})

	listener, err := net.Listen("tcp", ":443")
	if err != nil {
		t.Fatalf("cannot bind :443 (needed for Cloudflare to reach this probe): %v", err)
	}
	server := &http.Server{
		Handler:           engine,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if serveErr := server.ServeTLS(listener, "", ""); serveErr != nil && serveErr != http.ErrServerClosed {
			t.Logf("probe server stopped: %v", serveErr)
		}
	}()
	defer func() { _ = server.Close() }()
	t.Logf("LP-CF-E2E listening on :443 with %s, waiting for %d probe(s)", certFile, want)

	timeout := envDuration(t, "LP_CF_E2E_TIMEOUT", 240*time.Second)
	deadline := time.After(timeout)
	collected := make([]cfE2EProbe, 0, want)
	for len(collected) < want {
		select {
		case probe := <-probes:
			collected = append(collected, probe)
		case <-deadline:
			t.Fatalf("only %d/%d Cloudflare probes arrived within %s", len(collected), want, timeout)
		}
	}

	peers := map[string]struct{}{}
	resolved := map[string]struct{}{}
	legacyBlocks := 0
	for i, probe := range collected {
		peers[probe.Peer] = struct{}{}
		resolved[probe.Resolved] = struct{}{}
		if probe.LegacyBlocked {
			legacyBlocks++
		}
		t.Logf("probe %d: peer=%s cf_connecting_ip=%s xff=%q resolved=%s user=%q allowed=%v legacy_blocked=%v msg=%q",
			i+1, probe.Peer, probe.CFConnecting, probe.ForwardedFor, probe.Resolved, probe.Username, probe.Allowed, probe.LegacyBlocked, probe.Message)
	}
	fmt.Printf("LP-CF-E2E probes=%d distinct_peers=%d legacy_peer_keyed_blocks=%d distinct_resolved=%v\n",
		len(collected), len(peers), legacyBlocks, sortedKeys(resolved))

	for i, probe := range collected {
		if !isCloudflareIP(probe.Peer) {
			t.Errorf("probe %d: peer %q is not a Cloudflare edge address, so the probe did not come in through Cloudflare", i+1, probe.Peer)
		}
		if probe.CFConnecting == "" {
			t.Errorf("probe %d: Cloudflare did not send %s", i+1, cfConnectingIPHeader)
			continue
		}
		if probe.Resolved != probe.CFConnecting {
			t.Errorf("probe %d: resolved %q, want the %s value %q", i+1, probe.Resolved, cfConnectingIPHeader, probe.CFConnecting)
		}
		if isCloudflareIP(probe.Resolved) {
			t.Errorf("probe %d: resolved %q is still a Cloudflare edge address", i+1, probe.Resolved)
		}
	}
	if len(resolved) != 1 {
		t.Errorf("resolved %d distinct client IPs across the probes, want exactly 1: %v", len(resolved), sortedKeys(resolved))
	}
	if expect := os.Getenv("LP_CF_E2E_EXPECT_IP"); expect != "" {
		if _, ok := resolved[expect]; !ok {
			t.Errorf("resolved set %v does not contain the expected client IP %s", sortedKeys(resolved), expect)
		}
	}
	if os.Getenv("LP_CF_E2E_EXPECT_BLOCKED") == "1" {
		last := collected[len(collected)-1]
		if last.Allowed {
			t.Errorf("last probe (user=%q) was allowed, want it denied by the per-IP aggregate bucket", last.Username)
		}
		if last.BlockedUntil == "" {
			t.Errorf("last probe (user=%q) was denied without a blocked_until deadline", last.Username)
		}
	}
}

func formatProbeDeadline(until time.Time) string {
	if until.IsZero() {
		return ""
	}
	return until.Format(time.RFC3339)
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func envInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s=%q is not a positive integer", name, raw)
	}
	return value
}

func envDuration(t *testing.T, name string, fallback time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s=%q is not a positive duration", name, raw)
	}
	return value
}