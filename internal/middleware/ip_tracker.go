package middleware

import (
	"sync"
)

// IPConnectionTracker tracks concurrent WebSocket connections per IP address.
type IPConnectionTracker struct {
	mu     sync.Mutex
	counts map[string]int
	max    int
}

// NewIPConnectionTracker constructs a tracker with a max connections cap per IP.
func NewIPConnectionTracker(max int) *IPConnectionTracker {
	return &IPConnectionTracker{
		counts: make(map[string]int),
		max:    max,
	}
}

// Acquire attempts to increment the connection count for an IP.
// Returns false if the IP has reached the concurrent limit.
func (t *IPConnectionTracker) Acquire(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	current := t.counts[ip]
	if current >= t.max {
		return false
	}

	t.counts[ip] = current + 1
	return true
}

// Release decrements the connection count for an IP when a socket closes.
func (t *IPConnectionTracker) Release(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	current := t.counts[ip]
	if current <= 1 {
		delete(t.counts, ip)
	} else {
		t.counts[ip] = current - 1
	}
}

// Count returns the active connection count for an IP.
func (t *IPConnectionTracker) Count(ip string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[ip]
}
