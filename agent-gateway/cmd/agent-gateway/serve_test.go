package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/config"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/paths"
)

func TestServe_BindsAndShutsDown(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)

	cfg := config.DefaultConfig()
	cfg.Proxy.Listen = "127.0.0.1:0"
	cfg.Dashboard.Listen = "127.0.0.1:0"
	cfg.Dashboard.OpenBrowser = false
	require.NoError(t, config.Save(cfg, paths.ConfigFile()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	deps := newServeDeps()
	deps.Ready = ready

	done := make(chan error, 1)
	go func() { done <- RunServe(ctx, deps) }()

	// Wait for ready signal or timeout. Allow extra time under the race detector.
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not become ready within 30s")
	}

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(35 * time.Second):
		t.Fatal("serve did not shut down within 35s")
	}
}

func TestServe_ProxyReturns501AndDashboardReturns200(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)

	cfg := config.DefaultConfig()
	cfg.Proxy.Listen = "127.0.0.1:0"
	cfg.Dashboard.Listen = "127.0.0.1:0"
	cfg.Dashboard.OpenBrowser = false
	require.NoError(t, config.Save(cfg, paths.ConfigFile()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	proxyAddr := make(chan string, 1)
	dashAddr := make(chan string, 1)

	deps := newServeDeps()
	deps.Ready = ready
	deps.ProxyAddr = proxyAddr
	deps.DashboardAddr = dashAddr

	done := make(chan error, 1)
	go func() { done <- RunServe(ctx, deps) }()

	// Allow extra time under the race detector.
	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not become ready within 30s")
	}

	pAddr := <-proxyAddr
	dAddr := <-dashAddr

	// Proxy should return 501 Not Implemented.
	resp, err := http.Get(fmt.Sprintf("http://%s/", pAddr))
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	assert.Equal(t, http.StatusNotImplemented, resp.StatusCode)

	// Dashboard should return 200 with "hello".
	resp, err = http.Get(fmt.Sprintf("http://%s/", dAddr))
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "hello", string(body))

	cancel()
	require.NoError(t, <-done)
}

// TestServe_CustomConfigPath verifies that when ConfigPath is set, RunServe
// reads that file and does NOT fall back to the XDG default path.
func TestServe_CustomConfigPath(t *testing.T) {
	// Use separate temp dirs: one for XDG dirs, one for the custom config.
	xdgDir := t.TempDir()
	customDir := t.TempDir()

	t.Setenv("XDG_CONFIG_HOME", xdgDir)
	t.Setenv("XDG_DATA_HOME", xdgDir)

	// Write a config to the custom path only.
	customCfgPath := filepath.Join(customDir, "custom.hcl")
	cfg := config.DefaultConfig()
	cfg.Proxy.Listen = "127.0.0.1:0"
	cfg.Dashboard.Listen = "127.0.0.1:0"
	cfg.Dashboard.OpenBrowser = false
	require.NoError(t, config.Save(cfg, customCfgPath))

	// Confirm that the XDG config file does not exist yet.
	xdgCfgPath := paths.ConfigFile()
	_, err := os.Stat(xdgCfgPath)
	require.True(t, os.IsNotExist(err), "XDG config must not exist before the test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan struct{})
	deps := newServeDeps()
	deps.ConfigPath = customCfgPath
	deps.Ready = ready

	done := make(chan error, 1)
	go func() { done <- RunServe(ctx, deps) }()

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not become ready within 30s")
	}

	cancel()
	require.NoError(t, <-done)

	// The custom config file must exist (was read/written by config.Load).
	_, err = os.Stat(customCfgPath)
	require.NoError(t, err, "custom config file must exist after serve")

	// The XDG default config file must NOT have been created.
	_, err = os.Stat(xdgCfgPath)
	assert.True(t, os.IsNotExist(err), "XDG config must not be created when ConfigPath is set")
}
