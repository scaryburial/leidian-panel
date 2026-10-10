package sub

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// Addresses used as distinct callers in the limiter tests.
const (
	limitClientA = "203.0.113.9:51000"
	limitClientB = "198.51.100.7:52000"
)

// testClock is the limiter's clock in tests. It is read from several goroutines
// in the concurrency tests, so it is atomic rather than a plain field.
type testClock struct{ nanos atomic.Int64 }

func newTestClock(t *testing.T) *testClock {
	t.Helper()
	clock := &testClock{}
	clock.nanos.Store(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC).UnixNano())
	return clock
}

func (c *testClock) Now() time.Time { return time.Unix(0, c.nanos.Load()) }

func (c *testClock) Advance(d time.Duration) { c.nanos.Add(int64(d)) }

// limitConfig builds an enabled budget with explicit thresholds; zero values
// would be replaced by the shipped defaults inside the limiter.
func limitConfig(clock *testClock, successPerMin, successBurst, failPerMin, failBurst, maxEntries int) cardKeyRateLimitConfig {
	cfg := cardKeyRateLimitConfig{
		Enabled:       true,
		SuccessPerMin: successPerMin,
		SuccessBurst:  successBurst,
		FailurePerMin: failPerMin,
		FailureBurst:  failBurst,
		MaxEntries:    maxEntries,
		RefusalStatus: http.StatusNotFound,
	}
	if clock != nil {
		cfg.now = clock.Now
	}
	return cfg
}

// limitRequest issues a request from a chosen source address, which is what the
// limiter keys on.
func limitRequest(t *testing.T, router *gin.Engine, method, path, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	req.Host = "sub.example.com"
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func requireStatus(t *testing.T, label string, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s = %d, want %d (body %q, headers %v)", label, rec.Code, want, rec.Body.String(), rec.Header())
	}
}

// assertIdenticalResponses proves two responses cannot be told apart: same
// status, same body, byte-identical headers.
func assertIdenticalResponses(t *testing.T, label string, got, want *httptest.ResponseRecorder) {
	t.Helper()
	if got.Code != want.Code {
		t.Fatalf("%s: status %d != %d", label, got.Code, want.Code)
	}
	if got.Body.String() != want.Body.String() {
		t.Fatalf("%s: body %q != %q", label, got.Body.String(), want.Body.String())
	}
	if !reflect.DeepEqual(got.Header(), want.Header()) {
		t.Fatalf("%s: headers differ\n got: %v\nwant: %v", label, got.Header(), want.Header())
	}
}

// headerHash is a stable fingerprint of a response's header set, computed over
// sorted "Name: value" lines the same way the live probe does, so the two can
// be compared. Date is dropped because it moves by itself.
func headerHash(rec *httptest.ResponseRecorder) string {
	lines := make([]string, 0, len(rec.Header()))
	for name, values := range rec.Header() {
		if strings.EqualFold(name, "Date") {
			continue
		}
		for _, value := range values {
			lines = append(lines, name+": "+value)
		}
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

// assertNoStore checks the explicit anti-caching directive a subscription
// response must carry.
func assertNoStore(t *testing.T, label string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Fatalf("%s: Cache-Control = %q, want an explicit no-store directive", label, cc)
	}
}

// A budget is spent, refuses, and comes back as the clock moves: the whole
// trigger/refuse/recover cycle through the real router.
func TestCardKeyRateLimitTriggersAndRecovers(t *testing.T) {
	clock := newTestClock(t)
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 3, 10, 10, 100)))
	path := "/c/" + f.cardKey

	for i := 0; i < 3; i++ {
		requireStatus(t, fmt.Sprintf("burst request %d", i+1),
			limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	}

	refused := limitRequest(t, f.router, http.MethodGet, path, limitClientA)
	requireStatus(t, "over-budget request", refused, http.StatusNotFound)
	if refused.Body.Len() != 0 {
		t.Fatalf("refusal body = %q, want the empty body of a missing subscription", refused.Body.String())
	}
	assertNoStore(t, "refusal", refused)

	// One second later exactly one more request fits, and then the budget is
	// gone again.
	clock.Advance(time.Second)
	requireStatus(t, "after one second", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	requireStatus(t, "after the second token", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusNotFound)

	// A different caller is unaffected: the budget is per source address.
	requireStatus(t, "other address", limitRequest(t, f.router, http.MethodGet, path, limitClientB), http.StatusOK)

	// A full minute of quiet restores the full burst.
	clock.Advance(time.Minute)
	for i := 0; i < 3; i++ {
		requireStatus(t, fmt.Sprintf("recovered request %d", i+1),
			limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	}
	requireStatus(t, "recovered burst limit", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusNotFound)
}

// The budget slides with time instead of resetting on a fixed boundary: half
// the period earns half the tokens, and an unused fraction is kept.
func TestCardKeyRateLimitBudgetSlidesWithTime(t *testing.T) {
	clock := newTestClock(t)
	// 12 requests/minute with a burst of 6 == one token every five seconds.
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 12, 6, 10, 10, 100)))
	path := "/c/" + f.cardKey

	for i := 0; i < 6; i++ {
		requireStatus(t, fmt.Sprintf("burst %d", i+1), limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	}
	requireStatus(t, "burst exhausted", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusNotFound)

	// 2 of 5 seconds: 0.4 of a token, so still nothing.
	clock.Advance(2 * time.Second)
	requireStatus(t, "after 2s", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusNotFound)

	// 5 seconds in total: one whole token, so exactly one request fits.
	clock.Advance(3 * time.Second)
	requireStatus(t, "after 5s", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	requireStatus(t, "second request at 5s", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusNotFound)

	// 10 more seconds: exactly two tokens, not four.
	clock.Advance(10 * time.Second)
	for i := 0; i < 2; i++ {
		requireStatus(t, fmt.Sprintf("after 15s request %d", i+1), limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	}
	requireStatus(t, "after 15s third request", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusNotFound)

	// Long idle time never exceeds the burst.
	clock.Advance(time.Hour)
	for i := 0; i < 6; i++ {
		requireStatus(t, fmt.Sprintf("after an hour %d", i+1), limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	}
	requireStatus(t, "after an hour, past the burst", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusNotFound)
}

// A refusal must end the chain. Writing a status from a middleware does not,
// and a limiter that let the handler run anyway would append the subscriber's
// real configuration to the refusal — leaking precisely what it protects.
func TestCardKeyRateLimitRefusalNeverReachesTheHandler(t *testing.T) {
	markers := []string{"vless://", "trojan://", "vmess://", "ss://"}
	headerMarkers := []string{"Profile-Title", "Profile-Update-Interval", "Subscription-Userinfo", "Profile-Web-Page-Url"}

	for _, status := range []int{http.StatusNotFound, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			clock := newTestClock(t)
			cfg := limitConfig(clock, 60, 1, 10, 1, 100)
			cfg.RefusalStatus = status
			f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(cfg))
			path := "/c/" + f.cardKey

			served := limitRequest(t, f.router, http.MethodGet, path, limitClientA)
			requireStatus(t, "served request", served, http.StatusOK)
			if !strings.Contains(served.Body.String(), "vless://") && !strings.Contains(served.Body.String(), "trojan://") {
				t.Fatalf("the fixture's subscription body is not the shape this test guards: %q", served.Body.String())
			}

			refused := limitRequest(t, f.router, http.MethodGet, path, limitClientA)
			requireStatus(t, "refused request", refused, status)
			for _, marker := range markers {
				if strings.Contains(refused.Body.String(), marker) {
					t.Fatalf("the refusal body carries %q: the handler ran after the limiter refused", marker)
				}
			}
			for _, name := range headerMarkers {
				if got := refused.Header().Get(name); got != "" {
					t.Fatalf("the refusal carries %s: %q — the handler ran after the limiter refused", name, got)
				}
			}
			// The refusal is exactly the answer a key that does not exist gets,
			// and the served request is the only one that looked like a
			// subscription.
			if status == http.StatusNotFound {
				if refused.Body.Len() != 0 {
					t.Fatalf("refusal body = %q, want empty", refused.Body.String())
				}
			} else if refused.Body.String() != cardKeyRateLimitBody {
				t.Fatalf("refusal body = %q, want the fixed %q", refused.Body.String(), cardKeyRateLimitBody)
			}
		})
	}
}

// The explicit refusal mode is available for operators who want a retry signal;
// it is still byte-identical for a key that exists and one that does not.
func TestCardKeyRateLimitExplicitRefusalMode(t *testing.T) {
	clock := newTestClock(t)
	cfg := limitConfig(clock, 60, 2, 10, 10, 100)
	cfg.RefusalStatus = http.StatusTooManyRequests
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(cfg))
	path := "/c/" + f.cardKey

	for i := 0; i < 2; i++ {
		requireStatus(t, fmt.Sprintf("burst %d", i+1), limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	}
	refused := limitRequest(t, f.router, http.MethodGet, path, limitClientA)
	requireStatus(t, "over-budget request", refused, http.StatusTooManyRequests)
	if refused.Body.String() != cardKeyRateLimitBody {
		t.Fatalf("refusal body = %q, want the fixed %q", refused.Body.String(), cardKeyRateLimitBody)
	}
	if got := refused.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1 (60 requests/minute => the next token is one second away)", got)
	}
	assertNoStore(t, "refusal", refused)
	assertIdenticalResponses(t, "429 refusal of a valid vs an unknown key", refused,
		limitRequest(t, f.router, http.MethodGet, "/c/LD-2222-3333-4444", limitClientA))
}

// Failed lookups have their own budget: an enumeration burns it without
// touching the budget of a working subscription.
func TestCardKeyRateLimitFailureBudgetIsSeparateFromSuccess(t *testing.T) {
	clock := newTestClock(t)
	t.Run("misses do not spend the success budget", func(t *testing.T) {
		f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 2, 10, 10, 100)))
		unknown := "/c/LD-2222-3333-4444"

		for i := 0; i < 5; i++ {
			requireStatus(t, fmt.Sprintf("miss %d", i+1), limitRequest(t, f.router, http.MethodGet, unknown, limitClientA), http.StatusNotFound)
		}
		// The success budget still holds its full burst of two.
		for i := 0; i < 2; i++ {
			requireStatus(t, fmt.Sprintf("valid %d", i+1), limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey, limitClientA), http.StatusOK)
		}
		requireStatus(t, "valid, past the burst", limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey, limitClientA), http.StatusNotFound)
	})

	t.Run("an exhausted failure budget refuses valid keys too", func(t *testing.T) {
		// A refusal that only applied to bad keys would answer the question
		// "does this key exist?" all by itself.
		f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 5, 10, 2, 100)))
		unknown := "/c/LD-2222-3333-4444"

		for i := 0; i < 2; i++ {
			requireStatus(t, fmt.Sprintf("miss %d", i+1), limitRequest(t, f.router, http.MethodGet, unknown, limitClientA), http.StatusNotFound)
		}
		refusedMiss := limitRequest(t, f.router, http.MethodGet, unknown, limitClientA)
		requireStatus(t, "third miss", refusedMiss, http.StatusNotFound)
		refusedValid := limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey, limitClientA)
		requireStatus(t, "valid key while the failure budget is exhausted", refusedValid, http.StatusNotFound)
		assertIdenticalResponses(t, "refusal of a miss vs a valid key", refusedMiss, refusedValid)

		// One failed lookup every six seconds, and the key that exists is
		// served again the moment the budget allows.
		clock.Advance(6 * time.Second)
		requireStatus(t, "valid key after recovery", limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey, limitClientA), http.StatusOK)
	})
}

// The card-key route keeps answering exactly like the subId route, and once the
// limiter refuses, the refusal is the "missing subscription" answer byte for
// byte — never a function of the key that was asked for.
func TestCardKeyRateLimitRefusalIsNotAnExistenceOracle(t *testing.T) {
	clock := newTestClock(t)
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 5, 10, 1, 100)))

	// Under budget the routes are indistinguishable, cache headers included,
	// and the sorted-header fingerprint proves it.
	unknownKey := limitRequest(t, f.router, http.MethodGet, "/c/LD-2222-3333-4444", limitClientA)
	malformed := limitRequest(t, f.router, http.MethodGet, "/c/ABC", limitClientA)
	unknownSub := limitRequest(t, f.router, http.MethodGet, "/sub/does-not-exist", limitClientA)
	assertIdenticalResponses(t, "unknown card key vs unknown subId", unknownKey, unknownSub)
	assertIdenticalResponses(t, "malformed card key vs unknown subId", malformed, unknownSub)
	assertNoStore(t, "unknown card key", unknownKey)
	requireStatus(t, "unknown card key", unknownKey, http.StatusNotFound)
	t.Logf("header fingerprints: unknown key %s, malformed key %s, unknown subId %s",
		headerHash(unknownKey), headerHash(malformed), headerHash(unknownSub))
	if headerHash(unknownKey) != headerHash(unknownSub) || headerHash(malformed) != headerHash(unknownSub) {
		t.Fatalf("the three misses no longer share one header fingerprint: %s / %s / %s",
			headerHash(unknownKey), headerHash(malformed), headerHash(unknownSub))
	}

	// Exhaust this caller's failure budget with one more miss.
	requireStatus(t, "second miss", limitRequest(t, f.router, http.MethodGet, "/c/LD-2222-3333-4444", limitClientA), http.StatusNotFound)

	// The same caller now gets the very same bytes for a key that does not
	// exist, a malformed key, and a key that does exist.
	refusals := map[string]*httptest.ResponseRecorder{
		"unknown key":   limitRequest(t, f.router, http.MethodGet, "/c/LD-2222-3333-4444", limitClientA),
		"malformed key": limitRequest(t, f.router, http.MethodGet, "/c/ABC", limitClientA),
		"valid key":     limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey, limitClientA),
	}
	for label, rec := range refusals {
		requireStatus(t, label, rec, http.StatusNotFound)
		if rec.Body.Len() != 0 {
			t.Fatalf("%s: refusal body = %q, want empty", label, rec.Body.String())
		}
		assertIdenticalResponses(t, "refusal via "+label+" vs unknown subId", rec, unknownSub)
		assertNoStore(t, "refusal via "+label, rec)
	}
	t.Logf("refused valid key fingerprint: %s (unknown subId: %s)", headerHash(refusals["valid key"]), headerHash(unknownSub))
}

// HEAD and GET are the same request for the limiter: they draw on the same
// budget and produce the same refusal.
func TestCardKeyRateLimitGetAndHeadShareOneBudget(t *testing.T) {
	clock := newTestClock(t)
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 1, 10, 10, 100)))
	path := "/c/" + f.cardKey

	requireStatus(t, "GET", limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
	headRefusal := limitRequest(t, f.router, http.MethodHead, path, limitClientA)
	requireStatus(t, "HEAD after GET", headRefusal, http.StatusNotFound)

	getRefusal := limitRequest(t, f.router, http.MethodGet, path, limitClientA)
	requireStatus(t, "GET refusal", getRefusal, http.StatusNotFound)
	assertIdenticalResponses(t, "HEAD vs GET refusal", headRefusal, getRefusal)

	// Recovery is method-agnostic too: each method gets the next token.
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		clock.Advance(time.Second)
		requireStatus(t, method+" after recovery", limitRequest(t, f.router, method, path, limitClientA), http.StatusOK)
	}
}

// Concurrent requests cannot overshoot a burst: the check and the reservation
// happen under one lock.
func TestCardKeyRateLimitConcurrentRequestsCannotOvershoot(t *testing.T) {
	clock := newTestClock(t)
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 20, 10, 20, 100)))
	path := "/c/" + f.cardKey

	const workers = 60
	var served, refused, unexpected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch limitRequest(t, f.router, http.MethodGet, path, limitClientA).Code {
			case http.StatusOK:
				served.Add(1)
			case http.StatusNotFound:
				refused.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	wg.Wait()

	if unexpected.Load() != 0 {
		t.Fatalf("%d concurrent requests answered with an unexpected status (a 5xx here means the lookup failed under load)", unexpected.Load())
	}
	if got := served.Load(); got != 20 {
		t.Fatalf("%d concurrent requests were served, want exactly the 20-token burst", got)
	}
	if got := refused.Load(); got != workers-20 {
		t.Fatalf("%d concurrent requests were refused, want %d", got, workers-20)
	}
}

// A concurrent flood of guesses must not produce a single error: no 5xx, no
// failed lookup, and the caller is simply out of budget afterwards.
func TestCardKeyRateLimitConcurrentMissesStayClean(t *testing.T) {
	clock := newTestClock(t)
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 20, 10, 10, 100)))

	const workers = 60
	var notFound, unexpected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch limitRequest(t, f.router, http.MethodGet, "/c/LD-2222-3333-4444", limitClientA).Code {
			case http.StatusNotFound:
				notFound.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	wg.Wait()

	if unexpected.Load() != 0 {
		t.Fatalf("%d concurrent misses answered with an unexpected status", unexpected.Load())
	}
	if got := notFound.Load(); got != workers {
		t.Fatalf("%d concurrent misses answered 404, want %d", got, workers)
	}
	// The flood spent the failure budget: a valid key is refused too.
	requireStatus(t, "valid key after the flood", limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey, limitClientA), http.StatusNotFound)
}

// The tracked-caller table has a hard ceiling and drains when traffic stops.
func TestCardKeyRateLimiterBoundsItsTable(t *testing.T) {
	clock := newTestClock(t)
	const maxEntries = 16
	// A one-token failure budget makes "the record is still there" observable:
	// a tracked record refuses the next request, a fresh one would not.
	limiter := newCardKeyRateLimiter(limitConfig(clock, 60, 20, 10, 1, maxEntries))
	if limiter == nil {
		t.Fatal("newCardKeyRateLimiter returned nil for an enabled config")
	}

	address := func(i int) string { return fmt.Sprintf("198.51.100.%d", i%256) }
	for i := 0; i < 500; i++ {
		limiter.charge(address(i), "/c/*", true)
	}
	if got := limiter.size(); got > maxEntries {
		t.Fatalf("tracked callers = %d, want at most the %d-entry ceiling", got, maxEntries)
	}

	// The most recently seen caller survived the evictions.
	if _, denied := limiter.charge(address(499), "/c/*", true); !denied {
		t.Fatalf("the newest caller lost its record to eviction")
	}

	// After the idle TTL the sweep empties the table, even though nothing was
	// refused in the meantime.
	clock.Advance(cardKeyRateLimitIdleTTL + cardKeyRateLimitSweepInterval)
	limiter.charge("203.0.113.200", "/c/*", true)
	if got := limiter.size(); got != 1 {
		t.Fatalf("tracked callers after the idle sweep = %d, want 1", got)
	}

	// A disabled limiter is nil, which is what "no limit" means to the
	// middleware.
	if limiter := newCardKeyRateLimiter(cardKeyRateLimitConfig{Enabled: false}); limiter != nil {
		t.Fatal("a disabled config produced a limiter")
	}
}

// A client refreshing at the shipped cadence is never limited, and each address
// keeps its own budget.
func TestCardKeyRateLimitAllowsTheShippedRefreshCadence(t *testing.T) {
	clock := newTestClock(t)
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 20, 10, 10, 100)))
	path := "/c/" + f.cardKey

	for i := 0; i < 10; i++ {
		requireStatus(t, fmt.Sprintf("refresh %d at %s", i+1, clock.Now().Format(time.TimeOnly)),
			limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
		requireStatus(t, "second device on the same address",
			limitRequest(t, f.router, http.MethodGet, path, limitClientA), http.StatusOK)
		clock.Advance(12 * time.Minute)
	}
}

// Every route under /c/ carries its own budget, so one exhausted endpoint does
// not lock a subscriber out of the others.
func TestCardKeyRateLimitBudgetsFollowTheEndpoint(t *testing.T) {
	clock := newTestClock(t)
	f := initCardKeySubRouter(t, WithSUBCardKeyRateLimit(limitConfig(clock, 60, 5, 10, 1, 100)))
	unknown := "LD-2222-3333-4444"

	// One miss spends the raw endpoint's one-token failure budget.
	requireStatus(t, "raw miss", limitRequest(t, f.router, http.MethodGet, "/c/"+unknown, limitClientA), http.StatusNotFound)
	// A valid key on the raw endpoint is refused with the same answer, because
	// that endpoint's failure budget is what ran out.
	requireStatus(t, "valid key on the raw endpoint", limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey, limitClientA), http.StatusNotFound)

	// The other endpoints keep their own budgets.
	requireStatus(t, "clash", limitRequest(t, f.router, http.MethodGet, "/c/clash/"+f.cardKey, limitClientA), http.StatusOK)
	requireStatus(t, "json", limitRequest(t, f.router, http.MethodGet, "/c/json/"+f.cardKey, limitClientA), http.StatusOK)
	requireStatus(t, "hwid-status", limitRequest(t, f.router, http.MethodGet, "/c/"+f.cardKey+"/hwid-status", limitClientA), http.StatusOK)
}

// The limiter keys on the forwarded client address only when the peer is
// allowed to speak for it; otherwise it keys on the socket peer.
func TestCardKeyRateLimitClientIP(t *testing.T) {
	tests := []struct {
		name       string
		stored     *string
		remoteAddr string
		headers    map[string]string
		want       string
	}{
		{
			name:       "no forwarded headers uses the socket peer",
			remoteAddr: "203.0.113.9:51000",
			want:       "203.0.113.9",
		},
		{
			name:       "shipped default keeps trusting the forwarded client",
			stored:     new(""),
			remoteAddr: "203.0.113.9:51000",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "an origin outside the declared boundary is keyed by its own address",
			stored:     new("10.0.0.0/8"),
			remoteAddr: "203.0.113.9:51000",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7"},
			want:       "203.0.113.9",
		},
		{
			name:       "a proxy inside the boundary speaks for the client",
			stored:     new("10.0.0.0/8"),
			remoteAddr: "10.1.2.3:51000",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7, 10.9.9.9"},
			want:       "198.51.100.7",
		},
		{
			name:       "a chain of our own proxies falls back to the peer",
			stored:     new("10.0.0.0/8"),
			remoteAddr: "10.1.2.3:51000",
			headers:    map[string]string{"X-Forwarded-For": "10.9.9.9"},
			want:       "10.1.2.3",
		},
		{
			name:       "the hop a proxy appended to itself is skipped",
			remoteAddr: "203.0.113.9:51000",
			headers:    map[string]string{"X-Forwarded-For": "198.51.100.7, 127.0.0.1"},
			want:       "198.51.100.7",
		},
		{
			name:       "the rightmost hop wins",
			remoteAddr: "203.0.113.9:51000",
			headers:    map[string]string{"X-Forwarded-For": "1.2.3.4, 198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "an unparsable chain falls back to the peer",
			remoteAddr: "203.0.113.9:51000",
			headers:    map[string]string{"X-Forwarded-For": "not-an-ip"},
			want:       "203.0.113.9",
		},
		{
			name:       "X-Real-IP is used when there is no chain",
			remoteAddr: "203.0.113.9:51000",
			headers:    map[string]string{"X-Real-IP": "198.51.100.7"},
			want:       "198.51.100.7",
		},
		{
			name:       "IPv4-mapped addresses are canonicalized",
			remoteAddr: "[::ffff:198.51.100.7]:51000",
			want:       "198.51.100.7",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			initSubDB(t)
			if tc.stored != nil {
				setTrustedProxyCIDRs(t, *tc.stored)
			}
			s := &SubService{}
			if got := s.clientIPForLimit(requestFrom(t, tc.remoteAddr, tc.headers)); got != tc.want {
				t.Fatalf("clientIPForLimit() = %q, want %q", got, tc.want)
			}
		})
	}
}

// End to end over a real socket: the same probe shape that produced the
// baseline against the live panel, run against the router with the limiter
// installed. The burst is served, everything after it is refused, and not one
// answer is a server error.
func TestCardKeyRateLimitEndToEnd(t *testing.T) {
	f := initCardKeySubRouter(t)
	server := httptest.NewServer(f.router)
	defer server.Close()
	client := server.Client()
	client.Timeout = 30 * time.Second

	fetch := func(path string) (int, time.Duration) {
		t.Helper()
		start := time.Now()
		resp, err := client.Get(server.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, time.Since(start)
	}

	const attempts = 200
	path := "/c/" + f.cardKey
	codes := map[int]int{}
	var totalServed, totalRefused, slowest time.Duration
	for i := 0; i < attempts; i++ {
		code, elapsed := fetch(path)
		codes[code]++
		if elapsed > slowest {
			slowest = elapsed
		}
		if code == http.StatusOK {
			totalServed += elapsed
		} else {
			totalRefused += elapsed
		}
	}
	t.Logf("%d sequential requests to a valid card key: %v", attempts, codes)

	served := codes[http.StatusOK]
	if served != defaultCardKeyRateLimitBurst {
		t.Fatalf("%d requests were served before the limit, want %d", served, defaultCardKeyRateLimitBurst)
	}
	refused := codes[http.StatusNotFound]
	if refused != attempts-served {
		t.Fatalf("%d requests were refused, want %d", refused, attempts-served)
	}
	if other := attempts - served - refused; other != 0 {
		t.Fatalf("%d requests answered with an unexpected status: %v", other, codes)
	}
	t.Logf("average latency: served (card-key lookup + body) %v, refused (middleware only) %v, slowest %v",
		totalServed/time.Duration(served), totalRefused/time.Duration(refused), slowest)

	// A refused valid key is the answer an unknown key gets, so the live
	// histogram cannot be turned into an existence oracle.
	if code, _ := fetch("/c/LD-2222-3333-4444"); code != http.StatusNotFound {
		t.Fatalf("unknown key while over budget = %d, want 404", code)
	}

	// A concurrent burst on top of the exhausted budget: everything is refused,
	// nothing errors.
	var unexpected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if code, _ := fetch(path); code != http.StatusNotFound {
				unexpected.Add(1)
			}
		}()
	}
	wg.Wait()
	if unexpected.Load() != 0 {
		t.Fatalf("%d concurrent requests were not refused cleanly", unexpected.Load())
	}
	t.Logf("40 concurrent requests against the exhausted budget: all 404, 0 errors")
}

// Subscription bodies are per-subscriber secrets: every response must forbid
// caching, on the card-key route and on the subId route alike.
func TestCardKeyRateLimitResponsesForbidCaching(t *testing.T) {
	f := initCardKeySubRouter(t)

	pairs := []struct {
		name string
		card string
		sub  string
	}{
		{"raw", "/c/" + f.cardKey, "/sub/" + f.subID},
		{"clash", "/c/clash/" + f.cardKey, "/clash/" + f.subID},
		{"json", "/c/json/" + f.cardKey, "/json/" + f.subID},
		{"info", "/c/" + f.cardKey + "?format=info", "/sub/" + f.subID + "?format=info"},
		{"hwid-status", "/c/" + f.cardKey + "/hwid-status", "/sub/" + f.subID + "/hwid-status"},
		{"unknown", "/c/LD-2222-3333-4444", "/sub/does-not-exist"},
	}
	for _, pair := range pairs {
		cardRec := cardKeyRequest(t, f.router, pair.card)
		subRec := cardKeyRequest(t, f.router, pair.sub)
		assertNoStore(t, pair.name+" card key", cardRec)
		assertSameResponse(t, pair.name+" cache headers", cardRec, subRec)
	}

	// The three body routes carry the same explicit directive as the HTML,
	// info and hwid-status routes already did.
	const wantCacheControl = "no-cache, no-store, must-revalidate"
	for _, path := range []string{"/c/" + f.cardKey, "/c/clash/" + f.cardKey, "/c/json/" + f.cardKey} {
		rec := cardKeyRequest(t, f.router, path)
		requireStatus(t, "GET "+path, rec, http.StatusOK)
		if got := rec.Header().Get("Cache-Control"); got != wantCacheControl {
			t.Fatalf("GET %s: Cache-Control = %q, want %q", path, got, wantCacheControl)
		}
		if got := rec.Header().Get("Pragma"); got != "no-cache" {
			t.Fatalf("GET %s: Pragma = %q, want no-cache", path, got)
		}
		if got := rec.Header().Get("Expires"); got != "0" {
			t.Fatalf("GET %s: Expires = %q, want 0", path, got)
		}
		// The auto-detect branches answer the same URL with a different format
		// per client, so the response varies on the client too.
		if got := rec.Header().Get("Vary"); got != "User-Agent" {
			t.Fatalf("GET %s: Vary = %q, want User-Agent", path, got)
		}
	}
}

// The shipped defaults are the documented budget, and the environment can tune
// or switch the limiter off.
func TestCardKeyRateLimitConfigDefaultsAndOverrides(t *testing.T) {
	for _, name := range []string{
		cardKeyRateLimitEnvEnable,
		cardKeyRateLimitEnvPerMin,
		cardKeyRateLimitEnvBurst,
		cardKeyRateLimitEnvFailPerMin,
		cardKeyRateLimitEnvFailBurst,
		cardKeyRateLimitEnvMaxEntries,
		cardKeyRateLimitEnvStatus,
	} {
		t.Setenv(name, "")
	}
	cfg := defaultCardKeyRateLimitConfig()
	if !cfg.Enabled ||
		cfg.SuccessPerMin != 60 || cfg.SuccessBurst != 20 ||
		cfg.FailurePerMin != 10 || cfg.FailureBurst != 10 ||
		cfg.MaxEntries != 10000 || cfg.RefusalStatus != http.StatusNotFound {
		t.Fatalf("default budget = %+v, want 60/20, 10/10, 10000 entries, a 404 refusal, enabled", cfg)
	}

	t.Setenv(cardKeyRateLimitEnvPerMin, "120")
	t.Setenv(cardKeyRateLimitEnvBurst, "30")
	t.Setenv(cardKeyRateLimitEnvFailPerMin, "4")
	t.Setenv(cardKeyRateLimitEnvFailBurst, "2")
	t.Setenv(cardKeyRateLimitEnvMaxEntries, "500")
	t.Setenv(cardKeyRateLimitEnvStatus, "429")
	cfg = defaultCardKeyRateLimitConfig()
	if cfg.SuccessPerMin != 120 || cfg.SuccessBurst != 30 || cfg.FailurePerMin != 4 || cfg.FailureBurst != 2 || cfg.MaxEntries != 500 {
		t.Fatalf("tuned budget = %+v, want the environment values", cfg)
	}
	if cfg.RefusalStatus != http.StatusTooManyRequests {
		t.Fatalf("RefusalStatus = %d, want 429 from the environment", cfg.RefusalStatus)
	}

	// Junk and zero values fall back instead of disabling the ceilings.
	t.Setenv(cardKeyRateLimitEnvMaxEntries, "0")
	t.Setenv(cardKeyRateLimitEnvPerMin, "not-a-number")
	t.Setenv(cardKeyRateLimitEnvStatus, "418")
	cfg = defaultCardKeyRateLimitConfig()
	if cfg.MaxEntries != 10000 || cfg.SuccessPerMin != 60 {
		t.Fatalf("junk budget = %+v, want the shipped defaults", cfg)
	}
	if cfg.RefusalStatus != http.StatusNotFound {
		t.Fatalf("junk RefusalStatus = %d, want the not-found default", cfg.RefusalStatus)
	}
	if limiter := newCardKeyRateLimiter(cfg); limiter == nil {
		t.Fatal("a malformed environment must not disable the limiter")
	}

	for _, off := range []string{"off", "false", "0", "no", "disabled"} {
		t.Setenv(cardKeyRateLimitEnvEnable, off)
		if cfg := defaultCardKeyRateLimitConfig(); cfg.Enabled {
			t.Fatalf("%s did not switch the limiter off", off)
		}
		if limiter := newCardKeyRateLimiter(defaultCardKeyRateLimitConfig()); limiter != nil {
			t.Fatalf("%s left a limiter installed", off)
		}
	}
	for _, on := range []string{"on", "true", "1", "yes", "enabled"} {
		t.Setenv(cardKeyRateLimitEnvEnable, on)
		if cfg := defaultCardKeyRateLimitConfig(); !cfg.Enabled {
			t.Fatalf("%s did not switch the limiter on", on)
		}
	}
}
