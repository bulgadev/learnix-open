package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"learnix/internal/auth"
)

func TestRegister_GrantsInitialQuota(t *testing.T) {
	te := newTestEnv(t)
	te.register(t, "initialquota@test.com", "hunter2!")
	u, err := te.users.ByEmail(testCtx, "initialquota@test.com")
	if err != nil || u == nil {
		t.Fatalf("registered user missing: %v", err)
	}
	q, err := te.quotas.Get(testCtx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if q == nil || q.Quota != 250000 || q.Used != 0 {
		t.Fatalf("new user quota = %+v, want 250000/0", q)
	}
}

// A zero allowance still blocks AI endpoints before any provider call.
func TestChatStream_QuotaZero_Blocked(t *testing.T) {
	te := newTestEnv(t)
	var bodies [][]byte
	srv := fakeOpenAICapture("ola", &bodies)
	defer srv.Close()
	cookie := te.register(t, "noquota@test.com", "hunter2!")
	te.grantQuota(t, cookie, 0)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	rr := te.streamChat(t, loc, c.ID, "oi", cookie)
	body := rr.Body.String()
	if !strings.Contains(body, "cota de tokens acabou") || !strings.Contains(body, "Me chama no zapzap que eu do mais token vro 🥀🥀🙏") {
		t.Fatalf("expected quota error event, got: %s", body)
	}
	if len(bodies) != 0 {
		t.Errorf("AI backend must not be called when quota is exhausted, got %d calls", len(bodies))
	}
	msgs, _ := te.chats.Messages(testCtx, c.ID)
	if len(msgs) != 0 {
		t.Errorf("no message may be persisted on a refused call, got %d", len(msgs))
	}
}

func TestChatStream_QuotaRecordsUsage(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAI("ola mundo")
	defer srv.Close()
	cookie := te.register(t, "chatusage@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	rr := te.streamChat(t, loc, c.ID, "oi", cookie)
	if !strings.Contains(rr.Body.String(), `"done":true`) {
		t.Fatalf("expected done event, got: %s", rr.Body.String())
	}

	u, _ := te.users.ByEmail(testCtx, "chatusage@test.com")
	q, err := te.quotas.Get(testCtx, u.ID)
	if err != nil || q == nil || q.Used <= 0 {
		t.Fatalf("chat usage must be recorded, got %+v (%v)", q, err)
	}
	recent, _ := te.quotas.RecentUsage(testCtx, 10)
	if len(recent) != 1 || recent[0].Kind != "chat" || recent[0].Tokens <= 0 {
		t.Errorf("expected one chat usage_log entry, got %+v", recent)
	}
}

func TestQuizStart_QuotaZero_Blocked(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "quiznoquota@test.com", "hunter2!")
	te.grantQuota(t, cookie, 0)
	loc := te.createStudy(t, "fotossintese", cookie)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast"}`, cookie)
	body := rr.Body.String()
	if !strings.Contains(body, "cota de tokens acabou") || !strings.Contains(body, "Me chama no zapzap que eu do mais token vro 🥀🥀🙏") {
		t.Fatalf("expected quota error event, got: %s", body)
	}
}

func TestQuizStart_QuotaRecordsUsage(t *testing.T) {
	te := newTestEnv(t)
	hits := &tavilyHits{}
	tav := fakeTavily(hits)
	defer tav.Close()
	te.handler.TavilyKey = "test-key"
	te.handler.tavilyBase = tav.URL
	srv := fakeQuizAI(t, 5)
	defer srv.Close()

	cookie := te.register(t, "quizusage@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast"}`, cookie)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected accepted job, got %d: %s", rr.Code, rr.Body.String())
	}
	var started struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil || started.StatusURL == "" {
		t.Fatalf("invalid job response: %s", rr.Body.String())
	}
	if status := waitQuizJob(t, te, started.StatusURL, cookie); status.Status != "succeeded" {
		t.Fatalf("quiz job failed: %+v", status)
	}

	u, _ := te.users.ByEmail(testCtx, "quizusage@test.com")
	recent, _ := te.quotas.RecentUsage(testCtx, 10)
	if len(recent) != 1 || recent[0].Kind != "quiz" || recent[0].Tokens <= 0 {
		t.Errorf("expected one quiz usage_log entry, got %+v", recent)
	}
	q, _ := te.quotas.Get(testCtx, u.ID)
	if q == nil || q.Used <= 0 {
		t.Fatalf("quiz usage must be recorded, got %+v", q)
	}
}

// The remaining allowance shows in the layout; an exhausted budget shows a
// warning on the home page.
func TestHome_ShowsQuotaState(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "quotavis@test.com", "hunter2!")
	te.grantQuota(t, cookie, 1000)

	rr := te.req(t, "GET", "/", "", cookie)
	if !strings.Contains(rr.Body.String(), "1.000 restantes") || !strings.Contains(rr.Body.String(), "0 usados") {
		t.Errorf("home should show the remaining allowance, got: %.300s", rr.Body.String())
	}

	u, _ := te.users.ByEmail(testCtx, "quotavis@test.com")
	if err := te.quotas.AddUsage(testCtx, u.ID, 1000, "chat"); err != nil {
		t.Fatal(err)
	}
	rr = te.req(t, "GET", "/", "", cookie)
	if !strings.Contains(rr.Body.String(), "Sua cota de tokens acabou") {
		t.Errorf("exhausted home should warn about the quota, got: %.300s", rr.Body.String())
	}
}

// Users without a quota row render no allowance chip at all.
func TestHome_NoQuotaRow_NoChip(t *testing.T) {
	te := newTestEnv(t)
	uid, err := te.users.Create(testCtx, "nochip@test.com", "hash")
	if err != nil {
		t.Fatal(err)
	}
	sid := auth.NewSessionID()
	if err := te.sessions.Create(testCtx, sid, uid); err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: auth.CookieName, Value: auth.Sign(sid, te.secret)}
	rr := te.req(t, "GET", "/", "", cookie)
	if strings.Contains(rr.Body.String(), "Tokens restantes") {
		t.Error("users without a quota row must not see the allowance chip")
	}
}
