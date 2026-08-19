package handlers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"learnix/internal/ai"
	"learnix/internal/db"
	"learnix/internal/elements"
	"learnix/internal/mindmap"
	"learnix/internal/session"
	"learnix/internal/websearch"
)

// studyTools returns the file tools available to the AI in this study plus an
// exec closure bound to the request context, the study and the file repo.
func (h *Handler) studyTools(r *http.Request, s *session.Session) ([]ai.Tool, ai.ToolExec) {
	tools, exec, _ := h.studyToolsWithElements(r, s)
	return tools, exec
}

func (h *Handler) studyToolsWithElements(r *http.Request, s *session.Session) ([]ai.Tool, ai.ToolExec, *[]elements.Element) {
	tools := []ai.Tool{
		{Type: "function", Function: ai.ToolDef{
			Name:        "list_files",
			Description: "Lista os arquivos e notas do espaço de estudo.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "read_file",
			Description: "Lê o conteúdo de uma nota do espaço de estudo.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id": map[string]any{"type": "integer", "description": "ID do arquivo"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "create_note",
			Description: "Cria uma nova nota em Markdown no espaço de estudo.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":      map[string]any{"type": "string", "description": "Nome da nota"},
					"content":   map[string]any{"type": "string", "description": "Conteúdo da nota em Markdown"},
					"parent_id": map[string]any{"type": "integer", "description": "ID da pasta pai (opcional, 0 = raiz)"},
				},
				"required": []string{"name", "content"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "update_note",
			Description: "Atualiza o conteúdo de uma nota existente.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":      map[string]any{"type": "integer", "description": "ID da nota"},
					"content": map[string]any{"type": "string", "description": "Novo conteúdo da nota em Markdown"},
				},
				"required": []string{"id", "content"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "create_table",
			Description: "Cria uma tabela estruturada e legível para dados comparativos ou de apoio ao estudo.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":   map[string]any{"type": "string"},
					"caption": map[string]any{"type": "string"},
					"columns": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
					"rows":    map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
				},
				"required": []string{"columns", "rows"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "create_mind_map",
			Description: "Cria um mapa mental interativo como uma árvore de conceitos conectados.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"title":   map[string]any{"type": "string"},
					"root_id": map[string]any{"type": "string"},
					"nodes": map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{
						"id": map[string]any{"type": "string"}, "parent_id": map[string]any{"type": "string"},
						"label": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
					}, "required": []string{"id", "label"}}},
				},
				"required": []string{"nodes"},
			},
		}},
	}
	if h.mindMaps != nil {
		tools = append(tools, persistentMindMapTools()...)
	}
	artifacts := &[]elements.Element{}

	ctx := r.Context()
	studyID := s.StudyID
	files := h.files

	exec := func(name, argsJSON string) (string, string, error) {
		switch name {
		case "get_mind_map":
			var args struct{}
			if err := decodeToolArgs(argsJSON, &args); err != nil {
				return "", "", fmt.Errorf("parâmetros inválidos para get_mind_map")
			}
			graph, err := h.loadOrCreateMindMap(r, s)
			if err != nil {
				return "", "", err
			}
			return encodeMindMapToolResult(graph)

		case "add_mind_map_node":
			var args struct {
				ID          string            `json:"id"`
				ParentID    string            `json:"parent_id"`
				Label       string            `json:"label"`
				Description string            `json:"description"`
				Metadata    map[string]string `json:"metadata"`
			}
			if err := decodeToolArgs(argsJSON, &args); err != nil || strings.TrimSpace(args.ParentID) == "" || strings.TrimSpace(args.Label) == "" {
				return "", "", fmt.Errorf("parâmetros inválidos: parent_id e label são obrigatórios")
			}
			if args.ID == "" {
				args.ID = newMindMapNodeID()
			}
			var added mindmap.Node
			graph, err := h.mutateMindMap(r, s, func(graph mindmap.Graph) (mindmap.Graph, error) {
				added = mindmap.Node{ID: args.ID, ParentID: args.ParentID, Label: strings.TrimSpace(args.Label), Description: strings.TrimSpace(args.Description), Metadata: args.Metadata}
				return graph.AddNode(added)
			})
			if err != nil {
				return "", "", err
			}
			return encodeMindMapMutationResult(graph, "nó adicionado", added.ID)

		case "update_mind_map_node":
			var args struct {
				ID          string  `json:"id"`
				Label       *string `json:"label"`
				Description *string `json:"description"`
				ParentID    *string `json:"parent_id"`
				Collapsed   *bool   `json:"collapsed"`
			}
			if err := decodeToolArgs(argsJSON, &args); err != nil || strings.TrimSpace(args.ID) == "" {
				return "", "", fmt.Errorf("parâmetro id inválido")
			}
			var updated mindmap.Node
			graph, err := h.mutateMindMap(r, s, func(graph mindmap.Graph) (mindmap.Graph, error) {
				node, nodeErr := graph.Node(args.ID)
				if nodeErr != nil {
					return mindmap.Graph{}, nodeErr
				}
				if args.Label != nil {
					node.Label = strings.TrimSpace(*args.Label)
				}
				if args.Description != nil {
					node.Description = strings.TrimSpace(*args.Description)
				}
				if args.ParentID != nil {
					node.ParentID = strings.TrimSpace(*args.ParentID)
				}
				if args.Collapsed != nil {
					node.Collapsed = *args.Collapsed
				}
				updated = node
				return graph.UpdateNode(node)
			})
			if err != nil {
				return "", "", err
			}
			return encodeMindMapMutationResult(graph, "nó atualizado", updated.ID)

		case "remove_mind_map_node":
			var args struct {
				ID string `json:"id"`
			}
			if err := decodeToolArgs(argsJSON, &args); err != nil || strings.TrimSpace(args.ID) == "" {
				return "", "", fmt.Errorf("parâmetro id inválido")
			}
			graph, err := h.mutateMindMap(r, s, func(graph mindmap.Graph) (mindmap.Graph, error) {
				return graph.RemoveNode(args.ID)
			})
			if err != nil {
				return "", "", err
			}
			return encodeMindMapMutationResult(graph, "subárvore removida", args.ID)

		case "collapse_mind_map_node", "expand_mind_map_node":
			var args struct {
				ID string `json:"id"`
			}
			if err := decodeToolArgs(argsJSON, &args); err != nil || strings.TrimSpace(args.ID) == "" {
				return "", "", fmt.Errorf("parâmetro id inválido")
			}
			collapsed := name == "collapse_mind_map_node"
			graph, err := h.mutateMindMap(r, s, func(graph mindmap.Graph) (mindmap.Graph, error) {
				if collapsed {
					return graph.CollapseNode(args.ID)
				}
				return graph.ExpandNode(args.ID)
			})
			if err != nil {
				return "", "", err
			}
			label := "ramo expandido"
			if collapsed {
				label = "ramo recolhido"
			}
			return encodeMindMapMutationResult(graph, label, args.ID)

		case "create_table":
			var e elements.Element
			if err := json.Unmarshal([]byte(argsJSON), &e); err != nil {
				return "", "", fmt.Errorf("parâmetros inválidos para create_table")
			}
			e.Type = elements.TableType
			if err := e.Validate(); err != nil {
				return "", "", err
			}
			if len(*artifacts) >= elements.MaxElements {
				return "", "", fmt.Errorf("limite de elementos atingido")
			}
			*artifacts = append(*artifacts, e)
			return fmt.Sprintf(`{"type":"table","index":%d}`, len(*artifacts)), "tabela criada", nil

		case "create_mind_map":
			var e elements.Element
			if err := json.Unmarshal([]byte(argsJSON), &e); err != nil {
				return "", "", fmt.Errorf("parâmetros inválidos para create_mind_map")
			}
			e.Type = elements.MindMapType
			if err := e.Validate(); err != nil {
				return "", "", err
			}
			if len(*artifacts) >= elements.MaxElements {
				return "", "", fmt.Errorf("limite de elementos atingido")
			}
			*artifacts = append(*artifacts, e)
			return fmt.Sprintf(`{"type":"mind_map","index":%d}`, len(*artifacts)), "mapa mental criado", nil

		case "list_files":
			fs, err := files.ListByStudy(ctx, studyID)
			if err != nil {
				return "", "", err
			}
			type item struct {
				ID       int64  `json:"id"`
				Name     string `json:"name"`
				Kind     string `json:"kind"`
				ParentID int64  `json:"parent_id"`
			}
			out := make([]item, 0, len(fs))
			for _, f := range fs {
				out = append(out, item{ID: f.ID, Name: f.Name, Kind: f.Kind, ParentID: f.ParentID})
			}
			b, err := json.Marshal(out)
			if err != nil {
				return "", "", err
			}
			return string(b), fmt.Sprintf("%d arquivos", len(out)), nil

		case "read_file":
			var args struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.ID == 0 {
				return "", "", fmt.Errorf("parâmetro id inválido")
			}
			f, err := files.Get(ctx, args.ID)
			if err != nil {
				return "", "", err
			}
			if f == nil || f.StudyID != studyID {
				return "erro: arquivo não encontrado", "não encontrado", nil
			}
			if f.Kind == "image" {
				return fmt.Sprintf("arquivo de imagem: %s (%d bytes)", f.Name, f.Size), "imagem", nil
			}
			if f.Kind != "note" {
				return "erro: arquivo não encontrado", "não encontrado", nil
			}
			return f.Content, "lido", nil

		case "create_note":
			var args struct {
				Name     string `json:"name"`
				Content  string `json:"content"`
				ParentID int64  `json:"parent_id"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Name == "" {
				return "", "", fmt.Errorf("parâmetros inválidos para create_note")
			}
			f := &db.File{StudyID: studyID, ParentID: args.ParentID, Name: args.Name, Kind: "note", Content: args.Content}
			if err := files.CreateAuthored(ctx, f, "ai", "criado pela IA"); err != nil {
				return "", "", err
			}
			return fmt.Sprintf("nota criada: id=%d nome=%s", f.ID, f.Name), "nota criada", nil

		case "update_note":
			var args struct {
				ID      int64  `json:"id"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.ID == 0 {
				return "", "", fmt.Errorf("parâmetros inválidos para update_note")
			}
			if err := files.UpdateContent(ctx, args.ID, studyID, args.Content, "ai", "editado pela IA"); err != nil {
				return "", "", err
			}
			return fmt.Sprintf("nota atualizada: id=%d", args.ID), "nota atualizada", nil
		}
		return "", "", fmt.Errorf("ferramenta desconhecida: %s", name)
	}

	return tools, exec, artifacts
}

var mindMapNodeSequence uint64

func persistentMindMapTools() []ai.Tool {
	return []ai.Tool{
		{Type: "function", Function: ai.ToolDef{
			Name:        "get_mind_map",
			Description: "Lê o mapa mental persistente do estudo, incluindo raiz, parent_id, rótulos, descrições e estado collapsed.",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "add_mind_map_node",
			Description: "Adiciona um nó ao mapa persistente. parent_id é obrigatório: escolha um nó existente retornado por get_mind_map; nunca crie outro root.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "description": "ID estável opcional; se omitido, a aplicação gera um ID seguro"},
					"parent_id":   map[string]any{"type": "string", "description": "ID do nó pai existente; obrigatório"},
					"label":       map[string]any{"type": "string", "description": "Rótulo curto do conceito"},
					"description": map[string]any{"type": "string", "description": "Resumo opcional, sem texto longo"},
					"metadata":    map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "Associações curtas e seguras, como note_id"},
				},
				"required": []string{"parent_id", "label"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "update_mind_map_node",
			Description: "Atualiza um nó persistente existente. Informe parent_id somente para mover o nó; a raiz central não pode ser movida.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":          map[string]any{"type": "string", "description": "ID do nó existente"},
					"label":       map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
					"parent_id":   map[string]any{"type": "string", "description": "Novo pai existente, apenas para reparentear"},
					"collapsed":   map[string]any{"type": "boolean"},
				},
				"required": []string{"id"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "remove_mind_map_node",
			Description: "Remove um nó e toda a sua subárvore do mapa persistente. Nunca remove a raiz; confirme que o id é o ramo pretendido.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"id": map[string]any{"type": "string", "description": "ID do nó/ramo a remover"}},
				"required":   []string{"id"},
			},
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "collapse_mind_map_node",
			Description: "Recolhe os descendentes de um nó no mapa persistente para reduzir a densidade visual.",
			Parameters:  mindMapNodeIDParameters(),
		}},
		{Type: "function", Function: ai.ToolDef{
			Name:        "expand_mind_map_node",
			Description: "Expande os descendentes de um nó recolhido no mapa persistente.",
			Parameters:  mindMapNodeIDParameters(),
		}},
	}
}

func mindMapNodeIDParameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{"id": map[string]any{"type": "string", "description": "ID do nó existente"}},
		"required":   []string{"id"},
	}
}

func decodeToolArgs(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != nil {
		if err == io.EOF {
			return nil
		}
		return fmt.Errorf("argumentos JSON inválidos: %w", err)
	}
	return fmt.Errorf("argumentos JSON devem conter um único objeto")
}

func newMindMapNodeID() string {
	return fmt.Sprintf("ai-node-%d-%d", time.Now().UnixNano(), atomic.AddUint64(&mindMapNodeSequence, 1))
}

func (h *Handler) mutateMindMap(r *http.Request, s *session.Session, mutate func(mindmap.Graph) (mindmap.Graph, error)) (mindmap.Graph, error) {
	graph, err := h.loadOrCreateMindMap(r, s)
	if err != nil {
		return mindmap.Graph{}, err
	}
	updated, err := mutate(graph)
	if err != nil {
		return mindmap.Graph{}, err
	}
	if err := h.mindMaps.Save(r.Context(), s.StudyID, updated); err != nil {
		return mindmap.Graph{}, err
	}
	return updated, nil
}

func encodeMindMapToolResult(graph mindmap.Graph) (string, string, error) {
	b, err := mindmap.Encode(graph)
	if err != nil {
		return "", "", err
	}
	return string(b), fmt.Sprintf("mapa lido (%d nós)", len(graph.Nodes)), nil
}

func encodeMindMapMutationResult(graph mindmap.Graph, summary, nodeID string) (string, string, error) {
	b, err := mindmap.Encode(graph)
	if err != nil {
		return "", "", err
	}
	return fmt.Sprintf(`{"node_id":%q,"graph":%s}`, nodeID, b), summary, nil
}

func isPersistentMindMapTool(name string) bool {
	switch name {
	case "get_mind_map", "add_mind_map_node", "update_mind_map_node", "remove_mind_map_node", "collapse_mind_map_node", "expand_mind_map_node":
		return true
	default:
		return false
	}
}

// allTools returns the file tools plus, when web is enabled and a Tavily key
// is configured, the web tools (search_web, fetch_url). The returned
// collector accumulates every search result the model fetches so the caller
// can persist citations.
func (h *Handler) allTools(r *http.Request, s *session.Session, web bool) ([]ai.Tool, ai.ToolExec, *[]websearch.Result) {
	tools, exec, sources, _ := h.allToolsWithElements(r, s, web)
	return tools, exec, sources
}

func (h *Handler) allToolsWithElements(r *http.Request, s *session.Session, web bool) ([]ai.Tool, ai.ToolExec, *[]websearch.Result, *[]elements.Element) {
	tools, fileExec, artifacts := h.studyToolsWithElements(r, s)
	sources := &[]websearch.Result{}
	if !web || h.TavilyKey == "" {
		return tools, fileExec, sources, artifacts
	}

	client := websearch.NewClient(h.TavilyKey)
	if h.tavilyBase != "" {
		client = websearch.NewClientWithBase(h.TavilyKey, h.tavilyBase)
	}
	ctx := r.Context()

	tools = append(tools,
		ai.Tool{Type: "function", Function: ai.ToolDef{
			Name:        "search_web",
			Description: "Pesquisa na internet e retorna resultados com título, URL e trecho do conteúdo.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "Termos da pesquisa"},
				},
				"required": []string{"query"},
			},
		}},
		ai.Tool{Type: "function", Function: ai.ToolDef{
			Name:        "fetch_url",
			Description: "Lê o conteúdo de uma página web a partir da URL.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"url": map[string]any{"type": "string", "description": "URL da página"},
				},
				"required": []string{"url"},
			},
		}},
	)

	exec := func(name, argsJSON string) (string, string, error) {
		switch name {
		case "search_web":
			var args struct {
				Query string `json:"query"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.Query == "" {
				return "", "", fmt.Errorf("parâmetro query inválido")
			}
			res, err := client.Search(ctx, args.Query)
			if err != nil {
				return "", "", err
			}
			*sources = append(*sources, res...)
			b, err := json.Marshal(res)
			if err != nil {
				return "", "", err
			}
			return string(b), fmt.Sprintf("pesquisou: %s (%d resultados)", args.Query, len(res)), nil

		case "fetch_url":
			var args struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(argsJSON), &args); err != nil || args.URL == "" {
				return "", "", fmt.Errorf("parâmetro url inválido")
			}
			host := args.URL
			if u, perr := url.Parse(args.URL); perr == nil && u.Host != "" {
				host = u.Host
			}
			text, err := client.Extract(ctx, args.URL)
			if err != nil {
				return "", "", err
			}
			return text, "leu: " + host, nil
		}
		return fileExec(name, argsJSON)
	}

	return tools, exec, sources, artifacts
}
