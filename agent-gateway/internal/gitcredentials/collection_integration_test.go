//go:build integration

package gitcredentials

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/stretchr/testify/require"
)

func TestIntegrationGitCredentialCollectionSnapshot(t *testing.T) {
	s, backend, _ := fixture(t)
	ctx := audit.WithSystem(t.Context())
	var credentials []contract.GitCredential
	for i := 0; i < 53; i++ {
		def := definition()
		def.Name = fmt.Sprintf("Credential %02d", 52-i)
		if i == 52 {
			def.Name = "Éléphant"
		}
		c, err := s.Create(ctx, def, []byte("synthetic-secret"))
		require.NoError(t, err)
		credentials = append(credentials, c)
	}
	q := authorization.GitCollectionQuery{Sort: "name"}
	first, err := s.Query(ctx, q, "", 50)
	require.NoError(t, err)
	require.Len(t, first.Items, 50)
	require.Equal(t, 53, first.TotalCount)
	second, err := s.Query(ctx, q, *first.NextCursor, 50)
	require.NoError(t, err)
	require.Len(t, second.Items, 3)
	require.Nil(t, second.NextCursor)
	require.Equal(t, 50, second.Offset)
	require.Equal(t, "Credential 01", first.Items[0].Name)
	require.Equal(t, "Éléphant", second.Items[2].Name)
	for _, direction := range []string{"ascending", "descending"} {
		p, e := s.Query(ctx, authorization.GitCollectionQuery{Sort: "name", Direction: direction, Name: "elephnat", Origin: "EXAMPLE.COM", Status: "configured"}, "", 1)
		require.NoError(t, e)
		require.Len(t, p.Items, 1)
		require.Equal(t, credentials[52].ID, p.Items[0].ID)
	}
	p, e := s.Query(ctx, authorization.GitCollectionQuery{Name: credentials[52].ID[10:]}, "", 50)
	require.NoError(t, e)
	require.Len(t, p.Items, 1)
	c := credentials[52]
	def := c.GitCredentialDefinition
	def.Name = "Aardvark"
	c, err = s.Update(ctx, c.ID, c.Revision, def)
	require.NoError(t, err)
	_, err = s.Query(ctx, q, *first.NextCursor, 50)
	require.ErrorIs(t, err, authorization.ErrStaleCursor)
	first, err = s.Query(ctx, q, "", 50)
	require.NoError(t, err)
	repo, err := s.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Repository", URL: "https://example.com/team/repo", Aliases: []string{}, CredentialID: &c.ID})
	require.NoError(t, err)
	_, err = s.Query(ctx, q, *first.NextCursor, 50)
	require.ErrorIs(t, err, authorization.ErrStaleCursor)
	require.NoError(t, s.authority.DeleteGitRepository(ctx, repo.ID, repo.Revision))
	referenced, err := s.Query(ctx, authorization.GitCollectionQuery{Name: c.ID}, "", 1)
	require.NoError(t, err)
	require.Equal(t, []contract.GitCredentialReference{{ID: repo.ID}}, referenced.Items[0].References)
	first, err = s.Query(ctx, q, "", 50)
	require.NoError(t, err)
	backend.fail = true
	_, err = s.Rotate(ctx, c.ID, c.Revision, []byte("replacement"))
	require.Error(t, err)
	_, err = s.Query(ctx, q, *first.NextCursor, 50)
	require.ErrorIs(t, err, authorization.ErrStaleCursor)
	unavailable, err := s.Query(ctx, authorization.GitCollectionQuery{Status: "unavailable"}, "", 50)
	require.NoError(t, err)
	require.Len(t, unavailable.Items, 1)
	require.Equal(t, c.ID, unavailable.Items[0].ID)
	require.False(t, unavailable.Items[0].Available)
	legacyFirst, err := s.Query(ctx, authorization.GitCollectionQuery{}, "", 1)
	require.NoError(t, err)
	legacy := base64.RawURLEncoding.EncodeToString([]byte("v1\x00git_credentials\x00" + legacyFirst.Items[0].ID))
	legacyNext, err := s.Query(ctx, authorization.GitCollectionQuery{}, legacy, 1)
	require.NoError(t, err)
	require.Equal(t, 1, legacyNext.Offset)
}
