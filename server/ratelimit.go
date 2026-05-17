package server

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	loginRateWindow = time.Minute
	loginRateLimit  = 10 // max failed attempts per IP per window before lockout
)

// loginEntry tracks failed login attempts for a single IP address within a
// sliding time window.
type loginEntry struct {
	failures  int
	windowEnd time.Time
}

// rateLimiter counts failed login attempts per IP and blocks requests that
// exceed the threshold. It is safe for concurrent use.
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*loginEntry
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{entries: make(map[string]*loginEntry)}
}

// allow returns true when the IP has not exceeded the failure threshold.
// It also evicts stale entries from the map to bound memory use.
func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Evict all windows that have expired.
	for k, e := range rl.entries {
		if now.After(e.windowEnd) {
			delete(rl.entries, k)
		}
	}

	e, ok := rl.entries[ip]
	if !ok {
		return true // no recorded failures in the current window
	}

	return e.failures < loginRateLimit
}

// recordFailure increments the failure counter for the IP. If no window is
// active for this IP a fresh one is started.
func (rl *rateLimiter) recordFailure(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	e, ok := rl.entries[ip]
	if !ok || now.After(e.windowEnd) {
		rl.entries[ip] = &loginEntry{failures: 1, windowEnd: now.Add(loginRateWindow)}

		return
	}

	e.failures++
}

// clear removes any failure record for the IP. Called on successful login so
// that a user who previously had failures is not penalised for past mistakes.
func (rl *rateLimiter) clear(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	delete(rl.entries, ip)
}

// clientIP extracts the remote host from the request's RemoteAddr field,
// stripping the port. It uses RemoteAddr (not X-Forwarded-For) because
// idtrack is a directly-exposed server — trusting a forwarded header would
// allow clients to spoof their IP and bypass rate limiting.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}
