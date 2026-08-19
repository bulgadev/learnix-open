package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"learnix/internal/db"
	"learnix/internal/elements"
	"learnix/internal/session"
)

// fakeQuizAI serves the research stage as SSE (direct brief, no tool calls)
// and the author/review stages as non-streaming JSON completions.
func fakeQuizAI(t *testing.T, count int) *httptest.Server {
	t.Helper()
	questions := func(n int) string {
		type question struct {
			Text        string             `json:"text"`
			Context     string             `json:"context"`
			Elements    []elements.Element `json:"elements,omitempty"`
			Options     []string           `json:"options"`
			Correct     int                `json:"correct"`
			Explanation string             `json:"explanation"`
		}
		qs := make([]question, 0, n)
		for i := range n {
			qs = append(qs, question{
				Text:        fmt.Sprintf("Enunciado %d", i),
				Context:     fmt.Sprintf("Contexto %d", i),
				Options:     []string{"a", "b", "c", "d", "e"},
				Correct:     i % 5,
				Explanation: fmt.Sprintf("Explicação %d", i),
			})
		}
		if len(qs) > 0 {
			qs[0].Elements = []elements.Element{{Type: elements.TableType, Title: "Dados", Columns: []string{"A", "B"}, Rows: [][]string{{"1", "2"}}}}
		}
		b, err := json.Marshal(map[string]any{"questions": qs})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var mu sync.Mutex
	completes := 0
	streams := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var parsed struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&parsed)
		if parsed.Stream {
			mu.Lock()
			streams++
			n := streams
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			if n == 1 {
				fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"search_web","arguments":"{\"query\":\"tema\"}"}}]}}]}`+"\n\n")
				fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`+"\n\n")
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"Briefing com questões reais."}}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		mu.Lock()
		completes++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		content := questions(count)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{
			map[string]any{"message": map[string]any{"content": content}},
		}})
	}))
}

func TestQuizStart_NoTavilyKey_ReturnsImmediately(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "quiz-nokey@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudy(t, "fotossintese", cookie)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast"}`, cookie)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 JSON response, got %d: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "Tavily") {
		t.Errorf("body missing Tavily error: %s", body)
	}
}

func TestQuizStart_HappyPath_ReturnsJobAndCompletes(t *testing.T) {
	te := newTestEnv(t)
	hits := &tavilyHits{}
	tav := fakeTavily(hits)
	defer tav.Close()
	te.handler.TavilyKey = "test-key"
	te.handler.tavilyBase = tav.URL

	srv := fakeQuizAI(t, 5)
	defer srv.Close()
	cookie := te.register(t, "quiz@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)
	sid := fid64(t, loc)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast"}`, cookie)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	var started struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil || started.StatusURL == "" {
		t.Fatalf("invalid job response: %s", rr.Body.String())
	}
	status := waitQuizJob(t, te, started.StatusURL, cookie)
	if status.Status != "succeeded" || status.Redirect != loc {
		t.Fatalf("job status = %+v, want succeeded redirect %s", status, loc)
	}

	st, err := te.studies.Get(testCtx, sid)
	if err != nil || st == nil || st.Phase != "quiz" {
		t.Errorf("study phase not persisted: %+v (%v)", st, err)
	}
	q, err := te.quizzes.GetLatestByStudy(testCtx, sid)
	if err != nil || q == nil {
		t.Fatalf("quiz not persisted: %v", err)
	}
	if len(q.Questions) != 5 || len(q.Answers) != 5 {
		t.Fatalf("quiz has %d questions / %d answers, want 5/5", len(q.Questions), len(q.Answers))
	}
	if len(q.Questions[0].Elements) != 1 || q.Questions[0].Elements[0].Type != elements.TableType {
		t.Fatalf("quiz elements were not persisted: %+v", q.Questions[0].Elements)
	}
	for i, a := range q.Answers {
		if a != -1 {
			t.Errorf("answer %d = %d, want -1", i, a)
		}
	}
	if q.Phase != "quiz" || q.Current != 0 {
		t.Errorf("quiz state = phase %q current %d, want quiz/0", q.Phase, q.Current)
	}
	if q.TraceJSON == "" || !strings.Contains(q.TraceJSON, "fotossintese") {
		t.Errorf("quiz generation trace was not persisted: %s", q.TraceJSON)
	}

	answer := te.req(t, "POST", loc+"/quiz/answer", "index=0&answer=0&confidence=4", cookie)
	if answer.Code != http.StatusOK {
		t.Fatalf("answer: expected 200, got %d: %s", answer.Code, answer.Body.String())
	}
	q, err = te.quizzes.GetLatestByStudy(testCtx, sid)
	if err != nil || len(q.Confidence) != 5 || q.Confidence[0] != 4 {
		t.Fatalf("confidence was not persisted: %+v (%v)", q, err)
	}

	if rr := te.req(t, "POST", loc+"/quiz/answer", "index=0&answer=0&confidence=5", cookie); rr.Code != http.StatusBadRequest {
		t.Fatalf("expected invalid confidence to return 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestQuizStart_DebugNoWeb_NoTavily(t *testing.T) {
	te := newTestEnv(t)
	te.handler.Debug = true

	srv := fakeQuizAI(t, 5)
	defer srv.Close()
	cookie := te.register(t, "quiz-debug@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)
	sid := fid64(t, loc)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast","web":false}`, cookie)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	var started struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil || started.StatusURL == "" {
		t.Fatalf("invalid job response: %s", rr.Body.String())
	}
	status := waitQuizJob(t, te, started.StatusURL, cookie)
	if status.Status != "succeeded" || status.Redirect != loc {
		t.Fatalf("job status = %+v, want succeeded redirect %s", status, loc)
	}

	st, err := te.studies.Get(testCtx, sid)
	if err != nil || st == nil || st.Phase != "quiz" {
		t.Errorf("study phase not persisted: %+v (%v)", st, err)
	}
	q, err := te.quizzes.GetLatestByStudy(testCtx, sid)
	if err != nil || q == nil {
		t.Fatalf("quiz not persisted: %v", err)
	}
	if q.Phase != "quiz" {
		t.Errorf("quiz phase = %q, want quiz", q.Phase)
	}
	if len(q.Questions) != 5 {
		t.Errorf("quiz has %d questions, want 5", len(q.Questions))
	}
}

func TestQuizStart_NoDebugForcesWeb(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "quiz-nodebug@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudy(t, "fotossintese", cookie)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast","web":false}`, cookie)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 JSON response, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"error"`) || !strings.Contains(body, "Tavily") {
		t.Errorf("body missing Tavily error (web must be forced on): %s", body)
	}
}

type quizJobStatus struct {
	Status   string `json:"status"`
	Stage    string `json:"stage"`
	Message  string `json:"message"`
	Error    string `json:"error"`
	Redirect string `json:"redirect"`
}

func waitQuizJob(t *testing.T, te *testEnv, path string, cookie *http.Cookie) quizJobStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rr := te.req(t, http.MethodGet, path, "", cookie)
		if rr.Code != http.StatusOK {
			t.Fatalf("job status: expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		var status quizJobStatus
		if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
			t.Fatalf("decode job status: %v: %s", err, rr.Body.String())
		}
		if status.Status == "succeeded" || status.Status == "failed" {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("quiz job did not finish")
	return quizJobStatus{}
}

// A failed pipeline still consumed provider tokens, so it must be charged —
// otherwise users could drain the owner's key for free by making quizzes
// fail on purpose (validation errors after the calls were billed).
func TestQuizStart_FailureStillCharges(t *testing.T) {
	te := newTestEnv(t)
	hits := &tavilyHits{}
	tav := fakeTavily(hits)
	defer tav.Close()
	te.handler.TavilyKey = "test-key"
	te.handler.tavilyBase = tav.URL
	// Always returns 2 questions while the fast preset demands 5, so
	// validation fails on every attempt and the pipeline ends in error.
	srv := fakeQuizAI(t, 2)
	defer srv.Close()

	cookie := te.register(t, "quizfail@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast"}`, cookie)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected job response, got %d: %s", rr.Code, rr.Body.String())
	}
	var started struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil || started.StatusURL == "" {
		t.Fatalf("invalid job response: %s", rr.Body.String())
	}
	status := waitQuizJob(t, te, started.StatusURL, cookie)
	if status.Status != "failed" || status.Error == "" {
		t.Fatalf("expected failed quiz job, got %+v", status)
	}

	u, _ := te.users.ByEmail(testCtx, "quizfail@test.com")
	q, _ := te.quotas.Get(testCtx, u.ID)
	if q == nil || q.Used <= 0 {
		t.Fatalf("failed quiz must still charge the tokens it consumed: %+v", q)
	}
}

func seedMobileQuiz(t *testing.T, te *testEnv, cookie *http.Cookie, current int, answers []int) (string, int64) {
	t.Helper()
	loc := te.createStudy(t, "tema mobile", cookie)
	sid := fid64(t, loc)
	questions := []session.Question{
		{Text: "Primeira questão", Options: []string{"A", "B", "C", "D", "E"}, Correct: 0, Explanation: "Explicação"},
		{Text: "Segunda questão", Options: []string{"A", "B", "C", "D", "E"}, Correct: 1, Explanation: "Explicação"},
	}
	if err := te.quizzes.Save(testCtx, &db.Quiz{
		UserID: uidFromCookie(t, te, cookie), StudyID: sid, Topic: "tema mobile", Phase: "quiz", Current: current,
		Questions: questions, Answers: answers, Confidence: []int{0, 0}, Assessments: []string{"", ""},
		Mode: quizModePractice, Preset: "fast", WeightCents: 50,
	}); err != nil {
		t.Fatal(err)
	}
	st, err := te.studies.Get(testCtx, sid)
	if err != nil || st == nil {
		t.Fatalf("load study: %v", err)
	}
	st.Phase = "quiz"
	if err := te.studies.Update(testCtx, st); err != nil {
		t.Fatal(err)
	}
	return loc, sid
}

func TestStudyWorkspace_DuringQuizPreservesPhase(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mobile-workspace@test.com", "hunter2!")
	loc, sid := seedMobileQuiz(t, te, cookie, 0, []int{-1, -1})

	rr := te.req(t, http.MethodGet, loc+"/workspace?pane=chat&prompt=Explique%20isso", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("workspace: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "data-initial-prompt=\"Explique isso\"") {
		t.Fatalf("workspace did not preserve the prefilled prompt: %s", rr.Body.String())
	}
	st, _ := te.studies.Get(testCtx, sid)
	if st == nil || st.Phase != "quiz" {
		t.Fatalf("workspace changed quiz phase: %+v", st)
	}
}

func TestStudyPage_RendersReviewedCurrentQuestion(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mobile-resume@test.com", "hunter2!")
	loc, _ := seedMobileQuiz(t, te, cookie, 0, []int{0, -1})

	rr := te.req(t, http.MethodGet, loc, "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("resume: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Responder novamente") || strings.Contains(body, "Confirmar resposta") {
		t.Fatalf("resume should render review state: %s", body)
	}
}

func TestQuizReanswer_ClearsCurrentAnswer(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mobile-reanswer@test.com", "hunter2!")
	loc, sid := seedMobileQuiz(t, te, cookie, 0, []int{0, -1})

	rr := te.req(t, http.MethodPost, loc+"/quiz/reanswer", "index=0", cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Confirmar resposta") {
		t.Fatalf("reanswer: expected question form, got %d: %s", rr.Code, rr.Body.String())
	}
	q, _ := te.quizzes.GetLatestByStudy(testCtx, sid)
	if q == nil || q.Answers[0] != -1 || q.Confidence[0] != 0 {
		t.Fatalf("reanswer did not clear state: %+v", q)
	}
}

func TestQuizAnswer_RejectsInvalidAlternative(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mobile-invalid-answer@test.com", "hunter2!")
	loc, _ := seedMobileQuiz(t, te, cookie, 0, []int{-1, -1})

	rr := te.req(t, http.MethodPost, loc+"/quiz/answer", "index=0&answer=5&confidence=3", cookie)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("invalid alternative: expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestQuizNext_RejectsUnansweredQuestion(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mobile-next@test.com", "hunter2!")
	loc, _ := seedMobileQuiz(t, te, cookie, 0, []int{-1, -1})

	rr := te.req(t, http.MethodPost, loc+"/quiz/next", "", cookie)
	if rr.Code != http.StatusConflict {
		t.Fatalf("next unanswered: expected 409, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestQuizResult_RefusesIncompleteQuiz(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mobile-incomplete@test.com", "hunter2!")
	loc, sid := seedMobileQuiz(t, te, cookie, 0, []int{0, -1})

	rr := te.req(t, http.MethodPost, loc+"/quiz/result", "", cookie)
	if rr.Code != http.StatusSeeOther || rr.Header().Get("Location") != loc {
		t.Fatalf("incomplete result: expected redirect to quiz, got %d location=%q", rr.Code, rr.Header().Get("Location"))
	}
	st, _ := te.studies.Get(testCtx, sid)
	if st == nil || st.Phase != "quiz" {
		t.Fatalf("incomplete result changed phase: %+v", st)
	}
}

func TestQuizStart_ExamMode_SetsDeadlineAndFlags(t *testing.T) {
	te := newTestEnv(t)
	te.handler.Debug = true
	srv := fakeQuizAI(t, 5)
	defer srv.Close()
	cookie := te.register(t, "quiz-exam@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)
	sid := fid64(t, loc)

	rr := te.reqCT(t, "POST", loc+"/quiz/start", "application/json", `{"preset":"fast","web":false,"exam":true}`, cookie)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rr.Code, rr.Body.String())
	}
	var started struct {
		StatusURL string `json:"status_url"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &started); err != nil || started.StatusURL == "" {
		t.Fatalf("invalid job response: %s", rr.Body.String())
	}
	status := waitQuizJob(t, te, started.StatusURL, cookie)
	if status.Status != "succeeded" {
		t.Fatalf("job status = %+v", status)
	}
	q, err := te.quizzes.GetLatestByStudy(testCtx, sid)
	if err != nil || q == nil {
		t.Fatalf("quiz not persisted: %v", err)
	}
	if !q.Exam || q.ExamDeadline == nil || len(q.Flags) != len(q.Questions) {
		t.Fatalf("exam fields not persisted: exam=%v deadline=%v flags=%d questions=%d", q.Exam, q.ExamDeadline, len(q.Flags), len(q.Questions))
	}
	// Fast preset: 5-minute window.
	if !q.ExamDeadline.After(time.Now().Add(4*time.Minute)) || !q.ExamDeadline.Before(time.Now().Add(6*time.Minute)) {
		t.Errorf("deadline %v is not ~5 minutes ahead", q.ExamDeadline)
	}
}

func seedStudyExamQuiz(t *testing.T, te *testEnv, cookie *http.Cookie, deadline time.Time) (string, int64) {
	t.Helper()
	loc := te.createStudy(t, "simulado estudo", cookie)
	sid := fid64(t, loc)
	questions := []session.Question{
		{Text: "Estudo simulado 1", Options: []string{"A", "B", "C", "D", "E"}, Correct: 0, Explanation: "Explicação"},
		{Text: "Estudo simulado 2", Options: []string{"A", "B", "C", "D", "E"}, Correct: 1, Explanation: "Explicação"},
	}
	if err := te.quizzes.Save(testCtx, &db.Quiz{
		UserID: uidFromCookie(t, te, cookie), StudyID: sid, Topic: "simulado estudo", Phase: "quiz", Current: 0,
		Questions: questions, Answers: []int{-1, -1}, Confidence: []int{0, 0}, Assessments: []string{"", ""},
		Mode: quizModePractice, Preset: "fast", WeightCents: 50,
		Exam: true, ExamDeadline: &deadline, Flags: []bool{false, false},
	}); err != nil {
		t.Fatal(err)
	}
	st, err := te.studies.Get(testCtx, sid)
	if err != nil || st == nil {
		t.Fatalf("load study: %v", err)
	}
	st.Phase = "quiz"
	if err := te.studies.Update(testCtx, st); err != nil {
		t.Fatal(err)
	}
	return loc, sid
}

func TestStudyQuiz_ExamNavigationAndSubmission(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "study-exam@test.com", "hunter2!")
	loc, sid := seedStudyExamQuiz(t, te, cookie, time.Now().Add(5*time.Minute))

	page := te.req(t, http.MethodGet, loc, "", cookie)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Simulado · estilo prova") || !strings.Contains(page.Body.String(), `data-exam="1"`) {
		t.Fatalf("study page should render the exam surface: %d", page.Code)
	}
	// Answer at an index other than the current one, flag, and navigate freely.
	if rr := te.req(t, http.MethodPost, loc+"/quiz/answer", "index=1&answer=1", cookie); rr.Code != http.StatusOK {
		t.Fatalf("exam answer at any index: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodPost, loc+"/quiz/flag", "index=0", cookie); rr.Code != http.StatusOK {
		t.Fatalf("flag: got %d: %s", rr.Code, rr.Body.String())
	}
	if rr := te.req(t, http.MethodPost, loc+"/quiz/goto", "index=0", cookie); rr.Code != http.StatusOK {
		t.Fatalf("goto: got %d: %s", rr.Code, rr.Body.String())
	}
	q, _ := te.quizzes.GetLatestByStudy(testCtx, sid)
	if q == nil || q.Answers[1] != 1 || q.Current != 0 || len(q.Flags) != 2 || !q.Flags[0] {
		t.Fatalf("exam mutations were not persisted: %+v", q)
	}
	// Submitting with a blank question is allowed and grades it as wrong.
	if rr := te.req(t, http.MethodPost, loc+"/quiz/result", "", cookie); rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Resultado") {
		t.Fatalf("exam result with blanks: got %d", rr.Code)
	}
	q, _ = te.quizzes.GetLatestByStudy(testCtx, sid)
	if q == nil || q.Phase != "results" || q.Score != 1 {
		t.Fatalf("study exam result was not persisted: %+v", q)
	}
	st, _ := te.studies.Get(testCtx, sid)
	if st == nil || st.Phase != "results" {
		t.Fatalf("study phase should follow the finished quiz: %+v", st)
	}
}
