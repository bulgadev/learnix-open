package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"learnix/internal/db"
)

func saveNoteURL(loc string, cid, mid int64) string {
	return loc + "/chats/" + strconv.FormatInt(cid, 10) +
		"/messages/" + strconv.FormatInt(mid, 10) + "/save-to-note"
}

func TestSaveToNote_CreatesNoteFromStreamedAnswer(t *testing.T) {
	te := newTestEnv(t)
	srv := fakeOpenAI("## Mitocôndria\ntexto")
	defer srv.Close()
	cookie := te.register(t, "savenote@test.com", "hunter2!")
	te.grantQuota(t, cookie, 100000)
	loc := te.createStudyAt(t, "biologia", srv.URL, cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)

	rr := te.streamChat(t, loc, c.ID, "oi", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("stream: expected 200, got %d", rr.Code)
	}
	msgs, _ := te.chats.Messages(testCtx, c.ID)
	var asst db.ChatMessage
	for _, m := range msgs {
		if m.Role == "assistant" {
			asst = m
		}
	}
	if asst.ID == 0 {
		t.Fatal("no assistant message persisted")
	}

	rr = te.req(t, "POST", saveNoteURL(loc, c.ID, asst.ID), "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save-to-note: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("HX-Trigger"), "refreshFiles") {
		t.Errorf("HX-Trigger should contain refreshFiles, got %q", rr.Header().Get("HX-Trigger"))
	}
	if !strings.Contains(rr.Body.String(), "Nota criada") {
		t.Errorf("response should render the done pill, got: %s", rr.Body.String())
	}

	files, err := te.files.ListByStudy(testCtx, sid)
	if err != nil || len(files) != 1 {
		t.Fatalf("expected 1 note, got %d (%v)", len(files), err)
	}
	f := files[0]
	if f.Kind != "note" {
		t.Errorf("kind = %q, want note", f.Kind)
	}
	if f.Name != "Mitocôndria" {
		t.Errorf("title = %q, want Mitocôndria (derived from heading)", f.Name)
	}
	if f.Content != asst.Content {
		t.Errorf("note content = %q, want %q", f.Content, asst.Content)
	}
	versions, err := te.files.Versions(testCtx, f.ID)
	if err != nil || len(versions) == 0 {
		t.Fatalf("versions: %v (len %d)", err, len(versions))
	}
	if versions[0].Author != "ai" {
		t.Errorf("newest version author = %q, want ai", versions[0].Author)
	}
}

func TestSaveToNote_PlainTextTitleFallback(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "plain@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	sid := fid64(t, loc)
	c := te.firstChat(t, sid)

	long := "A fotossíntese converte luz em energia química nas plantas."
	m := &db.ChatMessage{ChatID: c.ID, Role: "assistant", Content: long}
	if err := te.chats.AddMessage(testCtx, m); err != nil {
		t.Fatal(err)
	}
	rr := te.req(t, "POST", saveNoteURL(loc, c.ID, m.ID), "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save-to-note: expected 200, got %d", rr.Code)
	}
	want := string([]rune(long)[:40])
	files, _ := te.files.ListByStudy(testCtx, sid)
	if len(files) != 1 || files[0].Name != want {
		t.Errorf("title = %q, want first 40 runes %q", files[0].Name, want)
	}

	short := &db.ChatMessage{ChatID: c.ID, Role: "assistant", Content: "Resposta curta."}
	if err := te.chats.AddMessage(testCtx, short); err != nil {
		t.Fatal(err)
	}
	rr = te.req(t, "POST", saveNoteURL(loc, c.ID, short.ID), "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save-to-note short: expected 200, got %d", rr.Code)
	}
	files, _ = te.files.ListByStudy(testCtx, sid)
	var got string
	for _, f := range files {
		if f.Content == "Resposta curta." {
			got = f.Name
		}
	}
	if got != "Resposta curta." {
		t.Errorf("title = %q, want full short line", got)
	}
}

func TestSaveToNote_NotFoundCases(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "save404@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	user := &db.ChatMessage{ChatID: c.ID, Role: "user", Content: "pergunta"}
	if err := te.chats.AddMessage(testCtx, user); err != nil {
		t.Fatal(err)
	}
	asst := &db.ChatMessage{ChatID: c.ID, ParentID: user.ID, Role: "assistant", Content: "## Título\nresposta"}
	if err := te.chats.AddMessage(testCtx, asst); err != nil {
		t.Fatal(err)
	}

	if rr := te.req(t, "POST", saveNoteURL(loc, c.ID, user.ID), "", cookie); rr.Code != http.StatusNotFound {
		t.Errorf("user message: expected 404, got %d", rr.Code)
	}
	if rr := te.req(t, "POST", saveNoteURL(loc, c.ID, 9999), "", cookie); rr.Code != http.StatusNotFound {
		t.Errorf("missing message: expected 404, got %d", rr.Code)
	}

	cookieB := te.register(t, "save-intruder@test.com", "hunter2!")
	if rr := te.req(t, "POST", saveNoteURL(loc, c.ID, asst.ID), "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("cross-user: expected 404, got %d", rr.Code)
	}

	files, _ := te.files.ListByStudy(testCtx, fid64(t, loc))
	if len(files) != 0 {
		t.Errorf("no note should have been created, got %d", len(files))
	}
}

func TestChatPane_RendersToolLogActivity(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "toollog@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	te.req(t, "GET", loc, "", cookie)
	c := te.firstChat(t, fid64(t, loc))

	m := &db.ChatMessage{
		ChatID:      c.ID,
		Role:        "assistant",
		Content:     "resposta",
		ToolLogJSON: `[{"name":"search_web","args":"{}","summary":"pesquisou: x (2 resultados)"}]`,
	}
	if err := te.chats.AddMessage(testCtx, m); err != nil {
		t.Fatal(err)
	}

	rr := te.req(t, "GET", loc+"/chats", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("chats pane: expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Atividade") {
		t.Errorf("pane should render the Atividade details")
	}
	if !strings.Contains(body, "pesquisou") {
		t.Errorf("pane should render the tool summary")
	}
	if !strings.Contains(body, "save-to-note") {
		t.Errorf("pane should render the save-to-note action")
	}
}
