// Package dashboard serves a read-only view of policy and audit history.
//
// Read-only is a design constraint, not an omission. Every handler here is a
// GET that reads state; none writes config, rules, or credentials. Policy is
// authored by editing rules.json and sending SIGHUP, so the dashboard cannot
// become a second, weaker path to changing what the proxy allows (AC-12).
//
// Every route listed here is enumerated in docs/dashboard.md, which is what
// the non-mutation sweep drives — the sweep needs a source of truth that is
// not this file's own route table.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/averycrespi/agent-tools/http-broker/internal/audit"
	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
	"github.com/averycrespi/agent-tools/http-broker/internal/rules"
)

//go:embed assets/*
var assets embed.FS

// Prefix is the path every authenticated dashboard route sits under, including
// the trailing slash. `serve` builds its startup URL from it, and the embedded
// assets reference it, so it is not free to change in one place only.
const Prefix = "/dashboard/"

// sseBuffer is how many events a slow client may fall behind before its
// events start being dropped.
const sseBuffer = 16

// keepaliveInterval keeps an idle SSE connection from being closed by an
// intermediary.
const keepaliveInterval = 15 * time.Second

// AuditQuerier reads audit history.
type AuditQuerier interface {
	Query(ctx context.Context, opts audit.QueryOpts) ([]audit.Record, int, error)
	Subscribe(fn func(audit.Record)) func()
}

// RulesLister exposes the active policy.
type RulesLister interface {
	Engine() *rules.Engine
}

// CredentialLister exposes credential names and bindings, never values.
type CredentialLister interface {
	List() CredentialListing
}

// CredentialInfo is what the dashboard may show about a credential.
//
// There is deliberately no value field. AC-8 requires that a resolved value
// never appears in any dashboard response, and the cheapest way to guarantee
// that is for the type carrying credentials to the dashboard to have nowhere
// to put one.
type CredentialInfo struct {
	Name   string   `json:"name"`
	Source string   `json:"source"`
	Hosts  []string `json:"hosts"`
	// Referenced reports whether any rule in the active policy injects this
	// credential. A stored credential no rule references is dead weight; a
	// referenced one that is missing produces a 403.
	Referenced bool `json:"referenced"`
}

// CredentialListing is what handleCredentials serialises.
//
// IndexError carries a failure to read the credential index to the UI rather
// than silently shortening the list. Neither type has a field able to hold a
// credential value.
type CredentialListing struct {
	Credentials []CredentialInfo `json:"credentials"`
	IndexError  string           `json:"index_error,omitempty"`
}

// CAProvider supplies the certificate served at /ca.pem.
type CAProvider interface {
	RootPEM() []byte
}

// Dashboard serves the read-only UI and its JSON API.
type Dashboard struct {
	audit       AuditQuerier
	rules       RulesLister
	credentials CredentialLister
	ca          CAProvider
	log         *slog.Logger

	// token is swapped on SIGHUP, for the same reason the proxy's is: a
	// rotated token must take effect without a restart.
	token atomic.Pointer[string]
}

// New returns a Dashboard.
func New(
	auditor AuditQuerier, rulesList RulesLister, creds CredentialLister,
	caProvider CAProvider, token string, logger *slog.Logger,
) *Dashboard {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	d := &Dashboard{
		audit: auditor, rules: rulesList, credentials: creds,
		ca: caProvider, log: logger,
	}
	d.SetToken(token)
	return d
}

// SetToken replaces the token the dashboard authenticates against.
func (d *Dashboard) SetToken(token string) { d.token.Store(&token) }

// authToken returns the token in force for this request.
func (d *Dashboard) authToken() string {
	if t := d.token.Load(); t != nil {
		return *t
	}
	return ""
}

// Handler returns the mux for the dashboard listener.
//
// Method patterns are explicit: every route is registered as "GET <path>", so
// a POST or DELETE to any of them is a 405 from the mux rather than something
// a handler has to remember to reject.
func (d *Dashboard) Handler() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated. /healthz is the liveness probe an external monitor
	// needs (AC-19); /ca.pem is fetched by provisioning before any token
	// exists in the sandbox. Both stay at the root: they are consumed by
	// scripts and monitors, not by the dashboard.
	mux.HandleFunc("GET /healthz", d.handleHealthz)
	mux.HandleFunc("GET /ca.pem", d.handleCA)

	// The bare listener address is a dead end otherwise. "/{$}" matches the
	// root exactly, so an unknown path is a mux 404 rather than the page.
	mux.HandleFunc("GET /{$}", d.handleRoot)

	// Authenticated, read-only. Prefixed to match mcp-broker, where the
	// dashboard shares a port with /mcp and the prefix is load-bearing. Here
	// it buys consistency rather than disambiguation.
	mux.HandleFunc("GET "+Prefix, d.requireAuth(d.handleIndex))
	mux.HandleFunc("GET "+Prefix+"app.js", d.requireAuth(d.handleAsset("assets/app.js", "text/javascript; charset=utf-8")))
	mux.HandleFunc("GET "+Prefix+"styles.css", d.requireAuth(d.handleAsset("assets/styles.css", "text/css; charset=utf-8")))
	mux.HandleFunc("GET "+Prefix+"favicon.svg", d.requireAuth(d.handleAsset("assets/favicon.svg", "image/svg+xml")))
	mux.HandleFunc("GET "+Prefix+"api/audit", d.requireAuth(d.handleAudit))
	mux.HandleFunc("GET "+Prefix+"api/rules", d.requireAuth(d.handleRules))
	mux.HandleFunc("GET "+Prefix+"api/credentials", d.requireAuth(d.handleCredentials))
	mux.HandleFunc("GET "+Prefix+"api/events", d.requireAuth(d.handleEvents))

	return mux
}

// handleRoot sends the bare listener address to the dashboard.
//
// A `?token=` parameter is carried over so a hand-typed root URL still
// authenticates, but it is re-encoded from the parsed query rather than echoed:
// the target is a literal prefix plus one escaped parameter, so it can neither
// leave the origin nor inject a header. TestRootRedirectStaysOnTheDashboard
// pins that.
func (d *Dashboard) handleRoot(w http.ResponseWriter, r *http.Request) {
	//nolint:gosec // G710: the target is built from Prefix, never from the request path.
	http.Redirect(w, r, rootRedirectTarget(r.URL.Query().Get("token")), http.StatusFound)
}

// rootRedirectTarget returns the dashboard URL a root visit is sent to.
func rootRedirectTarget(token string) string {
	if token == "" {
		return Prefix
	}
	return Prefix + "?token=" + url.QueryEscape(token)
}

// requireAuth wraps a handler with the bearer-token check.
//
// A ?token= query parameter sets a cookie and redirects, so an operator can
// open the dashboard from a link without a browser extension. The redirect
// drops the token from the URL so it does not linger in history.
func (d *Dashboard) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := d.authToken()
		// Constant-time, like every other token comparison in the tool. A
		// plain == here was the lone outlier.
		if qToken := r.URL.Query().Get("token"); qToken != "" && auth.ConstantTimeEqual(qToken, token) {
			//nolint:gosec // the dashboard is loopback-only plain HTTP; a
			// Secure cookie would never be sent and local auth would break.
			http.SetCookie(w, &http.Cookie{
				Name:     auth.DashboardCookieName,
				Value:    token,
				Path:     Prefix,
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
				MaxAge:   int(365 * 24 * time.Hour / time.Second),
			})
			// The target is chosen from a fixed set rather than echoed from
			// the request. A path like "//evil.com" is a valid protocol-
			// relative URL, so reflecting the request path — even after
			// escaping — would make this an open redirect.
			//nolint:gosec // G710 cannot see through the map lookup;
			// safeRedirectTarget returns one of five literals and never
			// anything derived from the request. TestSafeRedirectTarget
			// pins that.
			http.Redirect(w, r, safeRedirectTarget(r.URL.Path), http.StatusFound)
			return
		}

		if !auth.CheckDashboardAuth(r, token) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="http-broker"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// dashboardPaths is the set a post-authentication redirect may target.
var dashboardPaths = map[string]struct{}{
	Prefix:                     {},
	Prefix + "api/audit":       {},
	Prefix + "api/rules":       {},
	Prefix + "api/credentials": {},
	Prefix + "api/events":      {},
}

// safeRedirectTarget returns path when it is a known dashboard route, and the
// dashboard root otherwise.
func safeRedirectTarget(path string) string {
	if _, ok := dashboardPaths[path]; ok {
		return path
	}
	return Prefix
}

func (d *Dashboard) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

func (d *Dashboard) handleCA(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="http-broker-ca.pem"`)
	_, _ = w.Write(d.ca.RootPEM())
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, _ *http.Request) {
	d.serveAsset(w, "assets/index.html", "text/html; charset=utf-8")
}

func (d *Dashboard) handleAsset(name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		d.serveAsset(w, name, contentType)
	}
}

func (d *Dashboard) serveAsset(w http.ResponseWriter, name, contentType string) {
	data, err := assets.ReadFile(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// handleAudit returns audit history. Filters come from query parameters and
// are passed as bound values, never interpolated.
func (d *Dashboard) handleAudit(w http.ResponseWriter, r *http.Request) {
	opts := audit.QueryOpts{
		Host:    r.URL.Query().Get("host"),
		Outcome: r.URL.Query().Get("outcome"),
		Source:  r.URL.Query().Get("source"),
		Mode:    r.URL.Query().Get("mode"),
		Rule:    r.URL.Query().Get("rule"),
		Limit:   intParam(r, "limit", audit.DefaultLimit),
		Offset:  intParam(r, "offset", 0),
	}

	records, total, err := d.audit.Query(r.Context(), opts)
	if err != nil {
		d.log.Error("audit query failed", "error", err)
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	writeJSON(w, map[string]any{
		"records": recordsJSON(records),
		"total":   total,
		"limit":   opts.Limit,
		"offset":  opts.Offset,
	})
}

// handleRules returns the active policy.
func (d *Dashboard) handleRules(w http.ResponseWriter, _ *http.Request) {
	engine := d.rules.Engine()
	doc := engine.Document()
	writeJSON(w, map[string]any{
		"fallthrough": doc.Fallthrough,
		"rules":       doc.Rules,
	})
}

// handleCredentials returns credential names and bindings, never values.
func (d *Dashboard) handleCredentials(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, d.credentials.List())
}

// handleEvents streams new audit records over SSE.
//
// The per-client channel is buffered and the broadcast is non-blocking: a
// client that stops reading loses events rather than stalling the request
// path that produced them.
func (d *Dashboard) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch := make(chan []byte, sseBuffer)
	unsubscribe := d.audit.Subscribe(func(rec audit.Record) {
		data, err := json.Marshal(recordJSON(rec))
		if err != nil {
			return
		}
		select {
		case ch <- data:
		default:
			// Drop rather than block. The alternative is back-pressure from a
			// dashboard tab reaching the proxy's request path.
		}
	})
	defer unsubscribe()

	_, _ = fmt.Fprint(w, ": keepalive\n\n")
	flusher.Flush()

	heartbeat := time.NewTicker(keepaliveInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case data := <-ch:
			_, _ = fmt.Fprintf(w, "event: audit\ndata: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// recordJSON renders one audit record.
//
// The fields are enumerated rather than the struct being marshalled directly,
// so a field added to audit.Record cannot reach a dashboard response without
// someone deciding it should.
func recordJSON(rec audit.Record) map[string]any {
	return map[string]any{
		"id":             rec.ID,
		"ts":             rec.Timestamp.UTC().Format(time.RFC3339Nano),
		"interception":   rec.Interception,
		"method":         rec.Method,
		"host":           rec.Host,
		"port":           rec.Port,
		"path":           rec.Path,
		"query":          rec.Query,
		"status":         rec.Status,
		"duration_ms":    rec.DurationMS,
		"bytes_in":       rec.BytesIn,
		"bytes_out":      rec.BytesOut,
		"matched_rule":   rec.MatchedRule,
		"source":         rec.Source,
		"mode":           rec.Mode,
		"injection":      rec.Injection,
		"credential_ref": rec.CredentialRef,
		"outcome":        rec.Outcome,
		"error":          rec.Error,
	}
}

func recordsJSON(records []audit.Record) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		out = append(out, recordJSON(rec))
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func intParam(r *http.Request, name string, fallback int) int {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}
