package elements

import (
	"encoding/json"
	"testing"
)

func validTable() Element {
	return Element{Type: TableType, Title: "Estados", Columns: []string{"Substância", "Estado"}, Rows: [][]string{{"A", "Sólido"}}}
}

func validMap() Element {
	return Element{Type: MindMapType, RootID: "root", Nodes: []MindMapNode{
		{ID: "root", Label: "Fotossíntese"},
		{ID: "light", ParentID: "root", Label: "Fase clara"},
		{ID: "dark", ParentID: "root", Label: "Ciclo de Calvin"},
	}}
}

func TestValidateValidElements(t *testing.T) {
	for _, e := range []Element{validTable(), validMap()} {
		if err := e.Validate(); err != nil {
			t.Fatalf("valid element rejected: %v", err)
		}
	}
}

func TestValidateTableShape(t *testing.T) {
	e := validTable()
	e.Rows[0] = []string{"A"}
	if err := e.Validate(); err == nil {
		t.Fatal("expected row shape error")
	}
	e = validTable()
	e.Columns = make([]string, MaxColumns+1)
	if err := e.Validate(); err == nil {
		t.Fatal("expected column limit error")
	}
}

func TestValidateMindMapStructure(t *testing.T) {
	e := validMap()
	e.Nodes[2].ParentID = "missing"
	if err := e.Validate(); err == nil {
		t.Fatal("expected missing parent error")
	}
	e = validMap()
	e.Nodes[0].ParentID = "dark"
	e.Nodes[1].ParentID = "root"
	e.Nodes[2].ParentID = "light"
	if err := e.Validate(); err == nil {
		t.Fatal("expected cycle/disconnected error")
	}
	e = validMap()
	e.Nodes = append(e.Nodes, MindMapNode{ID: "light", ParentID: "root", Label: "duplicado"})
	if err := e.Validate(); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestEncodeDecode(t *testing.T) {
	raw, err := Encode([]Element{validTable(), validMap()})
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil || len(got) != 2 || got[1].Nodes[1].Label != "Fase clara" {
		t.Fatalf("round trip: %v %+v", err, got)
	}
	if _, err := Decode(`[ {"type":"table","columns":["a"],"rows":[["x","y"]]} ]`); err == nil {
		t.Fatal("expected malformed table rejection")
	}
}

func TestDecodeTableScalarCells(t *testing.T) {
	var got Element
	err := json.Unmarshal([]byte(`{"type":"table","columns":["Ano","Valor","Observação"],"rows":[[2024,2.5,null]]}`), &got)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("scalar table should validate: %v", err)
	}
	want := []string{"2024", "2.5", ""}
	for i, value := range want {
		if got.Rows[0][i] != value {
			t.Errorf("cell %d = %q, want %q", i, got.Rows[0][i], value)
		}
	}
}
