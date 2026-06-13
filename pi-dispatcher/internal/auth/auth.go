package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/pi-dispatcher/internal/config"
)

const CookieName = "pd-auth"

func TokenPath() string {
	return filepath.Join(config.ConfigDir(), "auth-token")
}

func EnsureToken(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // token path is controlled by XDG/user home.
	if err == nil {
		return string(data), nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return writeNewToken(path)
}

func LoadToken(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // token path is controlled by XDG/user home.
	if err != nil {
		return "", fmt.Errorf("reading token file: %w", err)
	}
	return string(data), nil
}

func RotateToken(path string) (string, error) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("removing token file: %w", err)
	}
	return writeNewToken(path)
}

type LogFunc func(format string, args ...any)

func Middleware(token string, next http.Handler) http.Handler {
	return MiddlewareWithLog(token, next, nil)
}

func MiddlewareWithLog(token string, next http.Handler, logf LogFunc) http.Handler {
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dashboard/unauthorized" {
			logAuth(logf, "auth granted path=%s reason=unauthorized-page", r.URL.Path)
			next.ServeHTTP(w, r)
			return
		}
		if hasBearer(r, tokenBytes) {
			logAuth(logf, "auth granted path=%s reason=bearer", r.URL.Path)
			next.ServeHTTP(w, r)
			return
		}
		if hasCookie(r, tokenBytes) {
			logAuth(logf, "auth granted path=%s reason=cookie", r.URL.Path)
			next.ServeHTTP(w, r)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/dashboard/") || r.URL.Path == "/dashboard" {
			if qToken := r.URL.Query().Get("token"); qToken != "" && subtle.ConstantTimeCompare([]byte(qToken), tokenBytes) == 1 {
				http.SetCookie(w, &http.Cookie{ //nolint:gosec // Local loopback dashboard may run over plain HTTP; cookie is HttpOnly and SameSite.
					Name:     CookieName,
					Value:    token,
					Path:     "/dashboard/",
					HttpOnly: true,
					SameSite: http.SameSiteStrictMode,
					MaxAge:   int((365 * 24 * time.Hour) / time.Second),
				})
				clean := *r.URL
				q := clean.Query()
				q.Del("token")
				clean.RawQuery = q.Encode()
				logAuth(logf, "auth granted path=%s reason=token-url redirect=%s", r.URL.Path, clean.RequestURI())
				http.Redirect(w, r, clean.RequestURI(), http.StatusFound) //nolint:gosec // Redirect target is the same request path with only the token query removed.
				return
			}
			logAuth(logf, "auth rejected path=%s reason=missing-or-invalid-token status=%d", r.URL.Path, http.StatusFound)
			http.Redirect(w, r, "/dashboard/unauthorized", http.StatusFound)
			return
		}

		logAuth(logf, "auth rejected path=%s reason=outside-dashboard status=%d", r.URL.Path, http.StatusUnauthorized)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	})
}

func logAuth(logf LogFunc, format string, args ...any) {
	if logf != nil {
		logf(format, args...)
	}
}

func hasBearer(r *http.Request, token []byte) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), token) == 1
}

func hasCookie(r *http.Request, token []byte) bool {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), token) == 1
}

func writeNewToken(path string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating token: %w", err)
	}
	token := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return "", fmt.Errorf("creating token directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("writing token file: %w", err)
	}
	return token, nil
}
