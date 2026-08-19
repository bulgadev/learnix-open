package mindmap

import (
	"context"
	"sync"
)

// Repository persists one complete graph per study. Implementations may use
// SQLite, another database, or a service; the domain package does not depend
// on any of them.
type Repository interface {
	Load(ctx context.Context, studyID int64) (Graph, error)
	Save(ctx context.Context, studyID int64, graph Graph) error
}

// MemoryRepository is a concurrency-safe repository for tests and local
// composition. It returns defensive copies so callers cannot mutate stored
// state without Save.
type MemoryRepository struct {
	mu     sync.RWMutex
	graphs map[int64]Graph
}

var _ Repository = (*MemoryRepository)(nil)

// NewMemoryRepository creates an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{graphs: make(map[int64]Graph)}
}

// Load returns the graph stored for studyID, or ErrNotFound when no graph was
// saved for that study.
func (r *MemoryRepository) Load(ctx context.Context, studyID int64) (Graph, error) {
	if err := validateRepositoryRequest(ctx, studyID); err != nil {
		return Graph{}, err
	}
	r.mu.RLock()
	graph, ok := r.graphs[studyID]
	r.mu.RUnlock()
	if !ok {
		return Graph{}, ErrNotFound
	}
	return graph.clone(), nil
}

// Save validates and stores a defensive copy of graph for studyID.
func (r *MemoryRepository) Save(ctx context.Context, studyID int64, graph Graph) error {
	if err := validateRepositoryRequest(ctx, studyID); err != nil {
		return err
	}
	if err := graph.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	r.graphs[studyID] = graph.clone()
	r.mu.Unlock()
	return nil
}

func validateRepositoryRequest(ctx context.Context, studyID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if studyID <= 0 {
		return ErrInvalidStudyID
	}
	return nil
}
