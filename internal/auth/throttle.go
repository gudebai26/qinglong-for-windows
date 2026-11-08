package auth

import (
	"math"
	"sync"
	"time"
)

// LoginThrottle mirrors back/shared/loginThrottle.ts: a bounded, per-source
// backoff that is not persisted and does not lock the account itself.
const (
	throttleTTL        = 15 * time.Minute
	throttleMaxSources = 10000
	throttleMaxSeconds = 5 * 60
)

type throttleState struct {
	failures    int
	lastFailure int64 // unix milliseconds
}

// LoginThrottle tracks consecutive failures per client IP.
type LoginThrottle struct {
	mu      sync.Mutex
	sources map[string]throttleState
}

// NewLoginThrottle returns an empty throttle.
func NewLoginThrottle() *LoginThrottle {
	return &LoginThrottle{sources: map[string]throttleState{}}
}

func (l *LoginThrottle) get(source string, now int64) (throttleState, bool) {
	state, ok := l.sources[source]
	if !ok {
		return throttleState{}, false
	}
	if now-state.lastFailure >= throttleTTL.Milliseconds() {
		delete(l.sources, source)
		return throttleState{}, false
	}
	return state, true
}

// RetryAfter returns the remaining backoff in seconds (0 when not throttled).
func (l *LoginThrottle) RetryAfter(source string, nowMillis int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	state, ok := l.get(source, nowMillis)
	if !ok || state.failures < 3 {
		return 0
	}
	seconds := math.Min(math.Pow(3, float64(state.failures)), throttleMaxSeconds)
	remaining := (seconds*1000 - float64(nowMillis-state.lastFailure)) / 1000
	if remaining <= 0 {
		return 0
	}
	return int(math.Ceil(remaining))
}

// Fail records a failure and returns the new counter (capped at 6).
func (l *LoginThrottle) Fail(source string, nowMillis int64) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	failures := 1
	if state, ok := l.get(source, nowMillis); ok {
		failures = state.failures + 1
	}
	if failures > 6 {
		failures = 6
	}
	delete(l.sources, source)
	if len(l.sources) >= throttleMaxSources {
		for oldest := range l.sources {
			delete(l.sources, oldest)
			break
		}
	}
	l.sources[source] = throttleState{failures: failures, lastFailure: nowMillis}
	return failures
}

// Reset clears the backoff for a source after a successful login.
func (l *LoginThrottle) Reset(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.sources, source)
}

// Clear drops every tracked source (used by the system auth reset endpoint).
func (l *LoginThrottle) Clear() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sources = map[string]throttleState{}
}
