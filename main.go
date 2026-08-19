package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"golang.org/x/time/rate"

	"learnix/internal/auth"
	"learnix/internal/components"
	"learnix/internal/db"
	"learnix/internal/handlers"
	"learnix/internal/ratelimit"
	"learnix/internal/session"
)

// envOr returns the env var value if the flag value is empty and the env var
// is set; otherwise the flag value wins. Env names mirror run.sh.
func envOr(flagVal, envName string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envName)
}

func boolEnvOr(flagVal bool, envName string) bool {
	if flagVal {
		return true
	}
	switch os.Getenv(envName) {
	case "true", "1":
		return true
	}
	return false
}

func resolveDatabasePath(path string) string {
	if path != "learnix.db" {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	if _, err := os.Stat("twstix.db"); err == nil {
		log.Println("Using legacy twstix.db; rename it to learnix.db when convenient.")
		return "twstix.db"
	}
	return path
}

// securityHeaders adds baseline hardening headers to every response. The CSP
// assumes all scripts are self-hosted (static/vendor + app.js) and that no
// inline script or inline event handlers remain in the markup. 'unsafe-eval'
// is required by Alpine's expression evaluator (it has no CSP build);
// third-party script sources and cross-origin fetch/XHR stay blocked, which
// still cuts the main exfiltration paths. Google Fonts is the only external
// origin, limited to styles and fonts.
func securityHeaders(next http.Handler) http.Handler {
	csp := "default-src 'self'; " +
		"script-src 'self' 'unsafe-eval'; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' https://fonts.gstatic.com; " +
		"img-src 'self' data: blob:; " +
		"connect-src 'self'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"frame-ancestors 'none'; " +
		"object-src 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// maxBody caps request bodies at 1 MiB so no endpoint can be fed unbounded
// data (memory abuse / DB bloat). The image upload route is exempt: it
// enforces its own larger limit in the handler.
func maxBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profileUpload := r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/profile/")
		if !strings.HasSuffix(r.URL.Path, "/files/upload") && !profileUpload {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}

func main() {
	model := flag.String("model", "", "Pre-configured model (e.g. openai/gpt-4o-mini)")
	apiKey := flag.String("api-key", "", "Pre-configured API key")
	baseURL := flag.String("base-url", "", "Pre-configured base URL (default: OpenRouter)")
	port := flag.String("port", "8080", "Listen port")
	dbPath := flag.String("db", "", "SQLite database path")
	sessionSecret := flag.String("session-secret", "", "Secret for signing session cookies (random if unset)")
	tavilyKey := flag.String("tavily-key", "", "Tavily API key for AI web search")
	adminEmail := flag.String("admin-email", "", "Email of the admin account (enables the /admin quota panel)")
	debug := flag.Bool("debug", false, "Debug mode: clients may disable web research for chat and quiz (web=false)")
	flag.Parse()

	*model = envOr(*model, "MODEL")
	*apiKey = envOr(*apiKey, "API_KEY")
	*baseURL = envOr(*baseURL, "BASE_URL")
	*port = envOr(*port, "PORT")
	*dbPath = envOr(*dbPath, "DB_PATH")
	if *dbPath == "" {
		*dbPath = "learnix.db"
	}
	*dbPath = resolveDatabasePath(*dbPath)
	*sessionSecret = envOr(*sessionSecret, "SESSION_SECRET")
	*tavilyKey = envOr(*tavilyKey, "TAVILY_API_KEY")
	*adminEmail = envOr(*adminEmail, "ADMIN_EMAIL")
	*debug = boolEnvOr(*debug, "DEBUG")
	components.BuildVersion = os.Getenv("LEARNIX_BUILD_VERSION")
	if components.BuildVersion == "" {
		components.BuildVersion = "dev"
	}

	secret := *sessionSecret
	if secret == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("generate session secret: %v", err)
		}
		secret = hex.EncodeToString(b)
		log.Println("WARNING: -session-secret not set; sessions will not survive restart. Set it explicitly for persistence.")
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer database.Close()
	if err := db.Migrate(database); err != nil {
		log.Fatalf("migrate db: %v", err)
	}

	preset := session.Config{
		Model:   *model,
		APIKey:  *apiKey,
		BaseURL: *baseURL,
	}

	users := db.NewUserRepo(database)
	sessions := db.NewSessionRepo(database)
	if err := sessions.PurgeExpired(context.Background()); err != nil {
		log.Printf("purge expired sessions: %v", err)
	}
	configs := db.NewConfigRepo(database)
	studies := db.NewStudyRepo(database)
	tests := db.NewTestRepo(database)
	quizzes := db.NewQuizRepo(database)
	files := db.NewFileRepo(database)
	chats := db.NewChatRepo(database)
	chatTurns := db.NewChatTurnRepo(database)
	highlights := db.NewHighlightRepo(database)
	quotas := db.NewQuotaRepo(database)
	profiles := db.NewProfileRepo(database)
	telemetry := db.NewTelemetryRepo(database)
	leaderboard := db.NewLeaderboardRepo(database)
	mindMaps := db.NewMindMapRepo(database)

	h := handlers.New(preset, users, sessions, configs, studies, tests, quizzes, files, chats, chatTurns, highlights, quotas, profiles, telemetry, leaderboard, mindMaps, secret, *tavilyKey, *adminEmail)
	if err := chatTurns.MarkInterrupted(context.Background()); err != nil {
		log.Printf("chat turns recovery: %v", err)
	}
	h.Debug = *debug
	h.QuizResearchModel = os.Getenv("QUIZ_RESEARCH_MODEL")
	h.QuizAuthorModel = os.Getenv("QUIZ_AUTHOR_MODEL")
	h.QuizReviewModel = os.Getenv("QUIZ_REVIEW_MODEL")
	components.Debug = *debug

	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(maxBody)
	r.Use(auth.CanonicalHostMiddleware(auth.CanonicalHost, auth.LegacyHost, secret, sessions))

	// Abuse guards: auth endpoints (brute force / mass sign-up) and the
	// expensive AI endpoints (each call spends LLM + web-search credits).
	authLimit := ratelimit.New(rate.Every(10*time.Second), 5).Middleware
	aiLimit := ratelimit.New(rate.Every(20*time.Second), 5).Middleware
	// Admin mutations are cheap but sensitive; keep them throttled too.
	adminLimit := ratelimit.New(rate.Every(2*time.Second), 10).Middleware

	fs := http.FileServer(http.Dir("static"))
	static := http.StripPrefix("/static/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		fs.ServeHTTP(w, r)
	}))

	// Public group: login, register, leaderboard and static assets.
	r.Group(func(r chi.Router) {
		r.Handle("/static/*", static)
		r.Get("/version", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(components.BuildVersion))
		})
		r.Get("/login", h.LoginFormPage)
		r.With(authLimit).Post("/login", h.LoginSubmit)
		r.Get("/register", h.RegisterFormPage)
		r.With(authLimit).Post("/register", h.RegisterSubmit)
		profileOptional := func(next http.Handler) http.Handler {
			return auth.OptionalMiddleware(secret, sessions, users, next)
		}
		r.With(profileOptional).Get("/profile/me", h.ProfileMe)
		r.With(profileOptional).Get("/profile/{slug}", h.ProfilePage)
		r.With(profileOptional).Get("/profile/{slug}/avatar", h.ProfileAvatar)
		r.With(profileOptional).Get("/leaderboard", h.LeaderboardPage)
		r.Get("/leaderboard/nota", h.LeaderboardScore)
		r.Get("/leaderboard/quizzes", h.LeaderboardQuizzes)
		r.Get("/leaderboard/tokens", h.LeaderboardTokens)
	})

	// Protected group: everything else requires a valid signed session cookie.
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return auth.Middleware(secret, sessions, users, next)
		})
		r.Get("/", h.Home)
		r.Post("/studies", h.CreateStudy)
		r.Get("/studies/new", h.StudyCreatePage)
		r.Get("/studies", h.StudiesList)
		r.Get("/tests", h.TestsHub)
		r.Get("/tests/new", h.TestCreatePage)
		r.With(aiLimit).Post("/tests", h.TestCreate)
		r.Get("/tests/jobs/{jobID}", h.TestJobStatus)
		r.Get("/tests/{testID}/jobs/{jobID}", h.TestJobStatus)
		r.With(aiLimit).Post("/tests/{testID}/start", h.TestAttemptStart)
		r.Get("/tests/{testID}/attempts/{attemptID}", h.TestAttemptPage)
		r.Post("/tests/{testID}/attempts/{attemptID}/answer", h.TestAttemptAnswer)
		r.Post("/tests/{testID}/attempts/{attemptID}/reanswer", h.TestAttemptReanswer)
		r.Post("/tests/{testID}/attempts/{attemptID}/next", h.TestAttemptNext)
		r.Post("/tests/{testID}/attempts/{attemptID}/goto", h.TestAttemptGoto)
		r.Post("/tests/{testID}/attempts/{attemptID}/flag", h.TestAttemptFlag)
		r.Post("/tests/{testID}/attempts/{attemptID}/result", h.TestAttemptResult)
		r.Post("/tests/{testID}/attempts/{attemptID}/diagnostic", h.TestAttemptDiagnostic)
		r.With(aiLimit).Post("/tests/{testID}/attempts/{attemptID}/tutor", h.TestAttemptTutor)
		r.Post("/tests/{id}/answer", h.TestAnswer)
		r.Post("/tests/{id}/reanswer", h.TestReanswer)
		r.Post("/tests/{id}/next", h.TestNext)
		r.Post("/tests/{id}/goto", h.TestGoto)
		r.Post("/tests/{id}/flag", h.TestFlag)
		r.Post("/tests/{id}/result", h.TestResult)
		r.Post("/tests/{id}/diagnostic", h.TestDiagnostic)
		r.With(aiLimit).Post("/tests/{id}/tutor", h.TestTutor)
		r.Get("/tests/{id}", h.TestPage)
		r.Get("/study/{id}", h.StudyPage)
		r.Get("/study/{id}/workspace", h.StudyWorkspace)
		r.Get("/study/{id}/mapa-mental", h.MindMapPage)
		r.Get("/study/{id}/mapa-mental.json", h.MindMapJSON)
		r.Put("/study/{id}/mapa-mental", h.MindMapUpdate)
		r.Get("/study/{id}/apps", h.LearningAppsPage)
		r.Get("/study/{id}/chats", h.ChatList)
		r.Post("/study/{id}/chats", h.ChatCreate)
		r.Post("/study/{id}/chats/{cid}/rename", h.ChatRename)
		r.Post("/study/{id}/chats/{cid}/delete", h.ChatDelete)
		r.With(aiLimit).Post("/study/{id}/chats/{cid}/turns", h.ChatTurnCreate)
		r.Get("/study/{id}/chats/{cid}/turns/{tid}", h.ChatTurnStatus)
		r.With(aiLimit).Post("/study/{id}/chats/{cid}/turns/{tid}/retry", h.ChatTurnRetry)
		r.With(aiLimit).Post("/study/{id}/chats/{cid}/stream", h.ChatStream)
		r.Post("/study/{id}/chats/{cid}/messages/{mid}/branch", h.ChatBranch)
		r.Post("/study/{id}/chats/{cid}/messages/{mid}/save", h.SaveMessage)
		r.Post("/study/{id}/highlights", h.CreateHighlight)
		r.Get("/study/{id}/saved", h.SavedPanel)
		r.Post("/study/{id}/highlights/{hid}/delete", h.DeleteHighlight)
		r.Post("/study/{id}/chats/{cid}/messages/{mid}/save-to-note", h.SaveToNote)
		r.Get("/study/{id}/files", h.FileList)
		r.Post("/study/{id}/files", h.FileCreate)
		r.Post("/study/{id}/files/upload", h.FileUpload)
		r.Get("/study/{id}/files/{fid}/edit", h.FileEdit)
		r.Get("/study/{id}/files/{fid}/raw", h.FileRaw)
		r.Post("/study/{id}/files/{fid}", h.FileUpdate)
		r.Post("/study/{id}/files/{fid}/delete", h.FileDelete)
		r.Post("/study/{id}/files/{fid}/content", h.FileContent)
		r.Get("/study/{id}/files/{fid}/versions", h.VersionList)
		r.Post("/study/{id}/files/{fid}/versions/{vid}/restore", h.VersionRestore)
		r.Post("/study/{id}/files/{fid}/versions/{vid}/branch", h.VersionBranch)
		r.With(aiLimit).Post("/study/{id}/quiz/start", h.QuizStart)
		r.Get("/study/{id}/quiz/jobs/{jobID}", h.QuizJobStatus)
		r.Post("/study/{id}/quiz/answer", h.QuizAnswer)
		r.Post("/study/{id}/quiz/reanswer", h.QuizReanswer)
		r.Post("/study/{id}/quiz/next", h.QuizNext)
		r.Post("/study/{id}/quiz/goto", h.QuizGoto)
		r.Post("/study/{id}/quiz/flag", h.QuizFlag)
		r.Post("/study/{id}/quiz/result", h.QuizResult)
		r.Post("/study/{id}/quiz/diagnostic", h.QuizDiagnostic)
		r.Post("/study/{id}/reset", h.StudyReset)
		r.Post("/study/{id}/delete", h.DeleteStudy)
		r.Post("/profile/{slug}", h.ProfileUpdate)
		r.Post("/logout", h.Logout)

		// Admin panel: nested inside the auth group so the user is already in
		// context; AdminMiddleware then narrows it to the admin email (404 for
		// everyone else, so the panel's existence never leaks). Mutations are
		// CSRF-protected and rate-limited.
		r.Group(func(r chi.Router) {
			r.Use(func(next http.Handler) http.Handler {
				return auth.AdminMiddleware(*adminEmail, next)
			})
			r.Get("/admin", h.AdminDashboard)
			r.With(adminLimit).Post("/admin/users/{uid}/quota", h.AdminSetQuota)
			r.With(adminLimit).Post("/admin/users/{uid}/reset", h.AdminResetUsage)
			r.With(adminLimit).Post("/admin/users/{uid}/delete", h.AdminDeleteUser)
		})
	})

	addr := ":" + *port
	log.Println("Learnix rodando em http://localhost" + addr)
	log.Fatal(http.ListenAndServe(addr, r))
}
