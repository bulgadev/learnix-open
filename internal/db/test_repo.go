package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// TestDefinition is the durable container for a topic and its attempts.
type TestDefinition struct {
	ID     int64
	UserID int64
	Topic  string
	Mode   string
	Preset string
	// Exam makes every attempt of this test run in exam-simulation mode:
	// free navigation, flag-for-review, countdown, feedback only at the end.
	Exam         bool
	Observations []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TestSummary is the lightweight projection used by the studies/tests hub.
type TestSummary struct {
	ID             int64
	Topic          string
	Mode           string
	Preset         string
	Exam           bool
	AttemptCount   int
	CompletedCount int
	LastScore      int
	LastTotal      int
	LastScoreCents int64
	LastFinishedAt *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// TestAttemptSummary is the history projection for a test definition.
type TestAttemptSummary struct {
	ID         int64
	TestID     int64
	Phase      string
	Score      int
	Total      int
	ScoreCents int64
	CreatedAt  time.Time
	FinishedAt *time.Time
}

type TestRepo struct{ db *sql.DB }

func NewTestRepo(database *sql.DB) *TestRepo { return &TestRepo{db: database} }

func (r *TestRepo) Create(ctx context.Context, test *TestDefinition) error {
	if test.Mode == "" {
		test.Mode = "practice"
	}
	if test.Preset == "" {
		test.Preset = "moderate"
	}
	observations, err := json.Marshal(test.Observations)
	if err != nil {
		return fmt.Errorf("test observations marshal: %w", err)
	}
	result, err := r.db.ExecContext(ctx,
		`INSERT INTO test_definitions(user_id, topic, mode, preset, observations_json, exam)
		 VALUES(?,?,?,?,?,?)`, test.UserID, test.Topic, test.Mode, test.Preset, string(observations), boolInt(test.Exam))
	if err != nil {
		return fmt.Errorf("test create: %w", err)
	}
	test.ID, err = result.LastInsertId()
	if err != nil {
		return fmt.Errorf("test create last id: %w", err)
	}
	return nil
}

func (r *TestRepo) Get(ctx context.Context, userID, id int64) (*TestDefinition, error) {
	var test TestDefinition
	var observations, created, updated string
	var exam int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, topic, mode, preset, observations_json, created_at, updated_at, exam
		 FROM test_definitions WHERE id = ? AND user_id = ?`, id, userID).
		Scan(&test.ID, &test.UserID, &test.Topic, &test.Mode, &test.Preset, &observations, &created, &updated, &exam)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("test get: %w", err)
	}
	test.Exam = exam != 0
	if observations != "" {
		_ = json.Unmarshal([]byte(observations), &test.Observations)
	}
	test.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	test.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &test, nil
}

func (r *TestRepo) ListByUser(ctx context.Context, userID int64) ([]TestSummary, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, topic, mode, preset, created_at, updated_at, exam
		 FROM test_definitions WHERE user_id = ? ORDER BY updated_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("test list: %w", err)
	}
	defer rows.Close()
	var out []TestSummary
	for rows.Next() {
		var summary TestSummary
		var created, updated string
		var exam int
		if err := rows.Scan(&summary.ID, &summary.Topic, &summary.Mode, &summary.Preset, &created, &updated, &exam); err != nil {
			return nil, fmt.Errorf("test list scan: %w", err)
		}
		summary.Exam = exam != 0
		summary.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		summary.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		if err := r.populateSummary(ctx, userID, &summary); err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}

func (r *TestRepo) populateSummary(ctx context.Context, userID int64, summary *TestSummary) error {
	var completed sql.NullInt64
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(CASE WHEN finished_at IS NOT NULL THEN 1 ELSE 0 END)
		 FROM quizzes WHERE user_id = ? AND test_id = ?`, userID, summary.ID).
		Scan(&summary.AttemptCount, &completed); err != nil {
		return fmt.Errorf("test summary counts: %w", err)
	}
	summary.CompletedCount = int(completed.Int64)
	var finished sql.NullString
	err := r.db.QueryRowContext(ctx,
		`SELECT score, total, score_cents, finished_at
		 FROM quizzes WHERE user_id = ? AND test_id = ?
		 ORDER BY created_at DESC, id DESC LIMIT 1`, userID, summary.ID).
		Scan(&summary.LastScore, &summary.LastTotal, &summary.LastScoreCents, &finished)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("test summary latest: %w", err)
	}
	if finished.Valid {
		value, _ := time.Parse("2006-01-02 15:04:05", finished.String)
		summary.LastFinishedAt = &value
	}
	return nil
}

func (r *TestRepo) ListAttempts(ctx context.Context, userID, testID int64) ([]TestAttemptSummary, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, test_id, phase, score, total, score_cents, created_at, finished_at
		 FROM quizzes WHERE user_id = ? AND test_id = ?
		 ORDER BY created_at DESC, id DESC`, userID, testID)
	if err != nil {
		return nil, fmt.Errorf("test attempts list: %w", err)
	}
	defer rows.Close()
	var out []TestAttemptSummary
	for rows.Next() {
		var attempt TestAttemptSummary
		var created, finished sql.NullString
		if err := rows.Scan(&attempt.ID, &attempt.TestID, &attempt.Phase, &attempt.Score, &attempt.Total, &attempt.ScoreCents, &created, &finished); err != nil {
			return nil, fmt.Errorf("test attempts scan: %w", err)
		}
		attempt.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created.String)
		if finished.Valid {
			value, _ := time.Parse("2006-01-02 15:04:05", finished.String)
			attempt.FinishedAt = &value
		}
		out = append(out, attempt)
	}
	return out, rows.Err()
}

func (r *TestRepo) UpdateObservations(ctx context.Context, userID, testID int64, observations []string) error {
	value, err := json.Marshal(observations)
	if err != nil {
		return fmt.Errorf("test observations marshal: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE test_definitions SET observations_json = ?, updated_at = datetime('now') WHERE id = ? AND user_id = ?`,
		string(value), testID, userID)
	if err != nil {
		return fmt.Errorf("test observations update: %w", err)
	}
	return nil
}
