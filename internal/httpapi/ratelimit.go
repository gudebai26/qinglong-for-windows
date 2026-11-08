package httpapi

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"qinglong-go/internal/auth"
)

// tooManyRequestsBody is express-rate-limit's default message. It is served as
// text/html, which is what Express does for a plain string body.
const tooManyRequestsBody = "Too many requests, please try again later."

// RateLimiter reproduces express-rate-limit v7 with its default settings: a
// fixed window per client IP, X-RateLimit-* headers and a 429 text response.
//
// It is a different mechanism from auth.LoginThrottle: this one caps request
// volume (100 login attempts per 15 minutes), the throttle backs off after
// consecutive wrong passwords (410 + data seconds).
type RateLimiter struct {
	limit      int
	window     time.Duration
	trustProxy string

	mu      sync.Mutex
	buckets map[string]*rateBucket
}

type rateBucket struct {
	count int
	reset time.Time
}

// NewRateLimiter builds a limiter for one route.
func NewRateLimiter(limit int, window time.Duration, trustProxy string) *RateLimiter {
	return &RateLimiter{
		limit:      limit,
		window:     window,
		trustProxy: trustProxy,
		buckets:    map[string]*rateBucket{},
	}
}

// Handler wraps next with the limiter.
func (l *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if l == nil {
			next.ServeHTTP(w, r)
			return
		}
		now := time.Now()
		key := auth.ClientIP(r, l.trustProxy)

		l.mu.Lock()
		bucket, ok := l.buckets[key]
		if !ok || now.After(bucket.reset) {
			bucket = &rateBucket{reset: now.Add(l.window)}
			l.buckets[key] = bucket
		}
		bucket.count++
		count := bucket.count
		reset := bucket.reset
		if len(l.buckets) > 10000 {
			for candidate, entry := range l.buckets {
				if now.After(entry.reset) {
					delete(l.buckets, candidate)
				}
			}
		}
		l.mu.Unlock()

		remaining := l.limit - count
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("X-RateLimit-Limit", strconv.Itoa(l.limit))
		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))

		if count > l.limit {
			retryAfter := int(math.Ceil(reset.Sub(now).Seconds()))
			if retryAfter < 0 {
				retryAfter = 0
			}
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(tooManyRequestsBody))
			return
		}
		next.ServeHTTP(w, r)
	})
}
