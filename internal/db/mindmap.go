package db

import (
	"context"
	"database/sql"
	"fmt"

	"learnix/internal/mindmap"
)

// MindMapRepo persists one validated graph per study.
type MindMapRepo struct{ db *sql.DB }

func NewMindMapRepo(db *sql.DB) *MindMapRepo { return &MindMapRepo{db: db} }

var _ mindmap.Repository = (*MindMapRepo)(nil)

func (r *MindMapRepo) Load(ctx context.Context, studyID int64) (mindmap.Graph, error) {
	if studyID <= 0 {
		return mindmap.Graph{}, mindmap.ErrInvalidStudyID
	}
	var raw string
	err := r.db.QueryRowContext(ctx,
		`SELECT graph_json FROM study_mind_maps WHERE study_id = ?`, studyID).Scan(&raw)
	if err == sql.ErrNoRows {
		return mindmap.Graph{}, mindmap.ErrNotFound
	}
	if err != nil {
		return mindmap.Graph{}, fmt.Errorf("mind map load: %w", err)
	}
	graph, err := mindmap.Decode([]byte(raw))
	if err != nil {
		return mindmap.Graph{}, fmt.Errorf("mind map load: %w", err)
	}
	return graph, nil
}

func (r *MindMapRepo) Save(ctx context.Context, studyID int64, graph mindmap.Graph) error {
	if studyID <= 0 {
		return mindmap.ErrInvalidStudyID
	}
	raw, err := mindmap.Encode(graph)
	if err != nil {
		return fmt.Errorf("mind map save: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`INSERT INTO study_mind_maps(study_id, graph_json)
		 VALUES(?, ?)
		 ON CONFLICT(study_id) DO UPDATE SET graph_json=excluded.graph_json, updated_at=datetime('now')`,
		studyID, string(raw))
	if err != nil {
		return fmt.Errorf("mind map save: %w", err)
	}
	return nil
}
