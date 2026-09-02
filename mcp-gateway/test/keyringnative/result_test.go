package keyringnative

import (
	"context"
	"encoding/json"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
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
	tests := [][]byte{
		[]byte(`{"schema_version":1,"result":"passed","result":"failed","platform":"linux","reason":"duplicate","evidence":[]}`),
		append(valid[:len(valid)-1], []byte(`,"unknown":true}`)...),
		[]byte(`{"schema_version":1,"result":"passed","platform":"linux","reason":"wrong","evidence":[{"name":"deterministic_material_composition","status":"passed","command":"go test -race ./test/material"},{"name":"native_backend","status":"skipped","command":"go test -race -tags=keyringnative ./internal/keyring ./test/material"}]}`),
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
		result, runErr := runner.Run(context.Background(), "env", "MCP_GATEWAY_KEYRING_NATIVE_SELF_TEST=1", "MCP_GATEWAY_KEYRING_NATIVE_FORCE_RESULT="+test.result, script)
		assert.Equal(t, test.exitCode, result.ExitCode)
		if test.exitCode == 0 {
			require.NoError(t, runErr)
		} else {
			require.Error(t, runErr)
		}
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		parsed, parseErr := Parse(result.Stdout)
		require.NoError(t, parseErr, "stderr: %s", result.Stderr)
		assert.Equal(t, test.result, parsed.Result)
		assert.Equal(t, 1, bytesLines(result.Stdout))
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
