package handlers

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func (te *testEnv) postHighlight(t *testing.T, loc, kind string, sourceID int64, excerpt string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"source_kind":` + strconv.Quote(kind) + `,"source_id":` + strconv.FormatInt(sourceID, 10) +
		`,"excerpt":` + strconv.Quote(excerpt) + `}`
	return te.reqCT(t, "POST", loc+"/highlights", "application/json", body, cookie)
}

func TestHighlights_CreateFromNoteAndMessage(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "hl@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)

	fid := te.createFile(t, loc, "note", "resumo", "", cookie)
	rr := te.postHighlight(t, loc, "note", fid, "trecho importante da nota", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("note highlight: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	c := te.firstChat(t, sid)
	chain := te.seedChain(t, c.ID, 2)
	rr = te.postHighlight(t, loc, "message", chain[1].ID, "resposta crucial da IA", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("message highlight: expected 200, got %d", rr.Code)
	}

	hls, err := te.highlights.ListByStudy(testCtx, sid)
	if err != nil || len(hls) != 2 {
		t.Fatalf("expected 2 highlights, got %d (%v)", len(hls), err)
	}

	rr = te.req(t, "GET", loc+"/saved", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("saved panel: %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "trecho importante") || !strings.Contains(body, "resposta crucial") {
		t.Errorf("saved panel should list both highlights")
	}
	if !strings.Contains(body, "Destaques") {
		t.Errorf("saved panel missing section label")
	}
}

func TestHighlights_Validation(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "hlv@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)

	if rr := te.postHighlight(t, loc, "note", 999999, "texto qualquer", cookie); rr.Code != http.StatusNotFound {
		t.Errorf("foreign note highlight: expected 404, got %d", rr.Code)
	}
	fid := te.createFile(t, loc, "note", "n", "", cookie)
	if rr := te.postHighlight(t, loc, "note", fid, "", cookie); rr.Code != http.StatusBadRequest {
		t.Errorf("empty excerpt: expected 400, got %d", rr.Code)
	}
	if rr := te.postHighlight(t, loc, "banana", fid, "texto", cookie); rr.Code != http.StatusBadRequest {
		t.Errorf("bad kind: expected 400, got %d", rr.Code)
	}
	if rr := te.postHighlight(t, loc, "message", 424242, "texto", cookie); rr.Code != http.StatusNotFound {
		t.Errorf("foreign message highlight: expected 404, got %d", rr.Code)
	}
}

func TestHighlights_Delete(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "hld@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	fid := te.createFile(t, loc, "note", "n", "", cookie)
	te.postHighlight(t, loc, "note", fid, "para remover", cookie)
	hls, _ := te.highlights.ListByStudy(testCtx, fid64(t, loc))
	if len(hls) != 1 {
		t.Fatalf("setup: expected 1 highlight, got %d", len(hls))
	}

	rr := te.req(t, "POST", loc+"/highlights/"+strconv.FormatInt(hls[0].ID, 10)+"/delete", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: %d", rr.Code)
	}
	hls, _ = te.highlights.ListByStudy(testCtx, fid64(t, loc))
	if len(hls) != 0 {
		t.Errorf("highlight should be gone, got %d", len(hls))
	}
	if !strings.Contains(rr.Body.String(), "Nada salvo ainda") {
		t.Errorf("panel should show empty state after delete")
	}
}

func TestHighlights_CrossUser404(t *testing.T) {
	te := newTestEnv(t)
	cookieA := te.register(t, "hla@test.com", "hunter2!")
	locA := te.createStudy(t, "tema A", cookieA)
	fid := te.createFile(t, locA, "note", "privada", "", cookieA)

	cookieB := te.register(t, "hlb@test.com", "hunter2!")
	if rr := te.postHighlight(t, locA, "note", fid, "x", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("highlight as other user: expected 404, got %d", rr.Code)
	}
	if rr := te.req(t, "GET", locA+"/saved", "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("saved panel as other user: expected 404, got %d", rr.Code)
	}
}

func TestSaveMessage_ToggleAndPanel(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "bm@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)
	chain := te.seedChain(t, c.ID, 2)
	mid := chain[1].ID
	cid := c.ID

	url := loc + "/chats/" + strconv.FormatInt(cid, 10) + "/messages/" + strconv.FormatInt(mid, 10) + "/save"
	rr := te.req(t, "POST", url, "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save toggle: %d body=%s", rr.Code, rr.Body.String())
	}
	msgs, _ := te.chats.Messages(testCtx, cid)
	if !msgs[1].Saved {
		t.Error("message should be saved after first toggle")
	}
	if !strings.Contains(rr.Body.String(), "Salvo") {
		t.Errorf("button should show saved state")
	}

	rr = te.req(t, "GET", loc+"/saved", "", cookie)
	if !strings.Contains(rr.Body.String(), "Respostas salvas") || !strings.Contains(rr.Body.String(), "msg 2") {
		t.Errorf("saved panel should list the bookmarked message")
	}

	// Toggle off.
	te.req(t, "POST", url, "", cookie)
	msgs, _ = te.chats.Messages(testCtx, cid)
	if msgs[1].Saved {
		t.Error("message should be unsaved after second toggle")
	}

	// Cross-user.
	cookieB := te.register(t, "bmb@test.com", "hunter2!")
	if rr := te.req(t, "POST", url, "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("save as other user: expected 404, got %d", rr.Code)
	}
}
