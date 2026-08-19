package quizgen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"learnix/internal/ai"
	"learnix/internal/elements"
	"learnix/internal/session"
	"learnix/internal/websearch"
)

func sseFrame(t *testing.T, payload any) string {
	t.Helper()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return "data: " + string(b) + "\n\n"
}

func contentDelta(content string) any {
	return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": content}}}}
}

func toolDelta(tcs ...any) any {
	return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": tcs}}}}
}

func finishToolCalls() any {
	return map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "tool_calls"}}}
}

// questionsJSON builds a {"questions":[...]} payload with n questions of opts
// options each.
func questionsJSON(t *testing.T, n, opts int) string {
	return questionsJSONWithElements(t, n, opts, nil)
}

func questionsJSONWithElements(t *testing.T, n, opts int, firstElements []elements.Element) string {
	t.Helper()
	qs := make([]session.Question, 0, n)
	for i := range n {
		options := make([]string, 0, opts)
		for j := range opts {
			options = append(options, fmt.Sprintf("q%d alternativa %d", i, j))
		}
		correct := 0
		if opts > 0 {
			correct = i % opts
		}
		qs = append(qs, session.Question{
			Text:        fmt.Sprintf("Enunciado da questão %d", i),
			Context:     fmt.Sprintf("Texto de apoio %d", i),
			Options:     options,
			Correct:     correct,
			Explanation: fmt.Sprintf("Gabarito comentado %d", i),
		})
	}
	if len(qs) > 0 {
		qs[0].Elements = firstElements
	}
	b, err := json.Marshal(map[string][]session.Question{"questions": qs})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func completionBody(t *testing.T, content string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
		"usage":   map[string]any{"prompt_tokens": 100, "completion_tokens": 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// usageChunk builds a final SSE chunk carrying only token usage, as providers
// send when stream_options.include_usage is true.
func usageChunk(prompt, completion int) any {
	return map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": prompt, "completion_tokens": completion}}
}

// aiMock serves both the streaming research loop and the non-streaming
// author/review completions, recording every request body.
type aiMock struct {
	mu        sync.Mutex
	bodies    []map[string]any
	streams   int
	completes int
	streamFn  func(w http.ResponseWriter, n int)
	jsonFn    func(w http.ResponseWriter, n int)
}

func (m *aiMock) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		m.mu.Lock()
		m.bodies = append(m.bodies, parsed)
		isStream := parsed["stream"] == true
		var n int
		if isStream {
			m.streams++
			n = m.streams
		} else {
			m.completes++
			n = m.completes
		}
		m.mu.Unlock()
		if isStream {
			w.Header().Set("Content-Type", "text/event-stream")
			m.streamFn(w, n)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		m.jsonFn(w, n)
	}))
}

func (m *aiMock) briefOnly(w http.ResponseWriter, _ int) {
	fmt.Fprint(w, sseFrameNoFail(contentDelta("Briefing: questões reais do ENEM sobre o tema.")))
	fmt.Fprint(w, sseFrameNoFail(usageChunk(20, 10)))
	fmt.Fprint(w, "data: [DONE]\n\n")
}

// searchThenBrief makes the research stage issue one search_web call (so the
// pipeline collects web sources) before streaming the briefing.
func (m *aiMock) searchThenBrief(w http.ResponseWriter, n int) {
	if n == 1 {
		fmt.Fprint(w, sseFrameNoFail(toolDelta(map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "search_web", "arguments": `{"query":"tema"}`},
		})))
		fmt.Fprint(w, sseFrameNoFail(finishToolCalls()))
		fmt.Fprint(w, sseFrameNoFail(usageChunk(15, 5)))
		fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	m.briefOnly(w, n)
}

func sseFrameNoFail(payload any) string {
	b, _ := json.Marshal(payload)
	return "data: " + string(b) + "\n\n"
}

// fakeTavily serves canned search results.
func fakeTavily() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/search":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{
				map[string]any{"title": "Questão ENEM 2019", "url": "https://exemplo.com/enem-2019", "content": "Questão real sobre o tema."},
				map[string]any{"title": "FUVEST 2020", "url": "https://exemplo.com/fuvest-2020", "content": "Outra questão real."},
			}})
		case "/extract":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{
				map[string]any{"raw_content": "Conteúdo da página."},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestGenerate_HappyPath(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.searchThenBrief
	mock.jsonFn = func(w http.ResponseWriter, n int) {
		switch n {
		case 1:
			fmt.Fprint(w, completionBody(t, questionsJSON(t, 3, 5)))
		case 2:
			fmt.Fprint(w, completionBody(t, questionsJSON(t, 3, 5)))
		}
	}
	aiSrv := mock.server()
	defer aiSrv.Close()
	tavSrv := fakeTavily()
	defer tavSrv.Close()

	var progress []Progress
	qs, sources, trace, err := GenerateWithTrace(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"},
		websearch.NewClientWithBase("test", tavSrv.URL),
		Spec{Topic: "fotossíntese", Feedback: "fase escura", Count: 3, Web: true},
		func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 3 {
		t.Fatalf("got %d questions, want 3", len(qs))
	}
	for i, q := range qs {
		if len(q.Options) != 5 {
			t.Errorf("question %d has %d options", i, len(q.Options))
		}
		if q.Correct < 0 || q.Correct > 4 {
			t.Errorf("question %d correct out of range: %d", i, q.Correct)
		}
	}
	if len(sources) != 2 {
		t.Errorf("sources = %d, want 2", len(sources))
	}
	if trace.Topic != "fotossíntese" || trace.ResearchBrief == "" || len(trace.Sources) != 2 || len(trace.EvaluationCriteria) == 0 {
		t.Errorf("trace missing research/evaluation data: %+v", trace)
	}
	if trace.TokenUsage.Total() != 350 {
		t.Errorf("trace token usage = %+v, want 350", trace.TokenUsage)
	}

	var stages []string
	for _, p := range progress {
		if len(stages) == 0 || stages[len(stages)-1] != p.Stage {
			stages = append(stages, p.Stage)
		}
		if p.Message == "" {
			t.Errorf("progress with empty message: %+v", p)
		}
	}
	if strings.Join(stages, ",") != "research,author,review" {
		t.Errorf("stage sequence = %v, want [research author review]", stages)
	}
	last := progress[len(progress)-1]
	if last.Metrics == nil || last.Metrics.Current != 3 || last.Metrics.Total != 3 || last.Metrics.Sources != 2 || last.Metrics.Searches != 1 || last.Metrics.ModelCalls != 4 || last.Metrics.Tokens != 350 {
		t.Errorf("final progress metrics = %+v, want 3 questions, 2 sources, 1 search, 4 calls, 350 tokens", last.Metrics)
	}

	if mock.streams != 2 || mock.completes != 2 {
		t.Errorf("requests = %d stream / %d complete, want 2/2", mock.streams, mock.completes)
	}
}

func TestGenerate_ResearchOffersOnlyWebTools(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.searchThenBrief
	mock.jsonFn = func(w http.ResponseWriter, n int) {
		fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
	}
	aiSrv := mock.server()
	defer aiSrv.Close()
	tavSrv := fakeTavily()
	defer tavSrv.Close()

	_, _, _, err := Generate(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"},
		websearch.NewClientWithBase("test", tavSrv.URL),
		Spec{Topic: "tema", Count: 2, Web: true}, nil)
	if err != nil {
		t.Fatal(err)
	}

	tools, ok := mock.bodies[0]["tools"].([]any)
	if !ok || len(tools) != 2 {
		t.Fatalf("research request tools = %v, want exactly 2", mock.bodies[0]["tools"])
	}
	names := map[string]bool{}
	for _, tl := range tools {
		def, _ := tl.(map[string]any)["function"].(map[string]any)
		names[fmt.Sprint(def["name"])] = true
	}
	if !names["search_web"] || !names["fetch_url"] {
		t.Errorf("web tools missing: %v", names)
	}
	for _, banned := range []string{"list_files", "read_file", "create_note", "update_note"} {
		if names[banned] {
			t.Errorf("file tool %s offered to research", banned)
		}
	}
	if rf, ok := mock.bodies[2]["response_format"].(map[string]any); !ok || rf["type"] != "json_object" {
		t.Errorf("author request missing json_object response_format: %v", mock.bodies[2]["response_format"])
	}
}

func TestGenerate_ValidationRetry(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.searchThenBrief
	mock.jsonFn = func(w http.ResponseWriter, n int) {
		switch n {
		case 1, 2:
			fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
		case 3, 4:
			fmt.Fprint(w, completionBody(t, questionsJSON(t, 3, 5)))
		}
	}
	aiSrv := mock.server()
	defer aiSrv.Close()
	tavSrv := fakeTavily()
	defer tavSrv.Close()

	var progress []Progress
	qs, _, trace, err := GenerateWithTrace(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"},
		websearch.NewClientWithBase("test", tavSrv.URL),
		Spec{Topic: "tema", Count: 3, Web: true}, func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 3 {
		t.Fatalf("got %d questions, want 3", len(qs))
	}
	if mock.completes != 4 {
		t.Errorf("completes = %d, want 4 (author+review twice)", mock.completes)
	}
	// retried attempts are billed too: research {35,15} + 4×{100,50}
	if trace.TokenUsage != (session.TokenUsage{PromptTokens: 435, CompletionTokens: 215}) {
		t.Errorf("usage = %+v, want {435 215}", trace.TokenUsage)
	}
	var sawValidationError bool
	for _, p := range progress {
		if p.Level == "error" && strings.Contains(p.Message, "Validação falhou") {
			sawValidationError = true
		}
	}
	if !sawValidationError {
		t.Errorf("progress did not explain validation retry: %+v", progress)
	}
}

func TestGenerate_InvalidAuthorTableDoesNotRetryWholeQuiz(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.briefOnly
	mock.jsonFn = func(w http.ResponseWriter, n int) {
		invalidTable := []elements.Element{{
			Type:    elements.TableType,
			Columns: []string{"A"},
			Rows:    [][]string{{"x", "extra"}},
		}}
		valid := questionsJSONWithElements(t, 2, 5, invalidTable)
		if n == 1 || n == 2 {
			fmt.Fprint(w, completionBody(t, valid))
			return
		}
		t.Fatalf("unexpected completion %d", n)
	}
	aiSrv := mock.server()
	defer aiSrv.Close()

	var progress []Progress
	qs, _, _, err := GenerateWithTrace(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"}, nil,
		Spec{Topic: "tema", Count: 2, Web: false}, func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if mock.completes != 2 {
		t.Fatalf("completes = %d, want one author/review pair", mock.completes)
	}
	if len(qs) != 2 || len(qs[0].Elements) != 0 {
		t.Fatalf("invalid author table was not removed: %+v", qs)
	}
	if !hasElementWarning(progress) {
		t.Fatalf("progress did not report the recovered table: %+v", progress)
	}
}

func TestGenerate_InvalidReviewTableKeepsAuthorTable(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.briefOnly
	mock.jsonFn = func(w http.ResponseWriter, n int) {
		validTable := []elements.Element{{
			Type:    elements.TableType,
			Title:   "Dados",
			Columns: []string{"A", "B"},
			Rows:    [][]string{{"1", "2"}},
		}}
		invalidTable := []elements.Element{{
			Type:    elements.TableType,
			Columns: []string{"A", "B"},
			Rows:    [][]string{{"1"}},
		}}
		switch n {
		case 1:
			fmt.Fprint(w, completionBody(t, questionsJSONWithElements(t, 2, 5, validTable)))
		case 2:
			fmt.Fprint(w, completionBody(t, questionsJSONWithElements(t, 2, 5, invalidTable)))
		default:
			t.Fatalf("unexpected completion %d", n)
		}
	}
	aiSrv := mock.server()
	defer aiSrv.Close()

	var progress []Progress
	qs, _, _, err := GenerateWithTrace(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"}, nil,
		Spec{Topic: "tema", Count: 2, Web: false}, func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if mock.completes != 2 {
		t.Fatalf("completes = %d, want one author/review pair", mock.completes)
	}
	if len(qs) != 2 || len(qs[0].Elements) != 1 || qs[0].Elements[0].Title != "Dados" {
		t.Fatalf("author table was not preserved: %+v", qs)
	}
	if !hasElementWarning(progress) {
		t.Fatalf("progress did not report the recovered review table: %+v", progress)
	}
}

func hasElementWarning(progress []Progress) bool {
	for _, p := range progress {
		if p.Level == "warning" && strings.Contains(p.Message, "elemento(s) estruturado(s)") {
			return true
		}
	}
	return false
}

func TestGenerate_ExhaustedRetries(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.searchThenBrief
	mock.jsonFn = func(w http.ResponseWriter, _ int) {
		fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
	}
	aiSrv := mock.server()
	defer aiSrv.Close()
	tavSrv := fakeTavily()
	defer tavSrv.Close()

	_, _, usage, err := Generate(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"},
		websearch.NewClientWithBase("test", tavSrv.URL),
		Spec{Topic: "tema", Count: 3, Web: true}, nil)
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}
	if mock.completes != 6 {
		t.Errorf("completes = %d, want 6 (3 author+review attempts)", mock.completes)
	}
	// usage is reported even when the pipeline fails: research {35,15} + 6×{100,50}
	if usage != (ai.Usage{Prompt: 635, Completion: 315}) {
		t.Errorf("usage = %+v, want {635 315}", usage)
	}
}

func TestGenerate_NoWebSkipsResearch(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = func(w http.ResponseWriter, _ int) {
		t.Error("stream must not be called when Web is false")
	}
	mock.jsonFn = func(w http.ResponseWriter, _ int) {
		fmt.Fprint(w, completionBody(t, questionsJSON(t, 3, 5)))
	}
	aiSrv := mock.server()
	defer aiSrv.Close()

	qs, sources, usage, err := Generate(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"},
		nil,
		Spec{Topic: "fotossíntese", Count: 3, Web: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 3 {
		t.Fatalf("got %d questions, want 3", len(qs))
	}
	if len(sources) != 0 {
		t.Errorf("sources = %d, want 0", len(sources))
	}
	if mock.streams != 0 {
		t.Errorf("streams = %d, want 0 (research must be skipped)", mock.streams)
	}
	if mock.completes != 2 {
		t.Errorf("completes = %d, want 2 (author+review)", mock.completes)
	}
	if usage != (ai.Usage{Prompt: 200, Completion: 100}) {
		t.Errorf("usage = %+v, want author+review {200 100}", usage)
	}
}

func TestValidate(t *testing.T) {
	good := func() []session.Question {
		var qs []session.Question
		if err := json.Unmarshal([]byte(questionsJSON(t, 2, 5)), &struct {
			Questions *[]session.Question `json:"questions"`
		}{&qs}); err != nil {
			t.Fatal(err)
		}
		return qs
	}
	if err := validate(good(), 2); err != nil {
		t.Errorf("valid set rejected: %v", err)
	}
	if err := validate(good(), 3); err == nil {
		t.Error("wrong count accepted")
	}
	qs := good()
	qs[0].Options = qs[0].Options[:4]
	if err := validate(qs, 2); err == nil {
		t.Error("4 options accepted")
	}
	qs = good()
	qs[1].Correct = 5
	if err := validate(qs, 2); err == nil {
		t.Error("correct out of range accepted")
	}
	qs = good()
	qs[0].Options[1] = qs[0].Options[0]
	if err := validate(qs, 2); err == nil {
		t.Error("duplicate option accepted")
	}
	qs = good()
	qs[1].Explanation = "  "
	if err := validate(qs, 2); err == nil {
		t.Error("blank explanation accepted")
	}
}

func TestParseQuestions_RejectsInvalidElements(t *testing.T) {
	content := `{"questions":[{"text":"Q","context":"","elements":[{"type":"table","columns":["A"],"rows":[["x","y"]]}],"options":["a","b","c","d","e"],"correct":0,"explanation":"ok"}]}`
	if _, err := parseQuestions(content); err == nil {
		t.Fatal("invalid element shape should be rejected")
	}
}

// lastUserContent returns the user message of the n-th recorded request.
func lastUserContent(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("request has no messages: %v", body)
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		m, _ := msgs[i].(map[string]any)
		if m != nil && m["role"] == "user" {
			content, _ := m["content"].(string)
			return content
		}
	}
	t.Fatalf("request has no user message: %v", body)
	return ""
}

// TestGenerate_ReviewGetsSourcesNotBrief ensures the review stage receives the
// compact source list instead of the full research brief a second time: the
// brief is the largest prompt in the pipeline and review already trusts the
// author to have used it.
func TestGenerate_ReviewGetsSourcesNotBrief(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.searchThenBrief
	mock.jsonFn = func(w http.ResponseWriter, _ int) {
		fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
	}
	aiSrv := mock.server()
	defer aiSrv.Close()
	tavSrv := fakeTavily()
	defer tavSrv.Close()

	_, _, _, err := Generate(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"},
		websearch.NewClientWithBase("test", tavSrv.URL),
		Spec{Topic: "tema", Count: 2, Web: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// bodies: 0=research round 1, 1=research brief, 2=author, 3=review
	authorContent := lastUserContent(t, mock.bodies[2])
	reviewContent := lastUserContent(t, mock.bodies[3])
	if !strings.Contains(authorContent, "Briefing de pesquisa") {
		t.Errorf("author prompt lost the research brief")
	}
	if strings.Contains(reviewContent, "Briefing de pesquisa") {
		t.Errorf("review prompt still carries the full research brief")
	}
	if !strings.Contains(reviewContent, "Fontes consultadas") {
		t.Errorf("review prompt missing the compact source list")
	}
	if !strings.Contains(reviewContent, "https://exemplo.com/enem-2019") {
		t.Errorf("review source list missing a researched URL")
	}
}

// TestGenerate_ReviewOnlyRetry checks that a failed review re-runs review once
// on the same authored draft instead of restarting the expensive author stage.
func TestGenerate_ReviewOnlyRetry(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.briefOnly
	mock.jsonFn = func(w http.ResponseWriter, n int) {
		switch n {
		case 1: // author: structurally valid set
			fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
		case 2: // first review: unparseable → review-only retry
			fmt.Fprint(w, completionBody(t, "{não é json"))
		case 3: // retried review over the same draft
			fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
		default:
			t.Errorf("unexpected completion %d: author must not restart", n)
		}
	}
	aiSrv := mock.server()
	defer aiSrv.Close()

	var progress []Progress
	qs, _, _, err := GenerateWithTrace(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "m"}, nil,
		Spec{Topic: "tema", Count: 2, Web: false}, func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 {
		t.Fatalf("got %d questions, want 2", len(qs))
	}
	if mock.completes != 3 {
		t.Errorf("completes = %d, want 3 (one author + two reviews)", mock.completes)
	}
	var sawWarning bool
	for _, p := range progress {
		if p.Stage == "review" && p.Level == "warning" && strings.Contains(p.Message, "revisando o mesmo rascunho") {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Errorf("progress did not explain the review-only retry: %+v", progress)
	}
}

// TestGenerate_PrevStemsInAuthorPrompt covers the anti-repetition block.
func TestGenerate_PrevStemsInAuthorPrompt(t *testing.T) {
	newMock := func() (*aiMock, *httptest.Server) {
		mock := &aiMock{}
		mock.streamFn = mock.briefOnly
		mock.jsonFn = func(w http.ResponseWriter, _ int) {
			fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
		}
		return mock, mock.server()
	}

	mock, srv := newMock()
	defer srv.Close()
	_, _, _, err := Generate(context.Background(), ai.New(),
		session.Config{BaseURL: srv.URL, Model: "m"}, nil,
		Spec{Topic: "tema", Count: 2, Web: false, PrevStems: []string{"Questão antiga sobre mitose"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	content := lastUserContent(t, mock.bodies[0])
	if !strings.Contains(content, "NÃO repita") || !strings.Contains(content, "Questão antiga sobre mitose") {
		t.Errorf("author prompt missing the anti-repetition block:\n%s", content)
	}

	mock2, srv2 := newMock()
	defer srv2.Close()
	_, _, _, err = Generate(context.Background(), ai.New(),
		session.Config{BaseURL: srv2.URL, Model: "m"}, nil,
		Spec{Topic: "tema", Count: 2, Web: false}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if content := lastUserContent(t, mock2.bodies[0]); strings.Contains(content, "NÃO repita") {
		t.Errorf("author prompt carries anti-repetition block without PrevStems")
	}
}

// TestGenerate_StageModelOverrides verifies each pipeline stage can target its
// own model (empty override = the session model).
func TestGenerate_StageModelOverrides(t *testing.T) {
	mock := &aiMock{}
	mock.streamFn = mock.searchThenBrief
	mock.jsonFn = func(w http.ResponseWriter, _ int) {
		fmt.Fprint(w, completionBody(t, questionsJSON(t, 2, 5)))
	}
	aiSrv := mock.server()
	defer aiSrv.Close()
	tavSrv := fakeTavily()
	defer tavSrv.Close()

	_, _, _, err := Generate(context.Background(), ai.New(),
		session.Config{BaseURL: aiSrv.URL, Model: "default-model"},
		websearch.NewClientWithBase("test", tavSrv.URL),
		Spec{Topic: "tema", Count: 2, Web: true, AuthorModel: "cheap-author", ReviewModel: "cheap-review"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	modelOf := func(body map[string]any) string {
		model, _ := body["model"].(string)
		return model
	}
	// bodies: 0+1=research streams, 2=author, 3=review
	if got := modelOf(mock.bodies[0]); got != "default-model" {
		t.Errorf("research model = %q, want default-model (no override)", got)
	}
	if got := modelOf(mock.bodies[2]); got != "cheap-author" {
		t.Errorf("author model = %q, want cheap-author", got)
	}
	if got := modelOf(mock.bodies[3]); got != "cheap-review" {
		t.Errorf("review model = %q, want cheap-review", got)
	}
}
