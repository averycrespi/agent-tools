package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestServeCleanupPreservesPrimaryAndCloseFailureOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	adapter := diagnostics.New(&stderr, diagnostics.Warn)
	calls := 0
	closeStore := onceServeCleanup("control storage", "/private/control.db", func() error { calls++; return errors.New("device close failed") }, adapter)
	failure := func() (err error) {
		defer func() { err = errors.Join(err, closeStore()) }()
		return errors.New("listener bind failed")
	}()
	require.NoError(t, closeStore())
	require.Equal(t, 1, calls)
	renderer, err := controlclient.NewRenderer(controlclient.OutputHuman, &stdout, adapter.TerminalOutput())
	require.NoError(t, err)
	require.Error(t, finishServe(adapter, controlclient.NewServePhases(renderer), false, "/private", failure))
	<-adapter.Done()
	require.Contains(t, stderr.String(), "listener bind failed")
	require.Contains(t, stderr.String(), "close control storage /private/control.db")
	require.Contains(t, stderr.String(), "device close failed")
	require.Contains(t, stderr.String(), `"component":"control storage"`)
	require.Contains(t, stderr.String(), `"resource":"/private/control.db"`)
	require.Empty(t, stdout.String())
}
