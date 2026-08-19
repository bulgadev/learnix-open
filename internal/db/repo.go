package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"learnix/internal/session"
)

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// User represents a registered user.
type User struct {
	ID           int64
	Email        string
	PasswordHash string
	PublicSlug   string
	CreatedAt    time.Time
}

// UserRepo persists users.
type UserRepo struct{ db *sql.DB }

func NewUserRepo(db *sql.DB) *UserRepo { return &UserRepo{db: db} }

func (r *UserRepo) Create(ctx context.Context, email, passwordHash string) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO users(email, password_hash) VALUES(?, ?)`,
		email, passwordHash)
	if err != nil {
		return 0, fmt.Errorf("user create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("user create last id: %w", err)
	}
	if err := ensureUserMetadata(ctx, r.db, id); err != nil {
		_, _ = r.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id)
		return 0, fmt.Errorf("user create metadata: %w", err)
	}
	return id, nil
}

func (r *UserRepo) ByEmail(ctx context.Context, email string) (*User, error) {
	var u User
	var created string
	err := r.db.QueryRowContext(ctx,
		`SELECT u.id, u.email, u.password_hash, COALESCE(p.slug, ''), u.created_at
		 FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id WHERE u.email = ?`,
		email).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.PublicSlug, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("user by email: %w", err)
	}
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	return &u, nil
}

// Delete removes a user; sessions, config, studies (and their quizzes,
// files, chats, highlights) and quota/usage rows cascade via foreign keys.
func (r *UserRepo) Delete(ctx context.Context, id int64) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("user delete: %w", err)
	}
	return nil
}

func (r *UserRepo) ByID(ctx context.Context, id int64) (*User, error) {
	var u User
	var created string
	err := r.db.QueryRowContext(ctx,
		`SELECT u.id, u.email, u.password_hash, COALESCE(p.slug, ''), u.created_at
		 FROM users u LEFT JOIN user_profiles p ON p.user_id=u.id WHERE u.id = ?`,
		id).Scan(&u.ID, &u.Email, &u.PasswordHash, &u.PublicSlug, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("user by id: %w", err)
	}
	u.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	return &u, nil
}

// SessionRow is a persisted server-side session.
type SessionRow struct {
	ID        string
	UserID    int64
	CreatedAt time.Time
}

type SessionRepo struct{ db *sql.DB }

func NewSessionRepo(db *sql.DB) *SessionRepo { return &SessionRepo{db: db} }

// SessionTTL is how long a login session stays valid.
const SessionTTL = 30 * 24 * time.Hour

func (r *SessionRepo) Create(ctx context.Context, id string, userID int64) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions(id, user_id, expires_at) VALUES(?, ?, datetime('now', '+30 days'))`,
		id, userID)
	if err != nil {
		return fmt.Errorf("session create: %w", err)
	}
	return nil
}

// Get returns the session only while it is unexpired. Legacy rows without an
// expiry (created before expiry enforcement) are treated as expired.
func (r *SessionRepo) Get(ctx context.Context, id string) (*SessionRow, error) {
	var s SessionRow
	var created string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, created_at FROM sessions
		 WHERE id = ? AND expires_at IS NOT NULL AND expires_at > datetime('now')`,
		id).Scan(&s.ID, &s.UserID, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("session get: %w", err)
	}
	s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	return &s, nil
}

func (r *SessionRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("session delete: %w", err)
	}
	return nil
}

// PurgeExpired removes expired session rows (and legacy rows without expiry).
func (r *SessionRepo) PurgeExpired(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at IS NULL OR expires_at <= datetime('now')`)
	if err != nil {
		return fmt.Errorf("session purge: %w", err)
	}
	return nil
}

// UserConfig holds the user's preferred AI defaults, used to prefill the
// new-study form. Study state lives in the studies table.
type UserConfig struct {
	UserID  int64
	BaseURL string
	APIKey  string
	Model   string
}

type ConfigRepo struct{ db *sql.DB }

func NewConfigRepo(db *sql.DB) *ConfigRepo { return &ConfigRepo{db: db} }

func (r *ConfigRepo) Get(ctx context.Context, userID int64) (*UserConfig, error) {
	var c UserConfig
	err := r.db.QueryRowContext(ctx,
		`SELECT user_id, base_url, api_key, model FROM user_config WHERE user_id = ?`,
		userID).Scan(&c.UserID, &c.BaseURL, &c.APIKey, &c.Model)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("config get: %w", err)
	}
	return &c, nil
}

func (r *ConfigRepo) Upsert(ctx context.Context, c UserConfig) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO user_config(user_id, base_url, api_key, model, updated_at)
		 VALUES(?,?,?,?,datetime('now'))
		 ON CONFLICT(user_id) DO UPDATE SET
		   base_url=excluded.base_url, api_key=excluded.api_key,
		   model=excluded.model, updated_at=datetime('now')`,
		c.UserID, c.BaseURL, c.APIKey, c.Model)
	if err != nil {
		return fmt.Errorf("config upsert: %w", err)
	}
	return nil
}

// Study is one independent study workspace.
type Study struct {
	ID        int64
	UserID    int64
	Topic     string
	BaseURL   string
	APIKey    string
	Model     string
	Phase     string
	Feedback  string
	Reviewing bool
	History   []session.Message
	Chat      []session.ChatMsg
	CreatedAt time.Time
	UpdatedAt time.Time
}

// StudySummary is the list-page projection.
type StudySummary struct {
	ID        int64
	Topic     string
	Phase     string
	Reviewing bool
	UpdatedAt time.Time
}

// QuizSummary is retained for compatibility with legacy standalone attempts.
type QuizSummary struct {
	ID         int64
	Topic      string
	Phase      string
	Mode       string
	Preset     string
	Score      int
	Total      int
	ScoreCents int64
	CreatedAt  time.Time
	FinishedAt *time.Time
}

type StudyRepo struct{ db *sql.DB }

func NewStudyRepo(db *sql.DB) *StudyRepo { return &StudyRepo{db: db} }

// Create inserts a new study and returns its row id.
func (r *StudyRepo) Create(ctx context.Context, s *Study) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO studies(user_id, topic, base_url, api_key, model, phase, feedback, reviewing, history_json, chat_json)
		 VALUES(?,?,?,?,?,?,?,?,?,?)`,
		s.UserID, s.Topic, s.BaseURL, s.APIKey, s.Model, s.Phase, s.Feedback,
		boolInt(s.Reviewing), "[]", "[]")
	if err != nil {
		return fmt.Errorf("study create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("study create last id: %w", err)
	}
	s.ID = id
	return nil
}

func (r *StudyRepo) Get(ctx context.Context, id int64) (*Study, error) {
	var s Study
	var reviewing int
	var historyJSON, chatJSON string
	var created, updated string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, topic, base_url, api_key, model, phase, feedback, reviewing,
		        history_json, chat_json, created_at, updated_at
		 FROM studies WHERE id = ?`,
		id).Scan(&s.ID, &s.UserID, &s.Topic, &s.BaseURL, &s.APIKey, &s.Model, &s.Phase,
		&s.Feedback, &reviewing, &historyJSON, &chatJSON, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("study get: %w", err)
	}
	s.Reviewing = reviewing != 0
	if historyJSON != "" {
		_ = json.Unmarshal([]byte(historyJSON), &s.History)
	}
	if chatJSON != "" {
		_ = json.Unmarshal([]byte(chatJSON), &s.Chat)
	}
	s.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	s.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &s, nil
}

// Update writes the mutable study state (phase, feedback, reviewing, history, chat).
func (r *StudyRepo) Update(ctx context.Context, s *Study) error {
	historyJSON, _ := json.Marshal(s.History)
	chatJSON, _ := json.Marshal(s.Chat)
	_, err := r.db.ExecContext(ctx,
		`UPDATE studies SET phase=?, feedback=?, reviewing=?, history_json=?, chat_json=?, updated_at=datetime('now')
		 WHERE id=? AND user_id=?`,
		s.Phase, s.Feedback, boolInt(s.Reviewing), string(historyJSON), string(chatJSON), s.ID, s.UserID)
	if err != nil {
		return fmt.Errorf("study update: %w", err)
	}
	return nil
}

func (r *StudyRepo) ListByUser(ctx context.Context, userID int64) ([]StudySummary, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, topic, phase, reviewing, updated_at
		 FROM studies WHERE user_id = ? ORDER BY updated_at DESC`,
		userID)
	if err != nil {
		return nil, fmt.Errorf("study list: %w", err)
	}
	defer rows.Close()
	var out []StudySummary
	for rows.Next() {
		var s StudySummary
		var reviewing int
		var updated string
		if err := rows.Scan(&s.ID, &s.Topic, &s.Phase, &reviewing, &updated); err != nil {
			return nil, fmt.Errorf("study list scan: %w", err)
		}
		s.Reviewing = reviewing != 0
		s.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *StudyRepo) Delete(ctx context.Context, id, userID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM studies WHERE id=? AND user_id=?`, id, userID)
	if err != nil {
		return fmt.Errorf("study delete: %w", err)
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Quiz is one quiz attempt (active or finished).
type Quiz struct {
	ID                 int64
	UserID             int64
	StudyID            int64
	TestID             int64
	Topic              string
	Phase              string
	Current            int
	Questions          []session.Question
	Answers            []int
	Confidence         []int
	Assessments        []string
	TraceJSON          string
	Score              int
	Total              int
	Mode               string
	Preset             string
	WeightCents        int64
	ScoreCents         int64
	WeightedScoreCents int64
	AdaptiveFromID     int64
	// Exam marks exam-simulation attempts: free navigation, flag-for-review,
	// countdown deadline and no feedback until submission.
	Exam         bool
	ExamDeadline *time.Time
	// Flags mirrors session.Session.Flags: one entry per question, true when
	// the student flagged it for review.
	Flags []bool
	// TutorJSON stores the per-question tutor threads of standalone attempts
	// (map of question index to message list), marshaled by the handlers.
	TutorJSON  string
	CreatedAt  *time.Time
	FinishedAt *time.Time
}

// applyExamColumns decodes the exam columns scanned from a quizzes row.
func (q *Quiz) applyExamColumns(exam int, deadline, flagsJSON sql.NullString) {
	q.Exam = exam != 0
	if deadline.Valid && deadline.String != "" {
		if t, err := time.Parse("2006-01-02 15:04:05", deadline.String); err == nil {
			q.ExamDeadline = &t
		}
	}
	if flagsJSON.Valid && flagsJSON.String != "" {
		_ = json.Unmarshal([]byte(flagsJSON.String), &q.Flags)
	}
}

type QuizRepo struct{ db *sql.DB }

func NewQuizRepo(db *sql.DB) *QuizRepo { return &QuizRepo{db: db} }

// Save inserts a new quiz when q.ID == 0, otherwise updates the existing row.
func (r *QuizRepo) Save(ctx context.Context, q *Quiz) error {
	qJSON, err := json.Marshal(q.Questions)
	if err != nil {
		return fmt.Errorf("marshal questions: %w", err)
	}
	aJSON, err := json.Marshal(q.Answers)
	if err != nil {
		return fmt.Errorf("marshal answers: %w", err)
	}
	cJSON, err := json.Marshal(q.Confidence)
	if err != nil {
		return fmt.Errorf("marshal confidence: %w", err)
	}
	assessmentJSON, err := json.Marshal(q.Assessments)
	if err != nil {
		return fmt.Errorf("marshal assessments: %w", err)
	}
	var finished any
	if q.FinishedAt != nil {
		finished = q.FinishedAt.UTC().Format("2006-01-02 15:04:05")
	}
	var deadline any
	if q.ExamDeadline != nil {
		deadline = q.ExamDeadline.UTC().Format("2006-01-02 15:04:05")
	}
	var flagsJSON any
	if len(q.Flags) > 0 {
		fJSON, err := json.Marshal(q.Flags)
		if err != nil {
			return fmt.Errorf("marshal flags: %w", err)
		}
		flagsJSON = string(fJSON)
	}
	tutorJSON := nullableString(q.TutorJSON)
	if q.Mode == "" {
		q.Mode = "practice"
	}
	if q.Preset == "" {
		q.Preset = "moderate"
	}
	var studyID any
	if q.StudyID != 0 {
		studyID = q.StudyID
	}
	if q.ID == 0 {
		res, err := r.db.ExecContext(ctx,
			`INSERT INTO quizzes(user_id, study_id, test_id, topic, phase, current, questions_json, answers_json, confidence_json, trace_json, assessments_json, score, total, mode, preset, weight_cents, score_cents, weighted_score_cents, adaptive_from_id, finished_at, exam, exam_deadline, flags_json, tutor_json)
				 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			q.UserID, studyID, nullableID(q.TestID), q.Topic, q.Phase, q.Current, string(qJSON), string(aJSON), string(cJSON), nullableString(q.TraceJSON), string(assessmentJSON), q.Score, q.Total, q.Mode, q.Preset, q.WeightCents, q.ScoreCents, q.WeightedScoreCents, nullableID(q.AdaptiveFromID), finished, boolInt(q.Exam), deadline, flagsJSON, tutorJSON)
		if err != nil {
			return fmt.Errorf("quiz insert: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("quiz insert last id: %w", err)
		}
		q.ID = id
		return nil
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE quizzes SET study_id=?, test_id=?, topic=?, phase=?, current=?, questions_json=?, answers_json=?, confidence_json=?, trace_json=?, assessments_json=?, score=?, total=?, mode=?, preset=?, weight_cents=?, score_cents=?, weighted_score_cents=?, adaptive_from_id=?, finished_at=?, exam=?, exam_deadline=?, flags_json=?, tutor_json=?
			 WHERE id=? AND user_id=?`,
		studyID, nullableID(q.TestID), q.Topic, q.Phase, q.Current, string(qJSON), string(aJSON), string(cJSON), nullableString(q.TraceJSON), string(assessmentJSON), q.Score, q.Total, q.Mode, q.Preset, q.WeightCents, q.ScoreCents, q.WeightedScoreCents, nullableID(q.AdaptiveFromID), finished, boolInt(q.Exam), deadline, flagsJSON, tutorJSON, q.ID, q.UserID)
	if err != nil {
		return fmt.Errorf("quiz update: %w", err)
	}
	return nil
}

func (r *QuizRepo) Get(ctx context.Context, id int64) (*Quiz, error) {
	var q Quiz
	var qJSON, aJSON string
	var confidenceJSON, traceJSON, assessmentsJSON sql.NullString
	var created, finished, deadline, flagsJSON, tutorJSON sql.NullString
	var studyID, testID, adaptiveFromID sql.NullInt64
	var exam int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, study_id, test_id, topic, phase, current, questions_json, answers_json, confidence_json, trace_json, assessments_json, score, total, mode, preset, weight_cents, score_cents, weighted_score_cents, adaptive_from_id, created_at, finished_at, exam, exam_deadline, flags_json, tutor_json
				 FROM quizzes WHERE id = ?`,
		id).Scan(&q.ID, &q.UserID, &studyID, &testID, &q.Topic, &q.Phase, &q.Current, &qJSON, &aJSON, &confidenceJSON, &traceJSON, &assessmentsJSON, &q.Score, &q.Total, &q.Mode, &q.Preset, &q.WeightCents, &q.ScoreCents, &q.WeightedScoreCents, &adaptiveFromID, &created, &finished, &exam, &deadline, &flagsJSON, &tutorJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("quiz get: %w", err)
	}
	q.StudyID = studyID.Int64
	q.TestID = testID.Int64
	q.AdaptiveFromID = adaptiveFromID.Int64
	q.applyExamColumns(exam, deadline, flagsJSON)
	if tutorJSON.Valid {
		q.TutorJSON = tutorJSON.String
	}
	if err := json.Unmarshal([]byte(qJSON), &q.Questions); err != nil {
		return nil, fmt.Errorf("quiz get questions: %w", err)
	}
	if err := json.Unmarshal([]byte(aJSON), &q.Answers); err != nil {
		return nil, fmt.Errorf("quiz get answers: %w", err)
	}
	if confidenceJSON.Valid && confidenceJSON.String != "" {
		_ = json.Unmarshal([]byte(confidenceJSON.String), &q.Confidence)
	}
	if assessmentsJSON.Valid && assessmentsJSON.String != "" {
		_ = json.Unmarshal([]byte(assessmentsJSON.String), &q.Assessments)
	}
	if traceJSON.Valid {
		q.TraceJSON = traceJSON.String
	}
	if created.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", created.String)
		q.CreatedAt = &t
	}
	if finished.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", finished.String)
		q.FinishedAt = &t
	}
	return &q, nil
}

// GetLatestByStudy returns the study's most recent quiz (active or finished),
// or nil if the study has no quizzes.
func (r *QuizRepo) GetLatestByStudy(ctx context.Context, studyID int64) (*Quiz, error) {
	var q Quiz
	var qJSON, aJSON string
	var confidenceJSON, traceJSON, assessmentsJSON sql.NullString
	var created, finished, deadline, flagsJSON, tutorJSON sql.NullString
	var studyIDCol, testID, adaptiveFromID sql.NullInt64
	var exam int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, user_id, study_id, test_id, topic, phase, current, questions_json, answers_json, confidence_json, trace_json, assessments_json, score, total, mode, preset, weight_cents, score_cents, weighted_score_cents, adaptive_from_id, created_at, finished_at, exam, exam_deadline, flags_json, tutor_json
				 FROM quizzes WHERE study_id = ?
				 ORDER BY created_at DESC, id DESC LIMIT 1`,
		studyID).Scan(&q.ID, &q.UserID, &studyIDCol, &testID, &q.Topic, &q.Phase, &q.Current, &qJSON, &aJSON, &confidenceJSON, &traceJSON, &assessmentsJSON, &q.Score, &q.Total, &q.Mode, &q.Preset, &q.WeightCents, &q.ScoreCents, &q.WeightedScoreCents, &adaptiveFromID, &created, &finished, &exam, &deadline, &flagsJSON, &tutorJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("quiz get latest: %w", err)
	}
	q.StudyID = studyIDCol.Int64
	q.TestID = testID.Int64
	q.AdaptiveFromID = adaptiveFromID.Int64
	q.applyExamColumns(exam, deadline, flagsJSON)
	if tutorJSON.Valid {
		q.TutorJSON = tutorJSON.String
	}
	if err := json.Unmarshal([]byte(qJSON), &q.Questions); err != nil {
		return nil, fmt.Errorf("quiz get latest questions: %w", err)
	}
	if err := json.Unmarshal([]byte(aJSON), &q.Answers); err != nil {
		return nil, fmt.Errorf("quiz get latest answers: %w", err)
	}
	if confidenceJSON.Valid && confidenceJSON.String != "" {
		_ = json.Unmarshal([]byte(confidenceJSON.String), &q.Confidence)
	}
	if assessmentsJSON.Valid && assessmentsJSON.String != "" {
		_ = json.Unmarshal([]byte(assessmentsJSON.String), &q.Assessments)
	}
	if traceJSON.Valid {
		q.TraceJSON = traceJSON.String
	}
	if created.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", created.String)
		q.CreatedAt = &t
	}
	if finished.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", finished.String)
		q.FinishedAt = &t
	}
	return &q, nil
}

// ListStandaloneByUser returns quizzes that are not attached to a study.
func (r *QuizRepo) ListStandaloneByUser(ctx context.Context, userID int64) ([]QuizSummary, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, topic, phase, mode, preset, score, total, score_cents, created_at, finished_at
		 FROM quizzes WHERE user_id = ? AND study_id IS NULL
		 ORDER BY created_at DESC, id DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("standalone quiz list: %w", err)
	}
	defer rows.Close()
	var out []QuizSummary
	for rows.Next() {
		var q QuizSummary
		var created, finished sql.NullString
		if err := rows.Scan(&q.ID, &q.Topic, &q.Phase, &q.Mode, &q.Preset, &q.Score, &q.Total, &q.ScoreCents, &created, &finished); err != nil {
			return nil, fmt.Errorf("standalone quiz list scan: %w", err)
		}
		q.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created.String)
		if finished.Valid {
			t, _ := time.Parse("2006-01-02 15:04:05", finished.String)
			q.FinishedAt = &t
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// DeleteByStudyInProgress removes the study's active (unfinished) quiz rows.
func (r *QuizRepo) DeleteByStudyInProgress(ctx context.Context, studyID int64) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM quizzes WHERE study_id = ? AND finished_at IS NULL`,
		studyID)
	if err != nil {
		return fmt.Errorf("quiz delete in-progress: %w", err)
	}
	return nil
}

// File is a workspace item: a folder, a text note, or an uploaded image.
type File struct {
	ID, StudyID, ParentID                   int64 // ParentID 0 = root (stored as NULL)
	Name, Kind, Mime, Content, ElementsJSON string
	Data                                    []byte
	Size                                    int64
	CreatedAt, UpdatedAt                    time.Time
}

// FileVersion is an immutable snapshot of a file's content.
type FileVersion struct {
	ID, FileID            int64
	Content, ElementsJSON string
	Data                  []byte
	Author, Message       string
	CreatedAt             time.Time
}

type FileRepo struct{ db *sql.DB }

func NewFileRepo(db *sql.DB) *FileRepo { return &FileRepo{db: db} }

// Create inserts the file plus an initial "criado" version snapshot.
func (r *FileRepo) Create(ctx context.Context, f *File) error {
	return r.CreateAuthored(ctx, f, "user", "criado")
}

// CreateAuthored inserts a file with its initial version attributed to author
// ("user" or "ai").
func (r *FileRepo) CreateAuthored(ctx context.Context, f *File, author, message string) error {
	if f.Size == 0 {
		if f.Kind == "image" {
			f.Size = int64(len(f.Data))
		} else {
			f.Size = int64(len(f.Content))
		}
	}
	var parent any
	if f.ParentID != 0 {
		parent = f.ParentID
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("file create tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO files(study_id, parent_id, name, kind, mime, content, elements_json, data, size)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		f.StudyID, parent, f.Name, f.Kind, f.Mime, f.Content, nullableString(f.ElementsJSON), f.Data, f.Size)
	if err != nil {
		return fmt.Errorf("file create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("file create last id: %w", err)
	}
	f.ID = id
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO file_versions(file_id, content, elements_json, data, author, message) VALUES(?,?,?,?,?,?)`,
		f.ID, f.Content, nullableString(f.ElementsJSON), f.Data, author, message); err != nil {
		return fmt.Errorf("file create version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("file create commit: %w", err)
	}
	return nil
}

func (r *FileRepo) Get(ctx context.Context, id int64) (*File, error) {
	var f File
	var parent sql.NullInt64
	var mime, elements sql.NullString
	var created, updated string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, study_id, parent_id, name, kind, mime, content, elements_json, data, size, created_at, updated_at
		 FROM files WHERE id = ?`,
		id).Scan(&f.ID, &f.StudyID, &parent, &f.Name, &f.Kind, &mime, &f.Content, &elements, &f.Data, &f.Size, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("file get: %w", err)
	}
	f.ParentID = parent.Int64
	f.Mime = mime.String
	f.ElementsJSON = elements.String
	f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &f, nil
}

// ListByStudy returns all of the study's files, folders first, then by name.
func (r *FileRepo) ListByStudy(ctx context.Context, studyID int64) ([]File, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, study_id, parent_id, name, kind, mime, content, elements_json, data, size, created_at, updated_at
		 FROM files WHERE study_id = ?
		 ORDER BY CASE kind WHEN 'folder' THEN 0 ELSE 1 END, name`,
		studyID)
	if err != nil {
		return nil, fmt.Errorf("file list: %w", err)
	}
	defer rows.Close()
	var out []File
	for rows.Next() {
		var f File
		var parent sql.NullInt64
		var mime, elements sql.NullString
		var created, updated string
		if err := rows.Scan(&f.ID, &f.StudyID, &parent, &f.Name, &f.Kind, &mime, &f.Content, &elements, &f.Data, &f.Size, &created, &updated); err != nil {
			return nil, fmt.Errorf("file list scan: %w", err)
		}
		f.ParentID = parent.Int64
		f.Mime = mime.String
		f.ElementsJSON = elements.String
		f.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		f.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpdateContent rewrites the file's content and records a version snapshot.
// Returns an error when no file matches id+studyID.
func (r *FileRepo) UpdateContent(ctx context.Context, id, studyID int64, content, author, message string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("file update content tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE files SET content=?, size=?, updated_at=datetime('now') WHERE id=? AND study_id=?`,
		content, len(content), id, studyID)
	if err != nil {
		return fmt.Errorf("file update content: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("file update content rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("file update content: file %d not found in study %d", id, studyID)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO file_versions(file_id, content, elements_json, data, author, message)
		 SELECT ?, ?, elements_json, data, ?, ? FROM files WHERE id = ?`,
		id, content, author, message, id); err != nil {
		return fmt.Errorf("file update content version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("file update content commit: %w", err)
	}
	return nil
}

func (r *FileRepo) Rename(ctx context.Context, id, studyID int64, name string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE files SET name=?, updated_at=datetime('now') WHERE id=? AND study_id=?`,
		name, id, studyID)
	if err != nil {
		return fmt.Errorf("file rename: %w", err)
	}
	return nil
}

// Move re-parents a file; parentID 0 means root. Moving a file into itself is
// rejected.
func (r *FileRepo) Move(ctx context.Context, id, studyID, parentID int64) error {
	if parentID == id {
		return fmt.Errorf("file move: cannot move folder into itself")
	}
	var parent any
	if parentID != 0 {
		parent = parentID
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE files SET parent_id=?, updated_at=datetime('now') WHERE id=? AND study_id=?`,
		parent, id, studyID)
	if err != nil {
		return fmt.Errorf("file move: %w", err)
	}
	return nil
}

func (r *FileRepo) Delete(ctx context.Context, id, studyID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM files WHERE id=? AND study_id=?`, id, studyID)
	if err != nil {
		return fmt.Errorf("file delete: %w", err)
	}
	return nil
}

// Versions returns the file's version snapshots, newest first.
func (r *FileRepo) Versions(ctx context.Context, fileID int64) ([]FileVersion, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, file_id, content, elements_json, data, author, message, created_at
		 FROM file_versions WHERE file_id = ?
		 ORDER BY created_at DESC, id DESC`,
		fileID)
	if err != nil {
		return nil, fmt.Errorf("file versions: %w", err)
	}
	defer rows.Close()
	var out []FileVersion
	for rows.Next() {
		var v FileVersion
		var elements, message sql.NullString
		var created string
		if err := rows.Scan(&v.ID, &v.FileID, &v.Content, &elements, &v.Data, &v.Author, &message, &created); err != nil {
			return nil, fmt.Errorf("file versions scan: %w", err)
		}
		v.Message = message.String
		v.ElementsJSON = elements.String
		v.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (r *FileRepo) GetVersion(ctx context.Context, versionID, fileID int64) (*FileVersion, error) {
	var v FileVersion
	var elements, message sql.NullString
	var created string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, file_id, content, elements_json, data, author, message, created_at
		 FROM file_versions WHERE id = ? AND file_id = ?`,
		versionID, fileID).Scan(&v.ID, &v.FileID, &v.Content, &elements, &v.Data, &v.Author, &message, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("file get version: %w", err)
	}
	v.Message = message.String
	v.ElementsJSON = elements.String
	v.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	return &v, nil
}

// RestoreVersion copies a version's content/data back into the file and records
// a new "restaurado da versão N" snapshot.
func (r *FileRepo) RestoreVersion(ctx context.Context, fileID, versionID, studyID int64) error {
	f, err := r.Get(ctx, fileID)
	if err != nil {
		return err
	}
	if f == nil {
		return fmt.Errorf("file restore: file %d not found", fileID)
	}
	v, err := r.GetVersion(ctx, versionID, fileID)
	if err != nil {
		return err
	}
	if v == nil {
		return fmt.Errorf("file restore: version %d not found", versionID)
	}
	size := int64(len(v.Content))
	if f.Kind == "image" {
		size = int64(len(v.Data))
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("file restore tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`UPDATE files SET content=?, elements_json=?, data=?, size=?, updated_at=datetime('now') WHERE id=? AND study_id=?`,
		v.Content, nullableString(v.ElementsJSON), v.Data, size, fileID, studyID)
	if err != nil {
		return fmt.Errorf("file restore: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("file restore rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("file restore: file %d not found in study %d", fileID, studyID)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO file_versions(file_id, content, elements_json, data, author, message) VALUES(?,?,?,?,?,?)`,
		fileID, v.Content, nullableString(v.ElementsJSON), v.Data, "user", fmt.Sprintf("restaurado da versão %d", versionID)); err != nil {
		return fmt.Errorf("file restore version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("file restore commit: %w", err)
	}
	return nil
}

// Chat is a branching conversation within a study.
type Chat struct {
	ID, StudyID          int64
	Title                string
	CreatedAt, UpdatedAt time.Time
}

// ChatMessage is one message in a chat; ParentID links form the branch tree
// (0 = root, stored as NULL).
type ChatMessage struct {
	ID, ChatID, ParentID                              int64
	Role, Content                                     string
	SourcesJSON, ToolLogJSON, ElementsJSON, UsageJSON string
	TurnID, TurnStatus, TurnError                     string
	Saved                                             bool
	CreatedAt                                         time.Time
}

type ChatRepo struct{ db *sql.DB }

func NewChatRepo(db *sql.DB) *ChatRepo { return &ChatRepo{db: db} }

func (r *ChatRepo) Create(ctx context.Context, c *Chat) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO chats(study_id, title) VALUES(?,?)`,
		c.StudyID, c.Title)
	if err != nil {
		return fmt.Errorf("chat create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("chat create last id: %w", err)
	}
	c.ID = id
	now := time.Now().UTC()
	c.CreatedAt = now
	c.UpdatedAt = now
	return nil
}

// ListByStudy returns the study's chats, most recently updated first.
func (r *ChatRepo) ListByStudy(ctx context.Context, studyID int64) ([]Chat, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, study_id, title, created_at, updated_at
		 FROM chats WHERE study_id = ?
		 ORDER BY updated_at DESC, id DESC`,
		studyID)
	if err != nil {
		return nil, fmt.Errorf("chat list: %w", err)
	}
	defer rows.Close()
	var out []Chat
	for rows.Next() {
		var c Chat
		var created, updated string
		if err := rows.Scan(&c.ID, &c.StudyID, &c.Title, &created, &updated); err != nil {
			return nil, fmt.Errorf("chat list scan: %w", err)
		}
		c.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		c.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *ChatRepo) Get(ctx context.Context, id int64) (*Chat, error) {
	var c Chat
	var created, updated string
	err := r.db.QueryRowContext(ctx,
		`SELECT id, study_id, title, created_at, updated_at FROM chats WHERE id = ?`,
		id).Scan(&c.ID, &c.StudyID, &c.Title, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chat get: %w", err)
	}
	c.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	c.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &c, nil
}

func (r *ChatRepo) Rename(ctx context.Context, id, studyID int64, title string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE chats SET title=?, updated_at=datetime('now') WHERE id=? AND study_id=?`,
		title, id, studyID)
	if err != nil {
		return fmt.Errorf("chat rename: %w", err)
	}
	return nil
}

func (r *ChatRepo) Delete(ctx context.Context, id, studyID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM chats WHERE id=? AND study_id=?`, id, studyID)
	if err != nil {
		return fmt.Errorf("chat delete: %w", err)
	}
	return nil
}

// Touch bumps the chat's updated_at.
func (r *ChatRepo) Touch(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE chats SET updated_at=datetime('now') WHERE id=?`, id)
	if err != nil {
		return fmt.Errorf("chat touch: %w", err)
	}
	return nil
}

func (r *ChatRepo) AddMessage(ctx context.Context, m *ChatMessage) error {
	var parent, sources, toolLog, elements, usage any
	if m.ParentID != 0 {
		parent = m.ParentID
	}
	if m.SourcesJSON != "" {
		sources = m.SourcesJSON
	}
	if m.ToolLogJSON != "" {
		toolLog = m.ToolLogJSON
	}
	if m.ElementsJSON != "" {
		elements = m.ElementsJSON
	}
	if m.UsageJSON != "" {
		usage = m.UsageJSON
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO chat_messages(chat_id, parent_id, role, content, sources_json, tool_log_json, elements_json, usage_json, saved)
		 VALUES(?,?,?,?,?,?,?,?,?)`,
		m.ChatID, parent, m.Role, m.Content, sources, toolLog, elements, usage, boolInt(m.Saved))
	if err != nil {
		return fmt.Errorf("chat add message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("chat add message last id: %w", err)
	}
	m.ID = id
	return nil
}

// Messages returns the chat's messages in insertion order.
func (r *ChatRepo) Messages(ctx context.Context, chatID int64) ([]ChatMessage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT m.id, m.chat_id, m.parent_id, m.role, m.content, m.sources_json, m.tool_log_json,
			m.elements_json, m.usage_json, m.saved, m.created_at,
			COALESCE(t.id,''), COALESCE(t.status,''), COALESCE(t.error_message,'')
		 FROM chat_messages m LEFT JOIN chat_turns t ON t.user_message_id=m.id
		 WHERE m.chat_id = ?
		 ORDER BY m.id ASC`,
		chatID)
	if err != nil {
		return nil, fmt.Errorf("chat messages: %w", err)
	}
	defer rows.Close()
	var out []ChatMessage
	for rows.Next() {
		var m ChatMessage
		var parent sql.NullInt64
		var sources, toolLog, elements, usage sql.NullString
		var turnID, turnStatus, turnError sql.NullString
		var saved int
		var created string
		if err := rows.Scan(&m.ID, &m.ChatID, &parent, &m.Role, &m.Content, &sources, &toolLog, &elements, &usage, &saved, &created, &turnID, &turnStatus, &turnError); err != nil {
			return nil, fmt.Errorf("chat messages scan: %w", err)
		}
		m.ParentID = parent.Int64
		m.SourcesJSON = sources.String
		m.ToolLogJSON = toolLog.String
		m.ElementsJSON = elements.String
		m.UsageJSON = usage.String
		m.TurnID = turnID.String
		m.TurnStatus = turnStatus.String
		m.TurnError = turnError.String
		m.Saved = saved != 0
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *ChatRepo) SetSaved(ctx context.Context, messageID, chatID int64, saved bool) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE chat_messages SET saved=? WHERE id=? AND chat_id=?`,
		boolInt(saved), messageID, chatID)
	if err != nil {
		return fmt.Errorf("chat set saved: %w", err)
	}
	return nil
}

// BranchFrom creates a new chat containing the parent-chain prefix ending at
// messageID (root→message order), with fresh parent links. The original chat is
// left untouched.
func (r *ChatRepo) BranchFrom(ctx context.Context, chatID, messageID, studyID int64) (*Chat, error) {
	var origTitle string
	err := r.db.QueryRowContext(ctx,
		`SELECT title FROM chats WHERE id = ? AND study_id = ?`,
		chatID, studyID).Scan(&origTitle)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("chat branch: chat %d not found", chatID)
	}
	if err != nil {
		return nil, fmt.Errorf("chat branch: %w", err)
	}
	type chainMsg struct {
		role, content, elements, usage string
		parent                         sql.NullInt64
	}
	var chain []chainMsg
	cur := sql.NullInt64{Int64: messageID, Valid: true}
	seen := map[int64]bool{}
	for cur.Valid {
		if seen[cur.Int64] {
			return nil, fmt.Errorf("chat branch: cycle in message chain")
		}
		seen[cur.Int64] = true
		var m chainMsg
		var elements, usage sql.NullString
		err := r.db.QueryRowContext(ctx,
			`SELECT role, content, elements_json, usage_json, parent_id FROM chat_messages WHERE id = ? AND chat_id = ?`,
			cur.Int64, chatID).Scan(&m.role, &m.content, &elements, &usage, &m.parent)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("chat branch: message %d not found", cur.Int64)
		}
		if err != nil {
			return nil, fmt.Errorf("chat branch walk: %w", err)
		}
		m.elements = elements.String
		m.usage = usage.String
		chain = append(chain, m)
		cur = m.parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	title := "Ramificação de " + origTitle
	if runes := []rune(title); len(runes) > 80 {
		title = string(runes[:80])
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("chat branch tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO chats(study_id, title) VALUES(?,?)`, studyID, title)
	if err != nil {
		return nil, fmt.Errorf("chat branch create: %w", err)
	}
	newID, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("chat branch create last id: %w", err)
	}
	var parent any
	for _, m := range chain {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO chat_messages(chat_id, parent_id, role, content, elements_json, usage_json) VALUES(?,?,?,?,?,?)`,
			newID, parent, m.role, m.content, nullableString(m.elements), nullableString(m.usage))
		if err != nil {
			return nil, fmt.Errorf("chat branch message: %w", err)
		}
		mid, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("chat branch message last id: %w", err)
		}
		parent = mid
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("chat branch commit: %w", err)
	}
	return r.Get(ctx, newID)
}

// Highlight is a saved excerpt from a note or a chat message.
type Highlight struct {
	ID, StudyID   int64
	SourceKind    string
	SourceID      int64
	Excerpt, Note string
	CreatedAt     time.Time
}

type HighlightRepo struct{ db *sql.DB }

func NewHighlightRepo(db *sql.DB) *HighlightRepo { return &HighlightRepo{db: db} }

func (r *HighlightRepo) Create(ctx context.Context, h *Highlight) error {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO highlights(study_id, source_kind, source_id, excerpt, note) VALUES(?,?,?,?,?)`,
		h.StudyID, h.SourceKind, h.SourceID, h.Excerpt, h.Note)
	if err != nil {
		return fmt.Errorf("highlight create: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("highlight create last id: %w", err)
	}
	h.ID = id
	return nil
}

// ListByStudy returns the study's highlights, newest first.
func (r *HighlightRepo) ListByStudy(ctx context.Context, studyID int64) ([]Highlight, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, study_id, source_kind, source_id, excerpt, note, created_at
		 FROM highlights WHERE study_id = ?
		 ORDER BY created_at DESC, id DESC`,
		studyID)
	if err != nil {
		return nil, fmt.Errorf("highlight list: %w", err)
	}
	defer rows.Close()
	var out []Highlight
	for rows.Next() {
		var h Highlight
		var note sql.NullString
		var created string
		if err := rows.Scan(&h.ID, &h.StudyID, &h.SourceKind, &h.SourceID, &h.Excerpt, &note, &created); err != nil {
			return nil, fmt.Errorf("highlight list scan: %w", err)
		}
		h.Note = note.String
		h.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, h)
	}
	return out, rows.Err()
}

func (r *HighlightRepo) Delete(ctx context.Context, id, studyID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM highlights WHERE id=? AND study_id=?`, id, studyID)
	if err != nil {
		return fmt.Errorf("highlight delete: %w", err)
	}
	return nil
}
