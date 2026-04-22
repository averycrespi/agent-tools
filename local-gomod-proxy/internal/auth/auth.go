// Package auth enforces HTTP Basic authentication on every request reaching
// the proxy. Credentials come from internal/state; see DESIGN.md for the
// trust model.
package auth

import (
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/averycrespi/agent-tools/local-gomod-proxy/internal/state"
)

const realm = "local-gomod-proxy"

// Middleware wraps next with HTTP Basic auth enforcement. Requests missing or
// carrying bad credentials are rejected with 401 before reaching next.
func Middleware(next http.Handler, creds state.Credentials) http.Handler {
	wantUser := []byte(creds.Username)
	wantPass := []byte(creds.Password)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || !secureEqual([]byte(user), wantUser) || !secureEqual([]byte(pass), wantPass) {
			// Log only the remote address — never the Authorization header or
			// any user-supplied bytes, which could include the password.
			slog.Warn("auth failed", "remote", r.RemoteAddr)
			w.Header().Set("WWW-Authenticate", `Basic realm="`+realm+`"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// secureEqual compares two byte slices in constant time relative to their
// length. It returns false for length-mismatched inputs (ConstantTimeCompare
// returns 0 in that case, which we report as a mismatch).
func secureEqual(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}
