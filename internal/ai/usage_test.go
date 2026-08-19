package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"learnix/internal/session"
)

func TestUsageTotalAndAdd(t *testing.T) {
	u := Usage{Prompt: 3, Completion: 4}
	if u.Total() != 7 {
		t.Errorf("Total() = %d, want 7", u.Total())
	}
	sum := u.Add(Usage{Prompt: 10, Completion: 1})
	if sum != (Usage{Prompt: 13, Completion: 5}) || sum.Total() != 18 {
		t.Errorf("Add = %+v", sum)
	}
}

func TestEstimateTokens(t *testing.T) {
	cases := []struct{ chars, want int }{{0, 0}, {1, 1}, {4, 1}, {5, 2}, {8, 2}, {9, 3}}
	for _, tc := range cases {
		if got := estimateTokens(tc.chars); got != tc.want {
			t.Errorf("estimateTokens(%d) = %d, want %d", tc.chars, got, tc.want)
		}
	}
}

func TestComplete_ParsesUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"olá"}}],"usage":{"prompt_tokens":11,"completion_tokens":7}}`)
	}))
	defer srv.Close()

	c := New()
	content, usage, err := c.Complete(context.Background(), session.Config{BaseURL: srv.URL, Model: "m"},
		[]Message{{Role: "user", Content: "oi"}}, 0.5, false)
	if err != nil {
		t.Fatal(err)
	}
	if content != "olá" {
		t.Errorf("content = %q", content)
	}
	if usage != (Usage{Prompt: 11, Completion: 7}) {
		t.Errorf("usage = %+v, want {11 7}", usage)
	}
}

func TestComplete_FallbackUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"resposta gerada"}}]}`)
	}))
	defer srv.Close()

	c := New()
	content, usage, err := c.Complete(context.Background(), session.Config{BaseURL: srv.URL, Model: "m"},
		[]Message{{Role: "user", Content: "uma pergunta qualquer"}}, 0.5, false)
	if err != nil {
		t.Fatal(err)
	}
	if content == "" {
		t.Fatal("empty content")
	}
	if usage.Prompt <= 0 || usage.Completion <= 0 {
		t.Errorf("fallback usage = %+v, want both > 0", usage)
	}
	if want := (len("uma pergunta qualquer") + 3) / 4; usage.Prompt != want {
		t.Errorf("prompt = %d, want %d", usage.Prompt, want)
	}
	if want := (len("resposta gerada") + 3) / 4; usage.Completion != want {
		t.Errorf("completion = %d, want %d", usage.Completion, want)
	}
}

func usageChunk(prompt, completion int) any {
	return map[string]any{"choices": []any{}, "usage": map[string]any{"prompt_tokens": prompt, "completion_tokens": completion}}
}

func TestStream_ParsesUsageAndRequestsIt(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(t, contentDelta("Olá ")))
		fmt.Fprint(w, sse(t, contentDelta("mundo")))
		fmt.Fprint(w, sse(t, usageChunk(21, 9)))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	full, usage, err := c.Stream(context.Background(), session.Config{BaseURL: srv.URL, Model: "m"},
		[]session.Message{{Role: "user", Content: "oi"}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if full != "Olá mundo" {
		t.Errorf("full = %q", full)
	}
	if usage != (Usage{Prompt: 21, Completion: 9}) {
		t.Errorf("usage = %+v, want {21 9}", usage)
	}
	so, ok := body["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Errorf("stream_options = %v, want include_usage=true", body["stream_options"])
	}
}

func TestStream_FallbackUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, sse(t, contentDelta("resposta sem uso")))
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	c := New()
	full, usage, err := c.Stream(context.Background(), session.Config{BaseURL: srv.URL, Model: "m"},
		[]session.Message{{Role: "user", Content: "pergunta de teste"}}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if full == "" {
		t.Fatal("empty content")
	}
	if usage.Prompt <= 0 || usage.Completion <= 0 {
		t.Errorf("fallback usage = %+v, want both > 0", usage)
	}
}
