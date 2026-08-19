package handlers

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
)

const (
	quizModePractice = "practice"
	quizModeRanked   = "ranked"

	telemetryStudyCreated            = "study_created"
	telemetryTestCreated             = "test_created"
	telemetryStudyReset              = "study_reset"
	telemetryQuizGenerationFailed    = "quiz_generation_failed"
	telemetryQuizGenerationSucceeded = "quiz_generation_succeeded"
	telemetryQuizAnswered            = "quiz_answered"
	telemetryQuizDiagnostic          = "quiz_diagnostic_submitted"
	telemetryQuizCompleted           = "quiz_completed"
	telemetryChatCompleted           = "chat_completed"
	telemetryChatFailed              = "chat_failed"
	telemetryChatTurnCreated         = "chat_turn_created"
	telemetryChatTurnRetried         = "chat_turn_retried"
	telemetryAIUsage                 = "ai_usage"
	telemetryUserRegistered          = "user_registered"
	telemetryUserLogin               = "user_login"
)

type quizPresetSpec struct {
	Name        string
	Count       int
	WeightCents int64
	// Minutes is the exam-simulation time limit for this preset.
	Minutes int
}

func quizPreset(name string) (quizPresetSpec, bool) {
	switch name {
	case "", "moderate":
		return quizPresetSpec{Name: "moderate", Count: 10, WeightCents: 100, Minutes: 15}, true
	case "fast":
		return quizPresetSpec{Name: "fast", Count: 5, WeightCents: 50, Minutes: 5}, true
	case "long":
		return quizPresetSpec{Name: "long", Count: 15, WeightCents: 200, Minutes: 30}, true
	default:
		return quizPresetSpec{}, false
	}
}

func quizMode(mode string) (string, bool) {
	if mode == "" {
		return quizModePractice, true
	}
	if mode == quizModePractice || mode == quizModeRanked {
		return mode, true
	}
	return "", false
}

func outcomeForError(err error) string {
	if err != nil {
		return "failed"
	}
	return "succeeded"
}

func (h *Handler) recordTelemetry(ctx context.Context, event db.TelemetryEvent) {
	if h.telemetry == nil {
		return
	}
	if err := h.telemetry.Record(ctx, event); err != nil {
		log.Printf("telemetry: %s: %v", event.Type, err)
	}
}

// recordAIUsage keeps the existing quota/usage_log behavior and emits the
// aggregate telemetry event for the same AI operation.
func (h *Handler) recordAIUsage(ctx context.Context, userID, tokens int64, kind string, studyID, quizID int64, metadata map[string]any) error {
	if err := h.quotas.AddUsage(ctx, userID, tokens, kind); err != nil {
		return err
	}
	if metadata == nil {
		metadata = make(map[string]any)
	}
	metadata["kind"] = kind
	metadata["tokens"] = tokens
	h.recordTelemetry(ctx, db.TelemetryEvent{
		UserID: userID, StudyID: studyID, QuizID: quizID,
		Type: telemetryAIUsage, ValueInt: tokens, Metadata: metadata,
		Delta: db.TelemetryDelta{AICalls: 1, TokensUsed: tokens},
	})
	return nil
}

type leaderboardPeriod struct {
	Name  string
	Month *string
	From  *time.Time
	To    *time.Time
}

func parseLeaderboardPeriod(r *http.Request) (leaderboardPeriod, error) {
	period := r.URL.Query().Get("period")
	if period == "" || period == "all" {
		return leaderboardPeriod{Name: "all"}, nil
	}
	if period != "month" {
		return leaderboardPeriod{}, errors.New("period deve ser all ou month")
	}
	month := r.URL.Query().Get("month")
	if month == "" {
		month = time.Now().UTC().Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, time.UTC)
	if err != nil {
		return leaderboardPeriod{}, errors.New("month deve estar no formato YYYY-MM")
	}
	end := start.AddDate(0, 1, 0)
	return leaderboardPeriod{Name: "month", Month: &month, From: &start, To: &end}, nil
}

func leaderboardLimit(r *http.Request) (int, error) {
	value := r.URL.Query().Get("limit")
	if value == "" {
		return 100, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > 100 {
		return 0, errors.New("limit deve estar entre 1 e 100")
	}
	return n, nil
}

type leaderboardJSONEntry struct {
	Rank      int     `json:"rank"`
	Slug      string  `json:"slug"`
	Username  string  `json:"username,omitempty"`
	Tag       string  `json:"tag,omitempty"`
	AvatarURL string  `json:"avatar_url,omitempty"`
	RankKey   string  `json:"rank_key,omitempty"`
	RankLabel string  `json:"rank_label,omitempty"`
	Value     float64 `json:"value"`
}

type leaderboardJSONResponse struct {
	Metric  string                 `json:"metric"`
	Period  string                 `json:"period"`
	Month   *string                `json:"month"`
	Entries []leaderboardJSONEntry `json:"entries"`
}

func (h *Handler) leaderboardJSON(w http.ResponseWriter, r *http.Request, metric string) {
	period, err := parseLeaderboardPeriod(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	limit, err := leaderboardLimit(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var entries []db.LeaderboardEntry
	switch metric {
	case "nota":
		entries, err = h.leaderboard.ListScore(r.Context(), period.From, period.To, limit)
	case "quizzes":
		entries, err = h.leaderboard.ListQuizzes(r.Context(), period.From, period.To, limit)
	case "tokens":
		entries, err = h.leaderboard.ListTokens(r.Context(), period.From, period.To, limit)
	default:
		err = errors.New("métrica inválida")
	}
	if err != nil {
		http.Error(w, "erro ao carregar leaderboard", http.StatusInternalServerError)
		return
	}
	response := leaderboardJSONResponse{Metric: metric, Period: period.Name, Month: period.Month}
	response.Entries = make([]leaderboardJSONEntry, 0, len(entries))
	for i, entry := range entries {
		value := float64(entry.Value)
		if metric == "nota" {
			value /= 100
		}
		jsonEntry := leaderboardJSONEntry{Rank: i + 1, Slug: entry.Slug, Value: value}
		if profile, profileErr := h.profiles.ViewByUser(r.Context(), entry.UserID); profileErr == nil && profile != nil {
			jsonEntry.Username = profile.Profile.Username
			jsonEntry.Tag = profile.Profile.Tag
			if profile.Visible(db.ProfileVisibilityAvatar, false) && len(profile.Details.AvatarData) > 0 {
				jsonEntry.AvatarURL = "/profile/" + url.PathEscape(profile.Profile.Slug) + "/avatar"
			}
			if profile.Visible(db.ProfileVisibilityRank, false) {
				jsonEntry.RankKey = profile.Rank.Key
				jsonEntry.RankLabel = profile.Rank.Label
			}
		}
		response.Entries = append(response.Entries, jsonEntry)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) LeaderboardPage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	render(w, r, components.LeaderboardPage(components.AuthedPageData("Leaderboard", "", "", u, h.quotaFor(r.Context(), u), h.isAdmin(u))))
}

func (h *Handler) LeaderboardScore(w http.ResponseWriter, r *http.Request) {
	h.leaderboardJSON(w, r, "nota")
}

func (h *Handler) LeaderboardQuizzes(w http.ResponseWriter, r *http.Request) {
	h.leaderboardJSON(w, r, "quizzes")
}

func (h *Handler) LeaderboardTokens(w http.ResponseWriter, r *http.Request) {
	h.leaderboardJSON(w, r, "tokens")
}
