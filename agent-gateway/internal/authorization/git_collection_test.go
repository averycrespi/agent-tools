package authorization

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestGitCollectionFullInventoryAndContinuation(t *testing.T) {
	r, store := newRepository(t, nil)
	entropy := make([]byte, 4096)
	for i := range entropy {
		entropy[i] = byte(i%251 + 1)
	}
	r.entropy = bytes.NewReader(entropy)
	ctx := t.Context()
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Éléphant agent"})
	var repositories []contract.GitRepository
	for i := 0; i < 63; i++ {
		name := "Duplicate"
		if i == 62 {
			name = "Alpine repository"
		}
		repo, err := r.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: name, URL: fmt.Sprintf("https://example.com/r%d", i), Aliases: []string{}})
		require.NoError(t, err)
		repositories = append(repositories, repo)
		_, err = r.PutGitGrant(ctx, "", "", GitGrantInput{PrincipalID: id(1), RepositoryID: repo.ID, Description: &name, Policy: json.RawMessage(gitReadPolicy)})
		require.NoError(t, err)
	}
	for _, direction := range []string{"ascending", "descending"} {
		q := GitCollectionQuery{Sort: "name", Direction: direction}
		first, err := r.QueryGitRepositories(ctx, q, "", 50)
		require.NoError(t, err)
		require.Len(t, first.Items, 50)
		require.Equal(t, 63, first.TotalCount)
		require.NotNil(t, first.NextCursor)
		next, err := r.QueryGitRepositories(ctx, q, *first.NextCursor, 50)
		require.NoError(t, err)
		require.Len(t, next.Items, 13)
		require.Equal(t, 50, next.Offset)
		require.Nil(t, next.NextCursor)
		again, err := r.QueryGitRepositories(ctx, q, *first.NextCursor, 50)
		require.NoError(t, err)
		require.Equal(t, next, again)
		all := append([]contract.GitRepository{}, first.Items...)
		all = append(all, next.Items...)
		seen := map[string]bool{}
		for i, item := range all {
			require.False(t, seen[item.ID])
			seen[item.ID] = true
			if i > 0 && all[i-1].Name == item.Name {
				require.Less(t, all[i-1].ID, item.ID)
			}
		}
		if direction == "ascending" {
			require.Equal(t, repositories[62].ID, first.Items[0].ID)
		} else {
			require.Equal(t, repositories[62].ID, next.Items[12].ID)
		}
		_, err = r.QueryGitRepositories(ctx, GitCollectionQuery{Sort: "destination"}, *first.NextCursor, 50)
		require.ErrorIs(t, err, ErrStaleCursor)
		_, err = r.QueryGitGrants(ctx, GitCollectionQuery{Sort: "description"}, *first.NextCursor, 50)
		require.ErrorIs(t, err, ErrStaleCursor)
	}
	matched, err := r.QueryGitRepositories(ctx, GitCollectionQuery{Name: "alpnie", Destination: "/r62", Sort: "name"}, "", 50)
	require.NoError(t, err)
	require.Len(t, matched.Items, 1)
	require.Equal(t, repositories[62].ID, matched.Items[0].ID)
	for _, value := range []string{repositories[62].ID, repositories[62].ID[10:]} {
		p, e := r.QueryGitRepositories(ctx, GitCollectionQuery{Name: value}, "", 50)
		require.NoError(t, e)
		require.True(t, len(p.Items) > 0)
	}
	p, e := r.QueryGitRepositories(ctx, GitCollectionQuery{Name: strings.ToLower(repositories[62].ID)}, "", 50)
	require.NoError(t, e)
	require.Empty(t, p.Items)
	g, err := r.QueryGitGrants(ctx, GitCollectionQuery{Identity: "alpnie", Principal: "elephnat", Repository: "alpnie", State: "active", Sort: "description"}, "", 50)
	require.NoError(t, err)
	require.Len(t, g.Items, 1)
	require.Equal(t, repositories[62].ID, g.Items[0].RepositoryID)
	first, err := r.QueryGitRepositories(ctx, GitCollectionQuery{}, "", 1)
	require.NoError(t, err)
	legacy := base64.RawURLEncoding.EncodeToString([]byte("v1\x00git_repositories\x00" + first.Items[0].ID))
	legacyPage, err := r.QueryGitRepositories(ctx, GitCollectionQuery{Sort: "id", Direction: "ascending"}, legacy, 1)
	require.NoError(t, err)
	require.Equal(t, 1, legacyPage.Offset)
	_, err = r.QueryGitRepositories(ctx, GitCollectionQuery{Sort: "name"}, legacy, 1)
	require.ErrorIs(t, err, ErrInvalidGitCursor)
	_, err = r.QueryGitRepositories(ctx, GitCollectionQuery{Sort: "id", Direction: "ascending"}, *first.NextCursor, 1)
	require.NoError(t, err)
	updated := repositories[62]
	updated.Name = "Moved"
	_, err = r.PutGitRepository(ctx, updated.ID, updated.Revision, updated.GitRepositoryDefinition)
	require.NoError(t, err)
	_, err = r.QueryGitRepositories(ctx, GitCollectionQuery{}, *first.NextCursor, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
}

func TestGitGrantCollectionHydratesOnlyPageAndInvalidatesNamesExpiry(t *testing.T) {
	r, store := newRepository(t, nil)
	ctx := t.Context()
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Agent"})
	repo, err := r.PutGitRepository(ctx, "", "", gitRepositoryInput())
	require.NoError(t, err)
	expires := testNow.Add(time.Minute)
	for _, name := range []string{"Alpine", "Zulu"} {
		_, err = r.PutGitGrant(ctx, "", "", GitGrantInput{PrincipalID: id(1), RepositoryID: repo.ID, Description: &name, Policy: json.RawMessage(gitReadPolicy), ExpiresAt: &expires})
		require.NoError(t, err)
	}
	q := GitCollectionQuery{Sort: "description"}
	first, err := r.QueryGitGrants(ctx, q, "", 1)
	require.NoError(t, err)
	require.NotNil(t, first.NextCursor)
	_, err = r.PatchPrincipal(ctx, id(1), PatchPrincipalRequest{ExpectedRevision: "1", DisplayName: strPtr("Renamed")})
	require.NoError(t, err)
	_, err = r.QueryGitGrants(ctx, q, *first.NextCursor, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	first, err = r.QueryGitGrants(ctx, q, "", 1)
	require.NoError(t, err)
	r.clock = &fixedClock{now: expires}
	_, err = r.QueryGitGrants(ctx, q, *first.NextCursor, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	expired, err := r.QueryGitGrants(ctx, GitCollectionQuery{State: "expired"}, "", 50)
	require.NoError(t, err)
	require.Len(t, expired.Items, 2)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE git_grants SET policy_json='{}' WHERE description='Zulu'`)
		return e
	}))
	first, err = r.QueryGitGrants(ctx, q, "", 1)
	require.NoError(t, err)
	require.Equal(t, "Alpine", *first.Items[0].Description)
	_, err = r.QueryGitGrants(ctx, q, *first.NextCursor, 1)
	require.Error(t, err)
}

func TestGitCollectionQueryBoundsAndCursorAuthentication(t *testing.T) {
	r, _ := newRepository(t, nil)
	items := []GitCollectionCandidate{{ID: id(1), Name: "Duplicate"}, {ID: id(2), Name: "Duplicate"}, {ID: id(3)}}
	q := GitCollectionQuery{Sort: "description"}
	first, err := r.SelectGitCollection("git_grants", q, items, "", 1)
	require.NoError(t, err)
	require.Equal(t, id(1), first.Items[0].ID)
	raw, err := base64.RawURLEncoding.DecodeString(*first.NextCursor)
	require.NoError(t, err)
	var cursor SnapshotCursor
	require.NoError(t, json.Unmarshal(raw, &cursor))
	cursor.AfterID = id(2)
	raw, err = json.Marshal(cursor)
	require.NoError(t, err)
	_, err = r.SelectGitCollection("git_grants", q, items, base64.RawURLEncoding.EncodeToString(raw), 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	other, _ := newRepository(t, nil)
	other.cursorKey[0] ^= 1
	_, err = other.SelectGitCollection("git_grants", q, items, *first.NextCursor, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	r.clock = &fixedClock{now: testNow.Add(contract.AuthorizationCursorLifetime)}
	_, err = r.SelectGitCollection("git_grants", q, items, *first.NextCursor, 1)
	require.ErrorIs(t, err, ErrStaleCursor)
	for _, q := range []GitCollectionQuery{{Name: strings.Repeat("x", 257)}, {Name: "bad\n"}, {Name: "\u200b"}, {Direction: "ascending"}, {Sort: "name", Direction: "sideways"}, {Principal: "agent"}} {
		require.False(t, q.Validate("git_repositories"))
	}
}
