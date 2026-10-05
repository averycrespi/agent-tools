package httpcredentials

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCredentialCollectionNamedOrderAcrossPages(t *testing.T) {
	now := time.Now()
	items := make([]Resource, 128)
	for i := range items {
		items[i] = Resource{ID: fmt.Sprintf("%026d", 128-i), Definition: Definition{Name: "Duplicate"}}
	}
	items[127].Name = "Álpha"
	items[0].Name = "Zulu"
	query := CollectionQuery{Sort: "name", Direction: "ascending"}
	var got []Resource
	cursor := ""
	for {
		page, err := SelectPage(items, query, cursor, 50, now, collectionTestCursorKey)
		require.NoError(t, err)
		require.Equal(t, len(got), page.Offset)
		require.Equal(t, 128, page.TotalCount)
		got = append(got, page.Items...)
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	require.Len(t, got, 128)
	for i, item := range got {
		require.Equal(t, fmt.Sprintf("%026d", i+1), item.ID)
	}
	first, err := SelectPage(items, query, "", 50, now, collectionTestCursorKey)
	require.NoError(t, err)
	descending := CollectionQuery{Sort: "name", Direction: "descending"}
	_, err = SelectPage(items, descending, *first.NextCursor, 50, now, collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
	reverse, err := SelectPage(items, descending, "", 50, now, collectionTestCursorKey)
	require.NoError(t, err)
	require.Equal(t, items[0].ID, reverse.Items[0].ID)
	require.Equal(t, fmt.Sprintf("%026d", 2), reverse.Items[1].ID)
	partial, err := SelectPage(items, CollectionQuery{Name: "000127", Sort: "name"}, "", 50, now, collectionTestCursorKey)
	require.NoError(t, err)
	require.Equal(t, 1, partial.TotalCount)
	require.Equal(t, items[1].ID, partial.Items[0].ID)
	items[100].Name = "Renamed"
	_, err = SelectPage(items, query, *first.NextCursor, 50, now, collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
	for _, invalid := range []CollectionQuery{{Sort: "unknown"}, {Direction: "ascending"}, {Sort: "name", Direction: "up"}} {
		require.False(t, invalid.Validate())
	}
}
