package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearch_ParsesAndTruncates(t *testing.T) {
	var gotAuth, gotQuery string
	var gotMax float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path = %q, want /search", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotQuery, _ = req["query"].(string)
		gotMax, _ = req["max_results"].(float64)
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{
			map[string]any{"title": "T1", "url": "https://ex.com/1", "content": strings.Repeat("a", 400)},
			map[string]any{"title": "T2", "url": "https://ex.com/2", "content": "curto"},
		}})
	}))
	defer srv.Close()

	res, err := NewClientWithBase("k123", srv.URL).Search(context.Background(), "fotossintese")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotAuth != "Bearer k123" {
		t.Errorf("Authorization = %q, want Bearer k123", gotAuth)
	}
	if gotQuery != "fotossintese" || gotMax != 5 {
		t.Errorf("payload query=%q max_results=%v", gotQuery, gotMax)
	}
	if len(res) != 2 {
		t.Fatalf("len = %d, want 2", len(res))
	}
	if res[0].Title != "T1" || res[0].URL != "https://ex.com/1" {
		t.Errorf("bad result[0]: %+v", res[0])
	}
	if n := len([]rune(res[0].Snippet)); n != 300 {
		t.Errorf("snippet len = %d, want 300", n)
	}
	if res[1].Snippet != "curto" {
		t.Errorf("short snippet altered: %q", res[1].Snippet)
	}
}

func TestSearch_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := NewClientWithBase("wrong", srv.URL).Search(context.Background(), "x")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}

func TestExtract_ReducesAt6000Runes(t *testing.T) {
	var gotURLs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/extract" {
			t.Errorf("path = %q, want /extract", r.URL.Path)
		}
		var req struct {
			URLs []string `json:"urls"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotURLs = req.URLs
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{
			map[string]any{"raw_content": strings.Repeat("ç", 25000)},
		}})
	}))
	defer srv.Close()

	text, err := NewClientWithBase("k", srv.URL).Extract(context.Background(), "https://ex.com/p")
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(gotURLs) != 1 || gotURLs[0] != "https://ex.com/p" {
		t.Errorf("urls = %v", gotURLs)
	}
	if n := len([]rune(text)); n != ReducedExtractLimitRunes {
		t.Errorf("extract len = %d runes, want %d", n, ReducedExtractLimitRunes)
	}
}

func TestContentReducer_SelectsFocusedBlocksInOriginalOrder(t *testing.T) {
	content := strings.Join([]string{
		"Introdução geral sobre biologia.",
		"Este bloco fala de genética e hereditariedade.",
		"A fotossíntese transforma energia luminosa em energia química.",
		"Na fase clara, a clorofila absorve luz e produz ATP.",
		"Conclusão sobre ecologia e cadeias alimentares.",
	}, "\n\n")

	reducer := ContentReducer{MaxRunes: 150, ChunkRunes: 80}
	got := reducer.Reduce(content, "fotossíntese fase clara ATP")
	if !strings.Contains(got, "A fotossíntese") || !strings.Contains(got, "fase clara") {
		t.Fatalf("focused reduction lost relevant content: %q", got)
	}
	if strings.Contains(got, "genética") && strings.Contains(got, "ecologia") {
		t.Fatalf("focused reduction kept unrelated content: %q", got)
	}
}

func TestExtract_EmptyContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
	}))
	defer srv.Close()

	_, err := NewClientWithBase("k", srv.URL).Extract(context.Background(), "https://ex.com/p")
	if err == nil || !strings.Contains(err.Error(), "sem conteúdo") {
		t.Errorf("expected sem conteúdo error, got %v", err)
	}
}

func TestExtract_Non200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	_, err := NewClientWithBase("k", srv.URL).Extract(context.Background(), "https://ex.com/p")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("expected 500 error, got %v", err)
	}
}
