package mcpingress

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPWorkAndStreamLimitsRejectWithoutQueuing(t *testing.T) {
	t.Parallel()
	authority := newTestAuthority(t)
	authority.add(t, "valid", contract.VisibilityRequestable)
	leases := make([]*authorization.Lease, 35)
	for index := range leases {
		var err error
		leases[index], err = authority.Authenticate(t.Context(), contract.AgentBearerPrefix+"valid")
		require.NoError(t, err)
	}
	handler := New(Options{Authenticator: &queuedAuthenticator{leases: leases}})
	boundary := newLegacyBoundary(t, handler)
	started := make(chan struct{}, 32)
	releaseWork := make(chan struct{})
	handler.modern = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		started <- struct{}{}
		<-releaseWork
		writer.WriteHeader(http.StatusNoContent)
	})

	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			request := modernRequest(http.MethodPost, modernPing)
			request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
			boundary.ServeHTTP(httptest.NewRecorder(), request)
		}()
	}
	for range 32 {
		<-started
	}
	blockedLeases := leases[:32]
	for _, lease := range blockedLeases {
		assert.False(t, leaseDone(lease))
	}
	work, streams, _ := handler.Status()
	assert.Equal(t, int64(32), work.InUse)
	assert.True(t, work.Saturated)
	assert.Zero(t, streams.InUse)

	request := modernRequest(http.MethodPost, modernPing)
	request.Header.Set("Mcp-Protocol-Version", contract.ModernProtocolVersion)
	response := httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.True(t, leaseDone(leases[32]))

	close(releaseWork)
	group.Wait()
	for _, lease := range blockedLeases {
		assert.True(t, leaseDone(lease))
	}
	sessionID := initializeLegacy(t, boundary)
	for range 32 {
		handler.streams <- struct{}{}
	}
	request = legacyRequest(http.MethodGet, "", "valid", sessionID)
	response = httptest.NewRecorder()
	boundary.ServeHTTP(response, request)
	assert.Equal(t, http.StatusTooManyRequests, response.Code)
	assert.True(t, leaseDone(leases[34]))
	work, streams, _ = handler.Status()
	assert.Zero(t, work.InUse)
	assert.True(t, streams.Saturated)
	for range 32 {
		<-handler.streams
	}
	handler.Shutdown()
	assert.True(t, leaseDone(leases[33]))
}
