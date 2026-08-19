package handlers

import (
	"net/http"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/learningapps"
)

// LearningAppsPage renders the allowlisted declarative apps for one owned
// study. A future AI publisher should call Registry.ValidateAll before adding
// any generated specs to this page.
func (h *Handler) LearningAppsPage(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	apps := studyLearningApps(s.Config.Topic)
	u := auth.UserFromContext(r.Context())
	render(w, r, components.LearningAppsPage(s.StudyID, s.Config.Topic, apps, u, h.quotaFor(r.Context(), u), h.isAdmin(u)))
}

func studyLearningApps(topic string) []learningapps.App {
	apps := []learningapps.App{
		{
			ID:     "conceito-chave",
			Title:  "Teste rápido",
			Prompt: "Pratique uma ideia central do estudo.",
			Lesson: learningapps.LessonMetadata{Subject: topic, Objective: "Reconhecer o conceito principal", Level: "revisão"},
			Modules: []learningapps.Module{{ID: "pergunta", Type: learningapps.ChoiceType, Params: map[string]interface{}{
				"prompt":  "Qual é a melhor próxima ação para aprender este conteúdo?",
				"options": []interface{}{"Explicar com suas palavras", "Ler sem testar a compreensão"},
				"answer":  0,
			}}},
			InitialState: map[string]interface{}{"attempts": 0},
			Limits:       &learningapps.InteractionLimits{MaxInteractions: 20, MaxDurationMS: 10 * 60 * 1000},
		},
		{
			ID:     "lembrar-e-conectar",
			Title:  "Lembrar e conectar",
			Prompt: "Revise e conecte os conceitos principais.",
			Lesson: learningapps.LessonMetadata{Subject: topic, Objective: "Recuperar conceitos e relações", Level: "revisão"},
			Modules: []learningapps.Module{{ID: "cartoes", Type: learningapps.FlashcardsType, Params: map[string]interface{}{
				"fronts": []interface{}{"Conceito", "Relação"},
				"backs":  []interface{}{"Defina com suas palavras", "Explique como se conectam"},
			}}},
			InitialState: map[string]interface{}{"cartoes.index": 0, "cartoes.revealed": false},
			Limits:       &learningapps.InteractionLimits{MaxInteractions: 40, MaxDurationMS: 10 * 60 * 1000},
		},
	}
	valid := make([]learningapps.App, 0, len(apps))
	for _, app := range apps {
		if app.Validate() == nil {
			valid = append(valid, app)
		}
	}
	return valid
}
