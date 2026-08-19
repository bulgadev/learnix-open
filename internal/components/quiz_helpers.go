package components

import (
	"net/url"
	"strconv"

	"learnix/internal/session"
)

func quizTokenLabel(usage session.TokenUsage) string {
	if total := usage.Total(); total > 0 {
		return strconv.Itoa(total)
	}
	return "—"
}

func confidenceLabel(value int) string {
	switch value {
	case session.ConfidenceGuess:
		return "Chutei"
	case session.ConfidenceUnsure:
		return "Incerto"
	case session.ConfidenceAlmostCertain:
		return "Quase certeza"
	case session.ConfidenceCertain:
		return "Tinha certeza"
	default:
		return "Não informado"
	}
}

func knowledgeSignal(s *session.Session, i int) string {
	if i < 0 || i >= len(s.Questions) || i >= len(s.Answers) || s.Answers[i] < 0 {
		return "Sem resposta"
	}
	correct := s.Answers[i] == s.Questions[i].Correct
	confidence := session.ConfidenceUnknown
	if i < len(s.Confidence) {
		confidence = s.Confidence[i]
	}
	switch {
	case correct && confidence == session.ConfidenceGuess:
		return "Acerto com baixa confiança — possível chute; revisar"
	case correct && confidence == session.ConfidenceUnsure:
		return "Acerto com incerteza — revisar para consolidar"
	case correct:
		return "Acerto com alta confiança — evidência forte, não prova absoluta"
	case !correct && confidence == session.ConfidenceGuess:
		return "Erro com baixa confiança — incerteza/chute"
	case !correct && confidence == session.ConfidenceUnsure:
		return "Erro com incerteza — revisar o conceito"
	default:
		return "Erro com alta confiança — conceito ou atenção; revisar"
	}
}

func assessmentLabel(value string) string {
	switch value {
	case session.AssessmentGuess:
		return "Foi um chute"
	case session.AssessmentAttention:
		return "Foi desatenção"
	case session.AssessmentDidNotKnow:
		return "Eu não sabia"
	case session.AssessmentOther:
		return "Outro motivo"
	default:
		return "Ainda não classifiquei"
	}
}

func assessmentSelected(current, option string) bool { return current == option }

func safeReferenceURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}
