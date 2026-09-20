//go:build e2e && browser

package e2e

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Browser scenarios have up to 75 seconds after Gateway startup. Keep their
// server alive for that work plus startup/cleanup without extending the browser.
const gatewayHarnessProcessDeadline = 90 * time.Second

func TestBrowserGatewayLifetime(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()

	// Exercise the real supervisor boundary, not merely the configured value.
	timer := time.NewTimer(gatewayProcessDeadline + time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-t.Context().Done():
		t.Fatal("cancelled before the ordinary Gateway lifetime elapsed")
	}
	response := harness.AdminJSON(http.MethodGet, "/api/v2/system-status", "", nil, nil)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusOK, response.StatusCode)
	harness.Stop(os.Interrupt)
}
