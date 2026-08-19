package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Quota is a user's AI token budget: Quota is the admin-granted allowance and
// Used is how much has been consumed. A missing row means zero allowance.
type Quota struct {
	UserID, Quota, Used int64
	// LifetimeUsed is the sum of all usage_log entries and is not affected by
	// an administrator resetting the current allowance.
	LifetimeUsed int64
}

// Exhausted reports whether no token allowance remains. A nil Quota (no row)
// counts as exhausted.
func (q *Quota) Exhausted() bool {
	return q == nil || q.Used >= q.Quota
}

// UserUsage is the admin-list projection of one user with their quota state.
type UserUsage struct {
	ID        int64
	Email     string
	CreatedAt time.Time
	Quota     int64
	Used      int64
}

// UsageEntry is one usage_log row joined with the user's email.
type UsageEntry struct {
	ID, UserID int64
	Email      string
	Kind       string
	Tokens     int64
	CreatedAt  time.Time
}

// QuotaRepo persists per-user AI token quotas and the usage log.
type QuotaRepo struct{ db *sql.DB }

func NewQuotaRepo(db *sql.DB) *QuotaRepo { return &QuotaRepo{db: db} }

// Get returns the user's quota row, or nil when the user has no row.
func (r *QuotaRepo) Get(ctx context.Context, userID int64) (*Quota, error) {
	var q Quota
	err := r.db.QueryRowContext(ctx,
		`SELECT user_id, quota, used,
			COALESCE((SELECT SUM(tokens) FROM usage_log WHERE user_id = ?), 0)
		 FROM user_quota WHERE user_id = ?`,
		userID, userID).Scan(&q.UserID, &q.Quota, &q.Used, &q.LifetimeUsed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("quota get: %w", err)
	}
	return &q, nil
}

// SetQuota upserts the user's token allowance without touching used, so
// re-granting a budget never resets consumption. Negative quotas are rejected.
func (r *QuotaRepo) SetQuota(ctx context.Context, userID, quota int64) error {
	if quota < 0 {
		return fmt.Errorf("quota set: quota must not be negative")
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO user_quota(user_id, quota) VALUES(?,?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   quota=excluded.quota, updated_at=datetime('now')`,
		userID, quota)
	if err != nil {
		return fmt.Errorf("quota set: %w", err)
	}
	return nil
}

// AddUsage records tokens consumed by a "chat" or "quiz" call: in one
// transaction it increments the user's used counter (creating the quota row
// when missing) and appends a usage_log entry. Non-positive tokens are a
// no-op.
func (r *QuotaRepo) AddUsage(ctx context.Context, userID, tokens int64, kind string) error {
	if tokens <= 0 {
		return nil
	}
	if kind != "chat" && kind != "quiz" {
		return fmt.Errorf("quota add usage: invalid kind %q", kind)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("quota add usage tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO user_quota(user_id, used) VALUES(?,?)
		 ON CONFLICT(user_id) DO UPDATE SET
		   used = user_quota.used + excluded.used, updated_at=datetime('now')`,
		userID, tokens); err != nil {
		return fmt.Errorf("quota add usage: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO usage_log(user_id, kind, tokens) VALUES(?,?,?)`,
		userID, kind, tokens); err != nil {
		return fmt.Errorf("quota add usage log: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("quota add usage commit: %w", err)
	}
	return nil
}

// ResetUsage zeros the user's used counter, keeping the granted quota. Users
// without a quota row are left untouched.
func (r *QuotaRepo) ResetUsage(ctx context.Context, userID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE user_quota SET used=0, updated_at=datetime('now') WHERE user_id=?`,
		userID)
	if err != nil {
		return fmt.Errorf("quota reset usage: %w", err)
	}
	return nil
}

// ListAll returns every user with their quota state; users without a quota
// row appear with 0/0. Newest users first.
func (r *QuotaRepo) ListAll(ctx context.Context) ([]UserUsage, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT u.id, u.email, u.created_at, COALESCE(q.quota, 0), COALESCE(q.used, 0)
		 FROM users u LEFT JOIN user_quota q ON q.user_id = u.id
		 ORDER BY u.created_at DESC, u.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("quota list all: %w", err)
	}
	defer rows.Close()
	var out []UserUsage
	for rows.Next() {
		var uu UserUsage
		var created string
		if err := rows.Scan(&uu.ID, &uu.Email, &created, &uu.Quota, &uu.Used); err != nil {
			return nil, fmt.Errorf("quota list all scan: %w", err)
		}
		uu.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, uu)
	}
	return out, rows.Err()
}

// RecentUsage returns the newest usage_log entries joined with the user's
// email; limit is clamped to 1..200.
func (r *QuotaRepo) RecentUsage(ctx context.Context, limit int) ([]UsageEntry, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT l.id, l.user_id, u.email, l.kind, l.tokens, l.created_at
		 FROM usage_log l JOIN users u ON u.id = l.user_id
		 ORDER BY l.id DESC LIMIT ?`,
		limit)
	if err != nil {
		return nil, fmt.Errorf("quota recent usage: %w", err)
	}
	defer rows.Close()
	var out []UsageEntry
	for rows.Next() {
		var e UsageEntry
		var created string
		if err := rows.Scan(&e.ID, &e.UserID, &e.Email, &e.Kind, &e.Tokens, &created); err != nil {
			return nil, fmt.Errorf("quota recent usage scan: %w", err)
		}
		e.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, e)
	}
	return out, rows.Err()
}
