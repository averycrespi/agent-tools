//go:build integration

package httpcredentials

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/audit"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/httppolicy"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/keyring"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestIntegrationImportedHTTPDefinitionRestartAndCompletion(t *testing.T) {
	s, store, owner := fixtureWithOwnership(t, nil)
	ctx := audit.WithSystem(t.Context())
	const id = "01ARZ3NDEKTSV4RRFFQ69G5FAX"
	const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FAW"
	def := testDefinition()
	credentialID := id
	policy := contract.HTTPPolicy{Version: 1, Type: contract.HTTPAllowRequests, CredentialID: &credentialID, Request: &contract.HTTPRequestSelector{Origin: contract.HTTPOriginSelector{Scheme: "https", Host: def.Boundary.Host, Port: 443}, Methods: contract.HTTPMethods{Any: true}, Path: contract.HTTPPathSelector{Kind: contract.HTTPPathAny}}}
	compiled, err := httppolicy.Compile(policy)
	require.NoError(t, err)
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(ctx, func(tx *sql.Tx) error {
		if err := StageImportedDefinitionTx(ctx, tx, id, def, clock{}.Now()); err != nil {
			return err
		}
		if err := s.repository.CheckReferenceTx(ctx, tx, id, compiled); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO test_http_grant_references VALUES(?,?,?)`, grantID, id, string(raw))
		return err
	}))
	require.NoError(t, ValidateStartup(ctx, store))
	require.NoError(t, store.Close())
	reopened, err := storage.Open(ctx, owner)
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	require.NoError(t, ValidateStartup(ctx, reopened))
	repo, err := NewRepository(reopened, clock{}, rand.Reader, referenceStore{})
	require.NoError(t, err)
	provider, err := keyring.NewProvider("01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.NoError(t, provider.UseDatabaseCustody(ctx, owner, reopened))
	s, err = NewService(repo, keyring.NewCoordinator(provider, reopened, clock{}, rand.Reader), "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	unset, err := s.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, contract.CredentialNotConfigured, unset.MaterialState)
	require.False(t, unset.Available)
	require.Equal(t, def, unset.Definition)
	require.Equal(t, grantID, unset.References[0].ID)
	_, err = s.Acquire(ctx, resourceRef(t, unset))
	require.ErrorIs(t, err, ErrNotConfigured)
	require.NoError(t, reopened.View(ctx, func(tx *sql.Tx) error {
		facts, err := SetupFactsTx(ctx, tx)
		require.NoError(t, err)
		require.Equal(t, []contract.CredentialSetupFact{{ID: id, Kind: "http_credential", Action: "supply_value"}}, facts)
		var count int
		require.NoError(t, tx.QueryRowContext(ctx, `SELECT count(*) FROM keyring_authorities WHERE owner=?`, id).Scan(&count))
		require.Zero(t, count)
		return nil
	}))
	unrelated, err := s.Create(ctx, def, []byte("unrelated-http-canary"))
	require.NoError(t, err)
	material, err := s.Acquire(ctx, resourceRef(t, unrelated))
	require.NoError(t, err)
	material.Clear()
	_, err = s.Create(ctx, def, nil)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = s.Rotate(ctx, id, unset.Revision, nil)
	require.ErrorIs(t, err, ErrInvalid)
	completed, err := s.Rotate(ctx, id, unset.Revision, []byte("imported-http-secret-canary"))
	require.NoError(t, err)
	require.Equal(t, id, completed.ID)
	require.Equal(t, unset.References, completed.References)
	require.Equal(t, contract.CredentialMaterialReady, completed.MaterialState)
	material, err = s.Acquire(ctx, resourceRef(t, completed))
	require.NoError(t, err)
	defer material.Clear()
	target, err := httppolicy.ParseRequest("https://api.example.com/", "GET", "api.example.com", "", nil)
	require.NoError(t, err)
	headers, err := material.Headers(target, nil)
	require.NoError(t, err)
	require.Equal(t, "Bearer imported-http-secret-canary", headers.Get("Authorization"))
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
	// Corruption checks still reject an invalid opaque handle, even on an unset row.
	require.NoError(t, reopened.Mutate(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE http_credentials SET handle='fabricated' WHERE id=?`, id)
		return err
	}))
	require.Error(t, ValidateStartup(ctx, reopened))
}
