package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRateLimiter_AllowsInitially(t *testing.T) {
	rl := newRateLimiter()

	if !rl.allow("1.2.3.4") {
		t.Error("fresh IP should be allowed")
	}
}

func TestRateLimiter_BlocksAfterLimit(t *testing.T) {
	rl := newRateLimiter()

	ip := "10.0.0.1"

	// Record loginRateLimit failures — the (limit+1)th attempt should be blocked.
	for i := 0; i < loginRateLimit; i++ {
		rl.recordFailure(ip)
	}

	if rl.allow(ip) {
		t.Errorf("IP should be blocked after %d failures", loginRateLimit)
	}
}

func TestRateLimiter_AllowedBelowLimit(t *testing.T) {
	rl := newRateLimiter()

	ip := "10.0.0.2"

	for i := 0; i < loginRateLimit-1; i++ {
		rl.recordFailure(ip)
	}

	if !rl.allow(ip) {
		t.Errorf("IP should be allowed when below limit (%d failures)", loginRateLimit-1)
	}
}

func TestRateLimiter_ClearResetsCounter(t *testing.T) {
	rl := newRateLimiter()

	ip := "10.0.0.3"

	for i := 0; i < loginRateLimit; i++ {
		rl.recordFailure(ip)
	}

	rl.clear(ip)

	if !rl.allow(ip) {
		t.Error("IP should be allowed after clear")
	}
}

func TestRateLimiter_ClearNonexistent(t *testing.T) {
	rl := newRateLimiter()

	// Should not panic.
	rl.clear("not-seen-before")
}

func TestRateLimiter_DifferentIPsIndependent(t *testing.T) {
	rl := newRateLimiter()

	ip1, ip2 := "192.168.1.1", "192.168.1.2"

	for i := 0; i < loginRateLimit; i++ {
		rl.recordFailure(ip1)
	}

	if !rl.allow(ip2) {
		t.Error("ip2 should not be blocked by ip1's failures")
	}
}

func TestClientIP_WithPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "1.2.3.4:5678"

	ip := clientIP(r)
	if ip != "1.2.3.4" {
		t.Errorf("clientIP: got %q, want %q", ip, "1.2.3.4")
	}
}

func TestClientIP_NoPort(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "1.2.3.4"

	ip := clientIP(r)
	// net.SplitHostPort will fail; fallback is the raw RemoteAddr.
	if ip != "1.2.3.4" {
		t.Errorf("clientIP fallback: got %q, want %q", ip, "1.2.3.4")
	}
}

func TestClientIP_IPv6(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "[::1]:9000"

	ip := clientIP(r)
	if ip != "::1" {
		t.Errorf("clientIP IPv6: got %q, want %q", ip, "::1")
	}
}
