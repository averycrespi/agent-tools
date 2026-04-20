package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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

func TestServe_ProxyAcceptsCONNECTAndDashboardReturns200(t *testing.T) {
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

	// Proxy must respond to an unauthenticated CONNECT with 407 Proxy
	// Authentication Required, because the registry is enabled and no
	// Proxy-Authorization header was supplied.
	conn, err := net.DialTimeout("tcp", pAddr, 5*time.Second)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	_, err = fmt.Fprintf(conn, "CONNECT a.invalid:443 HTTP/1.1\r\nHost: a.invalid:443\r\n\r\n")
	require.NoError(t, err)

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusProxyAuthRequired, resp.StatusCode)

	// Dashboard /dashboard/unauthorized is accessible without auth.
	dresp, err := http.Get(fmt.Sprintf("http://%s/dashboard/unauthorized", dAddr))
	require.NoError(t, err)
	_, err = io.ReadAll(dresp.Body)
	require.NoError(t, dresp.Body.Close())
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, dresp.StatusCode)

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

// startServe is a shared helper that starts RunServe with a temp XDG
// environment, waits for the ready signal, and returns the dashboard address
// plus a cleanup function. OpenBrowser is stubbed to a no-op.
func startServe(t *testing.T) (dashAddr string, cancel context.CancelFunc, done <-chan error) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)

	cfg := config.DefaultConfig()
	cfg.Proxy.Listen = "127.0.0.1:0"
	cfg.Dashboard.Listen = "127.0.0.1:0"
	cfg.Dashboard.OpenBrowser = false
	require.NoError(t, config.Save(cfg, paths.ConfigFile()))

	ctx, ctxCancel := context.WithCancel(context.Background())

	ready := make(chan struct{})
	dashCh := make(chan string, 1)

	deps := newServeDeps()
	deps.Ready = ready
	deps.DashboardAddr = dashCh
	deps.Headless = true

	doneCh := make(chan error, 1)
	go func() { doneCh <- RunServe(ctx, deps) }()

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		ctxCancel()
		t.Fatal("serve did not become ready within 30s")
	}

	addr := <-dashCh
	return addr, ctxCancel, doneCh
}

// adminToken reads the admin token from the XDG config dir (must be called
// after RunServe has started and created the file).
func adminToken(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(paths.AdminTokenFile())
	require.NoError(t, err)
	return strings.TrimSpace(string(data))
}

// TestServe_DashboardServesIndex verifies that the real dashboard serves the
// SPA index page at /dashboard/ when an authenticated request is made.
func TestServe_DashboardServesIndex(t *testing.T) {
	dAddr, cancel, done := startServe(t)
	defer func() {
		cancel()
		require.NoError(t, <-done)
	}()

	token := adminToken(t)

	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/dashboard/", dAddr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/html")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Contains(t, string(body), "<title>Agent Gateway</title>")
}

// TestServe_DashboardRequiresAuth verifies that unauthenticated requests to
// /dashboard/api/* are redirected to /dashboard/unauthorized.
func TestServe_DashboardRequiresAuth(t *testing.T) {
	dAddr, cancel, done := startServe(t)
	defer func() {
		cancel()
		require.NoError(t, <-done)
	}()

	// No auth header — expect redirect to /dashboard/unauthorized.
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get(fmt.Sprintf("http://%s/dashboard/api/pending", dAddr))
	require.NoError(t, err)
	defer resp.Body.Close() //nolint:errcheck

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/dashboard/unauthorized", resp.Header.Get("Location"))
}

// TestServe_OpenBrowserSkippedWhenHeadless verifies that the openBrowser
// function is never invoked when Headless=true, even if the token file did
// not previously exist (first-run scenario).
func TestServe_OpenBrowserSkippedWhenHeadless(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)

	cfg := config.DefaultConfig()
	cfg.Proxy.Listen = "127.0.0.1:0"
	cfg.Dashboard.Listen = "127.0.0.1:0"
	cfg.Dashboard.OpenBrowser = true // would open browser if not headless
	require.NoError(t, config.Save(cfg, paths.ConfigFile()))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var browserCalled atomic.Bool
	ready := make(chan struct{})

	deps := newServeDeps()
	deps.Ready = ready
	deps.Headless = true // must suppress browser
	deps.OpenBrowserFn = func(_ string) error {
		browserCalled.Store(true)
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- RunServe(ctx, deps) }()

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not become ready within 30s")
	}

	cancel()
	require.NoError(t, <-done)

	assert.False(t, browserCalled.Load(), "openBrowser must not be called when --headless is set")
}

// TestServe_OpenBrowserCalledOnSubsequentRun verifies that the browser is
// opened on every serve start, not only on the first run. Regression guard
// for the previous behaviour where OpenBrowser was gated on firstRun.
func TestServe_OpenBrowserCalledOnSubsequentRun(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_DATA_HOME", dir)

	cfg := config.DefaultConfig()
	cfg.Proxy.Listen = "127.0.0.1:0"
	cfg.Dashboard.Listen = "127.0.0.1:0"
	cfg.Dashboard.OpenBrowser = true
	require.NoError(t, config.Save(cfg, paths.ConfigFile()))

	// Run once to create the admin-token file (first run).
	runOnce := func() int32 {
		var calls atomic.Int32
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		ready := make(chan struct{})
		deps := newServeDeps()
		deps.Ready = ready
		deps.OpenBrowserFn = func(_ string) error {
			calls.Add(1)
			return nil
		}

		done := make(chan error, 1)
		go func() { done <- RunServe(ctx, deps) }()

		select {
		case <-ready:
		case <-time.After(30 * time.Second):
			t.Fatal("serve did not become ready within 30s")
		}

		cancel()
		require.NoError(t, <-done)
		return calls.Load()
	}

	assert.Equal(t, int32(1), runOnce(), "browser should open on first run")
	assert.Equal(t, int32(1), runOnce(), "browser should open on subsequent run")
}

// TestAdminTokenRotate_InvalidatesCookie verifies that after "admin-token
// rotate" is executed, the running server rejects the old token (simulated via
// the SIGHUP reload path) and accepts the new one.
func TestAdminTokenRotate_InvalidatesCookie(t *testing.T) {
	dAddr, cancel, done := startServe(t)
	defer func() {
		cancel()
		require.NoError(t, <-done)
	}()

	// Read the initial token and verify it works.
	oldToken := adminToken(t)
	req, err := http.NewRequest("GET", fmt.Sprintf("http://%s/dashboard/api/pending", dAddr), nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+oldToken)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "old token should work before rotation")

	// Rotate: generate a new token file, then signal the process via SIGHUP.
	var signalled atomic.Bool
	err = execAdminTokenRotate(
		paths.AdminTokenFile(),
		paths.PIDFile(),
		func(pid int) (bool, error) { return true, nil }, // pretend it's agent-gateway
		func(pid int, sig os.Signal) error {
			// Instead of actually sending SIGHUP (we're in-process), directly
			// invoke the signal handler logic by sending to the real process — but
			// since we're testing in-process we use os.Getpid() which IS the
			// current process. We record the call and let the OS deliver it.
			signalled.Store(true)
			p, findErr := os.FindProcess(pid)
			if findErr != nil {
				return findErr
			}
			return p.Signal(sig)
		},
		io.Discard,
		confirmYes,
	)
	require.NoError(t, err)
	assert.True(t, signalled.Load())

	// Give the signal handler time to reload.
	time.Sleep(100 * time.Millisecond)

	// Old token must now be rejected.
	req2, err := http.NewRequest("GET", fmt.Sprintf("http://%s/dashboard/api/pending", dAddr), nil)
	require.NoError(t, err)
	req2.Header.Set("Authorization", "Bearer "+oldToken)
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp2, err := client.Do(req2)
	require.NoError(t, err)
	_ = resp2.Body.Close()
	assert.Equal(t, http.StatusFound, resp2.StatusCode, "old token must be rejected after rotation")
	assert.Equal(t, "/dashboard/unauthorized", resp2.Header.Get("Location"))

	// New token must work.
	newToken := adminToken(t)
	require.NotEqual(t, oldToken, newToken)
	req3, err := http.NewRequest("GET", fmt.Sprintf("http://%s/dashboard/api/pending", dAddr), nil)
	require.NoError(t, err)
	req3.Header.Set("Authorization", "Bearer "+newToken)
	resp3, err := http.DefaultClient.Do(req3)
	require.NoError(t, err)
	_ = resp3.Body.Close()
	assert.Equal(t, http.StatusOK, resp3.StatusCode, "new token must be accepted after rotation")
}

// TestServe_StartupSummaryLogged verifies that RunServe emits a structured
// "startup summary" log line containing the agent count, secret count, rule
// count, and mitm_hosts field.
func TestServe_StartupSummaryLogged(t *testing.T) {
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

	// Capture log output in a buffer.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ready := make(chan struct{})
	deps := newServeDeps()
	deps.Logger = logger
	deps.Ready = ready
	deps.Headless = true

	done := make(chan error, 1)
	go func() { done <- RunServe(ctx, deps) }()

	select {
	case <-ready:
	case <-time.After(30 * time.Second):
		t.Fatal("serve did not become ready within 30s")
	}

	cancel()
	require.NoError(t, <-done)

	logs := buf.String()
	assert.Contains(t, logs, "startup summary", "startup summary log line must be present")
	assert.Contains(t, logs, "agents=", "startup summary must include agents count")
	assert.Contains(t, logs, "secrets=", "startup summary must include secrets count")
	assert.Contains(t, logs, "rules=", "startup summary must include rules count")
	assert.Contains(t, logs, "mitm_hosts=", "startup summary must include mitm_hosts field")
}
