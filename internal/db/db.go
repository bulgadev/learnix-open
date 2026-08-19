package db

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"learnix/internal/session"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (or creates) a SQLite database at path and enables foreign keys,
// WAL journaling, and a busy timeout. The driver is modernc.org/sqlite (pure Go,
// no CGO) so the binary stays portable.
func Open(path string) (*sql.DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

// Migrate runs all embedded SQL migrations in filename order. Idempotent because
// every migration uses CREATE TABLE IF NOT EXISTS.
func Migrate(db *sql.DB) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			return fmt.Errorf("exec migration %s: %w", e.Name(), err)
		}
	}
	if err := migrateLegacyChats(context.Background(), db); err != nil {
		return fmt.Errorf("migrate legacy chats: %w", err)
	}
	if err := ensureLearningElementColumns(context.Background(), db); err != nil {
		return fmt.Errorf("migrate learning elements: %w", err)
	}
	if err := ensureLeaderboardColumns(context.Background(), db); err != nil {
		return fmt.Errorf("migrate leaderboard columns: %w", err)
	}
	if err := ensureTestColumns(context.Background(), db); err != nil {
		return fmt.Errorf("migrate test definitions: %w", err)
	}
	if err := ensurePublicHandleColumns(context.Background(), db); err != nil {
		return fmt.Errorf("migrate public handles: %w", err)
	}
	if err := ensureExistingUserMetadata(context.Background(), db); err != nil {
		return fmt.Errorf("migrate user metadata: %w", err)
	}
	return nil
}

func ensureTestColumns(ctx context.Context, database *sql.DB) error {
	if err := ensureTableColumns(ctx, database, "quizzes", map[string]string{
		"test_id":       "INTEGER",
		"exam":          "INTEGER NOT NULL DEFAULT 0",
		"exam_deadline": "TEXT",
		"flags_json":    "TEXT",
		"tutor_json":    "TEXT",
	}); err != nil {
		return err
	}
	if err := ensureTableColumns(ctx, database, "test_definitions", map[string]string{
		"exam": "INTEGER NOT NULL DEFAULT 0",
	}); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_quizzes_test ON quizzes(test_id)`); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO test_definitions(user_id, topic, mode, preset, created_at, updated_at)
		SELECT user_id, topic, COALESCE(NULLIF(mode, ''), 'practice'), COALESCE(NULLIF(preset, ''), 'moderate'), MIN(created_at), MAX(created_at)
		FROM quizzes
		WHERE study_id IS NULL AND test_id IS NULL
		GROUP BY user_id, topic, COALESCE(NULLIF(mode, ''), 'practice'), COALESCE(NULLIF(preset, ''), 'moderate')`); err != nil {
		return err
	}
	_, err := database.ExecContext(ctx, `
		UPDATE quizzes
		SET test_id = (
			SELECT t.id FROM test_definitions t
			WHERE t.user_id = quizzes.user_id
			  AND t.topic = quizzes.topic
			  AND t.mode = COALESCE(NULLIF(quizzes.mode, ''), 'practice')
			  AND t.preset = COALESCE(NULLIF(quizzes.preset, ''), 'moderate')
			ORDER BY t.id LIMIT 1
		)
		WHERE study_id IS NULL AND test_id IS NULL`)
	return err
}

func ensurePublicHandleColumns(ctx context.Context, db *sql.DB) error {
	return ensureTableColumns(ctx, db, "user_profiles", map[string]string{
		"username":        "TEXT NOT NULL DEFAULT ''",
		"tag":             "TEXT NOT NULL DEFAULT '0000'",
		"slug_changed_at": "TEXT",
	})
}

func ensureLearningElementColumns(ctx context.Context, db *sql.DB) error {
	for table, columns := range map[string][]string{
		"chat_messages": {"elements_json", "usage_json"},
		"files":         {"elements_json"},
		"file_versions": {"elements_json"},
		"quizzes":       {"confidence_json", "trace_json", "assessments_json", "adaptive_from_id"},
	} {
		for _, column := range columns {
			rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
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
				if _, err := db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" TEXT"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// migrateLegacyChats copies non-empty legacy studies.chat_json entries into the
// chats/chat_messages tables. Idempotent: studies that already have chats rows
// are skipped. The legacy studies.chat_json and history_json columns are left
// untouched.
func migrateLegacyChats(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx,
		`SELECT s.id, s.topic, s.chat_json
		 FROM studies s
		 WHERE s.chat_json IS NOT NULL AND s.chat_json != '' AND s.chat_json != '[]'
		   AND NOT EXISTS (SELECT 1 FROM chats c WHERE c.study_id = s.id)`)
	if err != nil {
		return fmt.Errorf("legacy chats query: %w", err)
	}
	type legacyStudy struct {
		id       int64
		topic    string
		chatJSON string
	}
	var pending []legacyStudy
	for rows.Next() {
		var s legacyStudy
		if err := rows.Scan(&s.id, &s.topic, &s.chatJSON); err != nil {
			rows.Close()
			return fmt.Errorf("legacy chats scan: %w", err)
		}
		pending = append(pending, s)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("legacy chats rows: %w", err)
	}
	rows.Close()

	for _, s := range pending {
		var msgs []session.ChatMsg
		if err := json.Unmarshal([]byte(s.chatJSON), &msgs); err != nil || len(msgs) == 0 {
			continue
		}
		var chain []legacyMsg
		for _, m := range msgs {
			var role string
			switch m.Role {
			case "user":
				role = "user"
			case "ai":
				role = "assistant"
			default:
				continue
			}
			chain = append(chain, legacyMsg{role, m.Content})
		}
		if len(chain) == 0 {
			continue
		}
		title := strings.TrimSpace(s.topic)
		if runes := []rune(title); len(runes) > 60 {
			title = string(runes[:60])
		}
		if title == "" {
			title = "Conversa"
		}
		if err := insertLegacyChat(ctx, db, s.id, title, chain); err != nil {
			return err
		}
	}
	return nil
}

type legacyMsg struct {
	role    string
	content string
}

func insertLegacyChat(ctx context.Context, db *sql.DB, studyID int64, title string, chain []legacyMsg) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("legacy chats tx: %w", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO chats(study_id, title) VALUES(?,?)`, studyID, title)
	if err != nil {
		return fmt.Errorf("legacy chats insert chat: %w", err)
	}
	chatID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("legacy chats chat id: %w", err)
	}
	var parent any
	for _, m := range chain {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO chat_messages(chat_id, parent_id, role, content) VALUES(?,?,?,?)`,
			chatID, parent, m.role, m.content)
		if err != nil {
			return fmt.Errorf("legacy chats insert message: %w", err)
		}
		msgID, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("legacy chats message id: %w", err)
		}
		parent = msgID
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("legacy chats commit: %w", err)
	}
	return nil
}

// NewTestDB opens a fresh SQLite file in a per-test temp directory, migrates it,
// and returns a cleanup function. For use in tests only.
func NewTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	path := fmt.Sprintf("%s/test.db", t.TempDir())
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	if err := Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate test db: %v", err)
	}
	return db, func() {
		_ = db.Close()
	}
}
