package main

import (
	"bytes"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServeAllowedHostValidationBeforeInstallation(t *testing.T) {
	for _, args := range [][]string{
		{"--allowed-host", "http://host"}, {"--allowed-host", "host:8210"}, {"--allowed-host", "host/path"},
		{"--allowed-host", "*.example"}, {"--allowed-host", "a..b"}, {"--allowed-host", "host\n"},
		{"--allowed-host", "valid.internal", "--allowed-host", "bad:1"},
	} {
		cmd := newRootCmd()
		stdout, stderr := new(bytes.Buffer), new(bytes.Buffer)
		cmd.SetOut(stdout)
		cmd.SetErr(stderr)
		cmd.SetArgs(append([]string{"serve", "--data-dir", t.TempDir() + "/absent"}, args...))
		err := cmd.Execute()
		require.Error(t, err)
		assert.Equal(t, 2, commandExitCode(err), "%s", stderr.String())
		assert.Empty(t, stdout.String())
		assert.NotContains(t, stderr.String(), "storage_unavailable")
	}
}

func TestHostnameRefusalGuidanceAndHelp(t *testing.T) {
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"status"})
	require.NoError(t, err)
	require.NoError(t, cmd.Flags().Set("address", "http://host.lima.internal:18210"))
	problem := projectOnlineFailure(cmd, &controlclient.OnlineError{Code: "gateway_not_running", Exit: 9})
	assert.Contains(t, problem.Title, "trusted local forwarding")
	assert.Contains(t, problem.Title, "numeric IPv4 loopback listener")
	assert.NotContains(t, problem.Title, "--listen host")
	assert.Equal(t, 9, problem.Exit)
	_, err = renderOnlineServeCommand("http://host.lima.internal:18210", "", false)
	assert.Error(t, err)
	serve, _, err := root.Find([]string{"serve"})
	require.NoError(t, err)
	require.NotNil(t, serve.Flags().Lookup("allowed-host"))
	assert.Equal(t, "stringArray", serve.Flags().Lookup("allowed-host").Value.Type())
	assert.Contains(t, serve.Flags().Lookup("allowed-host").Usage, "repeatable")
	assert.Contains(t, cmd.Flags().Lookup("address").Usage, "trusted local forwarding hostname")
}
