package handlers

import (
	"net/http"
	"strings"
	"testing"
)

func TestLearningAppsPage_RendersOwnedStudyApps(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "apps@test.com", "password123")
	path := te.createStudy(t, "método científico", cookie)

	rr := te.req(t, "GET", path+"/apps", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("apps page: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"Apps interativos", "Recurso experimental", "data-learning-app", "cartoes", "learning-apps-runtime.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("apps page missing %q", want)
		}
	}
}

func TestLearningAppsPage_DoesNotCrossStudyOwnership(t *testing.T) {
	te := newTestEnv(t)
	owner := te.register(t, "owner-apps@test.com", "password123")
	path := te.createStudy(t, "privado", owner)
	other := te.register(t, "other-apps@test.com", "password123")

	rr := te.req(t, "GET", path+"/apps", "", other)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-user apps page: expected 404, got %d", rr.Code)
	}
}
