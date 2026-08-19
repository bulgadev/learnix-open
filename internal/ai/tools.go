package ai

import (
	"bufio"
	"context"
	"encoding/json"
	"sort"
	"strings"

	"learnix/internal/session"
)

// maxToolRounds caps how many consecutive tool-call rounds the model may run
// before we force a final tool-less request for a text answer.
const maxToolRounds = 8

// ToolCall is one function call requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Message is the OpenAI-compatible chat wire message (richer than
// session.Message: it carries tool calls and tool results).
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// Tool describes one function the model may call.
type Tool struct {
	Type     string  `json:"type"`
	Function ToolDef `json:"function"`
}

// ToolDef is the function definition; Parameters is a JSON Schema object.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// Event is a side-channel notification emitted during StreamWithTools:
// "tool" (model invoked a tool), "tool_result" (tool finished) or "note"
// (informational, e.g. capability fallback).
type Event struct {
	Type    string `json:"type"`
	Name    string `json:"name,omitempty"`
	Args    string `json:"args,omitempty"`
	Summary string `json:"summary,omitempty"`
	Text    string `json:"text,omitempty"`
	Usage   *Usage `json:"usage,omitempty"`
}

// ToolEvent is one persisted entry of the tool-call log.
type ToolEvent struct {
	Name    string `json:"name"`
	Args    string `json:"args"`
	Summary string `json:"summary"`
}

// ToolExec runs a tool by name with raw JSON arguments, returning the result
// text fed back to the model plus a short human summary for the UI/log.
type ToolExec func(name, argsJSON string) (result string, summary string, err error)

type toolChatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Stream        bool           `json:"stream"`
	Temperature   float64        `json:"temperature,omitempty"`
	Tools         []Tool         `json:"tools,omitempty"`
	ToolChoice    string         `json:"tool_choice,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

func isToolUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "tool") || strings.Contains(s, "function")
}

func streamOptionsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "stream_options") || strings.Contains(s, "stream options") || strings.Contains(s, "unknown field")
}

// streamRound issues a single streaming chat request and parses the SSE
// stream, accumulating content deltas (forwarded to onToken), per-index
// tool-call fragments and the usage reported in the final chunk (which often
// arrives with empty choices). Returns the round's content, any reassembled
// calls and the round's usage (estimated when the provider reports none).
func (c *Client) streamRound(ctx context.Context, cfg session.Config, msgs []Message, tools []Tool, onToken func(string)) (string, []ToolCall, Usage, error) {
	req := toolChatRequest{Model: cfg.Model, Stream: true, Temperature: 0.7, Messages: msgs, StreamOptions: &streamOptions{IncludeUsage: true}}
	if len(tools) > 0 {
		req.Tools = tools
		req.ToolChoice = "auto"
	}
	body, _ := json.Marshal(req)
	resp, err := c.do(ctx, cfg, body)
	if err != nil && streamOptionsUnsupported(err) {
		req.StreamOptions = nil
		body, _ = json.Marshal(req)
		resp, err = c.do(ctx, cfg, body)
	}
	if err != nil {
		return "", nil, Usage{}, err
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var content strings.Builder
	var usage Usage
	acc := map[int]*ToolCall{}
	var order []int
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
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage wireUsage `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &chunk) != nil {
			continue
		}
		if u := chunk.Usage.usage(); u.Total() > 0 {
			usage = u
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.Content != "" {
			content.WriteString(delta.Content)
			onToken(delta.Content)
		}
		for _, tc := range delta.ToolCalls {
			cur, ok := acc[tc.Index]
			if !ok {
				cur = &ToolCall{Type: "function"}
				acc[tc.Index] = cur
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" {
				cur.Type = tc.Type
			}
			cur.Function.Name += tc.Function.Name
			cur.Function.Arguments += tc.Function.Arguments
		}
	}
	sort.Ints(order)
	calls := make([]ToolCall, 0, len(order))
	for _, idx := range order {
		calls = append(calls, *acc[idx])
	}
	text := content.String()
	if usage.Total() == 0 {
		usage = fallbackUsage(messageChars(msgs), text)
	}
	return text, calls, usage, nil
}

// StreamWithTools streams a chat completion that may invoke tools. Each time
// the model returns tool_calls, exec runs them, the results are appended to the
// conversation and the request is re-issued, up to maxToolRounds rounds; a
// final tool-less request then forces a text answer. full accumulates all
// assistant content across rounds and log records every executed call. usage
// sums the tokens of every round (each round is a separate billed API call).
// If the first request fails with an error suggesting tools are unsupported,
// it falls back to plain Stream.
func (c *Client) StreamWithTools(ctx context.Context, cfg session.Config, msgs []Message, tools []Tool, exec ToolExec, onToken func(string), onEvent func(Event)) (full string, log []ToolEvent, usage Usage, err error) {
	var out strings.Builder
	cur := append([]Message(nil), msgs...)
	rounds := 0
	for {
		var roundTools []Tool
		if rounds < maxToolRounds {
			roundTools = tools
		}
		content, calls, ru, rerr := c.streamRound(ctx, cfg, cur, roundTools, onToken)
		usage = usage.Add(ru)
		if ru.Total() > 0 {
			u := ru
			onEvent(Event{Type: "usage", Usage: &u})
		}
		if rerr != nil {
			if rounds == 0 && len(tools) > 0 && isToolUnsupported(rerr) {
				onEvent(Event{Type: "note", Text: "modelo sem suporte a ferramentas"})
				plain := make([]session.Message, 0, len(cur))
				for _, m := range cur {
					plain = append(plain, session.Message{Role: m.Role, Content: m.Content})
				}
				s, su, serr := c.Stream(ctx, cfg, plain, onToken)
				out.WriteString(s)
				return out.String(), log, usage.Add(su), serr
			}
			return out.String(), log, usage, rerr
		}
		out.WriteString(content)
		if len(calls) == 0 || rounds >= maxToolRounds || len(tools) == 0 || exec == nil {
			return out.String(), log, usage, nil
		}
		cur = append(cur, Message{Role: "assistant", Content: content, ToolCalls: calls})
		for _, call := range calls {
			onEvent(Event{Type: "tool", Name: call.Function.Name, Args: call.Function.Arguments})
			result, summary, eerr := exec(call.Function.Name, call.Function.Arguments)
			if eerr != nil {
				result = "erro: " + eerr.Error()
				summary = "erro"
			}
			onEvent(Event{Type: "tool_result", Name: call.Function.Name, Summary: summary})
			log = append(log, ToolEvent{Name: call.Function.Name, Args: call.Function.Arguments, Summary: summary})
			cur = append(cur, Message{Role: "tool", ToolCallID: call.ID, Name: call.Function.Name, Content: result})
		}
		rounds++
	}
}

// WorkspaceSystemPrompt extends StudySystemPrompt with the file-tool
// instructions, the study's current file index (one "id=N nome — kind"
// line per file, or "(nenhum arquivo ainda)") and the web-search stance.
func WorkspaceSystemPrompt(topic, fileIndex string, webEnabled bool) string {
	web := "Você NÃO tem acesso à internet nesta configuração; não invente fontes."
	if webEnabled {
		web = "Você tem acesso à internet via search_web e fetch_url. Use-os para verificar fatos, buscar fontes confiáveis e fundamentar respostas; sempre baseie afirmações importantes em fontes reais."
	}
	return StudySystemPrompt(topic) + "\n\n" +
		"Você tem acesso a ferramentas para trabalhar com os arquivos do espaço de estudo:\n" +
		"- list_files: lista os arquivos e notas do espaço.\n" +
		"- read_file: lê o conteúdo de uma nota a partir do id.\n" +
		"- create_note: cria uma nova nota (nome e conteúdo em Markdown).\n" +
		"- update_note: atualiza o conteúdo de uma nota existente a partir do id.\n" +
		"- create_table: cria uma tabela estruturada para comparações e dados.\n" +
		"- create_mind_map: cria um mapa mental estruturado para hierarquias de conceitos.\n" +
		"- get_mind_map: lê o mapa mental persistente atual do estudo; use antes de qualquer edição.\n" +
		"- add_mind_map_node: adiciona um nó sob um parent_id existente e explícito.\n" +
		"- update_mind_map_node: atualiza rótulo, resumo, parent_id (reparenting) ou collapsed de um nó existente.\n" +
		"- remove_mind_map_node: remove um nó e sua subárvore; nunca remova a raiz.\n" +
		"- collapse_mind_map_node / expand_mind_map_node: controla a densidade visual de um ramo.\n" +
		"Antes de responder perguntas sobre as anotações do aluno, use read_file para lê-las. " +
		"Quando o aluno pedir para salvar, organizar ou completar material de estudo, crie ou atualize notas conforme apropriado.\n" +
		"Ao trabalhar no mapa persistente, leia o mapa primeiro, preserve a raiz central e use sempre o parent_id de um nó retornado pelo mapa; nunca adicione um segundo root, invente parent_id ou substitua o grafo inteiro sem necessidade. Faça alterações pequenas e valide o resultado.\n" +
		"O mapa deve parecer um mapa mental: núcleo central, branches distribuídos radialmente ou em árvore equilibrada em direções diferentes, conexões visíveis/curvas e espaço entre grupos. Evite parede de cartões, coluna vertical, fila horizontal ou dezenas de nós no mesmo nível. Use rótulos curtos e uma descrição resumida por nó; divida conceitos em branches hierárquicos e recolha ramos densos quando apropriado.\n" +
		"Use create_table e create_mind_map somente quando o elemento trouxer valor de aprendizagem ou for pedido explicitamente; caso contrário, use Markdown comum. Os elementos aceitam somente texto e são renderizados pela aplicação; nunca escreva HTML, CSS ou JavaScript para eles.\n" +
		"Arquivos atuais do espaço:\n" + fileIndex + "\n\n" + web
}
