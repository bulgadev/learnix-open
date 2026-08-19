package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"strings"

	"learnix/internal/db"
)

const (
	// CookieName is the current session cookie name.
	CookieName = "learnix_sid"
	// LegacyCookieName is accepted during the Twstix → Learnix migration.
	LegacyCookieName = "twstix_sid"
	// CanonicalHost is the public Learnix hostname.
	CanonicalHost = "learnix.bulga.top"
	// LegacyHost remains available during the hostname migration.
	LegacyHost = "twstix.bulga.top"
	// CookieDomain allows a valid legacy session to cross from the old host to
	// the new subdomain during the one-time migration redirect.
	CookieDomain = "bulga.top"
)

type ctxKey string

const userKey ctxKey = "user"

// Sign produces an HMAC-SHA256-signed cookie value: "<sig>.<value>".
func Sign(value, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return sig + "." + value
}

// Verify validates a signed value and returns the inner value, or ok=false.
func Verify(signed, secret string) (value string, ok bool) {
	idx := strings.IndexByte(signed, '.')
	if idx <= 0 {
		return "", false
	}
	sig := signed[:idx]
	value = signed[idx+1:]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return "", false
	}
	return value, true
}

func requestHost(r *http.Request) string {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(host)
}

// SessionID verifies whichever session cookie is valid, preferring the
// current Learnix cookie over the legacy Twstix cookie.
func SessionID(r *http.Request, secret string) (string, bool) {
	if c, err := r.Cookie(CookieName); err == nil {
		if sid, ok := Verify(c.Value, secret); ok {
			return sid, true
		}
	}
	if c, err := r.Cookie(LegacyCookieName); err == nil {
		return Verify(c.Value, secret)
	}
	return "", false
}

func hasLegacyCookie(r *http.Request, secret string) bool {
	if c, err := r.Cookie(CookieName); err == nil {
		if _, ok := Verify(c.Value, secret); ok {
			return false
		}
	}
	if c, err := r.Cookie(LegacyCookieName); err == nil {
		_, ok := Verify(c.Value, secret)
		return ok
	}
	return false
}

// CanonicalHostMiddleware redirects the old public hostname to Learnix. A
// valid legacy session is copied into a domain cookie before redirecting so
// the browser can send it to the new subdomain.
func CanonicalHostMiddleware(canonicalHost, legacyHost, secret string, sessions *db.SessionRepo) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requestHost(r) != strings.ToLower(legacyHost) {
				next.ServeHTTP(w, r)
				return
			}
			if sid, ok := SessionID(r, secret); ok {
				if row, err := sessions.Get(r.Context(), sid); err == nil && row != nil {
					setSessionCookie(w, r, secret, sid, CookieDomain)
				}
			}
			http.Redirect(w, r, "https://"+canonicalHost+r.URL.RequestURI(), http.StatusFound)
		})
	}
}

// NewSessionID returns a random 32-byte hex session id.
func NewSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Middleware verifies the signed session cookie, looks up the session row and
// the owning user, stashes *db.User in the request context, and calls next.
// On any failure it redirects to /login.
func Middleware(secret string, sessions *db.SessionRepo, users *db.UserRepo, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, ok := SessionID(r, secret)
		if !ok {
			redirectLogin(w, r)
			return
		}
		sRow, err := sessions.Get(r.Context(), value)
		if err != nil || sRow == nil {
			redirectLogin(w, r)
			return
		}
		u, err := users.ByID(r.Context(), sRow.UserID)
		if err != nil || u == nil {
			redirectLogin(w, r)
			return
		}
		if hasLegacyCookie(r, secret) {
			SetSessionCookie(w, r, secret, value)
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// OptionalMiddleware attaches the authenticated user when the session is
// valid, but leaves public visitors in the request without redirecting them.
// Public profile pages use it to distinguish the owner without making the
// profile itself private.
func OptionalMiddleware(secret string, sessions *db.SessionRepo, users *db.UserRepo, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value, ok := SessionID(r, secret)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		sRow, err := sessions.Get(r.Context(), value)
		if err != nil || sRow == nil {
			next.ServeHTTP(w, r)
			return
		}
		u, err := users.ByID(r.Context(), sRow.UserID)
		if err != nil || u == nil {
			next.ServeHTTP(w, r)
			return
		}
		if hasLegacyCookie(r, secret) {
			SetSessionCookie(w, r, secret, value)
		}
		ctx := context.WithValue(r.Context(), userKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func redirectLogin(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/login", http.StatusFound)
}

// UserFromContext returns the authenticated user, or nil.
func UserFromContext(ctx context.Context) *db.User {
	if v, ok := ctx.Value(userKey).(*db.User); ok {
		return v
	}
	return nil
}

// cookieSecure reports whether the session cookie should carry the Secure
// flag. It is set for every host except loopback names, so plain-HTTP local
// development keeps working while any real deployment (tunnel, LAN IP,
// domain) only ever transmits the cookie over HTTPS.
func cookieSecure(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return false
	}
	return true
}

// SetSessionCookie sets the signed session cookie on the response. Its
// lifetime matches the server-side session TTL.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, secret, sessionID string) {
	setSessionCookie(w, r, secret, sessionID, "")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, secret, sessionID, domain string) {
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    Sign(sessionID, secret),
		Path:     "/",
		HttpOnly: true,
		Secure:   cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(db.SessionTTL.Seconds()),
		Domain:   domain,
	})
}

// ClearSessionCookie expires the session cookie.
func ClearSessionCookie(w http.ResponseWriter, r *http.Request) {
	for _, name := range []string{CookieName, LegacyCookieName} {
		for _, domain := range []string{"", CookieDomain} {
			http.SetCookie(w, &http.Cookie{
				Name:     name,
				Value:    "",
				Path:     "/",
				Domain:   domain,
				HttpOnly: true,
				Secure:   cookieSecure(r),
				SameSite: http.SameSiteLaxMode,
				MaxAge:   -1,
			})
		}
	}
}
