package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/backup"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/events"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
)

const (
	testBearer = "mgw_admin_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testID     = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
)

type fakeCredentials struct{ items []contract.AdminCredential }

func (credentials *fakeCredentials) Authenticate(_ context.Context, bearer string) (contract.AdminCredential, error) {
	if strings.HasPrefix(bearer, contract.AgentBearerPrefix) {
		return contract.AdminCredential{}, admin.ErrCredentialDomainMismatch
	}
	if bearer != testBearer {
		return contract.AdminCredential{}, admin.ErrAuthenticationRequired
	}
	return credentials.items[0], nil
}
func (credentials *fakeCredentials) Create(_ context.Context, expires *time.Time) (contract.CreatedAdminCredential, error) {
	created := credentials.items[0]
	created.ID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	if expires != nil {
		value := expires.UTC().Format(time.RFC3339Nano)
		created.ExpiresAt, created.NonExpiring = &value, false
	}
	credentials.items = append(credentials.items, created)
	return contract.CreatedAdminCredential{AdminCredential: created, Bearer: "mgw_admin_ONETIME"}, nil
}
func (credentials *fakeCredentials) Get(_ context.Context, id string) (contract.AdminCredential, error) {
	for _, item := range credentials.items {
		if item.ID == id {
			return item, nil
		}
	}
	return contract.AdminCredential{}, admin.ErrNotFound
}
func (credentials *fakeCredentials) List(context.Context) ([]contract.AdminCredential, error) {
	return append([]contract.AdminCredential(nil), credentials.items...), nil
}
func (credentials *fakeCredentials) Revoke(context.Context, string) error { return nil }

type fakeBackups struct {
	items []contract.Backup
}

func (backups *fakeBackups) Create(_ context.Context, authorityID, key string) (contract.Backup, bool, error) {
	if key == "bad key" {
		return contract.Backup{}, false, backup.ErrInvalidIdempotency
	}
	for _, item := range backups.items {
		if key == "retry" {
			return item, true, nil
		}
	}
	item := contract.Backup{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAX", CreatedAt: "2026-08-22T18:00:00Z", InstallationID: testID, SchemaVersion: "3", SourceRevision: "1", SizeBytes: 4096, SHA256: strings.Repeat("a", 64)}
	backups.items = append(backups.items, item)
	return item, false, nil
}
func (backups *fakeBackups) List(context.Context) ([]contract.Backup, error) {
	return append([]contract.Backup(nil), backups.items...), nil
}
func (backups *fakeBackups) Get(_ context.Context, id string) (contract.Backup, error) {
	for _, item := range backups.items {
		if item.ID == id {
			return item, nil
		}
	}
	return contract.Backup{}, backup.ErrNotFound
}
func (backups *fakeBackups) Delete(_ context.Context, id string) error {
	if len(backups.items) == 0 || backups.items[0].ID != id {
		return backup.ErrNotFound
	}
	backups.items = nil
	return nil
}

type fakeSessions struct{}

func (fakeSessions) Exchange(context.Context, string) (admin.CreatedSession, error) {
	return admin.CreatedSession{ID: "session", CSRFToken: "csrf", IdleExpiresAt: time.Date(2026, 8, 22, 19, 0, 0, 0, time.UTC), AbsoluteExpiresAt: time.Date(2026, 8, 23, 2, 0, 0, 0, time.UTC)}, nil
}
func (fakeSessions) Authenticate(_ context.Context, bearer, session, csrf string, requireCSRF bool) (contract.AdminCredential, error) {
	if bearer != "" && session != "" {
		return contract.AdminCredential{}, admin.ErrAmbiguousCredentials
	}
	if session != "session" || (requireCSRF && csrf != "csrf") {
		return contract.AdminCredential{}, admin.ErrAuthenticationRequired
	}
	return credential(), nil
}
func (fakeSessions) Logout(session string) error {
	if session != "session" {
		return admin.ErrAuthenticationRequired
	}
	return nil
}
func (fakeSessions) Subscribe(session string) (<-chan struct{}, error) {
	if session != "session" {
		return nil, admin.ErrAuthenticationRequired
	}
	return make(chan struct{}), nil
}
func (fakeSessions) Status() contract.LimitStatus { return contract.LimitStatus{InUse: 1, Limit: 128} }

func credential() contract.AdminCredential {
	return contract.AdminCredential{ID: testID, Fingerprint: "0123456789abcdef", CreatedAt: "2026-08-22T18:00:00Z", NonExpiring: true, Status: contract.CredentialActive, Revision: "1"}
}

func newTestHandler(t *testing.T) http.Handler {
	t.Helper()
	credentials := &fakeCredentials{items: []contract.AdminCredential{credential()}}
	handler := New(Options{Credentials: credentials, Sessions: fakeSessions{}, Backups: &fakeBackups{}, Status: func() contract.SystemStatus {
		return contract.SystemStatus{
			Process:   contract.ProcessStatus{State: contract.ProcessReady, Ready: true, StartedAt: "2026-08-22T18:00:00Z"},
			SQLite:    contract.SQLiteStatus{State: contract.SQLiteReady, SchemaVersion: "3", Revision: "1"},
			Keyring:   contract.KeyringStatus{Capability: contract.KeyringInteractionRequired},
			Limits:    contract.LimitsStatus{KeyringWork: contract.LimitStatus{InUse: 1, Limit: 1, Saturated: true}},
			Backup:    contract.BackupStatus{State: contract.BackupIdle},
			Protocols: contract.ProtocolStatus{Modern: contract.ModernProtocolVersion, Legacy: contract.LegacyProtocolVersion, AgentAuth: contract.AgentAuthDenyAll},
		}
	}})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	if err != nil {
		t.Fatal(err)
	}
	return boundary
}

func perform(handler http.Handler, method, target, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.Host = contract.DefaultAuthority
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func TestSystemStatusIsAuthenticatedNoStoreAndClosed(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	response := perform(handler, http.MethodGet, "/api/v1/system-status", "", map[string]string{"Authorization": "Bearer " + testBearer})
	if response.Code != 200 {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("ETag") != "" {
		t.Fatalf("unsafe caching headers: %v", response.Header())
	}
	if got := response.Body.String(); !strings.Contains(got, `"keyring":{"capability":"interaction_required"}`) || strings.Contains(got, "remediation") || !strings.Contains(got, `"keyring_work":{"in_use":1,"limit":1,"saturated":true}`) {
		t.Fatalf("unsafe or incomplete status: %s", got)
	}
}

func TestAuthenticationDomainsAndAmbiguity(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	wrong := perform(handler, http.MethodGet, "/api/v1/system-status", "", map[string]string{"Authorization": "Bearer mgw_agent_value"})
	if wrong.Code != 403 || !strings.Contains(wrong.Body.String(), "credential_domain_mismatch") {
		t.Fatalf("wrong domain: %d %s", wrong.Code, wrong.Body.String())
	}
	ambiguous := perform(handler, http.MethodGet, "/api/v1/system-status", "", map[string]string{"Authorization": "Bearer " + testBearer, "Cookie": contract.SessionCookieName + "=session"})
	if ambiguous.Code != 400 || !strings.Contains(ambiguous.Body.String(), "ambiguous_credentials") {
		t.Fatalf("ambiguous: %d %s", ambiguous.Code, ambiguous.Body.String())
	}
}

func TestSessionExchangeAndCSRFLifecycle(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	exchange := perform(handler, http.MethodPost, "/api/v1/admin-sessions", `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON})
	if exchange.Code != 201 || !strings.Contains(exchange.Header().Get("Set-Cookie"), "HttpOnly") || !strings.Contains(exchange.Header().Get("Set-Cookie"), "SameSite=Strict") || strings.Contains(exchange.Header().Get("Set-Cookie"), "Domain=") {
		t.Fatalf("exchange: %d %s %s", exchange.Code, exchange.Header().Get("Set-Cookie"), exchange.Body.String())
	}
	logout := perform(handler, http.MethodDelete, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin, "Content-Type": contract.MediaTypeJSON, "X-CSRF-Token": "csrf"})
	if logout.Code != 204 {
		t.Fatalf("logout: %d %s", logout.Code, logout.Body.String())
	}
	missingOrigin := perform(handler, http.MethodDelete, "/api/v1/admin-sessions/current", `{}`, map[string]string{"Cookie": contract.SessionCookieName + "=session", "Content-Type": contract.MediaTypeJSON, "X-CSRF-Token": "csrf"})
	if missingOrigin.Code != 403 || !strings.Contains(missingOrigin.Body.String(), "forbidden_origin") {
		t.Fatalf("missing origin: %d %s", missingOrigin.Code, missingOrigin.Body.String())
	}
}

func TestCredentialCreateUsesStrictJSON(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	for name, body := range map[string]string{
		"duplicate": `{"expires_at":null,"expires_at":null}`,
		"unknown":   `{"expires_at":null,"other":1}`,
		"missing":   `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(handler, http.MethodPost, "/api/v1/admin-credentials", body, headers)
			if response.Code != 400 || !strings.Contains(response.Body.String(), "invalid_json") {
				t.Fatalf("response: %d %s", response.Code, response.Body.String())
			}
		})
	}
	valid := perform(handler, http.MethodPost, "/api/v1/admin-credentials", `{"expires_at":null}`, headers)
	if valid.Code != 201 || !strings.Contains(valid.Body.String(), `"bearer":"mgw_admin_ONETIME"`) {
		t.Fatalf("valid create: %d %s", valid.Code, valid.Body.String())
	}
}

func TestOAuthCallbackIsDenyByDefaultAndDoesNotEchoInput(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	response := perform(handler, http.MethodGet, "/oauth/callback?state=canary-secret&code=also-secret", "", nil)
	if response.Code != 400 || !strings.Contains(response.Body.String(), "invalid_oauth_state") {
		t.Fatalf("callback: %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "canary-secret") || strings.Contains(response.Body.String(), "also-secret") {
		t.Fatalf("callback echoed input: %s", response.Body.String())
	}
}

func TestStaticShellHasRestrictiveCSPAndNoExternalActiveContent(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	response := perform(handler, http.MethodGet, "/", "", nil)
	if response.Code != 200 {
		t.Fatalf("shell: %d %s", response.Code, response.Body.String())
	}
	csp := response.Header().Get("Content-Security-Policy")
	for _, required := range []string{"default-src 'self'", "base-uri 'none'", "object-src 'none'", "frame-ancestors 'none'", "form-action 'self'"} {
		if !strings.Contains(csp, required) {
			t.Errorf("CSP %q lacks %q", csp, required)
		}
	}
	if strings.Contains(csp, "unsafe-") || strings.Contains(response.Body.String(), "https://") || strings.Contains(response.Body.String(), "<script") {
		t.Fatalf("unsafe shell content or CSP: %q %s", csp, response.Body.String())
	}
	asset := perform(handler, http.MethodGet, "/assets/app.css", "", nil)
	if asset.Code != 200 || asset.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Fatalf("asset: %d %v", asset.Code, asset.Header())
	}
}

type streamWriter struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	status  int
	flushed chan struct{}
}

func newStreamWriter() *streamWriter {
	return &streamWriter{header: make(http.Header), flushed: make(chan struct{}, 8)}
}
func (writer *streamWriter) Header() http.Header { return writer.header }
func (writer *streamWriter) WriteHeader(status int) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.status == 0 {
		writer.status = status
	}
}
func (writer *streamWriter) Write(contents []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.body.Write(contents)
}
func (writer *streamWriter) Flush() {
	select {
	case writer.flushed <- struct{}{}:
	default:
	}
}
func (writer *streamWriter) snapshot() (int, string) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.status, writer.body.String()
}

func TestEventStreamIsInvalidationOnlyAndRejectsReplay(t *testing.T) {
	credentials := &fakeCredentials{items: []contract.AdminCredential{credential()}}
	hub := events.New()
	t.Cleanup(hub.Shutdown)
	keepalive := make(chan time.Time, 1)
	handler := New(Options{
		Credentials: credentials, Sessions: fakeSessions{}, Events: hub, Invalidate: hub.Publish,
		NewKeepalive: func() (<-chan time.Time, func()) { return keepalive, func() {} },
	})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	if err != nil {
		t.Fatal(err)
	}

	invalid := perform(boundary, http.MethodGet, "/api/v1/events", "", map[string]string{"Authorization": "Bearer " + testBearer, "Last-Event-ID": "old"})
	if invalid.Code != http.StatusBadRequest || !strings.Contains(invalid.Body.String(), "malformed_request") {
		t.Fatalf("Last-Event-ID: %d %s", invalid.Code, invalid.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/events", nil).WithContext(ctx)
	request.Host = contract.DefaultAuthority
	request.Header.Set("Authorization", "Bearer "+testBearer)
	writer := newStreamWriter()
	done := make(chan struct{})
	go func() {
		boundary.ServeHTTP(writer, request)
		close(done)
	}()
	<-writer.flushed
	hub.Publish(contract.Invalidation{Kind: contract.InvalidationBackups, ResourceID: ptr(testID)})
	<-writer.flushed
	keepalive <- time.Time{}
	<-writer.flushed
	cancel()
	<-done

	status, body := writer.snapshot()
	if status != http.StatusOK || writer.header.Get("Content-Type") != contract.MediaTypeEventStream || writer.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("handshake: %d %v", status, writer.header)
	}
	if strings.HasPrefix(body, "id:") || strings.Contains(body, "\nid:") || strings.Contains(body, testBearer) || !strings.Contains(body, "event: invalidate\ndata: {\"kind\":\"backups\",\"resource_id\":\""+testID+"\"}") || strings.Count(body, ": keepalive") != 2 {
		t.Fatalf("unsafe event stream: %q", body)
	}
	if hub.Status().InUse != 0 {
		t.Fatalf("stream permit leaked: %+v", hub.Status())
	}
}

func ptr(value string) *string { return &value }

func TestBackupResourcesAreIdempotentBoundedAndNoStore(t *testing.T) {
	t.Parallel()
	credentials := &fakeCredentials{items: []contract.AdminCredential{credential()}}
	backups := &fakeBackups{}
	var invalidations []contract.Invalidation
	handler := New(Options{Credentials: credentials, Sessions: fakeSessions{}, Backups: backups, Invalidate: func(event contract.Invalidation) {
		invalidations = append(invalidations, event)
	}})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON, "Idempotency-Key": "retry"}
	created := perform(boundary, http.MethodPost, "/api/v1/backups", `{}`, headers)
	if created.Code != 201 || created.Header().Get("Cache-Control") != "no-store" || created.Header().Get("ETag") != "" {
		t.Fatalf("create: %d %v %s", created.Code, created.Header(), created.Body.String())
	}
	replayed := perform(boundary, http.MethodPost, "/api/v1/backups", `{}`, headers)
	if replayed.Code != 200 || replayed.Body.String() != created.Body.String() || len(invalidations) != 2 {
		t.Fatalf("replay: %d %s invalidations=%d", replayed.Code, replayed.Body.String(), len(invalidations))
	}
	listed := perform(boundary, http.MethodGet, "/api/v1/backups?limit=50", "", map[string]string{"Authorization": "Bearer " + testBearer})
	if listed.Code != 200 || !strings.Contains(listed.Body.String(), `"next_cursor":null`) {
		t.Fatalf("list: %d %s", listed.Code, listed.Body.String())
	}
	id := backups.items[0].ID
	got := perform(boundary, http.MethodGet, "/api/v1/backups/"+id, "", map[string]string{"Authorization": "Bearer " + testBearer})
	if got.Code != 200 || got.Header().Get("ETag") != "" {
		t.Fatalf("get: %d %s", got.Code, got.Body.String())
	}
	deleted := perform(boundary, http.MethodDelete, "/api/v1/backups/"+id, `{}`, map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON})
	if deleted.Code != 204 || len(invalidations) != 4 || invalidations[2].Kind != contract.InvalidationBackups {
		t.Fatalf("delete: %d %s invalidations=%v", deleted.Code, deleted.Body.String(), invalidations)
	}
}

func TestBackupCreateRequiresValidIdempotencyKeyAndExactBody(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	base := map[string]string{"Authorization": "Bearer " + testBearer, "Content-Type": contract.MediaTypeJSON}
	missing := perform(handler, http.MethodPost, "/api/v1/backups", `{}`, base)
	if missing.Code != 400 || !strings.Contains(missing.Body.String(), "invalid_idempotency_key") {
		t.Fatalf("missing key: %d %s", missing.Code, missing.Body.String())
	}
	base["Idempotency-Key"] = "retry"
	invalidBody := perform(handler, http.MethodPost, "/api/v1/backups", `{"extra":true}`, base)
	if invalidBody.Code != 400 || !strings.Contains(invalidBody.Body.String(), "invalid_json") {
		t.Fatalf("invalid body: %d %s", invalidBody.Code, invalidBody.Body.String())
	}
	base["Idempotency-Key"] = "bad key"
	invalidKey := perform(handler, http.MethodPost, "/api/v1/backups", `{}`, base)
	if invalidKey.Code != 400 || !strings.Contains(invalidKey.Body.String(), "invalid_idempotency_key") {
		t.Fatalf("invalid key: %d %s", invalidKey.Code, invalidKey.Body.String())
	}
}

func TestCredentialPaginationRejectsCrossCollectionCursor(t *testing.T) {
	t.Parallel()
	handler := newTestHandler(t)
	headers := map[string]string{"Authorization": "Bearer " + testBearer}
	response := perform(handler, http.MethodGet, "/api/v1/admin-credentials?limit=1", "", headers)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"next_cursor":`) {
		t.Fatalf("list: %d %s", response.Code, response.Body.String())
	}
	invalid := perform(handler, http.MethodGet, "/api/v1/admin-credentials?cursor=dmVyMQBiYWNrdXBzADA", "", headers)
	if invalid.Code != 400 || !strings.Contains(invalid.Body.String(), "invalid_cursor") {
		t.Fatalf("cursor: %d %s", invalid.Code, invalid.Body.String())
	}
	unknown := perform(handler, http.MethodGet, "/api/v1/admin-credentials?other=1", "", headers)
	if unknown.Code != 400 || !strings.Contains(unknown.Body.String(), "malformed_request") {
		t.Fatalf("unknown query: %d %s", unknown.Code, unknown.Body.String())
	}
}
