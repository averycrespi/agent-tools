package authorization

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreatePrincipalAtomicallyCreatesOrdinaryDefaultGrant(t *testing.T) {
	repository, _ := newRepository(t, nil)
	created, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{
		DisplayName: "Build Agent", Visibility: contract.VisibilityAllowedOnly,
	})
	require.NoError(t, err)
	assert.Equal(t, "Build Agent", created.Principal.DisplayName)
	assert.Equal(t, contract.PrincipalActive, created.Principal.State)
	assert.Equal(t, contract.VisibilityAllowedOnly, created.Principal.Visibility)
	assert.Equal(t, "1", created.Principal.Revision)
	assert.Equal(t, "0", created.Principal.CredentialRevision)
	assert.Nil(t, created.Principal.Credential)
	assert.Equal(t, created.Principal.ID, created.DefaultGrant.PrincipalID)
	assert.Equal(t, contract.GrantAllow, created.DefaultGrant.Effect)
	assert.Equal(t, contract.SyntheticServerID, created.DefaultGrant.ServerID)
	assert.Nil(t, created.DefaultGrant.UpstreamName)
	assert.Nil(t, created.DefaultGrant.Constraint)
	assert.Nil(t, created.DefaultGrant.ExpiresAt)
	assert.Equal(t, contract.GrantActive, created.DefaultGrant.State)
	assert.Equal(t, created.Principal.CreatedAt, created.DefaultGrant.CreatedAt)

	revision, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "1", revision)
	principal, err := repository.GetPrincipal(context.Background(), created.Principal.ID)
	require.NoError(t, err)
	assert.Equal(t, created.Principal, principal)
	grant, err := repository.GetGrant(context.Background(), created.DefaultGrant.ID)
	require.NoError(t, err)
	assert.Equal(t, created.DefaultGrant, grant)
}

func TestCreatePrincipalRollsBackPrincipalGrantAndRevisionTogether(t *testing.T) {
	for _, test := range []struct {
		name    string
		trigger string
	}{
		{name: "default grant failure", trigger: `CREATE TRIGGER fail_default_grant BEFORE INSERT ON grants BEGIN SELECT RAISE(ABORT, 'injected'); END`},
		{name: "revision failure after both inserts", trigger: `CREATE TRIGGER fail_revision BEFORE UPDATE ON authorization_meta BEGIN SELECT RAISE(ABORT, 'injected'); END`},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository, store := newRepository(t, nil)
			require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
				_, err := transaction.Exec(test.trigger)
				return err
			}))

			_, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
			assert.ErrorIs(t, err, ErrStorageUnavailable)
			principals, grants, statusErr := repository.Occupancy(context.Background())
			require.NoError(t, statusErr)
			assert.Zero(t, principals.InUse)
			assert.Zero(t, grants.InUse)
			revision, revisionErr := repository.AuthorizationRevision(context.Background())
			require.NoError(t, revisionErr)
			assert.Equal(t, "0", revision)
		})
	}
}

func TestPatchPrincipalAppliesExactRevisionRules(t *testing.T) {
	repository, _ := newRepository(t, nil)
	created, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	principalID := created.Principal.ID

	display := "Renamed"
	patched, err := repository.PatchPrincipal(context.Background(), principalID, PatchPrincipalRequest{ExpectedRevision: "1", DisplayName: &display})
	require.NoError(t, err)
	assert.Equal(t, "2", patched.Revision)
	assert.Equal(t, "0", patched.CredentialRevision)
	assert.Equal(t, "Renamed", patched.DisplayName)
	assert.Equal(t, "1", authorizationRevision(t, repository))

	visibility := contract.VisibilityAll
	patched, err = repository.PatchPrincipal(context.Background(), principalID, PatchPrincipalRequest{ExpectedRevision: "2", Visibility: &visibility})
	require.NoError(t, err)
	assert.Equal(t, "3", patched.Revision)
	assert.Equal(t, "1", authorizationRevision(t, repository))

	disabled := contract.PrincipalDisabled
	patched, err = repository.PatchPrincipal(context.Background(), principalID, PatchPrincipalRequest{ExpectedRevision: "3", State: &disabled})
	require.NoError(t, err)
	assert.Equal(t, "4", patched.Revision)
	assert.Equal(t, "1", patched.CredentialRevision, "disable advances credential revision even without a credential")
	assert.Equal(t, "2", authorizationRevision(t, repository))

	display = "Disabled rename"
	patched, err = repository.PatchPrincipal(context.Background(), principalID, PatchPrincipalRequest{ExpectedRevision: "4", DisplayName: &display})
	require.NoError(t, err)
	assert.Equal(t, "5", patched.Revision)
	assert.Equal(t, "1", patched.CredentialRevision)
	assert.Equal(t, "2", authorizationRevision(t, repository), "display changes while disabled do not change policy")

	active := contract.PrincipalActive
	display = "Re-enabled"
	visibility = contract.VisibilityAllowedOnly
	patched, err = repository.PatchPrincipal(context.Background(), principalID, PatchPrincipalRequest{
		ExpectedRevision: "5", DisplayName: &display, State: &active, Visibility: &visibility,
	})
	require.NoError(t, err)
	assert.Equal(t, "6", patched.Revision, "a multi-field patch advances once")
	assert.Equal(t, "1", patched.CredentialRevision)
	assert.Nil(t, patched.Credential)
	assert.Equal(t, "3", authorizationRevision(t, repository), "a state-changing multi-field patch advances policy once")
}

func TestDisableClearsCurrentCredentialAndKeepsGrants(t *testing.T) {
	repository, store := newRepository(t, nil)
	created, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`UPDATE principals SET credential_revision = 1, credential_id = ?, credential_verifier = ?, credential_fingerprint = '0123456789abcdef', credential_created_at = ? WHERE id = ?`,
			id(101), bytes.Repeat([]byte{1}, 32), timestamp(testNow), created.Principal.ID)
		return err
	}))

	disabled := contract.PrincipalDisabled
	patched, err := repository.PatchPrincipal(context.Background(), created.Principal.ID, PatchPrincipalRequest{ExpectedRevision: "1", State: &disabled})
	require.NoError(t, err)
	assert.Equal(t, "2", patched.CredentialRevision)
	assert.Nil(t, patched.Credential)
	grant, err := repository.GetGrant(context.Background(), created.DefaultGrant.ID)
	require.NoError(t, err)
	assert.Equal(t, created.DefaultGrant.ID, grant.ID)
}

func TestPatchPrincipalRejectsEmptyNoopStaleMissingAndInvalidRequests(t *testing.T) {
	repository, _ := newRepository(t, nil)
	created, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	idValue := created.Principal.ID

	_, err = repository.PatchPrincipal(context.Background(), idValue, PatchPrincipalRequest{ExpectedRevision: "1"})
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.PatchPrincipal(context.Background(), idValue, PatchPrincipalRequest{ExpectedRevision: "01", DisplayName: stringPointer("Other")})
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.PatchPrincipal(context.Background(), idValue, PatchPrincipalRequest{ExpectedRevision: "2", DisplayName: stringPointer("Other")})
	assert.ErrorIs(t, err, ErrStaleRevision)
	_, err = repository.PatchPrincipal(context.Background(), id(99), PatchPrincipalRequest{ExpectedRevision: "1", DisplayName: stringPointer("Other")})
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = repository.PatchPrincipal(context.Background(), idValue, PatchPrincipalRequest{ExpectedRevision: "1", DisplayName: stringPointer("Agent")})
	assert.ErrorIs(t, err, ErrConflict)
	invalidState := contract.PrincipalState("foreign")
	_, err = repository.PatchPrincipal(context.Background(), idValue, PatchPrincipalRequest{ExpectedRevision: "1", State: &invalidState})
	assert.ErrorIs(t, err, ErrInvalidInput)
	invalidVisibility := contract.PrincipalVisibility("foreign")
	_, err = repository.PatchPrincipal(context.Background(), idValue, PatchPrincipalRequest{ExpectedRevision: "1", Visibility: &invalidVisibility})
	assert.ErrorIs(t, err, ErrInvalidInput)
	_, err = repository.PatchPrincipal(context.Background(), idValue, PatchPrincipalRequest{ExpectedRevision: "1", DisplayName: stringPointer("\n")})
	assert.ErrorIs(t, err, ErrInvalidInput)

	principal, err := repository.GetPrincipal(context.Background(), idValue)
	require.NoError(t, err)
	assert.Equal(t, created.Principal, principal)
	assert.Equal(t, "1", authorizationRevision(t, repository))
}

func TestCreatePrincipalMapsUncertainCommitToStorageUnavailable(t *testing.T) {
	armed := false
	repository, store := newRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("commit acknowledgement lost")
		}
		return nil
	})
	armed = true
	_, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	assert.True(t, store.Latched())
}

func TestCreatePrincipalValidatesInputAndEntropyBeforeMutation(t *testing.T) {
	repository, store := newRepository(t, nil)
	for _, request := range []CreatePrincipalRequest{
		{DisplayName: "", Visibility: contract.VisibilityRequestable},
		{DisplayName: "\n", Visibility: contract.VisibilityRequestable},
		{DisplayName: "Agent", Visibility: contract.PrincipalVisibility("foreign")},
	} {
		_, err := repository.CreatePrincipal(context.Background(), request)
		assert.ErrorIs(t, err, ErrInvalidInput)
	}

	failedEntropy, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(make([]byte, 32+10)))
	require.NoError(t, err)
	_, err = failedEntropy.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.Error(t, err)

	reservedID, err := New(store, &fixedClock{now: time.UnixMilli(0)}, bytes.NewReader(make([]byte, 32+20)))
	require.NoError(t, err)
	_, err = reservedID.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	assert.ErrorIs(t, err, ErrIdentityUnavailable)

	principals, grants, err := repository.Occupancy(context.Background())
	require.NoError(t, err)
	assert.Zero(t, principals.InUse)
	assert.Zero(t, grants.InUse)
}

func TestCreatePrincipalAcceptsNAndRejectsNPlusOnePrincipalCapacity(t *testing.T) {
	repository, store := newRepository(t, nil)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := 0; index < 127; index++ {
			_, err := transaction.Exec(`INSERT INTO principals
				(id, display_name, state, visibility, revision, credential_revision, created_at, updated_at)
				VALUES (?, 'Seed', 'active', 'requestable', 1, 0, ?, ?)`, wideID(index+1000), timestamp(testNow), timestamp(testNow))
			if err != nil {
				return err
			}
		}
		return nil
	}))
	_, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "At Capacity", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	principals, _, statusErr := repository.Occupancy(context.Background())
	require.NoError(t, statusErr)
	assert.Equal(t, contract.LimitStatus{InUse: 128, Limit: 128, Saturated: true}, principals)
	_, err = repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Over Capacity", Visibility: contract.VisibilityRequestable})
	assert.ErrorIs(t, err, ErrResourceLimit)
}

func TestCreatePrincipalDefaultGrantAcceptsNAndRejectsNPlusOneGrantCapacity(t *testing.T) {
	repository, store := newRepository(t, nil)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Seed"})
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		return insertGrantFixtures(transaction, 4095, id(1), contract.SyntheticServerID, "tool")
	}))
	_, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "At Capacity", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	_, grants, statusErr := repository.Occupancy(context.Background())
	require.NoError(t, statusErr)
	assert.Equal(t, contract.LimitStatus{InUse: 4096, Limit: 4096, Saturated: true}, grants)
	_, err = repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Over Capacity", Visibility: contract.VisibilityRequestable})
	assert.ErrorIs(t, err, ErrResourceLimit)
}

func TestPrincipalMutationErrorsRemainDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrConflict, ErrStaleRevision))
	assert.False(t, errors.Is(ErrIdentityUnavailable, ErrResourceLimit))
}

func authorizationRevision(t *testing.T, repository *Repository) string {
	t.Helper()
	revision, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	return revision
}

func stringPointer(value string) *string { return &value }
