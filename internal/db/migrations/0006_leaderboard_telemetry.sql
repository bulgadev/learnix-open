-- 0006_leaderboard_telemetry.sql: public identities, product telemetry and
-- immutable ranked quiz snapshots.

CREATE TABLE IF NOT EXISTS user_profiles (
    user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    slug       TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS telemetry_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event_type    TEXT NOT NULL,
    study_id      INTEGER,
    quiz_id       INTEGER,
    value_int     INTEGER NOT NULL DEFAULT 0,
    value_cents   INTEGER NOT NULL DEFAULT 0,
    metadata_json TEXT,
    created_at    TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_telemetry_events_user_time
    ON telemetry_events(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_telemetry_events_type_time
    ON telemetry_events(event_type, created_at DESC);

CREATE TABLE IF NOT EXISTS user_stats (
    user_id                    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    studies_created           INTEGER NOT NULL DEFAULT 0,
    study_resets              INTEGER NOT NULL DEFAULT 0,
    quizzes_started           INTEGER NOT NULL DEFAULT 0,
    quizzes_completed         INTEGER NOT NULL DEFAULT 0,
    ranked_quizzes_completed  INTEGER NOT NULL DEFAULT 0,
    quiz_generation_failures  INTEGER NOT NULL DEFAULT 0,
    questions_answered        INTEGER NOT NULL DEFAULT 0,
    correct_answers           INTEGER NOT NULL DEFAULT 0,
    diagnostics_submitted     INTEGER NOT NULL DEFAULT 0,
    chat_turns                INTEGER NOT NULL DEFAULT 0,
    ai_calls                  INTEGER NOT NULL DEFAULT 0,
    tokens_used               INTEGER NOT NULL DEFAULT 0,
    last_active_at            TEXT
);

CREATE TABLE IF NOT EXISTS ranked_results (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    quiz_id              INTEGER NOT NULL UNIQUE,
    user_id              INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic                TEXT NOT NULL,
    preset               TEXT NOT NULL,
    total                INTEGER NOT NULL,
    correct              INTEGER NOT NULL,
    score_cents          INTEGER NOT NULL,
    weight_cents         INTEGER NOT NULL,
    weighted_score_cents INTEGER NOT NULL,
    finished_at          TEXT NOT NULL,
    created_at           TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_ranked_results_user_time
    ON ranked_results(user_id, finished_at DESC);
CREATE INDEX IF NOT EXISTS idx_ranked_results_finished
    ON ranked_results(finished_at DESC);

-- Existing accounts receive an opaque identity and an initial aggregate row.
-- INSERT OR IGNORE makes this safe to run on every startup.
