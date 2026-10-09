package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyHTTPLaunchContext(t *testing.T) {
	for _, test := range []struct {
		platform, label string
		disabled        bool
	}{
		{"darwin", "dev.agent-tools.agent-gateway", true},
		{"darwin", "", false},
		{"darwin", "0", false},
		{"darwin", "dev.agent-tools.agent-gateway.other", false},
		{"linux", "dev.agent-tools.agent-gateway", false},
	} {
		require.Equal(t, test.disabled, legacyHTTPDisabled(test.platform, test.label))
	}
}

func TestHTTPProxyServeDefaultAndExplicitOptOut(t *testing.T) {
	root := newRootCmd()
	serve, _, err := root.Find([]string{"serve"})
	require.NoError(t, err)
	proxy, err := serve.Flags().GetString("http-proxy-listen")
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:8212", proxy)
	require.NotNil(t, serve.Flags().Lookup("clear-http-proxy-listen"))
}

func TestHTTPProxySelectionPreservesManagedIntent(t *testing.T) {
	for _, test := range []struct {
		name    string
		args    []string
		managed bool
		want    string
	}{
		{name: "foreground default", want: "127.0.0.1:8212"},
		{name: "legacy managed omission", managed: true},
		{name: "foreground disabled", args: []string{"--clear-http-proxy-listen"}},
		{name: "managed disabled", managed: true, args: []string{"--clear-http-proxy-listen"}},
		{name: "explicit custom", args: []string{"--http-proxy-listen", "127.0.0.1:8213"}, want: "127.0.0.1:8213"},
		{name: "managed explicit custom", managed: true, args: []string{"--http-proxy-listen", "127.0.0.1:8213"}, want: "127.0.0.1:8213"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := newRootCmd()
			serve, _, err := root.Find([]string{"serve"})
			require.NoError(t, err)
			require.NoError(t, serve.ParseFlags(test.args))
			got, err := selectedHTTPProxy(serve, test.managed)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
}

func TestHTTPProxyConflictingSelectionsFailBeforeInstallation(t *testing.T) {
	for _, commandArgs := range [][]string{
		{"serve", "--http-proxy-listen", "127.0.0.1:8213", "--clear-http-proxy-listen"},
	} {
		t.Run(commandArgs[0]+"/"+commandArgs[1], func(t *testing.T) {
			command := newRootCmd()
			var output, diagnostics bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&diagnostics)
			command.SetArgs(append(commandArgs, "--data-dir", filepath.Join(t.TempDir(), "absent")))
			err := command.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Equal(t, 2, commandExitCode(err))
			require.Empty(t, output.String())
			require.Contains(t, diagnostics.String(), "Choose --http-proxy-listen or --clear-http-proxy-listen, not both")
		})
	}
}
