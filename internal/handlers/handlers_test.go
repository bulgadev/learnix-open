package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"learnix/internal/auth"
	"learnix/internal/db"
	"learnix/internal/session"
)

var testCtx = context.Background()

// adminTestEmail is the admin account used by the test router.
const adminTestEmail = "admin@test.com"

// testEnv wires a full router + handlers against a temp SQLite DB.
type testEnv struct {
	handler     *Handler
	router      *chi.Mux
	secret      string
	users       *db.UserRepo
	sessions    *db.SessionRepo
	configs     *db.ConfigRepo
	studies     *db.StudyRepo
	tests       *db.TestRepo
	quizzes     *db.QuizRepo
	files       *db.FileRepo
	chats       *db.ChatRepo
	chatTurns   *db.ChatTurnRepo
	highlights  *db.HighlightRepo
	quotas      *db.QuotaRepo
	profiles    *db.ProfileRepo
	telemetry   *db.TelemetryRepo
	leaderboard *db.LeaderboardRepo
	mindMaps    *db.MindMapRepo
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	d, cleanup := db.NewTestDB(t)
	t.Cleanup(cleanup)
	secret := randSecret(t)
	users := db.NewUserRepo(d)
	sessions := db.NewSessionRepo(d)
	configs := db.NewConfigRepo(d)
	studies := db.NewStudyRepo(d)
	tests := db.NewTestRepo(d)
	quizzes := db.NewQuizRepo(d)
	files := db.NewFileRepo(d)
	chats := db.NewChatRepo(d)
	chatTurns := db.NewChatTurnRepo(d)
	highlights := db.NewHighlightRepo(d)
	quotas := db.NewQuotaRepo(d)
	profiles := db.NewProfileRepo(d)
	telemetry := db.NewTelemetryRepo(d)
	leaderboard := db.NewLeaderboardRepo(d)
	mindMaps := db.NewMindMapRepo(d)
	te := &testEnv{
		handler:     New(session.Config{}, users, sessions, configs, studies, tests, quizzes, files, chats, chatTurns, highlights, quotas, profiles, telemetry, leaderboard, mindMaps, secret, "", adminTestEmail),
		router:      chi.NewRouter(),
		secret:      secret,
		users:       users,
		sessions:    sessions,
		configs:     configs,
		studies:     studies,
		tests:       tests,
		quizzes:     quizzes,
		files:       files,
		chats:       chats,
		chatTurns:   chatTurns,
		highlights:  highlights,
		quotas:      quotas,
		profiles:    profiles,
		telemetry:   telemetry,
		leaderboard: leaderboard,
		mindMaps:    mindMaps,
	}
	te.router.Get("/login", te.handler.LoginFormPage)
	te.router.Post("/login", te.handler.LoginSubmit)
	te.router.Get("/register", te.handler.RegisterFormPage)
	te.router.Get("/leaderboard/nota", te.handler.LeaderboardScore)
	te.router.Get("/leaderboard/quizzes", te.handler.LeaderboardQuizzes)
	te.router.Get("/leaderboard/tokens", te.handler.LeaderboardTokens)
	profileOptional := func(next http.Handler) http.Handler {
		return auth.OptionalMiddleware(secret, sessions, users, next)
	}
	te.router.With(profileOptional).Get("/leaderboard", te.handler.LeaderboardPage)
	te.router.With(profileOptional).Get("/profile/me", te.handler.ProfileMe)
	te.router.With(profileOptional).Get("/profile/{slug}", te.handler.ProfilePage)
	te.router.With(profileOptional).Get("/profile/{slug}/avatar", te.handler.ProfileAvatar)
	te.router.Post("/register", te.handler.RegisterSubmit)
	te.router.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return auth.Middleware(secret, sessions, users, next)
		})
		r.Get("/", te.handler.Home)
		r.Post("/studies", te.handler.CreateStudy)
		r.Get("/studies/new", te.handler.StudyCreatePage)
		r.Get("/studies", te.handler.StudiesList)
		r.Get("/tests", te.handler.TestsHub)
		r.Get("/tests/new", te.handler.TestCreatePage)
		r.Post("/tests", te.handler.TestCreate)
		r.Get("/tests/jobs/{jobID}", te.handler.TestJobStatus)
		r.Get("/tests/{testID}/jobs/{jobID}", te.handler.TestJobStatus)
		r.Post("/tests/{testID}/start", te.handler.TestAttemptStart)
		r.Get("/tests/{testID}/attempts/{attemptID}", te.handler.TestAttemptPage)
		r.Post("/tests/{testID}/attempts/{attemptID}/answer", te.handler.TestAttemptAnswer)
		r.Post("/tests/{testID}/attempts/{attemptID}/reanswer", te.handler.TestAttemptReanswer)
		r.Post("/tests/{testID}/attempts/{attemptID}/next", te.handler.TestAttemptNext)
		r.Post("/tests/{testID}/attempts/{attemptID}/goto", te.handler.TestAttemptGoto)
		r.Post("/tests/{testID}/attempts/{attemptID}/flag", te.handler.TestAttemptFlag)
		r.Post("/tests/{testID}/attempts/{attemptID}/result", te.handler.TestAttemptResult)
		r.Post("/tests/{testID}/attempts/{attemptID}/diagnostic", te.handler.TestAttemptDiagnostic)
		r.Post("/tests/{testID}/attempts/{attemptID}/tutor", te.handler.TestAttemptTutor)
		r.Post("/tests/{id}/answer", te.handler.TestAnswer)
		r.Post("/tests/{id}/reanswer", te.handler.TestReanswer)
		r.Post("/tests/{id}/next", te.handler.TestNext)
		r.Post("/tests/{id}/goto", te.handler.TestGoto)
		r.Post("/tests/{id}/flag", te.handler.TestFlag)
		r.Post("/tests/{id}/result", te.handler.TestResult)
		r.Post("/tests/{id}/diagnostic", te.handler.TestDiagnostic)
		r.Post("/tests/{id}/tutor", te.handler.TestTutor)
		r.Get("/tests/{id}", te.handler.TestPage)
		r.Get("/study/{id}", te.handler.StudyPage)
		r.Get("/study/{id}/workspace", te.handler.StudyWorkspace)
		r.Get("/study/{id}/mapa-mental", te.handler.MindMapPage)
		r.Get("/study/{id}/mapa-mental.json", te.handler.MindMapJSON)
		r.Put("/study/{id}/mapa-mental", te.handler.MindMapUpdate)
		r.Get("/study/{id}/apps", te.handler.LearningAppsPage)
		r.Get("/study/{id}/chats", te.handler.ChatList)
		r.Post("/study/{id}/chats", te.handler.ChatCreate)
		r.Post("/study/{id}/chats/{cid}/rename", te.handler.ChatRename)
		r.Post("/study/{id}/chats/{cid}/delete", te.handler.ChatDelete)
		r.Post("/study/{id}/chats/{cid}/turns", te.handler.ChatTurnCreate)
		r.Get("/study/{id}/chats/{cid}/turns/{tid}", te.handler.ChatTurnStatus)
		r.Post("/study/{id}/chats/{cid}/turns/{tid}/retry", te.handler.ChatTurnRetry)
		r.Post("/study/{id}/chats/{cid}/stream", te.handler.ChatStream)
		r.Post("/study/{id}/chats/{cid}/messages/{mid}/branch", te.handler.ChatBranch)
		r.Post("/study/{id}/chats/{cid}/messages/{mid}/save", te.handler.SaveMessage)
		r.Post("/study/{id}/highlights", te.handler.CreateHighlight)
		r.Get("/study/{id}/saved", te.handler.SavedPanel)
		r.Post("/study/{id}/highlights/{hid}/delete", te.handler.DeleteHighlight)
		r.Post("/study/{id}/chats/{cid}/messages/{mid}/save-to-note", te.handler.SaveToNote)
		r.Get("/study/{id}/files", te.handler.FileList)
		r.Post("/study/{id}/files", te.handler.FileCreate)
		r.Post("/study/{id}/files/upload", te.handler.FileUpload)
		r.Get("/study/{id}/files/{fid}/edit", te.handler.FileEdit)
		r.Get("/study/{id}/files/{fid}/raw", te.handler.FileRaw)
		r.Post("/study/{id}/files/{fid}", te.handler.FileUpdate)
		r.Post("/study/{id}/files/{fid}/delete", te.handler.FileDelete)
		r.Post("/study/{id}/files/{fid}/content", te.handler.FileContent)
		r.Get("/study/{id}/files/{fid}/versions", te.handler.VersionList)
		r.Post("/study/{id}/files/{fid}/versions/{vid}/restore", te.handler.VersionRestore)
		r.Post("/study/{id}/files/{fid}/versions/{vid}/branch", te.handler.VersionBranch)
		r.Post("/study/{id}/quiz/start", te.handler.QuizStart)
		r.Get("/study/{id}/quiz/jobs/{jobID}", te.handler.QuizJobStatus)
		r.Post("/study/{id}/quiz/answer", te.handler.QuizAnswer)
		r.Post("/study/{id}/quiz/reanswer", te.handler.QuizReanswer)
		r.Post("/study/{id}/quiz/next", te.handler.QuizNext)
		r.Post("/study/{id}/quiz/goto", te.handler.QuizGoto)
		r.Post("/study/{id}/quiz/flag", te.handler.QuizFlag)
		r.Post("/study/{id}/quiz/result", te.handler.QuizResult)
		r.Post("/study/{id}/quiz/diagnostic", te.handler.QuizDiagnostic)
		r.Post("/study/{id}/reset", te.handler.StudyReset)
		r.Post("/study/{id}/delete", te.handler.DeleteStudy)
		r.Post("/profile/{slug}", te.handler.ProfileUpdate)
		r.Post("/logout", te.handler.Logout)
		r.Group(func(r chi.Router) {
			r.Use(func(next http.Handler) http.Handler {
				return auth.AdminMiddleware(adminTestEmail, next)
			})
			r.Get("/admin", te.handler.AdminDashboard)
			r.Post("/admin/users/{uid}/quota", te.handler.AdminSetQuota)
			r.Post("/admin/users/{uid}/reset", te.handler.AdminResetUsage)
			r.Post("/admin/users/{uid}/delete", te.handler.AdminDeleteUser)
		})
	})
	return te
}

// csrfToken derives the CSRF token for a session cookie the same way the
// handlers do, so tests can submit valid admin forms.
func (te *testEnv) csrfToken(t *testing.T, cookie *http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(cookie)
	tok := auth.CSRFToken(req, te.secret)
	if tok == "" {
		t.Fatal("could not derive CSRF token from test cookie")
	}
	return tok
}

// grantQuota grants the user behind the cookie an n-token allowance.
func (te *testEnv) grantQuota(t *testing.T, cookie *http.Cookie, n int64) {
	t.Helper()
	sid, ok := auth.Verify(cookie.Value, te.secret)
	if !ok {
		t.Fatal("grantQuota: invalid test cookie")
	}
	srow, err := te.sessions.Get(testCtx, sid)
	if err != nil || srow == nil {
		t.Fatalf("grantQuota: session lookup: %v", err)
	}
	if err := te.quotas.SetQuota(testCtx, srow.UserID, n); err != nil {
		t.Fatalf("grantQuota: %v", err)
	}
}

func randSecret(t *testing.T) string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (te *testEnv) req(t *testing.T, method, path string, body string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rr := httptest.NewRecorder()
	te.router.ServeHTTP(rr, req)
	return rr
}

// register creates a user and returns the session cookie.
func (te *testEnv) register(t *testing.T, email, pw string) *http.Cookie {
	t.Helper()
	rr := te.req(t, "POST", "/register", fmt.Sprintf("email=%s&password=%s&confirm=%s", email, pw, pw))
	if rr.Code != http.StatusFound {
		t.Fatalf("register %s: expected 302, got %d body=%s", email, rr.Code, rr.Body.String())
	}
	return getCookie(rr)
}

// createStudy posts the new-study form and returns the redirect target (e.g. "/study/1").
func (te *testEnv) createStudy(t *testing.T, topic string, cookie *http.Cookie) string {
	t.Helper()
	rr := te.req(t, "POST", "/studies", "topic="+topic+"&model=openai/gpt-4o-mini&base_url=https://openrouter.ai/api/v1", cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("create study: expected 302, got %d body=%s", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/study/") {
		t.Fatalf("expected redirect to /study/{id}, got %s", loc)
	}
	return loc
}

func getCookie(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	return nil
}

// ---- Auth tests ----

func TestRegister_NewUser(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "POST", "/register", "email=new@test.com&password=hunter2!&confirm=hunter2!")
	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rr.Code, rr.Body.String())
	}
	if rr.Header().Get("Location") != "/" {
		t.Errorf("expected redirect to /, got %s", rr.Header().Get("Location"))
	}
	if getCookie(rr) == nil {
		t.Error("no session cookie set")
	}
}

func TestRegister_DuplicateEmail(t *testing.T) {
	te := newTestEnv(t)
	te.register(t, "dup@test.com", "hunter2!")
	rr := te.req(t, "POST", "/register", "email=dup@test.com&password=hunter2!&confirm=hunter2!")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 (re-render), got %d", rr.Code)
	}
	// Generic error only: the response must not reveal the email exists.
	body := rr.Body.String()
	if !strings.Contains(body, "Não foi possível criar a conta") {
		t.Errorf("body should contain generic error, got: %s", body)
	}
	if strings.Contains(body, "já cadastrado") {
		t.Error("duplicate-email response must not enumerate accounts")
	}
}

func TestRegister_BadEmail(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "POST", "/register", "email=notanemail&password=hunter2!&confirm=hunter2!")
	if !strings.Contains(rr.Body.String(), "Email inválido") {
		t.Errorf("expected email error, got: %s", rr.Body.String())
	}
}

func TestRegister_ShortPassword(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "POST", "/register", "email=short@test.com&password=abc&confirm=abc")
	if !strings.Contains(rr.Body.String(), "8 caracteres") {
		t.Errorf("expected short pw error, got: %s", rr.Body.String())
	}
}

func TestRegister_PasswordMismatch(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "POST", "/register", "email=mm@test.com&password=hunter2!&confirm=hunter3!")
	if !strings.Contains(rr.Body.String(), "não conferem") {
		t.Errorf("expected mismatch error, got: %s", rr.Body.String())
	}
}

func TestLogin_OK(t *testing.T) {
	te := newTestEnv(t)
	te.register(t, "login@test.com", "hunter2!")
	rr := te.req(t, "POST", "/login", "email=login@test.com&password=hunter2!")
	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "/" {
		t.Errorf("expected redirect to /, got %s", rr.Header().Get("Location"))
	}
	if getCookie(rr) == nil {
		t.Error("no session cookie")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	te := newTestEnv(t)
	te.register(t, "wrong@test.com", "hunter2!")
	rr := te.req(t, "POST", "/login", "email=wrong@test.com&password=wrong!")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "inválidos") {
		t.Errorf("expected invalid message, got: %s", rr.Body.String())
	}
	if getCookie(rr) != nil {
		t.Error("should not set cookie on fail")
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "POST", "/login", "email=ghost@test.com&password=hunter2!")
	if !strings.Contains(rr.Body.String(), "inválidos") {
		t.Errorf("expected invalid message, got: %s", rr.Body.String())
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "out@test.com", "hunter2!")
	rr := te.req(t, "POST", "/logout", "", cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "/login" {
		t.Errorf("expected redirect to /login, got %s", rr.Header().Get("Location"))
	}
	setCookie := rr.Header().Get("Set-Cookie")
	if !strings.Contains(setCookie, "Max-Age=0") && !strings.Contains(setCookie, "learnix_sid=;") {
		t.Errorf("cookie not cleared: %s", setCookie)
	}
}

func TestAuthMiddleware_LegacyCookieMigrates(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "legacy@test.com", "hunter2!")
	legacy := &http.Cookie{Name: auth.LegacyCookieName, Value: cookie.Value}
	rr := te.req(t, "GET", "/", "", legacy)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected legacy cookie to authenticate, got %d: %s", rr.Code, rr.Body.String())
	}
	var migrated bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == auth.CookieName {
			migrated = true
		}
	}
	if !migrated {
		t.Error("legacy session should receive the Learnix cookie")
	}
}

// ---- Middleware / route guard tests ----

func TestAuthMiddleware_UnauthedRedirects(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "GET", "/", "")
	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d", rr.Code)
	}
	if rr.Header().Get("Location") != "/login" {
		t.Errorf("expected redirect to /login, got %s", rr.Header().Get("Location"))
	}
}

func TestAuthMiddleware_TamperedCookie(t *testing.T) {
	te := newTestEnv(t)
	rr := te.req(t, "GET", "/", "", &http.Cookie{Name: auth.CookieName, Value: "garbage.notsigned"})
	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302 for tampered, got %d", rr.Code)
	}
}

func TestAuthMiddleware_ValidCookie(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "valid@test.com", "hunter2!")
	rr := te.req(t, "GET", "/", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAuthMiddleware_PublicRoutesWork(t *testing.T) {
	te := newTestEnv(t)
	for _, p := range []string{"/login", "/register"} {
		rr := te.req(t, "GET", p, "")
		if rr.Code != http.StatusOK {
			t.Errorf("%s: expected 200, got %d", p, rr.Code)
		}
	}
}

// ---- Study navigation tests ----

func TestCreateStudy_RedirectsToStudyPage(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "create@test.com", "hunter2!")
	loc := te.createStudy(t, "logaritmos", cookie)

	rr := te.req(t, "GET", loc, "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "logaritmos") {
		t.Errorf("study page should contain topic, got: %s", rr.Body.String()[:300])
	}
}

func TestCreateStudy_Persists(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "cfg@test.com", "hunter2!")
	te.createStudy(t, "logaritmos", cookie)

	u, _ := te.users.ByEmail(testCtx, "cfg@test.com")
	studies, err := te.studies.ListByUser(testCtx, u.ID)
	if err != nil || len(studies) != 1 {
		t.Fatalf("study not persisted: %v %d", err, len(studies))
	}
	if studies[0].Topic != "logaritmos" {
		t.Errorf("topic mismatch: %+v", studies[0])
	}
}

// Users cannot point studies at their own endpoint/key anymore: any supplied
// base_url/api_key/model fields are ignored and the study stays on the
// server preset (blank columns resolved by effectiveConfig).
func TestCreateStudy_IgnoresUserSuppliedEndpoint(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "ownep@test.com", "hunter2!")
	rr := te.req(t, "POST", "/studies",
		"topic=t&base_url=https://evil.example/v1&api_key=sk-user&model=whatever", cookie)
	if rr.Code != http.StatusFound {
		t.Fatalf("expected 302, got %d: %s", rr.Code, rr.Body.String())
	}
	u, _ := te.users.ByEmail(testCtx, "ownep@test.com")
	studies, _ := te.studies.ListByUser(testCtx, u.ID)
	if len(studies) != 1 {
		t.Fatalf("expected 1 study, got %d", len(studies))
	}
	st, _ := te.studies.Get(testCtx, studies[0].ID)
	if st.BaseURL != "" || st.APIKey != "" || st.Model != "" {
		t.Errorf("user-supplied endpoint/key/model must be ignored, got %+v", st)
	}
}

func TestHome_NeverRedirects(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "home@test.com", "hunter2!")
	te.createStudy(t, "exponenciais", cookie)

	rr := te.req(t, "GET", "/", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("home should be 200 even with an active study, got %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Novo estudo") {
		t.Errorf("home should show the new-study form, got: %s", body[:300])
	}
	if !strings.Contains(body, "exponenciais") {
		t.Errorf("home should list the existing study, got: %s", body[:500])
	}
}

func TestHome_FreshUserShowsForm(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "fresh@test.com", "hunter2!")
	rr := te.req(t, "GET", "/", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "Estudar") {
		t.Errorf("expected new-study form, got: %s", rr.Body.String()[:300])
	}
}

func TestStudyPage_OtherUser_404(t *testing.T) {
	te := newTestEnv(t)
	cookieA := te.register(t, "owner@test.com", "hunter2!")
	cookieB := te.register(t, "intruder@test.com", "hunter2!")

	loc := te.createStudy(t, "privado", cookieA)

	rrB := te.req(t, "GET", loc, "", cookieB)
	if rrB.Code != http.StatusNotFound {
		t.Errorf("expected 404 for other user's study, got %d", rrB.Code)
	}

	rrA := te.req(t, "GET", loc, "", cookieA)
	if rrA.Code != http.StatusOK {
		t.Errorf("expected 200 for owner, got %d", rrA.Code)
	}
}

func TestStudyPage_Missing_404(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "missing@test.com", "hunter2!")
	rr := te.req(t, "GET", "/study/9999", "", cookie)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestStudiesList_OnlyOwn(t *testing.T) {
	te := newTestEnv(t)
	cookieA := te.register(t, "a@list.com", "hunter2!")
	cookieB := te.register(t, "b@list.com", "hunter2!")

	te.createStudy(t, "segredo-A", cookieA)

	rrB := te.req(t, "GET", "/studies", "", cookieB)
	if strings.Contains(rrB.Body.String(), "segredo-A") {
		t.Errorf("B should not see A's study")
	}

	rrA := te.req(t, "GET", "/studies", "", cookieA)
	if !strings.Contains(rrA.Body.String(), "segredo-A") {
		t.Errorf("A should see own study, body: %s", rrA.Body.String()[:300])
	}
}

func TestDeleteStudy_RemovesAndRedirectsHome(t *testing.T) {
	te := newTestEnv(t)
	cookie := te.register(t, "del@test.com", "hunter2!")
	loc := te.createStudy(t, "temporario", cookie)
	id := strings.TrimPrefix(loc, "/study/")

	rr := te.req(t, "POST", loc+"/delete", "", cookie)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/" {
		t.Fatalf("expected redirect home, got %d %s", rr.Code, rr.Header().Get("Location"))
	}

	rrGet := te.req(t, "GET", loc, "", cookie)
	if rrGet.Code != http.StatusNotFound {
		t.Errorf("deleted study should 404, got %d", rrGet.Code)
	}
	_ = id
}

// ---- Security regression tests ----

// C1: the server preset API key must never be rendered into the home page —
// any registered user could otherwise read it from the HTML.
func TestHome_DoesNotLeakPresetAPIKey(t *testing.T) {
	te := newTestEnv(t)
	te.handler.Preset = session.Config{
		APIKey:  "sk-server-secret",
		BaseURL: "https://openrouter.ai/api/v1",
		Model:   "openai/gpt-4o-mini",
	}
	cookie := te.register(t, "leak@test.com", "hunter2!")
	rr := te.req(t, "GET", "/", "", cookie)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "sk-server-secret") {
		t.Error("home page leaks the server preset API key")
	}
}

// C2: the server preset key must only be attached to requests going to the
// preset endpoint, never to a user-supplied base URL (SSRF key exfiltration).
func TestEffectiveConfig_PresetKeyOnlyForPresetEndpoint(t *testing.T) {
	te := newTestEnv(t)
	te.handler.Preset = session.Config{
		APIKey:  "sk-server-secret",
		BaseURL: "https://openrouter.ai/api/v1",
		Model:   "openai/gpt-4o-mini",
	}

	foreign := &session.Session{Config: session.Config{BaseURL: "https://evil.example/v1"}}
	if cfg := te.handler.effectiveConfig(foreign); cfg.APIKey != "" {
		t.Errorf("preset key attached to foreign base URL: %+v", cfg)
	}

	preset := &session.Session{Config: session.Config{}}
	if cfg := te.handler.effectiveConfig(preset); cfg.APIKey != "sk-server-secret" {
		t.Errorf("preset key should apply when using the preset endpoint, got %+v", cfg)
	}

	explicit := &session.Session{Config: session.Config{BaseURL: "https://evil.example/v1", APIKey: "sk-user"}}
	if cfg := te.handler.effectiveConfig(explicit); cfg.APIKey != "sk-user" {
		t.Errorf("user's own key should be kept, got %+v", cfg)
	}
}

// Emails are canonicalized to lowercase, so a case variant of an existing
// account is a duplicate — closing the admin-takeover path where a second
// account registered as a case variant of ADMIN_EMAIL folded to the same
// identity and inherited the panel.
func TestRegister_CaseVariantIsDuplicate(t *testing.T) {
	te := newTestEnv(t)
	te.register(t, "boss@test.com", "hunter2!")

	rr := te.req(t, "POST", "/register", "email=BOSS%40TEST.COM&password=hunter2!&confirm=hunter2!")
	if rr.Code == http.StatusFound {
		t.Fatal("case-variant registration must not create a second account")
	}
	all, err := te.quotas.ListAll(testCtx)
	if err != nil || len(all) != 1 {
		t.Fatalf("expected exactly one user, got %d (%v)", len(all), err)
	}
}

// Login is case-insensitive because stored emails are lowercase.
func TestLogin_EmailCaseInsensitive(t *testing.T) {
	te := newTestEnv(t)
	te.register(t, "mixed@test.com", "hunter2!")
	rr := te.req(t, "POST", "/login", "email=MIXED%40TEST.COM&password=hunter2!")
	if rr.Code != http.StatusFound {
		t.Fatalf("mixed-case login should succeed, got %d", rr.Code)
	}
}

// suppress unused import
var _ = bytes.NewBuffer
