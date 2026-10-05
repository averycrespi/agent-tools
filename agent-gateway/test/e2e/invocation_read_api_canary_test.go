//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"syscall"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvocationReadOnlyAPICanary(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()

	// This history-read canary needs the optional store; serving readiness does not.
	require.Eventually(t, func() bool {
		response := harness.adminSnapshot(http.MethodGet, "/api/v2/system-status", nil)
		var status contract.SystemStatus
		return response.StatusCode == http.StatusOK && json.Unmarshal(response.Body, &status) == nil && status.Traffic != nil && status.Traffic.Ready
	}, 5*time.Second, 10*time.Millisecond)

	response := harness.adminSnapshot(http.MethodGet, "/api/v2/mcp/invocations?limit=1", nil)
	var page contract.InvocationPage
	decodeSnapshot(t, response, http.StatusOK, &page)
	assert.Empty(t, page.Items)
	assert.Nil(t, page.NextCursor)
	assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
	assert.Equal(t, contract.MediaTypeJSON, response.Header.Get("Content-Type"))
	assert.Empty(t, response.Header.Get("Access-Control-Allow-Origin"))

	method := harness.adminSnapshot(http.MethodPost, "/api/v2/mcp/invocations", nil)
	require.Equal(t, http.StatusMethodNotAllowed, method.StatusCode, string(method.Body))
	assert.Equal(t, http.MethodGet, method.Header.Get("Allow"))
	missing := harness.adminSnapshot(http.MethodGet, "/api/v2/mcp/invocations/01ARZ3NDEKTSV4RRFFQ69G5FAV/replay", nil)
	assertProblem(t, missing, http.StatusNotFound, "not_found", "The resource was not found.", false)

	harness.Stop(syscall.SIGTERM)
}
