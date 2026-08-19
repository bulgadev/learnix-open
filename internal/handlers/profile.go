package handlers

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
)

const (
	profileMaxUploadBytes = 2 << 20
	profileMaxImageSide   = 4096
	profileMaxNameRunes   = 60
	profileMaxBioRunes    = 280
)

func profileSlugParam(r *http.Request) (string, bool) {
	slug := strings.TrimSpace(chi.URLParam(r, "slug"))
	return slug, slug != ""
}

func profileRedirectPath(slug string) string {
	return "/profile/" + url.PathEscape(slug)
}

// ProfileMe redirects the authenticated owner to their stable public slug.
func (h *Handler) ProfileMe(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFromContext(r.Context())
	if u == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	p, err := h.profiles.ByUser(r.Context(), u.ID)
	if err != nil || p == nil {
		http.Error(w, "perfil não encontrado", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, profileRedirectPath(p.Slug), http.StatusFound)
}

// ProfilePage renders both public profiles and the owner's editable view.
func (h *Handler) ProfilePage(w http.ResponseWriter, r *http.Request) {
	slug, ok := profileSlugParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	view, err := h.profiles.ViewBySlug(r.Context(), slug)
	if err != nil || view == nil {
		http.NotFound(w, r)
		return
	}
	viewer := auth.UserFromContext(r.Context())
	owner := viewer != nil && viewer.ID == view.Profile.UserID
	view.IsOwner = owner
	data := components.PageData{Title: "Perfil"}
	if viewer != nil {
		data = components.AuthedPageData("Perfil", "", "", viewer, h.quotaFor(r.Context(), viewer), h.isAdmin(viewer))
	}
	notice := ""
	if r.URL.Query().Get("updated") == "1" && owner {
		notice = "Perfil atualizado."
	}
	render(w, r, components.ProfilePage(data, *view, owner, auth.CSRFToken(r, h.sessionSecret), notice))
}

// ProfileAvatar serves only the selected public avatar. The optional auth
// context lets the owner preview an avatar while it is hidden publicly.
func (h *Handler) ProfileAvatar(w http.ResponseWriter, r *http.Request) {
	slug, ok := profileSlugParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	view, err := h.profiles.ViewBySlug(r.Context(), slug)
	if err != nil || view == nil || len(view.Details.AvatarData) == 0 {
		http.NotFound(w, r)
		return
	}
	viewer := auth.UserFromContext(r.Context())
	owner := viewer != nil && viewer.ID == view.Profile.UserID
	if !view.Visible(db.ProfileVisibilityAvatar, owner) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", view.Details.AvatarMIME)
	if owner {
		w.Header().Set("Cache-Control", "private, no-store")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=3600")
	}
	_, _ = w.Write(view.Details.AvatarData)
}

// ProfileUpdate validates and persists the owner's profile form.
func (h *Handler) ProfileUpdate(w http.ResponseWriter, r *http.Request) {
	slug, ok := profileSlugParam(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	viewer := auth.UserFromContext(r.Context())
	if viewer == nil {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	view, err := h.profiles.ViewBySlug(r.Context(), slug)
	if err != nil || view == nil || view.Profile.UserID != viewer.ID {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseMultipartForm(profileMaxUploadBytes); err != nil {
		http.Error(w, "formulário inválido ou muito grande", http.StatusBadRequest)
		return
	}
	if !auth.CSRFValid(r, h.sessionSecret) {
		http.Error(w, "token CSRF inválido", http.StatusBadRequest)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	_, usernameSet := r.MultipartForm.Value["username"]
	displayName := strings.TrimSpace(r.FormValue("display_name"))
	bio := strings.TrimSpace(r.FormValue("bio"))
	if utf8.RuneCountInString(displayName) > profileMaxNameRunes {
		http.Error(w, "nome muito longo", http.StatusBadRequest)
		return
	}
	if utf8.RuneCountInString(bio) > profileMaxBioRunes {
		http.Error(w, "bio muito longa", http.StatusBadRequest)
		return
	}

	visibility := make(map[string]bool)
	for _, field := range db.ProfileVisibilityFields() {
		visibility[field.Key] = r.FormValue("visible_"+field.Key) == "on"
	}

	update := db.ProfileUpdate{
		Username:     username,
		UsernameSet:  usernameSet,
		DisplayName:  displayName,
		Bio:          bio,
		Visibility:   visibility,
		RemoveAvatar: r.FormValue("remove_avatar") == "on",
	}
	file, _, fileErr := r.FormFile("avatar")
	if fileErr == nil {
		defer file.Close()
		data, err := io.ReadAll(io.LimitReader(file, profileMaxUploadBytes+1))
		if err != nil || len(data) > profileMaxUploadBytes {
			http.Error(w, "foto inválida ou muito grande", http.StatusBadRequest)
			return
		}
		mime, err := validateProfileImage(data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		update.AvatarMIME = mime
		update.AvatarData = data
	} else if fileErr != http.ErrMissingFile {
		http.Error(w, "foto inválida", http.StatusBadRequest)
		return
	}
	if update.RemoveAvatar && update.AvatarData != nil {
		http.Error(w, "escolha entre trocar ou remover a foto", http.StatusBadRequest)
		return
	}
	if err := h.profiles.Update(r.Context(), viewer.ID, update); err != nil {
		if errors.Is(err, db.ErrUsernameChangeCooldown) {
			http.Error(w, "você só pode trocar o usuário uma vez por mês", http.StatusBadRequest)
			return
		}
		if errors.Is(err, db.ErrUsernameTaken) {
			http.Error(w, "esse usuário já está em uso", http.StatusBadRequest)
			return
		}
		if strings.Contains(err.Error(), "usuário deve ter") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "não foi possível salvar o perfil", http.StatusInternalServerError)
		return
	}
	updated, err := h.profiles.ByUser(r.Context(), viewer.ID)
	if err != nil || updated == nil {
		http.Error(w, "perfil atualizado, mas não foi possível abrir o novo endereço", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, profileRedirectPath(updated.Slug)+"?updated=1", http.StatusSeeOther)
}

func validateProfileImage(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("foto vazia")
	}
	mime := http.DetectContentType(data)
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/webp" {
		return "", fmt.Errorf("formato de foto inválido; use PNG, JPEG ou WebP")
	}
	width, height, ok := profileImageDimensions(mime, data)
	if !ok || width <= 0 || height <= 0 || width > profileMaxImageSide || height > profileMaxImageSide {
		return "", fmt.Errorf("dimensões de foto inválidas")
	}
	return mime, nil
}

func profileImageDimensions(mime string, data []byte) (int, int, bool) {
	if mime == "image/png" || mime == "image/jpeg" {
		config, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return 0, 0, false
		}
		return config.Width, config.Height, true
	}
	return webpDimensions(data)
}

func webpDimensions(data []byte) (int, int, bool) {
	if len(data) < 20 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	chunk := string(data[12:16])
	switch chunk {
	case "VP8X":
		if len(data) < 30 {
			return 0, 0, false
		}
		return 1 + readLE24(data[24:27]), 1 + readLE24(data[27:30]), true
	case "VP8L":
		if len(data) < 25 || data[20] != 0x2f {
			return 0, 0, false
		}
		bits := uint32(data[21]) | uint32(data[22])<<8 | uint32(data[23])<<16 | uint32(data[24])<<24
		return 1 + int(bits&0x3fff), 1 + int((bits>>14)&0x3fff), true
	case "VP8 ":
		if len(data) < 30 || data[23] != 0x9d || data[24] != 0x01 || data[25] != 0x2a {
			return 0, 0, false
		}
		return int(binary.LittleEndian.Uint16(data[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(data[28:30]) & 0x3fff), true
	default:
		return 0, 0, false
	}
}

func readLE24(data []byte) int {
	return int(data[0]) | int(data[1])<<8 | int(data[2])<<16
}
