package controller

import (
	"strings"
	"sync"
	"time"
)

const (
	// 桶 A：(IP × 用户名)。同一个 IP 对同一个用户名连续失败 5 次 / 5 分钟
	// 就封锁 15 分钟，防止针对单个账号的定向爆破。
	loginLimitMaxFailures = 5
	loginLimitWindow      = 5 * time.Minute
	loginLimitCooldown    = 15 * time.Minute

	// 桶 B：(纯 IP 聚合)。同一个 IP 对 **任意** 用户名累计失败 20 次 / 5 分钟
	// 就封锁该 IP 15 分钟。
	//
	// 为什么必须有这个桶：只有桶 A 时，攻击者每换一个用户名就换了一个全新计数桶，
	// 等于可以无限次尝试（用户名是攻击者可控的自由文本）。桶 B 只看来源 IP，
	// 换用户名无用。阈值取 20（比桶 A 的 5 宽松 4 倍）：桶 A 对付的是“盯住一个
	// 账号反复试”，桶 B 会误伤共享出口（NAT、CF 边缘节点下的多个正常用户），
	// 所以放宽一些，同时用成功登录清零该 IP 的计数来进一步降低误伤。
	//
	// 两个桶都不区分用户名是否存在：不存在的用户名与存在的用户名都要计入失败，
	// 封锁时的返回/日志也不做区分，否则限速本身就变成了用户名枚举 oracle。
	loginIPLimitMaxFailures = 20
	loginIPLimitWindow      = 5 * time.Minute
	loginIPLimitCooldown    = 15 * time.Minute

	// Hard ceiling on tracked records, applied to each of the two maps. Bucket A's
	// key includes the caller-supplied username and bucket B's key is the peer IP,
	// so an unauthenticated attacker rotating usernames/IPs would otherwise grow
	// these maps without bound.
	loginLimitMaxRecords = 10000
)

var defaultLoginLimiter = newLoginLimiter(loginLimitMaxFailures, loginLimitWindow, loginLimitCooldown)

type loginLimiter struct {
	mu          sync.Mutex
	now         func() time.Time
	maxFailures int
	window      time.Duration
	cooldown    time.Duration
	// ipMaxFailures / ipWindow / ipCooldown configure the per-IP aggregate bucket
	// (bucket B) and default to the loginIP* constants above.
	ipMaxFailures int
	ipWindow      time.Duration
	ipCooldown    time.Duration
	// attempts holds bucket A records keyed by loginLimitKey(ip, username).
	attempts map[string]*loginLimitRecord
	// ipAttempts holds bucket B records keyed by the trimmed IP alone.
	ipAttempts map[string]*loginLimitRecord
}

type loginLimitRecord struct {
	failures     []time.Time
	blockedUntil time.Time
}

func newLoginLimiter(maxFailures int, window, cooldown time.Duration) *loginLimiter {
	return &loginLimiter{
		now:           time.Now,
		maxFailures:   maxFailures,
		window:        window,
		cooldown:      cooldown,
		ipMaxFailures: loginIPLimitMaxFailures,
		ipWindow:      loginIPLimitWindow,
		ipCooldown:    loginIPLimitCooldown,
		attempts:      make(map[string]*loginLimitRecord),
		ipAttempts:    make(map[string]*loginLimitRecord),
	}
}

// allow reports whether a login attempt from ip for username may proceed. It
// consults the per-IP aggregate bucket first: once an IP is blocked, rotating
// the username must not unlock it.
func (l *loginLimiter) allow(ip, username string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	if blockedUntil, blocked := l.checkBucket(l.ipAttempts, strings.TrimSpace(ip), l.ipWindow, now); blocked {
		return blockedUntil, false
	}
	if blockedUntil, blocked := l.checkBucket(l.attempts, loginLimitKey(ip, username), l.window, now); blocked {
		return blockedUntil, false
	}
	return time.Time{}, true
}

// registerFailure records one failed login in both buckets and returns the
// deadline of a block that this failure triggered (zero time when not blocked).
// Bucket A answers "this IP is hammering this username"; bucket B answers "this
// IP is hammering the panel with any username at all".
func (l *loginLimiter) registerFailure(ip, username string) (time.Time, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	byUser := l.recordFailure(l.attempts, loginLimitKey(ip, username), l.maxFailures, l.window, l.cooldown, now)
	byIP := l.recordFailure(l.ipAttempts, strings.TrimSpace(ip), l.ipMaxFailures, l.ipWindow, l.ipCooldown, now)

	if byUser.IsZero() {
		return byIP, !byIP.IsZero()
	}
	if byIP.After(byUser) {
		return byIP, true
	}
	return byUser, true
}

// registerSuccess clears the failures a successful login proves stale: the
// (ip, username) record and the whole per-IP aggregate record. Clearing the
// aggregate record matters because a successful login shows the source is not a
// pure brute-forcer, so a shared egress (NAT / Cloudflare edge) is not punished
// for a neighbour's failures.
func (l *loginLimiter) registerSuccess(ip, username string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.attempts, loginLimitKey(ip, username))
	delete(l.ipAttempts, strings.TrimSpace(ip))
}

// checkBucket returns the active block deadline for key, or expires a finished
// cooldown and drops records whose failures have aged out of the window.
// Callers hold l.mu.
func (l *loginLimiter) checkBucket(records map[string]*loginLimitRecord, key string, window time.Duration, now time.Time) (time.Time, bool) {
	record := records[key]
	if record == nil {
		return time.Time{}, false
	}
	if now.Before(record.blockedUntil) {
		return record.blockedUntil, true
	}
	record.blockedUntil = time.Time{}
	record.failures = pruneLoginFailures(record.failures, now.Add(-window))
	if len(record.failures) == 0 {
		delete(records, key)
	}
	return time.Time{}, false
}

// recordFailure appends a failure to one bucket and starts the cooldown once the
// bucket's threshold is reached. Callers hold l.mu.
func (l *loginLimiter) recordFailure(records map[string]*loginLimitRecord, key string, maxFailures int, window, cooldown time.Duration, now time.Time) time.Time {
	record := records[key]
	if record == nil {
		l.evictForRoom(records, window, now)
		record = &loginLimitRecord{}
		records[key] = record
	}
	record.failures = pruneLoginFailures(record.failures, now.Add(-window))
	record.failures = append(record.failures, now)
	if len(record.failures) >= maxFailures {
		record.failures = nil
		record.blockedUntil = now.Add(cooldown)
		return record.blockedUntil
	}
	return time.Time{}
}

// evictForRoom keeps one bucket bounded before inserting a new record. It first
// reclaims records that are no longer blocked and whose failures have aged out
// of the window; if the map is still at the ceiling (a genuine broad flood), it
// drops one arbitrary record so memory can never grow past the cap.
// Callers hold l.mu.
func (l *loginLimiter) evictForRoom(records map[string]*loginLimitRecord, window time.Duration, now time.Time) {
	if len(records) < loginLimitMaxRecords {
		return
	}
	cutoff := now.Add(-window)
	for key, record := range records {
		if now.Before(record.blockedUntil) {
			continue
		}
		record.failures = pruneLoginFailures(record.failures, cutoff)
		if len(record.failures) == 0 {
			delete(records, key)
		}
	}
	if len(records) < loginLimitMaxRecords {
		return
	}
	for key, record := range records {
		if now.Before(record.blockedUntil) {
			continue
		}
		delete(records, key)
		return
	}
	for key := range records {
		delete(records, key)
		return
	}
}

func loginLimitKey(ip, username string) string {
	return strings.TrimSpace(ip) + "\x00" + strings.ToLower(strings.TrimSpace(username))
}

func pruneLoginFailures(failures []time.Time, cutoff time.Time) []time.Time {
	keepFrom := 0
	for keepFrom < len(failures) && failures[keepFrom].Before(cutoff) {
		keepFrom++
	}
	return failures[keepFrom:]
}