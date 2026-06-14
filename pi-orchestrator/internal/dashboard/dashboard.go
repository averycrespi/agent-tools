package dashboard

import (
	"crypto/subtle"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/auth"
	"github.com/averycrespi/agent-tools/pi-orchestrator/internal/store"
)

const maxLogBytes = 64 * 1024

//go:embed index.html
var indexHTML []byte

//go:embed favicon.svg
var faviconSVG []byte

var eventPollInterval = time.Second

func NewHandler(db *store.Store, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/dashboard/api/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		summaries, err := db.ListWorkflowRunSummaries(r.Context())
		writeJSON(w, summaries, err)
	})
	mux.HandleFunc("/dashboard/events", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, _ := w.(http.Flusher)
		send := func() bool {
			summaries, err := db.ListWorkflowRunSummaries(r.Context())
			if err != nil {
				_, _ = fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
				return false
			}
			data, err := json.Marshal(summaries)
			if err != nil {
				_, _ = fmt.Fprintf(w, "event: error\ndata: %q\n\n", err.Error())
				return false
			}
			_, _ = w.Write([]byte("event: snapshot\n"))
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(data)
			_, _ = w.Write([]byte("\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
			return true
		}
		if !send() {
			return
		}
		ticker := time.NewTicker(eventPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				if !send() {
					return
				}
			}
		}
	})
	mux.HandleFunc("/dashboard/api/runs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id, logs, ok := parseRunPath(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if logs {
			writeRunLogs(w, r, db, id)
			return
		}
		detail, err := db.GetWorkflowRunDetail(r.Context(), id)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, detail, err)
	})
	mux.HandleFunc("/dashboard/favicon.svg", dashboardFavicon)
	mux.HandleFunc("/dashboard/", dashboardUI)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
	})
	return authMiddleware(token, mux)
}

func authMiddleware(token string, next http.Handler) http.Handler {
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token != "" && (hasBearer(r, tokenBytes) || hasCookie(r, tokenBytes)) {
			next.ServeHTTP(w, r)
			return
		}
		if token != "" && hasQueryToken(r, tokenBytes) && strings.HasPrefix(r.URL.Path, "/dashboard/") {
			http.SetCookie(w, &http.Cookie{ //nolint:gosec // Local loopback dashboard may run over plain HTTP; cookie is HttpOnly and SameSite.
				Name:     auth.CookieName,
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
			http.Redirect(w, r, clean.RequestURI(), http.StatusFound) //nolint:gosec // Redirect target is same path with only the token query removed.
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	})
}

func hasBearer(r *http.Request, token []byte) bool {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(strings.TrimPrefix(auth, "Bearer ")), token) == 1
}

func hasQueryToken(r *http.Request, token []byte) bool {
	value := r.URL.Query().Get("token")
	if value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(value), token) == 1
}

func hasCookie(r *http.Request, token []byte) bool {
	cookie, err := r.Cookie(auth.CookieName)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookie.Value), token) == 1
}

func parseRunPath(path string) (string, bool, bool) {
	suffix := strings.TrimPrefix(path, "/dashboard/api/runs/")
	if suffix == "" {
		return "", false, false
	}
	if strings.HasSuffix(suffix, "/logs") {
		id := strings.TrimSuffix(suffix, "/logs")
		return id, true, id != "" && !strings.Contains(id, "/")
	}
	return suffix, false, !strings.Contains(suffix, "/")
}

func writeRunLogs(w http.ResponseWriter, r *http.Request, db *store.Store, id string) {
	run, err := db.GetWorkflowRun(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	content, truncated, err := readLogWindow(run.SupervisorLogPath)
	writeJSON(w, map[string]any{"run_id": id, "path": run.SupervisorLogPath, "content": content, "truncated": truncated}, err)
}

func readLogWindow(path string) (string, bool, error) {
	if path == "" {
		return "", false, nil
	}
	file, err := os.Open(path) //nolint:gosec // Path is persisted po supervisor log metadata for a local dashboard.
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("open supervisor log: %w", err)
	}
	defer file.Close() //nolint:errcheck
	info, err := file.Stat()
	if err != nil {
		return "", false, fmt.Errorf("stat supervisor log: %w", err)
	}
	size := info.Size()
	truncated := size > maxLogBytes
	readSize := size
	if truncated {
		readSize = maxLogBytes
		if _, err := file.Seek(size-maxLogBytes, 0); err != nil {
			return "", false, fmt.Errorf("seek supervisor log: %w", err)
		}
	}
	data := make([]byte, int(readSize))
	if len(data) == 0 {
		return "", truncated, nil
	}
	if _, err := io.ReadFull(file, data); err != nil {
		return "", false, fmt.Errorf("read supervisor log: %w", err)
	}
	return string(data), truncated, nil
}

func dashboardUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.URL.Path != "/dashboard/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func dashboardFavicon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(faviconSVG)
}

func writeJSON(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}
