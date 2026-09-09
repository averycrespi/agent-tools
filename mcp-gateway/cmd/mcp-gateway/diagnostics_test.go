package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/controlclient"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/diagnostics"
	"github.com/stretchr/testify/require"
)

func TestServeDiagnosticLevelValidationBeforeStartup(t *testing.T) {
	for _, mode := range []string{"human", "json"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "not-created")
			var stdout, stderr bytes.Buffer
			command := newRootCmd()
			command.SetOut(&stdout)
			command.SetErr(&stderr)
			command.SetArgs([]string{"serve", "--data-dir", root, "--log-level", "debug\nsecret", "--output", mode})
			err := command.ExecuteContext(t.Context())
			require.Error(t, err)
			require.Empty(t, stdout.String())
			require.NotContains(t, stderr.String(), "secret")
			_, err = os.Stat(root)
			require.True(t, os.IsNotExist(err))
			if mode == "json" {
				var problem controlclient.Problem
				require.NoError(t, json.Unmarshal(stderr.Bytes(), &problem))
				require.NotEmpty(t, problem.Code)
			}
		})
	}
}
func TestServePostStartDiagnosticPipeFailureIsBounded(t *testing.T) {
	for _, mode := range []controlclient.OutputMode{controlclient.OutputHuman, controlclient.OutputJSON} {
		t.Run(string(mode), func(t *testing.T) {
			reader, writer, err := os.Pipe()
			require.NoError(t, err)
			diagnostic := diagnostics.New(writer, diagnostics.Debug)
			defer func() { require.NoError(t, reader.Close()); <-diagnostic.Done(); require.NoError(t, writer.Close()) }()
			var stdout bytes.Buffer
			renderer, err := controlclient.NewRenderer(mode, &stdout, diagnostic.TerminalOutput())
			require.NoError(t, err)
			phases := controlclient.NewServePhases(renderer)
			require.NoError(t, phases.Acknowledge([]byte(`{"ok":true}`), "started"))
			acknowledged := stdout.String()
			for range 100000 {
				diagnostic.Observe(diagnostics.Facts{Event: diagnostics.ExecutionStart, Call: 1, InvocationID: "01ARZ3NDEKTSV4RRFFQ69G5FA0"})
			}
			started := time.Now()
			err = finishServe(diagnostic, phases, true, "/private-secret", context.DeadlineExceeded)
			require.Equal(t, 7, commandExitCode(err))
			require.Less(t, time.Since(started), diagnostics.FlushDeadline+500*time.Millisecond)
			require.Equal(t, acknowledged, stdout.String())
		})
	}
}
func TestServeTerminalDiagnosticsMaintainProblemRepresentation(t *testing.T) {
	for _, mode := range []controlclient.OutputMode{controlclient.OutputHuman, controlclient.OutputJSON} {
		t.Run(string(mode), func(t *testing.T) {
			var stdout, stderr, expected bytes.Buffer
			diagnostic := diagnostics.New(&stderr, diagnostics.Debug)
			renderer, err := controlclient.NewRenderer(mode, &stdout, diagnostic.TerminalOutput())
			require.NoError(t, err)
			phases := controlclient.NewServePhases(renderer)
			err = finishServe(diagnostic, phases, true, "/private", context.DeadlineExceeded)
			require.Equal(t, 7, commandExitCode(err))
			<-diagnostic.Done()
			normal, err := controlclient.NewRenderer(mode, io.Discard, &expected)
			require.NoError(t, err)
			require.NoError(t, normal.WriteProblem(serveCommandProblem(context.DeadlineExceeded, true, "/private")))
			newline := bytes.IndexByte(stderr.Bytes(), '\n')
			require.Positive(t, newline)
			var record map[string]any
			require.NoError(t, json.Unmarshal(stderr.Bytes()[:newline], &record))
			require.Equal(t, "lifecycle_failure", record["event"])
			require.Equal(t, expected.String(), string(stderr.Bytes()[newline+1:]))
		})
	}
}
