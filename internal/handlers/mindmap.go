package handlers

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/mindmap"
	"learnix/internal/session"
)

var mindMapHeading = regexp.MustCompile(`^(#{1,6})\s+(.+?)\s*$`)

// MindMapPage renders the durable study graph and creates a useful outline
// from note headings the first time a study opens the route.
func (h *Handler) MindMapPage(w http.ResponseWriter, r *http.Request) {
	s, ok := h.studyForMindMap(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	graph, err := h.loadOrCreateMindMap(r, s)
	if err != nil {
		http.Error(w, "erro ao carregar o mapa mental", http.StatusInternalServerError)
		return
	}
	u := auth.UserFromContext(r.Context())
	render(w, r, components.MindMapPage(s.Config.Topic, s.StudyID, graph, u, h.quotaFor(r.Context(), u), h.isAdmin(u), auth.CSRFToken(r, h.sessionSecret)))
}

// MindMapJSON returns the canonical graph used by the browser and by future
// AI integrations.
func (h *Handler) MindMapJSON(w http.ResponseWriter, r *http.Request) {
	s, ok := h.studyForMindMap(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	graph, err := h.loadOrCreateMindMap(r, s)
	if err != nil {
		http.Error(w, "erro ao carregar o mapa mental", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

// MindMapUpdate replaces the graph atomically after validating the request,
// ownership and CSRF token. Partial edits remain pure operations in the domain
// package; this endpoint intentionally keeps one clear persistence boundary.
func (h *Handler) MindMapUpdate(w http.ResponseWriter, r *http.Request) {
	s, ok := h.studyForMindMap(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !h.validMindMapCSRF(r) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "token CSRF inválido"})
		return
	}
	if h.mindMaps == nil {
		http.Error(w, "mapa mental indisponível", http.StatusInternalServerError)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var graph mindmap.Graph
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&graph); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mapa mental inválido: " + err.Error()})
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mapa mental deve conter um único JSON"})
		return
	}
	if err := graph.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mapa mental inválido: " + err.Error()})
		return
	}
	if err := h.mindMaps.Save(r.Context(), s.StudyID, graph); err != nil {
		http.Error(w, "erro ao salvar o mapa mental", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "graph": graph})
}

func (h *Handler) studyForMindMap(r *http.Request) (*session.Session, bool) {
	id, ok := studyIDParam(r)
	if !ok || h.mindMaps == nil {
		return nil, false
	}
	s := h.loadStudy(r, id)
	return s, s != nil
}

func (h *Handler) validMindMapCSRF(r *http.Request) bool {
	want := auth.CSRFToken(r, h.sessionSecret)
	have := r.Header.Get("X-CSRF-Token")
	if want == "" || have == "" || len(want) != len(have) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(have)) == 1
}

func (h *Handler) loadOrCreateMindMap(r *http.Request, s *session.Session) (mindmap.Graph, error) {
	graph, err := h.mindMaps.Load(r.Context(), s.StudyID)
	if err == nil {
		return graph, nil
	}
	if !errors.Is(err, mindmap.ErrNotFound) {
		return mindmap.Graph{}, err
	}
	graph = buildMindMapFromStudy(s, func() []db.File {
		files, _ := h.files.ListByStudy(r.Context(), s.StudyID)
		return files
	}())
	if err := h.mindMaps.Save(r.Context(), s.StudyID, graph); err != nil {
		return mindmap.Graph{}, err
	}
	return graph, nil
}

func buildMindMapFromStudy(s *session.Session, files []db.File) mindmap.Graph {
	topic := clipMindMapText(strings.TrimSpace(s.Config.Topic), mindmap.MaxLabelRunes)
	if topic == "" {
		topic = "Meu estudo"
	}
	nodes := []mindmap.Node{{ID: "root", Label: topic, Metadata: map[string]string{"source": "study"}}}
	for _, file := range files {
		if file.Kind != "note" || len(nodes) >= mindmap.MaxNodes {
			continue
		}
		noteID := "note-" + strconv.FormatInt(file.ID, 10)
		note := mindmap.Node{
			ID:          noteID,
			ParentID:    "root",
			Label:       clipMindMapText(file.Name, mindmap.MaxLabelRunes),
			Description: clipMindMapText(firstParagraph(file.Content), 180),
			Metadata:    map[string]string{"note_id": strconv.FormatInt(file.ID, 10)},
		}
		nodes = append(nodes, note)
		parents := []struct {
			level int
			id    string
		}{}
		for index, line := range strings.Split(file.Content, "\n") {
			match := mindMapHeading.FindStringSubmatch(strings.TrimSpace(line))
			if len(match) != 3 || len(nodes) >= mindmap.MaxNodes {
				continue
			}
			level := len(match[1])
			for len(parents) > 0 && parents[len(parents)-1].level >= level {
				parents = parents[:len(parents)-1]
			}
			parentID := noteID
			if len(parents) > 0 {
				parentID = parents[len(parents)-1].id
			}
			headingID := fmt.Sprintf("%s-h-%d", noteID, index+1)
			nodes = append(nodes, mindmap.Node{
				ID:       headingID,
				ParentID: parentID,
				Label:    clipMindMapText(match[2], mindmap.MaxLabelRunes),
				Metadata: map[string]string{"note_id": strconv.FormatInt(file.ID, 10)},
			})
			parents = append(parents, struct {
				level int
				id    string
			}{level: level, id: headingID})
		}
	}
	graph := mindmap.Graph{RootID: "root", Nodes: nodes}
	if err := graph.Validate(); err != nil {
		return mindmap.Graph{RootID: "root", Nodes: []mindmap.Node{{ID: "root", Label: topic, Metadata: map[string]string{"source": "study"}}}}
	}
	return graph
}

func firstParagraph(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

func clipMindMapText(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}
