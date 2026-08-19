package handlers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/a-h/templ"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
)

// authRender renders a component for the auth pages (public, no auth context).
func authRender(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// RegisterFormPage shows the registration form.
func (h *Handler) RegisterFormPage(w http.ResponseWriter, r *http.Request) {
	authRender(w, r, components.RegisterForm(""))
}

// RegisterSubmit handles POST /register.
func (h *Handler) RegisterSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		authRender(w, r, components.RegisterForm("Formulário inválido."))
		return
	}
	// Emails are canonicalized to lowercase so UNIQUE(email) holds across
	// case variants — otherwise a second account registered as a case variant
	// of ADMIN_EMAIL would fold to the same identity and inherit the panel.
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")
	confirm := r.FormValue("confirm")

	if err := auth.ValidateEmail(email); err != nil {
		authRender(w, r, components.RegisterForm("Email inválido."))
		return
	}
	if err := auth.ValidatePassword(password); err != nil {
		authRender(w, r, components.RegisterForm("Senha precisa ter ao menos 8 caracteres."))
		return
	}
	if password != confirm {
		authRender(w, r, components.RegisterForm("As senhas não conferem."))
		return
	}

	existing, err := h.users.ByEmail(r.Context(), email)
	if err != nil {
		authRender(w, r, components.RegisterForm("Erro interno. Tente novamente."))
		return
	}
	if existing != nil {
		// Generic message: do not reveal that the email is already registered.
		authRender(w, r, components.RegisterForm("Não foi possível criar a conta. Verifique os dados e tente novamente."))
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		authRender(w, r, components.RegisterForm("Erro ao processar senha."))
		return
	}
	uid, err := h.users.Create(r.Context(), email, hash)
	if err != nil {
		authRender(w, r, components.RegisterForm("Não foi possível criar a conta. Verifique os dados e tente novamente."))
		return
	}
	if err := h.quotas.SetQuota(r.Context(), uid, defaultNewUserQuota); err != nil {
		_ = h.users.Delete(r.Context(), uid)
		authRender(w, r, components.RegisterForm("Não foi possível criar a conta. Verifique os dados e tente novamente."))
		return
	}
	h.recordTelemetry(r.Context(), db.TelemetryEvent{
		UserID: uid, Type: telemetryUserRegistered,
	})

	sid := auth.NewSessionID()
	if err := h.sessions.Create(r.Context(), sid, uid); err != nil {
		_ = h.users.Delete(r.Context(), uid)
		authRender(w, r, components.RegisterForm("Erro ao criar sessão."))
		return
	}
	auth.SetSessionCookie(w, r, h.sessionSecret, sid)
	http.Redirect(w, r, "/", http.StatusFound)
}

// LoginFormPage shows the login form.
func (h *Handler) LoginFormPage(w http.ResponseWriter, r *http.Request) {
	authRender(w, r, components.LoginForm(""))
}

// LoginSubmit handles POST /login.
func (h *Handler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		authRender(w, r, components.LoginForm("Formulário inválido."))
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.FormValue("email")))
	password := r.FormValue("password")

	u, err := h.users.ByEmail(r.Context(), email)
	if err != nil {
		authRender(w, r, components.LoginForm("Email ou senha inválidos."))
		return
	}
	if u == nil {
		// Spend the same time a real verification would so response timing
		// does not reveal whether the account exists.
		auth.BlindVerify(password)
		authRender(w, r, components.LoginForm("Email ou senha inválidos."))
		return
	}
	if err := auth.VerifyPassword(u.PasswordHash, password); err != nil {
		authRender(w, r, components.LoginForm("Email ou senha inválidos."))
		return
	}

	sid := auth.NewSessionID()
	if err := h.sessions.Create(r.Context(), sid, u.ID); err != nil {
		authRender(w, r, components.LoginForm("Erro ao criar sessão."))
		return
	}
	h.recordTelemetry(r.Context(), db.TelemetryEvent{UserID: u.ID, Type: telemetryUserLogin})
	auth.SetSessionCookie(w, r, h.sessionSecret, sid)
	http.Redirect(w, r, "/", http.StatusFound)
}

// Logout handles POST /logout.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if value, ok := auth.SessionID(r, h.sessionSecret); ok {
		_ = h.sessions.Delete(r.Context(), value)
	}
	auth.ClearSessionCookie(w, r)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ensure errors is referenced (used for sentinel comparison if needed later)
var _ = errors.New
