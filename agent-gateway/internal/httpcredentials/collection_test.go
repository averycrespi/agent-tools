package httpcredentials

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

const collectionTestCursorKey = "collection fixture signing authority"

func TestCredentialCollectionGlobalRecognitionAndSnapshot(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	items := make([]Resource, 128)
	for i := range items {
		items[i] = Resource{ID: fmt.Sprintf("%026d", i+1), Definition: Definition{Name: "Ordinary credential", Boundary: Boundary{Host: "near.invalid", Port: 443}, Recipe: contract.HTTPCredentialRecipe{Header: "Authorization", Prefix: "Bearer "}}, Available: true, Revision: "1", References: []Reference{}, CreatedAt: now.Format(time.RFC3339Nano)}
	}
	items[127].Name = "Éléphant Faraway"
	items[127].Boundary.Host = "faraway.invalid"
	items[127].Recipe = contract.HTTPCredentialRecipe{Header: "X-Api-Key", Prefix: "Fixed "}
	items[127].Available = false
	first, err := SelectPage(items, CollectionQuery{}, "", 50, now, collectionTestCursorKey)
	require.NoError(t, err)
	require.Len(t, first.Items, 50)
	require.Equal(t, 128, first.TotalCount)
	second, err := SelectPage(items, CollectionQuery{}, *first.NextCursor, 50, now.Add(time.Second), collectionTestCursorKey)
	require.NoError(t, err)
	require.Equal(t, 50, second.Offset)
	require.Len(t, second.Items, 50)
	third, err := SelectPage(items, CollectionQuery{}, *second.NextCursor, 50, now.Add(2*time.Second), collectionTestCursorKey)
	require.NoError(t, err)
	require.Equal(t, 100, third.Offset)
	require.Len(t, third.Items, 28)
	require.Nil(t, third.NextCursor)
	require.Equal(t, items, append(append(first.Items, second.Items...), third.Items...))
	for _, query := range []CollectionQuery{{Name: "elephnat faraway"}, {Name: items[127].ID}, {Boundary: "faraway 443"}, {Recipe: "x-api fixed"}, {Status: "unavailable"}, {Name: "faraway", Boundary: "faraway", Recipe: "Fixed", Status: "unavailable"}} {
		page, err := SelectPage(items, query, "", 50, now, collectionTestCursorKey)
		require.NoError(t, err)
		require.Equal(t, 1, page.TotalCount)
		require.Equal(t, 0, page.Offset)
		require.Equal(t, []Resource{items[127]}, page.Items)
	}
	for _, query := range []CollectionQuery{{Name: "opaque-material-canary"}, {Boundary: "*.faraway.invalid"}, {Boundary: "444"}, {Recipe: "Fi"}, {Name: strings.ToLower(items[127].ID) + "x"}, {Name: "faraway", Status: "configured"}} {
		// Recognition is not wildcard authority, secret search, or a current-page filter.
		page, err := SelectPage(items, query, "", 50, now, collectionTestCursorKey)
		require.NoError(t, err)
		if query.Recipe == "Fi" {
			require.Equal(t, 1, page.TotalCount)
		} else {
			require.Zero(t, page.TotalCount)
			require.Zero(t, page.Offset)
			require.Empty(t, page.Items)
		}
	}
	_, err = SelectPage(items, CollectionQuery{Status: "configured"}, *first.NextCursor, 50, now, collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
	_, err = SelectPage(items, CollectionQuery{}, *first.NextCursor, 50, now.Add(contract.AuthorizationCursorLifetime), collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
	items[127].Available = true
	_, err = SelectPage(items, CollectionQuery{}, *first.NextCursor, 50, now, collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
	fresh, err := SelectPage(items, CollectionQuery{}, "", 100, now, collectionTestCursorKey)
	require.NoError(t, err)
	require.Len(t, fresh.Items, 100)
	legacy := base64.RawURLEncoding.EncodeToString([]byte("v1\x00http_credentials\x00" + items[49].ID))
	continued, err := SelectPage(items, CollectionQuery{}, legacy, 50, now, collectionTestCursorKey)
	require.NoError(t, err)
	require.Equal(t, 50, continued.Offset)
	_, err = SelectPage(items, CollectionQuery{Name: "Ordinary"}, legacy, 50, now, collectionTestCursorKey)
	require.ErrorIs(t, err, ErrInvalidCursor)
}

func TestCredentialCollectionCursorExpiryCannotBeRenewed(t *testing.T) {
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	items := []Resource{{ID: "00000000000000000000000001"}, {ID: "00000000000000000000000002"}, {ID: "00000000000000000000000003"}}
	first, err := SelectPage(items, CollectionQuery{}, "", 1, now, collectionTestCursorKey)
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	late, err := SelectPage(items, CollectionQuery{}, *first.NextCursor, 1, now.Add(contract.AuthorizationCursorLifetime-time.Second), collectionTestCursorKey)
	require.NoError(t, err)
	require.NotNil(t, late.NextCursor)
	_, err = SelectPage(items, CollectionQuery{}, *late.NextCursor, 1, now.Add(contract.AuthorizationCursorLifetime), collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
	raw, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	require.NoError(t, err)
	var position collectionCursor
	require.NoError(t, json.Unmarshal(raw, &position))
	original := position
	position.Expires = now.Add(2 * contract.AuthorizationCursorLifetime).Unix()
	raw, err = json.Marshal(position)
	require.NoError(t, err)
	forged := base64.RawURLEncoding.EncodeToString(raw)
	_, err = SelectPage(items, CollectionQuery{}, forged, 1, now.Add(contract.AuthorizationCursorLifetime+time.Second), collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
	position = original
	position.After = items[1].ID
	raw, err = json.Marshal(position)
	require.NoError(t, err)
	_, err = SelectPage(items, CollectionQuery{}, base64.RawURLEncoding.EncodeToString(raw), 1, now, collectionTestCursorKey)
	require.ErrorIs(t, err, ErrStaleCursor)
}

func TestCredentialCollectionRejectsInvalidQueriesAndCursors(t *testing.T) {
	for _, q := range []CollectionQuery{{Name: strings.Repeat("é", 129)}, {Boundary: "a\n"}, {Recipe: "a\u200b"}, {Status: "active"}, {Status: "Configured"}} {
		require.False(t, q.Validate())
	}
	require.True(t, (CollectionQuery{Name: strings.Repeat("é", 128)}).Validate())
	for _, cursor := range []string{"bad", strings.Repeat("x", 513), base64.RawURLEncoding.EncodeToString([]byte(`{"Version":2,"After":"00000000000000000000000001","Extra":true}`))} {
		_, err := SelectPage(nil, CollectionQuery{}, cursor, 50, time.Now(), collectionTestCursorKey)
		require.ErrorIs(t, err, ErrInvalidCursor)
	}
	for _, limit := range []int{0, 101} {
		_, err := SelectPage(nil, CollectionQuery{}, "", limit, time.Now(), collectionTestCursorKey)
		require.ErrorIs(t, err, ErrInvalid)
	}
}
