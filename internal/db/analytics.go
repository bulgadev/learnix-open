package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Profile is the public, non-email handle used by leaderboards and profiles.
type Profile struct {
	UserID        int64
	Slug          string
	Username      string
	Tag           string
	CreatedAt     time.Time
	SlugChangedAt *time.Time
}

const PublicUsernameMaxLength = 24

var publicUsernamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,23})?$`)

// NormalizePublicUsername validates the editable part of a public handle.
// Handles are ASCII, case-insensitive, and cannot contain the # separator.
func NormalizePublicUsername(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > PublicUsernameMaxLength || !publicUsernamePattern.MatchString(value) {
		return "", fmt.Errorf("usuário deve ter de 1 a %d caracteres: letras, números, ponto, hífen ou sublinhado", PublicUsernameMaxLength)
	}
	return value, nil
}

func validPublicTag(value string) bool {
	if len(value) != 4 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// PublicHandle formats the public username and its stable four-digit tag.
func PublicHandle(username, tag string) string {
	if username == "" || !validPublicTag(tag) {
		return ""
	}
	return username + "#" + tag
}

// CanChangeUsername applies the once-per-calendar-month rule.
func (p Profile) CanChangeUsername(now time.Time) bool {
	if p.SlugChangedAt == nil {
		return true
	}
	last := p.SlugChangedAt.UTC()
	now = now.UTC()
	return last.Year() != now.Year() || last.Month() != now.Month()
}

// ProfileRepo persists public user identities.
type ProfileRepo struct{ db *sql.DB }

func NewProfileRepo(db *sql.DB) *ProfileRepo { return &ProfileRepo{db: db} }

func (r *ProfileRepo) ByUser(ctx context.Context, userID int64) (*Profile, error) {
	var p Profile
	var created, changed sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT user_id, slug, username, tag, created_at, slug_changed_at
		 FROM user_profiles WHERE user_id = ?`, userID).
		Scan(&p.UserID, &p.Slug, &p.Username, &p.Tag, &created, &changed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("profile by user: %w", err)
	}
	p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created.String)
	p.SlugChangedAt = parseProfileTime(changed)
	return &p, nil
}

func (r *ProfileRepo) BySlug(ctx context.Context, slug string) (*Profile, error) {
	var p Profile
	var created, changed sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT user_id, slug, username, tag, created_at, slug_changed_at
		 FROM user_profiles WHERE slug = ?`, slug).
		Scan(&p.UserID, &p.Slug, &p.Username, &p.Tag, &created, &changed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("profile by slug: %w", err)
	}
	p.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created.String)
	p.SlugChangedAt = parseProfileTime(changed)
	return &p, nil
}

func parseProfileTime(value sql.NullString) *time.Time {
	if !value.Valid || value.String == "" {
		return nil
	}
	t, err := time.Parse("2006-01-02 15:04:05", value.String)
	if err != nil {
		return nil
	}
	return &t
}

// TelemetryStats is the materialized per-user summary used by future profile
// pages. Leaderboard-specific totals remain queryable from their source logs.
type TelemetryStats struct {
	UserID                 int64
	StudiesCreated         int64
	StudyResets            int64
	QuizzesStarted         int64
	QuizzesCompleted       int64
	RankedQuizzesCompleted int64
	QuizGenerationFailures int64
	QuestionsAnswered      int64
	CorrectAnswers         int64
	DiagnosticsSubmitted   int64
	ChatTurns              int64
	AICalls                int64
	TokensUsed             int64
	LastActiveAt           *time.Time
}

// TelemetryDelta describes the counters changed by one event.
type TelemetryDelta struct {
	StudiesCreated         int64
	StudyResets            int64
	QuizzesStarted         int64
	QuizzesCompleted       int64
	RankedQuizzesCompleted int64
	QuizGenerationFailures int64
	QuestionsAnswered      int64
	CorrectAnswers         int64
	DiagnosticsSubmitted   int64
	ChatTurns              int64
	AICalls                int64
	TokensUsed             int64
}

// TelemetryEvent is deliberately limited to numeric values and allow-listed
// metadata assembled by the application. It never receives prompt/message
// bodies.
type TelemetryEvent struct {
	UserID     int64
	Type       string
	StudyID    int64
	QuizID     int64
	ValueInt   int64
	ValueCents int64
	Metadata   map[string]any
	Delta      TelemetryDelta
}

// TelemetryRepo stores append-only events and updates the materialized summary
// in the same transaction.
type TelemetryRepo struct{ db *sql.DB }

func NewTelemetryRepo(db *sql.DB) *TelemetryRepo { return &TelemetryRepo{db: db} }

func (r *TelemetryRepo) Record(ctx context.Context, e TelemetryEvent) error {
	if e.UserID <= 0 || strings.TrimSpace(e.Type) == "" {
		return fmt.Errorf("telemetry event requires user and type")
	}
	metadata, err := json.Marshal(e.Metadata)
	if err != nil {
		return fmt.Errorf("telemetry metadata: %w", err)
	}
	if string(metadata) == "null" {
		metadata = nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("telemetry begin: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO telemetry_events(user_id, event_type, study_id, quiz_id, value_int, value_cents, metadata_json)
		 VALUES(?,?,?,?,?,?,?)`, e.UserID, e.Type, nullableID(e.StudyID), nullableID(e.QuizID),
		e.ValueInt, e.ValueCents, nullableString(string(metadata))); err != nil {
		return fmt.Errorf("telemetry insert: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO user_stats(user_id) VALUES(?)`, e.UserID); err != nil {
		return fmt.Errorf("telemetry stats init: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE user_stats SET
		 studies_created = studies_created + ?,
		 study_resets = study_resets + ?,
		 quizzes_started = quizzes_started + ?,
		 quizzes_completed = quizzes_completed + ?,
		 ranked_quizzes_completed = ranked_quizzes_completed + ?,
		 quiz_generation_failures = quiz_generation_failures + ?,
		 questions_answered = questions_answered + ?,
		 correct_answers = correct_answers + ?,
		 diagnostics_submitted = diagnostics_submitted + ?,
		 chat_turns = chat_turns + ?,
		 ai_calls = ai_calls + ?,
		 tokens_used = tokens_used + ?,
		 last_active_at = datetime('now')
		 WHERE user_id = ?`,
		e.Delta.StudiesCreated, e.Delta.StudyResets, e.Delta.QuizzesStarted,
		e.Delta.QuizzesCompleted, e.Delta.RankedQuizzesCompleted,
		e.Delta.QuizGenerationFailures, e.Delta.QuestionsAnswered,
		e.Delta.CorrectAnswers, e.Delta.DiagnosticsSubmitted, e.Delta.ChatTurns,
		e.Delta.AICalls, e.Delta.TokensUsed, e.UserID); err != nil {
		return fmt.Errorf("telemetry stats update: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("telemetry commit: %w", err)
	}
	return nil
}

func (r *TelemetryRepo) Stats(ctx context.Context, userID int64) (*TelemetryStats, error) {
	var s TelemetryStats
	var last sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT user_id, studies_created, study_resets, quizzes_started,
		 quizzes_completed, ranked_quizzes_completed, quiz_generation_failures,
		 questions_answered, correct_answers, diagnostics_submitted, chat_turns,
		 ai_calls, tokens_used, last_active_at
		 FROM user_stats WHERE user_id = ?`, userID).
		Scan(&s.UserID, &s.StudiesCreated, &s.StudyResets, &s.QuizzesStarted,
			&s.QuizzesCompleted, &s.RankedQuizzesCompleted, &s.QuizGenerationFailures,
			&s.QuestionsAnswered, &s.CorrectAnswers, &s.DiagnosticsSubmitted,
			&s.ChatTurns, &s.AICalls, &s.TokensUsed, &last)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry stats: %w", err)
	}
	if last.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", last.String)
		s.LastActiveAt = &t
	}
	return &s, nil
}

// RankedResult is an immutable snapshot. It intentionally has no FK to the
// quiz row so deleting a study does not erase a published competition result.
type RankedResult struct {
	QuizID             int64
	UserID             int64
	Topic              string
	Preset             string
	Total              int
	Correct            int
	ScoreCents         int64
	WeightCents        int64
	WeightedScoreCents int64
	FinishedAt         time.Time
}

// LeaderboardEntry is the database projection before JSON formatting.
type LeaderboardEntry struct {
	UserID    int64
	Slug      string
	Value     int64
	QuizCount int64
}

type LeaderboardRepo struct{ db *sql.DB }

func NewLeaderboardRepo(db *sql.DB) *LeaderboardRepo { return &LeaderboardRepo{db: db} }

func (r *LeaderboardRepo) Record(ctx context.Context, result RankedResult) error {
	if result.QuizID <= 0 || result.UserID <= 0 || result.Total <= 0 || result.FinishedAt.IsZero() {
		return fmt.Errorf("invalid ranked result")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO ranked_results
		 (quiz_id, user_id, topic, preset, total, correct, score_cents, weight_cents, weighted_score_cents, finished_at)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		result.QuizID, result.UserID, result.Topic, result.Preset, result.Total,
		result.Correct, result.ScoreCents, result.WeightCents,
		result.WeightedScoreCents, result.FinishedAt.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return fmt.Errorf("ranked result insert: %w", err)
	}
	return nil
}

func (r *LeaderboardRepo) ListScore(ctx context.Context, from, to *time.Time, limit int) ([]LeaderboardEntry, error) {
	where, args := periodWhere("rr.finished_at", from, to)
	query := `SELECT rr.user_id, p.slug, COALESCE(SUM(rr.weighted_score_cents), 0), COUNT(*)
		FROM ranked_results rr JOIN user_profiles p ON p.user_id = rr.user_id` + where +
		` GROUP BY rr.user_id, p.slug ORDER BY 3 DESC, p.slug ASC LIMIT ?`
	args = append(args, clampLeaderboardLimit(limit))
	return r.list(ctx, query, args...)
}

func (r *LeaderboardRepo) ListQuizzes(ctx context.Context, from, to *time.Time, limit int) ([]LeaderboardEntry, error) {
	where, args := periodWhere("rr.finished_at", from, to)
	query := `SELECT rr.user_id, p.slug, COUNT(*), COUNT(*)
		FROM ranked_results rr JOIN user_profiles p ON p.user_id = rr.user_id` + where +
		` GROUP BY rr.user_id, p.slug ORDER BY 3 DESC, p.slug ASC LIMIT ?`
	args = append(args, clampLeaderboardLimit(limit))
	return r.list(ctx, query, args...)
}

func (r *LeaderboardRepo) ListTokens(ctx context.Context, from, to *time.Time, limit int) ([]LeaderboardEntry, error) {
	where, args := periodWhere("l.created_at", from, to)
	query := `SELECT l.user_id, p.slug, COALESCE(SUM(l.tokens), 0), 0
		FROM usage_log l JOIN user_profiles p ON p.user_id = l.user_id` + where +
		` GROUP BY l.user_id, p.slug ORDER BY 3 DESC, p.slug ASC LIMIT ?`
	args = append(args, clampLeaderboardLimit(limit))
	return r.list(ctx, query, args...)
}

func (r *LeaderboardRepo) list(ctx context.Context, query string, args ...any) ([]LeaderboardEntry, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("leaderboard query: %w", err)
	}
	defer rows.Close()
	var out []LeaderboardEntry
	for rows.Next() {
		var e LeaderboardEntry
		if err := rows.Scan(&e.UserID, &e.Slug, &e.Value, &e.QuizCount); err != nil {
			return nil, fmt.Errorf("leaderboard scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func periodWhere(column string, from, to *time.Time) (string, []any) {
	if from == nil || to == nil {
		return "", nil
	}
	return " WHERE " + column + " >= ? AND " + column + " < ?", []any{
		from.UTC().Format("2006-01-02 15:04:05"),
		to.UTC().Format("2006-01-02 15:04:05"),
	}
}

func clampLeaderboardLimit(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

func ensureUserMetadata(ctx context.Context, database *sql.DB, userID int64) error {
	var username, tag, slug string
	err := database.QueryRowContext(ctx,
		`SELECT username, tag, slug FROM user_profiles WHERE user_id = ?`, userID).
		Scan(&username, &tag, &slug)
	if err == sql.ErrNoRows {
		username = "user"
		tag, err = allocatePublicTag(ctx, database, username, userID)
		if err != nil {
			return err
		}
		slug = PublicHandle(username, tag)
		if _, err := database.ExecContext(ctx,
			`INSERT INTO user_profiles(user_id, slug, username, tag) VALUES(?, ?, ?, ?)`, userID, slug, username, tag); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		usernameValid := true
		if _, err := NormalizePublicUsername(username); err != nil {
			usernameValid = false
			username = "user"
		}
		if !usernameValid || !validPublicTag(tag) {
			tag, err = allocatePublicTag(ctx, database, username, userID)
			if err != nil {
				return err
			}
		}
		newSlug := PublicHandle(username, tag)
		if slug != newSlug {
			if _, err := database.ExecContext(ctx,
				`UPDATE user_profiles SET slug=?, username=?, tag=? WHERE user_id=?`, newSlug, username, tag, userID); err != nil {
				return err
			}
		}
	}
	_, err = database.ExecContext(ctx,
		`INSERT OR IGNORE INTO user_stats(user_id,
		 studies_created, quizzes_completed, tokens_used)
		 SELECT ?,
		   (SELECT COUNT(*) FROM studies WHERE user_id = ?),
		   (SELECT COUNT(*) FROM quizzes WHERE user_id = ? AND finished_at IS NOT NULL),
		   COALESCE((SELECT SUM(tokens) FROM usage_log WHERE user_id = ?), 0)`,
		userID, userID, userID, userID)
	return err
}

func allocatePublicTag(ctx context.Context, database *sql.DB, username string, userID int64) (string, error) {
	preferred := int(userID % 10000)
	if preferred == 0 {
		preferred = 1
	}
	for offset := 0; offset < 10000; offset++ {
		candidate := (preferred+offset-1)%10000 + 1
		tag := fmt.Sprintf("%04d", candidate)
		var taken int
		if err := database.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM user_profiles WHERE username=? AND tag=? AND user_id != ?`, username, tag, userID).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return tag, nil
		}
	}
	return "", fmt.Errorf("não há tags disponíveis para o usuário %q", username)
}

func ensureExistingUserMetadata(ctx context.Context, database *sql.DB) error {
	rows, err := database.QueryContext(ctx, `SELECT id FROM users ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := ensureUserMetadata(ctx, database, id); err != nil {
			return fmt.Errorf("user metadata %d: %w", id, err)
		}
	}
	_, err = database.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_user_profiles_username_tag
		 ON user_profiles(username, tag)`)
	return err
}

func ensureLeaderboardColumns(ctx context.Context, database *sql.DB) error {
	return ensureTableColumns(ctx, database, "quizzes", map[string]string{
		"mode":                 "TEXT NOT NULL DEFAULT 'practice'",
		"preset":               "TEXT NOT NULL DEFAULT 'moderate'",
		"weight_cents":         "INTEGER NOT NULL DEFAULT 0",
		"score_cents":          "INTEGER NOT NULL DEFAULT 0",
		"weighted_score_cents": "INTEGER NOT NULL DEFAULT 0",
	})
}

func ensureTableColumns(ctx context.Context, database *sql.DB, table string, columns map[string]string) error {
	for column, declaration := range columns {
		rows, err := database.QueryContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return err
		}
		found := false
		for rows.Next() {
			var cid, notNull, pk int
			var name, typ string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				rows.Close()
				return err
			}
			if name == column {
				found = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if !found {
			if _, err := database.ExecContext(ctx,
				"ALTER TABLE "+table+" ADD COLUMN "+column+" "+declaration); err != nil {
				return err
			}
		}
	}
	return nil
}
