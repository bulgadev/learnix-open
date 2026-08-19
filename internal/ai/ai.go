package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"learnix/internal/session"
)

type Client struct {
	http *http.Client
}

func New() *Client {
	return &Client{http: &http.Client{Timeout: 3 * time.Minute}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []chatMessage   `json:"messages"`
	Stream         bool            `json:"stream,omitempty"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
	Temperature    float64         `json:"temperature,omitempty"`
	StreamOptions  *streamOptions  `json:"stream_options,omitempty"`
}

func (c *Client) endpoint(cfg session.Config) string {
	return strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
}

func (c *Client) do(ctx context.Context, cfg session.Config, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint(cfg), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("erro da API (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return resp, nil
}

func StudySystemPrompt(topic string) string {
	return "Você é um tutor particular especialista e didático. O aluno está estudando o seguinte conteúdo: \"" + topic + "\".\n" +
		"Forneça material de estudo em português, bem estruturado em Markdown (use títulos ##, listas, **negrito**, exemplos e analogias).\n" +
		"Seja aprofundado porém acessível, focando no que mais cai em provas. Não use títulos de nível 1 (#)."
}

// Stream envia tokens via callback e retorna o conteúdo completo mais o uso
// de tokens reportado pelo provedor (estimado quando ausente).
func (c *Client) Stream(ctx context.Context, cfg session.Config, msgs []session.Message, onToken func(string)) (string, Usage, error) {
	req := chatRequest{Model: cfg.Model, Stream: true, Temperature: 0.7, StreamOptions: &streamOptions{IncludeUsage: true}}
	for _, m := range msgs {
		req.Messages = append(req.Messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	body, _ := json.Marshal(req)

	resp, err := c.do(ctx, cfg, body)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var full strings.Builder
	var usage Usage
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage wireUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if u := chunk.Usage.usage(); u.Total() > 0 {
			usage = u
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			full.WriteString(chunk.Choices[0].Delta.Content)
			onToken(chunk.Choices[0].Delta.Content)
		}
	}
	text := full.String()
	if usage.Total() == 0 {
		chars := 0
		for _, m := range msgs {
			chars += len(m.Content)
		}
		usage = fallbackUsage(chars, text)
	}
	return text, usage, nil
}

// StreamUsage is the session-facing compatibility form used by quiz traces
// and structured-element generation. The provider accounting itself remains
// represented by ai.Usage so quota callers can charge it directly.
func (c *Client) StreamUsage(ctx context.Context, cfg session.Config, msgs []session.Message, onToken func(string)) (string, session.TokenUsage, error) {
	text, usage, err := c.Stream(ctx, cfg, msgs, onToken)
	return text, session.TokenUsage{PromptTokens: usage.Prompt, CompletionTokens: usage.Completion}, err
}

// Complete issues a non-streaming chat completion and returns the assistant
// content plus the token usage reported by the provider (estimated when
// absent). With jsonMode the request asks for a JSON object response.
func (c *Client) Complete(ctx context.Context, cfg session.Config, msgs []Message, temperature float64, jsonMode bool) (string, Usage, error) {
	req := chatRequest{Model: cfg.Model, Temperature: temperature}
	if jsonMode {
		req.ResponseFormat = &responseFormat{Type: "json_object"}
	}
	for _, m := range msgs {
		req.Messages = append(req.Messages, chatMessage{Role: m.Role, Content: m.Content})
	}
	body, _ := json.Marshal(req)
	resp, err := c.do(ctx, cfg, body)
	if err != nil {
		return "", Usage{}, err
	}
	defer resp.Body.Close()

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage wireUsage `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", Usage{}, err
	}
	if len(parsed.Choices) == 0 {
		return "", Usage{}, fmt.Errorf("resposta vazia da API")
	}
	content := parsed.Choices[0].Message.Content
	usage := parsed.Usage.usage()
	if usage.Total() == 0 {
		usage = fallbackUsage(messageChars(msgs), content)
	}
	return content, usage, nil
}

// CompleteUsage is the session-facing compatibility form used by quiz traces.
func (c *Client) CompleteUsage(ctx context.Context, cfg session.Config, msgs []Message, temperature float64, jsonMode bool) (string, session.TokenUsage, error) {
	content, usage, err := c.Complete(ctx, cfg, msgs, temperature, jsonMode)
	return content, session.TokenUsage{PromptTokens: usage.Prompt, CompletionTokens: usage.Completion}, err
}
