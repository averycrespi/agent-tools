package httpboundary

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

func TestValidateAuthorityAcceptsOnlyNumericIPv4Loopback(t *testing.T) {
	t.Parallel()
	for _, authority := range []string{"127.0.0.1:8210", "127.12.34.56:1"} {
		if err := ValidateAuthority(authority); err != nil {
			t.Fatalf("ValidateAuthority(%q): %v", authority, err)
		}
	}
	for _, authority := range []string{"localhost:8210", "[::1]:8210", "0.0.0.0:8210", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:http"} {
		if err := ValidateAuthority(authority); err == nil {
			t.Errorf("ValidateAuthority(%q) succeeded", authority)
		}
	}
}

func TestBoundaryRejectsBeforeAuthenticationAndRoutesExactly(t *testing.T) {
	t.Parallel()
	var authCalls atomic.Int64
	boundary, err := New(Options{
		Authority: contract.DefaultAuthority,
		Ready:     func() bool { return true },
		Authenticate: func(ctx context.Context, request *http.Request, authority contract.CredentialAuthority) (context.Context, error) {
			authCalls.Add(1)
			return ctx, nil
		},
		Next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusNoContent)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, method, target, host string
		headers                    map[string]string
		status                     int
		allow                      string
	}{
		{name: "health", method: "GET", target: "/livez", host: contract.DefaultAuthority, status: 200},
		{name: "unknown", method: "GET", target: "/missing", host: contract.DefaultAuthority, status: 404},
		{name: "head not inherited", method: "HEAD", target: "/livez", host: contract.DefaultAuthority, status: 405, allow: "GET"},
		{name: "exact allow", method: "PUT", target: "/mcp", host: contract.DefaultAuthority, status: 405, allow: "DELETE, GET, POST"},
		{name: "host alias", method: "GET", target: "/api/v1/system-status", host: "localhost:8210", status: 421},
		{name: "forwarded", method: "GET", target: "/api/v1/system-status", host: contract.DefaultAuthority, headers: map[string]string{"Forwarded": "host=evil"}, status: 400},
		{name: "x forwarded", method: "GET", target: "/api/v1/system-status", host: contract.DefaultAuthority, headers: map[string]string{"X-Forwarded-For": "127.0.0.1"}, status: 400},
		{name: "origin", method: "GET", target: "/api/v1/system-status", host: contract.DefaultAuthority, headers: map[string]string{"Origin": "http://localhost:8210"}, status: 403},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.target, nil)
			request.Host = test.host
			for name, value := range test.headers {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			boundary.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.status, response.Body.String())
			}
			if got := response.Header().Get("Allow"); got != test.allow {
				t.Errorf("Allow = %q, want %q", got, test.allow)
			}
			if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("unexpected CORS header %q", got)
			}
		})
	}
	if authCalls.Load() != 0 {
		t.Fatalf("authentication called %d times for early rejects", authCalls.Load())
	}
}

func TestBoundaryHealthAndReadyResponsesAreMinimal(t *testing.T) {
	t.Parallel()
	ready := false
	boundary, err := New(Options{Authority: contract.DefaultAuthority, Ready: func() bool { return ready }})
	if err != nil {
		t.Fatal(err)
	}
	request := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = contract.DefaultAuthority
		boundary.ServeHTTP(response, r)
		return response
	}
	if response := request("/livez"); response.Code != 200 || response.Body.String() != "{\"status\":\"live\"}\n" {
		t.Fatalf("live response: %d %q", response.Code, response.Body.String())
	}
	if response := request("/readyz"); response.Code != 503 || response.Body.String() != "{\"status\":\"not_ready\"}\n" {
		t.Fatalf("not-ready response: %d %q", response.Code, response.Body.String())
	}
	ready = true
	if response := request("/readyz"); response.Code != 200 || response.Body.String() != "{\"status\":\"ready\"}\n" {
		t.Fatalf("ready response: %d %q", response.Code, response.Body.String())
	}
}

func TestBoundaryAdminWorkRejectsNPlusOneWithoutQueuing(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	boundary, err := New(Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, _ *http.Request, _ contract.CredentialAuthority) (context.Context, error) {
			return ctx, nil
		},
		Next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			started <- struct{}{}
			<-release
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/system-status", nil)
			request.Host = contract.DefaultAuthority
			boundary.ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	for range 16 {
		<-started
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system-status", nil)
	request.Host = contract.DefaultAuthority
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("N+1 status = %d, want 429", response.Code)
	}
	close(release)
	group.Wait()
}

func TestEventStreamsDoNotConsumeAuthenticatedAdminWorkCapacity(t *testing.T) {
	t.Parallel()
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	boundary, err := New(Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, _ *http.Request, _ contract.CredentialAuthority) (context.Context, error) {
			return ctx, nil
		},
		Next: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == "/api/v1/events" {
				started <- struct{}{}
				<-release
			}
			writer.WriteHeader(http.StatusNoContent)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil)
			request.Host = contract.DefaultAuthority
			boundary.ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	for range 16 {
		<-started
	}
	status := httptest.NewRequest(http.MethodGet, "/api/v1/system-status", nil)
	status.Host = contract.DefaultAuthority
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, status)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status while events saturated = %d", response.Code)
	}
	close(release)
	group.Wait()
}

func TestDrainingRejectsNewWorkButKeepsHealthAndStatus(t *testing.T) {
	t.Parallel()
	boundary, err := New(Options{
		Authority: contract.DefaultAuthority,
		Ready:     func() bool { return false }, Draining: func() bool { return true },
		Authenticate: func(ctx context.Context, request *http.Request, _ contract.CredentialAuthority) (context.Context, error) {
			if request.Header.Get("Authorization") == "" {
				return ctx, Error{Code: contract.ProblemAuthenticationRequired}
			}
			return ctx, nil
		},
		Next: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusNoContent) }),
	})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{"/livez": 200, "/readyz": 503, "/api/v1/system-status": 204, "/api/v1/backups": 503} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Host = contract.DefaultAuthority
		if strings.HasPrefix(path, "/api/") {
			request.Header.Set("Authorization", "Bearer valid")
		}
		response := httptest.NewRecorder()
		boundary.ServeHTTP(response, request)
		if response.Code != want {
			t.Errorf("%s = %d, want %d: %s", path, response.Code, want, response.Body.String())
		}
	}
	unauthenticated := httptest.NewRequest(http.MethodGet, "/api/v1/backups", nil)
	unauthenticated.Host = contract.DefaultAuthority
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, unauthenticated)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("drain bypassed authentication: %d %s", response.Code, response.Body.String())
	}
}

func TestOpenListenerProbesBeforeBinding(t *testing.T) {
	t.Parallel()
	var probed atomic.Bool
	listenCalled := false
	_, capability, err := openListener(context.Background(), "127.0.0.1:8210", func(context.Context) (contract.KeyringCapability, error) {
		probed.Store(true)
		return contract.KeyringReady, nil
	}, func(_, _ string) (net.Listener, error) {
		listenCalled = true
		if !probed.Load() {
			t.Fatal("listener opened before keyring probe")
		}
		return nil, errors.New("injected listen failure")
	})
	if !listenCalled || capability != contract.KeyringReady || err == nil {
		t.Fatalf("called=%v capability=%q err=%v", listenCalled, capability, err)
	}
}

func TestBoundaryRejectsOversizedInputBeforeHandler(t *testing.T) {
	t.Parallel()
	var called atomic.Bool
	boundary, err := New(Options{Authority: contract.DefaultAuthority, Next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called.Store(true) })})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/system-status", nil)
	request.Host = contract.DefaultAuthority
	request.Header.Set("X-Large", strings.Repeat("x", 8193))
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	if response.Code != 400 || called.Load() {
		t.Fatalf("oversized input response = %d, called=%v", response.Code, called.Load())
	}
}
