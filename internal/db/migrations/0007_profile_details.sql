-- 0007_profile_details.sql: editable public profile details.

CREATE TABLE IF NOT EXISTS profile_details (
    user_id          INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    display_name     TEXT NOT NULL DEFAULT '',
    bio              TEXT NOT NULL DEFAULT '',
    avatar_mime      TEXT,
    avatar_data      BLOB,
    visibility_json  TEXT NOT NULL DEFAULT '{}',
    updated_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
