package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrUsernameChangeCooldown = errors.New("username can only be changed once per month")
	ErrUsernameTaken          = errors.New("username tag is already in use")
)

const (
	ProfileVisibilityDisplayName          = "display_name"
	ProfileVisibilityAvatar               = "avatar"
	ProfileVisibilityBio                  = "bio"
	ProfileVisibilityRank                 = "rank"
	ProfileVisibilityAverageScore         = "average_score"
	ProfileVisibilityRankedQuizzes        = "ranked_quizzes"
	ProfileVisibilityQuizzesCompleted     = "quizzes_completed"
	ProfileVisibilityQuizzesStarted       = "quizzes_started"
	ProfileVisibilityStudiesCreated       = "studies_created"
	ProfileVisibilityStudyResets          = "study_resets"
	ProfileVisibilityQuestionsAnswered    = "questions_answered"
	ProfileVisibilityCorrectAnswers       = "correct_answers"
	ProfileVisibilityAccuracy             = "accuracy"
	ProfileVisibilityDiagnosticsSubmitted = "diagnostics_submitted"
	ProfileVisibilityChatTurns            = "chat_turns"
	ProfileVisibilityAICalls              = "ai_calls"
	ProfileVisibilityTokensUsed           = "tokens_used"
	ProfileVisibilityGenerationFailures   = "quiz_generation_failures"
)

// ProfileVisibilityField is one independently configurable public field.
type ProfileVisibilityField struct {
	Key   string
	Label string
}

var profileVisibilityFields = []ProfileVisibilityField{
	{ProfileVisibilityDisplayName, "Nome"},
	{ProfileVisibilityAvatar, "Foto"},
	{ProfileVisibilityBio, "Bio"},
	{ProfileVisibilityRank, "Rank"},
	{ProfileVisibilityAverageScore, "Média das notas"},
	{ProfileVisibilityRankedQuizzes, "Quizzes ranqueados"},
	{ProfileVisibilityQuizzesCompleted, "Quizzes concluídos"},
	{ProfileVisibilityQuizzesStarted, "Quizzes iniciados"},
	{ProfileVisibilityStudiesCreated, "Estudos criados"},
	{ProfileVisibilityStudyResets, "Estudos reiniciados"},
	{ProfileVisibilityQuestionsAnswered, "Perguntas respondidas"},
	{ProfileVisibilityCorrectAnswers, "Respostas corretas"},
	{ProfileVisibilityAccuracy, "Acurácia"},
	{ProfileVisibilityDiagnosticsSubmitted, "Diagnósticos"},
	{ProfileVisibilityChatTurns, "Turnos de chat"},
	{ProfileVisibilityAICalls, "Chamadas de IA"},
	{ProfileVisibilityTokensUsed, "Tokens usados"},
	{ProfileVisibilityGenerationFailures, "Falhas de geração"},
}

// ProfileVisibilityFields returns a copy so templates and handlers cannot
// mutate the repository's allow-list.
func ProfileVisibilityFields() []ProfileVisibilityField {
	return append([]ProfileVisibilityField(nil), profileVisibilityFields...)
}

func defaultProfileVisibility() map[string]bool {
	visibility := make(map[string]bool, len(profileVisibilityFields))
	for _, field := range profileVisibilityFields {
		visibility[field.Key] = true
	}
	return visibility
}

func normalizeProfileVisibility(input map[string]bool) map[string]bool {
	visibility := defaultProfileVisibility()
	for _, field := range profileVisibilityFields {
		if value, ok := input[field.Key]; ok {
			visibility[field.Key] = value
		}
	}
	return visibility
}

func decodeProfileVisibility(raw string) map[string]bool {
	visibility := defaultProfileVisibility()
	if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "{}" {
		return visibility
	}
	var stored map[string]bool
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		return visibility
	}
	return normalizeProfileVisibility(stored)
}

// ProfileDetails stores the editable, non-account portion of a profile.
type ProfileDetails struct {
	DisplayName string
	Bio         string
	AvatarMIME  string
	AvatarData  []byte
	Visibility  map[string]bool
	UpdatedAt   time.Time
}

// ProfileStats combines the materialized telemetry counters with derived
// learning metrics used by the public profile.
type ProfileStats struct {
	TelemetryStats
	RankedScoreSumCents int64
	RankedQuizCount     int64
	AverageScoreCents   int64
	AccuracyCents       int64
}

// ProfileRank is calculated from the exact sum/count pair of ranked scores.
type ProfileRank struct {
	Key               string
	Label             string
	AverageScoreCents int64
	QuizCount         int64
	Eligible          bool
}

type rankDefinition struct {
	Key          string
	Label        string
	MinimumCents int64
}

var profileRanks = []rankDefinition{
	{Key: "study-maxer", Label: "Study-maxer", MinimumCents: 950},
	{Key: "knowledge-maxer", Label: "Knowledge-maxer", MinimumCents: 900},
	{Key: "token-maxxer", Label: "Token-maxxer", MinimumCents: 850},
	{Key: "ouro", Label: "Ouro", MinimumCents: 750},
	{Key: "prata", Label: "Prata", MinimumCents: 600},
	{Key: "bronze", Label: "Bronze", MinimumCents: 0},
}

const minimumRankedQuizzes = int64(5)

// RankFor calculates the rank without floating point arithmetic. Scores are
// stored as cents of the normalized 0..10 grade, so thresholds are compared
// as sum >= threshold * count.
func RankFor(scoreSumCents, quizCount int64) ProfileRank {
	average := int64(0)
	if quizCount > 0 {
		average = (scoreSumCents + quizCount/2) / quizCount
	}
	rank := profileRanks[len(profileRanks)-1]
	if quizCount >= minimumRankedQuizzes {
		for _, candidate := range profileRanks {
			if scoreSumCents >= candidate.MinimumCents*quizCount {
				rank = candidate
				break
			}
		}
	}
	return ProfileRank{
		Key:               rank.Key,
		Label:             rank.Label,
		AverageScoreCents: average,
		QuizCount:         quizCount,
		Eligible:          quizCount >= minimumRankedQuizzes,
	}
}

// ProfileView is the complete server-side view. The handler/template filters
// fields for visitors using Visible; the email is intentionally absent.
type ProfileView struct {
	Profile Profile
	Details ProfileDetails
	Stats   ProfileStats
	Rank    ProfileRank
	IsOwner bool
}

func (p ProfileView) Visible(field string, owner bool) bool {
	return owner || p.Details.Visibility[field]
}

func (p ProfileView) PublicName(owner bool) string {
	if p.Visible(ProfileVisibilityDisplayName, owner) && strings.TrimSpace(p.Details.DisplayName) != "" {
		return p.Details.DisplayName
	}
	return p.Profile.Slug
}

// ProfileUpdate is the validated result of the profile form. A nil AvatarData
// means keep the existing image; RemoveAvatar takes precedence when true.
type ProfileUpdate struct {
	Username     string
	UsernameSet  bool
	DisplayName  string
	Bio          string
	Visibility   map[string]bool
	AvatarMIME   string
	AvatarData   []byte
	RemoveAvatar bool
}

// ViewBySlug loads a public profile and all aggregate metrics without loading
// any message, prompt, question, password, or email content.
func (r *ProfileRepo) ViewBySlug(ctx context.Context, slug string) (*ProfileView, error) {
	p, err := r.BySlug(ctx, slug)
	if err != nil || p == nil {
		return nil, err
	}
	return r.view(ctx, *p)
}

// ViewByUser loads the profile view for an authenticated owner.
func (r *ProfileRepo) ViewByUser(ctx context.Context, userID int64) (*ProfileView, error) {
	p, err := r.ByUser(ctx, userID)
	if err != nil || p == nil {
		return nil, err
	}
	return r.view(ctx, *p)
}

func (r *ProfileRepo) view(ctx context.Context, p Profile) (*ProfileView, error) {
	details, err := r.details(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	stats, err := r.stats(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	rank := RankFor(stats.RankedScoreSumCents, stats.RankedQuizCount)
	stats.AverageScoreCents = rank.AverageScoreCents
	return &ProfileView{Profile: p, Details: details, Stats: stats, Rank: rank}, nil
}

func (r *ProfileRepo) details(ctx context.Context, userID int64) (ProfileDetails, error) {
	details := ProfileDetails{Visibility: defaultProfileVisibility()}
	var displayName, bio, mime, visibility, updated sql.NullString
	var data []byte
	err := r.db.QueryRowContext(ctx,
		`SELECT display_name, bio, avatar_mime, avatar_data, visibility_json, updated_at
		 FROM profile_details WHERE user_id = ?`, userID).
		Scan(&displayName, &bio, &mime, &data, &visibility, &updated)
	if err == sql.ErrNoRows {
		return details, nil
	}
	if err != nil {
		return details, fmt.Errorf("profile details: %w", err)
	}
	details.DisplayName = displayName.String
	details.Bio = bio.String
	details.AvatarMIME = mime.String
	details.AvatarData = data
	details.Visibility = decodeProfileVisibility(visibility.String)
	if updated.Valid {
		details.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated.String)
	}
	return details, nil
}

func (r *ProfileRepo) stats(ctx context.Context, userID int64) (ProfileStats, error) {
	stats := ProfileStats{TelemetryStats: TelemetryStats{UserID: userID}}
	var last sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT studies_created, study_resets, quizzes_started, quizzes_completed,
		 ranked_quizzes_completed, quiz_generation_failures, questions_answered,
		 correct_answers, diagnostics_submitted, chat_turns, ai_calls, tokens_used,
		 last_active_at FROM user_stats WHERE user_id = ?`, userID).
		Scan(&stats.StudiesCreated, &stats.StudyResets, &stats.QuizzesStarted,
			&stats.QuizzesCompleted, &stats.RankedQuizzesCompleted,
			&stats.QuizGenerationFailures, &stats.QuestionsAnswered,
			&stats.CorrectAnswers, &stats.DiagnosticsSubmitted, &stats.ChatTurns,
			&stats.AICalls, &stats.TokensUsed, &last)
	if err != nil && err != sql.ErrNoRows {
		return stats, fmt.Errorf("profile stats: %w", err)
	}
	if last.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", last.String)
		stats.LastActiveAt = &t
	}
	if stats.QuestionsAnswered > 0 {
		stats.AccuracyCents = (stats.CorrectAnswers*10000 + stats.QuestionsAnswered/2) / stats.QuestionsAnswered
	}
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(SUM(score_cents), 0)
		 FROM ranked_results WHERE user_id = ?`, userID).
		Scan(&stats.RankedQuizCount, &stats.RankedScoreSumCents); err != nil {
		return stats, fmt.Errorf("profile ranked stats: %w", err)
	}
	return stats, nil
}

// Update saves profile fields atomically while preserving the avatar unless
// the caller explicitly replaces or removes it.
func (r *ProfileRepo) Update(ctx context.Context, userID int64, update ProfileUpdate) error {
	if userID <= 0 {
		return fmt.Errorf("profile update: invalid user")
	}
	visibilityJSON, err := json.Marshal(normalizeProfileVisibility(update.Visibility))
	if err != nil {
		return fmt.Errorf("profile visibility: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("profile update begin: %w", err)
	}
	defer tx.Rollback()
	var currentUsername, currentTag, changed sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT username, tag, slug_changed_at FROM user_profiles WHERE user_id=?`, userID).
		Scan(&currentUsername, &currentTag, &changed); err != nil {
		return fmt.Errorf("profile update identity: %w", err)
	}
	if update.UsernameSet || strings.TrimSpace(update.Username) != "" {
		username, err := NormalizePublicUsername(update.Username)
		if err != nil {
			return err
		}
		if username != currentUsername.String {
			profile := Profile{SlugChangedAt: parseProfileTime(changed)}
			if !profile.CanChangeUsername(time.Now().UTC()) {
				return ErrUsernameChangeCooldown
			}
			tag := currentTag.String
			if !validPublicTag(tag) {
				return fmt.Errorf("profile update: invalid public tag")
			}
			newSlug := PublicHandle(username, tag)
			var taken int
			if err := tx.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM user_profiles WHERE username=? AND tag=? AND user_id != ?`, username, tag, userID).Scan(&taken); err != nil {
				return fmt.Errorf("profile update username check: %w", err)
			}
			if taken > 0 {
				return ErrUsernameTaken
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE user_profiles SET username=?, slug=?, slug_changed_at=datetime('now') WHERE user_id=?`, username, newSlug, userID); err != nil {
				return fmt.Errorf("profile update username: %w", err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO profile_details(user_id, display_name, bio, visibility_json, updated_at)
		 VALUES(?,?,?,?,datetime('now'))
		 ON CONFLICT(user_id) DO UPDATE SET
		 display_name=excluded.display_name,
		 bio=excluded.bio,
		 visibility_json=excluded.visibility_json,
		 updated_at=datetime('now')`,
		userID, update.DisplayName, update.Bio, string(visibilityJSON)); err != nil {
		return fmt.Errorf("profile update details: %w", err)
	}
	if update.RemoveAvatar {
		if _, err := tx.ExecContext(ctx,
			`UPDATE profile_details SET avatar_mime=NULL, avatar_data=NULL, updated_at=datetime('now') WHERE user_id=?`, userID); err != nil {
			return fmt.Errorf("profile remove avatar: %w", err)
		}
	} else if update.AvatarData != nil {
		if _, err := tx.ExecContext(ctx,
			`UPDATE profile_details SET avatar_mime=?, avatar_data=?, updated_at=datetime('now') WHERE user_id=?`,
			update.AvatarMIME, update.AvatarData, userID); err != nil {
			return fmt.Errorf("profile save avatar: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("profile update commit: %w", err)
	}
	return nil
}
