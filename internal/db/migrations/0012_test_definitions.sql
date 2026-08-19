-- 0012_test_definitions.sql: persistent test containers for multiple attempts.

CREATE TABLE IF NOT EXISTS test_definitions (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id               INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic                 TEXT    NOT NULL,
    mode                  TEXT    NOT NULL DEFAULT 'practice',
    preset                TEXT    NOT NULL DEFAULT 'moderate',
    observations_json     TEXT    NOT NULL DEFAULT '[]',
    created_at            TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at            TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_test_definitions_user_updated
    ON test_definitions(user_id, updated_at DESC, id DESC);
