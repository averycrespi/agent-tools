package auth

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// Middleware enforces bearer-token auth. Accepts either:
//   - Authorization: Bearer <token>
//   - Authorization: Basic <base64(user:token)>  (user portion is ignored)
func Middleware(token string, next http.Handler) http.Handler {
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided, ok := extractToken(r)
		if !ok || subtle.ConstantTimeCompare(provided, tokenBytes) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractToken(r *http.Request) ([]byte, bool) {
	h := r.Header.Get("Authorization")
	switch {
	case strings.HasPrefix(h, "Bearer "):
		return []byte(strings.TrimPrefix(h, "Bearer ")), true
	case strings.HasPrefix(h, "Basic "):
		if _, pw, ok := r.BasicAuth(); ok {
			return []byte(pw), true
		}
	}
	return nil, false
}
