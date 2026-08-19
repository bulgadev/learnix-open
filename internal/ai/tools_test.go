package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"learnix/internal/session"
)

func nonUsageEvents(events []Event) []Event {
	out := events[:0]
	for _, ev := range events {
		if ev.Type != "usage" {
			out = append(out, ev)
		}
	}
	return out
}

func sse(t *testing.T, payload any) string {
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

func testTools() []Tool {
	return []Tool{{Type: "function", Function: ToolDef{
		Name:        "create_note",
		Description: "cria uma nota",
		Parameters:  map[string]any{"type": "object"},
	}}}
}

func TestStreamWithTools_DirectAnswer(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(t, contentDelta("Oi! ")))
		fmt.Fprint(w, sse(t, contentDelta("Tudo bem?")))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	cfg := session.Config{BaseURL: srv.URL, Model: "m"}
	var tokens string
	var events []Event
	full, log, _, err := c.StreamWithTools(context.Background(), cfg,
		[]Message{{Role: "user", Content: "oi"}}, testTools(),
		func(name, _ string) (string, string, error) {
			t.Errorf("exec must not be called, got %s", name)
			return "", "", nil
		},
		func(tok string) { tokens += tok },
		func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if full != "Oi! Tudo bem?" {
		t.Errorf("full = %q", full)
	}
	if tokens != full {
		t.Errorf("tokens = %q, want %q", tokens, full)
	}
	if len(nonUsageEvents(events)) != 0 || len(log) != 0 {
		t.Errorf("expected no events/log, got %+v / %+v", events, log)
	}
	if requests != 1 {
		t.Errorf("requests = %d, want 1", requests)
	}
}

func TestStreamWithTools_ToolRoundTrip(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	requests := 0
	args := `{"name":"Resumo","content":"x"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		mu.Lock()
		bodies = append(bodies, parsed)
		requests++
		cur := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if cur == 1 {
			fmt.Fprint(w, sse(t, toolDelta(map[string]any{
				"index": 0, "id": "call_1", "type": "function",
				"function": map[string]any{"name": "create_note", "arguments": args},
			})))
			fmt.Fprint(w, sse(t, finishToolCalls()))
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, sse(t, contentDelta("Nota salva!")))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	cfg := session.Config{BaseURL: srv.URL, Model: "m"}
	var execName, execArgs string
	var events []Event
	full, log, _, err := c.StreamWithTools(context.Background(), cfg,
		[]Message{{Role: "user", Content: "salve uma nota"}}, testTools(),
		func(name, a string) (string, string, error) {
			execName, execArgs = name, a
			return "nota criada: id=1 nome=Resumo", "nota criada", nil
		},
		func(string) {},
		func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if execName != "create_note" || execArgs != args {
		t.Errorf("exec got %s / %s", execName, execArgs)
	}
	if full != "Nota salva!" {
		t.Errorf("full = %q", full)
	}
	if len(log) != 1 || log[0].Name != "create_note" || log[0].Args != args || log[0].Summary != "nota criada" {
		t.Errorf("log = %+v", log)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if _, ok := bodies[0]["tools"]; !ok {
		t.Error("first request missing tools")
	}
	if bodies[0]["tool_choice"] != "auto" {
		t.Errorf("tool_choice = %v, want auto", bodies[0]["tool_choice"])
	}
	msgs, _ := bodies[1]["messages"].([]any)
	var sawAssistant, sawTool bool
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] == "assistant" {
			if tcs, ok := mm["tool_calls"].([]any); ok && len(tcs) == 1 {
				sawAssistant = true
			}
		}
		if mm["role"] == "tool" && mm["tool_call_id"] == "call_1" && strings.Contains(fmt.Sprint(mm["content"]), "nota criada") {
			sawTool = true
		}
	}
	if !sawAssistant || !sawTool {
		t.Errorf("second request missing assistant/tool messages: %v", msgs)
	}
	events = nonUsageEvents(events)
	if len(events) != 2 || events[0].Type != "tool" || events[0].Name != "create_note" ||
		events[1].Type != "tool_result" || events[1].Summary != "nota criada" {
		t.Errorf("events = %+v", events)
	}
}

func TestStreamWithTools_SplitToolCallDeltas(t *testing.T) {
	requests := 0
	var gotArgs string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			fmt.Fprint(w, sse(t, toolDelta(map[string]any{
				"index": 0, "id": "call_9", "type": "function",
				"function": map[string]any{"name": "create_note", "arguments": ""},
			})))
			fmt.Fprint(w, sse(t, toolDelta(map[string]any{"index": 0, "function": map[string]any{"arguments": `{"name":"A","con`}})))
			fmt.Fprint(w, sse(t, toolDelta(map[string]any{"index": 0, "function": map[string]any{"arguments": `tent":"B"}`}})))
			fmt.Fprint(w, sse(t, finishToolCalls()))
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, sse(t, contentDelta("ok")))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	cfg := session.Config{BaseURL: srv.URL, Model: "m"}
	full, log, _, err := c.StreamWithTools(context.Background(), cfg,
		[]Message{{Role: "user", Content: "x"}}, testTools(),
		func(_, a string) (string, string, error) {
			gotArgs = a
			return "r", "s", nil
		},
		func(string) {},
		func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if gotArgs != `{"name":"A","content":"B"}` {
		t.Errorf("reassembled args = %q", gotArgs)
	}
	if full != "ok" {
		t.Errorf("full = %q", full)
	}
	if len(log) != 1 {
		t.Errorf("log = %+v", log)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
}

func TestStreamWithTools_LoopCap(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		mu.Lock()
		bodies = append(bodies, parsed)
		requests++
		cur := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(t, toolDelta(map[string]any{
			"index": 0, "id": fmt.Sprintf("call_%d", cur), "type": "function",
			"function": map[string]any{"name": "create_note", "arguments": "{}"},
		})))
		fmt.Fprint(w, sse(t, finishToolCalls()))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	cfg := session.Config{BaseURL: srv.URL, Model: "m"}
	execCount := 0
	_, log, _, err := c.StreamWithTools(context.Background(), cfg,
		[]Message{{Role: "user", Content: "x"}}, testTools(),
		func(_, _ string) (string, string, error) {
			execCount++
			return "r", "s", nil
		},
		func(string) {},
		func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if requests != 9 {
		t.Errorf("requests = %d, want 9", requests)
	}
	if execCount != 8 || len(log) != 8 {
		t.Errorf("execCount = %d, log len = %d, want 8", execCount, len(log))
	}
	if _, ok := bodies[0]["tools"]; !ok {
		t.Error("first request must carry tools")
	}
	if _, ok := bodies[8]["tools"]; ok {
		t.Error("last request must not carry tools")
	}
}

func TestStreamWithTools_ExecError(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		mu.Lock()
		bodies = append(bodies, parsed)
		requests++
		cur := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if cur == 1 {
			fmt.Fprint(w, sse(t, toolDelta(map[string]any{
				"index": 0, "id": "call_e", "type": "function",
				"function": map[string]any{"name": "create_note", "arguments": "{}"},
			})))
			fmt.Fprint(w, sse(t, finishToolCalls()))
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, sse(t, contentDelta("feito")))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	cfg := session.Config{BaseURL: srv.URL, Model: "m"}
	var events []Event
	full, log, _, err := c.StreamWithTools(context.Background(), cfg,
		[]Message{{Role: "user", Content: "x"}}, testTools(),
		func(_, _ string) (string, string, error) {
			return "", "", errors.New("boom")
		},
		func(string) {},
		func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if full != "feito" {
		t.Errorf("full = %q", full)
	}
	if len(log) != 1 || log[0].Summary != "erro" {
		t.Errorf("log = %+v", log)
	}
	var sawErro bool
	msgs, _ := bodies[1]["messages"].([]any)
	for _, m := range msgs {
		mm, _ := m.(map[string]any)
		if mm["role"] == "tool" && mm["content"] == "erro: boom" {
			sawErro = true
		}
	}
	if !sawErro {
		t.Errorf("tool result with erro: boom not sent back: %v", msgs)
	}
	var sawResult bool
	for _, ev := range events {
		if ev.Type == "tool_result" && ev.Summary == "erro" {
			sawResult = true
		}
	}
	if !sawResult {
		t.Errorf("events missing erro tool_result: %+v", events)
	}
}

func TestStreamWithTools_UnsupportedFallback(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		requests++
		if strings.Contains(string(b), `"tools"`) {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"tools is not supported by this model"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(t, contentDelta("texto simples")))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	cfg := session.Config{BaseURL: srv.URL, Model: "m"}
	var events []Event
	full, _, _, err := c.StreamWithTools(context.Background(), cfg,
		[]Message{{Role: "user", Content: "x"}}, testTools(),
		func(_, _ string) (string, string, error) { return "", "", nil },
		func(string) {},
		func(ev Event) { events = append(events, ev) })
	if err != nil {
		t.Fatal(err)
	}
	if full != "texto simples" {
		t.Errorf("full = %q", full)
	}
	if requests != 2 {
		t.Errorf("requests = %d, want 2", requests)
	}
	if len(events) != 1 || events[0].Type != "note" || events[0].Text != "modelo sem suporte a ferramentas" {
		t.Errorf("events = %+v", events)
	}
}

func TestStreamWithTools_UsageSumsAcrossRounds(t *testing.T) {
	requests := 0
	var bodies []map[string]any
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var parsed map[string]any
		_ = json.Unmarshal(b, &parsed)
		mu.Lock()
		bodies = append(bodies, parsed)
		requests++
		cur := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if cur == 1 {
			fmt.Fprint(w, sse(t, toolDelta(map[string]any{
				"index": 0, "id": "call_u", "type": "function",
				"function": map[string]any{"name": "create_note", "arguments": "{}"},
			})))
			fmt.Fprint(w, sse(t, finishToolCalls()))
			fmt.Fprint(w, sse(t, usageChunk(10, 5)))
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		fmt.Fprint(w, sse(t, contentDelta("pronto")))
		fmt.Fprint(w, sse(t, usageChunk(30, 12)))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	cfg := session.Config{BaseURL: srv.URL, Model: "m"}
	full, _, usage, err := c.StreamWithTools(context.Background(), cfg,
		[]Message{{Role: "user", Content: "x"}}, testTools(),
		func(_, _ string) (string, string, error) { return "r", "s", nil },
		func(string) {},
		func(Event) {})
	if err != nil {
		t.Fatal(err)
	}
	if full != "pronto" {
		t.Errorf("full = %q", full)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if usage != (Usage{Prompt: 40, Completion: 17}) {
		t.Errorf("usage = %+v, want the sum of both rounds {40 17}", usage)
	}
	for i, b := range bodies {
		so, ok := b["stream_options"].(map[string]any)
		if !ok || so["include_usage"] != true {
			t.Errorf("request %d stream_options = %v, want include_usage=true", i, b["stream_options"])
		}
	}
}
