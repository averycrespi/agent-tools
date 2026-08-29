//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6CLIServerCatalogReads(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	alpha := harness.SetupCurrentCatalog("cli-alpha", []fixtureTool{
		{Name: "one", Description: "safe first", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "two", Description: "safe second", InputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	harness.SetupCurrentCatalog("cli-beta", []fixtureTool{{Name: "three", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	results := make([]testutil.ProcessResult, 0, 10)

	serverList := runOnlineCLI(t, harness, bearerPath, true, "server", "list", "--limit", "1", "--output", "json")
	results = append(results, serverList)
	serverAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/servers?limit=1", nil)
	assert.JSONEq(t, string(serverAPI.Body), string(serverList.Stdout))
	var servers contract.Collection[json.RawMessage]
	require.NoError(t, json.Unmarshal(serverList.Stdout, &servers))
	require.Len(t, servers.Items, 1)
	require.NotNil(t, servers.NextCursor)
	serverNext := runOnlineCLI(t, harness, bearerPath, true, "server", "list", "--limit", "1", "--cursor", *servers.NextCursor, "--output", "json")
	results = append(results, serverNext)
	var nextServers contract.Collection[json.RawMessage]
	require.NoError(t, json.Unmarshal(serverNext.Stdout, &nextServers))
	require.Len(t, nextServers.Items, 1, "CLI must request exactly one continuation page")

	serverGet := runOnlineCLI(t, harness, bearerPath, true, "server", "get", alpha.ServerID, "--output", "json")
	results = append(results, serverGet)
	serverGetAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+alpha.ServerID, nil)
	assert.JSONEq(t, string(serverGetAPI.Body), string(serverGet.Stdout))
	serverTable := runOnlineCLI(t, harness, bearerPath, true, "server", "get", alpha.ServerID)
	results = append(results, serverTable)
	assert.Contains(t, string(serverTable.Stdout), "cli-alpha")
	assert.Contains(t, string(serverTable.Stdout), "DURABLE")
	assert.Contains(t, string(serverTable.Stdout), "ACTIVE")

	descriptorList := runOnlineCLI(t, harness, bearerPath, true, "server", "descriptor", "list", alpha.ServerID, "--limit", "1", "--retired", "include", "--output", "json")
	results = append(results, descriptorList)
	descriptorAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+alpha.ServerID+"/descriptors?limit=1&retired=include", nil)
	assert.JSONEq(t, string(descriptorAPI.Body), string(descriptorList.Stdout))
	var descriptors contract.Collection[contract.ToolDescriptor]
	require.NoError(t, json.Unmarshal(descriptorList.Stdout, &descriptors))
	require.Len(t, descriptors.Items, 1)
	require.NotNil(t, descriptors.NextCursor)
	descriptor := descriptors.Items[0]
	descriptorGet := runOnlineCLI(t, harness, bearerPath, true, "server", "descriptor", "get", alpha.ServerID, descriptor.ID, "--output", "json")
	results = append(results, descriptorGet)
	descriptorGetAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/servers/"+alpha.ServerID+"/descriptors/"+descriptor.ID, nil)
	assert.JSONEq(t, string(descriptorGetAPI.Body), string(descriptorGet.Stdout))
	descriptorTable := runOnlineCLI(t, harness, bearerPath, true, "server", "descriptor", "list", alpha.ServerID, "--limit", "1", "--retired", "exclude")
	results = append(results, descriptorTable)
	assert.Contains(t, string(descriptorTable.Stdout), "EVIDENCE")
	assert.NotContains(t, string(descriptorTable.Stdout), "callable")

	catalogList := runOnlineCLI(t, harness, bearerPath, true, "catalog", "list", "--limit", "1", "--output", "json")
	results = append(results, catalogList)
	catalogTable := runOnlineCLI(t, harness, bearerPath, true, "catalog", "list", "--limit", "1")
	results = append(results, catalogTable)
	assert.Contains(t, string(catalogTable.Stdout), "published evidence")
	assert.NotContains(t, string(catalogTable.Stdout), "callable")
	catalogAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/catalog?limit=1", nil)
	assert.JSONEq(t, string(catalogAPI.Body), string(catalogList.Stdout))
	var catalog contract.CatalogPage
	require.NoError(t, json.Unmarshal(catalogList.Stdout, &catalog))
	require.NotNil(t, catalog.NextCursor)
	staleCursor := *catalog.NextCursor
	harness.SetupCurrentCatalog("cli-gamma", []fixtureTool{{Name: "four", InputSchema: json.RawMessage(`{"type":"object"}`)}})
	stale := runOnlineCLI(t, harness, bearerPath, false, "catalog", "list", "--cursor", staleCursor, "--output", "json")
	results = append(results, stale)
	assert.Equal(t, 5, stale.ExitCode)
	assert.Empty(t, stale.Stdout)
	assert.Contains(t, string(stale.Stderr), `"code":"stale_cursor"`)

	invalid := runOnlineCLI(t, harness, bearerPath, false, "server", "get", "invalid", "--output", "json")
	results = append(results, invalid)
	assert.Equal(t, 2, invalid.ExitCode)
	assert.Empty(t, invalid.Stdout)
	invalidRetired := runOnlineCLI(t, harness, bearerPath, false, "server", "descriptor", "list", alpha.ServerID, "--retired", "invalid", "--output", "json")
	results = append(results, invalidRetired)
	assert.Equal(t, 2, invalidRetired.ExitCode)
	assert.Empty(t, invalidRetired.Stdout)
	harness.Stop(syscall.SIGTERM)
	for _, result := range results {
		assert.False(t, result.StdoutTruncated)
		assert.False(t, result.StderrTruncated)
		assert.NotContains(t, string(result.Stdout), harness.bearer)
		assert.NotContains(t, string(result.Stderr), harness.bearer)
		assert.True(t, result.Cleanup.Reaped)
		assert.False(t, result.Cleanup.Survived)
	}
	assert.Len(t, harness.results, 1, "T38 must own one Gateway lifecycle")
}
