package session

import "testing"

func TestSessionScoreCentsIsProportional(t *testing.T) {
	s := &Session{
		Questions: make([]Question, 10),
		Answers:   []int{0, 0, 0, 0, 0, 1, 1, 1, 1, 1},
	}
	for i := range s.Questions {
		s.Questions[i].Correct = 0
	}
	if got := s.ScoreCents(); got != 500 {
		t.Fatalf("score cents = %d, want 500", got)
	}
	s.QuizWeightCents = 50
	if got := s.WeightedScoreCents(); got != 250 {
		t.Fatalf("weighted score cents = %d, want 250", got)
	}
}

func TestSessionScoreCentsRoundsToTwoDecimals(t *testing.T) {
	s := &Session{
		Questions: make([]Question, 7),
		Answers:   []int{0, 0, 0, 1, 1, 1, 1},
	}
	for i := range s.Questions {
		s.Questions[i].Correct = 0
	}
	if got := s.ScoreCents(); got != 429 {
		t.Fatalf("score cents = %d, want 429", got)
	}
}

func TestSessionScoreCentsEmptyQuiz(t *testing.T) {
	if got := (&Session{}).ScoreCents(); got != 0 {
		t.Fatalf("empty score cents = %d, want 0", got)
	}
}
