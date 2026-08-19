package handlers

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/session"
)

// CreateStudy saves a new study and redirects to its dedicated page. Users
// only supply a topic: every study runs on the server preset endpoint/key,
// so there is no user-supplied endpoint to validate (SSRF surface gone).
func (h *Handler) CreateStudy(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	topic := strings.TrimSpace(r.FormValue("topic"))
	if topic == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	st := &db.Study{
		UserID: u.ID,
		Topic:  topic,
		Phase:  "study",
	}
	if err := h.studies.Create(r.Context(), st); err != nil {
		http.Error(w, "erro ao criar estudo", http.StatusInternalServerError)
		return
	}
	h.recordTelemetry(r.Context(), db.TelemetryEvent{
		UserID: u.ID, StudyID: st.ID, Type: telemetryStudyCreated,
		Metadata: map[string]any{"topic_length": len([]rune(topic))},
		Delta:    db.TelemetryDelta{StudiesCreated: 1},
	})

	http.Redirect(w, r, fmt.Sprintf("/study/%d", st.ID), http.StatusFound)
}

// StudyCreatePage renders the dedicated study creation flow. Keeping this
// separate from the hub makes the choice between studying and testing clear.
func (h *Handler) StudyCreatePage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	render(w, r, components.StudyCreate(r.URL.Query().Get("topic"), u, h.quotaFor(r.Context(), u), h.isAdmin(u)))
}

// StudyPage renders a study workspace, choosing the view from its phase.
func (h *Handler) StudyPage(w http.ResponseWriter, r *http.Request) {
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
	u := auth.UserFromContext(r.Context())
	q, admin := h.quotaFor(r.Context(), u), h.isAdmin(u)

	// An exam that ran out of time while the learner was away is finalized
	// on load; the switch below then falls through to the results view.
	if s.Phase == "quiz" {
		expired, err := h.finalizeExpiredExam(r, s, s.TestID)
		if err != nil {
			http.Error(w, "erro ao finalizar prova", http.StatusInternalServerError)
			return
		}
		if expired {
			h.persist(r, s)
		}
	}

	switch s.Phase {
	case "quiz":
		if len(s.Questions) > 0 && s.Current < len(s.Questions) {
			render(w, r, components.Quiz(s, s.Current, s.Questions[s.Current], len(s.Questions), u, q, admin, fmt.Sprintf("/study/%d", s.StudyID), false))
			return
		}
		s.Phase = "study"
		h.persist(r, s)
		http.Redirect(w, r, fmt.Sprintf("/study/%d", s.StudyID), http.StatusSeeOther)
	case "results":
		if len(s.Questions) > 0 {
			score := s.Score()
			total := len(s.Questions)
			render(w, r, components.Result(s, score, total, score == total, u, q, admin, fmt.Sprintf("/study/%d", s.StudyID), false))
			return
		}
		s.Phase = "study"
		h.persist(r, s)
		http.Redirect(w, r, fmt.Sprintf("/study/%d", s.StudyID), http.StatusSeeOther)
	default:
		h.renderWorkspace(w, r, s, u, q, admin)
	}
}

// StudyWorkspace renders the study surface without changing the durable
// phase. This lets a learner leave an in-progress quiz, ask the tutor a
// follow-up question, and return to the same quiz later.
func (h *Handler) StudyWorkspace(w http.ResponseWriter, r *http.Request) {
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
	u := auth.UserFromContext(r.Context())
	h.renderWorkspace(w, r, s, u, h.quotaFor(r.Context(), u), h.isAdmin(u))
}

func (h *Handler) renderWorkspace(w http.ResponseWriter, r *http.Request, s *session.Session, u *db.User, q *db.Quota, admin bool) {
	files, _ := h.files.ListByStudy(r.Context(), s.StudyID)
	chats := h.ensureChats(r, s.StudyID)
	if len(chats) == 0 {
		http.Error(w, "erro ao carregar conversas", http.StatusInternalServerError)
		return
	}
	active := &chats[0]
	if v := r.URL.Query().Get("chat"); v != "" {
		if cid, err := strconv.ParseInt(v, 10, 64); err == nil {
			for i := range chats {
				if chats[i].ID == cid {
					active = &chats[i]
					break
				}
			}
		}
	}
	state := components.MobileStudyState{
		Pane:          mobilePane(r.URL.Query().Get("pane")),
		Prompt:        mobilePrompt(r.URL.Query().Get("prompt")),
		HasActiveQuiz: s.Phase == "quiz" && len(s.Questions) > 0,
		QuizCurrent:   s.Current,
		QuizTotal:     len(s.Questions),
	}
	msgs, _ := h.chats.Messages(r.Context(), active.ID)
	render(w, r, components.Workspace(s, files, chats, active, msgs, u, q, admin, state))
}

func mobilePane(value string) string {
	switch value {
	case "chat", "files", "editor":
		return value
	default:
		return ""
	}
}

func mobilePrompt(value string) string {
	value = strings.TrimSpace(value)
	const maxPromptRunes = 2000
	runes := []rune(value)
	if len(runes) > maxPromptRunes {
		return string(runes[:maxPromptRunes])
	}
	return value
}

// StudiesList shows all of the user's studies ("My Studies").
func (h *Handler) StudiesList(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	items, err := h.studies.ListByUser(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "erro ao carregar estudos", http.StatusInternalServerError)
		return
	}
	tests, err := h.tests.ListByUser(r.Context(), u.ID)
	if err != nil {
		http.Error(w, "erro ao carregar provas", http.StatusInternalServerError)
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab != "tests" {
		tab = "studies"
	}
	render(w, r, components.StudyTestHub(items, tests, tab, u, h.quotaFor(r.Context(), u), h.isAdmin(u)))
}
