package components

import (
	"testing"

	"learnix/internal/session"
)

func TestConfidenceLabel(t *testing.T) {
	tests := []struct {
		name  string
		value int
		want  string
	}{
		{name: "guess", value: session.ConfidenceGuess, want: "Chutei"},
		{name: "unsure", value: session.ConfidenceUnsure, want: "Incerto"},
		{name: "almost certain", value: session.ConfidenceAlmostCertain, want: "Quase certeza"},
		{name: "legacy certain", value: session.ConfidenceCertain, want: "Tinha certeza"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := confidenceLabel(tt.value); got != tt.want {
				t.Fatalf("confidenceLabel(%d) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
