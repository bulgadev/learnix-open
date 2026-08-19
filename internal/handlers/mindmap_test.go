package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"learnix/internal/mindmap"
)

func TestMindMapPageBuildsFromStudyNotes(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mindmap-page@test.com", "hunter2!")
	loc := te.createStudy(t, "Fotossíntese", cookie)
	fid := te.createFile(t, loc, "note", "Biologia.md", "", cookie)
	content := `# Fase clara

## ATP

## NADPH`
	update := te.reqCT(t, "POST", fileURL(loc, fid)+"/content", "application/json", `{"content":"`+strings.ReplaceAll(content, "\n", "\\n")+`"}`, cookie)
	if update.Code != http.StatusOK {
		t.Fatalf("save note: expected 200, got %d: %s", update.Code, update.Body.String())
	}
	rr := te.req(t, "GET", loc+"/mapa-mental", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("map page: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"Mapa mental", "Recurso experimental", "Biologia.md", "Fase clara", "ATP", "NADPH", "mind-map.js"} {
		if !strings.Contains(body, want) {
			t.Errorf("map page missing %q", want)
		}
	}
}

func TestMindMapJSONEnforcesStudyOwnership(t *testing.T) {
	te := newTestEnv(t)
	owner := te.register(t, "mindmap-owner@test.com", "hunter2!")
	intruder := te.register(t, "mindmap-intruder@test.com", "hunter2!")
	loc := te.createStudy(t, "Privado", owner)
	if rr := te.req(t, "GET", loc+"/mapa-mental.json", "", intruder); rr.Code != http.StatusNotFound {
		t.Fatalf("intruder status = %d, want 404", rr.Code)
	}
	rr := te.req(t, "GET", loc+"/mapa-mental.json", "", owner)
	if rr.Code != http.StatusOK {
		t.Fatalf("owner status = %d: %s", rr.Code, rr.Body.String())
	}
	var graph mindmap.Graph
	if err := json.Unmarshal(rr.Body.Bytes(), &graph); err != nil {
		t.Fatal(err)
	}
	if graph.RootID != "root" || len(graph.Nodes) != 1 {
		t.Fatalf("unexpected initial graph: %+v", graph)
	}
}

func TestMindMapUpdateRequiresCSRFAndValidGraph(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mindmap-update@test.com", "hunter2!")
	loc := te.createStudy(t, "Geometria", cookie)
	valid := `{"root_id":"root","nodes":[{"id":"root","label":"Geometria","collapsed":false},{"id":"triangulo","parent_id":"root","label":"Triângulos","collapsed":false}]}`
	withoutCSRF := te.reqCT(t, "PUT", loc+"/mapa-mental", "application/json", valid, cookie)
	if withoutCSRF.Code != http.StatusBadRequest {
		t.Fatalf("without csrf status = %d, want 400", withoutCSRF.Code)
	}
	csrf := te.csrfToken(t, cookie)
	request := httptest.NewRequest("PUT", loc+"/mapa-mental", strings.NewReader(valid))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response := httptest.NewRecorder()
	te.router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("valid update status = %d: %s", response.Code, response.Body.String())
	}
	bad := `{"root_id":"root","nodes":[{"id":"root","label":"Geometria","collapsed":false},{"id":"a","parent_id":"b","label":"A","collapsed":false},{"id":"b","parent_id":"a","label":"B","collapsed":false}]}`
	request = httptest.NewRequest("PUT", loc+"/mapa-mental", strings.NewReader(bad))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CSRF-Token", csrf)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	te.router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid graph status = %d, want 400", response.Code)
	}
	jsonResponse := te.req(t, "GET", loc+"/mapa-mental.json", "", cookie)
	if !strings.Contains(jsonResponse.Body.String(), "Triângulos") {
		t.Fatalf("valid graph was not persisted: %s", jsonResponse.Body.String())
	}
}
