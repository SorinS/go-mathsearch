package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"sorins/mathsearch/config"
)

// RateLimiter is a per-identity token-bucket limiter. Requests are keyed by the
// authenticated subject when present, otherwise by client IP. Privileged
// callers may be exempted. It is intended to sit behind Annotate so the caller
// role is known.
type RateLimiter struct {
	limit    rate.Limit
	burst    int
	exempt   bool
	mu       sync.Mutex
	buckets  map[string]*bucket
	lastGC   time.Time
	nowFunc  func() time.Time
	pathPref string // only limit requests under this prefix ("" = all)
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimiter builds a limiter from config. It limits only requests whose
// path starts with pathPrefix (e.g. "/api/").
func NewRateLimiter(c config.RateLimit, pathPrefix string) *RateLimiter {
	rpm := c.RequestsPerMinute
	if rpm <= 0 {
		rpm = 60
	}
	burst := c.Burst
	if burst <= 0 {
		burst = rpm
	}
	return &RateLimiter{
		limit:    rate.Limit(float64(rpm) / 60.0),
		burst:    burst,
		exempt:   c.PrivilegedExempt,
		buckets:  make(map[string]*bucket),
		nowFunc:  time.Now,
		pathPref: pathPrefix,
	}
}

// Middleware applies the limiter, returning 429 when a caller exceeds its rate.
func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rl.pathPref != "" && !strings.HasPrefix(r.URL.Path, rl.pathPref) {
			next.ServeHTTP(w, r)
			return
		}
		_, role, authed := PrincipalFrom(r.Context())
		if authed && role == RolePrivileged && rl.exempt {
			next.ServeHTTP(w, r)
			return
		}
		key := "ip:" + clientIP(r)
		if sub, _, ok := PrincipalFrom(r.Context()); ok {
			key = "user:" + sub
		}
		if !rl.bucketFor(key).Allow() {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) bucketFor(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := rl.nowFunc()
	if now.Sub(rl.lastGC) > 10*time.Minute {
		for k, b := range rl.buckets {
			if now.Sub(b.seen) > 10*time.Minute {
				delete(rl.buckets, k)
			}
		}
		rl.lastGC = now
	}
	b, ok := rl.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(rl.limit, rl.burst)}
		rl.buckets[key] = b
	}
	b.seen = now
	return b.lim
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
