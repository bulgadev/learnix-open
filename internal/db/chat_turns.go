package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// ChatTurn is the durable lifecycle of one user question and its assistant
// answer. The user message is created with the turn so retries never duplicate
// the visible question.
type ChatTurn struct {
	ID                 string
	ChatID             int64
	StudyID            int64
	UserID             int64
	ClientKey          string
	UserMessageID      int64
	AssistantMessageID int64
	Status             string
	Attempt            int
	Web                bool
	ErrorCode          string
	ErrorMessage       string
	ProviderStatus     int
	CreatedAt          time.Time
	StartedAt          *time.Time
	FinishedAt         *time.Time
	UpdatedAt          time.Time
}

type ChatTurnRepo struct{ db *sql.DB }

func NewChatTurnRepo(database *sql.DB) *ChatTurnRepo { return &ChatTurnRepo{db: database} }

// NewChatTurnID returns a collision-resistant opaque id safe to expose in URLs.
func NewChatTurnID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Create is idempotent for (user, chat, clientKey). It inserts the user
// message and its turn in one transaction, so a lost HTTP response can be
// safely retried without creating a second question.
func (r *ChatTurnRepo) Create(ctx context.Context, turn *ChatTurn, message string, parentID int64) error {
	if turn == nil || turn.ID == "" || turn.ClientKey == "" || strings.TrimSpace(message) == "" {
		return fmt.Errorf("chat turn create: invalid input")
	}
	existing, err := r.ByClientKey(ctx, turn.UserID, turn.ChatID, turn.ClientKey)
	if err != nil {
		return err
	}
	if existing != nil {
		*turn = *existing
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("chat turn create tx: %w", err)
	}
	defer tx.Rollback()

	var existingID string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM chat_turns WHERE user_id=? AND chat_id=? AND client_key=?`,
		turn.UserID, turn.ChatID, turn.ClientKey).Scan(&existingID)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("chat turn existing commit: %w", err)
		}
		existing, getErr := r.Get(ctx, existingID, turn.UserID, turn.StudyID, turn.ChatID)
		if getErr != nil {
			return getErr
		}
		if existing == nil {
			return fmt.Errorf("chat turn existing row disappeared")
		}
		*turn = *existing
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("chat turn lookup: %w", err)
	}

	var parent any
	if parentID != 0 {
		parent = parentID
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO chat_messages(chat_id, parent_id, role, content, saved) VALUES(?,?,?,?,0)`,
		turn.ChatID, parent, "user", message)
	if err != nil {
		return fmt.Errorf("chat turn user message: %w", err)
	}
	turn.UserMessageID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("chat turn user message id: %w", err)
	}
	web := 0
	if turn.Web {
		web = 1
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO chat_turns(id, chat_id, study_id, user_id, client_key, user_message_id, status, attempt, web)
		 VALUES(?,?,?,?,?,?, 'queued', 1, ?)`,
		turn.ID, turn.ChatID, turn.StudyID, turn.UserID, turn.ClientKey, turn.UserMessageID, web)
	if err != nil {
		// A concurrent request with the same client key may have committed
		// between the initial lookup and this insert. Its committed turn is the
		// idempotent answer; never expose the transient uniqueness error.
		_ = tx.Rollback()
		if existing, lookupErr := r.ByClientKey(ctx, turn.UserID, turn.ChatID, turn.ClientKey); lookupErr == nil && existing != nil {
			*turn = *existing
			return nil
		}
		return fmt.Errorf("chat turn insert: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("chat turn create commit: %w", err)
	}
	turn.Status = "queued"
	turn.Attempt = 1
	turn.CreatedAt = time.Now().UTC()
	turn.UpdatedAt = turn.CreatedAt
	return nil
}

func (r *ChatTurnRepo) Get(ctx context.Context, id string, userID, studyID, chatID int64) (*ChatTurn, error) {
	var turn ChatTurn
	var created, updated string
	var startedAt, finishedAt sql.NullString
	var web int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, chat_id, study_id, user_id, client_key, user_message_id,
			COALESCE(assistant_message_id,0), status, attempt, web, COALESCE(error_code,''),
			COALESCE(error_message,''), COALESCE(provider_status,0), created_at, started_at,
			finished_at, updated_at
		 FROM chat_turns WHERE id=? AND user_id=? AND study_id=? AND chat_id=?`,
		id, userID, studyID, chatID).Scan(
		&turn.ID, &turn.ChatID, &turn.StudyID, &turn.UserID, &turn.ClientKey,
		&turn.UserMessageID, &turn.AssistantMessageID, &turn.Status, &turn.Attempt, &web,
		&turn.ErrorCode, &turn.ErrorMessage, &turn.ProviderStatus, &created, &startedAt,
		&finishedAt, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chat turn get: %w", err)
	}
	turn.Web = web != 0
	turn.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	turn.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	if startedAt.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", startedAt.String)
		turn.StartedAt = &t
	}
	if finishedAt.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", finishedAt.String)
		turn.FinishedAt = &t
	}
	return &turn, nil
}

func (r *ChatTurnRepo) GetAny(ctx context.Context, id string) (*ChatTurn, error) {
	var turn ChatTurn
	var created, updated string
	var startedAt, finishedAt sql.NullString
	var web int
	err := r.db.QueryRowContext(ctx,
		`SELECT id, chat_id, study_id, user_id, client_key, user_message_id,
			COALESCE(assistant_message_id,0), status, attempt, web, COALESCE(error_code,''),
			COALESCE(error_message,''), COALESCE(provider_status,0), created_at, started_at,
			finished_at, updated_at
		 FROM chat_turns WHERE id=?`, id).Scan(
		&turn.ID, &turn.ChatID, &turn.StudyID, &turn.UserID, &turn.ClientKey,
		&turn.UserMessageID, &turn.AssistantMessageID, &turn.Status, &turn.Attempt, &web,
		&turn.ErrorCode, &turn.ErrorMessage, &turn.ProviderStatus, &created, &startedAt,
		&finishedAt, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chat turn get any: %w", err)
	}
	turn.Web = web != 0
	turn.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	turn.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	if startedAt.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", startedAt.String)
		turn.StartedAt = &t
	}
	if finishedAt.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", finishedAt.String)
		turn.FinishedAt = &t
	}
	return &turn, nil
}

func (r *ChatTurnRepo) ByClientKey(ctx context.Context, userID, chatID int64, clientKey string) (*ChatTurn, error) {
	var id string
	var studyID int64
	err := r.db.QueryRowContext(ctx,
		`SELECT id, study_id FROM chat_turns WHERE user_id=? AND chat_id=? AND client_key=?`,
		userID, chatID, clientKey).Scan(&id, &studyID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("chat turn by client key: %w", err)
	}
	return r.Get(ctx, id, userID, studyID, chatID)
}

func (r *ChatTurnRepo) ForChat(ctx context.Context, chatID int64) (map[int64]ChatTurn, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, chat_id, study_id, user_id, client_key, user_message_id,
			COALESCE(assistant_message_id,0), status, attempt, web, COALESCE(error_code,''),
			COALESCE(error_message,''), COALESCE(provider_status,0), created_at, started_at,
			finished_at, updated_at
		 FROM chat_turns WHERE chat_id=? ORDER BY created_at ASC, id ASC`, chatID)
	if err != nil {
		return nil, fmt.Errorf("chat turns for chat: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]ChatTurn)
	for rows.Next() {
		turn, err := scanChatTurn(rows)
		if err != nil {
			return nil, err
		}
		out[turn.UserMessageID] = turn
	}
	return out, rows.Err()
}

func (r *ChatTurnRepo) MarkRunning(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE chat_turns SET status='running', started_at=COALESCE(started_at,datetime('now')),
			updated_at=datetime('now') WHERE id=? AND status='queued'`, id)
	if err != nil {
		return fmt.Errorf("chat turn running: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("chat turn %s is not queued", id)
	}
	return nil
}

func (r *ChatTurnRepo) Retry(ctx context.Context, id string, userID, studyID, chatID int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE chat_turns SET status='queued', attempt=attempt+1, error_code=NULL,
			error_message=NULL, provider_status=NULL, started_at=NULL, finished_at=NULL,
			updated_at=datetime('now') WHERE id=? AND user_id=? AND study_id=? AND chat_id=? AND status='failed'`,
		id, userID, studyID, chatID)
	if err != nil {
		return fmt.Errorf("chat turn retry: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("chat turn is not retryable")
	}
	return nil
}

func (r *ChatTurnRepo) Fail(ctx context.Context, id, code, message string, providerStatus int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE chat_turns SET status='failed', error_code=?, error_message=?, provider_status=?,
			finished_at=datetime('now'), updated_at=datetime('now') WHERE id=? AND status IN ('queued','running')`,
		code, message, providerStatus, id)
	if err != nil {
		return fmt.Errorf("chat turn fail: %w", err)
	}
	return nil
}

func (r *ChatTurnRepo) Complete(ctx context.Context, turnID string, message *ChatMessage) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("chat turn complete tx: %w", err)
	}
	defer tx.Rollback()
	var status string
	var chatID, userMessageID int64
	err = tx.QueryRowContext(ctx,
		`SELECT status, chat_id, user_message_id FROM chat_turns WHERE id=?`, turnID).
		Scan(&status, &chatID, &userMessageID)
	if err != nil {
		return fmt.Errorf("chat turn complete lookup: %w", err)
	}
	if status == "succeeded" {
		return tx.Commit()
	}
	if status != "running" {
		return fmt.Errorf("chat turn complete: status %s", status)
	}
	message.ChatID = chatID
	message.ParentID = userMessageID
	var sources, toolLog, elements, usage any
	if message.SourcesJSON != "" {
		sources = message.SourcesJSON
	}
	if message.ToolLogJSON != "" {
		toolLog = message.ToolLogJSON
	}
	if message.ElementsJSON != "" {
		elements = message.ElementsJSON
	}
	if message.UsageJSON != "" {
		usage = message.UsageJSON
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO chat_messages(chat_id, parent_id, role, content, sources_json, tool_log_json, elements_json, usage_json, saved)
		 VALUES(?,?,?,?,?,?,?,?,?)`, chatID, userMessageID, "assistant", message.Content, sources, toolLog, elements, usage, boolInt(message.Saved))
	if err != nil {
		return fmt.Errorf("chat turn assistant message: %w", err)
	}
	message.ID, err = res.LastInsertId()
	if err != nil {
		return fmt.Errorf("chat turn assistant id: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE chat_turns SET status='succeeded', assistant_message_id=?, error_code=NULL,
			error_message=NULL, finished_at=datetime('now'), updated_at=datetime('now') WHERE id=? AND status='running'`,
		message.ID, turnID)
	if err != nil {
		return fmt.Errorf("chat turn success: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET updated_at=datetime('now') WHERE id=?`, chatID); err != nil {
		return fmt.Errorf("chat turn touch: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("chat turn complete commit: %w", err)
	}
	return nil
}

func (r *ChatTurnRepo) MarkInterrupted(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE chat_turns SET status='failed', error_code='server_restart',
			error_message='A geração foi interrompida; tente novamente.', finished_at=datetime('now'), updated_at=datetime('now')
		 WHERE status IN ('queued','running')`)
	if err != nil {
		return fmt.Errorf("chat turns interrupt: %w", err)
	}
	return nil
}

type chatTurnScanner interface{ Scan(...any) error }

func scanChatTurn(s chatTurnScanner) (ChatTurn, error) {
	var turn ChatTurn
	var created, updated string
	var startedAt, finishedAt sql.NullString
	var web int
	err := s.Scan(&turn.ID, &turn.ChatID, &turn.StudyID, &turn.UserID, &turn.ClientKey,
		&turn.UserMessageID, &turn.AssistantMessageID, &turn.Status, &turn.Attempt, &web,
		&turn.ErrorCode, &turn.ErrorMessage, &turn.ProviderStatus, &created, &startedAt,
		&finishedAt, &updated)
	if err != nil {
		return ChatTurn{}, fmt.Errorf("chat turn scan: %w", err)
	}
	turn.Web = web != 0
	turn.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
	turn.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	if startedAt.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", startedAt.String)
		turn.StartedAt = &t
	}
	if finishedAt.Valid {
		t, _ := time.Parse("2006-01-02 15:04:05", finishedAt.String)
		turn.FinishedAt = &t
	}
	return turn, nil
}
