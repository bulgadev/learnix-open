// Package ratelimit provides a small per-client token-bucket HTTP middleware
// used to protect expensive or abuse-prone endpoints (login, register, quiz
// generation, chat streaming) from scripted hammering.
package ratelimit

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter hands out one token bucket per client IP.
type Limiter struct {
	mu       sync.Mutex
	visitors map[string]*visitor
	rate     rate.Limit
	burst    int
}

type visitor struct {
	limiter *rate.Limiter
	last    time.Time
}

// New returns a Limiter that allows burst requests up front and then refills
// at r. For example New(rate.Every(10*time.Second), 5) allows 5 requests
// immediately, then one every 10 seconds.
func New(r rate.Limit, burst int) *Limiter {
	return &Limiter{
		visitors: make(map[string]*visitor),
		rate:     r,
		burst:    burst,
	}
}

func (l *Limiter) get(key string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Lazy sweep so the map cannot grow without bound.
	if len(l.visitors) > 10000 {
		cutoff := time.Now().Add(-15 * time.Minute)
		for k, v := range l.visitors {
			if v.last.Before(cutoff) {
				delete(l.visitors, k)
			}
		}
	}
	v, ok := l.visitors[key]
	if !ok {
		v = &visitor{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.visitors[key] = v
	}
	v.last = time.Now()
	return v.limiter
}

// Middleware returns 429 when the client exceeds the limit, else calls next.
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.get(clientIP(r)).Allow() {
			http.Error(w, "Muitas requisições. Tente novamente em instantes.", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP derives the client address. Behind the Cloudflare tunnel every
// request arrives from loopback, so the last X-Forwarded-For entry (the one
// cloudflared appends) is preferred when present; earlier entries are
// attacker-controllable.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if ip := strings.TrimSpace(parts[len(parts)-1]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
