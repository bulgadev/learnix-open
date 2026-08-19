package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/session"
)

// errQuizUnanswered is returned when a quiz still has blank answers and the
// submission did not allow them (only exam mode does).
var errQuizUnanswered = errors.New("unanswered questions remain")

// examExpired reports whether an exam-simulation attempt ran out of time.
func examExpired(s *session.Session) bool {
	return s.Exam && s.ExamDeadline != nil && !time.Now().Before(*s.ExamDeadline)
}

// nextExamIndex returns the next unanswered question index after from,
// wrapping around the end; from itself when every question is answered.
func nextExamIndex(answers []int, from, total int) int {
	for step := 1; step <= total; step++ {
		j := (from + step) % total
		if j >= len(answers) || answers[j] < 0 {
			return j
		}
	}
	return from
}

// finishQuiz transitions an in-progress quiz to results and runs the one-time
// finish side effects: ranked leaderboard record, telemetry, and (for attempts
// owned by a test definition) refreshed observations. allowUnanswered lets
// exam-mode submission and timeout finalization grade blanks as wrong.
func (h *Handler) finishQuiz(ctx context.Context, userID int64, s *session.Session, testID int64, allowUnanswered bool) error {
	if !allowUnanswered && firstUnanswered(s.Answers, len(s.Questions)) >= 0 {
		return errQuizUnanswered
	}
	wasFinished := s.FinishedAt != nil
	s.Phase = "results"
	if err := h.persistQuizState(ctx, userID, s); err != nil {
		return fmt.Errorf("persist finished quiz: %w", err)
	}
	if wasFinished {
		return nil
	}
	if s.QuizMode == quizModeRanked {
		if err := h.leaderboard.Record(ctx, db.RankedResult{
			QuizID: s.ActiveQuizID, UserID: userID, Topic: s.Config.Topic, Preset: s.QuizPreset,
			Total: len(s.Questions), Correct: s.Score(), ScoreCents: s.NormalizedScoreCents,
			WeightCents: s.QuizWeightCents, WeightedScoreCents: s.RankedScoreCents,
			FinishedAt: *s.FinishedAt,
		}); err != nil {
			return fmt.Errorf("record ranked result: %w", err)
		}
	}
	delta := db.TelemetryDelta{QuizzesCompleted: 1}
	if s.QuizMode == quizModeRanked {
		delta.RankedQuizzesCompleted = 1
	}
	h.recordTelemetry(ctx, db.TelemetryEvent{
		UserID: userID, StudyID: s.StudyID, QuizID: s.ActiveQuizID,
		Type: telemetryQuizCompleted,
		Metadata: map[string]any{
			"test_id": testID, "mode": s.QuizMode, "preset": s.QuizPreset, "exam": s.Exam,
			"score_cents": s.NormalizedScoreCents, "weighted_score_cents": s.RankedScoreCents,
		},
		Delta: delta,
	})
	if testID > 0 {
		_ = h.tests.UpdateObservations(ctx, userID, testID, testObservations(s))
	}
	return nil
}

// finalizeExpiredExam finishes the attempt once its deadline has passed. It
// reports whether the exam expired; when it returns (true, nil) the session
// phase is already "results".
func (h *Handler) finalizeExpiredExam(r *http.Request, s *session.Session, testID int64) (bool, error) {
	if !examExpired(s) {
		return false, nil
	}
	u := auth.UserFromContext(r.Context())
	return true, h.finishQuiz(r.Context(), u.ID, s, testID, true)
}

// rejectExpiredExam writes a client-facing response for an attempt whose
// deadline has passed and reports whether the caller must stop. Used by
// mutation endpoints; page renders use finalizeExpiredExam directly so they
// can show the results instead of an error.
func (h *Handler) rejectExpiredExam(w http.ResponseWriter, r *http.Request, s *session.Session, testID int64) bool {
	expired, err := h.finalizeExpiredExam(r, s, testID)
	if !expired {
		return false
	}
	if err != nil {
		http.Error(w, "erro ao finalizar prova", http.StatusInternalServerError)
		return true
	}
	http.Error(w, "tempo esgotado: prova entregue automaticamente", http.StatusConflict)
	return true
}

// examMutation applies goto/flag/answer actions to an exam-simulation attempt
// and re-renders the whole exam surface (countdown + palette + card). There is
// no reanswer/next in exam mode: navigation is free and feedback waits for
// submission.
func (h *Handler) examMutation(w http.ResponseWriter, r *http.Request, s *session.Session, testID int64, baseURL, action string, standalone bool) {
	idx, _ := strconv.Atoi(r.FormValue("index"))
	if idx < 0 || idx >= len(s.Questions) {
		http.Error(w, "índice inválido", http.StatusBadRequest)
		return
	}
	switch action {
	case "goto":
		s.Current = idx
		h.persistQuiz(r, s)
	case "flag":
		if len(s.Flags) != len(s.Questions) {
			s.Flags = make([]bool, len(s.Questions))
		}
		s.Flags[idx] = !s.Flags[idx]
		s.Current = idx
		h.persistQuiz(r, s)
	case "answer":
		ans, _ := strconv.Atoi(r.FormValue("answer"))
		if ans < 0 || ans >= len(s.Questions[idx].Options) {
			http.Error(w, "resposta inválida", http.StatusBadRequest)
			return
		}
		if len(s.Answers) != len(s.Questions) {
			s.Answers = normalizeAnswers(s.Answers, len(s.Questions))
		}
		s.Answers[idx] = ans
		u := auth.UserFromContext(r.Context())
		delta := db.TelemetryDelta{QuestionsAnswered: 1}
		if ans == s.Questions[idx].Correct {
			delta.CorrectAnswers = 1
		}
		h.recordTelemetry(r.Context(), db.TelemetryEvent{
			UserID: u.ID, StudyID: s.StudyID, QuizID: s.ActiveQuizID,
			Type: telemetryQuizAnswered, ValueInt: int64(idx),
			Metadata: map[string]any{"test_id": testID, "exam": true},
			Delta:    delta,
		})
		idx = nextExamIndex(s.Answers, idx, len(s.Questions))
		s.Current = idx
		h.persistQuiz(r, s)
	default:
		http.Error(w, "ação indisponível no simulado", http.StatusBadRequest)
		return
	}
	render(w, r, components.ExamBoard(baseURL, s, idx, standalone))
}
