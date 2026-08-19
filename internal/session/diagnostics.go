package session

// Confidence values sent by the quiz UI. Values 0-3 are already persisted by
// older clients, so their meanings must remain stable. Zero is kept for old
// clients and incomplete attempts; it is never treated as evidence of mastery.
const (
	ConfidenceUnknown       = 0
	ConfidenceGuess         = 1
	ConfidenceUnsure        = 2
	ConfidenceCertain       = 3
	ConfidenceAlmostCertain = 4
)

func ValidConfidence(value int) bool {
	switch value {
	case ConfidenceUnknown, ConfidenceGuess, ConfidenceUnsure, ConfidenceCertain, ConfidenceAlmostCertain:
		return true
	default:
		return false
	}
}

const (
	AssessmentUnknown    = ""
	AssessmentGuess      = "guess"
	AssessmentAttention  = "attention"
	AssessmentDidNotKnow = "did_not_know"
	AssessmentOther      = "other"
)

func ValidAssessment(value string) bool {
	switch value {
	case AssessmentUnknown, AssessmentGuess, AssessmentAttention, AssessmentDidNotKnow, AssessmentOther:
		return true
	default:
		return false
	}
}
