package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSuiteJSONOutputPreservesExecutionAndFailure(t *testing.T) {
	for _, test := range []struct {
		name, statement, action string
		exitCode                int
	}{
		{name: "passed", action: "pass"},
		{name: "failed", statement: `t.Fatal("deliberate probe failure")`, action: "fail", exitCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			module := filepath.Join(root, "mcp-gateway")
			directory := filepath.Join(module, "internal", "strictjson")
			require.NoError(t, os.MkdirAll(directory, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.invalid/suitejson\n\ngo 1.25.0\n"), 0o600))
			source := "package strictjson\nimport \"testing\"\nfunc TestJSONProbe(t *testing.T) { t.Log(\"structured probe\"); " + test.statement + " }\n"
			require.NoError(t, os.WriteFile(filepath.Join(directory, "probe_test.go"), []byte(source), 0o600))
			t.Setenv("MCP_GATEWAY_SUITE_JSON_HELPER", root)
			t.Setenv("GOWORK", "off")
			binary, err := os.Executable()
			require.NoError(t, err)
			runner, err := testutil.NewBinaryRunner(30*time.Second, 128*1024)
			require.NoError(t, err)
			result, runErr := runner.Run(t.Context(), binary, "-test.run=^TestSuiteJSONSubprocess$")
			if test.exitCode == 0 {
				require.NoError(t, runErr, string(result.Stderr))
			} else {
				require.Error(t, runErr)
			}
			require.Equal(t, test.exitCode, result.ExitCode, string(result.Stderr))
			assert.True(t, result.Cleanup.Reaped)
			assert.False(t, result.Cleanup.Survived)
			assert.False(t, result.StdoutTruncated)
			assert.False(t, result.StderrTruncated)
			decoder := json.NewDecoder(bytes.NewReader(result.Stdout))
			var actions []string
			for {
				var event struct{ Action, Test string }
				if err := decoder.Decode(&event); err == io.EOF {
					break
				} else {
					require.NoError(t, err)
				}
				if event.Test == "TestJSONProbe" && event.Action != "output" {
					actions = append(actions, event.Action)
				}
			}
			assert.Equal(t, []string{"run", test.action}, actions)
			assert.Contains(t, string(result.Stdout), "structured probe")
		})
	}
}

func TestSuiteJSONSubprocess(t *testing.T) {
	root := os.Getenv("MCP_GATEWAY_SUITE_JSON_HELPER")
	if root == "" {
		t.Skip("subprocess fixture for structured suite output")
	}
	os.Exit(runSuite(root, "run-suite", []string{"test-unit", "--json"}))
}

func TestAcceptanceTemporaryRootsDetectsRootsCreatedDuringRun(t *testing.T) {
	before, err := acceptanceTemporaryRoots()
	require.NoError(t, err)
	created, err := os.MkdirTemp("", "mcp-gateway-ui-development-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(created) })

	after, err := acceptanceTemporaryRoots()
	require.NoError(t, err)
	assert.False(t, containsNoNewTemporaryRoots(before, after))
	require.NoError(t, os.RemoveAll(created))
	restored, err := acceptanceTemporaryRoots()
	require.NoError(t, err)
	assert.True(t, containsNoNewTemporaryRoots(before, restored))
}

func TestAcceptanceCommandRejectsRemovedModes(t *testing.T) {
	for _, argument := range []string{"--profile", "--profile=retired", "--task", "--milestone", "--qualify-external"} {
		assert.Equal(t, 2, run([]string{argument}), argument)
	}
}

func TestAcceptanceCommandRequiresOneClosedInterface(t *testing.T) {
	assert.Equal(t, 2, run(nil))
	assert.Equal(t, 2, run([]string{"unknown"}))
	assert.Equal(t, 2, run([]string{"accept"}))
	assert.Equal(t, 2, run([]string{"adopt-acceptance-report"}))
	assert.Equal(t, 2, run([]string{"qualify-external-evidence", "extra"}))
}
