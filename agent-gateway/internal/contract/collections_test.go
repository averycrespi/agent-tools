package contract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestV2CollectionContractsCoverEveryCursorResource(t *testing.T) {
	collections := CollectionContracts()
	require.Len(t, collections, 12)
	byPath := make(map[string]CollectionContract)
	for _, collection := range collections {
		require.NotContains(t, byPath, collection.Pattern)
		byPath[collection.Pattern] = collection
		require.Equal(t, 50, collection.DefaultLimit)
		require.GreaterOrEqual(t, collection.MaximumLimit, collection.DefaultLimit)
		require.NotEmpty(t, collection.DefaultOrder)
		require.Contains(t, collection.QueryMembers, "cursor")
		require.Contains(t, collection.QueryMembers, "limit")
		require.NotContains(t, collection.QueryMembers, "representation")
	}
	covered := make(map[string]bool)
	counts := make(map[string]bool)
	for _, mechanic := range ResourceMechanics() {
		if mechanic.Method != "GET" || !mechanic.Cursor {
			continue
		}
		collection, ok := byPath[mechanic.Pattern]
		require.True(t, ok, mechanic.Pattern)
		require.Contains(t, strings.Split(mechanic.SuccessSchema, "|"), collection.SuccessSchema)
		covered[mechanic.Pattern] = true
		if strings.HasPrefix(collection.SuccessSchema, "QueryPage<") {
			counts[mechanic.Pattern] = true
		}
	}
	require.Len(t, covered, len(collections))
	require.Equal(t, map[string]bool{"/api/v2/principals": true, "/api/v2/mcp/grants": true, "/api/v2/mcp/grant-requests": true, "/api/v2/mcp/servers/{id}/operations": true}, counts)
	require.Equal(t, []string{"active"}, byPath["/api/v2/mcp/servers/{id}/operations"].Projections)
	require.Equal(t, []string{"full", "summary"}, byPath["/api/v2/mcp/servers/{id}/descriptors"].Projections)
	collections[0].QueryMembers[0] = "mutated"
	require.Equal(t, "cursor", CollectionContracts()[0].QueryMembers[0])
	for _, token := range ServerStatusFilters() {
		require.Equal(t, strings.ToLower(token), token)
		require.NotContains(t, token, " ")
	}
}

func TestV2AuthorizationCollectionTablesMatchMechanics(t *testing.T) {
	text := readDesignCorpus(t)
	for _, collection := range CollectionContracts() {
		if collection.Pattern != "/api/v2/principals" && collection.Pattern != "/api/v2/mcp/grants" {
			continue
		}
		found := false
		for _, line := range strings.Split(text, "\n") {
			columns := strings.Split(line, "|")
			if len(columns) < 5 || strings.TrimSpace(columns[1]) != "`GET "+collection.Pattern+"`" {
				continue
			}
			found = true
			require.Equal(t, "`"+collection.SuccessSchema+"` / 200", strings.TrimSpace(columns[3]), collection.Pattern)
		}
		require.True(t, found, collection.Pattern)
	}
}

func TestV2MechanicsCoverEveryAdministrativeMethod(t *testing.T) {
	mechanics := make(map[string]bool)
	for _, mechanic := range ResourceMechanics() {
		key := mechanic.Method + " " + mechanic.Pattern
		require.False(t, mechanics[key], key)
		mechanics[key] = true
	}
	for _, route := range Routes() {
		if !strings.HasPrefix(route.Pattern, "/api/v2/") {
			continue
		}
		for _, method := range route.Methods {
			require.True(t, mechanics[method+" "+route.Pattern], method+" "+route.Pattern)
		}
	}
}
