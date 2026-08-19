package quizjobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Snapshot is the client-visible state of one background job.
type Snapshot struct {
	ID          string     `json:"job_id"`
	UserID      int64      `json:"-"`
	StudyID     int64      `json:"-"`
	Status      string     `json:"status"`
	Stage       string     `json:"stage,omitempty"`
	Level       string     `json:"level,omitempty"`
	Message     string     `json:"message,omitempty"`
	Error       string     `json:"error,omitempty"`
	Redirect    string     `json:"redirect,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	HeartbeatAt time.Time  `json:"heartbeat_at"`
	Current     int        `json:"current"`
	Total       int        `json:"total"`
	Sources     int        `json:"sources"`
	Searches    int        `json:"searches"`
	Pages       int        `json:"pages"`
	ModelCalls  int        `json:"model_calls"`
	Tokens      int64      `json:"tokens"`
	Attempt     int        `json:"attempt"`
	MaxAttempts int        `json:"max_attempts"`
	Activities  []Activity `json:"activities,omitempty"`
}

// Activity is a durable-in-memory explanation of the latest job work. It is
// intentionally bounded by Manager.activityLimit so polling stays cheap.
type Activity struct {
	ID         int       `json:"id"`
	At         time.Time `json:"at"`
	Stage      string    `json:"stage,omitempty"`
	Level      string    `json:"level,omitempty"`
	Message    string    `json:"message"`
	Current    int       `json:"current"`
	Total      int       `json:"total"`
	Sources    int       `json:"sources"`
	Searches   int       `json:"searches"`
	Pages      int       `json:"pages"`
	ModelCalls int       `json:"model_calls"`
	Tokens     int64     `json:"tokens"`
	Attempt    int       `json:"attempt"`
}

// Update is emitted by the worker whenever the job has meaningful progress.
// Metrics are copied as a complete snapshot when Metrics is true.
type Update struct {
	Stage       string
	Level       string
	Message     string
	Current     int
	Total       int
	Sources     int
	Searches    int
	Pages       int
	ModelCalls  int
	Tokens      int64
	Attempt     int
	MaxAttempts int
	Metrics     bool
}

// Work performs a job and reports progress. The context is independent from
// the request that created the job, so closing the browser cannot cancel it.
type Work func(context.Context, func(Update)) (string, error)

type Manager struct {
	mu            sync.RWMutex
	jobs          map[string]*Snapshot
	retention     time.Duration
	activityLimit int
}

func NewManager(retention time.Duration) *Manager {
	return &Manager{jobs: make(map[string]*Snapshot), retention: retention, activityLimit: 100}
}

// Start registers a job and runs it asynchronously. The returned snapshot is
// safe to send in the HTTP response immediately.
func (m *Manager) Start(userID, studyID int64, work Work) Snapshot {
	m.mu.Lock()
	m.pruneLocked(time.Now())
	now := time.Now()
	s := &Snapshot{
		ID:          newID(),
		UserID:      userID,
		StudyID:     studyID,
		Status:      "queued",
		StartedAt:   now,
		UpdatedAt:   now,
		HeartbeatAt: now,
	}
	m.jobs[s.ID] = s
	initial := *s
	m.mu.Unlock()

	go func(jobID string) {
		m.update(jobID, func(s *Snapshot) { s.Status = "running" })
		done := make(chan struct{})
		go m.heartbeat(jobID, done)
		redirect, err := work(context.Background(), func(update Update) { m.report(jobID, update) })
		close(done)
		m.update(jobID, func(s *Snapshot) {
			if err != nil {
				s.Status = "failed"
				s.Error = err.Error()
				return
			}
			s.Status = "succeeded"
			s.Redirect = redirect
		})
	}(initial.ID)

	return initial
}

func (m *Manager) Get(id string, userID, studyID int64) (Snapshot, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked(time.Now())
	s, ok := m.jobs[id]
	if !ok || s.UserID != userID || s.StudyID != studyID {
		return Snapshot{}, false
	}
	copy := *s
	copy.Activities = append([]Activity(nil), s.Activities...)
	return copy, true
}

func (m *Manager) update(id string, update func(*Snapshot)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.jobs[id]; ok {
		update(s)
		s.UpdatedAt = time.Now()
	}
}

func (m *Manager) report(id string, update Update) {
	m.update(id, func(s *Snapshot) {
		if update.Stage != "" {
			s.Stage = update.Stage
		}
		s.Level = update.Level
		if update.Message != "" {
			s.Message = update.Message
		}
		if update.Metrics {
			s.Current = update.Current
			s.Total = update.Total
			s.Sources = update.Sources
			s.Searches = update.Searches
			s.Pages = update.Pages
			s.ModelCalls = update.ModelCalls
			s.Tokens = update.Tokens
			s.Attempt = update.Attempt
			s.MaxAttempts = update.MaxAttempts
		}
		if update.MaxAttempts > 0 {
			s.MaxAttempts = update.MaxAttempts
		}
		if update.Attempt > 0 {
			s.Attempt = update.Attempt
		}
		if update.Message != "" {
			s.HeartbeatAt = time.Now()
			s.Activities = append(s.Activities, Activity{
				ID: len(s.Activities) + 1, At: time.Now(), Stage: s.Stage,
				Level: update.Level, Message: update.Message,
				Current: s.Current, Total: s.Total, Sources: s.Sources,
				Searches: s.Searches, Pages: s.Pages, ModelCalls: s.ModelCalls,
				Tokens: s.Tokens, Attempt: s.Attempt,
			})
			if len(s.Activities) > m.activityLimit {
				s.Activities = s.Activities[len(s.Activities)-m.activityLimit:]
			}
		}
	})
}

func (m *Manager) heartbeat(id string, done <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.update(id, func(s *Snapshot) { s.HeartbeatAt = time.Now() })
		case <-done:
			return
		}
	}
}

func (m *Manager) pruneLocked(now time.Time) {
	if m.retention <= 0 {
		return
	}
	for id, s := range m.jobs {
		if (s.Status == "succeeded" || s.Status == "failed") && now.Sub(s.UpdatedAt) > m.retention {
			delete(m.jobs, id)
		}
	}
}

func newID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
