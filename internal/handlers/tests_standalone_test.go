package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"learnix/internal/db"
	"learnix/internal/session"
)

func seedStandaloneTest(t *testing.T, te *testEnv, cookie *http.Cookie, mode string) (int64, int64) {
	t.Helper()
	userID := uidFromCookie(t, te, cookie)
	testDefinition := &db.TestDefinition{UserID: userID, Topic: "tema independente", Mode: mode, Preset: "fast"}
	if err := te.tests.Create(testCtx, testDefinition); err != nil {
		t.Fatal(err)
	}
	questions := []session.Question{
		{Text: "Prova independente 1", Options: []string{"A", "B", "C", "D", "E"}, Correct: 0, Explanation: "Explicação 1"},
		{Text: "Prova independente 2", Options: []string{"A", "B", "C", "D", "E"}, Correct: 1, Explanation: "Explicação 2"},
	}
	answers := []int{-1, -1}
	if err := te.quizzes.Save(testCtx, &db.Quiz{
		UserID: userID, TestID: testDefinition.ID, Topic: "tema independente", Phase: "quiz", Current: 0,
		Questions: questions, Answers: answers, Confidence: []int{0, 0}, Assessments: []string{"", ""},
		Mode: mode, Preset: "fast", WeightCents: 50,
	}); err != nil {
		t.Fatal(err)
	}
	q, err := te.tests.ListAttempts(testCtx, userID, testDefinition.ID)
	if err != nil || len(q) != 1 {
		t.Fatalf("standalone quiz was not listed: %+v (%v)", q, err)
	}
	return testDefinition.ID, q[0].ID
}

func TestStandaloneTests_AreListedAndIsolated(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "standalone@test.com", "hunter2!")
	testID, attemptID := seedStandaloneTest(t, te, cookie, quizModePractice)

	if rr := te.req(t, http.MethodGet, "/tests", "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "tema independente") {
		t.Fatalf("tests hub: expected the standalone quiz, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodGet, "/tests/new", "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Crie um teste") {
		t.Fatalf("test creation page: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodGet, "/studies/new?topic=tema+guiado", "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "tema guiado") {
		t.Fatalf("study creation page: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodGet, "/tests/"+itoa(testID), "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Continuar tentativa") || !strings.Contains(rr.Body.String(), "O que anotamos") {
		t.Fatalf("test page: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodGet, "/tests/"+itoa(testID)+"/attempts/"+itoa(attemptID), "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Prova independente 1") {
		t.Fatalf("attempt page: got %d: %s", rr.Code, rr.Body.String())
	}

	other := te.register(t, "standalone-other@test.com", "hunter2!")
	if rr := te.req(t, http.MethodGet, "/tests/"+itoa(testID), "", other); rr.Code != http.StatusNotFound {
		t.Fatalf("other user should not access standalone quiz, got %d", rr.Code)
	}
}

func TestTestCreate_OpensOverviewWithoutStartingAttempt(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "test-create@test.com", "hunter2!")
	rr := te.req(t, http.MethodPost, "/tests", "topic=álgebra+linear&mode=practice&preset=fast", cookie)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("test create: expected redirect, got %d: %s", rr.Code, rr.Body.String())
	}
	location := rr.Header().Get("Location")
	if !strings.HasPrefix(location, "/tests/") {
		t.Fatalf("test create redirect = %q", location)
	}
	testID, err := strconv.ParseInt(strings.TrimPrefix(location, "/tests/"), 10, 64)
	if err != nil {
		t.Fatalf("test create location = %q: %v", location, err)
	}
	u := uidFromCookie(t, te, cookie)
	attempts, err := te.tests.ListAttempts(testCtx, u, testID)
	if err != nil || len(attempts) != 0 {
		t.Fatalf("new test should not have an attempt: %+v (%v)", attempts, err)
	}
	page := te.req(t, http.MethodGet, location, "", cookie)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Você ainda não iniciou este teste") || !strings.Contains(page.Body.String(), "Iniciar o teste") {
		t.Fatalf("new test overview: got %d: %s", page.Code, page.Body.String())
	}
}

func TestTestAttemptStart_CreatesAttemptOnlyAfterExplicitStart(t *testing.T) {
	te := newTestEnv(t)
	te.handler.Debug = true
	srv := fakeQuizAI(t, 5)
	defer srv.Close()
	te.handler.Preset = session.Config{BaseURL: srv.URL}
	cookie := te.register(t, "test-start@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)

	created := te.req(t, http.MethodPost, "/tests", "topic=cinemática&mode=practice&preset=fast", cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create test: got %d: %s", created.Code, created.Body.String())
	}
	testID, err := strconv.ParseInt(strings.TrimPrefix(created.Header().Get("Location"), "/tests/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	start := te.req(t, http.MethodPost, "/tests/"+itoa(testID)+"/start", "", cookie)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start attempt: got %d: %s", start.Code, start.Body.String())
	}
	var started struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil || started.StatusURL == "" {
		t.Fatalf("invalid start response: %s", start.Body.String())
	}
	status := waitQuizJob(t, te, started.StatusURL, cookie)
	if status.Status != "succeeded" || !strings.HasPrefix(status.Redirect, "/tests/"+itoa(testID)+"/attempts/") {
		t.Fatalf("attempt job = %+v", status)
	}
	attempts, err := te.tests.ListAttempts(testCtx, uidFromCookie(t, te, cookie), testID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("expected one generated attempt: %+v (%v)", attempts, err)
	}
}

func TestStandaloneTests_CanFinishAndRank(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "standalone-ranked@test.com", "hunter2!")
	testID, attemptID := seedStandaloneTest(t, te, cookie, quizModeRanked)
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	if rr := te.req(t, http.MethodPost, path+"/answer", "index=0&answer=0&confidence=3", cookie); rr.Code != http.StatusOK {
		t.Fatalf("first answer: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodPost, path+"/next", "index=0", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Prova independente 2") {
		t.Fatalf("next question: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodPost, path+"/answer", "index=1&answer=1&confidence=4", cookie); rr.Code != http.StatusOK {
		t.Fatalf("second answer: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodPost, path+"/result", "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Resultado") {
		t.Fatalf("result: got %d: %s", rr.Code, rr.Body.String())
	}

	q, err := te.quizzes.Get(testCtx, attemptID)
	if err != nil || q == nil || q.StudyID != 0 || q.TestID != testID || q.FinishedAt == nil || q.Score != 2 {
		t.Fatalf("standalone result was not persisted: %+v (%v)", q, err)
	}
	if rr := te.req(t, http.MethodGet, "/tests/"+itoa(testID), "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Histórico de tentativas") || !strings.Contains(rr.Body.String(), "Na última tentativa") {
		t.Fatalf("test overview did not update after result: %d: %s", rr.Code, rr.Body.String())
	}
	rows, err := te.leaderboard.ListQuizzes(testCtx, nil, nil, 10)
	if err != nil || len(rows) != 1 || rows[0].QuizCount != 1 {
		t.Fatalf("ranked standalone result was not recorded: %+v (%v)", rows, err)
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func seedExamAttempt(t *testing.T, te *testEnv, cookie *http.Cookie, mode string, deadline time.Time) (int64, int64) {
	t.Helper()
	userID := uidFromCookie(t, te, cookie)
	testDefinition := &db.TestDefinition{UserID: userID, Topic: "simulado tema", Mode: mode, Preset: "fast", Exam: true}
	if err := te.tests.Create(testCtx, testDefinition); err != nil {
		t.Fatal(err)
	}
	questions := []session.Question{
		{Text: "Simulado questão 1", Options: []string{"A", "B", "C", "D", "E"}, Correct: 0, Explanation: "Explicação 1"},
		{Text: "Simulado questão 2", Options: []string{"A", "B", "C", "D", "E"}, Correct: 1, Explanation: "Explicação 2"},
	}
	if err := te.quizzes.Save(testCtx, &db.Quiz{
		UserID: userID, TestID: testDefinition.ID, Topic: "simulado tema", Phase: "quiz", Current: 0,
		Questions: questions, Answers: []int{-1, -1}, Confidence: []int{0, 0}, Assessments: []string{"", ""},
		Mode: mode, Preset: "fast", WeightCents: 50,
		Exam: true, ExamDeadline: &deadline, Flags: []bool{false, false},
	}); err != nil {
		t.Fatal(err)
	}
	q, err := te.tests.ListAttempts(testCtx, userID, testDefinition.ID)
	if err != nil || len(q) != 1 {
		t.Fatalf("exam quiz was not listed: %+v (%v)", q, err)
	}
	return testDefinition.ID, q[0].ID
}

func TestTestAttemptStart_ExamPersistsDeadlineAndFlags(t *testing.T) {
	te := newTestEnv(t)
	te.handler.Debug = true
	srv := fakeQuizAI(t, 5)
	defer srv.Close()
	te.handler.Preset = session.Config{BaseURL: srv.URL}
	cookie := te.register(t, "exam-start@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)

	created := te.req(t, http.MethodPost, "/tests", "topic=cinemática&mode=practice&preset=fast&exam=on", cookie)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create test: got %d: %s", created.Code, created.Body.String())
	}
	testID, err := strconv.ParseInt(strings.TrimPrefix(created.Header().Get("Location"), "/tests/"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := te.tests.Get(testCtx, uidFromCookie(t, te, cookie), testID)
	if err != nil || definition == nil || !definition.Exam {
		t.Fatalf("test definition should be exam: %+v (%v)", definition, err)
	}

	start := te.req(t, http.MethodPost, "/tests/"+itoa(testID)+"/start", "", cookie)
	if start.Code != http.StatusAccepted {
		t.Fatalf("start attempt: got %d: %s", start.Code, start.Body.String())
	}
	var started struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil || started.StatusURL == "" {
		t.Fatalf("invalid start response: %s", start.Body.String())
	}
	status := waitQuizJob(t, te, started.StatusURL, cookie)
	if status.Status != "succeeded" || !strings.HasPrefix(status.Redirect, "/tests/"+itoa(testID)+"/attempts/") {
		t.Fatalf("attempt job = %+v", status)
	}
	attempts, err := te.tests.ListAttempts(testCtx, uidFromCookie(t, te, cookie), testID)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("expected one generated attempt: %+v (%v)", attempts, err)
	}
	q, err := te.quizzes.Get(testCtx, attempts[0].ID)
	if err != nil || q == nil {
		t.Fatalf("load generated quiz: %v", err)
	}
	if !q.Exam || q.ExamDeadline == nil || len(q.Flags) != len(q.Questions) {
		t.Fatalf("exam fields not persisted: exam=%v deadline=%v flags=%d questions=%d", q.Exam, q.ExamDeadline, len(q.Flags), len(q.Questions))
	}
	// Fast preset: 5-minute window.
	if !q.ExamDeadline.After(time.Now().Add(4*time.Minute)) || !q.ExamDeadline.Before(time.Now().Add(6*time.Minute)) {
		t.Fatalf("deadline %v is not ~5 minutes ahead", q.ExamDeadline)
	}
	page := te.req(t, http.MethodGet, status.Redirect, "", cookie)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Simulado · estilo prova") || !strings.Contains(page.Body.String(), `data-exam="1"`) || !strings.Contains(page.Body.String(), "Entregar prova") {
		t.Fatalf("exam attempt page did not render the exam surface: %d", page.Code)
	}
}

func TestExamAttempt_FreeNavigationFlagsAndAnyIndexAnswer(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "exam-nav@test.com", "hunter2!")
	testID, attemptID := seedExamAttempt(t, te, cookie, quizModePractice, time.Now().Add(5*time.Minute))
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	// Answering a question other than the current one is allowed in exam mode,
	// and the board advances to the next unanswered question.
	if rr := te.req(t, http.MethodPost, path+"/answer", "index=1&answer=1", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Simulado questão 1") {
		t.Fatalf("exam answer at any index: got %d: %s", rr.Code, rr.Body.String())
	}
	q, err := te.quizzes.Get(testCtx, attemptID)
	if err != nil || q == nil || q.Answers[1] != 1 || q.Current != 0 {
		t.Fatalf("answer did not advance to the next unanswered: %+v (%v)", q, err)
	}

	if rr := te.req(t, http.MethodPost, path+"/flag", "index=0", cookie); rr.Code != http.StatusOK {
		t.Fatalf("flag: got %d: %s", rr.Code, rr.Body.String())
	}
	q, _ = te.quizzes.Get(testCtx, attemptID)
	if q == nil || len(q.Flags) != 2 || !q.Flags[0] {
		t.Fatalf("flag was not persisted: %+v", q)
	}

	if rr := te.req(t, http.MethodPost, path+"/goto", "index=1", cookie); rr.Code != http.StatusOK {
		t.Fatalf("goto: got %d: %s", rr.Code, rr.Body.String())
	}
	q, _ = te.quizzes.Get(testCtx, attemptID)
	if q == nil || q.Current != 1 {
		t.Fatalf("goto did not move the current question: %+v", q)
	}

	// Linear-flow actions do not exist in exam mode.
	if rr := te.req(t, http.MethodPost, path+"/reanswer", "index=1", cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("reanswer should be rejected in exam mode, got %d", rr.Code)
	}
	if rr := te.req(t, http.MethodPost, path+"/next", "index=1", cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("next should be rejected in exam mode, got %d", rr.Code)
	}
	if rr := te.req(t, http.MethodPost, path+"/answer", "index=9&answer=0", cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("out-of-range index should be rejected, got %d", rr.Code)
	}
}

func TestExamAttempt_ResultAllowsBlanksAndGradesThemWrong(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "exam-blanks@test.com", "hunter2!")
	testID, attemptID := seedExamAttempt(t, te, cookie, quizModePractice, time.Now().Add(5*time.Minute))
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	if rr := te.req(t, http.MethodPost, path+"/answer", "index=0&answer=0", cookie); rr.Code != http.StatusOK {
		t.Fatalf("answer: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodPost, path+"/result", "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Resultado") {
		t.Fatalf("exam result with blanks: got %d: %s", rr.Code, rr.Body.String())
	}
	q, err := te.quizzes.Get(testCtx, attemptID)
	if err != nil || q == nil || q.Phase != "results" || q.FinishedAt == nil {
		t.Fatalf("exam result was not persisted: %+v (%v)", q, err)
	}
	if q.Score != 1 {
		t.Fatalf("blank questions must count as wrong: score=%d", q.Score)
	}
}

func TestExamAttempt_DeadlineAutoFinalizesExactlyOnce(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "exam-expired@test.com", "hunter2!")
	testID, attemptID := seedExamAttempt(t, te, cookie, quizModeRanked, time.Now().Add(-time.Minute))
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	// Loading the page finalizes the expired attempt and renders the results.
	page := te.req(t, http.MethodGet, path, "", cookie)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Resultado") {
		t.Fatalf("expired exam page: got %d", page.Code)
	}
	q, err := te.quizzes.Get(testCtx, attemptID)
	if err != nil || q == nil || q.Phase != "results" || q.FinishedAt == nil {
		t.Fatalf("expired exam was not finalized on load: %+v (%v)", q, err)
	}
	if q.Score != 0 {
		t.Fatalf("all-blank exam must grade zero, got %d", q.Score)
	}
	rows, err := te.leaderboard.ListQuizzes(testCtx, nil, nil, 10)
	if err != nil || len(rows) != 1 || rows[0].QuizCount != 1 {
		t.Fatalf("expired ranked exam must record exactly once: %+v (%v)", rows, err)
	}
	// Re-rendering must not record the ranked result again.
	if rr := te.req(t, http.MethodGet, path, "", cookie); rr.Code != http.StatusOK {
		t.Fatalf("second load: got %d", rr.Code)
	}
	rows, _ = te.leaderboard.ListQuizzes(testCtx, nil, nil, 10)
	if len(rows) != 1 || rows[0].QuizCount != 1 {
		t.Fatalf("ranked result recorded more than once: %+v", rows)
	}
	// Mutations on a finished attempt are gone.
	if rr := te.req(t, http.MethodPost, path+"/answer", "index=0&answer=0", cookie); rr.Code != http.StatusNotFound {
		t.Fatalf("answer after finalization: got %d", rr.Code)
	}
}

func TestExamAttempt_ExpiredMutationIsRejectedAndFinalizes(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "exam-expired-mutation@test.com", "hunter2!")
	testID, attemptID := seedExamAttempt(t, te, cookie, quizModePractice, time.Now().Add(-time.Minute))
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	if rr := te.req(t, http.MethodPost, path+"/answer", "index=0&answer=0", cookie); rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), "tempo esgotado") {
		t.Fatalf("expired mutation: got %d: %s", rr.Code, rr.Body.String())
	}
	q, err := te.quizzes.Get(testCtx, attemptID)
	if err != nil || q == nil || q.Phase != "results" || q.FinishedAt == nil {
		t.Fatalf("expired mutation must finalize the attempt: %+v (%v)", q, err)
	}
}

// fakeTutorAI answers the tutor's single non-streaming completion with a fixed
// reply and a small usage block, mirroring the provider wire shape.
func fakeTutorAI(reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"choices":[{"message":{"content":%q}}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`, reply)
	}))
}

func TestTestTutor_PersistsThreadAndChargesUsage(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeTutorAI("A resposta certa é a letra A porque fotossíntese ocorre no cloroplasto.")
	defer srv.Close()
	te.handler.Preset = session.Config{BaseURL: srv.URL}

	cookie := te.register(t, "tutor@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	testID, attemptID := seedStandaloneTest(t, te, cookie, quizModePractice)
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	body := `{"index":0,"message":"por que a alternativa A está certa?"}`
	rr := te.reqCT(t, http.MethodPost, path+"/tutor", "application/json", body, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "cloroplasto") {
		t.Fatalf("tutor reply: got %d: %s", rr.Code, rr.Body.String())
	}

	// The thread must land on the quiz row so it survives reloads.
	q, err := te.quizzes.Get(testCtx, attemptID)
	if err != nil || q == nil {
		t.Fatalf("quiz lookup: %v", err)
	}
	thread := parseTutorJSON(q.TutorJSON)
	msgs := thread[0]
	if len(msgs) != 2 {
		t.Fatalf("expected user+assistant persisted, got %d messages: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "por que a alternativa A está certa?" {
		t.Errorf("user message not persisted: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || !strings.Contains(msgs[1].Content, "cloroplasto") {
		t.Errorf("assistant reply not persisted: %+v", msgs[1])
	}

	// Tutor calls are billed as chat usage against the user's quota.
	u, _ := te.users.ByEmail(testCtx, "tutor@test.com")
	quota, err := te.quotas.Get(testCtx, u.ID)
	if err != nil || quota == nil || quota.Used <= 0 {
		t.Fatalf("tutor usage must be recorded, got %+v (%v)", quota, err)
	}

	// A second message continues the same thread.
	rr = te.reqCT(t, http.MethodPost, path+"/tutor", "application/json", `{"index":0,"message":"e a B?"}`, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("second tutor message: got %d: %s", rr.Code, rr.Body.String())
	}
	q, _ = te.quizzes.Get(testCtx, attemptID)
	if msgs := parseTutorJSON(q.TutorJSON)[0]; len(msgs) != 4 {
		t.Fatalf("thread should grow to 4 messages, got %d", len(msgs))
	}
}

func TestTestTutor_QuotaZero_Refused(t *testing.T) {
	te := newTestEnv(t)
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"oi"}}]}`)
	}))
	defer srv.Close()
	te.handler.Preset = session.Config{BaseURL: srv.URL}

	cookie := te.register(t, "tutor-noquota@test.com", "hunter2!")
	te.grantQuota(t, cookie, 0)
	testID, attemptID := seedStandaloneTest(t, te, cookie, quizModePractice)
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	rr := te.reqCT(t, http.MethodPost, path+"/tutor", "application/json", `{"index":0,"message":"oi"}`, cookie)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(rr.Body.String(), "cota de tokens acabou") {
		t.Fatalf("expected quota refusal, got %d: %s", rr.Code, rr.Body.String())
	}
	if calls != 0 {
		t.Errorf("AI backend must not be called when quota is exhausted, got %d calls", calls)
	}
	q, _ := te.quizzes.Get(testCtx, attemptID)
	if q == nil || q.TutorJSON != "" {
		t.Errorf("no thread may be persisted on a refused call: %+v", q)
	}
}

func TestTestTutor_Busy_Refused(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeTutorAI("resposta")
	defer srv.Close()
	te.handler.Preset = session.Config{BaseURL: srv.URL}

	cookie := te.register(t, "tutor-busy@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	testID, attemptID := seedStandaloneTest(t, te, cookie, quizModePractice)
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	// Occupy the user's single in-flight AI slot, then try to ask the tutor.
	userID := uidFromCookie(t, te, cookie)
	if !te.handler.startAI(userID) {
		t.Fatal("could not occupy the in-flight slot")
	}
	defer te.handler.endAI(userID)

	rr := te.reqCT(t, http.MethodPost, path+"/tutor", "application/json", `{"index":0,"message":"oi"}`, cookie)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(rr.Body.String(), "requisição de IA em andamento") {
		t.Fatalf("expected busy refusal, got %d: %s", rr.Code, rr.Body.String())
	}
	q, _ := te.quizzes.Get(testCtx, attemptID)
	if q == nil || q.TutorJSON != "" {
		t.Errorf("no thread may be persisted on a busy refusal: %+v", q)
	}
}

func TestTestTutor_ValidatesInput(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeTutorAI("resposta")
	defer srv.Close()
	te.handler.Preset = session.Config{BaseURL: srv.URL}

	cookie := te.register(t, "tutor-input@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	testID, attemptID := seedStandaloneTest(t, te, cookie, quizModePractice)
	path := "/tests/" + itoa(testID) + "/attempts/" + itoa(attemptID)

	// Empty message and out-of-range indices are rejected before any AI call.
	for _, body := range []string{`{"index":0,"message":"   "}`, `{"index":9,"message":"oi"}`, `{"index":-1,"message":"oi"}`} {
		rr := te.reqCT(t, http.MethodPost, path+"/tutor", "application/json", body, cookie)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %s: expected 400, got %d: %s", body, rr.Code, rr.Body.String())
		}
	}
	q, _ := te.quizzes.Get(testCtx, attemptID)
	if q == nil || q.TutorJSON != "" {
		t.Errorf("invalid input must not persist a thread: %+v", q)
	}
}
