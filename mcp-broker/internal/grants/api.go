package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Duration wraps time.Duration with JSON support using Go's duration syntax.
type Duration time.Duration

func (d *Duration) UnmarshalJSON(b []byte) error {
	if s := string(b); len(s) > 0 && s[0] == '"' {
		unquoted, err := strconvUnquote(s)
		if err != nil {
			return err
		}
		parsed, err := time.ParseDuration(unquoted)
		if err != nil {
			return fmt.Errorf("parsing duration %q: %w", unquoted, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var ns int64
	if err := json.Unmarshal(b, &ns); err != nil {
		return err
	}
	*d = Duration(time.Duration(ns))
	return nil
}

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func strconvUnquote(s string) (string, error) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", errors.New("not a quoted string")
	}
	return s[1 : len(s)-1], nil
}

// CreateRequest is the JSON body for POST /api/grants.
type CreateRequest struct {
	Description string   `json:"description,omitempty"`
	TTL         Duration `json:"ttl"`
	Entries     []Entry  `json:"entries"`
}

// CreateResponse is the JSON body returned from POST /api/grants.
// Token is the raw bearer string, shown exactly once.
type CreateResponse struct {
	ID          string    `json:"id"`
	Token       string    `json:"token"`
	Description string    `json:"description,omitempty"`
	Tools       []string  `json:"tools"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// API wires the HTTP handlers for /api/grants*.
type API struct {
	store  *Store
	engine *Engine
}

// NewAPI constructs an API backed by the given store and engine.
func NewAPI(store *Store, engine *Engine) *API {
	return &API{store: store, engine: engine}
}

func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/grants":
		a.handleList(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/grants":
		a.handleCreate(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/grants/"):
		a.handleRevoke(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *API) handleList(w http.ResponseWriter, r *http.Request) {
	all := r.URL.Query().Get("status") == "all"
	grants, err := a.store.List(r.Context(), all)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if grants == nil {
		grants = []Grant{} // stable JSON: [] not null
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(grants)
}

func (a *API) handleRevoke(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/grants/")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "missing grant id", http.StatusBadRequest)
		return
	}
	if err := a.store.Revoke(r.Context(), id, time.Now().UTC()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	a.engine.Invalidate(id)
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decoding request: %v", err), http.StatusBadRequest)
		return
	}
	if time.Duration(req.TTL) <= 0 {
		http.Error(w, "ttl must be positive", http.StatusBadRequest)
		return
	}
	if len(req.Entries) == 0 {
		http.Error(w, "at least one entry required", http.StatusBadRequest)
		return
	}
	for _, e := range req.Entries {
		if _, err := CompileSchema(e.ArgSchema); err != nil {
			http.Error(w, fmt.Sprintf("entry %q: %v", e.Tool, err), http.StatusBadRequest)
			return
		}
	}

	cred, err := NewCredential()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC()
	g := Grant{
		ID:          cred.ID,
		Description: req.Description,
		Entries:     req.Entries,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Duration(req.TTL)),
	}
	if err := a.store.Create(r.Context(), g, cred.TokenHash); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	tools := make([]string, len(g.Entries))
	for i, e := range g.Entries {
		tools[i] = e.Tool
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CreateResponse{
		ID:          g.ID,
		Token:       cred.Token,
		Description: g.Description,
		Tools:       tools,
		CreatedAt:   g.CreatedAt,
		ExpiresAt:   g.ExpiresAt,
	})
}
