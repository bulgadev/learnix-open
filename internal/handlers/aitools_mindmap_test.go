package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"learnix/internal/ai"
	"learnix/internal/mindmap"
	"learnix/internal/session"
)

func TestPersistentMindMapToolsMutateValidatedGraph(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mindmap-tools@test.com", "hunter2!")
	loc := te.createStudy(t, "Fotossíntese", cookie)
	req := httptest.NewRequest("POST", loc+"/chats/1/stream", nil)
	s := &session.Session{StudyID: fid64(t, loc)}
	tools, exec := te.handler.studyTools(req, s)

	for _, name := range []string{"get_mind_map", "add_mind_map_node", "update_mind_map_node", "remove_mind_map_node", "collapse_mind_map_node", "expand_mind_map_node"} {
		if !containsTool(tools, name) {
			t.Fatalf("missing persistent tool %q", name)
		}
	}

	result, _, err := exec("get_mind_map", `{}`)
	if err != nil {
		t.Fatal(err)
	}
	var initial mindmap.Graph
	if err := json.Unmarshal([]byte(result), &initial); err != nil {
		t.Fatalf("decode initial map: %v", err)
	}
	if initial.RootID != "root" {
		t.Fatalf("initial root = %q, want root", initial.RootID)
	}

	result, _, err = exec("add_mind_map_node", `{"id":"energia","parent_id":"root","label":"Energia","description":"Resumo curto"}`)
	if err != nil {
		t.Fatalf("add node: %v", err)
	}
	var added struct {
		NodeID string        `json:"node_id"`
		Graph  mindmap.Graph `json:"graph"`
	}
	if err := json.Unmarshal([]byte(result), &added); err != nil {
		t.Fatalf("decode add result: %v", err)
	}
	if added.NodeID != "energia" || len(added.Graph.Nodes) != 2 {
		t.Fatalf("unexpected add result: %+v", added)
	}

	if _, _, err := exec("add_mind_map_node", `{"parent_id":"no-such-parent","label":"Inválido"}`); err == nil {
		t.Fatal("add under unknown parent should fail")
	}
	if _, _, err := exec("add_mind_map_node", `{"parent_id":"root","label":"Outro","extra":true}`); err == nil {
		t.Fatal("unknown tool argument should fail")
	}

	if _, _, err := exec("update_mind_map_node", `{"id":"energia","label":"Energia química","description":"Forma resumida"}`); err != nil {
		t.Fatalf("update node: %v", err)
	}
	if _, _, err := exec("collapse_mind_map_node", `{"id":"root"}`); err != nil {
		t.Fatalf("collapse root: %v", err)
	}
	collapsed, err := te.mindMaps.Load(testCtx, s.StudyID)
	if err != nil || !collapsed.Nodes[0].Collapsed {
		t.Fatalf("collapsed map was not persisted: %v %+v", err, collapsed)
	}
	if _, _, err := exec("expand_mind_map_node", `{"id":"root"}`); err != nil {
		t.Fatalf("expand root: %v", err)
	}
	if _, _, err := exec("remove_mind_map_node", `{"id":"root"}`); err == nil {
		t.Fatal("removing root should fail")
	}
	if _, _, err := exec("remove_mind_map_node", `{"id":"energia"}`); err != nil {
		t.Fatalf("remove branch: %v", err)
	}
	final, err := te.mindMaps.Load(testCtx, s.StudyID)
	if err != nil || len(final.Nodes) != 1 || strings.TrimSpace(final.Nodes[0].Label) == "" {
		t.Fatalf("unexpected final map: %v %+v", err, final)
	}
}

func containsTool(tools []ai.Tool, name string) bool {
	for _, tool := range tools {
		if tool.Function.Name == name {
			return true
		}
	}
	return false
}
