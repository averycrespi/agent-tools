package authorization

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/servers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateAndDeleteImmutableGrantsAdvanceAuthorizationOnce(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	constraint := json.RawMessage(`{"equals":{"/repository":"owner/repo","/attempt":1.0}}`)
	expiresAt := testNow.Add(time.Hour)

	grant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{
		Description: stringPointer("Block direct repository access"), PrincipalID: principal.ID, Effect: contract.GrantDeny, ServerID: id(51), UpstreamName: stringPointer("direct_tool"),
		Constraint: &constraint, ExpiresAt: &expiresAt,
	}, allowCurrentTarget)
	require.NoError(t, err)
	require.NotNil(t, grant.Description)
	assert.Equal(t, "Block direct repository access", *grant.Description)
	assert.Equal(t, principal.ID, grant.PrincipalID)
	assert.Equal(t, contract.GrantDeny, grant.Effect)
	assert.Equal(t, id(51), grant.ServerID)
	assert.Equal(t, "direct_tool", *grant.UpstreamName)
	require.NotNil(t, grant.Constraint)
	assert.Equal(t, string(constraint), string(*grant.Constraint))
	assert.Equal(t, timestamp(expiresAt), *grant.ExpiresAt)
	assert.Equal(t, contract.GrantActive, grant.State)
	assert.Equal(t, "2", authorizationRevision(t, repository))

	constraint[0] = '['
	loaded, err := repository.GetGrant(context.Background(), grant.ID)
	require.NoError(t, err)
	assert.JSONEq(t, `{"equals":{"/repository":"owner/repo","/attempt":1.0}}`, string(*loaded.Constraint))

	require.NoError(t, repository.DeleteGrant(context.Background(), grant.ID))
	assert.Equal(t, "3", authorizationRevision(t, repository))
	_, err = repository.GetGrant(context.Background(), grant.ID)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.ErrorIs(t, repository.DeleteGrant(context.Background(), grant.ID), ErrNotFound)
	assert.Equal(t, "3", authorizationRevision(t, repository))
}

func TestPatchGrantDescriptionUsesItsOwnRevisionWithoutChangingPolicy(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	credential, err := repository.IssueCredential(context.Background(), principal.ID, principal.Revision)
	require.NoError(t, err)
	lease := mustAuthenticateLease(t, repository, credential.Bearer)
	defer lease.Release()
	description := "Initial description"
	grant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{
		Description: &description, PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51),
	}, allowCurrentTarget)
	require.NoError(t, err)
	assert.Equal(t, "1", grant.Revision)
	assert.Equal(t, "2", authorizationRevision(t, repository))

	updatedDescription := "Updated description"
	updatedDescriptionValue := &updatedDescription
	updated, err := repository.PatchGrant(context.Background(), grant.ID, PatchGrantRequest{
		Description: &updatedDescriptionValue, ExpectedRevision: "1",
	})
	require.NoError(t, err)
	require.NotNil(t, updated.Description)
	assert.Equal(t, updatedDescription, *updated.Description)
	assert.Equal(t, "2", updated.Revision)
	assert.Equal(t, "2", authorizationRevision(t, repository))
	assertLeaseOpen(t, lease)
	assert.Equal(t, grant.Effect, updated.Effect)
	assert.Equal(t, grant.ServerID, updated.ServerID)

	clearedDescription := (*string)(nil)
	_, err = repository.PatchGrant(context.Background(), grant.ID, PatchGrantRequest{
		Description: &clearedDescription, ExpectedRevision: "1",
	})
	assert.ErrorIs(t, err, ErrStaleRevision)
	_, err = repository.PatchGrant(context.Background(), grant.ID, PatchGrantRequest{
		Description: &updatedDescriptionValue, ExpectedRevision: "2",
	})
	assert.ErrorIs(t, err, ErrConflict)
	cleared, err := repository.PatchGrant(context.Background(), grant.ID, PatchGrantRequest{
		Description: &clearedDescription, ExpectedRevision: "2",
	})
	require.NoError(t, err)
	assert.Nil(t, cleared.Description)
	assert.Equal(t, "3", cleared.Revision)
	assert.Equal(t, "2", authorizationRevision(t, repository))
	assertLeaseOpen(t, lease)
}

func TestGrantMutationsRollBackWithAuthorizationRevisionFailure(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		principal := mustCreatePrincipal(t, repository)
		require.NoError(t, installAuthorizationRevisionFailure(t, store))
		_, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
		assert.ErrorIs(t, err, ErrStorageUnavailable)
		_, grants, statusErr := repository.Occupancy(context.Background())
		require.NoError(t, statusErr)
		assert.Equal(t, int64(1), grants.InUse)
		assert.Equal(t, "1", authorizationRevision(t, repository))
	})

	t.Run("delete", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		principal := mustCreatePrincipal(t, repository)
		grant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
		require.NoError(t, err)
		require.NoError(t, installAuthorizationRevisionFailure(t, store))
		err = repository.DeleteGrant(context.Background(), grant.ID)
		assert.ErrorIs(t, err, ErrStorageUnavailable)
		loaded, loadErr := repository.GetGrant(context.Background(), grant.ID)
		require.NoError(t, loadErr)
		assert.Equal(t, grant.ID, loaded.ID)
		assert.Equal(t, "2", authorizationRevision(t, repository))
	})
}

func TestCreateGrantSupportsPermanentServerWideAndDirectExactNames(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)

	serverWide, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"),
		PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51),
	}, allowCurrentTarget)
	require.NoError(t, err)
	assert.Nil(t, serverWide.UpstreamName)
	assert.Nil(t, serverWide.Constraint)
	assert.Nil(t, serverWide.ExpiresAt)

	exact, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"),
		PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: stringPointer("not_in_any_descriptor"),
	}, allowCurrentTarget)
	require.NoError(t, err)
	assert.Equal(t, "not_in_any_descriptor", *exact.UpstreamName)
}

func TestCreateGrantRejectsInvalidScopeConstraintExpiryPrincipalAndTarget(t *testing.T) {
	repository, _ := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	constraint := json.RawMessage(`{"equals":{"/x":1}}`)
	invalidConstraint := json.RawMessage(`{"equals":{}}`)
	now := testNow
	past := testNow.Add(-time.Nanosecond)

	tests := []struct {
		name      string
		request   CreateGrantRequest
		validator CurrentGrantTargetValidator
		wanted    error
	}{
		{name: "missing principal", request: CreateGrantRequest{Description: stringPointer("Missing principal"), PrincipalID: id(99), Effect: contract.GrantAllow, ServerID: id(51)}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "invalid principal ID", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: "bad", Effect: contract.GrantAllow, ServerID: id(51)}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "invalid effect", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantEffect("foreign"), ServerID: id(51)}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "invalid server ID", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: "bad"}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "invalid exact name", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: stringPointer("bad/name")}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "server-wide constraint", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), Constraint: &constraint}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "invalid constraint", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), UpstreamName: stringPointer("tool"), Constraint: &invalidConstraint}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "expiry equal now", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), ExpiresAt: &now}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "expiry before now", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), ExpiresAt: &past}, validator: allowCurrentTarget, wanted: ErrInvalidInput},
		{name: "missing current target", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, validator: rejectCurrentTarget, wanted: ErrInvalidInput},
		{name: "missing validator", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, wanted: ErrInvalidInput},
		{name: "target storage failure", request: CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, validator: failingCurrentTarget, wanted: ErrStorageUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := repository.CreateGrant(context.Background(), test.request, test.validator)
			assert.ErrorIs(t, err, test.wanted)
		})
	}
	assert.Equal(t, "1", authorizationRevision(t, repository))
}

func TestDeleteExpiredGrantIsAllowed(t *testing.T) {
	clock := &fixedClock{now: testNow}
	repository, _ := newRepository(t, nil)
	repository.clock = clock
	principal := mustCreatePrincipal(t, repository)
	expiresAt := testNow.Add(time.Nanosecond)
	grant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"),
		PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51), ExpiresAt: &expiresAt,
	}, allowCurrentTarget)
	require.NoError(t, err)
	clock.now = expiresAt
	loaded, err := repository.GetGrant(context.Background(), grant.ID)
	require.NoError(t, err)
	assert.Equal(t, contract.GrantExpired, loaded.State)
	require.NoError(t, repository.DeleteGrant(context.Background(), grant.ID))

	principals, grants, err := repository.Occupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(1), principals.InUse)
	assert.Equal(t, int64(1), grants.InUse, "only the ordinary default grant remains")
}

func TestGrantTargetValidationOrdersWithServerDeletion(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	entropy := make([]byte, 1024)
	for index := range entropy {
		entropy[index] = byte(index%251 + 1)
	}
	serverRepository, err := servers.New(store, &fixedClock{now: testNow}, bytes.NewReader(entropy))
	require.NoError(t, err)
	first := mustCreateS2Server(t, serverRepository, wideID(7000), "grant_first")
	second := mustCreateS2Server(t, serverRepository, wideID(7001), "delete_first")
	validator := currentServerTargets(serverRepository)

	grant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"),
		PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: first.ID,
	}, validator)
	require.NoError(t, err)
	_, err = serverRepository.Delete(context.Background(), first.ID, first.DesiredRevision)
	require.NoError(t, err)
	loaded, err := repository.GetGrant(context.Background(), grant.ID)
	require.NoError(t, err)
	assert.Equal(t, first.ID, loaded.ServerID, "later deletion does not rewrite grants")

	_, err = serverRepository.Delete(context.Background(), second.ID, second.DesiredRevision)
	require.NoError(t, err)
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"),
		PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: second.ID,
	}, validator)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestGrantListingsBindFiltersAndExcludeLaterMutations(t *testing.T) {
	repository, _ := newRepository(t, nil)
	firstPrincipal := mustCreatePrincipal(t, repository)
	secondPrincipal := mustCreatePrincipal(t, repository)
	firstGrant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: firstPrincipal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
	require.NoError(t, err)
	secondGrant, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: firstPrincipal.ID, Effect: contract.GrantDeny, ServerID: id(52)}, allowCurrentTarget)
	require.NoError(t, err)
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: secondPrincipal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
	require.NoError(t, err)

	filter := GrantFilter{PrincipalID: firstPrincipal.ID}
	page, err := repository.ListGrants(context.Background(), filter, nil, 2)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	require.NotNil(t, page.Next, "the principal default grant plus two created grants require continuation")
	assert.Equal(t, []string{firstPrincipal.ID, firstPrincipal.ID}, []string{page.Items[0].PrincipalID, page.Items[1].PrincipalID})
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: firstPrincipal.ID, Effect: contract.GrantAllow, ServerID: id(53)}, allowCurrentTarget)
	require.NoError(t, err)
	continuation, err := repository.ListGrants(context.Background(), filter, page.Next, 10)
	require.NoError(t, err)
	assert.Len(t, continuation.Items, 1)
	assert.Equal(t, secondGrant.ID, continuation.Items[0].ID)
	assert.NotEqual(t, firstGrant.ID, secondGrant.ID)
	assert.Nil(t, continuation.Next)

	_, err = repository.ListGrants(context.Background(), GrantFilter{ServerID: id(51)}, page.Next, 10)
	assert.ErrorIs(t, err, ErrStaleCursor)
}

func TestCreateGrantAcceptsNAndRejectsNPlusOneCapacity(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := 0; index < 4094; index++ {
			_, err := transaction.Exec(`INSERT INTO grants
				(id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at)
				VALUES (?, ?, 'allow', ?, 'tool', NULL, NULL, ?)`, wideID(index+1000), principal.ID, contract.SyntheticServerID, timestamp(testNow))
			if err != nil {
				return err
			}
		}
		return nil
	}))
	_, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
	require.NoError(t, err)
	_, err = repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"), PrincipalID: principal.ID, Effect: contract.GrantAllow, ServerID: id(51)}, allowCurrentTarget)
	assert.ErrorIs(t, err, ErrResourceLimit)
}

func TestGrantSchemaAndResourcesHaveNoMutationHistoryMachinery(t *testing.T) {
	repository, store := newRepository(t, nil)
	_ = repository
	var columns []string
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		rows, err := transaction.Query(`SELECT name FROM pragma_table_info('grants') ORDER BY cid`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var column string
			if err := rows.Scan(&column); err != nil {
				return err
			}
			columns = append(columns, column)
		}
		return rows.Err()
	}))
	for _, forbidden := range []string{"issuer", "reason", "revoked_at", "history", "idempotency_key"} {
		assert.NotContains(t, columns, forbidden)
	}
}

var allowCurrentTarget CurrentGrantTargetValidator = func(context.Context, *sql.Tx, string) (bool, error) { return true, nil }
var rejectCurrentTarget CurrentGrantTargetValidator = func(context.Context, *sql.Tx, string) (bool, error) { return false, nil }
var failingCurrentTarget CurrentGrantTargetValidator = func(context.Context, *sql.Tx, string) (bool, error) {
	return false, errors.New("target storage failed")
}

func currentServerTargets(repository *servers.Repository) CurrentGrantTargetValidator {
	return func(ctx context.Context, transaction *sql.Tx, serverID string) (bool, error) {
		_, err := repository.ValidateGrantTargetTx(ctx, transaction, serverID)
		if errors.Is(err, servers.ErrNotFound) {
			return false, nil
		}
		return err == nil, err
	}
}

func installAuthorizationRevisionFailure(t *testing.T, store interface {
	Mutate(context.Context, func(*sql.Tx) error) error
}) error {
	t.Helper()
	return store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`CREATE TRIGGER fail_grant_revision BEFORE UPDATE ON authorization_meta BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	})
}

func mustCreatePrincipal(t *testing.T, repository *Repository) contract.Principal {
	t.Helper()
	created, err := repository.CreatePrincipal(context.Background(), CreatePrincipalRequest{DisplayName: "Agent", Visibility: contract.VisibilityRequestable})
	require.NoError(t, err)
	return created.Principal
}

func mustCreateS2Server(t *testing.T, repository *servers.Repository, serverID, namespace string) servers.Server {
	t.Helper()
	digest := sha256.Sum256([]byte(namespace))
	created, err := repository.Create(context.Background(), servers.CreateRequest{
		ID: serverID,
		Definition: servers.Definition{Namespace: namespace, DisplayName: namespace, Enabled: false, Transport: contract.StdioTransport{
			Kind: contract.TransportStdio, Executable: "/bin/true", Arguments: []string{}, WorkingDirectory: "/tmp", Environment: map[string]string{}, SecretEnvironment: map[string]string{},
		}},
		Idempotency: &servers.IdempotencyRequest{AuthorityID: testInstallationID, Method: "POST", Route: "/api/v1/servers", Key: namespace, RequestHash: digest},
	})
	require.NoError(t, err)
	return created.Server
}
