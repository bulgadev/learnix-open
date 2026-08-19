// Package websearch gives the AI internet research via the Tavily API.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Result is one search hit.
type Result struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// Client talks to the Tavily search/extract endpoints.
type Client struct {
	apiKey  string
	http    *http.Client
	baseURL string
}

// NewClient returns a Client for the public Tavily API.
func NewClient(apiKey string) *Client {
	return NewClientWithBase(apiKey, "https://api.tavily.com")
}

// NewClientWithBase is NewClient with an overridable base URL (for tests).
func NewClientWithBase(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
		baseURL: baseURL,
	}
}

// post sends a JSON payload to path with the bearer key and decodes the
// JSON response into out. Any non-200 status becomes an error.
func (c *Client) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("tavily %s: status %d: %s", path, resp.StatusCode, bytes.TrimSpace(b))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// truncateRunes shortens s to at most n runes.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// Search runs a web search and returns up to 5 results, each with the
// content snippet truncated to 300 runes.
func (c *Client) Search(ctx context.Context, query string) ([]Result, error) {
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	err := c.post(ctx, "/search", map[string]any{
		"query":          query,
		"max_results":    5,
		"include_answer": false,
	}, &out)
	if err != nil {
		return nil, err
	}
	res := make([]Result, 0, len(out.Results))
	for _, r := range out.Results {
		res = append(res, Result{Title: r.Title, URL: r.URL, Snippet: truncateRunes(r.Content, 300)})
	}
	return res, nil
}

// Extract fetches readable content and returns a bounded deterministic excerpt.
func (c *Client) Extract(ctx context.Context, url string) (string, error) {
	return c.ExtractFocused(ctx, url, "")
}

// ExtractFocused fetches readable content and keeps blocks relevant to focus.
// The full page never leaves this client, so large pages do not enter the
// model's tool history.
func (c *Client) ExtractFocused(ctx context.Context, url, focus string) (string, error) {
	var out struct {
		Results []struct {
			RawContent string `json:"raw_content"`
		} `json:"results"`
	}
	if err := c.post(ctx, "/extract", map[string]any{"urls": []string{url}}, &out); err != nil {
		return "", err
	}
	if len(out.Results) == 0 || out.Results[0].RawContent == "" {
		return "", fmt.Errorf("sem conteúdo")
	}
	raw := truncateRunes(out.Results[0].RawContent, RawExtractLimitRunes)
	return DefaultContentReducer.Reduce(raw, focus), nil
}
