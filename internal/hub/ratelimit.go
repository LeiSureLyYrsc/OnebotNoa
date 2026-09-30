package hub

import (
	"math"
	"sync"
	"time"

	"github.com/LeiSureLyYrsc/OnebotNoa/internal/config"
)

// bucket is a classic token bucket: rate tokens per second with a burst cap.
// One token is one forwarded action.
type bucket struct {
	mu       sync.Mutex
	rate     float64
	burst    float64
	tokens   float64
	last     time.Time
	lastUsed time.Time
}

func newBucket(rate float64, burst int, now time.Time) *bucket {
	if burst <= 0 {
		burst = 1
	}
	return &bucket{
		rate:     rate,
		burst:    float64(burst),
		tokens:   float64(burst),
		last:     now,
		lastUsed: now,
	}
}

// take consumes one token, reporting how long the caller should wait otherwise.
func (b *bucket) take(now time.Time) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lastUsed = now
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(b.burst, b.tokens+elapsed*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	if b.rate <= 0 {
		return false, time.Hour
	}
	wait := time.Duration((1 - b.tokens) / b.rate * float64(time.Second))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return false, wait
}

// Limiter enforces three independent protections:
//
//  1. the account-level global bucket - several Bots sharing one QQ account must
//     not exceed what a single direct connection would have sent (anti-ban);
//  2. the per-Bot per-account bucket - one greedy Bot cannot starve the others;
//  3. a per-Bot in-flight cap - a Bot that fires actions faster than the
//     upstream answers cannot grow the pending table without bound.
//
// Read-only actions (get_*, can_*) bypass the buckets: they neither send
// messages nor risk a flood ban.
type Limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	inflight map[string]int

	account   config.RateLimit
	bot       config.RateLimit
	now       func() time.Time
	lastSweep time.Time

	limAccount   uint64
	limBot       uint64
	limInflight  uint64
}

// NewLimiter builds a limiter from policy config.
func NewLimiter(policy config.Policy) *Limiter {
	return &Limiter{
		buckets:  map[string]*bucket{},
		inflight: map[string]int{},
		account:  policy.PerAccountRate,
		bot:      policy.PerBotRate,
		now:      time.Now,
	}
}

// ReadOnlyAction reports whether an action only reads state.
func ReadOnlyAction(action string) bool {
	switch {
	case action == "":
		return false
	case action == "get_status", action == "get_version_info", action == "get_login_info",
		action == "can_send_image", action == "can_send_record":
		return true
	}
	trimmed := action
	for len(trimmed) > 0 && trimmed[0] == '.' {
		trimmed = trimmed[1:]
	}
	for _, prefix := range []string{"get_", "can_", "hub_", "x_hub."} {
		if len(trimmed) >= len(prefix) && trimmed[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

// AllowAccount checks the account-wide bucket.
func (l *Limiter) AllowAccount(selfID string) (bool, time.Duration) {
	if l.account.Rate <= 0 {
		return true, 0
	}
	return l.take("account:"+selfID, l.account)
}

// AllowBot checks the per-Bot, per-account bucket.
func (l *Limiter) AllowBot(botName, selfID string) (bool, time.Duration) {
	if l.bot.Rate <= 0 {
		return true, 0
	}
	return l.take("bot:"+botName+"->"+selfID, l.bot)
}

func (l *Limiter) take(key string, cfg config.RateLimit) (bool, time.Duration) {
	now := l.now()

	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = newBucket(cfg.Rate, cfg.Burst, now)
		l.buckets[key] = b
	}
	l.mu.Unlock()

	allowed, wait := b.take(now)
	if !allowed {
		l.mu.Lock()
		if len(key) >= 4 && key[:4] == "bot:" {
			l.limBot++
		} else {
			l.limAccount++
		}
		l.mu.Unlock()
	}
	return allowed, wait
}

// Acquire reserves an in-flight slot for a Bot. The caller must Release it when
// the action finishes (the pending table does that exactly once).
func (l *Limiter) Acquire(botName string) bool {
	if l.bot.Concurrency <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight[botName] >= l.bot.Concurrency {
		l.limInflight++
		return false
	}
	l.inflight[botName]++
	return true
}

// Release gives back an in-flight slot (safe to call for a slot never taken
// when the cap is disabled).
func (l *Limiter) Release(botName string) {
	if l.bot.Concurrency <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if n := l.inflight[botName]; n > 1 {
		l.inflight[botName] = n - 1
	} else {
		delete(l.inflight, botName)
	}
}

// Inflight reports the current in-flight count of a Bot.
func (l *Limiter) Inflight(botName string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight[botName]
}

// Counters reports how many actions each protection refused.
func (l *Limiter) Counters() (account, bot, inflight uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limAccount, l.limBot, l.limInflight
}

// Sweep drops buckets that have been idle for a while so the map cannot grow
// without bound on a long-running hub.
func (l *Limiter) Sweep(maxIdle time.Duration) int {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	removed := 0
	for key, b := range l.buckets {
		b.mu.Lock()
		idle := now.Sub(b.lastUsed)
		b.mu.Unlock()
		if idle > maxIdle {
			delete(l.buckets, key)
			removed++
		}
	}
	l.lastSweep = now
	return removed
}

// Buckets reports how many buckets are currently tracked.
func (l *Limiter) Buckets() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}
