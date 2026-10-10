package controller

import (
	"fmt"
	"strconv"
	"testing"
	"time"
)

// TestLoginLimiterThresholdsMatchDocumentedPolicy guards the numbers written in
// the code comments: per (IP, username) 5/5min -> 15min, per IP aggregate
// 20/5min -> 15min. Loosening them by accident should fail loudly.
func TestLoginLimiterThresholdsMatchDocumentedPolicy(t *testing.T) {
	if loginLimitMaxFailures != 5 {
		t.Errorf("loginLimitMaxFailures = %d, want 5", loginLimitMaxFailures)
	}
	if loginLimitWindow != 5*time.Minute || loginLimitCooldown != 15*time.Minute {
		t.Errorf("per-account bucket window/cooldown = %s/%s, want 5m/15m", loginLimitWindow, loginLimitCooldown)
	}
	if loginIPLimitMaxFailures != 20 {
		t.Errorf("loginIPLimitMaxFailures = %d, want 20", loginIPLimitMaxFailures)
	}
	if loginIPLimitWindow != 5*time.Minute || loginIPLimitCooldown != 15*time.Minute {
		t.Errorf("per-IP bucket window/cooldown = %s/%s, want 5m/15m", loginIPLimitWindow, loginIPLimitCooldown)
	}
	if defaultLoginLimiter.ipMaxFailures != loginIPLimitMaxFailures {
		t.Errorf("production limiter aggregate threshold = %d, want %d", defaultLoginLimiter.ipMaxFailures, loginIPLimitMaxFailures)
	}
	if defaultLoginLimiter.ipAttempts == nil {
		t.Error("production limiter has no aggregate bucket map")
	}
}

// TestLoginLimiterBlocksIPAfterAggregateFailuresAcrossUsernames is the core new
// behaviour: rotating the username must not give an attacker an unlimited number
// of tries from one source address.
func TestLoginLimiterBlocksIPAfterAggregateFailuresAcrossUsernames(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)
	limiter.now = func() time.Time { return now }

	const attacker = "203.0.113.66"
	username := func(i int) string { return "victim-" + strconv.Itoa(i) }

	for i := 1; i < loginIPLimitMaxFailures; i++ {
		if _, blocked := limiter.registerFailure(attacker, username(i)); blocked {
			t.Fatalf("failure %d should not block the IP yet", i)
		}
		if _, ok := limiter.allow(attacker, username(i+1)); !ok {
			t.Fatalf("failure %d should still allow login attempts", i)
		}
	}

	blockedUntil, blocked := limiter.registerFailure(attacker, username(loginIPLimitMaxFailures))
	if !blocked {
		t.Fatal("the aggregate per-IP threshold should block the source address")
	}
	if want := now.Add(loginIPLimitCooldown); !blockedUntil.Equal(want) {
		t.Fatalf("blocked until %s, want %s", blockedUntil, want)
	}

	// Every username — including a never-seen one and the empty string — is now
	// denied, because the block belongs to the IP, not to a username.
	for _, name := range []string{"3344", "lsprobe-nosuchuser", "", username(loginIPLimitMaxFailures + 1)} {
		if _, ok := limiter.allow(attacker, name); ok {
			t.Fatalf("blocked IP must stay blocked for username %q", name)
		}
	}

	// A different source address is unaffected.
	if _, ok := limiter.allow("203.0.113.67", "3344"); !ok {
		t.Fatal("another IP must not be blocked by this IP's failures")
	}

	now = blockedUntil
	if _, ok := limiter.allow(attacker, "3344"); !ok {
		t.Fatal("login should be allowed again once the aggregate cooldown expires")
	}
}

// TestLoginLimiterAggregateBlockIsIdenticalForExistingAndMissingUsernames pins
// the anti-oracle property: the limiter never consults the user table, so a
// username that exists in the panel and one that does not are indistinguishable
// in when they are blocked, for how long, and what allow() answers afterwards.
func TestLoginLimiterAggregateBlockIsIdenticalForExistingAndMissingUsernames(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

	run := func(names func(i int) string) (int, time.Time) {
		limiter := newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)
		limiter.now = func() time.Time { return now }
		for i := 1; i <= loginIPLimitMaxFailures; i++ {
			if blockedUntil, blocked := limiter.registerFailure("198.51.100.20", names(i)); blocked {
				return i, blockedUntil
			}
		}
		return 0, time.Time{}
	}

	// "3344" is the panel's real administrator name; the other one cannot exist.
	existingAt, existingUntil := run(func(i int) string { return "3344-" + strconv.Itoa(i) })
	missingAt, missingUntil := run(func(i int) string { return "lsprobe-nosuchuser-" + strconv.Itoa(i) })

	if existingAt != loginIPLimitMaxFailures || missingAt != loginIPLimitMaxFailures {
		t.Fatalf("aggregate block happened after %d existing / %d missing-username failures, want both %d",
			existingAt, missingAt, loginIPLimitMaxFailures)
	}
	if existingAt != missingAt || !existingUntil.Equal(missingUntil) {
		t.Fatalf("block is distinguishable: existing=(%d,%s) missing=(%d,%s)",
			existingAt, existingUntil, missingAt, missingUntil)
	}
}

// TestLoginLimiterCountsFailuresForEveryUsername shows that a failure is always
// recorded, whatever the username looks like — including one that matches the
// real admin account and one that cannot exist.
func TestLoginLimiterCountsFailuresForEveryUsername(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)
	limiter.now = func() time.Time { return now }

	const ip = "192.0.2.77"
	probeName := func(name string) string { return fmt.Sprintf("probe-%s-%d", name, len(name)) }

	for i := range loginIPLimitMaxFailures - 1 {
		limiter.registerFailure(ip, probeName("lsprobe-nosuchuser"+strconv.Itoa(i)))
	}
	if _, blocked := limiter.registerFailure(ip, probeName("3344")); !blocked {
		t.Fatal("a failure for the existing admin username must count towards the same IP bucket")
	}
}

// TestLoginLimiterPerAccountBucketStillBlocksTargetedBruteForce keeps the old
// guarantee intact: five failures against one username block that (IP, username)
// pair without needing the aggregate bucket to fill up.
func TestLoginLimiterPerAccountBucketStillBlocksTargetedBruteForce(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)
	limiter.now = func() time.Time { return now }

	const ip = "192.0.2.90"
	for i := 1; i < loginLimitMaxFailures; i++ {
		if _, blocked := limiter.registerFailure(ip, "3344"); blocked {
			t.Fatalf("failure %d should not block yet", i)
		}
	}
	blockedUntil, blocked := limiter.registerFailure(ip, "3344")
	if !blocked {
		t.Fatal("the per-account threshold should block the (IP, username) pair")
	}
	if want := now.Add(loginLimitCooldown); !blockedUntil.Equal(want) {
		t.Fatalf("blocked until %s, want %s", blockedUntil, want)
	}
	if _, ok := limiter.allow(ip, "3344"); ok {
		t.Fatal("the targeted username must stay blocked")
	}
	// The aggregate bucket has only 5 of its 20 failures, so other usernames are
	// still allowed from this IP (single-account lockout only).
	if _, ok := limiter.allow(ip, "lsprobe-nosuchuser"); !ok {
		t.Fatal("only the targeted (IP, username) pair should be blocked")
	}
}

func TestLoginLimiterSuccessClearsAggregateIPFailures(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	limiter := newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)
	limiter.now = func() time.Time { return now }

	const ip = "192.0.2.100"
	for i := 1; i < loginIPLimitMaxFailures; i++ {
		limiter.registerFailure(ip, "noise-"+strconv.Itoa(i))
	}

	limiter.registerSuccess(ip, "3344")

	// After a successful login the IP starts from a clean slate, so a shared
	// egress is not punished for a neighbour's earlier failures.
	for i := 1; i < loginIPLimitMaxFailures; i++ {
		if _, blocked := limiter.registerFailure(ip, "after-"+strconv.Itoa(i)); blocked {
			t.Fatalf("failure %d after a successful login should not block the IP", i)
		}
	}
	if _, blocked := limiter.registerFailure(ip, "after-final"); !blocked {
		t.Fatal("the aggregate threshold should still apply after the reset")
	}
}

// TestLoginLimiterBoundsAggregateBucketMemory mirrors the username-flood ceiling
// test for the new IP-keyed map: rotating source addresses must not grow it past
// the cap either.
func TestLoginLimiterBoundsAggregateBucketMemory(t *testing.T) {
	limiter := newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)
	for i := range loginLimitMaxRecords + 100 {
		limiter.registerFailure(fmt.Sprintf("198.18.%d.%d", (i/250)%250, i%250), "admin")
	}

	limiter.mu.Lock()
	n := len(limiter.ipAttempts)
	limiter.mu.Unlock()

	if n > loginLimitMaxRecords {
		t.Fatalf("aggregate bucket grew to %d, exceeding the %d ceiling under an IP flood", n, loginLimitMaxRecords)
	}
}