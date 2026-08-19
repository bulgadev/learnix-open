package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/session"
)

// messageOwned reports whether the message exists in a chat that belongs to
// the study.
func (h *Handler) messageOwned(ctx context.Context, studyID, messageID int64) bool {
	chats, _ := h.chats.ListByStudy(ctx, studyID)
	for _, c := range chats {
		msgs, _ := h.chats.Messages(ctx, c.ID)
		for _, m := range msgs {
			if m.ID == messageID {
				return true
			}
		}
	}
	return false
}

// CreateHighlight saves a text selection from a note or chat message.
func (h *Handler) CreateHighlight(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		SourceKind string `json:"source_kind"`
		SourceID   int64  `json:"source_id"`
		Excerpt    string `json:"excerpt"`
		Note       string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "requisição inválida", http.StatusBadRequest)
		return
	}
	excerpt := strings.TrimSpace(body.Excerpt)
	if excerpt == "" || body.SourceID == 0 {
		http.Error(w, "destaque incompleto", http.StatusBadRequest)
		return
	}
	if r := []rune(excerpt); len(r) > 2000 {
		excerpt = string(r[:2000])
	}
	switch body.SourceKind {
	case "note":
		f, err := h.files.Get(r.Context(), body.SourceID)
		if err != nil || f == nil || f.StudyID != s.StudyID {
			http.NotFound(w, r)
			return
		}
	case "message":
		if !h.messageOwned(r.Context(), s.StudyID, body.SourceID) {
			http.NotFound(w, r)
			return
		}
	default:
		http.Error(w, "origem inválida", http.StatusBadRequest)
		return
	}
	hl := &db.Highlight{StudyID: s.StudyID, SourceKind: body.SourceKind, SourceID: body.SourceID, Excerpt: excerpt, Note: strings.TrimSpace(body.Note)}
	if err := h.highlights.Create(r.Context(), hl); err != nil {
		http.Error(w, "erro ao salvar destaque", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": hl.ID})
}

// SavedPanel renders the saved highlights + bookmarked answers drawer.
func (h *Handler) SavedPanel(w http.ResponseWriter, r *http.Request) {
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
	h.renderSavedPanel(w, r, s)
}

func (h *Handler) renderSavedPanel(w http.ResponseWriter, r *http.Request, s *session.Session) {
	hls, _ := h.highlights.ListByStudy(r.Context(), s.StudyID)
	var saved []db.ChatMessage
	chatOf := map[int64]string{}
	chats, _ := h.chats.ListByStudy(r.Context(), s.StudyID)
	for _, c := range chats {
		chatOf[c.ID] = c.Title
		msgs, _ := h.chats.Messages(r.Context(), c.ID)
		for _, m := range msgs {
			if m.Saved {
				saved = append(saved, m)
			}
		}
	}
	render(w, r, components.SavedPanel(s.StudyID, hls, saved, chatOf))
}

// DeleteHighlight removes one highlight owned by the study.
func (h *Handler) DeleteHighlight(w http.ResponseWriter, r *http.Request) {
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
	hid, err := strconv.ParseInt(chi.URLParam(r, "hid"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	_ = h.highlights.Delete(r.Context(), hid, s.StudyID)
	h.renderSavedPanel(w, r, s)
}

// SaveMessage toggles the bookmark on a chat message.
func (h *Handler) SaveMessage(w http.ResponseWriter, r *http.Request) {
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
	var target *db.ChatMessage
	msgs, _ := h.chats.Messages(r.Context(), cid)
	for i := range msgs {
		if msgs[i].ID == mid {
			target = &msgs[i]
			break
		}
	}
	if target == nil {
		http.NotFound(w, r)
		return
	}
	if err := h.chats.SetSaved(r.Context(), mid, cid, !target.Saved); err != nil {
		http.Error(w, "erro ao salvar", http.StatusInternalServerError)
		return
	}
	target.Saved = !target.Saved
	render(w, r, components.BookmarkButton(s.StudyID, *target))
}
