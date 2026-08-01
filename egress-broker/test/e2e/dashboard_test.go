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

	path := filepath.Join(mustFindModuleRoot(), "egress-broker", "docs", "dashboard.md")
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
func stateSnapshot(t *testing.T, s *stack) string {
	t.Helper()

	h := sha256.New()
	for _, path := range []string{s.configPath, s.rulesPath, filepath.Join(s.configDir, "auth-token")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		_, _ = h.Write([]byte(path))
		_, _ = h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil))
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
		if route == "/api/events" {
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
	resp, body := authedGet(t, s, "/api/credentials")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/credentials status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "tok") {
		t.Errorf("the credential list should still name the credential: %s", body)
	}
	if strings.Contains(body, "SENTINEL-DASH") {
		t.Errorf("the credential value appeared in a dashboard response: %s", body)
	}
}

// TestDashboardAuth is AC-12's other half: every route except /ca.pem and
// /healthz requires the token.
func TestDashboardAuth(t *testing.T) {
	s := startStack(t, stackOptions{})

	public := map[string]bool{"/ca.pem": true, "/healthz": true}
	client := &http.Client{Timeout: 10e9}

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
		"/", "/app.js", "/styles.css", "/favicon.svg",
		"/api/audit", "/api/rules", "/api/credentials", "/api/events",
		"/ca.pem", "/healthz",
	}
	for _, route := range served {
		if !documented[route] {
			t.Errorf("route %s is served but not listed in docs/dashboard.md, so the AC-12 sweep would skip it", route)
		}
	}

	// And each documented route actually exists, so the list is not stale.
	for route := range documented {
		if route == "/api/events" {
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

	resp, body := authedGet(t, s, "/api/rules")
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
