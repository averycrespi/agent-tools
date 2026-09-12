package keyringnative

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResultParserAcceptsDistinctClassifications(t *testing.T) {
	tests := []struct {
		result        string
		deterministic string
		native        string
	}{
		{result: ResultPassed, deterministic: ResultPassed, native: ResultPassed},
		{result: ResultSkipped, deterministic: ResultPassed, native: ResultSkipped},
		{result: ResultFailed, deterministic: ResultPassed, native: ResultFailed},
		{result: ResultFailed, deterministic: ResultFailed, native: ResultSkipped},
	}
	for _, test := range tests {
		contents, err := json.Marshal(NewResult(test.result, runtime.GOOS, "forced_result", test.deterministic, test.native))
		require.NoError(t, err)
		parsed, err := Parse(contents)
		require.NoError(t, err)
		assert.Equal(t, test.result, parsed.Result)
	}
}

func TestResultParserRejectsMalformedAndInconsistentEvidence(t *testing.T) {
	valid, err := json.Marshal(NewResult(ResultPassed, runtime.GOOS, "valid", ResultPassed, ResultPassed))
	require.NoError(t, err)
	inconsistent, err := json.Marshal(NewResult(ResultPassed, runtime.GOOS, "wrong", ResultPassed, ResultSkipped))
	require.NoError(t, err)
	tests := [][]byte{
		inconsistent,
		[]byte(`{"schema_version":1,"result":"passed","result":"failed","platform":"linux","reason":"duplicate","evidence":[]}`),
		append(valid[:len(valid)-1], []byte(`,"unknown":true}`)...),
		[]byte(`{"schema_version":1,"result":"passed","platform":"linux","reason":"historical","evidence":[{"name":"deterministic_material_composition","status":"passed","command":"go test -race ./test/material"},{"name":"native_backend","status":"passed","command":"go test -race -tags=keyringnative ./internal/keyring ./test/material"}]}`),
		[]byte(`not-json`),
	}
	for _, contents := range tests {
		_, parseErr := Parse(contents)
		assert.Error(t, parseErr)
	}
}

func TestNativeHarnessForcesPassSkipAndFailureAsStructuredJSON(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok)
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	realGo, err := exec.LookPath("go")
	require.NoError(t, err)
	shimRoot := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(shimRoot, "go"), []byte("#!/bin/sh\nfor argument in \"$@\"; do\n  case \"$argument\" in test|run-suite) echo 'self-test reselected a real suite' >&2; exit 99;; esac\ndone\nexec \"$AGENT_GATEWAY_REAL_GO\" \"$@\"\n"), 0o700))
	script := filepath.Join(moduleRoot, "test", "keyring-native.sh")
	runner, err := testutil.NewBinaryRunner(30*time.Second, 16*1024)
	require.NoError(t, err)
	for _, test := range []struct {
		result   string
		exitCode int
	}{
		{result: ResultPassed, exitCode: 0},
		{result: ResultSkipped, exitCode: 0},
		{result: ResultFailed, exitCode: 1},
	} {
		result, runErr := runner.Run(context.Background(), "env", "PATH="+shimRoot+string(os.PathListSeparator)+os.Getenv("PATH"), "AGENT_GATEWAY_REAL_GO="+realGo, "AGENT_GATEWAY_KEYRING_NATIVE_SELF_TEST=1", "AGENT_GATEWAY_KEYRING_NATIVE_FORCE_RESULT="+test.result, script)
		assert.Equal(t, test.exitCode, result.ExitCode)
		if test.exitCode == 0 {
			require.NoError(t, runErr)
		} else {
			require.Error(t, runErr)
		}
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
		parsed, parseErr := Parse(result.Stdout)
		require.NoError(t, parseErr, "stderr: %s", result.Stderr)
		assert.Equal(t, test.result, parsed.Result)
		assert.Equal(t, 1, bytesLines(result.Stdout))
	}
}

func TestNativeHarnessRejectsRetiredSettingsBeforeSetup(t *testing.T) {
	script, err := filepath.Abs("../keyring-native.sh")
	require.NoError(t, err)
	runner, err := testutil.NewBinaryRunner(5*time.Second, 16*1024)
	require.NoError(t, err)
	for _, name := range []string{"KEYRING_NATIVE", "KEYRING_NATIVE_SELF_TEST", "DISPOSABLE_MACOS_KEYCHAIN", "KEYRING_NATIVE_FORCE_RESULT"} {
		for _, value := range []string{"", "private-canary"} {
			result, runErr := runner.Run(t.Context(), "env", "MCP_GATEWAY_"+name+"="+value, script)
			require.Error(t, runErr)
			require.Equal(t, 2, result.ExitCode)
			require.Empty(t, result.Stdout)
			require.Equal(t, "MCP_GATEWAY_"+name+" is retired; use AGENT_GATEWAY_"+name+"\n", string(result.Stderr))
		}
	}
}

func bytesLines(contents []byte) int {
	count := 0
	for _, value := range contents {
		if value == '\n' {
			count++
		}
	}
	return count
}
