package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDescriptorQueryGlobalPagesAndRevisionFence(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "pages")
	names := make([]string, 125)
	for index := range names {
		names[index] = fmt.Sprintf("tool_%03d", 124-index)
	}
	_, err := repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, "pages", names...))
	require.NoError(t, err)
	query := ToolQuery{Sort: "tool", Direction: "ascending"}
	first, err := repository.QueryDescriptors(context.Background(), server.ID, query, nil, 50)
	require.NoError(t, err)
	require.Len(t, first.Items, 50)
	require.NotNil(t, first.Next)
	second, err := repository.QueryDescriptors(context.Background(), server.ID, query, first.Next, 50)
	require.NoError(t, err)
	require.Len(t, second.Items, 50)
	require.NotNil(t, second.Next)
	third, err := repository.QueryDescriptors(context.Background(), server.ID, query, second.Next, 50)
	require.NoError(t, err)
	require.Len(t, third.Items, 25)
	assert.Nil(t, third.Next)
	all := append(append(descriptorNames(first.Items), descriptorNames(second.Items)...), descriptorNames(third.Items)...)
	sort.Strings(names)
	assert.Equal(t, names, all)
	back, err := repository.QueryDescriptors(context.Background(), server.ID, query, first.Next, 50)
	require.NoError(t, err)
	assert.Equal(t, second.Items, back.Items)
	matched, err := repository.QueryDescriptors(context.Background(), server.ID, ToolQuery{Tool: "TOOL_124"}, nil, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"tool_124"}, descriptorNames(matched.Items))
	_, err = repository.QueryDescriptors(context.Background(), server.ID, ToolQuery{Tool: "TOOL_124"}, first.Next, 50)
	require.ErrorIs(t, err, servers.ErrStaleCursor)
	_, err = repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredInclude, first.Next, 50)
	require.ErrorIs(t, err, servers.ErrStaleCursor)
	_, err = repository.ListDescriptorSummaries(context.Background(), server.ID, contract.DescriptorRetiredInclude, first.Next, 50)
	require.ErrorIs(t, err, servers.ErrStaleCursor)
	// Every last-seen value ties: immutable IDs must still produce one traversal.
	tied := ToolQuery{Sort: "last-seen", Direction: "descending"}
	var cursor *DescriptorCursor
	ids := make([]string, 0, len(names))
	for {
		page, readErr := repository.QueryDescriptors(context.Background(), server.ID, tied, cursor, 50)
		require.NoError(t, readErr)
		for _, item := range page.Items {
			ids = append(ids, item.Resource.ID)
		}
		cursor = page.Next
		if cursor == nil {
			break
		}
		require.Less(t, len(ids), 126)
	}
	require.Len(t, ids, 125)
	assert.True(t, sort.StringsAreSorted(ids))
	for index := 1; index < len(ids); index++ {
		assert.NotEqual(t, ids[index-1], ids[index])
	}
	clock.now = clock.now.Add(time.Minute)
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "1"), candidateFor(t, server.ID, "pages", "tool_124"))
	require.NoError(t, err)
	_, err = repository.QueryDescriptors(context.Background(), server.ID, query, first.Next, 50)
	require.ErrorIs(t, err, servers.ErrStaleCursor)
	available, err := repository.QueryDescriptors(context.Background(), server.ID, ToolQuery{Status: "available"}, nil, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"tool_124"}, descriptorNames(available.Items))
	retired, err := repository.QueryDescriptors(context.Background(), server.ID, ToolQuery{Status: "retired", Tool: "tool_000"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, retired.Items, 1)
	assert.NotNil(t, retired.Items[0].Resource.RetiredAt)
	empty, err := repository.QueryDescriptors(context.Background(), server.ID, ToolQuery{Tool: "missing"}, nil, 50)
	require.NoError(t, err)
	assert.Empty(t, empty.Items)
	assert.Nil(t, empty.Next)
}

func TestActiveQueryPagesAndGenerationFence(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "aggregate")
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	names := make([]string, 125)
	for index := range names {
		names[index] = fmt.Sprintf("tool_%03d", 124-index)
	}
	_, err = registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-1", RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, "aggregate", names...), Current: func() bool { return true }})
	require.NoError(t, err)
	query := ToolQuery{Sort: "tool"}
	first, err := registry.Query(query, nil, 50)
	require.NoError(t, err)
	require.Len(t, first.Items, 50)
	require.NotNil(t, first.Next)
	second, err := registry.Query(query, first.Next, 50)
	require.NoError(t, err)
	require.Len(t, second.Items, 50)
	require.NotNil(t, second.Next)
	third, err := registry.Query(query, second.Next, 50)
	require.NoError(t, err)
	require.Len(t, third.Items, 25)
	assert.Nil(t, third.Next)
	all := append(append(descriptorNames(first.Items), descriptorNames(second.Items)...), descriptorNames(third.Items)...)
	sort.Strings(names)
	assert.Equal(t, names, all)
	back, err := registry.Query(query, first.Next, 50)
	require.NoError(t, err)
	assert.Equal(t, second.Items, back.Items)
	matching, err := registry.Query(ToolQuery{Tool: "TOOL_124", Status: "available"}, nil, 50)
	require.NoError(t, err)
	assert.Equal(t, []string{"tool_124"}, descriptorNames(matching.Items))
	_, err = registry.List(first.Next, 50)
	require.ErrorIs(t, err, servers.ErrStaleCursor)
	_, err = registry.Query(ToolQuery{Sort: "server"}, first.Next, 50)
	require.ErrorIs(t, err, servers.ErrStaleCursor)
	tied, err := registry.Query(ToolQuery{Sort: "server", Direction: "descending"}, nil, 50)
	require.NoError(t, err)
	for index := 1; index < len(tied.Items); index++ {
		assert.Less(t, tied.Items[index-1].Resource.ID, tied.Items[index].Resource.ID)
	}
	require.True(t, registry.MarkStale(server.ID, "runtime-1", 1))
	_, err = registry.Query(query, first.Next, 50)
	require.ErrorIs(t, err, servers.ErrStaleCursor)
	available, err := registry.Query(ToolQuery{Status: "available"}, nil, 50)
	require.NoError(t, err)
	assert.Empty(t, available.Items)
	issues, err := registry.Query(ToolQuery{Status: "issue", Tool: "tool_124"}, nil, 50)
	require.NoError(t, err)
	require.Len(t, issues.Items, 1)
	assert.Equal(t, contract.ActiveCatalogStale, issues.ServerStates[server.ID])
	issues.Items[0].Resource.UpstreamName = "mutated"
	again, err := registry.Query(ToolQuery{Status: "issue", Tool: "tool_124"}, nil, 50)
	require.NoError(t, err)
	assert.Equal(t, "tool_124", again.Items[0].Resource.UpstreamName)
}

func TestDescriptorQuerySortOrder(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	server := createCatalogServer(t, serverRepository, "sorting")
	_, err := repository.Commit(context.Background(), catalogFence(server.ID, "0"), candidateFor(t, server.ID, "sorting", "z", "b", "a", "y", "c", "x"))
	require.NoError(t, err)
	clock.now = clock.now.Add(time.Minute)
	_, err = repository.Commit(context.Background(), catalogFence(server.ID, "1"), candidateFor(t, server.ID, "sorting", "b", "y", "x"))
	require.NoError(t, err)
	all, err := repository.ListDescriptors(context.Background(), server.ID, contract.DescriptorRetiredInclude, nil, 50)
	require.NoError(t, err)
	byName := make(map[string]string)
	for _, item := range all.Items {
		byName[item.Resource.UpstreamName] = item.Resource.ID
	}
	ids := func(names ...string) []string {
		result := make([]string, len(names))
		for index, name := range names {
			result[index] = byName[name]
		}
		return result
	}
	available, retired := ids("b", "y", "x"), ids("z", "a", "c")
	sort.Strings(available)
	sort.Strings(retired)
	for _, test := range []struct {
		key, direction string
		expected       []string
	}{
		{"tool", "ascending", ids("a", "b", "c", "x", "y", "z")},
		{"tool", "descending", ids("z", "y", "x", "c", "b", "a")},
		{"status", "ascending", append(append([]string{}, available...), retired...)},
		{"status", "descending", append(append([]string{}, retired...), available...)},
		{"last-seen", "ascending", append(append([]string{}, retired...), available...)},
		{"last-seen", "descending", append(append([]string{}, available...), retired...)},
	} {
		t.Run(test.key+"/"+test.direction, func(t *testing.T) {
			var cursor *DescriptorCursor
			var actual []string
			for pageIndex := 0; pageIndex < 3; pageIndex++ {
				page, err := repository.QueryDescriptors(context.Background(), server.ID, ToolQuery{Sort: test.key, Direction: test.direction}, cursor, 2)
				require.NoError(t, err)
				require.Len(t, page.Items, 2)
				for _, item := range page.Items {
					actual = append(actual, item.Resource.ID)
				}
				cursor = page.Next
			}
			assert.Nil(t, cursor)
			assert.Equal(t, test.expected, actual)
		})
	}
}

func TestActiveQuerySortOrder(t *testing.T) {
	repository, serverRepository, clock, _ := newCatalogRepository(t)
	registry, err := NewActiveRegistry(repository, clock, activeProcessID)
	require.NoError(t, err)
	byName := make(map[string][]string)
	displayNames := map[string]string{"zeta": "Alpha server", "alpha": "Zeta server", "middle": "Middle server"}
	for _, namespace := range []string{"zeta", "alpha", "middle"} {
		server := createCatalogServer(t, serverRepository, namespace)
		_, err := registry.Publish(context.Background(), Publication{Fence: catalogFence(server.ID, "0"), RuntimeID: "runtime-" + namespace, ServerDisplayName: displayNames[namespace], RuntimeGeneration: 1, Candidate: candidateFor(t, server.ID, namespace, "z", "a"), Current: func() bool { return true }})
		require.NoError(t, err)
		page, err := registry.Query(ToolQuery{Server: displayNames[namespace], Sort: "tool"}, nil, 50)
		require.NoError(t, err)
		require.Len(t, page.Items, 2)
		for _, item := range page.Items {
			byName[namespace] = append(byName[namespace], item.Resource.ID)
		}
	}
	for _, key := range []string{"tool", "server"} {
		for _, direction := range []string{"ascending", "descending"} {
			t.Run(key+"/"+direction, func(t *testing.T) {
				groups := []string{"alpha", "middle", "zeta"}
				if key == "server" {
					groups = []string{"zeta", "middle", "alpha"}
				}
				if direction == "descending" {
					groups[0], groups[2] = groups[2], groups[0]
				}
				var expected []string
				for _, group := range groups {
					values := append([]string{}, byName[group]...)
					if key == "server" {
						sort.Strings(values)
					} else if direction == "descending" {
						values[0], values[1] = values[1], values[0]
					}
					expected = append(expected, values...)
				}
				var cursor *ActiveCursor
				var actual []string
				for pageIndex := 0; pageIndex < 6; pageIndex++ {
					page, err := registry.Query(ToolQuery{Sort: key, Direction: direction}, cursor, 1)
					require.NoError(t, err)
					require.Len(t, page.Items, 1)
					for _, item := range page.Items {
						actual = append(actual, item.Resource.ID)
					}
					cursor = page.Next
				}
				assert.Nil(t, cursor)
				assert.Equal(t, expected, actual)
			})
		}
	}
}

func TestToolQueryRejectsInvalidOptions(t *testing.T) {
	for _, query := range []ToolQuery{
		{Tool: "\x00"}, {Tool: "\u202e"}, {Tool: "\xff"}, {Server: "server"},
		{Tool: strings.Repeat("\ufdfa", 20)},
		{Status: "issue"}, {Sort: "server"}, {Direction: "ascending"}, {Sort: "tool", Direction: "up"},
	} {
		assert.False(t, query.Validate(false), "%+v", query)
	}
	assert.True(t, (ToolQuery{Tool: "é", Status: "retired", Sort: "status", Direction: "descending"}).Validate(false))
}
