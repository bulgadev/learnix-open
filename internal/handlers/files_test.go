package handlers

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

var pngHead = []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52}

func fid64(t *testing.T, loc string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(strings.TrimPrefix(loc, "/study/"), 10, 64)
	if err != nil {
		t.Fatalf("bad study path %q: %v", loc, err)
	}
	return id
}

func fileURL(loc string, fid int64) string {
	return loc + "/files/" + strconv.FormatInt(fid, 10)
}

func (te *testEnv) reqCT(t *testing.T, method, path, contentType, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	te.router.ServeHTTP(rr, req)
	return rr
}

// createFile posts the create form and returns the new file's ID.
func (te *testEnv) createFile(t *testing.T, loc, kind, name, parent string, cookie *http.Cookie) int64 {
	t.Helper()
	form := "kind=" + kind + "&name=" + name
	if parent != "" {
		form += "&parent_id=" + parent
	}
	rr := te.req(t, "POST", loc+"/files", form, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("create %s %q: expected 200, got %d body=%s", kind, name, rr.Code, rr.Body.String())
	}
	files, err := te.files.ListByStudy(testCtx, fid64(t, loc))
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	for _, f := range files {
		if f.Name == name && f.Kind == kind {
			return f.ID
		}
	}
	t.Fatalf("file %q (%s) not found after create", name, kind)
	return 0
}

func (te *testEnv) upload(t *testing.T, loc, filename string, data []byte, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", loc+"/files/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rr := httptest.NewRecorder()
	te.router.ServeHTTP(rr, req)
	return rr
}

func TestFiles_CreateNote(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "files@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)

	fid := te.createFile(t, loc, "note", "notas", "", cookie)
	f, err := te.files.Get(testCtx, fid)
	if err != nil || f == nil {
		t.Fatalf("file not persisted: %v", err)
	}
	if f.Kind != "note" {
		t.Errorf("expected kind note, got %q", f.Kind)
	}
	vs, err := te.files.Versions(testCtx, fid)
	if err != nil || len(vs) != 1 {
		t.Errorf("expected 1 initial version, got %d (%v)", len(vs), err)
	}
}

func TestFiles_CreateNoteInsideFolder(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "fold@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)

	folder := te.createFile(t, loc, "folder", "materias", "", cookie)
	note := te.createFile(t, loc, "note", "resumo", strconv.FormatInt(folder, 10), cookie)

	f, _ := te.files.Get(testCtx, note)
	if f == nil || f.ParentID != folder {
		t.Errorf("note should live inside the folder, got %+v", f)
	}
}

func TestFiles_SaveContentIncrementsVersions(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "save@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)
	fid := te.createFile(t, loc, "note", "notas", "", cookie)

	rr := te.reqCT(t, "POST", fileURL(loc, fid)+"/content", "application/json", `{"content":"# Ola"}`, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("save content: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"ok":true`) {
		t.Errorf("expected ok:true, got %s", rr.Body.String())
	}

	vs, _ := te.files.Versions(testCtx, fid)
	if len(vs) != 2 {
		t.Fatalf("expected 2 versions after save, got %d", len(vs))
	}
	if vs[0].Author != "user" {
		t.Errorf("latest version author should be user, got %q", vs[0].Author)
	}
	f, _ := te.files.Get(testCtx, fid)
	if f.Content != "# Ola" {
		t.Errorf("content not updated: %q", f.Content)
	}
}

func TestFiles_RenameAndMoveRoundTrip(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "mv@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)

	folder := te.createFile(t, loc, "folder", "pasta1", "", cookie)
	note := te.createFile(t, loc, "note", "original", "", cookie)

	rr := te.req(t, "POST", fileURL(loc, note), "name=renomeada", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("rename: expected 200, got %d", rr.Code)
	}
	f, _ := te.files.Get(testCtx, note)
	if f.Name != "renomeada" {
		t.Errorf("rename failed: %q", f.Name)
	}

	rr = te.req(t, "POST", fileURL(loc, note), "parent_id="+strconv.FormatInt(folder, 10), cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("move: expected 200, got %d", rr.Code)
	}
	f, _ = te.files.Get(testCtx, note)
	if f.ParentID != folder {
		t.Errorf("move into folder failed: %+v", f)
	}

	rr = te.req(t, "POST", fileURL(loc, note), "parent_id=0", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("move to root: expected 200, got %d", rr.Code)
	}
	f, _ = te.files.Get(testCtx, note)
	if f.ParentID != 0 {
		t.Errorf("move back to root failed: %+v", f)
	}
}

func TestFiles_DeleteRemovesFile(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "del-file@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)
	fid := te.createFile(t, loc, "note", "temporaria", "", cookie)

	rr := te.req(t, "POST", fileURL(loc, fid)+"/delete", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d", rr.Code)
	}
	f, _ := te.files.Get(testCtx, fid)
	if f != nil {
		t.Errorf("file should be gone, got %+v", f)
	}
}

func TestFiles_UploadPNGAndRaw(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "up@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)

	data := append(append([]byte{}, pngHead...), make([]byte, 64)...)
	rr := te.upload(t, loc, "foto.png", data, cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("upload: expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	files, _ := te.files.ListByStudy(testCtx, fid64(t, loc))
	var imgID int64
	for _, f := range files {
		if f.Kind == "image" {
			imgID = f.ID
		}
	}
	if imgID == 0 {
		t.Fatal("no image file found after upload")
	}
	f, _ := te.files.Get(testCtx, imgID)
	if f.Size <= 0 || f.Mime != "image/png" {
		t.Errorf("bad image meta: size=%d mime=%q", f.Size, f.Mime)
	}

	rrRaw := te.req(t, "GET", fileURL(loc, imgID)+"/raw", "", cookie)
	if rrRaw.Code != http.StatusOK {
		t.Fatalf("raw: expected 200, got %d", rrRaw.Code)
	}
	if ct := rrRaw.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("raw content-type: %q", ct)
	}
	got, _ := io.ReadAll(rrRaw.Result().Body)
	if !bytes.Equal(got, data) {
		t.Errorf("raw bytes differ: got %d bytes, want %d", len(got), len(data))
	}
}

func TestFiles_UploadRejectsNonImage(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "up-bad@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)

	rr := te.upload(t, loc, "texto.txt", []byte("hello plain text"), cookie)
	if rr.Code < 400 {
		t.Errorf("non-image upload should fail with 4xx, got %d", rr.Code)
	}
}

func TestFiles_UploadRejectsOversize(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "up-big@test.com", "hunter2!")
	loc := te.createStudy(t, "workspace", cookie)

	big := bytes.Repeat([]byte("a"), 11<<20)
	rr := te.upload(t, loc, "big.png", big, cookie)
	if rr.Code < 400 {
		t.Errorf("oversize upload should fail with 4xx, got %d", rr.Code)
	}
}

func TestFiles_CrossUser404(t *testing.T) {
	te := newTestEnv(t)
	cookieA := te.register(t, "dono@test.com", "hunter2!")
	cookieB := te.register(t, "invasor@test.com", "hunter2!")
	loc := te.createStudy(t, "privado", cookieA)

	data := append(append([]byte{}, pngHead...), make([]byte, 32)...)
	if rr := te.upload(t, loc, "segredo.png", data, cookieA); rr.Code != http.StatusOK {
		t.Fatalf("upload as owner: expected 200, got %d", rr.Code)
	}
	files, _ := te.files.ListByStudy(testCtx, fid64(t, loc))
	if len(files) == 0 {
		t.Fatal("owner has no files")
	}
	fid := files[0].ID

	if rr := te.req(t, "GET", fileURL(loc, fid)+"/edit", "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("edit as other user: expected 404, got %d", rr.Code)
	}
	if rr := te.req(t, "GET", fileURL(loc, fid)+"/raw", "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("raw as other user: expected 404, got %d", rr.Code)
	}
	if rr := te.req(t, "POST", fileURL(loc, fid)+"/delete", "", cookieB); rr.Code != http.StatusNotFound {
		t.Errorf("delete as other user: expected 404, got %d", rr.Code)
	}
	if f, _ := te.files.Get(testCtx, fid); f == nil {
		t.Error("file must survive the intruder's delete attempt")
	}
}

func TestWorkspacePage_RendersSidebar(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "ws@test.com", "hunter2!")
	loc := te.createStudy(t, "biomas", cookie)

	rr := te.req(t, "GET", loc, "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("workspace page: expected 200, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `id="file-tree"`) {
		t.Error("workspace should contain the file tree sidebar")
	}
	if !strings.Contains(body, "Nova nota") {
		t.Error("workspace should expose the Nova nota action")
	}
	if !strings.Contains(body, "Mind Map") {
		t.Error("workspace should expose Mind Map in the file manager")
	}
	if !strings.Contains(body, loc+`/mapa-mental`) {
		t.Error("Mind Map item should link to the study mind-map route")
	}
}
