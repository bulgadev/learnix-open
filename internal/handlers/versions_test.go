package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func (te *testEnv) saveContent(t *testing.T, loc string, fid int64, content string, cookie *http.Cookie) {
	t.Helper()
	rr := te.reqCT(t, "POST", fileURL(loc, fid)+"/content", "application/json",
		`{"content":`+strconv.Quote(content)+`}`, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save content: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestVersions_ListShowsAll(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "vers@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	fid := te.createFile(t, loc, "note", "notas", "", cookie)
	te.saveContent(t, loc, fid, "conteudo alfa", cookie)
	te.saveContent(t, loc, fid, "conteudo bravo", cookie)

	rr := te.req(t, "GET", fileURL(loc, fid)+"/versions", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("versions: expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"conteudo alfa", "conteudo bravo", "Restaurar", "Ramificar nota"} {
		if !strings.Contains(body, want) {
			t.Errorf("versions fragment missing %q", want)
		}
	}
	vs, _ := te.files.Versions(testCtx, fid)
	if len(vs) != 3 {
		t.Fatalf("expected 3 versions (create + 2 saves), got %d", len(vs))
	}
}

func TestVersions_Restore(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "restore@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	fid := te.createFile(t, loc, "note", "notas", "", cookie)
	te.saveContent(t, loc, fid, "versao dois", cookie)

	vs, _ := te.files.Versions(testCtx, fid)
	oldest := vs[len(vs)-1] // newest first → last is the original

	rr := te.req(t, "POST", fileURL(loc, fid)+"/versions/"+strconv.FormatInt(oldest.ID, 10)+"/restore", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("restore: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Trigger"); !strings.Contains(got, "refreshFiles") {
		t.Errorf("expected HX-Trigger refreshFiles, got %q", got)
	}

	f, _ := te.files.Get(testCtx, fid)
	if f == nil || f.Content != "" {
		t.Errorf("content should revert to the original (empty), got %+v", f)
	}
	vs, _ = te.files.Versions(testCtx, fid)
	if len(vs) != 3 {
		t.Fatalf("restore must add a version: expected 3, got %d", len(vs))
	}
	if vs[0].Author != "user" || !strings.Contains(vs[0].Message, "restaurado") {
		t.Errorf("newest version should be the user restore, got %+v", vs[0])
	}
}

func TestVersions_Branch(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "branch@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	fid := te.createFile(t, loc, "note", "original", "", cookie)
	te.saveContent(t, loc, fid, "texto ramificado", cookie)

	vs, _ := te.files.Versions(testCtx, fid)
	vid := vs[0].ID // newest ("texto ramificado")

	rr := te.req(t, "POST", fileURL(loc, fid)+"/versions/"+strconv.FormatInt(vid, 10)+"/branch", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("branch: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("HX-Trigger"); !strings.Contains(got, "refreshFiles") {
		t.Errorf("expected HX-Trigger refreshFiles, got %q", got)
	}

	sid := fid64(t, loc)
	files, _ := te.files.ListByStudy(testCtx, sid)
	var branchID int64
	for _, f := range files {
		if f.Name == "original (ramificação)" {
			branchID = f.ID
			if f.Content != "texto ramificado" {
				t.Errorf("branch content = %q, want the version content", f.Content)
			}
		}
	}
	if branchID == 0 {
		t.Fatal("branch file 'original (ramificação)' not found")
	}
	bv, _ := te.files.Versions(testCtx, branchID)
	if len(bv) != 1 || bv[0].Author != "user" || !strings.Contains(bv[0].Message, "ramificado") {
		t.Errorf("branch should have 1 user version tagged ramificado, got %+v", bv)
	}

	// Branching again must not collide.
	te.req(t, "POST", fileURL(loc, fid)+"/versions/"+strconv.FormatInt(vid, 10)+"/branch", "", cookie)
	files, _ = te.files.ListByStudy(testCtx, sid)
	found := false
	for _, f := range files {
		if f.Name == "original (ramificação) 2" {
			found = true
		}
	}
	if !found {
		t.Error("second branch should be named 'original (ramificação) 2'")
	}
}

func TestVersions_CrossUser404(t *testing.T) {
	te := newTestEnv(t)
	cookieA := te.register(t, "va@test.com", "hunter2!")
	locA := te.createStudy(t, "tema A", cookieA)
	fid := te.createFile(t, locA, "note", "privada", "", cookieA)

	cookieB := te.register(t, "vb@test.com", "hunter2!")
	if rr := te.req(t, "GET", fileURL(locA, fid)+"/versions", "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("versions as other user: expected 404, got %d", rr.Code)
	}
	vs, _ := te.files.Versions(testCtx, fid)
	if rr := te.req(t, "POST", fileURL(locA, fid)+"/versions/"+strconv.FormatInt(vs[0].ID, 10)+"/restore", "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("restore as other user: expected 404, got %d", rr.Code)
	}
}

func TestVersions_WrongVersion404(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "wrongv@test.com", "hunter2!")
	loc := te.createStudy(t, "tema", cookie)
	fid1 := te.createFile(t, loc, "note", "uma", "", cookie)
	fid2 := te.createFile(t, loc, "note", "duas", "", cookie)

	vs2, _ := te.files.Versions(testCtx, fid2)
	// Version of file 2 against file 1's route → 404.
	rr := te.req(t, "POST", fileURL(loc, fid1)+"/versions/"+strconv.FormatInt(vs2[0].ID, 10)+"/restore", "", cookie)
	if rr.Code != http.StatusNotFound {
		t.Errorf("foreign version restore: expected 404, got %d", rr.Code)
	}
}
