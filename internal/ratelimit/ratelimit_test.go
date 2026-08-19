package ratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func do(t *testing.T, h http.Handler, xff string) int {
	t.Helper()
	req := httptest.NewRequest("POST", "/x", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr.Code
}

func TestMiddleware_AllowsBurstThen429(t *testing.T) {
	l := New(rate.Every(time.Hour), 3)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := range 3 {
		if code := do(t, h, ""); code != http.StatusOK {
			t.Fatalf("request %d: expected 200, got %d", i, code)
		}
	}
	if code := do(t, h, ""); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after burst, got %d", code)
	}
}

func TestMiddleware_TracksClientsSeparately(t *testing.T) {
	l := New(rate.Every(time.Hour), 1)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	if code := do(t, h, "1.1.1.1"); code != http.StatusOK {
		t.Fatalf("first client: expected 200, got %d", code)
	}
	if code := do(t, h, "1.1.1.1"); code != http.StatusTooManyRequests {
		t.Fatalf("first client over limit: expected 429, got %d", code)
	}
	if code := do(t, h, "2.2.2.2"); code != http.StatusOK {
		t.Fatalf("second client must have its own bucket, got %d", code)
	}
}

// Behind the tunnel, RemoteAddr is loopback for everyone; the last
// X-Forwarded-For entry (appended by cloudflared) must be the bucket key, not
// attacker-controllable earlier entries.
func TestMiddleware_UsesLastXForwardedFor(t *testing.T) {
	l := New(rate.Every(time.Hour), 1)
	h := l.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	if code := do(t, h, "9.9.9.9, 1.1.1.1"); code != http.StatusOK {
		t.Fatalf("expected 200, got %d", code)
	}
	// Same real client (last entry) even though the spoofed prefix differs.
	if code := do(t, h, "8.8.8.8, 1.1.1.1"); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for same real client, got %d", code)
	}
}
