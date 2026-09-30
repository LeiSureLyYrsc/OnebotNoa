package api

import (
	"sync"
	"time"
)

// loginLimiter throttles failed logins per client IP (fixed window, in memory).
type loginLimiter struct {
	mu       sync.Mutex
	max      int
	window   time.Duration
	failures map[string][]time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	if max <= 0 {
		max = 8
	}
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &loginLimiter{max: max, window: window, failures: map[string][]time.Time{}}
}

// blocked reports whether key has exhausted its attempts, plus the wait time.
func (l *loginLimiter) blocked(key string) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	nowT := time.Now()
	kept := l.prune(key, nowT)
	if len(kept) < l.max {
		return 0, false
	}
	oldest := kept[0]
	wait := l.window - nowT.Sub(oldest)
	if wait < 0 {
		wait = 0
	}
	return wait, true
}

// record counts one failed attempt.
func (l *loginLimiter) record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	nowT := time.Now()
	l.failures[key] = append(l.prune(key, nowT), nowT)
}

// clear forgets the attempts of key after a successful login.
func (l *loginLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// prune drops attempts older than the window (caller holds the lock).
func (l *loginLimiter) prune(key string, nowT time.Time) []time.Time {
	cutoff := nowT.Add(-l.window)
	attempts := l.failures[key]
	kept := attempts[:0:0]
	for _, at := range attempts {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}
