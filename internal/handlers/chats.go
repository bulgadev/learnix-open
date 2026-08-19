package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"learnix/internal/ai"
	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/elements"
	"learnix/internal/session"
	"learnix/internal/websearch"
)

// chatIDParam parses the {cid} URL param.
func chatIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "cid"), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// loadChat fetches a chat and verifies it belongs to the (already
// ownership-checked) study. Returns nil on miss (callers respond 404).
func (h *Handler) loadChat(r *http.Request, studyID, cid int64) *db.Chat {
	c, err := h.chats.Get(r.Context(), cid)
	if err != nil || c == nil || c.StudyID != studyID {
		return nil
	}
	return c
}

// ensureChats returns the study's chats, auto-creating a first one so the
// pane is never empty.
func (h *Handler) ensureChats(r *http.Request, studyID int64) []db.Chat {
	chats, _ := h.chats.ListByStudy(r.Context(), studyID)
	if len(chats) > 0 {
		return chats
	}
	c := &db.Chat{StudyID: studyID, Title: "Nova conversa"}
	if err := h.chats.Create(r.Context(), c); err != nil {
		return nil
	}
	return []db.Chat{*c}
}

// renderChatPane renders the ChatPane fragment with activeID selected
// (0 or unknown falls back to the most recent chat).
func (h *Handler) renderChatPane(w http.ResponseWriter, r *http.Request, s *session.Session, activeID int64) {
	chats := h.ensureChats(r, s.StudyID)
	if len(chats) == 0 {
		http.Error(w, "erro ao carregar conversas", http.StatusInternalServerError)
		return
	}
	active := &chats[0]
	for i := range chats {
		if chats[i].ID == activeID {
			active = &chats[i]
			break
		}
	}
	msgs, _ := h.chats.Messages(r.Context(), active.ID)
	render(w, r, components.ChatPane(chats, active, msgs, s, components.MobileStudyState{}))
}

// ChatList renders the chat pane fragment, optionally switching to ?chat={cid}.
func (h *Handler) ChatList(w http.ResponseWriter, r *http.Request) {
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
	var activeID int64
	if v := r.URL.Query().Get("chat"); v != "" {
		activeID, _ = strconv.ParseInt(v, 10, 64)
	}
	h.renderChatPane(w, r, s, activeID)
}

// ChatCreate creates a new chat and returns the pane with it active.
func (h *Handler) ChatCreate(w http.ResponseWriter, r *http.Request) {
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
	c := &db.Chat{StudyID: s.StudyID, Title: "Nova conversa"}
	if err := h.chats.Create(r.Context(), c); err != nil {
		http.Error(w, "erro ao criar conversa", http.StatusInternalServerError)
		return
	}
	_ = h.chats.Touch(r.Context(), c.ID)
	h.renderChatPane(w, r, s, c.ID)
}

// ChatRename renames the chat from the form's "title" field.
func (h *Handler) ChatRename(w http.ResponseWriter, r *http.Request) {
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
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if h.loadChat(r, s.StudyID, cid) == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if title := strings.TrimSpace(r.FormValue("title")); title != "" {
		_ = h.chats.Rename(r.Context(), cid, s.StudyID, title)
	}
	h.renderChatPane(w, r, s, cid)
}

// ChatDelete removes the chat; the pane falls back to the most recent
// remaining chat (auto-creating one when none are left).
func (h *Handler) ChatDelete(w http.ResponseWriter, r *http.Request) {
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
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if h.loadChat(r, s.StudyID, cid) == nil {
		http.NotFound(w, r)
		return
	}
	_ = h.chats.Delete(r.Context(), cid, s.StudyID)
	h.renderChatPane(w, r, s, 0)
}

// ChatBranch creates a new chat copying the conversation path from the root
// up to the given message, and returns the pane with the new chat active.
func (h *Handler) ChatBranch(w http.ResponseWriter, r *http.Request) {
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
	nc, err := h.chats.BranchFrom(r.Context(), cid, mid, s.StudyID)
	if err != nil || nc == nil {
		http.NotFound(w, r)
		return
	}
	h.renderChatPane(w, r, s, nc.ID)
}

// ChatStream streams an AI reply for one chat over SSE and persists both the
// user and assistant messages to chat_messages. Request body:
// {"message":string,"web":bool} (web defaults to true and is forced true
// unless the server runs in debug mode; it gates the AI web research tools
// and stance). Events: {"token"}, {"tool"}, {"tool_result"}, {"note"},
// {"error"}, {"done"}.
func (h *Handler) ChatStream(w http.ResponseWriter, r *http.Request) {
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
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if h.loadChat(r, s.StudyID, cid) == nil {
		http.NotFound(w, r)
		return
	}

	var body struct {
		Message string `json:"message"`
		Web     *bool  `json:"web"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		// Empty requests used to synthesize an unsolicited study prompt. Keep
		// the SSE contract for stale clients, but make the operation a no-op.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, "data: {\"done\":true}\n\n")
		return
	}
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.NotFound(w, r)
		return
	}
	if h.quotaFor(r.Context(), u).Exhausted() {
		w.Header().Set("Content-Type", "text/event-stream")
		payload, _ := json.Marshal(map[string]string{"error": quotaExhaustedErr})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		return
	}
	if !h.startAI(u.ID) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"error\":\"Você já tem uma requisição de IA em andamento — aguarde alguns instantes.\"}\n\n")
		return
	}
	defer h.endAI(u.ID)

	web := true
	if body.Web != nil {
		web = *body.Web
	}
	if !h.Debug {
		web = true
	}
	webOn := web && h.TavilyKey != ""

	msgs, _ := h.chats.Messages(r.Context(), cid)
	baseTokenTotal := 0
	for _, existing := range msgs {
		if existing.UsageJSON == "" {
			continue
		}
		var existingUsage session.TokenUsage
		if json.Unmarshal([]byte(existing.UsageJSON), &existingUsage) == nil {
			baseTokenTotal += existingUsage.Total()
		}
	}
	var lastID int64
	if len(msgs) > 0 {
		lastID = msgs[len(msgs)-1].ID
	}
	if msg != "" {
		um := &db.ChatMessage{ChatID: cid, ParentID: lastID, Role: "user", Content: msg}
		if err := h.chats.AddMessage(r.Context(), um); err != nil {
			http.Error(w, "erro ao salvar mensagem", http.StatusInternalServerError)
			return
		}
		lastID = um.ID
	}

	cfg := h.effectiveConfig(s)
	fileIndex := "(nenhum arquivo ainda)"
	if fs, ferr := h.files.ListByStudy(r.Context(), s.StudyID); ferr == nil && len(fs) > 0 {
		var b strings.Builder
		for _, f := range fs {
			fmt.Fprintf(&b, "id=%d %s — %s\n", f.ID, f.Name, f.Kind)
		}
		fileIndex = strings.TrimSpace(b.String())
	}
	apiMsgs := []ai.Message{{Role: "system", Content: ai.WorkspaceSystemPrompt(cfg.Topic, fileIndex, webOn)}}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			apiMsgs = append(apiMsgs, ai.Message{Role: "user", Content: m.Content})
		case "assistant":
			apiMsgs = append(apiMsgs, ai.Message{Role: "assistant", Content: m.Content})
		}
	}
	apiMsgs = append(apiMsgs, ai.Message{Role: "user", Content: msg})

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	send := func(payload map[string]any) {
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}

	tools, exec, sources, artifacts := h.allToolsWithElements(r, s, webOn)
	var eventUsage ai.Usage
	sentArtifacts := 0
	full, toolLog, totalUsage, err := h.client.StreamWithTools(r.Context(), cfg, apiMsgs, tools, exec,
		func(tok string) {
			send(map[string]any{"token": tok})
		},
		func(ev ai.Event) {
			switch ev.Type {
			case "tool":
				send(map[string]any{"tool": map[string]any{"name": ev.Name, "args": ev.Args}})
			case "tool_result":
				send(map[string]any{"tool_result": map[string]any{"name": ev.Name, "summary": ev.Summary}})
				if isPersistentMindMapTool(ev.Name) {
					if graph, mapErr := h.loadOrCreateMindMap(r, s); mapErr == nil {
						send(map[string]any{"mind_map": graph})
					}
				}
				if ev.Name == "create_table" || ev.Name == "create_mind_map" {
					for sentArtifacts < len(*artifacts) {
						send(map[string]any{"element": (*artifacts)[sentArtifacts]})
						sentArtifacts++
					}
				}
			case "note":
				send(map[string]any{"note": ev.Text})
			case "usage":
				if ev.Usage != nil {
					eventUsage = eventUsage.Add(*ev.Usage)
					sessionUsage := session.TokenUsage{PromptTokens: eventUsage.Prompt, CompletionTokens: eventUsage.Completion, TotalTokens: eventUsage.Total()}
					send(map[string]any{"usage": sessionUsage, "token_total": baseTokenTotal + sessionUsage.Total()})
				}
			}
		})
	if uerr := h.recordAIUsage(r.Context(), u.ID, int64(totalUsage.Total()), "chat", s.StudyID, 0, map[string]any{
		"web": webOn, "outcome": outcomeForError(err),
	}); uerr != nil {
		log.Printf("quota: record chat usage for user %d: %v", u.ID, uerr)
	}
	if err != nil {
		send(map[string]any{"error": err.Error()})
		return
	}

	am := &db.ChatMessage{ChatID: cid, ParentID: lastID, Role: "assistant", Content: full}
	if encoded, encodeErr := elements.Encode(*artifacts); encodeErr == nil {
		am.ElementsJSON = encoded
	}
	usage := session.TokenUsage{PromptTokens: totalUsage.Prompt, CompletionTokens: totalUsage.Completion, TotalTokens: totalUsage.Total()}
	if usage.Known() {
		if b, usageErr := json.Marshal(usage); usageErr == nil {
			am.UsageJSON = string(b)
		}
	}
	if len(toolLog) > 0 {
		if b, merr := json.Marshal(toolLog); merr == nil {
			am.ToolLogJSON = string(b)
		}
	}
	if srcs := *sources; len(srcs) > 0 {
		seen := make(map[string]bool, len(srcs))
		dedup := make([]websearch.Result, 0, len(srcs))
		for _, src := range srcs {
			if seen[src.URL] {
				continue
			}
			seen[src.URL] = true
			dedup = append(dedup, src)
		}
		if b, merr := json.Marshal(dedup); merr == nil {
			am.SourcesJSON = string(b)
		}
	}
	_ = h.chats.AddMessage(r.Context(), am)
	_ = h.chats.Touch(r.Context(), cid)
	h.recordTelemetry(r.Context(), db.TelemetryEvent{
		UserID: u.ID, StudyID: s.StudyID, Type: telemetryChatCompleted,
		Metadata: map[string]any{"web": webOn, "tool_calls": len(toolLog)},
		Delta:    db.TelemetryDelta{ChatTurns: 1},
	})
	send(map[string]any{"done": true})
}
