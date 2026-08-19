package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"learnix/internal/db"
)

func sessionCookie(t *testing.T, host string) (secure bool) {
	t.Helper()
	req := httptest.NewRequest("GET", "http://"+host+"/", nil)
	rr := httptest.NewRecorder()
	SetSessionCookie(rr, req, "secret", "sid")
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if cookies[0].Name != CookieName {
		t.Fatalf("unexpected cookie name %s", cookies[0].Name)
	}
	if !cookies[0].HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}
	return cookies[0].Secure
}

// H3: the session cookie must be Secure for any real (tunneled/public) host,
// and only plain for loopback local development.
func TestSetSessionCookie_SecureFlag(t *testing.T) {
	for _, host := range []string{"learnix.bulga.top", "example.com", "192.168.0.10:8080"} {
		if !sessionCookie(t, host) {
			t.Errorf("host %s: expected Secure cookie", host)
		}
	}
	for _, host := range []string{"localhost", "localhost:8080", "127.0.0.1:8080"} {
		if sessionCookie(t, host) {
			t.Errorf("host %s: expected non-Secure cookie for local dev", host)
		}
	}
}

func TestSessionID_AcceptsLegacyCookie(t *testing.T) {
	req := httptest.NewRequest("GET", "https://learnix.bulga.top/", nil)
	req.AddCookie(&http.Cookie{Name: LegacyCookieName, Value: Sign("sid", "secret")})
	if sid, ok := SessionID(req, "secret"); !ok || sid != "sid" {
		t.Fatalf("legacy session cookie was not accepted: %q %v", sid, ok)
	}
}

func TestCanonicalHostMiddleware_MigratesSession(t *testing.T) {
	database, cleanup := db.NewTestDB(t)
	defer cleanup()
	users := db.NewUserRepo(database)
	sessions := db.NewSessionRepo(database)
	uid, err := users.Create(context.Background(), "migrate@test.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	sid := NewSessionID()
	if err := sessions.Create(context.Background(), sid, uid); err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("legacy host should redirect before reaching the route")
	})
	h := CanonicalHostMiddleware(CanonicalHost, LegacyHost, "secret", sessions)(next)
	req := httptest.NewRequest("GET", "https://"+LegacyHost+"/studies?from=old", nil)
	req.AddCookie(&http.Cookie{Name: LegacyCookieName, Value: Sign(sid, "secret")})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "https://"+CanonicalHost+"/studies?from=old" {
		t.Fatalf("unexpected canonical redirect: %d %s", rr.Code, rr.Header().Get("Location"))
	}
	var migrated *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == CookieName {
			migrated = c
		}
	}
	if migrated == nil || migrated.Domain != CookieDomain || !strings.Contains(migrated.Value, ".") {
		t.Fatalf("missing domain-scoped migrated cookie: %+v", migrated)
	}
}
