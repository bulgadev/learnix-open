-- 0010_chat_turns.sql: durable lifecycle and idempotency for chat requests.

CREATE TABLE IF NOT EXISTS chat_turns (
    id TEXT PRIMARY KEY,
    chat_id INTEGER NOT NULL REFERENCES chats(id) ON DELETE CASCADE,
    study_id INTEGER NOT NULL REFERENCES studies(id) ON DELETE CASCADE,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    client_key TEXT NOT NULL,
    user_message_id INTEGER NOT NULL UNIQUE REFERENCES chat_messages(id) ON DELETE CASCADE,
    assistant_message_id INTEGER REFERENCES chat_messages(id) ON DELETE SET NULL,
    status TEXT NOT NULL CHECK(status IN ('queued','running','succeeded','failed')),
    attempt INTEGER NOT NULL DEFAULT 1,
    web INTEGER NOT NULL DEFAULT 1,
    error_code TEXT,
    error_message TEXT,
    provider_status INTEGER,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    started_at TEXT,
    finished_at TEXT,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_chat_turns_client_key
    ON chat_turns(user_id, chat_id, client_key);
CREATE INDEX IF NOT EXISTS idx_chat_turns_chat
    ON chat_turns(chat_id, created_at, id);
CREATE INDEX IF NOT EXISTS idx_chat_turns_status
    ON chat_turns(status, updated_at);
