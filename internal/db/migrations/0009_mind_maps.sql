CREATE TABLE IF NOT EXISTS study_mind_maps (
    study_id INTEGER PRIMARY KEY REFERENCES studies(id) ON DELETE CASCADE,
    graph_json TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);
