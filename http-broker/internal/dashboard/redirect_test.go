package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/http-broker/internal/auth"
)

// hostileTargets are paths a redirect must never reflect. A path like
// "//evil.com" is a valid protocol-relative URL, so echoing the request path
// would leave the origin.
var hostileTargets = []string{
	"//evil.com",
	"///evil.com",
	"//evil.com/path",
	"https://evil.com",
	"/\\evil.com",
	"/../../etc/passwd",
	"/dashboard/api/audit/../../../evil",
	"/dashboard//evil.com",
	"",
	"/unknown",
	"/dashboard",
}

// TestSafeRedirectTarget is the assertion the nolint on the redirect relies
// on: the target is always one of the known routes, never anything derived
// from the request.
func TestSafeRedirectTarget(t *testing.T) {
	for _, path := range hostileTargets {
		if got := safeRedirectTarget(path); got != Prefix {
			t.Errorf("safeRedirectTarget(%q) = %q, want %q", path, got, Prefix)
		}
	}

	for path := range dashboardPaths {
		if got := safeRedirectTarget(path); got != path {
			t.Errorf("safeRedirectTarget(%q) = %q, want it preserved", path, got)
		}
	}
}

// TestRedirectTargetsAreRelativePaths guards the set itself: an absolute or
// protocol-relative entry would defeat the check.
func TestRedirectTargetsAreRelativePaths(t *testing.T) {
	for path := range dashboardPaths {
		if len(path) == 0 || path[0] != '/' {
			t.Errorf("route %q must start with /", path)
		}
		if len(path) > 1 && path[1] == '/' {
			t.Errorf("route %q is protocol-relative and would leave the origin", path)
		}
	}
}

// TestRedirectTargetsArePrefixed keeps the allowlist and the mux registrations
// from drifting apart: a route added without the prefix would be unreachable
// but still an accepted redirect target.
func TestRedirectTargetsArePrefixed(t *testing.T) {
	for path := range dashboardPaths {
		if !strings.HasPrefix(path, Prefix) {
			t.Errorf("route %q is not under %q", path, Prefix)
		}
	}
}

// TestRootRedirectStaysOnTheDashboard is what the nolint on handleRoot rests
// on. The token is re-encoded from the parsed query rather than echoed, so a
// hostile value cannot leave the origin or inject a header.
func TestRootRedirectStaysOnTheDashboard(t *testing.T) {
	hostile := append([]string{
		"//evil.com",
		"https://evil.com",
		"x\r\nLocation: https://evil.com",
		"x\nSet-Cookie: a=b",
		"../../etc/passwd",
	}, hostileTargets...)

	for _, token := range hostile {
		got := rootRedirectTarget(token)
		if !strings.HasPrefix(got, Prefix) {
			t.Errorf("rootRedirectTarget(%q) = %q, want it under %q", token, got, Prefix)
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Errorf("rootRedirectTarget(%q) = %q, want no CR or LF", token, got)
		}
	}

	if got := rootRedirectTarget(""); got != Prefix {
		t.Errorf("rootRedirectTarget(\"\") = %q, want %q", got, Prefix)
	}
	if got := rootRedirectTarget("deadbeef"); got != Prefix+"?token=deadbeef" {
		t.Errorf("rootRedirectTarget(%q) = %q, want %q", "deadbeef", got, Prefix+"?token=deadbeef")
	}
}

// TestRootRedirects drives the handler, so the registered route and the
// Location header are both covered rather than just the helper.
func TestRootRedirects(t *testing.T) {
	d := New(nil, nil, nil, nil, dashboardTestStore(t), nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	for _, tc := range []struct{ path, want string }{
		{"/", Prefix},
		{"/?token=deadbeef", Prefix + "?token=deadbeef"},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatalf("building request for %q: %v", tc.path, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %q: %v", tc.path, err)
		}
		defer func() { _ = resp.Body.Close() }()

		if resp.StatusCode != http.StatusFound {
			t.Errorf("GET %q status = %d, want %d", tc.path, resp.StatusCode, http.StatusFound)
		}
		if got := resp.Header.Get("Location"); got != tc.want {
			t.Errorf("GET %q Location = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// TestUnknownRootPathIs404 pins the reason the root is registered as "/{$}"
// rather than "/": a subtree pattern would serve the dashboard page for every
// unmatched path.
func TestDashboardAcceptsOnlyAdminRole(t *testing.T) {
	d := New(nil, nil, nil, nil, dashboardTestStore(t), nil)
	handler := d.Handler()
	for _, tt := range []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "admin", token: strings.Repeat("b", 64), wantStatus: http.StatusOK},
		{name: "agent", token: strings.Repeat("a", 64), wantStatus: http.StatusFound},
		{name: "missing", wantStatus: http.StatusFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, Prefix, nil)
			if tt.token != "" {
				request.Header.Set("Authorization", "Bearer "+tt.token)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if tt.wantStatus == http.StatusFound && response.Header().Get("Location") != Prefix+"unauthorized" {
				t.Fatalf("Location = %q, want %q", response.Header().Get("Location"), Prefix+"unauthorized")
			}
		})
	}
}

func TestSlashlessDashboardRedirectsDirectlyToUnauthorizedPage(t *testing.T) {
	d := New(nil, nil, nil, nil, dashboardTestStore(t), nil)
	request := httptest.NewRequest(http.MethodGet, "/dashboard", nil)
	response := httptest.NewRecorder()

	d.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusFound)
	}
	if got := response.Header().Get("Location"); got != unauthorizedPath {
		t.Fatalf("Location = %q, want %q", got, unauthorizedPath)
	}
}

func TestUnauthorizedPageExplainsAdminAuthentication(t *testing.T) {
	d := New(nil, nil, nil, nil, dashboardTestStore(t), nil)
	request := httptest.NewRequest(http.MethodGet, Prefix+"unauthorized", nil)
	response := httptest.NewRecorder()

	d.Handler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if got := response.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html; charset=utf-8", got)
	}
	for _, text := range []string{
		"Unauthorized",
		"You need to authenticate to access the HTTP Broker dashboard.",
		"http-broker token show admin",
		"http://localhost:PORT/dashboard/?token=&lt;admin-token&gt;",
		"An agent credential cannot authenticate here.",
	} {
		if !strings.Contains(response.Body.String(), text) {
			t.Errorf("body does not contain %q", text)
		}
	}
}

func dashboardTestStore(t *testing.T) *auth.Store {
	t.Helper()
	store, err := auth.NewStore(auth.TokenSet{Agent: strings.Repeat("a", 64), Admin: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return store
}

func TestUnknownRootPathIs404(t *testing.T) {
	d := New(nil, nil, nil, nil, dashboardTestStore(t), nil)
	srv := httptest.NewServer(d.Handler())
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/nope", nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /nope status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}
