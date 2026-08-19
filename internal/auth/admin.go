package auth

import (
	"net/http"
	"strings"
)

// AdminMiddleware lets only the user whose email matches the configured
// admin email through; everyone else gets 404 so the panel's existence is
// never leaked. An empty adminEmail disables the panel entirely. It must run
// after Middleware (it reads the user from the request context).
func AdminMiddleware(adminEmail string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r.Context())
		if u == nil || adminEmail == "" || !strings.EqualFold(u.Email, adminEmail) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
