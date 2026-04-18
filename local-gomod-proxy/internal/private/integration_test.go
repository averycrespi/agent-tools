//go:build integration

// Integration test requires network access to proxy.golang.org.
// Exercises the real os/exec Runner against `go mod download`.

package private

import (
	"net/http"
	"net/http/httptest"
	"testing"

	proxyExec "github.com/averycrespi/agent-tools/local-gomod-proxy/internal/exec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegration_PrivateFetcher_Info(t *testing.T) {
	// Real Go toolchain, real network, real module.
	// Uses rsc.io/quote@v1.5.2 as a stable known-good target.
	f := New(proxyExec.NewOSRunner())
	req := Request{
		Module:   "rsc.io/quote",
		Version:  "v1.5.2",
		Artifact: ArtifactInfo,
	}

	w := httptest.NewRecorder()
	err := f.Serve(w, httptest.NewRequest(http.MethodGet, "/", nil), req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"Version":"v1.5.2"`)
}
