package authorization

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testInstallationID = "01ARZ3NDEKTSV4RRFFQ69G5FAV"

var testNow = time.Date(2026, 8, 25, 18, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (clock *fixedClock) Now() time.Time { return clock.now }

func TestRepositoryReadsSyntheticRevisionOccupancyAndSafeResources(t *testing.T) {
	repository, store := newRepository(t, nil)
	emptyPrincipals, err := repository.ListPrincipals(context.Background(), nil, 10)
	require.NoError(t, err)
	assert.Empty(t, emptyPrincipals.Items)
	assert.Nil(t, emptyPrincipals.Next)
	emptyGrants, err := repository.ListGrants(context.Background(), GrantFilter{}, nil, 10)
	require.NoError(t, err)
	assert.Empty(t, emptyGrants.Items)
	assert.Nil(t, emptyGrants.Next)

	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Agent One", credential: true})
	seedPrincipal(t, store, principalRow{id: id(2), displayName: "Agent Two", state: contract.PrincipalDisabled, visibility: contract.VisibilityAll})
	seedGrant(t, store, grantRow{id: id(11), principalID: id(1), serverID: contract.SyntheticServerID, constraint: `{"equals":{"/n":1.0}}`})

	synthetic, err := repository.SyntheticIdentity(context.Background())
	require.NoError(t, err)
	assert.Equal(t, SyntheticIdentity{ServerID: contract.SyntheticServerID, Namespace: contract.SyntheticServerNamespace}, synthetic)
	revision, err := repository.AuthorizationRevision(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "0", revision)
	principals, grants, err := repository.Occupancy(context.Background())
	require.NoError(t, err)
	assert.Equal(t, contract.LimitStatus{InUse: 2, Limit: 128, Saturated: false}, principals)
	assert.Equal(t, contract.LimitStatus{InUse: 1, Limit: 4096, Saturated: false}, grants)

	principal, err := repository.GetPrincipal(context.Background(), id(1))
	require.NoError(t, err)
	assert.Equal(t, "Agent One", principal.DisplayName)
	require.NotNil(t, principal.Credential)
	assert.Equal(t, id(101), principal.Credential.ID)
	assert.Equal(t, "0123456789abcdef", principal.Credential.Fingerprint)
	assert.Equal(t, "4", principal.Credential.Revision)
	_, err = repository.GetPrincipal(context.Background(), id(99))
	assert.ErrorIs(t, err, ErrNotFound)

	grant, err := repository.GetGrant(context.Background(), id(11))
	require.NoError(t, err)
	assert.Equal(t, contract.GrantActive, grant.State)
	require.NotNil(t, grant.Constraint)
	assert.JSONEq(t, `{"equals":{"/n":1.0}}`, string(*grant.Constraint))
	_, err = repository.GetGrant(context.Background(), id(99))
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestPrincipalListingPinsInsertionWatermark(t *testing.T) {
	repository, store := newRepository(t, nil)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "One"})
	seedPrincipal(t, store, principalRow{id: id(2), displayName: "Two"})

	first, err := repository.ListPrincipals(context.Background(), nil, 1)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotNil(t, first.Next)
	assert.Equal(t, id(1), first.Items[0].ID)
	seedPrincipal(t, store, principalRow{id: id(3), displayName: "Later"})

	second, err := repository.ListPrincipals(context.Background(), first.Next, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{id(2)}, principalIDs(second.Items))
	assert.Nil(t, second.Next)

	_, err = repository.ListPrincipals(context.Background(), &SnapshotCursor{Collection: grantCollection, Upper: 2}, 1)
	assert.ErrorIs(t, err, ErrStaleCursor)
	_, err = repository.ListPrincipals(context.Background(), nil, 0)
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestGrantListingPinsFiltersWatermarkAndProjectsExpiryLazily(t *testing.T) {
	repository, store := newRepository(t, nil)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "One"})
	seedPrincipal(t, store, principalRow{id: id(2), displayName: "Two"})
	past := testNow.Add(-time.Nanosecond)
	future := testNow.Add(time.Nanosecond)
	seedGrant(t, store, grantRow{id: id(11), principalID: id(1), serverID: id(51), expiresAt: &past})
	seedGrant(t, store, grantRow{id: id(12), principalID: id(1), serverID: id(52), expiresAt: &future})
	seedGrant(t, store, grantRow{id: id(13), principalID: id(2), serverID: id(51)})

	filter := GrantFilter{PrincipalID: id(1)}
	first, err := repository.ListGrants(context.Background(), filter, nil, 1)
	require.NoError(t, err)
	require.Len(t, first.Items, 1)
	require.NotNil(t, first.Next)
	assert.Equal(t, contract.GrantExpired, first.Items[0].State)
	seedGrant(t, store, grantRow{id: id(14), principalID: id(1), serverID: id(51)})

	second, err := repository.ListGrants(context.Background(), filter, first.Next, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{id(12)}, grantIDs(second.Items))
	assert.Equal(t, contract.GrantActive, second.Items[0].State)
	assert.Nil(t, second.Next)

	serverPage, err := repository.ListGrants(context.Background(), GrantFilter{ServerID: id(51)}, nil, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{id(11), id(13), id(14)}, grantIDs(serverPage.Items))
	_, err = repository.ListGrants(context.Background(), GrantFilter{PrincipalID: id(2)}, first.Next, 10)
	assert.ErrorIs(t, err, ErrStaleCursor)
}

func TestRepositoryRejectsLatchedStorageAndMapsStorageFailure(t *testing.T) {
	armed := false
	repository, store := newRepository(t, func(point storage.FaultPoint) error {
		if armed && point == storage.FaultAfterCommit {
			return errors.New("commit acknowledgement lost")
		}
		return nil
	})
	armed = true
	err := store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.Exec(`UPDATE authorization_meta SET revision = revision`)
		return err
	})
	require.Error(t, err)
	require.True(t, store.Latched())

	_, err = repository.AuthorizationRevision(context.Background())
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	_, _, err = repository.Occupancy(context.Background())
	assert.ErrorIs(t, err, ErrStorageUnavailable)
}

func TestRepositoryMapsClosedStorageFailure(t *testing.T) {
	repository, store := newRepository(t, nil)
	require.NoError(t, store.Close())
	_, err := repository.AuthorizationRevision(context.Background())
	assert.ErrorIs(t, err, ErrStorageUnavailable)
}

func TestRepositoryDependenciesAndErrorsAreDistinct(t *testing.T) {
	_, err := New(nil, &fixedClock{now: testNow}, bytes.NewReader(make([]byte, 64)))
	require.Error(t, err)
	_, err = New(&storage.Store{}, nil, bytes.NewReader(make([]byte, 64)))
	require.Error(t, err)
	_, err = New(&storage.Store{}, &fixedClock{now: testNow}, nil)
	require.Error(t, err)
	assert.False(t, errors.Is(ErrNotFound, ErrStaleCursor))
	assert.False(t, errors.Is(ErrInvalidInput, ErrStorageUnavailable))
}

type principalRow struct {
	id          string
	displayName string
	state       contract.PrincipalState
	visibility  contract.PrincipalVisibility
	credential  bool
}

func seedPrincipal(t *testing.T, store *storage.Store, row principalRow) {
	t.Helper()
	if row.state == "" {
		row.state = contract.PrincipalActive
	}
	if row.visibility == "" {
		row.visibility = contract.VisibilityRequestable
	}
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		if row.credential {
			_, err := transaction.Exec(`INSERT INTO principals
				(id, display_name, state, visibility, revision, credential_revision, credential_id, credential_verifier, credential_fingerprint, credential_created_at, created_at, updated_at)
				VALUES (?, ?, ?, ?, 3, 4, ?, ?, '0123456789abcdef', ?, ?, ?)`,
				row.id, row.displayName, row.state, row.visibility, id(101), bytes.Repeat([]byte{1}, 32), timestamp(testNow.Add(-time.Hour)), timestamp(testNow.Add(-2*time.Hour)), timestamp(testNow))
			return err
		}
		_, err := transaction.Exec(`INSERT INTO principals
			(id, display_name, state, visibility, revision, credential_revision, created_at, updated_at)
			VALUES (?, ?, ?, ?, 1, 0, ?, ?)`, row.id, row.displayName, row.state, row.visibility, timestamp(testNow.Add(-2*time.Hour)), timestamp(testNow))
		return err
	}))
}

type grantRow struct {
	id          string
	principalID string
	serverID    string
	constraint  string
	expiresAt   *time.Time
}

func seedGrant(t *testing.T, store *storage.Store, row grantRow) {
	t.Helper()
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		var constraint any
		if row.constraint != "" {
			constraint = row.constraint
		}
		var expires any
		if row.expiresAt != nil {
			expires = timestamp(*row.expiresAt)
		}
		_, err := transaction.Exec(`INSERT INTO grants
			(id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at)
			VALUES (?, ?, 'allow', ?, 'tool', ?, ?, ?)`, row.id, row.principalID, row.serverID, constraint, expires, timestamp(testNow.Add(-time.Hour)))
		return err
	}))
}

func newRepository(t *testing.T, fault func(storage.FaultPoint) error) (*Repository, *storage.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	var store *storage.Store
	if fault == nil {
		store, err = storage.Initialize(context.Background(), ownership, testInstallationID)
	} else {
		store, err = storage.InitializeWithFaultInjection(context.Background(), ownership, testInstallationID, fault)
	}
	require.NoError(t, err)
	repository, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(make([]byte, 1024)))
	require.NoError(t, err)
	t.Cleanup(func() {
		if !store.Latched() {
			_ = store.Close()
		}
		_ = ownership.Close()
	})
	return repository, store
}

func id(sequence int) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	encoded := []byte("01J60000000000000000000000")
	encoded[24] = alphabet[(sequence/32)%32]
	encoded[25] = alphabet[sequence%32]
	return string(encoded)
}

func timestamp(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
}

func principalIDs(items []contract.Principal) []string {
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].ID
	}
	return ids
}

func grantIDs(items []contract.Grant) []string {
	ids := make([]string, len(items))
	for index := range items {
		ids[index] = items[index].ID
	}
	return ids
}
