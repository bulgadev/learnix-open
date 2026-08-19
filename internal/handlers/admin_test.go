package handlers

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// registerAdmin registers the configured admin account and returns its cookie.
func (te *testEnv) registerAdmin(t *testing.T) *http.Cookie {
	t.Helper()
	return te.register(t, adminTestEmail, "hunter2!")
}

func TestAdmin_Unauthenticated_Redirects(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "GET", "/admin", "")
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/login" {
		t.Errorf("expected redirect to /login, got %d %s", rr.Code, rr.Header().Get("Location"))
	}
}

// Non-admins get 404 on every admin route — the panel must not leak.
func TestAdmin_NonAdmin_404(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "pleb@test.com", "hunter2!")

	if rr := te.req(t, "GET", "/admin", "", cookie); rr.Code != http.StatusNotFound {
		t.Errorf("GET /admin as non-admin: expected 404, got %d", rr.Code)
	}
	csrf := te.csrfToken(t, cookie)
	rr := te.req(t, "POST", "/admin/users/1/quota", "csrf="+csrf+"&quota=100", cookie)
	if rr.Code != http.StatusNotFound {
		t.Errorf("POST quota as non-admin: expected 404, got %d", rr.Code)
	}
}

func TestAdmin_Dashboard_ListsUsers(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	te.register(t, "other@test.com", "hunter2!")

	rr := te.req(t, "GET", "/admin", "", admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	for _, email := range []string{adminTestEmail, "other@test.com"} {
		if !strings.Contains(body, email) {
			t.Errorf("dashboard should list %s", email)
		}
	}
	if !strings.Contains(body, "250.000") {
		t.Error("new users should be listed with the initial 250,000-token quota")
	}
}

func TestAdmin_SetQuota(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	te.register(t, "target@test.com", "hunter2!")
	u, _ := te.users.ByEmail(testCtx, "target@test.com")

	csrf := te.csrfToken(t, admin)
	rr := te.req(t, "POST", fmt.Sprintf("/admin/users/%d/quota", u.ID), "csrf="+csrf+"&quota=5000", admin)
	if rr.Code != http.StatusSeeOther || !strings.Contains(rr.Header().Get("Location"), "quota-saved") {
		t.Fatalf("expected 303 quota-saved, got %d %s", rr.Code, rr.Header().Get("Location"))
	}
	q, _ := te.quotas.Get(testCtx, u.ID)
	if q == nil || q.Quota != 5000 {
		t.Errorf("quota not saved: %+v", q)
	}
}

func TestAdmin_SetQuota_CSRFRequired(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	te.register(t, "target2@test.com", "hunter2!")
	u, _ := te.users.ByEmail(testCtx, "target2@test.com")

	rr := te.req(t, "POST", fmt.Sprintf("/admin/users/%d/quota", u.ID), "quota=5000", admin)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing CSRF: expected 400, got %d", rr.Code)
	}
	rr = te.req(t, "POST", fmt.Sprintf("/admin/users/%d/quota", u.ID), "csrf=bogus&quota=5000", admin)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("wrong CSRF: expected 400, got %d", rr.Code)
	}
	if q, _ := te.quotas.Get(testCtx, u.ID); q == nil || q.Quota != 250000 || q.Used != 0 {
		t.Errorf("quota must stay untouched on CSRF failure: %+v", q)
	}
}

func TestAdmin_SetQuota_InvalidValue(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	te.register(t, "target3@test.com", "hunter2!")
	u, _ := te.users.ByEmail(testCtx, "target3@test.com")
	csrf := te.csrfToken(t, admin)

	for _, val := range []string{"-5", "abc", ""} {
		rr := te.req(t, "POST", fmt.Sprintf("/admin/users/%d/quota", u.ID), "csrf="+csrf+"&quota="+val, admin)
		if !strings.Contains(rr.Header().Get("Location"), "invalid") {
			t.Errorf("quota=%q: expected invalid notice, got %s", val, rr.Header().Get("Location"))
		}
	}
	if q, _ := te.quotas.Get(testCtx, u.ID); q == nil || q.Quota != 250000 || q.Used != 0 {
		t.Errorf("invalid values must not alter the initial quota: %+v", q)
	}
}

func TestAdmin_ResetUsage(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	te.register(t, "used@test.com", "hunter2!")
	u, _ := te.users.ByEmail(testCtx, "used@test.com")
	if err := te.quotas.SetQuota(testCtx, u.ID, 9000); err != nil {
		t.Fatal(err)
	}
	if err := te.quotas.AddUsage(testCtx, u.ID, 1234, "chat"); err != nil {
		t.Fatal(err)
	}

	csrf := te.csrfToken(t, admin)
	rr := te.req(t, "POST", fmt.Sprintf("/admin/users/%d/reset", u.ID), "csrf="+csrf, admin)
	if rr.Code != http.StatusSeeOther || !strings.Contains(rr.Header().Get("Location"), "usage-reset") {
		t.Fatalf("expected 303 usage-reset, got %d %s", rr.Code, rr.Header().Get("Location"))
	}
	q, _ := te.quotas.Get(testCtx, u.ID)
	if q == nil || q.Used != 0 || q.Quota != 9000 {
		t.Errorf("reset must zero usage and keep the quota: %+v", q)
	}
	recent, _ := te.quotas.RecentUsage(testCtx, 10)
	if len(recent) != 1 {
		t.Errorf("reset must keep the usage history, got %d entries", len(recent))
	}
}

func TestAdmin_DeleteUser(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	victim := te.register(t, "victim@test.com", "hunter2!")
	u, _ := te.users.ByEmail(testCtx, "victim@test.com")

	csrf := te.csrfToken(t, admin)
	rr := te.req(t, "POST", fmt.Sprintf("/admin/users/%d/delete", u.ID), "csrf="+csrf, admin)
	if rr.Code != http.StatusSeeOther || !strings.Contains(rr.Header().Get("Location"), "user-deleted") {
		t.Fatalf("expected 303 user-deleted, got %d %s", rr.Code, rr.Header().Get("Location"))
	}
	if gone, _ := te.users.ByEmail(testCtx, "victim@test.com"); gone != nil {
		t.Error("user must be deleted")
	}
	// The victim's session cascaded: their cookie no longer authenticates.
	if rr := te.req(t, "GET", "/", "", victim); rr.Code != http.StatusFound {
		t.Errorf("deleted user's cookie must stop working, got %d", rr.Code)
	}
}

func TestAdmin_DeleteSelf_Refused(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	u, _ := te.users.ByEmail(testCtx, adminTestEmail)

	csrf := te.csrfToken(t, admin)
	rr := te.req(t, "POST", fmt.Sprintf("/admin/users/%d/delete", u.ID), "csrf="+csrf, admin)
	if !strings.Contains(rr.Header().Get("Location"), "self-delete") {
		t.Fatalf("expected self-delete notice, got %s", rr.Header().Get("Location"))
	}
	if gone, _ := te.users.ByEmail(testCtx, adminTestEmail); gone == nil {
		t.Error("admin must not be able to delete themselves")
	}
}

// Admin matching is case-insensitive (EqualFold), so ADMIN_EMAIL keeps
// working even if the account was registered with different casing.
func TestAdmin_EmailCaseInsensitive(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, strings.ToUpper(adminTestEmail), "hunter2!")
	if rr := te.req(t, "GET", "/admin", "", cookie); rr.Code != http.StatusOK {
		t.Errorf("uppercase-registered admin should still get in, got %d", rr.Code)
	}
}

// The admin's own allowance chip shows on the dashboard like anywhere else.
func TestAdmin_Dashboard_RecentUsage(t *testing.T) {
	te := newTestEnv(t)
	admin := te.registerAdmin(t)
	te.register(t, "spender@test.com", "hunter2!")
	u, _ := te.users.ByEmail(testCtx, "spender@test.com")
	if err := te.quotas.AddUsage(testCtx, u.ID, 777, "quiz"); err != nil {
		t.Fatal(err)
	}

	rr := te.req(t, "GET", "/admin", "", admin)
	body := rr.Body.String()
	if !strings.Contains(body, "777") || !strings.Contains(body, "spender@test.com") {
		t.Errorf("dashboard must show recent usage entries, got: %.400s", body)
	}
}
