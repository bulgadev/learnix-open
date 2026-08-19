package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"learnix/internal/db"
)

// withUser injects a user into the request context the way Middleware does.
func withUser(u *db.User, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), userKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func TestAdminMiddleware(t *testing.T) {
	user := &db.User{ID: 1, Email: "boss@example.com"}
	other := &db.User{ID: 2, Email: "pleb@example.com"}

	cases := []struct {
		name       string
		adminEmail string
		user       *db.User
		want       int
	}{
		{"admin matches", "boss@example.com", user, http.StatusOK},
		{"case-insensitive match", "BOSS@example.com", user, http.StatusOK},
		{"non-admin is 404", "boss@example.com", other, http.StatusNotFound},
		{"no user is 404", "boss@example.com", nil, http.StatusNotFound},
		{"empty admin email disables panel", "", user, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := AdminMiddleware(tc.adminEmail, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			h = withUser(tc.user, h)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest("GET", "/admin", nil))
			if rr.Code != tc.want {
				t.Errorf("got %d, want %d", rr.Code, tc.want)
			}
		})
	}
}

func TestCSRFToken_RoundTrip(t *testing.T) {
	secret := "test-secret"
	sid := NewSessionID()
	cookie := &http.Cookie{Name: CookieName, Value: Sign(sid, secret)}

	newReq := func(form string) *http.Request {
		req := httptest.NewRequest("POST", "/admin/users/1/quota", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		return req
	}

	tok := CSRFToken(newReq(""), secret)
	if tok == "" {
		t.Fatal("expected a token for a valid session cookie")
	}
	// Stable for the whole session.
	if again := CSRFToken(newReq(""), secret); again != tok {
		t.Error("token must be stable for one session")
	}

	if CSRFValid(newReq(""), secret) {
		t.Error("request without the csrf form value must fail")
	}
	if !CSRFValid(newReq("csrf="+url.QueryEscape(tok)), secret) {
		t.Error("valid token must be accepted")
	}
	if CSRFValid(newReq("csrf=bogus"), secret) {
		t.Error("wrong token must be rejected")
	}
}

func TestCSRFToken_ForgedOrMissingCookie(t *testing.T) {
	secret := "test-secret"

	// No cookie at all.
	req := httptest.NewRequest("POST", "/", nil)
	if tok := CSRFToken(req, secret); tok != "" {
		t.Errorf("no cookie must yield no token, got %q", tok)
	}
	if CSRFValid(req, secret) {
		t.Error("no cookie must never validate")
	}

	// Forged cookie (wrong secret) yields no token.
	sid := NewSessionID()
	req.AddCookie(&http.Cookie{Name: CookieName, Value: Sign(sid, "other-secret")})
	if tok := CSRFToken(req, secret); tok != "" {
		t.Errorf("forged cookie must yield no token, got %q", tok)
	}
}

// Tokens are bound to the session: another session's token must not validate.
func TestCSRFToken_SessionBound(t *testing.T) {
	secret := "test-secret"
	reqA := httptest.NewRequest("POST", "/", nil)
	reqA.AddCookie(&http.Cookie{Name: CookieName, Value: Sign(NewSessionID(), secret)})

	tokA := CSRFToken(reqA, secret)
	reqB := httptest.NewRequest("POST", "/", strings.NewReader("csrf="+url.QueryEscape(tokA)))
	reqB.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	reqB.AddCookie(&http.Cookie{Name: CookieName, Value: Sign(NewSessionID(), secret)})
	if CSRFValid(reqB, secret) {
		t.Error("session A's token must not validate session B's request")
	}
}
