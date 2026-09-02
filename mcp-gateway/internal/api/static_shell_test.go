package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticShellSecurityHeaders(t *testing.T) {
	handler := newTestHandler(t)
	shell := perform(handler, http.MethodGet, "/", "", nil)
	require.Equal(t, http.StatusOK, shell.Code)
	assert.Equal(t, "no-store", shell.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", shell.Header().Get("Referrer-Policy"))
	csp := shell.Header().Get("Content-Security-Policy")
	assert.Contains(t, csp, "default-src 'self'")
	assert.NotContains(t, csp, "unsafe-")
	assert.NotContains(t, shell.Body.String(), "https://")

	for path, contentType := range map[string]string{
		"/assets/app.css": "text/css; charset=utf-8",
		"/assets/app.js":  "application/javascript; charset=utf-8",
	} {
		asset := perform(handler, http.MethodGet, path, "", nil)
		assert.Equal(t, http.StatusOK, asset.Code)
		assert.Equal(t, contentType, asset.Header().Get("Content-Type"))
		assert.Equal(t, "no-referrer", asset.Header().Get("Referrer-Policy"))
		assert.Equal(t, "nosniff", asset.Header().Get("X-Content-Type-Options"))
		assert.False(t, strings.Contains(asset.Body.String(), "sourceMappingURL"))
	}
}
