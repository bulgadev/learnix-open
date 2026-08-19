package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"learnix/internal/auth"
	"learnix/internal/db"
	"learnix/internal/elements"
	"learnix/internal/session"
)

// fakeOpenAI serves an OpenAI-compatible SSE chat completion that replies with
// a fixed token.
func fakeOpenAI(reply string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// fakeOpenAIToolCall serves a first response that calls create_note, then a
// plain text answer once the tool result comes back.
func fakeOpenAIToolCall(name, content string) *httptest.Server {
	args, _ := json.Marshal(map[string]string{"name": name, "content": content})
	return fakeOpenAIToolCallNamed("create_note", string(args), "Pronto, nota criada.")
}

// fakeOpenAIToolCallNamed serves a first response that calls toolName with
// raw argsJSON, then a plain text answer once the tool result comes back.
func fakeOpenAIToolCallNamed(toolName, argsJSON, reply string) *httptest.Server {
	first, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{
		map[string]any{"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": toolName, "arguments": argsJSON}},
	}}}}})
	var mu sync.Mutex
	n := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n++
		cur := n
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if cur == 1 {
			fmt.Fprintf(w, "data: %s\n\n", first)
			fmt.Fprint(w, "data: {\"choices\":[{\"finish_reason\":\"tool_calls\",\"delta\":{}}]}\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// fakeOpenAICapture serves a direct SSE reply (no tool calls) and records
// every request body it receives.
func fakeOpenAICapture(reply string, bodies *[][]byte) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		*bodies = append(*bodies, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", reply)
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

// tavilyHits counts which Tavily endpoints were called.
type tavilyHits struct {
	mu              sync.Mutex
	search, extract int
}

// fakeTavily serves canned Tavily search/extract responses.
func fakeTavily(hits *tavilyHits) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.mu.Lock()
		defer hits.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			hits.search++
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{
				map[string]any{"title": "Fotossíntese — Wikipédia", "url": "https://pt.wikipedia.org/wiki/Fotossintese", "content": "Processo pelo qual plantas convertem luz em energia química."},
			}})
		case "/extract":
			hits.extract++
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{
				map[string]any{"raw_content": "Conteúdo extraído da página sobre fotossíntese."},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
}

// createStudyAt creates the study directly in the DB instead of going through
// POST /studies: tests point studies at local fake LLM servers (127.0.0.1),
// which the handler's SSRF guard rejects on purpose.
func (te *testEnv) createStudyAt(t *testing.T, topic, baseURL string, cookie *http.Cookie) string {
	t.Helper()
	sid, ok := auth.Verify(cookie.Value, te.secret)
	if !ok {
		t.Fatal("createStudyAt: invalid test cookie")
	}
	srow, err := te.sessions.Get(testCtx, sid)
	if err != nil || srow == nil {
		t.Fatalf("createStudyAt: session lookup: %v", err)
	}
	st := &db.Study{
		UserID:  srow.UserID,
		Topic:   topic,
		BaseURL: baseURL,
		Model:   "test-model",
		Phase:   "study",
	}
	if err := te.studies.Create(testCtx, st); err != nil {
		t.Fatalf("createStudyAt: %v", err)
	}
	return fmt.Sprintf("/study/%d", st.ID)
}

func (te *testEnv) streamChat(t *testing.T, loc string, cid int64, message string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"message":` + strconv.Quote(message) + `}`
	return te.reqCT(t, "POST", loc+"/chats/"+strconv.FormatInt(cid, 10)+"/stream", "application/json", body, cookie)
}

func (te *testEnv) firstChat(t *testing.T, studyID int64) db.Chat {
	t.Helper()
	chats, err := te.chats.ListByStudy(testCtx, studyID)
	if err != nil || len(chats) == 0 {
		t.Fatalf("expected at least one chat, got %d (%v)", len(chats), err)
	}
	return chats[0]
}

func TestChats_AutoCreateOnStudyPage(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "chats@test.com", "hunter2!")
	loc := te.createStudy(t, "fotossintese", cookie)

	rr := te.req(t, "GET", loc, "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("study page: expected 200, got %d", rr.Code)
	}
	chats, err := te.chats.ListByStudy(testCtx, fid64(t, loc))
	if err != nil || len(chats) != 1 {
		t.Fatalf("expected exactly 1 auto-created chat, got %d (%v)", len(chats), err)
	}

	// Loading again must not create another chat.
	te.req(t, "GET", loc, "", cookie)
	chats, _ = te.chats.ListByStudy(testCtx, fid64(t, loc))
	if len(chats) != 1 {
		t.Errorf("expected still 1 chat after reload, got %d", len(chats))
	}
}

func TestChats_CreateSecond(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "chat2@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)

	rr := te.req(t, "POST", loc+"/chats", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create chat: expected 200, got %d", rr.Code)
	}
	chats, _ := te.chats.ListByStudy(testCtx, fid64(t, loc))
	if len(chats) != 2 {
		t.Fatalf("expected 2 chats, got %d", len(chats))
	}
	if !strings.Contains(rr.Body.String(), "Nova conversa") {
		t.Errorf("pane should list the new chat")
	}
}

func TestChats_StreamPersistsBothMessages(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAI("ola mundo")
	defer srv.Close()
	cookie := te.register(t, "stream@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)

	rr := te.streamChat(t, loc, c.ID, "oi", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"token"`) || !strings.Contains(rr.Body.String(), `"done":true`) {
		t.Errorf("SSE body missing token/done events: %s", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"token_total":10`) {
		t.Errorf("SSE body missing token usage: %s", rr.Body.String())
	}

	msgs, err := te.chats.Messages(testCtx, c.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d (%v)", len(msgs), err)
	}
	if msgs[0].Role != "user" || msgs[0].Content != "oi" {
		t.Errorf("bad user message: %+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "ola mundo" {
		t.Errorf("bad assistant message: %+v", msgs[1])
	}
	if msgs[1].ParentID != msgs[0].ID {
		t.Errorf("assistant message should chain to user message (parent %d, want %d)", msgs[1].ParentID, msgs[0].ID)
	}
	if !strings.Contains(msgs[1].UsageJSON, `"total_tokens":10`) {
		t.Errorf("assistant usage missing: %s", msgs[1].UsageJSON)
	}
}

func TestChats_EmptyStreamIsNoop(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAI("visao geral")
	defer srv.Close()
	cookie := te.register(t, "empty@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)

	rr := te.streamChat(t, loc, c.ID, "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d", rr.Code)
	}
	msgs, _ := te.chats.Messages(testCtx, c.ID)
	if len(msgs) != 0 {
		t.Fatalf("empty stream must not create a message, got %d", len(msgs))
	}
	if !strings.Contains(rr.Body.String(), `"done":true`) {
		t.Errorf("empty stream should return a terminal no-op event: %s", rr.Body.String())
	}
}

func TestChats_SwitchIsolation(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAI("resp")
	defer srv.Close()
	cookie := te.register(t, "switch@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	a := te.firstChat(t, sid)
	te.streamChat(t, loc, a.ID, "pergunta A", cookie)

	rr := te.req(t, "POST", loc+"/chats", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create chat B: %d", rr.Code)
	}
	var b db.Chat
	for _, c := range mustListChats(t, te, sid) {
		if c.ID != a.ID {
			b = c
		}
	}
	if b.ID == 0 {
		t.Fatal("chat B not found")
	}

	rr = te.req(t, "GET", loc+"/chats?chat="+strconv.FormatInt(b.ID, 10), "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("switch: %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "pergunta A") {
		t.Errorf("chat B pane must not show chat A messages")
	}
	msgs, _ := te.chats.Messages(testCtx, a.ID)
	if len(msgs) != 2 {
		t.Errorf("chat A should keep its 2 messages, got %d", len(msgs))
	}
}

func mustListChats(t *testing.T, te *testEnv, studyID int64) []db.Chat {
	t.Helper()
	chats, err := te.chats.ListByStudy(testCtx, studyID)
	if err != nil {
		t.Fatalf("list chats: %v", err)
	}
	return chats
}

func TestChats_RenameAndDelete(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "ren@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)

	rr := te.req(t, "POST", loc+"/chats/"+strconv.FormatInt(c.ID, 10)+"/rename", "title=Revisao+ENEM", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("rename: %d", rr.Code)
	}
	got, _ := te.chats.Get(testCtx, c.ID)
	if got == nil || got.Title != "Revisao ENEM" {
		t.Errorf("rename not persisted: %+v", got)
	}

	// Delete the only chat → pane must auto-create a fresh one.
	rr = te.req(t, "POST", loc+"/chats/"+strconv.FormatInt(c.ID, 10)+"/delete", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d", rr.Code)
	}
	chats := mustListChats(t, te, sid)
	if len(chats) != 1 || chats[0].ID == c.ID {
		t.Errorf("expected one fresh auto-created chat after deleting the only one, got %+v", chats)
	}
}

func TestChats_CrossUser404(t *testing.T) {
	te := newTestEnv(t)
	cookieA := te.register(t, "a@chats.com", "hunter2!")
	locA := te.createStudy(t, "tema A", cookieA)
	te.req(t, "GET", locA, "", cookieA)
	sidA := fid64(t, locA)
	cA := te.firstChat(t, sidA)

	cookieB := te.register(t, "b@chats.com", "hunter2!")
	cid := strconv.FormatInt(cA.ID, 10)
	for _, route := range []struct{ method, path string }{
		{"POST", locA + "/chats/" + cid + "/rename"},
		{"POST", locA + "/chats/" + cid + "/delete"},
		{"POST", locA + "/chats/" + cid + "/stream"},
	} {
		rr := te.reqCT(t, route.method, route.path, "application/json", `{"message":"x"}`, cookieB)
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s %s as other user: expected 404, got %d", route.method, route.path, rr.Code)
		}
	}
	// Chat must survive the attempts.
	if c, _ := te.chats.Get(testCtx, cA.ID); c == nil {
		t.Error("chat was deleted by unauthorized request")
	}
}

func TestChats_StreamCreatesNoteViaTool(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAIToolCall("Resumo da IA", "## Top\n- ponto")
	defer srv.Close()
	cookie := te.register(t, "tools@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)

	rr := te.streamChat(t, loc, c.ID, "crie uma nota", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"tool"`) || !strings.Contains(body, `"tool_result"`) {
		t.Errorf("SSE body missing tool/tool_result events: %s", body)
	}
	if !strings.Contains(body, `"done":true`) {
		t.Errorf("SSE body missing done event: %s", body)
	}

	files, err := te.files.ListByStudy(testCtx, sid)
	if err != nil || len(files) != 1 {
		t.Fatalf("expected 1 file created by the tool, got %d (%v)", len(files), err)
	}
	f := files[0]
	if f.Name != "Resumo da IA" || f.Kind != "note" || f.Content != "## Top\n- ponto" {
		t.Errorf("bad note: %+v", f)
	}
	versions, err := te.files.Versions(testCtx, f.ID)
	if err != nil || len(versions) == 0 {
		t.Fatalf("versions: %v (len %d)", err, len(versions))
	}
	if versions[0].Author != "ai" {
		t.Errorf("newest version author = %q, want ai", versions[0].Author)
	}

	msgs, err := te.chats.Messages(testCtx, c.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d (%v)", len(msgs), err)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "Pronto, nota criada." {
		t.Errorf("bad assistant message: %+v", msgs[1])
	}
	var log []struct {
		Name    string `json:"name"`
		Args    string `json:"args"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(msgs[1].ToolLogJSON), &log); err != nil {
		t.Fatalf("ToolLogJSON invalid: %v (%q)", err, msgs[1].ToolLogJSON)
	}
	if len(log) != 1 || log[0].Name != "create_note" || log[0].Summary != "nota criada" {
		t.Errorf("tool log = %+v", log)
	}
}

func TestChats_StreamCreatesTableElement(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAIToolCallNamed("create_table", `{"title":"Estados","columns":["Substância","Estado"],"rows":[["A","Sólido"]]}`, "Veja a tabela.")
	defer srv.Close()
	cookie := te.register(t, "table-tool@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "química", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)

	rr := te.streamChat(t, loc, c.ID, "crie uma tabela", cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"element"`) {
		t.Fatalf("table stream missing element event: %d %s", rr.Code, rr.Body.String())
	}
	msgs, _ := te.chats.Messages(testCtx, c.ID)
	var assistant db.ChatMessage
	for _, msg := range msgs {
		if msg.Role == "assistant" {
			assistant = msg
		}
	}
	decoded, err := elements.Decode(assistant.ElementsJSON)
	if err != nil || len(decoded) != 1 || decoded[0].Type != elements.TableType {
		t.Fatalf("persisted elements: %v %+v", err, decoded)
	}
	if body := te.req(t, "GET", loc+"/chats", "", cookie).Body.String(); !strings.Contains(body, "learning-table-card") || !strings.Contains(body, "Substância") || !strings.Contains(body, "chat-assistant-avatar") || !strings.Contains(body, "chat-assistant-message") {
		t.Fatalf("chat pane did not render persisted table: %s", body)
	}

	if rr := te.req(t, "POST", saveNoteURL(loc, c.ID, assistant.ID), "", cookie); rr.Code != http.StatusOK {
		t.Fatalf("save table response: %d", rr.Code)
	}
	files, _ := te.files.ListByStudy(testCtx, sid)
	if len(files) != 1 || files[0].ElementsJSON == "" {
		t.Fatalf("saved note did not preserve elements: %+v", files)
	}
}

func TestAllTools_WebGating(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "alltools@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	req := httptest.NewRequest("GET", "/", nil)
	s := &session.Session{StudyID: sid}

	webTrue, webFalse := true, false
	for _, tc := range []struct {
		name  string
		key   string
		web   *bool
		debug bool
		want  int
	}{
		{"no key web=true", "", &webTrue, false, 12},
		{"key web=true", "k", &webTrue, false, 14},
		{"key web=false debug", "k", &webFalse, true, 12},
		{"key web=false no debug", "k", &webFalse, false, 14},
	} {
		te.handler.TavilyKey = tc.key
		te.handler.Debug = tc.debug
		web := true
		if tc.web != nil {
			web = *tc.web
		}
		if !te.handler.Debug {
			web = true
		}
		tools, exec, sources := te.handler.allTools(req, s, web)
		if len(tools) != tc.want {
			t.Errorf("%s: %d tools, want %d", tc.name, len(tools), tc.want)
		}
		if sources == nil || exec == nil {
			t.Fatalf("%s: nil exec or collector", tc.name)
		}
		names := map[string]bool{}
		for _, tl := range tools {
			names[tl.Function.Name] = true
		}
		if web && tc.key != "" && (!names["search_web"] || !names["fetch_url"]) {
			t.Errorf("%s: web tools missing: %v", tc.name, names)
		}
		if !web && (names["search_web"] || names["fetch_url"]) {
			t.Errorf("%s: web tools must be gated: %v", tc.name, names)
		}
	}
}

func TestChats_StreamWebSearchPersistsSources(t *testing.T) {
	te := newTestEnv(t)
	hits := &tavilyHits{}
	tav := fakeTavily(hits)
	defer tav.Close()
	te.handler.TavilyKey = "test-key"
	te.handler.tavilyBase = tav.URL

	args, _ := json.Marshal(map[string]string{"query": "fotossintese"})
	srv := fakeOpenAIToolCallNamed("search_web", string(args), "A fotossíntese converte luz em energia.")
	defer srv.Close()
	cookie := te.register(t, "web@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	rr := te.streamChat(t, loc, c.ID, "o que é fotossíntese?", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"tool_result"`) || !strings.Contains(body, "pesquisou") {
		t.Errorf("SSE body missing tool_result summary: %s", body)
	}
	if hits.search != 1 {
		t.Errorf("tavily search hits = %d, want 1", hits.search)
	}

	msgs, err := te.chats.Messages(testCtx, c.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d (%v)", len(msgs), err)
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "A fotossíntese converte luz em energia." {
		t.Errorf("bad assistant message: %+v", msgs[1])
	}
	var sources []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(msgs[1].SourcesJSON), &sources); err != nil {
		t.Fatalf("SourcesJSON invalid: %v (%q)", err, msgs[1].SourcesJSON)
	}
	if len(sources) != 1 || sources[0].URL != "https://pt.wikipedia.org/wiki/Fotossintese" {
		t.Errorf("sources = %+v", sources)
	}
}

func TestChats_StreamFetchURL(t *testing.T) {
	te := newTestEnv(t)
	hits := &tavilyHits{}
	tav := fakeTavily(hits)
	defer tav.Close()
	te.handler.TavilyKey = "test-key"
	te.handler.tavilyBase = tav.URL

	args, _ := json.Marshal(map[string]string{"url": "https://pt.wikipedia.org/wiki/Fotossintese"})
	srv := fakeOpenAIToolCallNamed("fetch_url", string(args), "Resumo da página.")
	defer srv.Close()
	cookie := te.register(t, "fetch@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	rr := te.streamChat(t, loc, c.ID, "leia a página", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"tool_result"`) || !strings.Contains(body, "leu: pt.wikipedia.org") {
		t.Errorf("SSE body missing fetch summary: %s", body)
	}
	if hits.extract != 1 {
		t.Errorf("tavily extract hits = %d, want 1", hits.extract)
	}

	msgs, err := te.chats.Messages(testCtx, c.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("expected 2 persisted messages, got %d (%v)", len(msgs), err)
	}
	if msgs[1].Content != "Resumo da página." {
		t.Errorf("bad assistant message: %+v", msgs[1])
	}
	if msgs[1].SourcesJSON != "" {
		t.Errorf("fetch_url must not add sources, got %q", msgs[1].SourcesJSON)
	}
}

func TestChats_StreamDebugNoWeb(t *testing.T) {
	te := newTestEnv(t)
	te.handler.Debug = true
	hits := &tavilyHits{}
	tav := fakeTavily(hits)
	defer tav.Close()
	te.handler.TavilyKey = "test-key"
	te.handler.tavilyBase = tav.URL

	var bodies [][]byte
	srv := fakeOpenAICapture("resposta sem web", &bodies)
	defer srv.Close()
	cookie := te.register(t, "debugnoweb@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	rr := te.reqCT(t, "POST", loc+"/chats/"+strconv.FormatInt(c.ID, 10)+"/stream", "application/json", `{"message":"oi","web":false}`, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"done":true`) {
		t.Errorf("SSE body missing done event: %s", rr.Body.String())
	}
	if len(bodies) == 0 {
		t.Fatal("AI backend was never called")
	}

	var first struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(bodies[0], &first); err != nil {
		t.Fatalf("first AI request body invalid: %v (%s)", err, bodies[0])
	}
	for _, tl := range first.Tools {
		if tl.Function.Name == "search_web" || tl.Function.Name == "fetch_url" {
			t.Errorf("web tool %q offered with web=false", tl.Function.Name)
		}
	}
	var system string
	for _, m := range first.Messages {
		if m.Role == "system" {
			system = m.Content
			break
		}
	}
	if !strings.Contains(system, "NÃO tem acesso à internet") {
		t.Errorf("system message missing no-web stance: %q", system)
	}
	if hits.search != 0 || hits.extract != 0 {
		t.Errorf("tavily hits = search %d extract %d, want 0/0", hits.search, hits.extract)
	}
}

func TestChats_StreamNoDebugForcesWeb(t *testing.T) {
	te := newTestEnv(t)
	tav := fakeTavily(&tavilyHits{})
	defer tav.Close()
	te.handler.TavilyKey = "test-key"
	te.handler.tavilyBase = tav.URL

	var bodies [][]byte
	srv := fakeOpenAICapture("resposta com web", &bodies)
	defer srv.Close()
	cookie := te.register(t, "forcedweb@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "fotossintese", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	rr := te.reqCT(t, "POST", loc+"/chats/"+strconv.FormatInt(c.ID, 10)+"/stream", "application/json", `{"message":"oi","web":false}`, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if len(bodies) == 0 {
		t.Fatal("AI backend was never called")
	}

	var first struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(bodies[0], &first); err != nil {
		t.Fatalf("first AI request body invalid: %v (%s)", err, bodies[0])
	}
	var names []string
	for _, tl := range first.Tools {
		names = append(names, tl.Function.Name)
	}
	if !slices.Contains(names, "search_web") || !slices.Contains(names, "fetch_url") {
		t.Errorf("web tools not offered despite web=false with Debug=false: %v", names)
	}
	var system string
	for _, m := range first.Messages {
		if m.Role == "system" {
			system = m.Content
			break
		}
	}
	if !strings.Contains(system, "search_web e fetch_url") {
		t.Errorf("system message missing web stance: %q", system)
	}
}

// One AI call at a time per user: while a stream is in flight, a second one
// is refused before touching the provider — this closes the quota race where
// concurrent streams all passed the gate before any of them charged.
func TestChatStream_SingleFlight(t *testing.T) {
	te := newTestEnv(t)
	hit := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(hit) })
		<-release
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cookie := te.register(t, "inflight@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "tema", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	first := make(chan string, 1)
	go func() { first <- te.streamChat(t, loc, c.ID, "oi", cookie).Body.String() }()
	<-hit

	rr := te.streamChat(t, loc, c.ID, "de novo", cookie)
	if !strings.Contains(rr.Body.String(), "em andamento") {
		t.Fatalf("second concurrent stream must be refused, got: %s", rr.Body.String())
	}

	close(release)
	body := <-first
	if !strings.Contains(body, `"done":true`) {
		t.Fatalf("first stream must complete, got: %s", body)
	}

	msgs, _ := te.chats.Messages(testCtx, c.ID)
	for _, m := range msgs {
		if m.Content == "de novo" {
			t.Error("the refused stream must not persist its message")
		}
	}
}
