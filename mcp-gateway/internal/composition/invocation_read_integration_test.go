//go:build integration

package composition

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInvocationReadCompositionIntegration(t *testing.T) {
	options, cleanup := newCompositionOptions(t)
	defer cleanup()
	built, err := New(options)
	require.NoError(t, err)
	defer built.shutdownConstructed()

	control, ok := built.ControlAPI()
	require.True(t, ok)
	assert.Same(t, built.invocationRepository, control.Invocations)
	page, err := control.Invocations.List(context.Background(), contract.InvocationListQuery{Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, page.Items)

	collection, ok := contract.RouteForPath("/api/v1/invocations")
	require.True(t, ok)
	assert.Equal(t, []string{"GET"}, collection.Methods)
	assert.Equal(t, contract.AuthorityAdmin, collection.Authority)
	item, ok := contract.RouteForPath("/api/v1/invocations/01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.True(t, ok)
	assert.Equal(t, []string{"GET"}, item.Methods)

	root := gatewayModuleRoot(t)
	for _, source := range productionSources(t, root) {
		assert.Empty(t, s4SQLViolations(source), source.path)
	}
	reads := readProductionSource(t, root, "internal/invocation/reads.go")
	for _, forbidden := range []string{" JOIN ", "INSERT ", "UPDATE ", "DELETE ", "replay"} {
		assert.NotContains(t, strings.ToUpper(reads), strings.ToUpper(forbidden), forbidden)
	}
	apiSource := readProductionSource(t, root, "internal/api/invocations.go")
	for _, forbidden := range []string{"handler.emit(", "contract.Invalidation", "replay", "http.MethodPost", "http.MethodPatch", "http.MethodDelete"} {
		assert.NotContains(t, apiSource, forbidden, forbidden)
	}
	compositionSource := readProductionSource(t, root, "internal/composition/composition.go")
	assert.Equal(t, 1, strings.Count(compositionSource, "invocation.NewRepository("))
	rootSource := readProductionSource(t, root, "cmd/mcp-gateway/root.go")
	assert.Contains(t, rootSource, "Invocations:   controlAPI.Invocations")
	assert.NotContains(t, rootSource, "/internal/invocation")

	migrations, err := filepath.Glob(filepath.Join(root, "internal/storage/migrations/*.sql"))
	require.NoError(t, err)
	assert.Len(t, migrations, storage.CurrentSchema)
	assert.FileExists(t, filepath.Join(root, "internal/storage/migrations/009_invocations.sql"))
	assert.NoFileExists(t, filepath.Join(root, "internal/storage/migrations/011_invocation_reads.sql"))
}
