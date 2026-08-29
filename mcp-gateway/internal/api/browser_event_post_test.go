package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/admin"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/events"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type postEventSessions struct{ fakeSessions }

func (postEventSessions) Authenticate(_ context.Context, bearer, session, csrf string, requireCSRF bool) (contract.AdminCredential, error) {
	if bearer != "" || session != "session" {
		return contract.AdminCredential{}, admin.ErrAuthenticationRequired
	}
	if requireCSRF && csrf != "csrf" {
		return contract.AdminCredential{}, admin.ErrCSRF
	}
	return credential(), nil
}

func TestBrowserEventPostAPI(t *testing.T) {
	hub := events.New()
	t.Cleanup(hub.Shutdown)
	keepalive := make(chan time.Time)
	handler := New(Options{
		Credentials: &fakeCredentials{items: []contract.AdminCredential{credential()}}, Sessions: postEventSessions{}, Events: hub,
		NewKeepalive: func() (<-chan time.Time, func()) { return keepalive, func() {} },
	})
	boundary, err := httpboundary.New(httpboundary.Options{Authority: contract.DefaultAuthority, Authenticate: handler.Authenticate, Next: handler})
	require.NoError(t, err)

	bearer := perform(boundary, http.MethodPost, "/api/v1/events", `{}`, map[string]string{
		"Authorization": "Bearer " + testBearer, "Origin": contract.CanonicalOrigin, "Content-Type": contract.MediaTypeJSON,
	})
	assert.Equal(t, http.StatusUnauthorized, bearer.Code)
	missingOrigin := perform(boundary, http.MethodPost, "/api/v1/events", `{}`, map[string]string{
		"Cookie": contract.SessionCookieName + "=session", "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON,
	})
	assert.Equal(t, http.StatusForbidden, missingOrigin.Code)
	missingCSRF := perform(boundary, http.MethodPost, "/api/v1/events", `{}`, map[string]string{
		"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin, "Content-Type": contract.MediaTypeJSON,
	})
	assert.Equal(t, http.StatusForbidden, missingCSRF.Code)
	ambiguous := perform(boundary, http.MethodPost, "/api/v1/events", `{}`, map[string]string{
		"Cookie": contract.SessionCookieName + "=session", "Authorization": "Bearer " + testBearer,
		"Origin": contract.CanonicalOrigin, "X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON,
	})
	assert.Equal(t, http.StatusBadRequest, ambiguous.Code)

	sessionHeaders := map[string]string{
		"Cookie": contract.SessionCookieName + "=session", "Origin": contract.CanonicalOrigin,
		"X-CSRF-Token": "csrf", "Content-Type": contract.MediaTypeJSON,
	}
	for name, targetAndBody := range map[string][2]string{
		"query":         {"/api/v1/events?cursor=x", `{}`},
		"last event id": {"/api/v1/events", `{}`},
		"empty body":    {"/api/v1/events", ""},
		"nonempty":      {"/api/v1/events", `{"value":1}`},
	} {
		t.Run(name, func(t *testing.T) {
			headers := make(map[string]string, len(sessionHeaders)+1)
			for key, value := range sessionHeaders {
				headers[key] = value
			}
			if name == "last event id" {
				headers["Last-Event-ID"] = "old"
			}
			response := perform(boundary, http.MethodPost, targetAndBody[0], targetAndBody[1], headers)
			assert.Equal(t, http.StatusBadRequest, response.Code)
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/events", strings.NewReader(`{}`)).WithContext(ctx)
	request.Host = contract.DefaultAuthority
	for name, value := range sessionHeaders {
		request.Header.Set(name, value)
	}
	writer := newStreamWriter()
	done := make(chan struct{})
	go func() {
		boundary.ServeHTTP(writer, request)
		close(done)
	}()
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("POST stream did not flush its handshake")
	}
	hub.Publish(contract.Invalidation{Kind: contract.InvalidationSystemStatus})
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		t.Fatal("POST stream did not flush invalidation")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("POST stream did not close after cancellation")
	}

	status, body := writer.snapshot()
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, contract.MediaTypeEventStream, writer.header.Get("Content-Type"))
	assert.Equal(t, "keep-alive", writer.header.Get("Connection"))
	assert.Contains(t, body, ": keepalive\n\n")
	assert.Contains(t, body, "event: invalidate\ndata: {\"kind\":\"system_status\",\"resource_id\":null}\n\n")
	assert.NotContains(t, body, "id:")
	assert.Equal(t, int64(0), hub.Status().InUse)
}
