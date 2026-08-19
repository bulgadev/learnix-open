package handlers

import (
	"encoding/json"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/session"
)

const maxUploadBytes = 10 << 20

var allowedImageMimes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

func fileIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "fid"), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// studyFile loads the session plus the file addressed by {id}/{fid}, enforcing
// study ownership and that the file belongs to that study.
func (h *Handler) studyFile(r *http.Request) (*session.Session, *db.File, bool) {
	id, ok := studyIDParam(r)
	if !ok {
		return nil, nil, false
	}
	fid, ok := fileIDParam(r)
	if !ok {
		return nil, nil, false
	}
	s := h.loadStudy(r, id)
	if s == nil {
		return nil, nil, false
	}
	f, err := h.files.Get(r.Context(), fid)
	if err != nil || f == nil || f.StudyID != s.StudyID {
		return nil, nil, false
	}
	return s, f, true
}

func (h *Handler) validParent(r *http.Request, s *session.Session, parentID int64) bool {
	if parentID == 0 {
		return true
	}
	p, err := h.files.Get(r.Context(), parentID)
	return err == nil && p != nil && p.StudyID == s.StudyID && p.Kind == "folder"
}

func (h *Handler) renderFileTree(w http.ResponseWriter, r *http.Request, s *session.Session) {
	files, _ := h.files.ListByStudy(r.Context(), s.StudyID)
	render(w, r, components.FileTree(files, s.StudyID))
}

func (h *Handler) FileList(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	h.renderFileTree(w, r, s)
}

func (h *Handler) FileCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	kind := r.FormValue("kind")
	if kind != "folder" && kind != "note" {
		http.Error(w, "tipo inválido", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		if kind == "folder" {
			name = "Nova pasta"
		} else {
			name = "Nova nota"
		}
	}
	parentID, _ := strconv.ParseInt(r.FormValue("parent_id"), 10, 64)
	if !h.validParent(r, s, parentID) {
		http.Error(w, "pasta inválida", http.StatusBadRequest)
		return
	}
	f := &db.File{StudyID: s.StudyID, ParentID: parentID, Name: name, Kind: kind}
	if err := h.files.Create(r.Context(), f); err != nil {
		http.Error(w, "erro ao criar", http.StatusInternalServerError)
		return
	}
	h.renderFileTree(w, r, s)
}

func (h *Handler) FileUpdate(w http.ResponseWriter, r *http.Request) {
	s, f, ok := h.studyFile(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if name := strings.TrimSpace(r.FormValue("name")); name != "" {
		if err := h.files.Rename(r.Context(), f.ID, s.StudyID, name); err != nil {
			http.Error(w, "erro ao renomear", http.StatusInternalServerError)
			return
		}
	}
	if ps := r.FormValue("parent_id"); ps != "" {
		parentID, err := strconv.ParseInt(ps, 10, 64)
		if err != nil || !h.validParent(r, s, parentID) {
			http.Error(w, "pasta inválida", http.StatusBadRequest)
			return
		}
		if err := h.files.Move(r.Context(), f.ID, s.StudyID, parentID); err != nil {
			http.Error(w, "erro ao mover", http.StatusBadRequest)
			return
		}
	}
	// The renamed/moved file may be open in the editor pane; ask the client to
	// re-fetch it so its title stays in sync with the tree.
	w.Header().Set("HX-Trigger", `{"refreshEditor":{"study_id":`+strconv.FormatInt(s.StudyID, 10)+`,"file_id":`+strconv.FormatInt(f.ID, 10)+`}}`)
	h.renderFileTree(w, r, s)
}

func (h *Handler) FileDelete(w http.ResponseWriter, r *http.Request) {
	s, f, ok := h.studyFile(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.files.Delete(r.Context(), f.ID, s.StudyID); err != nil {
		http.Error(w, "erro ao excluir", http.StatusInternalServerError)
		return
	}
	h.renderFileTree(w, r, s)
}

func (h *Handler) FileContent(w http.ResponseWriter, r *http.Request) {
	s, f, ok := h.studyFile(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if f.Kind != "note" {
		http.Error(w, "apenas notas têm conteúdo", http.StatusBadRequest)
		return
	}
	var body struct {
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "corpo inválido", http.StatusBadRequest)
		return
	}
	if err := h.files.UpdateContent(r.Context(), f.ID, s.StudyID, body.Content, "user", ""); err != nil {
		http.Error(w, "erro ao salvar", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (h *Handler) FileEdit(w http.ResponseWriter, r *http.Request) {
	s, f, ok := h.studyFile(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	render(w, r, components.NoteEditor(*f, s.StudyID))
}

func (h *Handler) FileRaw(w http.ResponseWriter, r *http.Request) {
	_, f, ok := h.studyFile(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if f.Kind != "image" {
		http.NotFound(w, r)
		return
	}
	mime := f.Mime
	if mime == "" {
		mime = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(f.Data)
}

func (h *Handler) FileUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := studyIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	s := h.loadStudy(r, id)
	if s == nil {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		http.Error(w, "arquivo muito grande", http.StatusBadRequest)
		return
	}
	src, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "arquivo ausente", http.StatusBadRequest)
		return
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		http.Error(w, "arquivo muito grande", http.StatusBadRequest)
		return
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	mime := strings.SplitN(http.DetectContentType(sniff), ";", 2)[0]
	if !allowedImageMimes[mime] {
		http.Error(w, "tipo não suportado", http.StatusBadRequest)
		return
	}
	parentID, _ := strconv.ParseInt(r.FormValue("parent_id"), 10, 64)
	if !h.validParent(r, s, parentID) {
		http.Error(w, "pasta inválida", http.StatusBadRequest)
		return
	}
	f := &db.File{
		StudyID:  s.StudyID,
		ParentID: parentID,
		Name:     sanitizeFilename(header.Filename),
		Kind:     "image",
		Mime:     mime,
		Data:     data,
		Size:     int64(len(data)),
	}
	if err := h.files.Create(r.Context(), f); err != nil {
		http.Error(w, "erro ao salvar", http.StatusInternalServerError)
		return
	}
	h.renderFileTree(w, r, s)
}

func sanitizeFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == "/" {
		return "imagem"
	}
	return name
}
