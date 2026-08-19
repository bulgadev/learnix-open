-- 0001_init.sql: initial schema for users, sessions, user_config, studies, quizzes

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    email         TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT    PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TEXT    NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT
);

-- user_config holds only the user's preferred AI defaults, used to prefill the
-- new-study form. Study state lives in the studies table.
CREATE TABLE IF NOT EXISTS user_config (
    user_id    INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    base_url   TEXT,
    api_key    TEXT,
    model      TEXT,
    updated_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

-- studies: one row per independent study workspace.
CREATE TABLE IF NOT EXISTS studies (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    topic        TEXT    NOT NULL,
    base_url     TEXT,
    api_key      TEXT,
    model        TEXT,
    phase        TEXT    NOT NULL DEFAULT 'study',
    feedback     TEXT,
    reviewing    INTEGER NOT NULL DEFAULT 0,
    history_json TEXT,
    chat_json    TEXT,
    created_at   TEXT    NOT NULL DEFAULT (datetime('now')),
    updated_at   TEXT    NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_studies_user_created ON studies(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS quizzes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    study_id      INTEGER REFERENCES studies(id) ON DELETE CASCADE,
    topic         TEXT    NOT NULL,
    phase         TEXT    NOT NULL,
    current       INTEGER NOT NULL DEFAULT 0,
    questions_json TEXT   NOT NULL,
    answers_json   TEXT   NOT NULL,
    score         INTEGER NOT NULL DEFAULT 0,
    total         INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT    NOT NULL DEFAULT (datetime('now')),
    finished_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_quizzes_user_created ON quizzes(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_quizzes_study ON quizzes(study_id);
