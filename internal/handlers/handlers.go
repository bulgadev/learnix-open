package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"learnix/internal/ai"
	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/mindmap"
	"learnix/internal/quizgen"
	"learnix/internal/quizjobs"
	"learnix/internal/session"
	"learnix/internal/websearch"
)

type Handler struct {
	client        *ai.Client
	Preset        session.Config
	users         *db.UserRepo
	sessions      *db.SessionRepo
	configs       *db.ConfigRepo
	studies       *db.StudyRepo
	tests         *db.TestRepo
	quizzes       *db.QuizRepo
	files         *db.FileRepo
	chats         *db.ChatRepo
	chatTurns     *db.ChatTurnRepo
	highlights    *db.HighlightRepo
	quotas        *db.QuotaRepo
	profiles      *db.ProfileRepo
	telemetry     *db.TelemetryRepo
	leaderboard   *db.LeaderboardRepo
	mindMaps      mindmap.Repository
	sessionSecret string
	TavilyKey     string
	tavilyBase    string
	// AdminEmail is the account allowed into /admin; empty disables it.
	AdminEmail string
	// aiMu/aiInFlight enforce one in-flight AI call per user so concurrent
	// requests cannot race the quota gate.
	aiMu       sync.Mutex
	aiInFlight map[int64]bool
	quizJobs   *quizjobs.Manager
	// Debug allows clients to skip web research with web=false in quizzes
	// and tutor chats.
	Debug bool
	// Quiz*Model optionally override the model used by each quiz-generation
	// stage (empty = the session/preset model). Cost lever: run research on a
	// strong model and author/review on a cheaper one without code changes.
	QuizResearchModel string
	QuizAuthorModel   string
	QuizReviewModel   string
}

const defaultNewUserQuota int64 = 250_000

func New(preset session.Config, users *db.UserRepo, sessions *db.SessionRepo, configs *db.ConfigRepo, studies *db.StudyRepo, tests *db.TestRepo, quizzes *db.QuizRepo, files *db.FileRepo, chats *db.ChatRepo, chatTurns *db.ChatTurnRepo, highlights *db.HighlightRepo, quotas *db.QuotaRepo, profiles *db.ProfileRepo, telemetry *db.TelemetryRepo, leaderboard *db.LeaderboardRepo, mindMaps mindmap.Repository, sessionSecret, tavilyKey, adminEmail string) *Handler {
	return &Handler{
		client:        ai.New(),
		Preset:        preset,
		users:         users,
		sessions:      sessions,
		configs:       configs,
		studies:       studies,
		tests:         tests,
		quizzes:       quizzes,
		files:         files,
		chats:         chats,
		chatTurns:     chatTurns,
		highlights:    highlights,
		quotas:        quotas,
		profiles:      profiles,
		telemetry:     telemetry,
		leaderboard:   leaderboard,
		mindMaps:      mindMaps,
		sessionSecret: sessionSecret,
		TavilyKey:     tavilyKey,
		AdminEmail:    adminEmail,
		aiInFlight:    make(map[int64]bool),
		quizJobs:      quizjobs.NewManager(30 * time.Minute),
	}
}

// quotaFor returns the user's quota row, or nil when they have none
// (which counts as exhausted — the default allowance is zero).
func (h *Handler) quotaFor(ctx context.Context, u *db.User) *db.Quota {
	if u == nil {
		return nil
	}
	q, _ := h.quotas.Get(ctx, u.ID)
	return q
}

// isAdmin reports whether u is the configured admin account.
func (h *Handler) isAdmin(u *db.User) bool {
	return h.AdminEmail != "" && u != nil && strings.EqualFold(u.Email, h.AdminEmail)
}

// quotaExhaustedErr is the message users see when their token budget runs
// out. Quotas are granted by the admin, so point them there.
const quotaExhaustedErr = "Sua cota de tokens acabou. Peça ao administrador para aumentar seu limite. Me chama no zapzap que eu do mais token vro 🥀🥀🙏"

// aiBusyErr is returned when the user already has an AI call in flight.
const aiBusyErr = "Você já tem uma requisição de IA em andamento — aguarde alguns instantes."

// startAI marks an AI call as in flight for the user; it reports false when
// another call is already running (per-user single-flight).
func (h *Handler) startAI(userID int64) bool {
	h.aiMu.Lock()
	defer h.aiMu.Unlock()
	if h.aiInFlight[userID] {
		return false
	}
	h.aiInFlight[userID] = true
	return true
}

// endAI releases the user's in-flight slot.
func (h *Handler) endAI(userID int64) {
	h.aiMu.Lock()
	defer h.aiMu.Unlock()
	delete(h.aiInFlight, userID)
}

func render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := c.Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// studyIDParam parses the {id} URL param.
func studyIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// loadStudy reconstructs a session.Session from the study row owned by the
// authenticated user. Returns nil if the study does not exist or is not owned
// by the user (callers respond 404).
func (h *Handler) loadStudy(r *http.Request, id int64) *session.Session {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		return nil
	}
	st, err := h.studies.Get(r.Context(), id)
	if err != nil || st == nil || st.UserID != u.ID {
		return nil
	}
	s := &session.Session{
		StudyID: st.ID,
		Config: session.Config{
			BaseURL: st.BaseURL,
			APIKey:  st.APIKey,
			Model:   st.Model,
			Topic:   st.Topic,
		},
		Phase:     st.Phase,
		Feedback:  st.Feedback,
		Reviewing: st.Reviewing,
	}
	if s.Phase == "quiz" || s.Phase == "results" {
		if q, _ := h.quizzes.GetLatestByStudy(r.Context(), st.ID); q != nil {
			s.Questions = q.Questions
			s.Answers = q.Answers
			s.Confidence = q.Confidence
			s.Assessments = q.Assessments
			s.TraceJSON = q.TraceJSON
			if q.TraceJSON != "" {
				_ = json.Unmarshal([]byte(q.TraceJSON), &s.Trace)
			}
			s.Current = q.Current
			s.ActiveQuizID = q.ID
			s.TestID = q.TestID
			s.AdaptiveFromID = q.AdaptiveFromID
			s.Exam = q.Exam
			s.ExamDeadline = q.ExamDeadline
			s.Flags = q.Flags
			s.Tutor = parseTutorJSON(q.TutorJSON)
			s.QuizMode = q.Mode
			if s.QuizMode == "" {
				s.QuizMode = "practice"
			}
			s.QuizPreset = q.Preset
			s.QuizWeightCents = q.WeightCents
			s.NormalizedScoreCents = q.ScoreCents
			s.RankedScoreCents = q.WeightedScoreCents
			s.FinishedAt = q.FinishedAt
		}
	}
	return s
}

// persist writes the durable study state (phase + feedback + reviewing) to
// the DB. Chat history lives in the chat_messages table now.
func (h *Handler) persist(r *http.Request, s *session.Session) {
	u := auth.UserFromContext(r.Context())
	if u == nil || s.StudyID == 0 {
		return
	}
	_ = h.persistStudyState(r.Context(), u.ID, s)
}

func (h *Handler) persistStudyState(ctx context.Context, userID int64, s *session.Session) error {
	return h.studies.Update(ctx, &db.Study{
		ID:        s.StudyID,
		UserID:    userID,
		Phase:     s.Phase,
		Feedback:  s.Feedback,
		Reviewing: s.Reviewing,
	})
}

// persistQuiz saves the in-progress or finished quiz to the DB.
func (h *Handler) persistQuiz(r *http.Request, s *session.Session) {
	u := auth.UserFromContext(r.Context())
	if u == nil || len(s.Questions) == 0 {
		return
	}
	_ = h.persistQuizState(r.Context(), u.ID, s)
}

func (h *Handler) persistQuizState(ctx context.Context, userID int64, s *session.Session) error {
	q := &db.Quiz{
		ID:                 s.ActiveQuizID,
		UserID:             userID,
		StudyID:            s.StudyID,
		TestID:             s.TestID,
		Topic:              s.Config.Topic,
		Phase:              s.Phase,
		Current:            s.Current,
		Questions:          s.Questions,
		Answers:            s.Answers,
		Confidence:         s.Confidence,
		Assessments:        s.Assessments,
		Mode:               s.QuizMode,
		Preset:             s.QuizPreset,
		WeightCents:        s.QuizWeightCents,
		ScoreCents:         s.NormalizedScoreCents,
		WeightedScoreCents: s.RankedScoreCents,
		AdaptiveFromID:     s.AdaptiveFromID,
		Exam:               s.Exam,
		ExamDeadline:       s.ExamDeadline,
		Flags:              s.Flags,
		TutorJSON:          marshalTutorJSON(s.Tutor),
		FinishedAt:         s.FinishedAt,
	}
	if s.TraceJSON == "" {
		if b, err := json.Marshal(s.Trace); err == nil && string(b) != "null" {
			s.TraceJSON = string(b)
		}
	}
	q.TraceJSON = s.TraceJSON
	if s.Phase == "results" {
		q.Score = s.Score()
		q.Total = len(s.Questions)
		q.ScoreCents = s.ScoreCents()
		q.WeightedScoreCents = s.WeightedScoreCents()
		if q.FinishedAt == nil {
			now := time.Now()
			q.FinishedAt = &now
		}
		s.NormalizedScoreCents = q.ScoreCents
		s.RankedScoreCents = q.WeightedScoreCents
		s.FinishedAt = q.FinishedAt
	}
	if err := h.quizzes.Save(ctx, q); err != nil {
		return err
	}
	s.ActiveQuizID = q.ID
	return nil
}

// Tutor threads are capped so a long-lived conversation cannot grow the quiz
// row without bound: only the most recent messages per question survive.
const (
	maxTutorMessages     = 10
	maxTutorMessageRunes = 4000
)

func parseTutorJSON(value string) map[int][]session.Message {
	if value == "" {
		return nil
	}
	var tutor map[int][]session.Message
	if err := json.Unmarshal([]byte(value), &tutor); err != nil {
		return nil
	}
	return tutor
}

func marshalTutorJSON(tutor map[int][]session.Message) string {
	if len(tutor) == 0 {
		return ""
	}
	value, err := json.Marshal(tutor)
	if err != nil {
		return ""
	}
	return string(value)
}

func capTutorMessage(content string) string {
	runes := []rune(content)
	if len(runes) <= maxTutorMessageRunes {
		return content
	}
	return string(runes[:maxTutorMessageRunes])
}

// effectiveConfig returns the session config, falling back to the server preset
// for any blank field. The preset API key is only attached when the request
// goes to the preset endpoint itself — never to a user-supplied base URL, so a
// study cannot exfiltrate the server key to an arbitrary host (SSRF).
func (h *Handler) effectiveConfig(s *session.Session) session.Config {
	c := s.Config
	if c.BaseURL == "" {
		c.BaseURL = h.Preset.BaseURL
	}
	if c.APIKey == "" && sameEndpoint(c.BaseURL, h.Preset.BaseURL) {
		c.APIKey = h.Preset.APIKey
	}
	if c.Model == "" {
		c.Model = h.Preset.Model
	}
	return c
}

func sameEndpoint(a, b string) bool {
	return strings.TrimRight(a, "/") == strings.TrimRight(b, "/")
}

// Home renders the dashboard: new-study form plus the user's studies. It never
// redirects into an active study — the homepage is always the homepage. All AI
// traffic runs on the server preset key (never rendered anywhere); users only
// pick a topic.
func (h *Handler) Home(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	items, _ := h.studies.ListByUser(r.Context(), u.ID)
	render(w, r, components.Home(items, u, h.quotaFor(r.Context(), u), h.isAdmin(u)))
}

func (h *Handler) initialPrompt(s *session.Session) string {
	base := "Apresente uma visão geral completa e bem estruturada deste conteúdo, cobrindo os conceitos fundamentais com exemplos práticos. Use Markdown."
	if s.Feedback != "" {
		return "O aluno errou recentemente questões sobre: " + s.Feedback + ". Foque em esclarecer bem esses pontos. " + base
	}
	return base
}

// stemsCollector dedupes question stems across previous quiz attempts so the
// author stage can be told which questions the learner already saw.
type stemsCollector struct {
	seen map[string]bool
	out  []string
	cap  int
}

func newStemsCollector(capacity int) *stemsCollector {
	return &stemsCollector{seen: make(map[string]bool), cap: capacity}
}

func (c *stemsCollector) add(qs []session.Question) {
	for _, q := range qs {
		if len(c.out) >= c.cap {
			return
		}
		stem := strings.TrimSpace(q.Text)
		if stem == "" {
			continue
		}
		key := strings.ToLower(stem)
		if c.seen[key] {
			continue
		}
		c.seen[key] = true
		c.out = append(c.out, stem)
	}
}

func (c *stemsCollector) stems() []string { return c.out }

// QuizStart queues the web-researched quiz pipeline and returns immediately.
// The client polls QuizJobStatus so the long-running AI work is not tied to an
// HTTP connection.
func (h *Handler) QuizStart(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.NotFound(w, r)
		return
	}
	if h.quotaFor(r.Context(), u).Exhausted() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": quotaExhaustedErr})
		return
	}

	var body struct {
		Preset string `json:"preset"`
		Mode   string `json:"mode"`
		Web    *bool  `json:"web"`
		Exam   bool   `json:"exam"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	preset, ok := quizPreset(body.Preset)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "preset inválido"})
		return
	}
	mode, ok := quizMode(body.Mode)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "modo inválido"})
		return
	}

	web := true
	if body.Web != nil {
		web = *body.Web
	}
	if !h.Debug {
		web = true
	}

	var client *websearch.Client
	if web {
		if h.TavilyKey == "" {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{
				"error": "A geração de questões usa pesquisa na web: configure uma chave Tavily (TAVILY_API_KEY) e tente novamente.",
			})
			return
		}
		client = websearch.NewClient(h.TavilyKey)
		if h.tavilyBase != "" {
			client = websearch.NewClientWithBase(h.TavilyKey, h.tavilyBase)
		}
	}

	cfg := h.effectiveConfig(s)
	// Anti-repetition: remember the stems of the study's previous quiz so the
	// author does not recycle questions the learner already answered.
	stems := newStemsCollector(30)
	if prev, _ := h.quizzes.GetLatestByStudy(r.Context(), s.StudyID); prev != nil {
		stems.add(prev.Questions)
	}
	prevStems := stems.stems()
	if !h.startAI(u.ID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": aiBusyErr})
		return
	}
	studyID, userID, feedback := s.StudyID, u.ID, s.Feedback
	job := h.quizJobs.Start(userID, studyID, func(ctx context.Context, report func(quizjobs.Update)) (string, error) {
		defer h.endAI(userID)
		jobCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		defer cancel()
		qs, _, trace, err := quizgen.GenerateWithTrace(jobCtx, h.client, cfg, client,
			quizgen.Spec{Topic: cfg.Topic, Feedback: feedback, Count: preset.Count, Web: web,
				PrevStems: prevStems, ResearchModel: h.QuizResearchModel, AuthorModel: h.QuizAuthorModel, ReviewModel: h.QuizReviewModel},
			func(p quizgen.Progress) {
				u := quizjobs.Update{Stage: p.Stage, Level: p.Level, Message: p.Message}
				if p.Metrics != nil {
					u.Metrics = true
					u.Current = p.Metrics.Current
					u.Total = p.Metrics.Total
					u.Sources = p.Metrics.Sources
					u.Searches = p.Metrics.Searches
					u.Pages = p.Metrics.Pages
					u.ModelCalls = p.Metrics.ModelCalls
					u.Tokens = p.Metrics.Tokens
					u.Attempt = p.Metrics.Attempt
					u.MaxAttempts = p.Metrics.MaxAttempts
				}
				report(u)
			})
		if uerr := h.recordAIUsage(jobCtx, userID, int64(trace.TokenUsage.Total()), "quiz", studyID, 0, map[string]any{
			"mode": mode, "preset": body.Preset, "outcome": outcomeForError(err),
		}); uerr != nil {
			log.Printf("quota: record quiz usage for user %d: %v", userID, uerr)
		}
		if err != nil {
			h.recordTelemetry(jobCtx, db.TelemetryEvent{
				UserID: userID, StudyID: studyID, Type: telemetryQuizGenerationFailed,
				Metadata: map[string]any{"mode": mode, "preset": body.Preset},
				Delta:    db.TelemetryDelta{QuizGenerationFailures: 1},
			})
			return "", err
		}
		completed := &session.Session{
			StudyID: studyID, Config: cfg, Phase: "quiz", Feedback: feedback,
			Questions: qs, Answers: make([]int, len(qs)),
			Confidence: make([]int, len(qs)), Assessments: make([]string, len(qs)), Trace: trace,
			QuizMode: mode, QuizPreset: preset.Name, QuizWeightCents: preset.WeightCents,
		}
		if b, merr := json.Marshal(trace); merr == nil {
			completed.TraceJSON = string(b)
		}
		for i := range completed.Answers {
			completed.Answers[i] = -1
		}
		if body.Exam {
			deadline := time.Now().Add(time.Duration(preset.Minutes) * time.Minute)
			completed.Exam = true
			completed.ExamDeadline = &deadline
			completed.Flags = make([]bool, len(qs))
		}
		report(quizjobs.Update{Stage: "save", Message: "Questões prontas; salvando seu quiz..."})
		if err := h.quizzes.DeleteByStudyInProgress(jobCtx, studyID); err != nil {
			return "", err
		}
		if err := h.persistStudyState(jobCtx, userID, completed); err != nil {
			return "", err
		}
		if err := h.persistQuizState(jobCtx, userID, completed); err != nil {
			return "", err
		}
		h.recordTelemetry(jobCtx, db.TelemetryEvent{
			UserID: userID, StudyID: studyID, QuizID: completed.ActiveQuizID,
			Type:     telemetryQuizGenerationSucceeded,
			Metadata: map[string]any{"mode": mode, "preset": preset.Name, "questions": len(qs)},
			Delta:    db.TelemetryDelta{QuizzesStarted: 1},
		})
		report(quizjobs.Update{Stage: "save", Message: "Quiz salvo. Abrindo suas questões..."})
		return fmt.Sprintf("/study/%d", studyID), nil
	})

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": job.ID, "status": job.Status,
		"status_url": fmt.Sprintf("/study/%d/quiz/jobs/%s", studyID, job.ID),
	})
}

func (h *Handler) QuizJobStatus(w http.ResponseWriter, r *http.Request) {
	studyID, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	u := auth.UserFromContext(r.Context())
	jobID := chi.URLParam(r, "jobID")
	if u == nil || jobID == "" {
		http.NotFound(w, r)
		return
	}
	job, ok := h.quizJobs.Get(jobID, u.ID, studyID)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (h *Handler) QuizAnswer(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if s.Phase != "quiz" {
		http.Error(w, "quiz não está em andamento", http.StatusConflict)
		return
	}
	if h.rejectExpiredExam(w, r, s, s.TestID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	idx, _ := strconv.Atoi(r.FormValue("index"))
	ans, _ := strconv.Atoi(r.FormValue("answer"))
	if idx < 0 || idx >= len(s.Questions) {
		http.Error(w, "índice inválido", http.StatusBadRequest)
		return
	}
	if idx != s.Current && !s.Exam {
		http.Error(w, "esta não é a questão atual", http.StatusConflict)
		return
	}
	if s.Exam {
		h.examMutation(w, r, s, s.TestID, fmt.Sprintf("/study/%d", s.StudyID), "answer", false)
		return
	}
	if ans < 0 || ans >= len(s.Questions[idx].Options) {
		http.Error(w, "alternativa inválida", http.StatusBadRequest)
		return
	}
	if len(s.Confidence) != len(s.Questions) {
		s.Confidence = make([]int, len(s.Questions))
	}
	if len(s.Answers) != len(s.Questions) {
		answers := make([]int, len(s.Questions))
		for i := range answers {
			answers[i] = -1
		}
		copy(answers, s.Answers)
		s.Answers = answers
	}
	confidence, _ := strconv.Atoi(r.FormValue("confidence"))
	if !session.ValidConfidence(confidence) {
		http.Error(w, "confiança inválida", http.StatusBadRequest)
		return
	}
	s.Confidence[idx] = confidence
	s.Answers[idx] = ans
	h.persistQuiz(r, s)
	u := auth.UserFromContext(r.Context())
	delta := db.TelemetryDelta{QuestionsAnswered: 1}
	if ans == s.Questions[idx].Correct {
		delta.CorrectAnswers = 1
	}
	h.recordTelemetry(r.Context(), db.TelemetryEvent{
		UserID: u.ID, StudyID: s.StudyID, QuizID: s.ActiveQuizID,
		Type: telemetryQuizAnswered, ValueInt: int64(idx),
		Metadata: map[string]any{"correct": ans == s.Questions[idx].Correct, "confidence": confidence},
		Delta:    delta,
	})
	isLast := idx >= len(s.Questions)-1
	render(w, r, components.ReviewedCard(fmt.Sprintf("/study/%d", s.StudyID), s.StudyID, idx, s.Questions[idx], ans, len(s.Questions), isLast, false, nil))
}

// QuizReanswer clears only the active question so the learner can reconsider
// it after returning from another app or asking the tutor for help.
func (h *Handler) QuizReanswer(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if s.Phase != "quiz" || len(s.Questions) == 0 {
		http.Error(w, "quiz não está em andamento", http.StatusConflict)
		return
	}
	if s.Exam {
		http.Error(w, "ação indisponível no simulado", http.StatusBadRequest)
		return
	}
	if h.rejectExpiredExam(w, r, s, s.TestID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	idx, err := strconv.Atoi(r.FormValue("index"))
	if err != nil || idx < 0 || idx >= len(s.Questions) {
		http.Error(w, "índice inválido", http.StatusBadRequest)
		return
	}
	if idx != s.Current {
		http.Error(w, "esta não é a questão atual", http.StatusConflict)
		return
	}
	if len(s.Answers) != len(s.Questions) {
		s.Answers = normalizeAnswers(s.Answers, len(s.Questions))
	}
	if len(s.Confidence) != len(s.Questions) {
		s.Confidence = make([]int, len(s.Questions))
	}
	s.Answers[idx] = -1
	s.Confidence[idx] = 0
	h.persistQuiz(r, s)
	render(w, r, components.QuestionCard(fmt.Sprintf("/study/%d", s.StudyID), s.ActiveQuizID, idx, s.Questions[idx], len(s.Questions), false))
}

// QuizDiagnostic stores the student's post-result explanation of an answer.
// This self-report prevents an MCQ result from pretending to distinguish
// inattention from a missing concept on its own.
func (h *Handler) QuizDiagnostic(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
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
	h.recordTelemetry(r.Context(), db.TelemetryEvent{
		UserID: u.ID, StudyID: s.StudyID, QuizID: s.ActiveQuizID,
		Type: telemetryQuizDiagnostic, Metadata: map[string]any{"assessment": assessment},
		Delta: db.TelemetryDelta{DiagnosticsSubmitted: 1},
	})
	http.Redirect(w, r, fmt.Sprintf("/study/%d", s.StudyID), http.StatusSeeOther)
}

func (h *Handler) QuizNext(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if len(s.Questions) == 0 {
		http.NotFound(w, r)
		return
	}
	if s.Phase != "quiz" {
		http.Error(w, "quiz não está em andamento", http.StatusConflict)
		return
	}
	if s.Exam {
		http.Error(w, "ação indisponível no simulado", http.StatusBadRequest)
		return
	}
	if s.Current < 0 || s.Current >= len(s.Questions) {
		http.Error(w, "questão atual inválida", http.StatusConflict)
		return
	}
	if len(s.Answers) <= s.Current || s.Answers[s.Current] < 0 {
		http.Error(w, "responda a questão atual antes de avançar", http.StatusConflict)
		return
	}
	if s.Current < len(s.Questions)-1 {
		s.Current++
	}
	h.persistQuiz(r, s)
	if len(s.Answers) > s.Current && s.Answers[s.Current] >= 0 {
		render(w, r, components.ReviewedCard(fmt.Sprintf("/study/%d", s.StudyID), s.StudyID, s.Current, s.Questions[s.Current], s.Answers[s.Current], len(s.Questions), s.Current == len(s.Questions)-1, false, nil))
		return
	}
	render(w, r, components.QuestionCard(fmt.Sprintf("/study/%d", s.StudyID), s.ActiveQuizID, s.Current, s.Questions[s.Current], len(s.Questions), false))
}

// QuizGoto and QuizFlag are exam-only navigation actions; the instant-feedback
// path is strictly linear and rejects them.
func (h *Handler) QuizGoto(w http.ResponseWriter, r *http.Request) {
	h.studyQuizExamAction(w, r, "goto")
}

func (h *Handler) QuizFlag(w http.ResponseWriter, r *http.Request) {
	h.studyQuizExamAction(w, r, "flag")
}

func (h *Handler) studyQuizExamAction(w http.ResponseWriter, r *http.Request, action string) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil || s.Phase != "quiz" || !s.Exam {
		http.NotFound(w, r)
		return
	}
	if h.rejectExpiredExam(w, r, s, s.TestID) {
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return
	}
	h.examMutation(w, r, s, s.TestID, fmt.Sprintf("/study/%d", s.StudyID), action, false)
}

func (h *Handler) QuizResult(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if (s.Phase != "quiz" && !(s.Phase == "results" && s.FinishedAt != nil)) || len(s.Questions) == 0 {
		http.Error(w, "quiz não está em andamento", http.StatusConflict)
		return
	}
	u := auth.UserFromContext(r.Context())
	if s.Phase == "quiz" {
		// Exam attempts may be handed in with blanks (they grade as wrong);
		// the practice flow sends the learner back to the first unanswered
		// question instead of finishing.
		if !s.Exam {
			if idx := firstUnanswered(s.Answers, len(s.Questions)); idx >= 0 {
				s.Current = idx
				h.persistQuiz(r, s)
				http.Redirect(w, r, fmt.Sprintf("/study/%d", s.StudyID), http.StatusSeeOther)
				return
			}
		}
		s.Phase = "results"
		if err := h.persistStudyState(r.Context(), u.ID, s); err != nil {
			http.Error(w, "erro ao salvar resultado", http.StatusInternalServerError)
			return
		}
		if err := h.finishQuiz(r.Context(), u.ID, s, s.TestID, s.Exam); err != nil {
			http.Error(w, "erro ao salvar resultado", http.StatusInternalServerError)
			return
		}
	}
	score := s.Score()
	total := len(s.Questions)
	render(w, r, components.Result(s, score, total, score == total, u, h.quotaFor(r.Context(), u), h.isAdmin(u), fmt.Sprintf("/study/%d", s.StudyID), false))
}

func normalizeAnswers(answers []int, total int) []int {
	out := make([]int, total)
	for i := range out {
		out[i] = -1
	}
	copy(out, answers)
	return out
}

func firstUnanswered(answers []int, total int) int {
	for i := 0; i < total; i++ {
		if i >= len(answers) || answers[i] < 0 {
			return i
		}
	}
	return -1
}

func (h *Handler) StudyReset(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	var wrong []string
	for _, idx := range s.WrongTopics() {
		wrong = append(wrong, s.Questions[idx].Text)
	}
	s.Feedback = strings.Join(wrong, "; ")
	s.Reviewing = true
	s.Phase = "study"
	s.Questions = nil
	s.Answers = nil
	s.Confidence = nil
	s.Assessments = nil
	s.Trace = session.QuizTrace{}
	s.TraceJSON = ""
	s.ActiveQuizID = 0
	h.persist(r, s)
	h.recordTelemetry(r.Context(), db.TelemetryEvent{
		UserID: auth.UserFromContext(r.Context()).ID, StudyID: s.StudyID,
		Type: telemetryStudyReset, Delta: db.TelemetryDelta{StudyResets: 1},
	})
	_ = h.quizzes.DeleteByStudyInProgress(r.Context(), s.StudyID)
	// Fresh chat; generation starts only after the user explicitly sends a message.
	_ = h.chats.Create(r.Context(), &db.Chat{StudyID: s.StudyID, Title: "Nova conversa"})
	http.Redirect(w, r, fmt.Sprintf("/study/%d", s.StudyID), http.StatusSeeOther)
}

// DeleteStudy removes a study and returns to the homepage.
func (h *Handler) DeleteStudy(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = h.studies.Delete(r.Context(), id, u.ID)
	http.Redirect(w, r, "/", http.StatusFound)
}
