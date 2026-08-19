package handlers

import (
	"net/http"
	"strings"
	"testing"

	"learnix/internal/auth"
	"learnix/internal/db"
	"learnix/internal/session"
)

func uidFromCookie(t *testing.T, te *testEnv, cookie *http.Cookie) int64 {
	t.Helper()
	sid, ok := auth.Verify(cookie.Value, te.secret)
	if !ok {
		t.Fatal("invalid session cookie")
	}
	s, err := te.sessions.Get(testCtx, sid)
	if err != nil || s == nil {
		t.Fatalf("session lookup: %v", err)
	}
	return s.UserID
}

func TestLeaderboard_PublicRankedResultIsIdempotent(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "ranked@test.com", "hunter2!")
	loc := te.createStudy(t, "álgebra", cookie)
	sid := fid64(t, loc)
	q := &db.Quiz{
		UserID: uidFromCookie(t, te, cookie), StudyID: sid, Topic: "álgebra", Phase: "quiz",
		Mode: quizModeRanked, Preset: "fast", WeightCents: 50,
		Questions: []session.Question{{Correct: 0}, {Correct: 1}}, Answers: []int{0, 1},
		Confidence: []int{3, 3}, Assessments: []string{"", ""},
	}
	if err := te.quizzes.Save(testCtx, q); err != nil {
		t.Fatal(err)
	}
	st, err := te.studies.Get(testCtx, sid)
	if err != nil || st == nil {
		t.Fatal(err)
	}
	st.Phase = "quiz"
	if err := te.studies.Update(testCtx, st); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		rr := te.req(t, http.MethodPost, loc+"/quiz/result", "", cookie)
		if rr.Code != http.StatusOK {
			t.Fatalf("quiz result %d: %d %s", i, rr.Code, rr.Body.String())
		}
	}
	rr := te.req(t, http.MethodGet, "/leaderboard/nota?period=all", "")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"entries"`) {
		t.Fatalf("leaderboard response: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Count(rr.Body.String(), `"slug"`) != 1 {
		t.Fatalf("duplicate ranked row: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"value":5`) {
		t.Fatalf("expected 10/10 score with fast weight 0.5: %s", rr.Body.String())
	}
}

func TestLeaderboard_PracticeQuizStaysPrivate(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "practice@test.com", "hunter2!")
	loc := te.createStudy(t, "história", cookie)
	sid := fid64(t, loc)
	q := &db.Quiz{
		UserID: uidFromCookie(t, te, cookie), StudyID: sid, Topic: "história", Phase: "quiz",
		Mode: quizModePractice, Preset: "moderate", WeightCents: 100,
		Questions: []session.Question{{Correct: 0}}, Answers: []int{0},
		Confidence: []int{3}, Assessments: []string{""},
	}
	if err := te.quizzes.Save(testCtx, q); err != nil {
		t.Fatal(err)
	}
	st, _ := te.studies.Get(testCtx, sid)
	st.Phase = "quiz"
	if err := te.studies.Update(testCtx, st); err != nil {
		t.Fatal(err)
	}
	if rr := te.req(t, http.MethodPost, loc+"/quiz/result", "", cookie); rr.Code != http.StatusOK {
		t.Fatalf("practice result: %d %s", rr.Code, rr.Body.String())
	}
	rr := te.req(t, http.MethodGet, "/leaderboard/quizzes?period=all", "")
	if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), `"entries":[{`) {
		t.Fatalf("practice quiz leaked into ranking: %d %s", rr.Code, rr.Body.String())
	}
}

func TestLeaderboard_PublicPageAndPeriodValidation(t *testing.T) {
	te := newTestEnv(t)
	page := te.req(t, http.MethodGet, "/leaderboard", "")
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Leaderboard") {
		t.Fatalf("leaderboard page: %d %s", page.Code, page.Body.String())
	}
	if strings.Contains(page.Body.String(), "data-mobile-nav") {
		t.Fatal("anonymous leaderboard page should not render authenticated mobile navigation")
	}
	cookie := te.register(t, "leaderboard-nav@test.com", "hunter2!")
	page = te.req(t, http.MethodGet, "/leaderboard", "", cookie)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "data-mobile-nav") {
		t.Fatalf("authenticated leaderboard page missing mobile navigation: %d %s", page.Code, page.Body.String())
	}
	rr := te.req(t, http.MethodGet, "/leaderboard/nota?period=year", "")
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid period: expected 400, got %d %s", rr.Code, rr.Body.String())
	}
}
