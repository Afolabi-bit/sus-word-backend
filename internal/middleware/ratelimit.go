package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ipEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// IPRateLimiter provides per-IP token bucket rate limiting.
type IPRateLimiter struct {
	mu      sync.Mutex
	ips     map[string]*ipEntry
	rate    rate.Limit
	burst   int
	cleanup time.Duration
}

// NewIPRateLimiter creates an IP rate limiter.
func NewIPRateLimiter(r rate.Limit, burst int) *IPRateLimiter {
	limiter := &IPRateLimiter{
		ips:     make(map[string]*ipEntry),
		rate:    r,
		burst:   burst,
		cleanup: 10 * time.Minute,
	}

	go limiter.startCleanup()
	return limiter
}

func (l *IPRateLimiter) getLimiter(ip string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, exists := l.ips[ip]
	if !exists {
		entry = &ipEntry{
			limiter:  rate.NewLimiter(l.rate, l.burst),
			lastSeen: time.Now(),
		}
		l.ips[ip] = entry
	} else {
		entry.lastSeen = time.Now()
	}

	return entry.limiter
}

func (l *IPRateLimiter) startCleanup() {
	ticker := time.NewTicker(l.cleanup)
	for range ticker.C {
		l.mu.Lock()
		now := time.Now()
		for ip, entry := range l.ips {
			if now.Sub(entry.lastSeen) > 30*time.Minute {
				delete(l.ips, ip)
			}
		}
		l.mu.Unlock()
	}
}

// LimitMiddleware wraps an http.Handler with rate limiting.
func (l *IPRateLimiter) LimitMiddleware(errMsg string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ExtractIP(r)
			limiter := l.getLimiter(ip)

			if !limiter.Allow() {
				http.Error(w, errMsg, http.StatusTooManyRequests)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func ExtractIP(r *http.Request) string {
	if xRealIP := r.Header.Get("X-Real-IP"); xRealIP != "" {
		return strings.TrimSpace(xRealIP)
	}
	if xForwardedFor := r.Header.Get("X-Forwarded-For"); xForwardedFor != "" {
		parts := strings.Split(xForwardedFor, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
