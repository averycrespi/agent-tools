//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func runCLIGrantInputMatrix(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	dir := t.TempDir()
	principalResult := runOnlineCLI(t, harness, bearerPath, true, "principal", "create", "--display-name", "Grant principal", "--visibility", "allowed-only", "--output", "json")
	var principalCreation contract.PrincipalCreation
	require.NoError(t, json.Unmarshal(principalResult.Stdout, &principalCreation))
	principalID := principalCreation.Principal.ID

	grantPath := filepath.Join(dir, "grant.json")
	invalidPath := filepath.Join(dir, "invalid.json")
	grantBody := `{"name":"Restricted access","principal_id":"` + principalID + `","effect":"deny","server_id":"` + contract.SyntheticServerID + `","upstream_name":"get_identity","constraint":{"equals":{"/count":1.0,"/nested~1name":true}},"expires_at":null}`
	invalidBody := `{"name":"Invalid access","principal_id":"` + principalID + `","effect":"allow","server_id":"` + contract.SyntheticServerID + `","upstream_name":null,"constraint":{"equals":{"/bad":true}},"expires_at":null}`
	require.NoError(t, os.WriteFile(grantPath, []byte(grantBody), 0o600))
	require.NoError(t, os.WriteFile(invalidPath, []byte(invalidBody), 0o600))
	results := []testutil.ProcessResult{principalResult}

	invalid := runOnlineCLI(t, harness, bearerPath, false, "grant", "create", "--file", invalidPath, "--output", "json")
	results = append(results, invalid)
	assert.Equal(t, 2, invalid.ExitCode)
	direct := runOnlineCLI(t, harness, bearerPath, true, "grant", "create", "--name", "Direct access", "--principal-id", principalID, "--effect", "allow", "--server-id", contract.SyntheticServerID, "--upstream-name", "get_identity", "--output", "json")
	results = append(results, direct)
	var directGrant contract.Grant
	require.NoError(t, json.Unmarshal(direct.Stdout, &directGrant))
	assert.Nil(t, directGrant.Constraint)
	created := runOnlineCLI(t, harness, bearerPath, true, "grant", "create", "--file", grantPath, "--output", "json")
	results = append(results, created)
	var grant contract.Grant
	require.NoError(t, json.Unmarshal(created.Stdout, &grant))
	assert.Equal(t, contract.GrantDeny, grant.Effect)
	require.NotNil(t, grant.Constraint)
	assert.Contains(t, string(*grant.Constraint), "1.0")

	listed := runOnlineCLI(t, harness, bearerPath, true, "grant", "list", "--principal-id", principalID, "--server-id", contract.SyntheticServerID, "--limit", "10", "--output", "json")
	got := runOnlineCLI(t, harness, bearerPath, true, "grant", "get", grant.ID)
	results = append(results, listed, got)
	assert.Contains(t, string(listed.Stdout), grant.ID)
	assert.Contains(t, string(got.Stdout), "scalar equals")

	refused := runOnlineCLI(t, harness, bearerPath, false, "grant", "delete", grant.ID, "--output", "json")
	results = append(results, refused)
	assert.Equal(t, 2, refused.ExitCode)
	deleted := runOnlineCLI(t, harness, bearerPath, true, "grant", "delete", grant.ID, "--yes", "--output", "json")
	results = append(results, deleted)
	assert.JSONEq(t, `{}`, string(deleted.Stdout))
	missing := runOnlineCLI(t, harness, bearerPath, false, "grant", "get", grant.ID, "--output", "json")
	results = append(results, missing)
	assert.Equal(t, 4, missing.ExitCode)

	var attempts atomic.Int64
	fake := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts.Add(1)
		writer.Header().Set("Content-Type", contract.MediaTypeProblemJSON)
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"status":503,"code":"storage_unavailable","title":"Storage is unavailable."}`))
	}))
	defer fake.Close()
	uncertain := runCLIAt(t, harness, bearerPath, fake.URL, "grant", "create", "--file", grantPath, "--output", "json")
	results = append(results, uncertain)
	assert.Equal(t, 8, uncertain.ExitCode)
	assert.Equal(t, int64(1), attempts.Load())
	assert.Contains(t, string(uncertain.Stderr), "identical immutable rows")

	harness.Stop(syscall.SIGTERM)
	preHandoff := runOnlineCLI(t, harness, bearerPath, false, "grant", "list", "--output", "json")
	results = append(results, preHandoff)
	assert.Equal(t, 9, preHandoff.ExitCode)
	for _, result := range results {
		if result.ExitCode != 0 {
			assert.Empty(t, result.Stdout)
		}
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T48 must own one Gateway lifecycle")
}
