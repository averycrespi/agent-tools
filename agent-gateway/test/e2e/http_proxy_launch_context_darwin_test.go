//go:build e2e

package e2e

import (
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/service"
	"github.com/stretchr/testify/require"
)

// This injects the canonical launch context into disposable processes. It does
// not install a LaunchAgent or qualify launchd's native environment delivery.
func TestHTTPProxyLegacyManagedLaunchPreservesOmission(t *testing.T) {
	h := newGatewayHarness(t)
	h.serveArgs = h.serveArgs[:len(h.serveArgs)-1]
	t.Setenv("XPC_SERVICE_NAME", service.Label)
	h.Start()
	status := h.adminSnapshot("GET", "/api/v2/system-status", nil)
	require.Contains(t, string(status.Body), `"enabled":false`)
	h.Restart()
	h.Stop(syscall.SIGTERM)
	// An explicit custom address still requests signing readiness rather than
	// being suppressed by the context hint; this fixture has no persistent key.
	args := append(append([]string(nil), h.serveArgs...), "--http-proxy-listen", unusedAuthority(t))
	result, err := h.runner.Run(h.ctx, h.binary, args...)
	require.Error(t, err)
	require.Empty(t, result.Stdout)
	require.Contains(t, string(result.Stderr), "CA signing material")
}
