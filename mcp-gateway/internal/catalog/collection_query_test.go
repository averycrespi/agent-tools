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
