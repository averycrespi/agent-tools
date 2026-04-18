package dashboard

import (
	"crypto/subtle"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/agents"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/approval"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/rules"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
)

//go:embed index.html
var indexHTML []byte

//go:embed app.js
var appJS []byte

//go:embed styles.css
var stylesCSS []byte

//go:embed favicon.svg
var faviconSVG []byte

// RulesLister provides the configured rules in evaluation order and host
// coverage for the tunneled-hosts API.
type RulesLister interface {
	Rules() []*rules.Rule
	AllRuleHosts() []string
}

// ApprovalBrokerIface is a subset of *approval.Broker used by the dashboard.
type ApprovalBrokerIface interface {
	Pending() []proxy.ApprovalRequest
	Decide(id string, decision proxy.ApprovalDecision) error
}

// Deps groups all dependencies for the dashboard Server.
type Deps struct {
	AdminTokenPath string
	Rules          RulesLister
	Agents         agents.Registry
	Secrets        secrets.Store
	Auditor        audit.Logger
	Approval       ApprovalBrokerIface
	CAPath         string
	Logger         *slog.Logger
}

// Server is the dashboard HTTP handler.
type Server struct {
	deps     Deps
	sse      *sseBroker
	log      *slog.Logger
	tokenPtr atomic.Pointer[string] // current admin token; swapped atomically on SIGHUP
}

// New constructs a dashboard Server from Deps.
func New(deps Deps) *Server {
	log := deps.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		deps: deps,
		sse:  newSSEBroker(),
		log:  log,
	}
}

// Broadcast fans an event out to all current SSE subscribers. It is safe to
// call from any goroutine. Callers supply a human-readable kind string (e.g.
// "request", "approval") and any JSON-serialisable data payload.
func (s *Server) Broadcast(kind string, data any) {
	s.sse.Broadcast(Event{
		Kind: kind,
		ID:   ulid.Make(),
		Data: data,
	})
}

// Handler returns the HTTP handler for the dashboard, wrapped in auth middleware.
// It also returns created=true when the admin token file was newly generated
// (i.e. this is the first run), and false when an existing token was loaded.
func (s *Server) Handler() (http.Handler, bool) {
	token, created, err := EnsureAdminTokenCreated(s.deps.AdminTokenPath)
	if err != nil {
		s.log.Error("dashboard: failed to load admin token", "error", err)
		token = ""
	}
	s.tokenPtr.Store(&token)

	mux := http.NewServeMux()

	// Unauthenticated routes.
	mux.HandleFunc("GET /ca.pem", s.handleCAPem)
	mux.HandleFunc("GET /dashboard/unauthorized", s.handleUnauthorizedGET)
	mux.HandleFunc("POST /dashboard/unauthorized", s.handleUnauthorizedPOST)

	// Static assets (authenticated; served under /dashboard/).
	mux.HandleFunc("GET /dashboard/", s.handleIndex)
	mux.HandleFunc("GET /dashboard/app.js", s.handleAppJS)
	mux.HandleFunc("GET /dashboard/styles.css", s.handleStylesCSS)
	mux.HandleFunc("GET /dashboard/favicon.svg", s.handleFaviconSVG)

	// Authenticated API routes (under /dashboard/api/* so the auth cookie
	// Path "/dashboard/" covers them — browsers send the cookie with every
	// fetch('/dashboard/api/...') call from the SPA).
	mux.HandleFunc("GET /dashboard/api/pending", s.handlePending)
	mux.HandleFunc("GET /dashboard/api/audit", s.handleAudit)
	mux.HandleFunc("GET /dashboard/api/events", s.handleEvents)
	mux.HandleFunc("POST /dashboard/api/decide", s.handleDecide)
	mux.HandleFunc("GET /dashboard/api/agents", s.handleAgents)
	mux.HandleFunc("GET /dashboard/api/rules", s.handleRules)
	mux.HandleFunc("GET /dashboard/api/secrets", s.handleSecrets)
	mux.HandleFunc("GET /dashboard/api/stats/tunneled-hosts", s.handleTunneledHosts)

	return authMiddleware(&s.tokenPtr, mux), created
}

// ReloadToken re-reads the admin token file and atomically swaps the in-memory
// token. After this returns, new requests are validated against the new token;
// any in-flight cookie carrying the old token is rejected.
func (s *Server) ReloadToken() error {
	data, err := os.ReadFile(s.deps.AdminTokenPath)
	if err != nil {
		return fmt.Errorf("dashboard: reload token: %w", err)
	}
	tok := strings.TrimSpace(string(data))
	s.tokenPtr.Store(&tok)
	return nil
}

// Token returns the current admin token (used for URL generation at startup).
func (s *Server) Token() string {
	p := s.tokenPtr.Load()
	if p == nil {
		return ""
	}
	return *p
}

// handleCAPem serves the CA certificate (unauthenticated).
func (s *Server) handleCAPem(w http.ResponseWriter, _ *http.Request) {
	if s.deps.CAPath == "" {
		http.Error(w, "CA path not configured", http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(s.deps.CAPath)
	if err != nil {
		http.Error(w, "CA cert not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	_, _ = w.Write(data)
}

// handleUnauthorizedGET renders the re-auth form.
func (s *Server) handleUnauthorizedGET(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, unauthorizedForm(""))
}

// handleUnauthorizedPOST promotes a posted token to a cookie and redirects.
func (s *Server) handleUnauthorizedPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	submitted := r.FormValue("token")
	current := s.Token()
	tokenBytes := []byte(current)
	if subtle.ConstantTimeCompare([]byte(submitted), tokenBytes) == 1 {
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    current,
			Path:     "/dashboard/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   int(365 * 24 * time.Hour / time.Second),
			// Secure is false for local dev (127.0.0.1 over HTTP).
			// TODO(TLS): set Secure: true when the gateway is served over HTTPS.
			Secure: false,
		})
		http.Redirect(w, r, "/dashboard/", http.StatusFound)
		return
	}
	// Invalid token: re-render form with error.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprint(w, unauthorizedForm("Invalid token. Please try again."))
}

// unauthorizedForm returns the HTML for the /dashboard/unauthorized page.
// If errMsg is non-empty an error paragraph is included.
// errMsg is HTML-escaped to prevent XSS if a dynamic value is ever passed.
func unauthorizedForm(errMsg string) string {
	errHTML := ""
	if errMsg != "" {
		errHTML = `<p class="err">` + html.EscapeString(errMsg) + `</p>`
	}
	return `<!DOCTYPE html>
<html><head><title>Unauthorized - agent-gateway</title>
<style>body{font-family:system-ui,sans-serif;max-width:600px;margin:80px auto;padding:0 20px;color:#333}
h1{color:#c00}.err{color:#c00;font-weight:bold}input{padding:6px 10px;font-size:1em;width:100%}
button{margin-top:8px;padding:8px 16px;font-size:1em;cursor:pointer}</style>
</head><body>
<h1>Unauthorized</h1>
<p>You need to authenticate to access the agent-gateway dashboard.</p>
<p>Open the authenticated URL printed at startup:</p>
<pre>Dashboard: http://localhost:PORT/dashboard/?token=TOKEN</pre>
` + errHTML + `
<form method="POST" action="/dashboard/unauthorized">
  <label for="token">Admin token:</label><br>
  <input type="password" id="token" name="token" autocomplete="current-password" required>
  <button type="submit">Sign in</button>
</form>
</body></html>`
}

// handlePending returns the pending approval requests.
func (s *Server) handlePending(w http.ResponseWriter, _ *http.Request) {
	var pending []proxy.ApprovalRequest
	if s.deps.Approval != nil {
		pending = s.deps.Approval.Pending()
	}
	if pending == nil {
		pending = []proxy.ApprovalRequest{}
	}
	writeJSON(w, pending)
}

// handleDecide resolves a pending approval request.
func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		ID       string `json:"id"`
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if s.deps.Approval == nil {
		http.Error(w, "no approval broker configured", http.StatusServiceUnavailable)
		return
	}

	var decision proxy.ApprovalDecision
	switch payload.Decision {
	case "approve":
		decision = proxy.DecisionApproved
	case "deny":
		decision = proxy.DecisionDenied
	default:
		http.Error(w, "decision must be 'approve' or 'deny'", http.StatusBadRequest)
		return
	}

	if err := s.deps.Approval.Decide(payload.ID, decision); err != nil {
		if errors.Is(err, approval.ErrUnknownID) {
			http.Error(w, "unknown request id", http.StatusNotFound)
			return
		}
		s.log.Error("dashboard: decide failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// handleAudit paginates audit log entries.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if s.deps.Auditor == nil {
		writeJSON(w, map[string]any{"records": []audit.Entry{}, "total": 0})
		return
	}

	f := audit.Filter{}
	if v := r.URL.Query().Get("agent"); v != "" {
		f.Agent = &v
	}
	if v := r.URL.Query().Get("host"); v != "" {
		f.Host = &v
	}
	if v := r.URL.Query().Get("rule"); v != "" {
		f.Rule = &v
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = &n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Offset = &n
		}
	}

	records, err := s.deps.Auditor.Query(r.Context(), f)
	if err != nil {
		s.log.Error("dashboard: audit query failed", "error", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	if records == nil {
		records = []audit.Entry{}
	}

	// Count uses the same filter predicates (ignoring Limit/Offset) so the
	// caller gets the true total matching row count, not just the page size.
	total, err := s.deps.Auditor.Count(r.Context(), f)
	if err != nil {
		s.log.Error("dashboard: audit count failed", "error", err)
		http.Error(w, "count failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{"records": records, "total": total})
}

// handleAgents returns agent metadata (no token values).
func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Agents == nil {
		writeJSON(w, map[string]any{"agents": []agents.AgentMetadata{}})
		return
	}
	list, err := s.deps.Agents.List(r.Context())
	if err != nil {
		s.log.Error("dashboard: agents list failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []agents.AgentMetadata{}
	}
	writeJSON(w, map[string]any{"agents": list})
}

// handleRules returns rules from the rules engine.
func (s *Server) handleRules(w http.ResponseWriter, _ *http.Request) {
	var ruleList []*rules.Rule
	if s.deps.Rules != nil {
		ruleList = s.deps.Rules.Rules()
	}

	type ruleView struct {
		Name    string `json:"name"`
		Verdict string `json:"verdict"`
	}
	views := make([]ruleView, len(ruleList))
	for i, r := range ruleList {
		views[i] = ruleView{
			Name:    r.Name,
			Verdict: r.Verdict,
		}
	}
	writeJSON(w, map[string]any{"rules": views})
}

// handleSecrets returns secret metadata only (no values).
func (s *Server) handleSecrets(w http.ResponseWriter, r *http.Request) {
	if s.deps.Secrets == nil {
		writeJSON(w, map[string]any{"secrets": []secrets.Metadata{}})
		return
	}
	list, err := s.deps.Secrets.List(r.Context())
	if err != nil {
		s.log.Error("dashboard: secrets list failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if list == nil {
		list = []secrets.Metadata{}
	}
	writeJSON(w, map[string]any{"secrets": list})
}

// handleIndex serves the embedded SPA index page.
func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

// handleAppJS serves the embedded app.js bundle.
func (s *Server) handleAppJS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(appJS)
}

// handleStylesCSS serves the embedded styles.css.
func (s *Server) handleStylesCSS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(stylesCSS)
}

// handleFaviconSVG serves the embedded favicon.svg.
func (s *Server) handleFaviconSVG(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(faviconSVG)
}

// tunneledHostEntry is one item in the /api/stats/tunneled-hosts response.
type tunneledHostEntry struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// handleTunneledHosts returns hosts that appeared in tunnel audit rows within
// the requested window (default 24h) but are NOT covered by any current rule.
//
// Query param: since=<duration> (e.g. "24h", "7d"). Defaults to "24h".
func (s *Server) handleTunneledHosts(w http.ResponseWriter, r *http.Request) {
	// Parse the since window; default to 24h.
	sinceDur := 24 * time.Hour
	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			sinceDur = d
		}
	}

	if s.deps.Auditor == nil {
		writeJSON(w, []tunneledHostEntry{})
		return
	}

	after := time.Now().Add(-sinceDur)
	f := audit.Filter{After: &after}
	entries, err := s.deps.Auditor.Query(r.Context(), f)
	if err != nil {
		s.log.Error("dashboard: tunneled-hosts query failed", "error", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	// Build the set of all hosts covered by current rules.
	covered := make(map[string]struct{})
	if s.deps.Rules != nil {
		for _, h := range s.deps.Rules.AllRuleHosts() {
			covered[h] = struct{}{}
		}
	}

	// Count tunnel rows, excluding hosts already covered by a rule.
	counts := make(map[string]int)
	order := []string{}
	for _, e := range entries {
		if e.Interception != "tunnel" {
			continue
		}
		host := e.Host
		if _, ok := covered[host]; ok {
			continue
		}
		if _, exists := counts[host]; !exists {
			order = append(order, host)
		}
		counts[host]++
	}

	result := make([]tunneledHostEntry, 0, len(order))
	for _, host := range order {
		result = append(result, tunneledHostEntry{Host: host, Count: counts[host]})
	}
	writeJSON(w, result)
}

// writeJSON writes v as JSON with Content-Type: application/json.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// Ensure *approval.Broker satisfies ApprovalBrokerIface at compile time.
var _ ApprovalBrokerIface = (*approval.Broker)(nil)
