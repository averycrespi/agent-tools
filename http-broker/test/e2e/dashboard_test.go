//go:build e2e

package e2e_test

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// dashboardRoutes is the route list from docs/dashboard.md.
//
// It is read from the document rather than hard-coded here on purpose: V-17 is
// only as good as that list, and reading it means the sweep cannot silently
// miss a route someone added to the code and documented but not tested — or
// pass because the test and the implementation share one author's assumption.
func dashboardRoutes(t *testing.T) []string {
	t.Helper()

	path := filepath.Join(mustFindModuleRoot(), "http-broker", "docs", "dashboard.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var routes []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		// Route rows look like: | `GET /api/audit` | ... |
		if !strings.HasPrefix(line, "| `GET ") {
			continue
		}
		start := strings.Index(line, "`GET ")
		end := strings.Index(line[start+1:], "`")
		if start < 0 || end < 0 {
			continue
		}
		route := strings.TrimPrefix(line[start+1:start+1+end], "GET ")
		routes = append(routes, strings.TrimSpace(route))
	}

	if len(routes) == 0 {
		t.Fatalf("no routes parsed from %s; V-17 cannot verify anything", path)
	}
	sort.Strings(routes)
	return routes
}

// stateSnapshot hashes everything the dashboard must not be able to change.
//
// The credential index is in the list because the dashboard reads it, and the
// obvious way to describe a credential — credentials.Store.Describe —
// re-registers the name as a side effect. Paths are built from the stack's own
// fields rather than the paths package: the XDG variables are exported only
// into the subprocess's environment, so calling paths here would resolve
// against the developer's real home.
func stateSnapshot(t *testing.T, s *stack) string {
	t.Helper()

	h := sha256.New()
	for _, path := range []string{
		s.configPath,
		s.rulesPath,
		filepath.Join(s.configDir, "auth-token"),
		filepath.Join(s.dataDir, "credentials.json"),
	} {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			// Tests that never seed an index have none to protect.
			continue
		}
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		_, _ = h.Write([]byte(path))
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// seedCredentialIndex writes a names-only credential index into the stack's
// data directory.
func seedCredentialIndex(t *testing.T, s *stack, names ...string) {
	t.Helper()

	if err := os.MkdirAll(s.dataDir, 0o750); err != nil {
		t.Fatalf("creating %s: %v", s.dataDir, err)
	}
	doc := map[string]any{"version": 1, "names": names}
	writeJSON(t, filepath.Join(s.dataDir, "credentials.json"), doc)
}

// TestDashboardReadOnly is AC-12 / V-17: driving every documented route, with
// every method and adversarial query parameters, changes no state.
func TestDashboardReadOnly(t *testing.T) {
	s := startStack(t, stackOptions{
		Rules: rulesDoc("tunnel",
			rule{Name: "ads", Host: "*.doubleclick.net", Mode: "deny"},
		),
		EnvCredentials: map[string]any{
			"tok": map[string]any{"var": "TOK", "hosts": []string{"api.github.com"}},
		},
		Env: map[string]string{"TOK": "SENTINEL-DASH"},
	})

	// Seed the credential index before snapshotting. Without a name in it
	// there is nothing for a mutating dashboard call to change, and the
	// before/after hashes would match no matter what the dashboard did.
	// Written directly, holding names only, so this touches no keychain and
	// respects the suite's env_credentials-only rule.
	//
	// Reach: this catches a mutating describe only where a keychain exists.
	// credentials.Store.Describe re-registers a name on a keychain *hit*, and
	// Store.List prunes on a *not found* — on a host with no Secret Service
	// every lookup is ErrUnavailable instead, which by design writes nothing.
	// So this fires on macOS; on Linux the grep in V-4.3 is what holds.
	seedCredentialIndex(t, s, "seeded")

	before := stateSnapshot(t, s)
	routes := dashboardRoutes(t)
	t.Logf("sweeping %d documented routes: %s", len(routes), strings.Join(routes, " "))

	adversarial := []string{
		"",
		"?limit=999999&offset=-1",
		"?host=%27%20OR%20%271%27%3D%271",
		"?outcome=allowed%3B%20DROP%20TABLE%20audit_records%3B--",
		"?rule=../../../../etc/passwd",
		"?token=wrong",
		"?limit=abc&offset=xyz",
	}
	methods := []string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions,
	}

	client := &http.Client{Timeout: 10e9}
	for _, route := range routes {
		// The SSE feed blocks until the client disconnects, so it gets its own
		// bounded exercise below rather than the full sweep.
		if route == "/dashboard/api/events" {
			continue
		}
		for _, method := range methods {
			for _, query := range adversarial {
				req, err := http.NewRequest(method, s.dashURL(route)+query, nil)
				if err != nil {
					t.Fatalf("building %s %s: %v", method, route, err)
				}
				req.Header.Set("Authorization", "Bearer "+s.token)

				resp, err := client.Do(req)
				if err != nil {
					continue // a refused or reset request changes nothing either
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}
	}

	if after := stateSnapshot(t, s); after != before {
		t.Errorf("dashboard traffic changed on-disk state:\nbefore %s\nafter  %s", before, after)
	}

	// The credential list still holds exactly what it did, and never a value.
	resp, body := authedGet(t, s, "/dashboard/api/credentials")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/dashboard/api/credentials status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "tok") {
		t.Errorf("the credential list should still name the credential: %s", body)
	}
	if strings.Contains(body, "SENTINEL-DASH") {
		t.Errorf("the credential value appeared in a dashboard response: %s", body)
	}
}

// TestDashboardAuth is AC-12's other half: every route except /ca.pem and
// /healthz requires the token, and the root only redirects.
func TestDashboardAuth(t *testing.T) {
	s := startStack(t, stackOptions{})

	public := map[string]bool{"/ca.pem": true, "/healthz": true}
	// The listener root serves nothing to authenticate; it only redirects.
	redirects := map[string]bool{"/": true}
	client := &http.Client{
		Timeout: 10e9,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	for _, route := range dashboardRoutes(t) {
		req, err := http.NewRequest(http.MethodGet, s.dashURL(route), nil)
		if err != nil {
			t.Fatalf("building the request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", route, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()

		if public[route] {
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s status = %d, want 200 without a token", route, resp.StatusCode)
			}
			continue
		}
		if redirects[route] {
			if resp.StatusCode != http.StatusFound {
				t.Errorf("%s status = %d, want 302 without a token", route, resp.StatusCode)
			}
			continue
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s status = %d, want 401 without a token", route, resp.StatusCode)
		}
	}
}

// TestDashboardRoutesAreDocumented closes the gap the other way: a route the
// code serves but the document omits would never be swept.
func TestDashboardRoutesAreDocumented(t *testing.T) {
	s := startStack(t, stackOptions{})

	documented := make(map[string]bool)
	for _, route := range dashboardRoutes(t) {
		documented[route] = true
	}

	// Every route the implementation is known to serve.
	served := []string{
		"/dashboard/", "/dashboard/app.js", "/dashboard/styles.css", "/dashboard/favicon.svg",
		"/dashboard/api/audit", "/dashboard/api/rules", "/dashboard/api/credentials",
		"/dashboard/api/events",
		"/", "/ca.pem", "/healthz",
	}
	for _, route := range served {
		if !documented[route] {
			t.Errorf("route %s is served but not listed in docs/dashboard.md, so the AC-12 sweep would skip it", route)
		}
	}

	// And each documented route actually exists, so the list is not stale.
	for route := range documented {
		if route == "/dashboard/api/events" {
			continue
		}
		req, _ := http.NewRequest(http.MethodGet, s.dashURL(route), nil)
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := (&http.Client{Timeout: 10e9}).Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", route, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("docs/dashboard.md lists %s but the server returns 404", route)
		}
	}
}

func TestDashboardRulesReflectPolicy(t *testing.T) {
	s := startStack(t, stackOptions{
		Rules: rulesDoc("deny",
			rule{Name: "ads", Host: "*.doubleclick.net", Mode: "deny"},
			rule{Name: "gh", Host: "api.github.com", Mode: "intercept"},
		),
	})

	resp, body := authedGet(t, s, "/dashboard/api/rules")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	for _, want := range []string{"ads", "gh", "doubleclick", "deny"} {
		if !strings.Contains(body, want) {
			t.Errorf("the rules response should mention %q: %s", want, body)
		}
	}
}

func authedGet(t *testing.T, s *stack, route string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, s.dashURL(route), nil)
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)

	resp, err := (&http.Client{Timeout: 10e9}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", route, err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the body: %v", err)
	}
	return resp, string(data)
}
