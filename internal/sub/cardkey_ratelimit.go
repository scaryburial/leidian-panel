package sub

import (
	"container/list"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Card-key ("卡密") endpoint rate limiting.
//
// /c/<card_key> is the only subscription entry point that takes a
// caller-chosen secret, so it is the one an attacker can enumerate: the panel
// resolves every well-formed guess with a database lookup and answers 404. The
// limiter below bounds that work per source address and per endpoint, and it is
// deliberately blind to whether the requested key existed:
//
//   - the decision to refuse comes from the caller's own stored history, never
//     from the key on the current request, so a 429 cannot tell a caller
//     whether a key exists;
//   - every refusal is one status, one fixed body and one fixed header set, so
//     refusals cannot be told apart from one another either;
//   - a caller over one budget is refused for every key, valid or not, so the
//     refusal cannot be turned into a probe;
//   - untouched state is a fixed-size map with an LRU eviction order and a
//     periodic idle sweep, so a flood of distinct addresses cannot grow memory
//     without bound.
//
// Thresholds are read once, from the environment (the XUI_* pattern the rest of
// the panel uses for operator-tunable knobs), when the subscription router is
// built. Changing them needs a panel restart.
const (
	// Default budgets. 60 requests/minute with a 20-request burst is far above
	// what a VPN client needs (the shipped update interval is 12 minutes) and
	// still bounds a single address; the failure budget is the enumeration
	// budget and is deliberately tighter: 10 unknown keys a minute, then one
	// more every 6 seconds.
	defaultCardKeyRateLimitPerMinute     = 60
	defaultCardKeyRateLimitBurst         = 20
	defaultCardKeyRateLimitFailPerMinute = 10
	defaultCardKeyRateLimitFailBurst     = 10

	// Hard ceiling on tracked (address, endpoint) records. 10000 distinct
	// callers inside the idle TTL is far beyond this deployment's fan-out, and
	// holds the limiter at roughly a megabyte.
	defaultCardKeyRateLimitMaxEntries = 10000

	// cardKeyRateLimitIdleTTL is how long a record with no traffic is kept.
	// It comfortably exceeds the longest possible refusal (a full bucket of
	// failures refills in one minute), so an idle record is always refilled
	// when the sweep drops it.
	cardKeyRateLimitIdleTTL = 10 * time.Minute
	// cardKeyRateLimitSweepInterval bounds the idle sweep to once a minute.
	cardKeyRateLimitSweepInterval = time.Minute

	// cardKeyRateLimitBody is the refusal body used by the explicit refusal
	// mode (RefusalStatus 429). It is a constant on purpose: a body derived
	// from the request, or from the budget that refused it, would hand the
	// caller the state that produced the refusal.
	cardKeyRateLimitBody = "rate limit exceeded"

	cardKeyRateLimitEnvEnable     = "XUI_SUB_CARDKEY_RATELIMIT"
	cardKeyRateLimitEnvPerMin     = "XUI_SUB_CARDKEY_RATELIMIT_PER_MIN"
	cardKeyRateLimitEnvBurst      = "XUI_SUB_CARDKEY_RATELIMIT_BURST"
	cardKeyRateLimitEnvFailPerMin = "XUI_SUB_CARDKEY_RATELIMIT_FAIL_PER_MIN"
	cardKeyRateLimitEnvFailBurst  = "XUI_SUB_CARDKEY_RATELIMIT_FAIL_BURST"
	cardKeyRateLimitEnvMaxEntries = "XUI_SUB_CARDKEY_RATELIMIT_MAX_ENTRIES"
	cardKeyRateLimitEnvStatus     = "XUI_SUB_CARDKEY_RATELIMIT_STATUS"
)

// tokenScale is the fixed-point unit of one token. Counting in integers keeps
// top-ups exact (no float drift), which is what lets a test advance a fake
// clock and assert an exact number of remaining requests.
const tokenScale = 1_000_000

// cardKeyRateLimitConfig is the limiter's budget. Build one with
// defaultCardKeyRateLimitConfig and adjust it through WithSUBCardKeyRateLimit.
type cardKeyRateLimitConfig struct {
	// Enabled turns the whole limiter off; disabled means the middleware is not
	// installed at all.
	Enabled bool

	// SuccessPerMin/SuccessBurst bound requests that resolved to a real
	// subscription. Without them a caller holding one working key could use the
	// endpoint as an unmetered subscription proxy.
	SuccessPerMin int
	SuccessBurst  int

	// FailurePerMin/FailureBurst bound requests whose key (or malformed key)
	// resolved to nothing — the enumeration budget.
	FailurePerMin int
	FailureBurst  int

	// MaxEntries caps the tracked (address, endpoint) records.
	MaxEntries int

	// RefusalStatus is the answer given to a caller that is over budget:
	// http.StatusNotFound (the default) repeats the "no such subscription"
	// answer byte for byte, so a refusal cannot be told apart from a failed
	// guess; http.StatusTooManyRequests is the explicit variant for operators
	// who would rather have their clients see a retry signal.
	RefusalStatus int

	// now is the clock seam used by tests; nil means time.Now.
	now func() time.Time
}

// defaultCardKeyRateLimitConfig returns the shipped budget with any
// environment overrides applied.
func defaultCardKeyRateLimitConfig() cardKeyRateLimitConfig {
	cfg := cardKeyRateLimitConfig{
		Enabled:       true,
		SuccessPerMin: defaultCardKeyRateLimitPerMinute,
		SuccessBurst:  defaultCardKeyRateLimitBurst,
		FailurePerMin: defaultCardKeyRateLimitFailPerMinute,
		FailureBurst:  defaultCardKeyRateLimitFailBurst,
		MaxEntries:    defaultCardKeyRateLimitMaxEntries,
		RefusalStatus: http.StatusNotFound,
	}
	if value, ok := envToggle(cardKeyRateLimitEnvEnable); ok {
		cfg.Enabled = value
	}
	cfg.SuccessPerMin = envPositiveInt(cardKeyRateLimitEnvPerMin, cfg.SuccessPerMin)
	cfg.SuccessBurst = envPositiveInt(cardKeyRateLimitEnvBurst, cfg.SuccessBurst)
	cfg.FailurePerMin = envPositiveInt(cardKeyRateLimitEnvFailPerMin, cfg.FailurePerMin)
	cfg.FailureBurst = envPositiveInt(cardKeyRateLimitEnvFailBurst, cfg.FailureBurst)
	cfg.MaxEntries = envPositiveInt(cardKeyRateLimitEnvMaxEntries, cfg.MaxEntries)
	if status, ok := envRefusalStatus(); ok {
		cfg.RefusalStatus = status
	}
	cfg.sanitize()
	return cfg
}

// sanitize replaces any unusable threshold with its shipped default. MaxEntries
// in particular must never end up at zero ("unlimited"): the ceiling is what
// keeps the map bounded.
func (cfg *cardKeyRateLimitConfig) sanitize() {
	if cfg.SuccessPerMin < 1 {
		cfg.SuccessPerMin = defaultCardKeyRateLimitPerMinute
	}
	if cfg.SuccessBurst < 1 {
		cfg.SuccessBurst = defaultCardKeyRateLimitBurst
	}
	if cfg.FailurePerMin < 1 {
		cfg.FailurePerMin = defaultCardKeyRateLimitFailPerMinute
	}
	if cfg.FailureBurst < 1 {
		cfg.FailureBurst = defaultCardKeyRateLimitFailBurst
	}
	if cfg.MaxEntries < 1 {
		cfg.MaxEntries = defaultCardKeyRateLimitMaxEntries
	}
	// Anything but the explicit 429 falls back to the not-found shape, which is
	// the one that cannot be told apart from a failed lookup.
	if cfg.RefusalStatus != http.StatusTooManyRequests {
		cfg.RefusalStatus = http.StatusNotFound
	}
}

// envPositiveInt reads a positive integer override, keeping fallback (with a
// warning) when the value is absent or unusable.
func envPositiveInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		logger.Warningf("sub: ignoring %s=%q (want a positive integer); keeping %d", name, raw, fallback)
		return fallback
	}
	return value
}

// envToggle reads an on/off override. It accepts the words an operator is
// likely to write ("off", "on") in addition to the booleans ParseBool knows.
func envToggle(name string) (value, ok bool) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	switch raw {
	case "":
		return false, false
	case "1", "t", "true", "on", "yes", "enable", "enabled":
		return true, true
	case "0", "f", "false", "off", "no", "disable", "disabled":
		return false, true
	}
	logger.Warningf("sub: ignoring %s=%q (want on/off); keeping the default", name, raw)
	return false, false
}

// envRefusalStatus reads the refusal-shape override. Only the two shapes the
// middleware can produce are accepted.
func envRefusalStatus() (int, bool) {
	raw := strings.TrimSpace(os.Getenv(cardKeyRateLimitEnvStatus))
	if raw == "" {
		return 0, false
	}
	switch raw {
	case "404":
		return http.StatusNotFound, true
	case "429":
		return http.StatusTooManyRequests, true
	}
	logger.Warningf("sub: ignoring %s=%q (want 404 or 429); keeping the default %d", cardKeyRateLimitEnvStatus, raw, http.StatusNotFound)
	return 0, false
}

// tokenBucket is one budget: tokens accrue at perMinute and are spent one per
// request, up to burst of them at once.
type tokenBucket struct {
	tokens int64 // in tokenScale units
	last   time.Time
}

// refill credits the tokens the elapsed time earned, capped at burst. It also
// initialises a fresh bucket to full, so the first request from an address is
// never refused.
func (b *tokenBucket) refill(now time.Time, perMinute, burst int) {
	if b.last.IsZero() {
		b.last = now
		b.tokens = int64(burst) * tokenScale
		return
	}
	elapsed := now.Sub(b.last)
	// Sub-millisecond gaps are left uncredited rather than rounded away, so
	// the remainder is not lost.
	if elapsed < time.Millisecond {
		return
	}
	b.last = now
	b.tokens += elapsed.Milliseconds() * int64(perMinute) * tokenScale / 60000
	if full := int64(burst) * tokenScale; b.tokens > full {
		b.tokens = full
	}
}

func (b *tokenBucket) available() bool { return b.tokens >= tokenScale }

// retryAfter reports how long until one whole token is available again.
func (b *tokenBucket) retryAfter(perMinute int) time.Duration {
	if b.available() {
		return 0
	}
	missing := tokenScale - b.tokens
	// Ceiling division: a caller told to wait 0 seconds would retry into
	// another refusal.
	ms := (missing*60000 + int64(perMinute)*tokenScale - 1) / (int64(perMinute) * tokenScale)
	if ms < 1 {
		ms = 1
	}
	return time.Duration(ms) * time.Millisecond
}

// cardKeyBucket is the state kept for one (address, endpoint) pair: the
// success budget, the failure budget, and its place in the LRU order.
type cardKeyBucket struct {
	key      string
	success  tokenBucket
	failure  tokenBucket
	lastSeen time.Time
	elem     *list.Element
}

// cardKeyRateLimiter stores the per-caller budgets. It is safe for concurrent
// use; every mutation happens under mu.
type cardKeyRateLimiter struct {
	mu        sync.Mutex
	cfg       cardKeyRateLimitConfig
	now       func() time.Time
	buckets   map[string]*cardKeyBucket
	order     *list.List // LRU: front is the most recently seen record
	lastSweep time.Time
}

// newCardKeyRateLimiter builds a limiter, or returns nil when the feature is
// switched off (a nil limiter is what the middleware uses to mean "no limit").
func newCardKeyRateLimiter(cfg cardKeyRateLimitConfig) *cardKeyRateLimiter {
	if !cfg.Enabled {
		return nil
	}
	cfg.sanitize()
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	return &cardKeyRateLimiter{
		cfg:     cfg,
		now:     now,
		buckets: make(map[string]*cardKeyBucket),
		order:   list.New(),
	}
}

// cardKeyLimitKey is the (address, endpoint) identity budgets are kept under.
// Both parts are normalized by the caller: the address is always a parsed IP
// or the literal "unknown", never a caller-supplied string of arbitrary length.
func cardKeyLimitKey(address, endpoint string) string {
	return address + "\x00" + endpoint
}

// peek reports whether this caller is already over budget, without consuming
// anything and without creating a record — a flood of distinct addresses can
// hammer the gate without ever growing the map.
func (l *cardKeyRateLimiter) peek(address, endpoint string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepIfDueLocked(now)

	record := l.buckets[cardKeyLimitKey(address, endpoint)]
	if record == nil {
		return 0, false
	}
	l.refillLocked(record, now)
	l.touchLocked(record, now)

	// The failure budget is consulted first, but either exhausted budget
	// refuses every request from this caller, whether or not the key on this
	// request exists: the refusal must not depend on the key.
	if !record.failure.available() {
		return record.failure.retryAfter(l.cfg.FailurePerMin), true
	}
	if !record.success.available() {
		return record.success.retryAfter(l.cfg.SuccessPerMin), true
	}
	return 0, false
}

// charge reserves exactly one token from the budget the outcome belongs to:
// the failure budget when failed is true (the key resolved to nothing), the
// success budget otherwise. It reports a refusal when the caller has no budget
// left, in which case nothing is consumed and the request must not run.
//
// The check and the reservation share one lock, so concurrent requests cannot
// overshoot a burst. The availability of both budgets is checked before either
// is charged, so a caller that is over one budget is refused for every key.
func (l *cardKeyRateLimiter) charge(address, endpoint string, failed bool) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepIfDueLocked(now)

	key := cardKeyLimitKey(address, endpoint)
	record := l.buckets[key]
	if record == nil {
		l.evictForRoomLocked(now)
		record = &cardKeyBucket{key: key}
		record.elem = l.order.PushFront(record)
		l.buckets[key] = record
	}
	l.refillLocked(record, now)
	l.touchLocked(record, now)

	if !record.failure.available() {
		return record.failure.retryAfter(l.cfg.FailurePerMin), true
	}
	if !record.success.available() {
		return record.success.retryAfter(l.cfg.SuccessPerMin), true
	}

	budget := &record.success
	if failed {
		budget = &record.failure
	}
	budget.tokens -= tokenScale
	return 0, false
}

// size reports how many records are tracked. Used by tests to prove the map
// stays bounded.
func (l *cardKeyRateLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

func (l *cardKeyRateLimiter) refillLocked(record *cardKeyBucket, now time.Time) {
	record.success.refill(now, l.cfg.SuccessPerMin, l.cfg.SuccessBurst)
	record.failure.refill(now, l.cfg.FailurePerMin, l.cfg.FailureBurst)
}

func (l *cardKeyRateLimiter) touchLocked(record *cardKeyBucket, now time.Time) {
	record.lastSeen = now
	if record.elem != nil {
		l.order.MoveToFront(record.elem)
	}
}

func (l *cardKeyRateLimiter) removeLocked(record *cardKeyBucket) {
	if record.elem != nil {
		l.order.Remove(record.elem)
		record.elem = nil
	}
	delete(l.buckets, record.key)
}

// sweepIfDueLocked runs the idle sweep at most once per sweep interval, so the
// cost of walking the table stays proportional to the table's size and not to
// the request rate.
func (l *cardKeyRateLimiter) sweepIfDueLocked(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < cardKeyRateLimitSweepInterval {
		return
	}
	l.lastSweep = now
	l.sweepLocked(now)
}

// sweepLocked drops records that have seen no traffic for the idle TTL. The LRU
// order means the idle ones are at the back, so the walk stops at the first
// live record.
func (l *cardKeyRateLimiter) sweepLocked(now time.Time) {
	cutoff := now.Add(-cardKeyRateLimitIdleTTL)
	for elem := l.order.Back(); elem != nil; {
		record := elem.Value.(*cardKeyBucket)
		if record.lastSeen.After(cutoff) {
			return
		}
		previous := elem.Prev()
		l.removeLocked(record)
		elem = previous
	}
}

// evictForRoomLocked makes room for one new record once the ceiling is reached.
// Idle records go first; if the table is still full (a broad flood of distinct
// addresses), the least recently seen record is dropped so the footprint can
// never grow past MaxEntries.
func (l *cardKeyRateLimiter) evictForRoomLocked(now time.Time) {
	if len(l.buckets) < l.cfg.MaxEntries {
		return
	}
	l.sweepLocked(now)
	for len(l.buckets) >= l.cfg.MaxEntries {
		back := l.order.Back()
		if back == nil {
			return
		}
		l.removeLocked(back.Value.(*cardKeyBucket))
	}
}

// cardKeyRateLimitIdentityCacheKey memoizes the resolved source address for the
// duration of one request: the gate and the charge middleware must agree on it,
// and resolving it reads a setting.
const cardKeyRateLimitIdentityCacheKey = "sub.cardkey.ratelimit.identity"

// cardKeyRateLimitIdentity returns the (address, endpoint) pair this request is
// budgeted under.
func (a *SUBController) cardKeyRateLimitIdentity(c *gin.Context) (address, endpoint string) {
	if cached, ok := c.Get(cardKeyRateLimitIdentityCacheKey); ok {
		address, _ = cached.(string)
	} else {
		address = a.subService.clientIPForLimit(c)
		c.Set(cardKeyRateLimitIdentityCacheKey, address)
	}
	return address, cardKeyEndpointFamily(c)
}

// cardKeyEndpointFamily names the route shape rather than the key, so that
// /c/<key>, /c/clash/<key>, /c/json/<key> and /c/<key>/hwid-status each carry a
// budget of their own instead of exhausting one shared pool. A route that is
// not one of them (which cannot happen while the group is mounted) falls back
// to a single shared pool.
func cardKeyEndpointFamily(c *gin.Context) string {
	full := c.FullPath()
	if full == "" {
		return "unknown"
	}
	trimmed, ok := strings.CutPrefix(full, subCardKeyPath)
	if !ok {
		return "unknown"
	}
	return strings.ReplaceAll(trimmed, ":subid", "*")
}

// cardKeyRateLimitGate refuses a caller that is already over budget before the
// card-key lookup runs, so a flood of guesses stops costing a database query
// each. It only reads state, which is why the charge middleware below — not
// this one — is what reserves a token.
func (a *SUBController) cardKeyRateLimitGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		limiter := a.cardKeyLimiter
		if limiter == nil {
			c.Next()
			return
		}
		address, endpoint := a.cardKeyRateLimitIdentity(c)
		if retryAfter, denied := limiter.peek(address, endpoint); denied {
			a.refuseCardKey(c, retryAfter)
			return
		}
		c.Next()
	}
}

// cardKeyRateLimitCharge runs after the card-key lookup has rewritten :subid
// and before the handler, and reserves the token for this request.
func (a *SUBController) cardKeyRateLimitCharge() gin.HandlerFunc {
	return func(c *gin.Context) {
		limiter := a.cardKeyLimiter
		if limiter == nil {
			c.Next()
			return
		}
		address, endpoint := a.cardKeyRateLimitIdentity(c)
		miss := c.Param("subid") == cardKeyMissParam
		if retryAfter, denied := limiter.charge(address, endpoint, miss); denied {
			a.refuseCardKey(c, retryAfter)
			return
		}
		c.Next()
	}
}

// refuseCardKey answers a caller that is over budget.
//
// The default shape is not a distinct "rate limited" answer at all: it repeats
// the not-found answer — 404, empty body, the same headers — that a key which
// does not exist gets, byte for byte. A limited caller therefore cannot tell a
// refusal from a failed guess, and cannot use the refusal to ask whether a key
// exists. The explicit 429 shape (RefusalStatus, XUI_SUB_CARDKEY_RATELIMIT_STATUS)
// keeps the same guarantee — its body and headers are constants and its
// Retry-After comes from the caller's own bucket — at the cost of advertising
// the limit to the caller, and of showing a real client a status its app may
// report as an error.
//
// Abort comes first and is not optional: writing a status from a middleware
// does not end the chain, so without it the subscription handler would run
// anyway and append the subscriber's real configuration to the refusal — a
// limiter that leaks exactly what it is meant to protect.
func (a *SUBController) refuseCardKey(c *gin.Context, retryAfter time.Duration) {
	c.Abort()
	if a.cardKeyRefusalStatus != http.StatusTooManyRequests {
		writeSubError(c, nil)
		return
	}
	seconds := int((retryAfter + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	setNoCacheHeaders(c)
	c.Header("Retry-After", strconv.Itoa(seconds))
	c.String(http.StatusTooManyRequests, cardKeyRateLimitBody)
}
