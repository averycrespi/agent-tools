//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCLIAudit(t *testing.T) {
	harness := newGatewayHarness(t)
	harness.Start()
	bearerPath := filepath.Join(t.TempDir(), "admin-bearer")
	require.NoError(t, os.WriteFile(bearerPath, []byte(harness.bearer+"\n"), 0o600))
	list := runOnlineCLI(t, harness, bearerPath, true, "audit", "list", "--limit", "1", "--json")
	api := harness.adminSnapshot(http.MethodGet, "/api/v1/audit-events?limit=1", nil)
	assert.JSONEq(t, string(api.Body), string(list.Stdout))
	var page contract.AuditPage
	require.NoError(t, json.Unmarshal(list.Stdout, &page))
	require.Len(t, page.Items, 1)
	require.NotNil(t, page.NextCursor)
	item := runOnlineCLI(t, harness, bearerPath, true, "audit", "get", page.Items[0].ID, "--generation", page.History.Generation, "--json")
	itemAPI := harness.adminSnapshot(http.MethodGet, "/api/v1/audit-events/"+page.Items[0].ID+"?generation="+page.History.Generation, nil)
	assert.JSONEq(t, string(itemAPI.Body), string(item.Stdout))
	older := runOnlineCLI(t, harness, bearerPath, true, "audit", "list", "--limit", "1", "--generation", page.History.Generation, "--cursor", *page.NextCursor, "--json")
	var olderPage contract.AuditPage
	require.NoError(t, json.Unmarshal(older.Stdout, &olderPage))
	require.Len(t, olderPage.Items, 1)
	assert.NotEqual(t, page.Items[0].ID, olderPage.Items[0].ID)
	assert.Equal(t, page.History.Generation, olderPage.History.Generation)
	filtered := runOnlineCLI(t, harness, bearerPath, true, "audit", "list", "--actor-type", string(page.Items[0].Actor.Type), "--correlation-id", page.Items[0].CorrelationID, "--json")
	var matches contract.AuditPage
	require.NoError(t, json.Unmarshal(filtered.Stdout, &matches))
	require.NotEmpty(t, matches.Items)
	for _, event := range matches.Items {
		assert.Equal(t, page.Items[0].Actor.Type, event.Actor.Type)
		assert.Equal(t, page.Items[0].CorrelationID, event.CorrelationID)
	}
	different := strings.Repeat("a", 64)
	if different == page.History.Generation {
		different = strings.Repeat("b", 64)
	}
	mismatch := runOnlineCLI(t, harness, bearerPath, false, "audit", "get", page.Items[0].ID, "--generation", different)
	assert.Equal(t, 5, mismatch.ExitCode)
	assert.Empty(t, mismatch.Stdout)
	assert.Contains(t, string(mismatch.Stderr), "replaced by restore")
	assert.NotContains(t, string(list.Stdout)+string(item.Stdout)+string(older.Stdout)+string(filtered.Stdout), harness.bearer)
	harness.Stop(os.Interrupt)
}
