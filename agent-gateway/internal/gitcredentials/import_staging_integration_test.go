//go:build integration

package gitcredentials

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/authorization"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIntegrationImportedGitDefinitionRestartAndCompletion(t *testing.T) {
	s, _, owner := fixtureWithFault(t, nil)
	ctx := audit.WithSystem(t.Context())
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	require.NoError(t, s.store.Mutate(ctx, func(tx *sql.Tx) error {
		return StageConfigurationDefinitionTx(ctx, tx, id, definition(), false, testClock{}.Now())
	}))
	credentialID := id
	repository, err := s.authority.PutGitRepository(ctx, "", "", contract.GitRepositoryDefinition{Name: "Imported repository", URL: "https://example.com/team/repo", Aliases: []string{}, CredentialID: &credentialID})
	require.NoError(t, err)
	require.NoError(t, ValidateStartup(ctx, s.store))
	require.NoError(t, s.store.Close())
	reopened, err := storage.Open(ctx, owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	require.NoError(t, ValidateStartup(ctx, reopened))
	authority, err := authorization.New(reopened, testClock{}, rand.Reader)
	require.NoError(t, err)
	provider, err := keyring.NewProvider(installation)
	require.NoError(t, err)
	require.NoError(t, provider.UseDatabaseCustody(ctx, owner, reopened))
	s, err = NewService(reopened, keyring.NewCoordinator(provider, reopened, testClock{}, rand.Reader), authority, testClock{}, rand.Reader, installation)
	require.NoError(t, err)
	unset, err := s.Get(ctx, id)
	require.NoError(t, err)
	canonical, err := Normalize(definition())
	require.NoError(t, err)
	require.True(t, contract.ValidResourceKey(unset.Key))
	require.Equal(t, canonical, unset.GitCredentialDefinition)
	require.Equal(t, contract.CredentialNotConfigured, unset.MaterialState)
	require.False(t, unset.Available)
	require.Equal(t, []contract.GitCredentialReference{{ID: repository.ID}}, unset.References)
	_, err = s.Acquire(ctx, ref(unset))
	require.ErrorIs(t, err, ErrNotConfigured)
	dispatched := false
	require.Error(t, s.ConfirmMaterial(ctx, ref(unset), "0", func() error { dispatched = true; return nil }))
	require.False(t, dispatched)
	require.NoError(t, reopened.View(ctx, func(tx *sql.Tx) error {
		facts, err := SetupFactsTx(ctx, tx)
		require.NoError(t, err)
		require.Equal(t, []contract.CredentialSetupFact{{ID: id, Kind: "git_credential", Action: "supply_value"}}, facts)
		var count int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities WHERE owner=?`, id).Scan(&count))
		require.Zero(t, count)
		return nil
	}))
	unrelated, err := s.Create(ctx, definition(), []byte("unrelated-git-canary"))
	require.NoError(t, err)
	material, err := s.Acquire(ctx, ref(unrelated))
	require.NoError(t, err)
	material.Clear()
	_, err = s.Create(ctx, definition(), nil)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.Rotate(ctx, id, unset.Revision, nil)
	require.ErrorIs(t, err, ErrInvalid)
	completed, err := s.Rotate(ctx, id, unset.Revision, []byte("imported-git-secret-canary"))
	require.NoError(t, err)
	require.Equal(t, id, completed.ID)
	require.Equal(t, unset.References, completed.References)
	require.Equal(t, contract.CredentialMaterialReady, completed.MaterialState)
	material, err = s.Acquire(ctx, ref(completed))
	require.NoError(t, err)
	defer material.Clear()
	headers, err := material.Apply(repository.URL, nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer imported-git-secret-canary", headers.Get("Authorization"))
	encoded, err := json.Marshal(completed)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "secret-canary")
	require.NoError(t, reopened.View(ctx, func(tx *sql.Tx) error {
		exports, err := ConfigurationTx(ctx, tx)
		require.NoError(t, err)
		encoded, err := json.Marshal(exports)
		require.NoError(t, err)
		require.NotContains(t, string(encoded), "secret-canary")
		return nil
	}))
	require.NoError(t, reopened.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE git_credentials SET handle='fabricated' WHERE id=?`, id)
		return err
	}))
	require.Error(t, ValidateStartup(ctx, reopened))
}
