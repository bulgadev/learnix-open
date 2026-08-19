package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"learnix/internal/components"
	"learnix/internal/db"
)

func versionIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "vid"), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// studyFileVersion loads session + file + version, enforcing that the file
// belongs to the study and the version belongs to the file.
func (h *Handler) studyFileVersion(r *http.Request) (studyID, fileID, versionID int64, ok bool) {
	s, f, found := h.studyFile(r)
	if !found {
		return 0, 0, 0, false
	}
	vid, ok := versionIDParam(r)
	if !ok {
		return 0, 0, 0, false
	}
	v, err := h.files.GetVersion(r.Context(), vid, f.ID)
	if err != nil || v == nil {
		return 0, 0, 0, false
	}
	return s.StudyID, f.ID, vid, true
}

// VersionList renders the history drawer fragment for a file.
func (h *Handler) VersionList(w http.ResponseWriter, r *http.Request) {
	_, f, ok := h.studyFile(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	versions, err := h.files.Versions(r.Context(), f.ID)
	if err != nil {
		http.Error(w, "erro ao carregar versões", http.StatusInternalServerError)
		return
	}
	render(w, r, components.VersionList(f.StudyID, f.ID, versions))
}

// VersionRestore reverts the file to a version (creating a new user version)
// and returns the refreshed editor.
func (h *Handler) VersionRestore(w http.ResponseWriter, r *http.Request) {
	studyID, fileID, vid, ok := h.studyFileVersion(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := h.files.RestoreVersion(r.Context(), fileID, vid, studyID); err != nil {
		http.Error(w, "erro ao restaurar versão", http.StatusInternalServerError)
		return
	}
	f, _ := h.files.Get(r.Context(), fileID)
	if f == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("HX-Trigger", "refreshFiles")
	render(w, r, components.NoteEditor(*f, studyID))
}

// VersionBranch duplicates the file at a version's content into a new note.
func (h *Handler) VersionBranch(w http.ResponseWriter, r *http.Request) {
	studyID, fileID, vid, ok := h.studyFileVersion(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	original, err := h.files.Get(r.Context(), fileID)
	if err != nil || original == nil {
		http.NotFound(w, r)
		return
	}
	v, err := h.files.GetVersion(r.Context(), vid, fileID)
	if err != nil || v == nil {
		http.NotFound(w, r)
		return
	}
	name := uniqueBranchName(r, h, studyID, original.Name)
	nf := newBranchFile(studyID, original.ParentID, name, v.Content, v.ElementsJSON)
	if err := h.files.CreateAuthored(r.Context(), nf, "user", "ramificado da versão "+strconv.FormatInt(vid, 10)); err != nil {
		http.Error(w, "erro ao ramificar nota", http.StatusInternalServerError)
		return
	}
	w.Header().Set("HX-Trigger", "refreshFiles")
	render(w, r, components.NoteEditor(*nf, studyID))
}

// uniqueBranchName returns "<name> (ramificação)", appending a counter when
// that name already exists in the study.
func uniqueBranchName(r *http.Request, h *Handler, studyID int64, base string) string {
	existing := map[string]bool{}
	if files, err := h.files.ListByStudy(r.Context(), studyID); err == nil {
		for _, f := range files {
			existing[f.Name] = true
		}
	}
	candidate := base + " (ramificação)"
	if !existing[candidate] {
		return candidate
	}
	for n := 2; ; n++ {
		candidate = base + " (ramificação) " + strconv.Itoa(n)
		if !existing[candidate] {
			return candidate
		}
	}
}

func newBranchFile(studyID, parentID int64, name, content, elementsJSON string) *db.File {
	return &db.File{StudyID: studyID, ParentID: parentID, Name: name, Kind: "note", Content: content, ElementsJSON: elementsJSON}
}
