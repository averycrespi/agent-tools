package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/service"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestServiceResultOutput(t *testing.T) {
	result := service.Result{Installed: true, Settings: &service.Settings{Binary: "/bin/gateway", DataDir: "/data", Listen: "127.0.0.1:8210", AllowedHosts: []string{"host.lima.internal", "host.docker.internal"}}, Plist: "/private/job.plist", Stdout: "/logs/stdout", Stderr: "/logs/stderr", Launchd: "unloaded", Readiness: "unavailable", Message: "Readiness is not credential health.", Warnings: []string{"Legacy encoding; no repair performed."}}
	for _, mode := range []controlclient.OutputMode{controlclient.OutputHuman, controlclient.OutputJSON} {
		for _, verb := range []string{"status", "install", "start", "stop", "restart", "update", "uninstall"} {
			t.Run(string(mode)+"/"+verb, func(t *testing.T) {
				var out, stderr bytes.Buffer
				command := &cobra.Command{}
				command.SetOut(&out)
				command.SetErr(&stderr)
				require.NoError(t, writeServiceResult(command, mode, verb, result))
				require.Empty(t, stderr.String())
				if mode == controlclient.OutputJSON {
					var got service.Result
					require.NoError(t, json.Unmarshal(out.Bytes(), &got))
					require.Equal(t, result, got)
				} else {
					require.Contains(t, out.String(), "LaunchAgent:   unloaded\nReadiness:     unavailable")
					require.Contains(t, out.String(), "Warning: Legacy encoding; no repair performed.")
					if verb == "status" {
						require.Contains(t, out.String(), "Settings\n")
						require.Contains(t, out.String(), "host.lima.internal, host.docker.internal")
						require.Contains(t, out.String(), "Paths\nBinary:        /bin/gateway")
					} else {
						require.Contains(t, out.String(), "Operation:     "+verb)
						require.NotContains(t, out.String(), result.Plist)
						require.NotContains(t, out.String(), "Settings\n")
					}
				}
			})
		}
	}
}

func TestServiceResultLaunchAcceptanceAndSafeText(t *testing.T) {
	var out bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&out)
	result := service.Result{Launchd: "launch-accepted", Readiness: "not-probed", Message: "Launch accepted; readiness not established.", Plist: "/tmp/\x1b[31m", Warnings: []string{"bad\x1b[31m"}}
	require.NoError(t, writeServiceResult(command, controlclient.OutputHuman, "status", result))
	require.Contains(t, out.String(), "Readiness:     not-probed")
	require.Contains(t, out.String(), "service status` to check readiness")
	require.NotContains(t, out.String(), "\x1b")
}

func TestServiceInvalidOutputSelectionBeforeExecution(t *testing.T) {
	for _, flags := range [][]string{{"--output", "invalid"}, {"--output", "human", "--json"}} {
		command := newRootCmd()
		var out, stderr bytes.Buffer
		command.SetOut(&out)
		command.SetErr(&stderr)
		command.SetArgs(append([]string{"service", "restart"}, flags...))
		err := command.ExecuteContext(t.Context())
		require.Error(t, err)
		require.Empty(t, out.String())
		require.Contains(t, stderr.String(), "Choose either --output human or --output json")
	}
}

func TestServiceOutputFlagsAndUsageErrors(t *testing.T) {
	for _, verb := range []string{"install", "start", "stop", "restart", "update", "status", "uninstall"} {
		for _, flags := range [][]string{nil, {"--json"}, {"--output", "json"}, {"--output", "human"}, {"--json", "--output", "human"}, {"--output", "invalid"}} {
			command := newRootCmd()
			var out, stderr bytes.Buffer
			command.SetOut(&out)
			command.SetErr(&stderr)
			// Invalid positional input guarantees no native service access on any OS.
			command.SetArgs(append([]string{"service", verb, "unexpected"}, flags...))
			err := command.ExecuteContext(t.Context())
			var problem *controlclient.Problem
			require.True(t, errors.As(err, &problem))
			require.Empty(t, out.String())
			if len(flags) > 0 && (len(flags) == 1 || flags[1] == "json") {
				require.True(t, json.Valid(stderr.Bytes()), stderr.String())
			} else {
				require.NotContains(t, stderr.String(), `"code"`)
			}
		}
	}
}
