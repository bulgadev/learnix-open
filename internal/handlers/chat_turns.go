package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"learnix/internal/ai"
	"learnix/internal/auth"
	"learnix/internal/db"
	"learnix/internal/elements"
	"learnix/internal/session"
	"learnix/internal/websearch"
)

const (
	chatTurnMaxMessage = 32 * 1024
	chatTurnMaxKey     = 128
	chatTurnRetryDelay = 500 * time.Millisecond
)

type chatTurnResponse struct {
	TurnID             string `json:"turn_id"`
	Status             string `json:"status"`
	ClientKey          string `json:"client_key"`
	Attempt            int    `json:"attempt"`
	ErrorCode          string `json:"error_code,omitempty"`
	Error              string `json:"error,omitempty"`
	AssistantMessageID int64  `json:"assistant_message_id,omitempty"`
	StatusURL          string `json:"status_url"`
}

func turnIDParam(r *http.Request) string {
	id := strings.TrimSpace(chi.URLParam(r, "tid"))
	if len(id) < 16 || len(id) > 64 {
		return ""
	}
	return id
}

func (h *Handler) ChatTurnCreate(w http.ResponseWriter, r *http.Request) {
	tid, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cid, ok := chatIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, tid)
	u := auth.UserFromContext(r.Context())
	if s == nil || u == nil || h.loadChat(r, s.StudyID, cid) == nil || h.chatTurns == nil {
		http.NotFound(w, r)
		return
	}
	var body struct {
		Message   string `json:"message"`
		Web       *bool  `json:"web"`
		ClientKey string `json:"client_key"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, chatTurnMaxMessage+2048)).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mensagem inválida"})
		return
	}
	message := strings.TrimSpace(body.Message)
	if message == "" || len(message) > chatTurnMaxMessage {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a mensagem deve conter entre 1 e 32.768 caracteres"})
		return
	}
	clientKey := strings.TrimSpace(body.ClientKey)
	if clientKey == "" {
		clientKey = db.NewChatTurnID()
	}
	if len(clientKey) > chatTurnMaxKey {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "identificador de tentativa inválido"})
		return
	}
	web := true
	if body.Web != nil {
		web = *body.Web
	}
	if !h.Debug {
		web = true
	}
	web = web && h.TavilyKey != ""
	if h.quotaFor(r.Context(), u).Exhausted() {
		existing, lookupErr := h.chatTurns.ByClientKey(r.Context(), u.ID, cid, clientKey)
		if lookupErr == nil && existing != nil {
			h.startChatTurn(existing.ID)
			writeJSON(w, http.StatusAccepted, h.chatTurnView(existing, strings.TrimRight(r.URL.Path, "/")+"/"+existing.ID))
			return
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": quotaExhaustedErr, "error_code": "quota_exhausted"})
		return
	}
	if existing, lookupErr := h.chatTurns.ByClientKey(r.Context(), u.ID, cid, clientKey); lookupErr == nil && existing != nil {
		h.startChatTurn(existing.ID)
		h.recordTelemetry(r.Context(), db.TelemetryEvent{UserID: u.ID, StudyID: s.StudyID, Type: telemetryChatTurnCreated, Metadata: map[string]any{"idempotent": true, "status": existing.Status}})
		writeJSON(w, http.StatusAccepted, h.chatTurnView(existing, strings.TrimRight(r.URL.Path, "/")+"/"+existing.ID))
		return
	}

	msgs, err := h.chats.Messages(r.Context(), cid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "não foi possível carregar a conversa"})
		return
	}
	parentID := int64(0)
	if len(msgs) > 0 {
		parentID = msgs[len(msgs)-1].ID
	}
	turn := &db.ChatTurn{
		ID: db.NewChatTurnID(), ChatID: cid, StudyID: s.StudyID, UserID: u.ID,
		ClientKey: clientKey, Web: web,
	}
	if err := h.chatTurns.Create(r.Context(), turn, message, parentID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "não foi possível salvar a mensagem"})
		return
	}
	h.startChatTurn(turn.ID)
	h.recordTelemetry(r.Context(), db.TelemetryEvent{UserID: u.ID, StudyID: s.StudyID, Type: telemetryChatTurnCreated, Metadata: map[string]any{"idempotent": false}})
	writeJSON(w, http.StatusAccepted, h.chatTurnView(turn, strings.TrimRight(r.URL.Path, "/")+"/"+turn.ID))
}

func (h *Handler) ChatTurnStatus(w http.ResponseWriter, r *http.Request) {
	tid, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cid, ok := chatIDParam(r)
	turnID := turnIDParam(r)
	u := auth.UserFromContext(r.Context())
	if !ok || turnID == "" || u == nil || h.chatTurns == nil {
		http.NotFound(w, r)
		return
	}
	turn, err := h.chatTurns.Get(r.Context(), turnID, u.ID, tid, cid)
	if err != nil || turn == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, h.chatTurnView(turn, r.URL.Path))
}

func (h *Handler) ChatTurnRetry(w http.ResponseWriter, r *http.Request) {
	tid, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	cid, ok := chatIDParam(r)
	turnID := turnIDParam(r)
	u := auth.UserFromContext(r.Context())
	if !ok || turnID == "" || u == nil || h.chatTurns == nil {
		http.NotFound(w, r)
		return
	}
	turn, err := h.chatTurns.Get(r.Context(), turnID, u.ID, tid, cid)
	if err != nil || turn == nil {
		http.NotFound(w, r)
		return
	}
	if turn.Status != "failed" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "esta tentativa ainda está em andamento ou já terminou"})
		return
	}
	if h.quotaFor(r.Context(), u).Exhausted() {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": quotaExhaustedErr, "error_code": "quota_exhausted"})
		return
	}
	if err := h.chatTurns.Retry(r.Context(), turnID, u.ID, tid, cid); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "esta tentativa não pode ser repetida agora"})
		return
	}
	turn, _ = h.chatTurns.Get(r.Context(), turnID, u.ID, tid, cid)
	h.startChatTurn(turnID)
	h.recordTelemetry(r.Context(), db.TelemetryEvent{UserID: u.ID, StudyID: tid, Type: telemetryChatTurnRetried, Metadata: map[string]any{"attempt": turn.Attempt}})
	writeJSON(w, http.StatusAccepted, h.chatTurnView(turn, strings.TrimSuffix(r.URL.Path, "/retry")))
}

func (h *Handler) chatTurnView(turn *db.ChatTurn, statusURL string) chatTurnResponse {
	return chatTurnResponse{
		TurnID: turn.ID, Status: turn.Status, ClientKey: turn.ClientKey, Attempt: turn.Attempt,
		ErrorCode: turn.ErrorCode, Error: turn.ErrorMessage, AssistantMessageID: turn.AssistantMessageID,
		StatusURL: statusURL,
	}
}

func (h *Handler) startChatTurn(turnID string) {
	go func() {
		_ = h.processChatTurn(context.Background(), turnID)
	}()
}

func (h *Handler) processChatTurn(ctx context.Context, turnID string) error {
	turn, err := h.chatTurns.GetAny(ctx, turnID)
	if err != nil || turn == nil {
		return err
	}
	if turn.Status != "queued" {
		return nil
	}
	if err := h.chatTurns.MarkRunning(ctx, turnID); err != nil {
		return err
	}
	if !h.startAI(turn.UserID) {
		_ = h.chatTurns.Fail(ctx, turnID, "busy", aiBusyErr, 0)
		return nil
	}
	defer h.endAI(turn.UserID)
	if h.quotaFor(ctx, &db.User{ID: turn.UserID}).Exhausted() {
		_ = h.chatTurns.Fail(ctx, turnID, "quota_exhausted", quotaExhaustedErr, 0)
		return nil
	}

	st, err := h.studies.Get(ctx, turn.StudyID)
	if err != nil || st == nil || st.UserID != turn.UserID {
		_ = h.chatTurns.Fail(ctx, turnID, "study_unavailable", "o estudo não está disponível.", 0)
		return err
	}
	s := &session.Session{StudyID: st.ID, Config: session.Config{BaseURL: st.BaseURL, APIKey: st.APIKey, Model: st.Model, Topic: st.Topic}, Phase: st.Phase, Feedback: st.Feedback, Reviewing: st.Reviewing}
	full, toolLog, sources, artifacts, usage, runErr := h.executeChatTurn(ctx, turn, s)
	if uerr := h.recordAIUsage(ctx, turn.UserID, int64(usage.Total()), "chat", turn.StudyID, 0, map[string]any{
		"web": turn.Web, "outcome": outcomeForError(runErr), "attempt": turn.Attempt,
	}); uerr != nil {
		// Quota accounting must not turn a completed provider response into a
		// lost chat answer; the error remains in operational logs.
		log.Printf("quota: record chat usage for user %d: %v", turn.UserID, uerr)
	}
	if runErr != nil {
		code, friendly := classifyChatTurnError(runErr)
		providerStatus := chatProviderStatus(runErr)
		log.Printf("chat turn %s failed: code=%s err=%v", turnID, code, runErr)
		_ = h.chatTurns.Fail(ctx, turnID, code, friendly, providerStatus)
		h.recordTelemetry(ctx, db.TelemetryEvent{UserID: turn.UserID, StudyID: turn.StudyID, Type: telemetryChatFailed, Metadata: map[string]any{"code": code, "attempt": turn.Attempt}})
		return runErr
	}
	am := &db.ChatMessage{Content: full}
	if encoded, encodeErr := elements.Encode(artifacts); encodeErr == nil {
		am.ElementsJSON = encoded
	}
	if usage.Total() > 0 {
		if b, usageErr := json.Marshal(session.TokenUsage{PromptTokens: usage.Prompt, CompletionTokens: usage.Completion, TotalTokens: usage.Total()}); usageErr == nil {
			am.UsageJSON = string(b)
		}
	}
	if len(toolLog) > 0 {
		if b, merr := json.Marshal(toolLog); merr == nil {
			am.ToolLogJSON = string(b)
		}
	}
	if len(sources) > 0 {
		seen := make(map[string]bool, len(sources))
		dedup := make([]websearch.Result, 0, len(sources))
		for _, src := range sources {
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
	if err := h.chatTurns.Complete(ctx, turnID, am); err != nil {
		log.Printf("chat turn %s persistence failed: %v", turnID, err)
		_ = h.chatTurns.Fail(ctx, turnID, "persistence_error", "A resposta foi gerada, mas não conseguimos salvá-la. Tente novamente.", 0)
		return err
	}
	h.recordTelemetry(ctx, db.TelemetryEvent{UserID: turn.UserID, StudyID: turn.StudyID, Type: telemetryChatCompleted, Metadata: map[string]any{"web": turn.Web, "tool_calls": len(toolLog), "attempt": turn.Attempt}, Delta: db.TelemetryDelta{ChatTurns: 1}})
	return nil
}

func (h *Handler) executeChatTurn(ctx context.Context, turn *db.ChatTurn, s *session.Session) (string, []ai.ToolEvent, []websearch.Result, []elements.Element, ai.Usage, error) {
	msgs, err := h.chats.Messages(ctx, turn.ChatID)
	if err != nil {
		return "", nil, nil, nil, ai.Usage{}, err
	}
	fileIndex := "(nenhum arquivo ainda)"
	if fs, ferr := h.files.ListByStudy(ctx, s.StudyID); ferr == nil && len(fs) > 0 {
		var b strings.Builder
		for _, f := range fs {
			_, _ = fmt.Fprintf(&b, "id=%d %s — %s\n", f.ID, f.Name, f.Kind)
		}
		fileIndex = strings.TrimSpace(b.String())
	}
	apiMsgs := []ai.Message{{Role: "system", Content: ai.WorkspaceSystemPrompt(s.Config.Topic, fileIndex, turn.Web)}}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			apiMsgs = append(apiMsgs, ai.Message{Role: "user", Content: m.Content})
		case "assistant":
			apiMsgs = append(apiMsgs, ai.Message{Role: "assistant", Content: m.Content})
		}
	}
	r := (&http.Request{}).WithContext(ctx)
	tools, exec, sources, artifacts := h.allToolsWithElements(r, s, turn.Web)
	var total ai.Usage
	var full string
	var logEntries []ai.ToolEvent
	var runErr error
	for attempt := 0; attempt < 2; attempt++ {
		full, logEntries, totalRound, err := h.client.StreamWithTools(ctx, h.effectiveConfig(s), apiMsgs, tools, exec, func(string) {}, func(ai.Event) {})
		total = total.Add(totalRound)
		if err == nil {
			return full, logEntries, *sources, *artifacts, total, nil
		}
		runErr = err
		if attempt == 0 && full == "" && len(logEntries) == 0 && isTransientChatError(err) {
			time.Sleep(chatTurnRetryDelay)
			continue
		}
		break
	}
	return full, logEntries, *sources, *artifacts, total, runErr
}

func isTransientChatError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "timeout") || strings.Contains(s, "connection reset") ||
		strings.Contains(s, "erro da api (429)") || strings.Contains(s, "erro da api (502)") ||
		strings.Contains(s, "erro da api (503)") || strings.Contains(s, "erro da api (504)")
}

func classifyChatTurnError(err error) (string, string) {
	if isTransientChatError(err) {
		return "transient_network", "A conexão com a IA caiu antes de concluir. Tente novamente."
	}
	if strings.Contains(strings.ToLower(err.Error()), "erro da api (401)") {
		return "provider_auth", "O serviço de IA recusou a autenticação."
	}
	return "provider_error", "A IA não conseguiu concluir esta resposta. Tente novamente."
}

func chatProviderStatus(err error) int {
	if err == nil {
		return 0
	}
	const prefix = "erro da API ("
	s := strings.ToLower(err.Error())
	start := strings.Index(s, prefix)
	if start < 0 {
		return 0
	}
	start += len(prefix)
	end := strings.IndexByte(s[start:], ')')
	if end < 0 {
		return 0
	}
	status, _ := strconv.Atoi(s[start : start+end])
	return status
}

// GetAny is intentionally narrow and only used by the trusted background
// worker after the turn id was created by an authenticated request.
