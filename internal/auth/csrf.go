package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
)

// CSRFToken derives a per-session CSRF token from the signed session cookie:
// HMAC-SHA256(secret, sessionID || ":csrf"). It is stateless (no DB column),
// unforgeable without the server secret, bound to one session, and stable for
// the whole session so server-rendered forms can embed it. Returns "" when
// the cookie is missing or invalid.
func CSRFToken(r *http.Request, secret string) string {
	sid, ok := SessionID(r, secret)
	if !ok {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(sid + ":csrf"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// CSRFValid reports whether the request's "csrf" form value matches the
// token derived from the caller's session cookie (constant-time compare).
// The caller must have parsed the form already.
func CSRFValid(r *http.Request, secret string) bool {
	want := CSRFToken(r, secret)
	if want == "" {
		return false
	}
	return hmac.Equal([]byte(r.FormValue("csrf")), []byte(want))
}
