package mcpingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/httpboundary"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	legacyInitialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"fixture","version":"1"}}}`
	legacyPing       = `{"jsonrpc":"2.0","id":2,"method":"ping","params":{}}`
)

type manualTimer struct {
	callback func()
	stopped  bool
}

func (timer *manualTimer) Stop() bool {
	wasActive := !timer.stopped
	timer.stopped = true
	return wasActive
}

func (timer *manualTimer) Reset(time.Duration) bool {
	wasActive := !timer.stopped
	timer.stopped = false
	return wasActive
}

type manualScheduler struct {
	mu     sync.Mutex
	timers []*manualTimer
}

type immediateScheduler struct {
	timers []*manualTimer
}

func (scheduler *immediateScheduler) AfterFunc(_ time.Duration, callback func()) Timer {
	timer := &manualTimer{callback: callback}
	scheduler.timers = append(scheduler.timers, timer)
	callback()
	return timer
}

func (scheduler *manualScheduler) AfterFunc(_ time.Duration, callback func()) Timer {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	timer := &manualTimer{callback: callback}
	scheduler.timers = append(scheduler.timers, timer)
	return timer
}

func (scheduler *manualScheduler) Fire(index int) {
	scheduler.mu.Lock()
	timer := scheduler.timers[index]
	if timer.stopped {
		scheduler.mu.Unlock()
		return
	}
	timer.stopped = true
	callback := timer.callback
	scheduler.mu.Unlock()
	callback()
}

func (scheduler *manualScheduler) Stopped(index int) bool {
	scheduler.mu.Lock()
	defer scheduler.mu.Unlock()
	return scheduler.timers[index].stopped
}

func newLegacyHarness(t *testing.T, clock *testutil.FakeClock, entropy *testutil.FakeEntropy) (*Handler, *httpboundary.Boundary, *testAuthority) {
	t.Helper()
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	authority.add(t, "other", contract.VisibilityAll)
	handler := New(Options{Authenticator: authority, Now: clock.Now, Entropy: entropy})
	boundary := newLegacyBoundary(t, handler)
	return handler, boundary, authority
}

func newLegacyBoundary(t *testing.T, handler *Handler) *httpboundary.Boundary {
	t.Helper()
	boundary, err := httpboundary.New(httpboundary.Options{
		Authority: contract.DefaultAuthority,
		Authenticate: func(ctx context.Context, request *http.Request, credentialAuthority contract.CredentialAuthority) (context.Context, error) {
			return handler.Authenticate(ctx, request, credentialAuthority)
		},
		Next: handler,
	})
	require.NoError(t, err)
	return boundary
}

func legacyRequest(method, body, bearer, sessionID string) *http.Request {
	request := modernRequest(method, body)
	request.Header.Set("Authorization", "Bearer "+contract.AgentBearerPrefix+bearer)
	if sessionID != "" {
		request.Header.Set("Mcp-Session-Id", sessionID)
		request.Header.Set("Mcp-Protocol-Version", contract.LegacyProtocolVersion)
	}
	return request
}

func initializeLegacy(t *testing.T, boundary *httpboundary.Boundary) string {
	t.Helper()
	request := legacyRequest(http.MethodPost, legacyInitialize, "valid", "")
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	sessionID := response.Header().Get("Mcp-Session-Id")
	require.NotEmpty(t, sessionID)
	var envelope struct {
		Result struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &envelope))
	assert.Equal(t, contract.LegacyProtocolVersion, envelope.Result.ProtocolVersion)
	assert.Empty(t, envelope.Result.Capabilities)
	assert.NotContains(t, response.Body.String(), "listChanged")
	return sessionID
}

func TestLegacySessionBindsCredentialRevisionAndReleasesEveryTerminalPath(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	handler, boundary, authority := newLegacyHarness(t, clock, testutil.NewFakeEntropy(makeDistinctEntropy(4)))
	sessionID := initializeLegacy(t, boundary)
	leases := authority.captured()
	require.Len(t, leases, 1)
	assert.False(t, leaseDone(leases[0]), "session lease released after initialization")

	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", sessionID))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	leases = authority.captured()
	require.Len(t, leases, 2)
	assert.True(t, leaseDone(leases[1]), "request reauthentication lease survived")
	assert.False(t, leaseDone(leases[0]), "session lease did not remain owned")

	handler.mu.Lock()
	session := handler.legacy[sessionID]
	originalRevision := session.binding.CredentialRevision
	session.binding.CredentialRevision = "999"
	handler.mu.Unlock()
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)
	handler.mu.Lock()
	session.binding.CredentialRevision = originalRevision
	handler.mu.Unlock()

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "other", sessionID))
	assert.Equal(t, http.StatusNotFound, response.Code)

	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodDelete, "", "valid", sessionID))
	require.Equal(t, http.StatusNoContent, response.Code, response.Body.String())
	assert.True(t, leaseDone(leases[0]))
	_, _, legacy := handler.Status()
	assert.Zero(t, legacy.InUse)

	expiredSession := initializeLegacy(t, boundary)
	expiredLease := authority.captured()[len(authority.captured())-1]
	clock.Advance(contract.LegacyIdleLifetime + time.Second)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", expiredSession))
	assert.Equal(t, http.StatusNotFound, response.Code)
	assert.True(t, leaseDone(expiredLease))

	invalidatedSession := initializeLegacy(t, boundary)
	handler.mu.Lock()
	invalidatedDone := handler.legacy[invalidatedSession].done
	handler.mu.Unlock()
	principal, err := authority.repository.GetPrincipal(t.Context(), authority.principals["valid"])
	require.NoError(t, err)
	disabled := contract.PrincipalDisabled
	_, err = authority.repository.PatchPrincipal(t.Context(), principal.ID, authorization.PatchPrincipalRequest{
		ExpectedRevision: principal.Revision, State: &disabled,
	})
	require.NoError(t, err)
	select {
	case <-invalidatedDone:
	case <-time.After(time.Second):
		t.Fatal("lease invalidation did not close legacy session")
	}
	_, _, legacy = handler.Status()
	assert.Zero(t, legacy.InUse)

	restarted, restartedBoundary, _ := newLegacyHarness(t, clock, testutil.NewFakeEntropy(makeDistinctEntropy(1)))
	response = httptest.NewRecorder()
	restartedBoundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyPing, "valid", invalidatedSession))
	assert.Equal(t, http.StatusNotFound, response.Code)
	restarted.Shutdown()
	handler.Shutdown()
}

func TestLegacyInvalidationBeforePublicationCreatesNoSession(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	handler, _, authority := newLegacyHarness(t, clock, testutil.NewFakeEntropy(makeDistinctEntropy(1)))
	request := legacyRequest(http.MethodPost, legacyInitialize, "valid", "")
	authenticated, err := handler.Authenticate(request.Context(), request, contract.AuthorityAgent)
	require.NoError(t, err)
	lease, ok := LeaseFromContext(authenticated)
	require.True(t, ok)
	lease.Release()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request.WithContext(authenticated))
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	_, _, legacy := handler.Status()
	assert.Zero(t, legacy.InUse)
	captured := authority.captured()
	require.Len(t, captured, 1)
	assert.True(t, leaseDone(captured[0]))
}

func TestLegacyTimerFiringDuringInstallationCannotPublishOrLeak(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	scheduler := &immediateScheduler{}
	handler := New(Options{
		Authenticator: authority, Now: clock.Now, Entropy: testutil.NewFakeEntropy(makeDistinctEntropy(1)), AfterFunc: scheduler.AfterFunc,
	})
	boundary := newLegacyBoundary(t, handler)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyInitialize, "valid", ""))
	assert.Equal(t, http.StatusUnauthorized, response.Code)
	_, _, legacy := handler.Status()
	assert.Zero(t, legacy.InUse)
	require.Len(t, scheduler.timers, 2)
	assert.True(t, scheduler.timers[0].stopped)
	assert.True(t, scheduler.timers[1].stopped)
	leases := authority.captured()
	require.Len(t, leases, 1)
	assert.True(t, leaseDone(leases[0]))
}

func TestLegacyCredentialMutationsClosePublishedSession(t *testing.T) {
	for _, mutation := range []string{"replacement", "revocation", "disable"} {
		t.Run(mutation, func(t *testing.T) {
			clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
			handler, boundary, authority := newLegacyHarness(t, clock, testutil.NewFakeEntropy(makeDistinctEntropy(1)))
			sessionID := initializeLegacy(t, boundary)
			handler.mu.Lock()
			done := handler.legacy[sessionID].done
			handler.mu.Unlock()
			principal, err := authority.repository.GetPrincipal(t.Context(), authority.principals["valid"])
			require.NoError(t, err)
			switch mutation {
			case "replacement":
				_, err = authority.repository.IssueCredential(t.Context(), principal.ID, principal.Revision)
			case "revocation":
				_, err = authority.repository.RevokeCredential(t.Context(), principal.ID, principal.Revision)
			case "disable":
				disabled := contract.PrincipalDisabled
				_, err = authority.repository.PatchPrincipal(t.Context(), principal.ID, authorization.PatchPrincipalRequest{
					ExpectedRevision: principal.Revision, State: &disabled,
				})
			}
			require.NoError(t, err)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("credential mutation did not close legacy session")
			}
			_, _, legacy := handler.Status()
			assert.Zero(t, legacy.InUse)
		})
	}
}

func TestLegacyIdleAndAbsoluteTimersCloseSessionWithoutCredentialExpiry(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	scheduler := &manualScheduler{}
	handler := New(Options{
		Authenticator: authority, Now: clock.Now, Entropy: testutil.NewFakeEntropy(makeDistinctEntropy(2)), AfterFunc: scheduler.AfterFunc,
	})
	defer handler.Shutdown()
	boundary := newLegacyBoundary(t, handler)
	initializeLegacy(t, boundary)
	scheduler.Fire(0)
	_, _, legacy := handler.Status()
	assert.Zero(t, legacy.InUse)
	assert.True(t, scheduler.Stopped(1), "absolute timer survived idle closure")

	sessionID := initializeLegacy(t, boundary)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodDelete, "", "valid", sessionID))
	require.Equal(t, http.StatusNoContent, response.Code)
	assert.True(t, scheduler.Stopped(2), "idle timer survived DELETE")
	assert.True(t, scheduler.Stopped(3), "absolute timer survived DELETE")
	assert.Len(t, scheduler.timers, 4, "credential expiry installed a third timer")
}

func TestLegacyInitializationReservesNBeforeEntropyAndRejectsNPlusOne(t *testing.T) {
	clock := testutil.NewFakeClock(time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC))
	entropy := testutil.NewFakeEntropy(makeDistinctEntropy(129))
	handler, boundary, authority := newLegacyHarness(t, clock, entropy)

	for range 128 {
		initializeLegacy(t, boundary)
	}
	active := authority.captured()
	require.Len(t, active, 128)
	for _, lease := range active {
		assert.False(t, leaseDone(lease))
	}
	before := entropy.Remaining()
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, legacyRequest(http.MethodPost, legacyInitialize, "valid", ""))
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.Equal(t, before, entropy.Remaining(), "N+1 initialization consumed entropy")
	captured := authority.captured()
	require.Len(t, captured, 129)
	assert.True(t, leaseDone(captured[128]), "rejected initialization lease survived")
	_, _, legacy := handler.Status()
	assert.Equal(t, int64(128), legacy.InUse)
	assert.True(t, legacy.Saturated)

	handler.Shutdown()
	for _, lease := range active {
		assert.True(t, leaseDone(lease))
	}
}

func makeDistinctEntropy(blocks int) []byte {
	result := make([]byte, blocks*32)
	for block := range blocks {
		for index := range 32 {
			result[block*32+index] = byte(block + index)
		}
	}
	return result
}
