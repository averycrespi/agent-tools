package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_MissingWritesDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.hcl")

	cfg, err := config.Load(path)
	require.NoError(t, err)

	assert.Equal(t, "127.0.0.1:8220", cfg.Proxy.Listen)
	assert.Equal(t, "127.0.0.1:8221", cfg.Dashboard.Listen)
	assert.Equal(t, 90, cfg.Audit.RetentionDays)
	assert.Equal(t, 5*time.Minute, cfg.Approval.Timeout)
	assert.Equal(t, 50, cfg.Approval.MaxPending)
	assert.Equal(t, int64(1<<20), cfg.ProxyBehavior.MaxBodyBuffer)

	// File was created with 0600.
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
}

func TestLoad_PartialOverridesKept(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.hcl")
	require.NoError(t, os.WriteFile(path, []byte(`
proxy { listen = "127.0.0.1:9999" }
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9999", cfg.Proxy.Listen)
	assert.Equal(t, "127.0.0.1:8221", cfg.Dashboard.Listen)
	assert.Equal(t, 5*time.Minute, cfg.Approval.Timeout)
}

func TestLoad_ParseError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.hcl")
	require.NoError(t, os.WriteFile(path, []byte(`proxy { listen = }`), 0o600))
	_, err := config.Load(path)
	require.Error(t, err)
}

func TestLoad_PartialDashboardPreservesOpenBrowser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.hcl")
	// Only set listen; omit open_browser entirely.
	require.NoError(t, os.WriteFile(path, []byte(`
dashboard { listen = "127.0.0.1:9221" }
`), 0o600))

	cfg, err := config.Load(path)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9221", cfg.Dashboard.Listen)
	// Default is true; omitting open_browser must NOT flip it to false.
	assert.True(t, cfg.Dashboard.OpenBrowser, "open_browser default must be preserved when omitted")
}

func TestLoad_RejectsWildcardNoInterceptHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.hcl")
	require.NoError(t, os.WriteFile(path, []byte(`
proxy_behavior {
  no_intercept_hosts = ["**"]
  max_body_buffer    = "1MiB"
}
`), 0o600))

	_, err := config.Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no_intercept_hosts")
	assert.Contains(t, err.Error(), "matches every")
}

func TestSave_RejectsWildcardNoInterceptHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.hcl")
	cfg := config.DefaultConfig()
	cfg.ProxyBehavior.NoInterceptHosts = []string{"**"}

	err := config.Save(cfg, path)
	require.Error(t, err)

	// File must not have been written.
	_, statErr := os.Stat(path)
	assert.True(t, os.IsNotExist(statErr),
		"Save must not write a file when validation fails")
}

func TestLoad_InvalidDurationReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.hcl")
	require.NoError(t, os.WriteFile(path, []byte(`
secrets { cache_ttl = "xyz" }
`), 0o600))

	_, err := config.Load(path)
	require.Error(t, err, "malformed duration string must cause Load to return an error")
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"1KiB", 1024, false},
		{"4KiB", 4096, false},
		{"1MiB", 1 << 20, false},
		{"2MiB", 2 << 20, false},
		{"1GiB", 1 << 30, false},
		{"3GiB", 3 << 30, false},
		{"1024", 1024, false},
		{"0", 0, false},
		{"xyz", 0, true},
		{"1TiB", 0, true},
		{"MiB", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := config.ParseSize(tc.input)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}
