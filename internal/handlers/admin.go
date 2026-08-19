package handlers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"learnix/internal/auth"
	"learnix/internal/components"
)

// adminNotice maps fixed query values to user-facing messages. The map is a
// closed set — nothing from the request is ever reflected into the page.
var adminNotices = map[string]string{
	"quota-saved":  "Cota atualizada.",
	"usage-reset":  "Uso zerado.",
	"user-deleted": "Usuário removido.",
	"self-delete":  "Você não pode remover a própria conta de administrador.",
	"invalid":      "Valor inválido — nada foi alterado.",
	"user-missing": "Usuário não encontrado.",
}

// AdminDashboard renders the user list with quota state plus recent usage.
func (h *Handler) AdminDashboard(w http.ResponseWriter, r *http.Request) {
	users, err := h.quotas.ListAll(r.Context())
	if err != nil {
		http.Error(w, "erro ao carregar usuários", http.StatusInternalServerError)
		return
	}
	recent, _ := h.quotas.RecentUsage(r.Context(), 30)
	u := auth.UserFromContext(r.Context())
	render(w, r, components.AdminDashboard(
		components.AuthedPageData("Admin", "", "", u, h.quotaFor(r.Context(), u), true),
		users, recent,
		auth.CSRFToken(r, h.sessionSecret),
		adminNotices[r.URL.Query().Get("notice")],
	))
}

// adminUIDParam parses the {uid} URL param.
func adminUIDParam(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// adminGuard validates the CSRF token and the target user id for admin
// mutations. On failure it writes the response and reports ok=false.
func (h *Handler) adminGuard(w http.ResponseWriter, r *http.Request) (uid int64, ok bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "formulário inválido", http.StatusBadRequest)
		return 0, false
	}
	if !auth.CSRFValid(r, h.sessionSecret) {
		http.Error(w, "token CSRF inválido", http.StatusBadRequest)
		return 0, false
	}
	uid, ok = adminUIDParam(r)
	if !ok {
		http.NotFound(w, r)
		return 0, false
	}
	return uid, true
}

// adminRedirect returns to the dashboard with a fixed notice key.
func adminRedirect(w http.ResponseWriter, r *http.Request, notice string) {
	http.Redirect(w, r, "/admin?notice="+notice, http.StatusSeeOther)
}

// AdminSetQuota sets the token allowance for one user (quota >= 0).
func (h *Handler) AdminSetQuota(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.adminGuard(w, r)
	if !ok {
		return
	}
	quota, err := strconv.ParseInt(r.FormValue("quota"), 10, 64)
	if err != nil || quota < 0 {
		adminRedirect(w, r, "invalid")
		return
	}
	target, _ := h.users.ByID(r.Context(), uid)
	if target == nil {
		adminRedirect(w, r, "user-missing")
		return
	}
	if err := h.quotas.SetQuota(r.Context(), uid, quota); err != nil {
		adminRedirect(w, r, "invalid")
		return
	}
	admin := auth.UserFromContext(r.Context())
	log.Printf("admin: %s set quota=%d for user %d (%s)", admin.Email, quota, uid, target.Email)
	adminRedirect(w, r, "quota-saved")
}

// AdminResetUsage zeros the user's consumed-token counter (history stays).
func (h *Handler) AdminResetUsage(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.adminGuard(w, r)
	if !ok {
		return
	}
	target, _ := h.users.ByID(r.Context(), uid)
	if target == nil {
		adminRedirect(w, r, "user-missing")
		return
	}
	if err := h.quotas.ResetUsage(r.Context(), uid); err != nil {
		adminRedirect(w, r, "invalid")
		return
	}
	admin := auth.UserFromContext(r.Context())
	log.Printf("admin: %s reset usage for user %d (%s)", admin.Email, uid, target.Email)
	adminRedirect(w, r, "usage-reset")
}

// AdminDeleteUser removes a user and all their data (FK cascade). The admin
// cannot delete their own account.
func (h *Handler) AdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.adminGuard(w, r)
	if !ok {
		return
	}
	admin := auth.UserFromContext(r.Context())
	if uid == admin.ID {
		adminRedirect(w, r, "self-delete")
		return
	}
	target, _ := h.users.ByID(r.Context(), uid)
	if target == nil {
		adminRedirect(w, r, "user-missing")
		return
	}
	if err := h.users.Delete(r.Context(), uid); err != nil {
		adminRedirect(w, r, "invalid")
		return
	}
	log.Printf("admin: %s deleted user %d (%s)", admin.Email, uid, target.Email)
	adminRedirect(w, r, "user-deleted")
}
