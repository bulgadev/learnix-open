package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"learnix/internal/ai"
	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/quizgen"
	"learnix/internal/quizjobs"
	"learnix/internal/session"
	"learnix/internal/websearch"
)

func standaloneIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id, err == nil && id > 0
}

func testDefinitionIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "testID"), 10, 64)
	return id, err == nil && id > 0
}

func testAttemptIDParam(r *http.Request) (int64, bool) {
	value := chi.URLParam(r, "attemptID")
	if value == "" {
		value = chi.URLParam(r, "id")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return id, err == nil && id > 0
}

func (h *Handler) loadStandaloneQuiz(r *http.Request, id int64) *session.Session {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		return nil
	}
	q, err := h.quizzes.Get(r.Context(), id)
	if err != nil || q == nil || q.UserID != u.ID || q.StudyID != 0 {
		return nil
	}
	s := &session.Session{
		Config: session.Config{Topic: q.Topic}, Phase: q.Phase, Questions: q.Questions,
		Answers: q.Answers, Confidence: q.Confidence, Assessments: q.Assessments,
		TraceJSON: q.TraceJSON, Current: q.Current, ActiveQuizID: q.ID,
		TestID:   q.TestID,
		QuizMode: q.Mode, QuizPreset: q.Preset, QuizWeightCents: q.WeightCents,
		NormalizedScoreCents: q.ScoreCents, RankedScoreCents: q.WeightedScoreCents,
		AdaptiveFromID: q.AdaptiveFromID, FinishedAt: q.FinishedAt,
		Exam: q.Exam, ExamDeadline: q.ExamDeadline, Flags: q.Flags, Tutor: parseTutorJSON(q.TutorJSON),
	}
	if s.QuizMode == "" {
		s.QuizMode = quizModePractice
	}
	if s.TraceJSON != "" {
		_ = json.Unmarshal([]byte(s.TraceJSON), &s.Trace)
	}
	return s
}

func (h *Handler) loadTestAttempt(r *http.Request, testID, attemptID int64) *session.Session {
	s := h.loadStandaloneQuiz(r, attemptID)
	if s == nil || s.TestID != testID {
		return nil
	}
	return s
}

func (h *Handler) TestCreatePage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	render(w, r, components.TestCreate(r.URL.Query().Get("topic"), u, h.quotaFor(r.Context(), u), h.isAdmin(u)))
}

func (h *Handler) TestsHub(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	query.Set("tab", "tests")
	clone := r.Clone(r.Context())
	clone.URL.RawQuery = query.Encode()
	h.StudiesList(w, clone)
}

// TestCreate persists the test container only. Question generation happens
// later, from the overview page, when the learner explicitly starts an attempt.
func (h *Handler) TestCreate(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	topic := strings.TrimSpace(r.FormValue("topic"))
	if topic == "" || len([]rune(topic)) > 2000 {
		http.Error(w, "tema inválido", http.StatusBadRequest)
		return
	}
	preset, ok := quizPreset(r.FormValue("preset"))
	if !ok {
		http.Error(w, "preset inválido", http.StatusBadRequest)
		return
	}
	mode, ok := quizMode(r.FormValue("mode"))
	if !ok {
		http.Error(w, "modo inválido", http.StatusBadRequest)
		return
	}
	test := &db.TestDefinition{UserID: u.ID, Topic: topic, Mode: mode, Preset: preset.Name, Exam: r.FormValue("exam") == "on"}
	if err := h.tests.Create(r.Context(), test); err != nil {
		http.Error(w, "erro ao criar teste", http.StatusInternalServerError)
		return
	}
	h.recordTelemetry(r.Context(), db.TelemetryEvent{UserID: u.ID, Type: telemetryTestCreated, Metadata: map[string]any{"mode": mode, "preset": preset.Name, "exam": test.Exam}})
	http.Redirect(w, r, fmt.Sprintf("/tests/%d", test.ID), http.StatusSeeOther)
}

func (h *Handler) TestPage(w http.ResponseWriter, r *http.Request) {
	id, ok := standaloneIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	u := auth.UserFromContext(r.Context())
	test, err := h.tests.Get(r.Context(), u.ID, id)
	if err != nil {
		http.Error(w, "erro ao carregar teste", http.StatusInternalServerError)
		return
	}
	if test != nil {
		h.renderTestOverview(w, r, test)
		return
	}
	// Legacy attempts created before test containers remain accessible.
	h.renderTestAttempt(w, r, 0, id)
}

func (h *Handler) TestAttemptPage(w http.ResponseWriter, r *http.Request) {
	testID, ok := testDefinitionIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	attemptID, ok := testAttemptIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.renderTestAttempt(w, r, testID, attemptID)
}

func (h *Handler) renderTestOverview(w http.ResponseWriter, r *http.Request, test *db.TestDefinition) {
	u := auth.UserFromContext(r.Context())
	attempts, err := h.tests.ListAttempts(r.Context(), u.ID, test.ID)
	if err != nil {
		http.Error(w, "erro ao carregar histórico", http.StatusInternalServerError)
		return
	}
	summaries, err := h.tests.ListByUser(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "erro ao carregar desempenho", http.StatusInternalServerError)
		return
	}
	var summary db.TestSummary
	for _, item := range summaries {
		if item.ID == test.ID {
			summary = item
			break
		}
	}
	notes := test.Observations
	if len(notes) == 0 {
		if latest := latestFinishedQuiz(r.Context(), h.quizzes, attempts); latest != nil {
			notes = testObservations(latest)
		}
	}
	render(w, r, components.TestOverview(*test, summary, attempts, notes, u, h.quotaFor(r.Context(), u), h.isAdmin(u)))
}

func latestFinishedQuiz(ctx context.Context, quizzes *db.QuizRepo, attempts []db.TestAttemptSummary) *session.Session {
	for _, attempt := range attempts {
		if attempt.FinishedAt == nil {
			continue
		}
		q, err := quizzes.Get(ctx, attempt.ID)
		if err != nil || q == nil {
			return nil
		}
		return quizToSession(q)
	}
	return nil
}

func quizToSession(q *db.Quiz) *session.Session {
	s := &session.Session{Config: session.Config{Topic: q.Topic}, Phase: q.Phase, Questions: q.Questions, Answers: q.Answers, Confidence: q.Confidence, Assessments: q.Assessments, TraceJSON: q.TraceJSON, Current: q.Current, ActiveQuizID: q.ID, TestID: q.TestID, QuizMode: q.Mode, QuizPreset: q.Preset, QuizWeightCents: q.WeightCents, NormalizedScoreCents: q.ScoreCents, RankedScoreCents: q.WeightedScoreCents, AdaptiveFromID: q.AdaptiveFromID, FinishedAt: q.FinishedAt, Exam: q.Exam, ExamDeadline: q.ExamDeadline, Flags: q.Flags, Tutor: parseTutorJSON(q.TutorJSON)}
	if s.TraceJSON != "" {
		_ = json.Unmarshal([]byte(s.TraceJSON), &s.Trace)
	}
	return s
}

func testObservations(s *session.Session) []string {
	if s == nil || len(s.Questions) == 0 {
		return nil
	}
	notes := []string{fmt.Sprintf("Na última tentativa, você acertou %d de %d questões.", s.Score(), len(s.Questions))}
	var weak []string
	lowConfidence := 0
	for i, question := range s.Questions {
		if i >= len(s.Answers) || s.Answers[i] == question.Correct {
			continue
		}
		label := question.Skill
		if label == "" {
			label = question.Text
		}
		label = strings.TrimSpace(label)
		if len([]rune(label)) > 72 {
			label = string([]rune(label)[:72]) + "…"
		}
		weak = append(weak, label)
		if i < len(s.Confidence) && s.Confidence[i] >= 3 {
			lowConfidence++
		}
	}
	if len(weak) > 0 {
		if len(weak) > 3 {
			weak = weak[:3]
		}
		notes = append(notes, "Pontos para revisar: "+strings.Join(weak, "; "))
	}
	if lowConfidence > 0 {
		notes = append(notes, fmt.Sprintf("%d erro(s) vieram com confiança alta; vamos reforçar esse conteúdo na próxima tentativa.", lowConfidence))
	}
	return notes
}

func (h *Handler) renderTestAttempt(w http.ResponseWriter, r *http.Request, testID, attemptID int64) {
	var s *session.Session
	if testID > 0 {
		s = h.loadTestAttempt(r, testID, attemptID)
	} else {
		s = h.loadStandaloneQuiz(r, attemptID)
	}
	if s == nil {
		http.NotFound(w, r)
		return
	}
	// Exam deadlines are server-authoritative: an attempt that ran out of
	// time while the learner was away is finalized before anything renders.
	if s.Phase == "quiz" {
		if expired, err := h.finalizeExpiredExam(r, s, testID); expired {
			if err != nil {
				http.Error(w, "erro ao finalizar prova", http.StatusInternalServerError)
				return
			}
		}
	}
	u := auth.UserFromContext(r.Context())
	base := fmt.Sprintf("/tests/%d", attemptID)
	if testID > 0 {
		base = fmt.Sprintf("/tests/%d/attempts/%d", testID, attemptID)
	}
	if s.Phase == "quiz" && s.Current < len(s.Questions) {
		render(w, r, components.Quiz(s, s.Current, s.Questions[s.Current], len(s.Questions), u, h.quotaFor(r.Context(), u), h.isAdmin(u), base, true))
		return
	}
	if s.Phase == "results" {
		render(w, r, components.Result(s, s.Score(), len(s.Questions), s.Score() == len(s.Questions), u, h.quotaFor(r.Context(), u), h.isAdmin(u), base, true))
		return
	}
	http.Redirect(w, r, base, http.StatusSeeOther)
}

// TestAttemptStart queues generation for a test definition. It never enters
// the attempt in the same request; the overview remains the user's anchor.
func (h *Handler) TestAttemptStart(w http.ResponseWriter, r *http.Request) {
	testID, ok := testDefinitionIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	u := auth.UserFromContext(r.Context())
	test, err := h.tests.Get(r.Context(), u.ID, testID)
	if err != nil || test == nil {
		http.NotFound(w, r)
		return
	}
	preset, ok := quizPreset(test.Preset)
	if !ok {
		preset, _ = quizPreset("moderate")
	}
	mode, ok := quizMode(test.Mode)
	if !ok {
		mode = quizModePractice
	}
	feedback := ""
	attempts, _ := h.tests.ListAttempts(r.Context(), u.ID, testID)
	if previous := latestFinishedQuiz(r.Context(), h.quizzes, attempts); previous != nil {
		feedback = strings.Join(testWeakQuestions(previous), "; ")
	}
	// Anti-repetition: collect stems from every finished attempt of this test
	// (not just the latest) so adaptive retries explore new questions.
	stems := newStemsCollector(30)
	for _, attempt := range attempts {
		if attempt.FinishedAt == nil {
			continue
		}
		if q, err := h.quizzes.Get(r.Context(), attempt.ID); err == nil && q != nil {
			stems.add(q.Questions)
		}
	}
	prevStems := stems.stems()
	quota := h.quotaFor(r.Context(), u)
	if quota == nil || quota.Exhausted() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": quotaExhaustedErr})
		return
	}
	if !h.startAI(u.ID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": aiBusyErr})
		return
	}
	web := true
	if h.TavilyKey == "" {
		if !h.Debug {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "A geração de questões usa pesquisa na web: configure uma chave Tavily (TAVILY_API_KEY) e tente novamente."})
			h.endAI(u.ID)
			return
		}
		web = false
	}
	var search *websearch.Client
	if web {
		search = websearch.NewClient(h.TavilyKey)
		if h.tavilyBase != "" {
			search = websearch.NewClientWithBase(h.TavilyKey, h.tavilyBase)
		}
	}
	job := h.quizJobs.Start(u.ID, testID, func(ctx context.Context, report func(quizjobs.Update)) (string, error) {
		defer h.endAI(u.ID)
		jobCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		cfg := h.Preset
		cfg.Topic = test.Topic
		questions, _, trace, generateErr := quizgen.GenerateWithTrace(jobCtx, h.client, h.effectiveConfig(&session.Session{Config: cfg}), search, quizgen.Spec{Topic: test.Topic, Feedback: feedback, Count: preset.Count, Web: web, PrevStems: prevStems, ResearchModel: h.QuizResearchModel, AuthorModel: h.QuizAuthorModel, ReviewModel: h.QuizReviewModel}, func(progress quizgen.Progress) {
			update := quizjobs.Update{Stage: progress.Stage, Level: progress.Level, Message: progress.Message}
			if progress.Metrics != nil {
				update.Metrics = true
				update.Current = progress.Metrics.Current
				update.Total = progress.Metrics.Total
				update.Sources = progress.Metrics.Sources
				update.Searches = progress.Metrics.Searches
				update.Pages = progress.Metrics.Pages
				update.ModelCalls = progress.Metrics.ModelCalls
				update.Tokens = progress.Metrics.Tokens
				update.Attempt = progress.Metrics.Attempt
				update.MaxAttempts = progress.Metrics.MaxAttempts
			}
			report(update)
		})
		if generateErr != nil {
			return "", generateErr
		}
		completed := &session.Session{Config: session.Config{Topic: test.Topic}, Phase: "quiz", Questions: questions, Answers: make([]int, len(questions)), Confidence: make([]int, len(questions)), Assessments: make([]string, len(questions)), Trace: trace, TestID: testID, QuizMode: mode, QuizPreset: preset.Name, QuizWeightCents: preset.WeightCents, AdaptiveFromID: latestAttemptID(attempts)}
		for i := range completed.Answers {
			completed.Answers[i] = -1
		}
		if test.Exam {
			deadline := time.Now().Add(time.Duration(preset.Minutes) * time.Minute)
			completed.Exam = true
			completed.ExamDeadline = &deadline
			completed.Flags = make([]bool, len(questions))
		}
		if err := h.persistQuizState(jobCtx, u.ID, completed); err != nil {
			return "", err
		}
		h.recordAIUsage(jobCtx, u.ID, int64(trace.TokenUsage.Total()), "quiz", 0, completed.ActiveQuizID, map[string]any{"mode": mode, "preset": preset.Name, "test_id": testID})
		h.recordTelemetry(jobCtx, db.TelemetryEvent{UserID: u.ID, QuizID: completed.ActiveQuizID, Type: telemetryQuizGenerationSucceeded, Metadata: map[string]any{"mode": mode, "test_id": testID}, Delta: db.TelemetryDelta{QuizzesStarted: 1}})
		return fmt.Sprintf("/tests/%d/attempts/%d", testID, completed.ActiveQuizID), nil
	})
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": job.ID, "status": job.Status, "status_url": fmt.Sprintf("/tests/%d/jobs/%s", testID, job.ID)})
}

func latestAttemptID(attempts []db.TestAttemptSummary) int64 {
	for _, attempt := range attempts {
		if attempt.FinishedAt != nil {
			return attempt.ID
		}
	}
	return 0
}

func testWeakQuestions(s *session.Session) []string {
	var weak []string
	for i, question := range s.Questions {
		if i >= len(s.Answers) || s.Answers[i] == question.Correct {
			continue
		}
		weak = append(weak, question.Text)
	}
	return weak
}

func (h *Handler) TestJobStatus(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	jobID := chi.URLParam(r, "jobID")
	scopeID, _ := strconv.ParseInt(chi.URLParam(r, "testID"), 10, 64)
	if u == nil || jobID == "" {
		http.NotFound(w, r)
		return
	}
	job, ok := h.quizJobs.Get(jobID, u.ID, scopeID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) TestAnswer(w http.ResponseWriter, r *http.Request) { h.testMutation(w, r, "answer") }
func (h *Handler) TestReanswer(w http.ResponseWriter, r *http.Request) {
	h.testMutation(w, r, "reanswer")
}
func (h *Handler) TestNext(w http.ResponseWriter, r *http.Request) { h.testMutation(w, r, "next") }
func (h *Handler) TestGoto(w http.ResponseWriter, r *http.Request) { h.testMutation(w, r, "goto") }
func (h *Handler) TestFlag(w http.ResponseWriter, r *http.Request) { h.testMutation(w, r, "flag") }

func (h *Handler) TestAttemptAnswer(w http.ResponseWriter, r *http.Request) {
	h.testAttemptMutation(w, r, "answer")
}
func (h *Handler) TestAttemptReanswer(w http.ResponseWriter, r *http.Request) {
	h.testAttemptMutation(w, r, "reanswer")
}
func (h *Handler) TestAttemptNext(w http.ResponseWriter, r *http.Request) {
	h.testAttemptMutation(w, r, "next")
}
func (h *Handler) TestAttemptGoto(w http.ResponseWriter, r *http.Request) {
	h.testAttemptMutation(w, r, "goto")
}
func (h *Handler) TestAttemptFlag(w http.ResponseWriter, r *http.Request) {
	h.testAttemptMutation(w, r, "flag")
}

func (h *Handler) testAttemptMutation(w http.ResponseWriter, r *http.Request, action string) {
	testID, ok := testDefinitionIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	attemptID, ok := testAttemptIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testMutationForAttempt(w, r, action, testID, attemptID)
}

func (h *Handler) testMutation(w http.ResponseWriter, r *http.Request, action string) {
	attemptID, ok := standaloneIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testMutationForAttempt(w, r, action, 0, attemptID)
}

func (h *Handler) testMutationForAttempt(w http.ResponseWriter, r *http.Request, action string, testID, attemptID int64) {
	var s *session.Session
	if testID > 0 {
		s = h.loadTestAttempt(r, testID, attemptID)
	} else {
		s = h.loadStandaloneQuiz(r, attemptID)
	}
	if s == nil || s.Phase != "quiz" {
		http.NotFound(w, r)
		return
	}
	if h.rejectExpiredExam(w, r, s, testID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	idx, _ := strconv.Atoi(r.FormValue("index"))
	// Exam attempts allow answering at any index (free navigation); the
	// instant-feedback path stays strictly linear.
	if idx < 0 || idx >= len(s.Questions) || (idx != s.Current && !s.Exam) {
		http.Error(w, "índice inválido", http.StatusBadRequest)
		return
	}
	base := fmt.Sprintf("/tests/%d", attemptID)
	if testID > 0 {
		base = fmt.Sprintf("/tests/%d/attempts/%d", testID, attemptID)
	}
	if s.Exam {
		h.examMutation(w, r, s, testID, base, action, true)
		return
	}
	if action == "answer" {
		ans, _ := strconv.Atoi(r.FormValue("answer"))
		confidence, _ := strconv.Atoi(r.FormValue("confidence"))
		if ans < 0 || ans >= len(s.Questions[idx].Options) || !session.ValidConfidence(confidence) {
			http.Error(w, "resposta inválida", http.StatusBadRequest)
			return
		}
		if len(s.Answers) != len(s.Questions) {
			s.Answers = normalizeAnswers(s.Answers, len(s.Questions))
		}
		if len(s.Confidence) != len(s.Questions) {
			s.Confidence = make([]int, len(s.Questions))
		}
		s.Answers[idx] = ans
		s.Confidence[idx] = confidence
		h.persistQuiz(r, s)
		u := auth.UserFromContext(r.Context())
		delta := db.TelemetryDelta{QuestionsAnswered: 1}
		if ans == s.Questions[idx].Correct {
			delta.CorrectAnswers = 1
		}
		h.recordTelemetry(r.Context(), db.TelemetryEvent{UserID: u.ID, QuizID: s.ActiveQuizID, Type: telemetryQuizAnswered, ValueInt: int64(idx), Metadata: map[string]any{"test_id": testID}, Delta: delta})
		render(w, r, components.ReviewedCard(base, 0, idx, s.Questions[idx], ans, len(s.Questions), idx == len(s.Questions)-1, true, s.Tutor[idx]))
		return
	}
	if action == "reanswer" {
		if len(s.Answers) != len(s.Questions) {
			s.Answers = normalizeAnswers(s.Answers, len(s.Questions))
		}
		s.Answers[idx] = -1
		if len(s.Confidence) == len(s.Questions) {
			s.Confidence[idx] = 0
		}
		h.persistQuiz(r, s)
		render(w, r, components.QuestionCard(base, s.ActiveQuizID, idx, s.Questions[idx], len(s.Questions), true))
		return
	}
	if idx >= len(s.Questions)-1 || idx >= len(s.Answers) || s.Answers[idx] < 0 {
		http.Error(w, "responda a questão atual antes de avançar", http.StatusConflict)
		return
	}
	s.Current++
	h.persistQuiz(r, s)
	if s.Answers[s.Current] >= 0 {
		render(w, r, components.ReviewedCard(base, 0, s.Current, s.Questions[s.Current], s.Answers[s.Current], len(s.Questions), s.Current == len(s.Questions)-1, true, s.Tutor[s.Current]))
		return
	}
	render(w, r, components.QuestionCard(base, s.ActiveQuizID, s.Current, s.Questions[s.Current], len(s.Questions), true))
}

func (h *Handler) TestResult(w http.ResponseWriter, r *http.Request) {
	attemptID, ok := standaloneIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testResultForAttempt(w, r, 0, attemptID)
}

func (h *Handler) TestAttemptResult(w http.ResponseWriter, r *http.Request) {
	testID, ok := testDefinitionIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	attemptID, ok := testAttemptIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testResultForAttempt(w, r, testID, attemptID)
}

func (h *Handler) testResultForAttempt(w http.ResponseWriter, r *http.Request, testID, attemptID int64) {
	var s *session.Session
	if testID > 0 {
		s = h.loadTestAttempt(r, testID, attemptID)
	} else {
		s = h.loadStandaloneQuiz(r, attemptID)
	}
	if s == nil || (s.Phase != "quiz" && s.Phase != "results") {
		http.NotFound(w, r)
		return
	}
	u := auth.UserFromContext(r.Context())
	if s.Phase == "quiz" {
		// Exam submission grades blanks as wrong; the practice path refuses
		// to finish while questions remain unanswered.
		if err := h.finishQuiz(r.Context(), u.ID, s, testID, s.Exam); err != nil {
			if errors.Is(err, errQuizUnanswered) {
				http.Error(w, "responda todas as questões", http.StatusConflict)
				return
			}
			http.Error(w, "erro ao salvar resultado", http.StatusInternalServerError)
			return
		}
	}
	base := fmt.Sprintf("/tests/%d", attemptID)
	if testID > 0 {
		base = fmt.Sprintf("/tests/%d/attempts/%d", testID, attemptID)
	}
	render(w, r, components.Result(s, s.Score(), len(s.Questions), s.Score() == len(s.Questions), u, h.quotaFor(r.Context(), u), h.isAdmin(u), base, true))
}

func (h *Handler) TestDiagnostic(w http.ResponseWriter, r *http.Request) {
	attemptID, ok := standaloneIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testDiagnosticForAttempt(w, r, 0, attemptID)
}

func (h *Handler) TestAttemptDiagnostic(w http.ResponseWriter, r *http.Request) {
	testID, ok := testDefinitionIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	attemptID, ok := testAttemptIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testDiagnosticForAttempt(w, r, testID, attemptID)
}

func (h *Handler) testDiagnosticForAttempt(w http.ResponseWriter, r *http.Request, testID, attemptID int64) {
	var s *session.Session
	if testID > 0 {
		s = h.loadTestAttempt(r, testID, attemptID)
	} else {
		s = h.loadStandaloneQuiz(r, attemptID)
	}
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	idx, _ := strconv.Atoi(r.FormValue("index"))
	assessment := r.FormValue("assessment")
	if idx < 0 || idx >= len(s.Questions) || !session.ValidAssessment(assessment) {
		http.Error(w, "diagnóstico inválido", http.StatusBadRequest)
		return
	}
	if len(s.Assessments) != len(s.Questions) {
		s.Assessments = make([]string, len(s.Questions))
	}
	s.Assessments[idx] = assessment
	h.persistQuiz(r, s)
	u := auth.UserFromContext(r.Context())
	h.recordTelemetry(r.Context(), db.TelemetryEvent{UserID: u.ID, QuizID: s.ActiveQuizID, Type: telemetryQuizDiagnostic, Metadata: map[string]any{"test_id": testID, "assessment": assessment}, Delta: db.TelemetryDelta{DiagnosticsSubmitted: 1}})
	if testID > 0 {
		_ = h.tests.UpdateObservations(r.Context(), u.ID, testID, testObservations(s))
		http.Redirect(w, r, fmt.Sprintf("/tests/%d/attempts/%d", testID, attemptID), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/tests/%d", attemptID), http.StatusSeeOther)
}

// TestTutor and TestAttemptTutor answer a learner's question about one
// question of a standalone attempt. The thread lives on the quiz row itself
// (tutor_json) — standalone attempts have no study chat to lean on.
func (h *Handler) TestTutor(w http.ResponseWriter, r *http.Request) {
	attemptID, ok := standaloneIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testTutorForAttempt(w, r, 0, attemptID)
}

func (h *Handler) TestAttemptTutor(w http.ResponseWriter, r *http.Request) {
	testID, ok := testDefinitionIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	attemptID, ok := testAttemptIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	h.testTutorForAttempt(w, r, testID, attemptID)
}

func (h *Handler) testTutorForAttempt(w http.ResponseWriter, r *http.Request, testID, attemptID int64) {
	var s *session.Session
	if testID > 0 {
		s = h.loadTestAttempt(r, testID, attemptID)
	} else {
		s = h.loadStandaloneQuiz(r, attemptID)
	}
	if s == nil || (s.Phase != "quiz" && s.Phase != "results") {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Index   int    `json:"index"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "requisição inválida"})
		return
	}
	message := capTutorMessage(strings.TrimSpace(body.Message))
	if message == "" || body.Index < 0 || body.Index >= len(s.Questions) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mensagem ou questão inválida"})
		return
	}
	u := auth.UserFromContext(r.Context())
	if h.quotaFor(r.Context(), u).Exhausted() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": quotaExhaustedErr})
		return
	}
	if !h.startAI(u.ID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": aiBusyErr})
		return
	}
	defer h.endAI(u.ID)

	msgs := []ai.Message{{Role: "system", Content: tutorSystemPrompt(s, body.Index)}}
	for _, m := range s.Tutor[body.Index] {
		msgs = append(msgs, ai.Message{Role: m.Role, Content: m.Content})
	}
	msgs = append(msgs, ai.Message{Role: "user", Content: message})

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	reply, usage, err := h.client.CompleteUsage(ctx, h.effectiveConfig(s), msgs, 0.4, false)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "o tutor não conseguiu responder agora — tente novamente em instantes"})
		return
	}
	reply = capTutorMessage(strings.TrimSpace(reply))
	if reply == "" {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "o tutor retornou uma resposta vazia — tente novamente"})
		return
	}
	thread := append(s.Tutor[body.Index],
		session.Message{Role: "user", Content: message},
		session.Message{Role: "assistant", Content: reply})
	if len(thread) > maxTutorMessages {
		thread = thread[len(thread)-maxTutorMessages:]
	}
	if s.Tutor == nil {
		s.Tutor = map[int][]session.Message{}
	}
	s.Tutor[body.Index] = thread
	h.persistQuiz(r, s)
	_ = h.recordAIUsage(r.Context(), u.ID, int64(usage.Total()), "chat", 0, s.ActiveQuizID, map[string]any{"test_id": testID, "question": body.Index})
	render(w, r, components.TutorThread(thread))
}

func tutorSystemPrompt(s *session.Session, idx int) string {
	q := s.Questions[idx]
	letterAt := func(i int) string { return string(rune('A' + i)) }
	var b strings.Builder
	b.WriteString("Você é um tutor particular brasileiro que prepara estudantes para o ENEM. Responda sempre em português do Brasil, de forma clara, direta e acolhedora, ajudando o aluno a entender a questão abaixo. Seja conciso (no máximo alguns parágrafos curtos).\n\n")
	fmt.Fprintf(&b, "Tema do teste: %s\n\nQuestão:\n%s\n", s.Config.Topic, q.Text)
	if q.Context != "" {
		fmt.Fprintf(&b, "Contexto: %s\n", q.Context)
	}
	b.WriteString("Alternativas:\n")
	for oi, opt := range q.Options {
		fmt.Fprintf(&b, "%s) %s\n", letterAt(oi), opt)
	}
	fmt.Fprintf(&b, "Alternativa correta: %s\n", letterAt(q.Correct))
	if q.Explanation != "" {
		fmt.Fprintf(&b, "Explicação registrada: %s\n", q.Explanation)
	}
	if idx < len(s.Answers) && s.Answers[idx] >= 0 {
		fmt.Fprintf(&b, "Resposta do aluno: %s\n", letterAt(s.Answers[idx]))
	}
	return b.String()
}
