package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/proxy"
	"github.com/stretchr/testify/require"
)

// ---------- helpers ----------

func newTestServer(t *testing.T, deps Deps) (*httptest.Server, string) {
	t.Helper()
	if deps.AdminTokenPath == "" {
		dir := t.TempDir()
		deps.AdminTokenPath = filepath.Join(dir, "admin-token")
	}
	s := New(deps)
	h := s.Handler()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	// read back generated token
	data, err := os.ReadFile(deps.AdminTokenPath)
	require.NoError(t, err)
	return srv, strings.TrimSpace(string(data))
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// ---------- EnsureAdminToken ----------

func TestEnsureAdminToken_CreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin-token")
	tok, err := EnsureAdminToken(path)
	require.NoError(t, err)
	require.Len(t, tok, 64)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, tok, strings.TrimSpace(string(data)))
}

func TestEnsureAdminToken_ReuseExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "admin-token")
	tok1, err := EnsureAdminToken(path)
	require.NoError(t, err)
	tok2, err := EnsureAdminToken(path)
	require.NoError(t, err)
	require.Equal(t, tok1, tok2)
}

// ---------- auth middleware ----------

func TestAuth_ValidCookiePassesThrough(t *testing.T) {
	srv, token := newTestServer(t, Deps{})

	req, _ := http.NewRequest("GET", srv.URL+"/dashboard/api/pending", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: token})
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAuth_ValidBearerPassesThrough(t *testing.T) {
	srv, token := newTestServer(t, Deps{})

	req, _ := http.NewRequest("GET", srv.URL+"/dashboard/api/pending", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestAuth_TokenQueryParamSetsCookieAndRedirects(t *testing.T) {
	srv, token := newTestServer(t, Deps{})

	resp, err := noRedirect().Get(srv.URL + "/dashboard/?token=" + token + "&foo=bar")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/?foo=bar", resp.Header.Get("Location"))

	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, cookieName, cookies[0].Name)
	require.Equal(t, token, cookies[0].Value)
	require.True(t, cookies[0].HttpOnly)
}

func TestAuth_NoAuthDashboardRedirectsToUnauthorized(t *testing.T) {
	srv, _ := newTestServer(t, Deps{})

	resp, err := noRedirect().Get(srv.URL + "/dashboard/")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/unauthorized", resp.Header.Get("Location"))
}

func TestAuth_UnauthorizedPageAllowedWithoutAuth(t *testing.T) {
	srv, _ := newTestServer(t, Deps{})

	resp, err := http.Get(srv.URL + "/dashboard/unauthorized")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "Unauthorized")
	require.Contains(t, string(body), `<form method="POST" action="/dashboard/unauthorized">`)
}

func TestAuth_UnauthorizedPOST_ValidToken_SetsCookieAndRedirects(t *testing.T) {
	srv, token := newTestServer(t, Deps{})

	form := strings.NewReader("token=" + token)
	resp, err := noRedirect().Post(srv.URL+"/dashboard/unauthorized", "application/x-www-form-urlencoded", form)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusFound, resp.StatusCode)
	require.Equal(t, "/dashboard/", resp.Header.Get("Location"))

	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	require.Equal(t, cookieName, cookies[0].Name)
}

func TestAuth_UnauthorizedPOST_InvalidToken_Returns401(t *testing.T) {
	srv, _ := newTestServer(t, Deps{})

	form := strings.NewReader("token=wrongtoken")
	resp, err := http.DefaultClient.Post(srv.URL+"/dashboard/unauthorized", "application/x-www-form-urlencoded", form)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}

func TestAuth_CAPemUnauthenticated(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, []byte("FAKE CA"), 0o644))

	srv, _ := newTestServer(t, Deps{CAPath: caPath})

	// No auth at all — should still succeed.
	resp, err := http.Get(srv.URL + "/ca.pem")
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := io.ReadAll(resp.Body)
	require.Equal(t, "FAKE CA", string(body))
}

// ---------- API smoke tests ----------

func authedGet(t *testing.T, srv *httptest.Server, token, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", srv.URL+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestAPI_Pending_ReturnsJSON(t *testing.T) {
	srv, token := newTestServer(t, Deps{})
	resp := authedGet(t, srv, token, "/dashboard/api/pending")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	var out []any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	require.NotNil(t, out)
}

func TestAPI_Audit_ReturnsJSON(t *testing.T) {
	srv, token := newTestServer(t, Deps{})
	resp := authedGet(t, srv, token, "/dashboard/api/audit")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")
}

func TestAPI_Agents_ReturnsJSON(t *testing.T) {
	srv, token := newTestServer(t, Deps{})
	resp := authedGet(t, srv, token, "/dashboard/api/agents")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")
}

func TestAPI_Rules_ReturnsJSON(t *testing.T) {
	srv, token := newTestServer(t, Deps{})
	resp := authedGet(t, srv, token, "/dashboard/api/rules")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")
}

func TestAPI_Secrets_ReturnsJSON(t *testing.T) {
	srv, token := newTestServer(t, Deps{})
	resp := authedGet(t, srv, token, "/dashboard/api/secrets")
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	// Must not leak values — only metadata keys.
	body, _ := io.ReadAll(resp.Body)
	require.NotContains(t, strings.ToLower(string(body)), "plaintext")
	require.NotContains(t, strings.ToLower(string(body)), "value")
}

func TestAPI_Decide_UnknownID_Returns404(t *testing.T) {
	broker := newFakeBroker()
	srv, token := newTestServer(t, Deps{Approval: broker})

	payload := `{"id":"nonexistent","decision":"approve"}`
	req, _ := http.NewRequest("POST", srv.URL+"/dashboard/api/decide", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAPI_Decide_ApprovesPendingRequest(t *testing.T) {
	broker := newFakeBroker()
	srv, token := newTestServer(t, Deps{Approval: broker})

	// Register a pending request.
	reqID := "test-req-1"
	broker.addPending(proxy.ApprovalRequest{RequestID: reqID})

	payload := `{"id":"` + reqID + `","decision":"approve"}`
	req, _ := http.NewRequest("POST", srv.URL+"/dashboard/api/decide", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, proxy.DecisionApproved, broker.lastDecision)
}

// ---------- SSE broker ----------

func TestSSE_SubscribeReceivesBroadcast(t *testing.T) {
	b := newSSEBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := b.Subscribe(ctx)

	ev := Event{Kind: "test", Data: map[string]string{"hello": "world"}}
	b.Broadcast(ev)

	select {
	case frame := <-ch:
		s := string(frame)
		require.Contains(t, s, "event: test\n")
		require.Contains(t, s, "data: ")
		require.Contains(t, s, "\n\n")
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for SSE event")
	}
}

func TestSSE_DropOnFull(t *testing.T) {
	b := newSSEBroker()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := b.Subscribe(ctx)
	ev := Event{Kind: "fill", Data: "x"}

	// Fill the buffer (32) + 1 extra that should be dropped.
	for i := 0; i < sseBufferSize+10; i++ {
		b.Broadcast(ev)
	}

	// Channel must not block and must have at most sseBufferSize items.
	count := 0
drain:
	for {
		select {
		case <-ch:
			count++
		default:
			break drain
		}
	}
	require.LessOrEqual(t, count, sseBufferSize)
}

func TestSSE_SubscriberDeregisteredOnCtxCancel(t *testing.T) {
	b := newSSEBroker()
	ctx, cancel := context.WithCancel(context.Background())

	b.Subscribe(ctx)
	b.mu.Lock()
	require.Len(t, b.subscribers, 1)
	b.mu.Unlock()

	cancel()
	time.Sleep(50 * time.Millisecond)

	b.mu.Lock()
	require.Empty(t, b.subscribers)
	b.mu.Unlock()
}

func TestSSE_Events_KeepaliveComment(t *testing.T) {
	srv, token := newTestServer(t, Deps{})

	req, _ := http.NewRequest("GET", srv.URL+"/dashboard/api/events", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req = req.WithContext(ctx)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	// Read lines until we see the keepalive comment.
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == ": keepalive" {
			found = true
			break
		}
	}
	require.True(t, found, "expected keepalive comment in SSE stream")
}

// ---------- fakes ----------

type fakeBroker struct {
	pending      []proxy.ApprovalRequest
	lastDecision proxy.ApprovalDecision
}

func newFakeBroker() *fakeBroker { return &fakeBroker{} }

func (f *fakeBroker) addPending(r proxy.ApprovalRequest) {
	f.pending = append(f.pending, r)
}

func (f *fakeBroker) Pending() []proxy.ApprovalRequest { return f.pending }

func (f *fakeBroker) Decide(id string, d proxy.ApprovalDecision) error {
	for i, p := range f.pending {
		if p.RequestID == id {
			f.pending = append(f.pending[:i], f.pending[i+1:]...)
			f.lastDecision = d
			return nil
		}
	}
	return ErrUnknownIDForTest
}

// ErrUnknownIDForTest is used by fakeBroker to simulate approval.ErrUnknownID.
// We import the real error via the approval package in production code; here we
// need errors.Is to work so we use the real error type.
var ErrUnknownIDForTest = errUnknownIDTest{}

type errUnknownIDTest struct{}

func (errUnknownIDTest) Error() string { return "approval: unknown request id" }
func (errUnknownIDTest) Is(target error) bool {
	// Make errors.Is(err, approval.ErrUnknownID) pass.
	return target.Error() == "approval: unknown request id"
}
