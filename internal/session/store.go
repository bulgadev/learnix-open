package session

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"learnix/internal/elements"
)

type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Topic   string
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Question struct {
	Text        string             `json:"text"`
	Context     string             `json:"context"`
	Elements    []elements.Element `json:"elements,omitempty"`
	Skill       string             `json:"skill,omitempty"`
	Difficulty  string             `json:"difficulty,omitempty"`
	Options     []string           `json:"options"`
	Correct     int                `json:"correct"`
	Explanation string             `json:"explanation"`
}

// Reference is a source used by the quiz research stage. Only user-visible
// metadata is stored; credentials and hidden tool arguments never enter it.
type Reference struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
}

// QuizTrace records the material and criteria used to build and assess a quiz.
// It is intentionally explanatory rather than a copy of private model prompts.
type QuizTrace struct {
	Topic              string      `json:"topic"`
	Feedback           string      `json:"feedback,omitempty"`
	Count              int         `json:"count"`
	Web                bool        `json:"web"`
	Model              string      `json:"model,omitempty"`
	ResearchBrief      string      `json:"research_brief,omitempty"`
	Sources            []Reference `json:"sources,omitempty"`
	AuthorCriteria     []string    `json:"author_criteria,omitempty"`
	ReviewCriteria     []string    `json:"review_criteria,omitempty"`
	EvaluationCriteria []string    `json:"evaluation_criteria,omitempty"`
	TokenUsage         TokenUsage  `json:"token_usage,omitempty"`
}

type ChatMsg struct {
	Role    string
	Content string
}

type Session struct {
	ID        string
	StudyID   int64
	Config    Config
	History   []Message
	Chat      []ChatMsg
	Questions []Question
	Answers   []int
	// Confidence is 1=chutei, 2=incerto, 3=tinha certeza and
	// 4=quase certeza; 0 means not supplied by an older client. It is
	// deliberately separate from the selected answer.
	Confidence []int
	// Assessments are optional post-result self-reports: guess, attention,
	// did_not_know or other. They keep the evaluator from pretending an MCQ can
	// distinguish carelessness from a conceptual gap on its own.
	Assessments []string
	Trace       QuizTrace
	TraceJSON   string
	Current     int
	Phase       string
	Feedback    string
	Reviewing   bool
	// ActiveQuizID is the DB row id of the in-progress quiz (0 = none).
	ActiveQuizID int64
	// TestID identifies the persistent test container for standalone attempts.
	TestID int64
	// AdaptiveFromID links an adaptive attempt to the previous standalone quiz.
	AdaptiveFromID int64
	// Exam marks exam-simulation attempts: free navigation, no feedback until
	// submission, and a hard deadline. ExamDeadline is the moment the attempt
	// auto-submits (server-authoritative); Flags tracks per-question
	// flag-for-review marks.
	Exam         bool
	ExamDeadline *time.Time
	Flags        []bool
	// Tutor holds per-question tutor chat threads for standalone attempts,
	// keyed by question index. Each thread is a chronological user/assistant
	// message list persisted alongside the quiz.
	Tutor map[int][]Message
	// QuizMode is practice or ranked. Older persisted quizzes default to
	// practice when reconstructed by the handler.
	QuizMode             string
	QuizPreset           string
	QuizWeightCents      int64
	NormalizedScoreCents int64
	RankedScoreCents     int64
	FinishedAt           *time.Time
}

func (s *Session) Score() int {
	n := 0
	for i, a := range s.Answers {
		if i < len(s.Questions) && a == s.Questions[i].Correct {
			n++
		}
	}
	return n
}

// ScoreCents returns the proportional 0..10 grade rounded to two decimals.
func (s *Session) ScoreCents() int64 {
	total := len(s.Questions)
	if total == 0 {
		return 0
	}
	return (int64(s.Score())*1000 + int64(total)/2) / int64(total)
}

// WeightedScoreCents returns the ranked points using a weight expressed in
// hundredths (50 = 0.5, 100 = 1, 200 = 2).
func (s *Session) WeightedScoreCents() int64 {
	return (s.ScoreCents()*s.QuizWeightCents + 50) / 100
}

func (s *Session) WrongTopics() []int {
	var idx []int
	for i, a := range s.Answers {
		if i < len(s.Questions) && a != s.Questions[i].Correct {
			idx = append(idx, i)
		}
	}
	return idx
}

type Store struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewStore() *Store {
	return &Store{sessions: make(map[string]*Session)}
}

func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Store) Get(id string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}

func (s *Store) Create() *Session {
	sess := &Session{ID: NewID(), Phase: "setup"}
	s.mu.Lock()
	s.sessions[sess.ID] = sess
	s.mu.Unlock()
	return sess
}
