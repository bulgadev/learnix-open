package handlers

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/elements"
)

// messageIDParam parses the {mid} URL param.
func messageIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "mid"), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// noteTitle derives a note name from assistant content: the first Markdown
// heading (# / ##) with the hashes stripped wins; otherwise the first
// non-empty line, trimmed and cut to 40 runes; fallback "Resposta salva".
func noteTitle(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			if title := strings.TrimSpace(strings.TrimLeft(line, "#")); title != "" {
				return title
			}
		}
	}
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if runes := []rune(line); len(runes) > 40 {
			line = string(runes[:40])
		}
		return line
	}
	return "Resposta salva"
}

// SaveToNote persists an assistant chat message as a note file authored by
// "ai" and answers with the done-state button fragment plus an HX-Trigger
// header so the sidebar file tree refreshes.
func (h *Handler) SaveToNote(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cid, ok := chatIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	mid, ok := messageIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if h.loadChat(r, s.StudyID, cid) == nil {
		http.NotFound(w, r)
		return
	}
	msgs, err := h.chats.Messages(r.Context(), cid)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var msg *db.ChatMessage
	for i := range msgs {
		if msgs[i].ID == mid {
			msg = &msgs[i]
			break
		}
	}
	if msg == nil || msg.Role != "assistant" {
		http.NotFound(w, r)
		return
	}
	f := &db.File{
		StudyID: s.StudyID,
		Name:    noteTitle(msg.Content),
		Kind:    "note",
		Content: msg.Content,
	}
	if decoded, derr := elements.Decode(msg.ElementsJSON); derr == nil && len(decoded) > 0 {
		f.ElementsJSON, _ = elements.Encode(decoded)
	}
	if err := h.files.CreateAuthored(r.Context(), f, "ai", "salvo do chat"); err != nil {
		http.Error(w, "erro ao salvar nota", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", "refreshFiles")
	render(w, r, components.SavedButton())
}
