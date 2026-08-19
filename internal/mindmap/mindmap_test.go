package mindmap

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func validGraph() Graph {
	return Graph{
		RootID: "root",
		Nodes: []Node{
			{ID: "root", Label: "Fotossíntese", Metadata: map[string]string{"app_id": "study"}},
			{ID: "light", ParentID: "root", Label: "Fase clara", Description: "Produz ATP", Metadata: map[string]string{"note_id": "note-1"}},
			{ID: "dark", ParentID: "root", Label: "Ciclo de Calvin"},
			{ID: "atp", ParentID: "light", Label: "ATP"},
		},
	}
}

func TestGraphValidateRejectsEmptyAndUnsafeData(t *testing.T) {
	tests := []struct {
		name  string
		graph Graph
		want  string
	}{
		{name: "empty", graph: Graph{}, want: "cannot be empty"},
		{name: "blank label", graph: Graph{RootID: "root", Nodes: []Node{{ID: "root", Label: " "}}}, want: "label cannot be blank"},
		{name: "unsafe id", graph: Graph{RootID: "root/x", Nodes: []Node{{ID: "root/x", Label: "Raiz"}}}, want: "unsafe character"},
		{name: "control label", graph: Graph{RootID: "root", Nodes: []Node{{ID: "root", Label: "Raiz\nX"}}}, want: "control character"},
		{name: "long label", graph: Graph{RootID: "root", Nodes: []Node{{ID: "root", Label: strings.Repeat("a", MaxLabelRunes+1)}}}, want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.graph.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestGraphValidateRejectsRootsParentsCyclesAndDisconnectedNodes(t *testing.T) {
	tests := []struct {
		name  string
		graph Graph
		want  string
	}{
		{
			name: "two roots",
			graph: Graph{RootID: "a", Nodes: []Node{
				{ID: "a", Label: "A"}, {ID: "b", Label: "B"},
			}},
			want: "exactly one root",
		},
		{
			name: "missing parent",
			graph: Graph{RootID: "root", Nodes: []Node{
				{ID: "root", Label: "Root"}, {ID: "child", ParentID: "missing", Label: "Child"},
			}},
			want: "missing parent",
		},
		{
			name: "cycle",
			graph: Graph{RootID: "root", Nodes: []Node{
				{ID: "root", Label: "Root"}, {ID: "a", ParentID: "b", Label: "A"}, {ID: "b", ParentID: "a", Label: "B"},
			}},
			want: "cycle",
		},
		{
			name: "disconnected",
			graph: Graph{RootID: "root", Nodes: []Node{
				{ID: "root", Label: "Root"}, {ID: "a", ParentID: "root", Label: "A"}, {ID: "b", ParentID: "b", Label: "B"},
			}},
			want: "cycle",
		},
		{
			name: "root id points at child",
			graph: Graph{RootID: "child", Nodes: []Node{
				{ID: "root", Label: "Root"}, {ID: "child", ParentID: "root", Label: "Child"},
			}},
			want: "must not have a parent",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.graph.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestGraphOperationsArePure(t *testing.T) {
	original := validGraph()
	collapsed, err := original.CollapseNode("light")
	if err != nil {
		t.Fatal(err)
	}
	if original.Nodes[1].Collapsed {
		t.Fatal("CollapseNode mutated the original graph")
	}
	if !collapsed.Nodes[1].Collapsed {
		t.Fatal("CollapseNode did not collapse the node")
	}

	added, err := original.AddNode(Node{ID: "chlorophyll", ParentID: "light", Label: "Clorofila"})
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Nodes) != 4 || len(added.Nodes) != 5 {
		t.Fatalf("node counts original=%d added=%d", len(original.Nodes), len(added.Nodes))
	}

	updated, err := original.UpdateNode(Node{ID: "dark", ParentID: "root", Label: "Ciclo escuro", Collapsed: true})
	if err != nil {
		t.Fatal(err)
	}
	if original.Nodes[2].Label == updated.Nodes[2].Label || !updated.Nodes[2].Collapsed {
		t.Fatal("UpdateNode result is incorrect or original was mutated")
	}

	expanded, err := collapsed.ExpandNode("light")
	if err != nil {
		t.Fatal(err)
	}
	if expanded.Nodes[1].Collapsed {
		t.Fatal("ExpandNode did not expand the node")
	}
}

func TestGraphRemoveNodeRemovesEntireSubtree(t *testing.T) {
	result, err := validGraph().RemoveNode("light")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("RemoveNode left %d nodes, want 2", len(result.Nodes))
	}
	if _, err := result.Node("atp"); err == nil {
		t.Fatal("descendant atp survived subtree removal")
	}
	if _, err := result.Node("dark"); err != nil {
		t.Fatalf("sibling was removed: %v", err)
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
	if _, err := validGraph().RemoveNode("root"); err == nil {
		t.Fatal("expected root removal error")
	}
}

func TestGraphOutlineHonorsCollapseAndIsDeterministic(t *testing.T) {
	graph := validGraph()
	items, err := graph.Outline()
	if err != nil {
		t.Fatal(err)
	}
	wantIDs := []string{"root", "dark", "light", "atp"}
	gotIDs := make([]string, len(items))
	for i, item := range items {
		gotIDs[i] = item.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("outline ids = %v, want %v", gotIDs, wantIDs)
	}

	collapsed, err := graph.CollapseNode("light")
	if err != nil {
		t.Fatal(err)
	}
	items, err = collapsed.Outline()
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.ID == "atp" {
			t.Fatal("collapsed descendant appeared in outline")
		}
	}
	text, err := collapsed.OutlineText()
	if err != nil {
		t.Fatal(err)
	}
	if want := "- Fotossíntese\n  - Ciclo de Calvin\n  - Fase clara"; text != want {
		t.Fatalf("outline text = %q, want %q", text, want)
	}
}

func TestGraphJSONRoundTripIsStable(t *testing.T) {
	graph := validGraph()
	first, err := Encode(graph)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(first)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("JSON changed after round trip:\n%s\n%s", first, second)
	}
	want := `{"root_id":"root","nodes":[{"id":"root","label":"Fotossíntese","metadata":{"app_id":"study"},"collapsed":false},{"id":"dark","parent_id":"root","label":"Ciclo de Calvin","collapsed":false},{"id":"light","parent_id":"root","label":"Fase clara","description":"Produz ATP","metadata":{"note_id":"note-1"},"collapsed":false},{"id":"atp","parent_id":"light","label":"ATP","collapsed":false}]}`
	if string(first) != want {
		t.Fatalf("JSON = %s, want %s", first, want)
	}

	var decodedWithJSON Graph
	if err := json.Unmarshal(first, &decodedWithJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, decodedWithJSON) {
		t.Fatal("encoding/json and Decode disagree")
	}
}

func TestDecodeRejectsEmptyInvalidAndUnknownJSON(t *testing.T) {
	for _, raw := range []string{"", "   ", `{"root_id":"root","nodes":[]}`, `{"root_id":"root","nodes":[{"id":"root","label":"Root"}],"extra":true}`} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Errorf("Decode(%q) succeeded, want error", raw)
		}
	}
}

func TestMemoryRepositoryPersistsPerStudyAndCopiesState(t *testing.T) {
	repo := NewMemoryRepository()
	ctx := context.Background()
	graph := validGraph()
	if err := repo.Save(ctx, 7, graph); err != nil {
		t.Fatal(err)
	}
	loaded, err := repo.Load(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Nodes[0].Label = "alterado fora do repo"
	loaded.Nodes[0].Metadata["app_id"] = "alterado"
	again, err := repo.Load(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if again.Nodes[0].Label != "Fotossíntese" || again.Nodes[0].Metadata["app_id"] != "study" {
		t.Fatal("repository returned mutable internal state")
	}
	if _, err := repo.Load(ctx, 8); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing graph error = %v, want ErrNotFound", err)
	}
	if err := repo.Save(ctx, 0, graph); !errors.Is(err, ErrInvalidStudyID) {
		t.Fatalf("invalid study error = %v, want ErrInvalidStudyID", err)
	}
	if err := repo.Save(ctx, 9, Graph{}); err == nil {
		t.Fatal("repository accepted invalid graph")
	}
}

func TestRepositoryHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repo := NewMemoryRepository()
	if _, err := repo.Load(ctx, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load error = %v, want context.Canceled", err)
	}
	if err := repo.Save(ctx, 1, validGraph()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save error = %v, want context.Canceled", err)
	}
}
