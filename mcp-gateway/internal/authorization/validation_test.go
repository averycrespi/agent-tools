package authorization

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
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

type targetInspector struct {
	missing map[string]bool
	err     error
}

func (inspector targetInspector) StoredGrantTargetExistsTx(_ context.Context, _ *sql.Tx, serverID string) (bool, error) {
	if inspector.err != nil {
		return false, inspector.err
	}
	return !inspector.missing[serverID], nil
}

func TestValidateStartupAcceptsEmptyAndValidPopulatedAuthority(t *testing.T) {
	repository, store := newRepository(t, nil)
	require.NoError(t, repository.ValidateStartup(context.Background(), targetInspector{}))

	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Valid Agent", credential: true})
	seedGrant(t, store, grantRow{id: id(11), principalID: id(1), serverID: id(51), constraint: `{"equals":{"/n":1.0}}`, expiresAt: timePointer(testNow.Add(time.Hour))})
	require.NoError(t, repository.ValidateStartup(context.Background(), targetInspector{}))
}

func TestValidateStartupRejectsMalformedDurableRows(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*sql.Tx) error
		inspector targetInspector
	}{
		{name: "missing synthetic singleton", mutate: execMutation(`DELETE FROM synthetic_server_identity`)},
		{name: "duplicate synthetic singleton", mutate: uncheckedMutation(`INSERT INTO synthetic_server_identity (singleton, server_id, namespace) VALUES (2, '01J60000000000000000000099', 'foreign')`)},
		{name: "wrong synthetic identity", mutate: uncheckedMutation(`UPDATE synthetic_server_identity SET server_id = '01J60000000000000000000099'`)},
		{name: "synthetic S2 collision", mutate: noMutation, inspector: targetInspector{missing: map[string]bool{contract.SyntheticServerID: true}}},
		{name: "missing authorization singleton", mutate: execMutation(`DELETE FROM authorization_meta`)},
		{name: "negative authorization revision", mutate: uncheckedMutation(`UPDATE authorization_meta SET revision = -1`)},
		{name: "invalid principal state", mutate: uncheckedMutation(`UPDATE principals SET state = 'foreign'`)},
		{name: "invalid principal visibility", mutate: uncheckedMutation(`UPDATE principals SET visibility = 'foreign'`)},
		{name: "invalid principal revision", mutate: uncheckedMutation(`UPDATE principals SET revision = 0`)},
		{name: "noncanonical principal timestamp", mutate: execMutation(`UPDATE principals SET updated_at = '2026-08-25T18:00:00Z'`)},
		{name: "principal timestamp reversal", mutate: execMutation(`UPDATE principals SET updated_at = '2020-01-01T00:00:00.000000000Z'`)},
		{name: "partial credential slot", mutate: uncheckedMutation(`UPDATE principals SET credential_verifier = NULL`)},
		{name: "disabled current credential", mutate: uncheckedMutation(`UPDATE principals SET state = 'disabled'`)},
		{name: "invalid credential fingerprint", mutate: uncheckedMutation(`UPDATE principals SET credential_fingerprint = 'ABCDEF0123456789'`)},
		{name: "invalid grant effect", mutate: uncheckedMutation(`UPDATE grants SET effect = 'foreign'`)},
		{name: "invalid grant target ID", mutate: uncheckedMutation(`UPDATE grants SET server_id = 'malformed'`)},
		{name: "missing grant target", mutate: noMutation, inspector: targetInspector{missing: map[string]bool{id(51): true}}},
		{name: "server-wide constraint", mutate: uncheckedMutation(`UPDATE grants SET upstream_name = NULL`)},
		{name: "nonobject constraint", mutate: uncheckedMutation(`UPDATE grants SET constraint_json = '[]'`)},
		{name: "duplicate constraint member", mutate: uncheckedMutation(`UPDATE grants SET constraint_json = '{"equals":{},"equals":{}}'`)},
		{name: "invalid upstream name", mutate: uncheckedMutation(`UPDATE grants SET upstream_name = 'bad/name'`)},
		{name: "noncanonical grant timestamp", mutate: execMutation(`UPDATE grants SET created_at = '2026-08-25T17:00:00Z'`)},
		{name: "expiry not after creation", mutate: execMutation(`UPDATE grants SET expires_at = created_at`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, store := newRepository(t, nil)
			seedPrincipal(t, store, principalRow{id: id(1), displayName: "Valid Agent", credential: true})
			seedGrant(t, store, grantRow{id: id(11), principalID: id(1), serverID: id(51), constraint: `{"equals":{"/n":1.0}}`, expiresAt: timePointer(testNow.Add(time.Hour))})
			require.NoError(t, store.Mutate(context.Background(), test.mutate))

			err := repository.ValidateStartup(context.Background(), test.inspector)
			assert.ErrorIs(t, err, ErrInvalidState)
		})
	}
}

func TestGrantReadsRejectInvalidLoadedConstraint(t *testing.T) {
	repository, store := newRepository(t, nil)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Agent"})
	seedGrant(t, store, grantRow{id: id(11), principalID: id(1), serverID: id(51), constraint: `{"equals":{"/x":1.0}}`})
	require.NoError(t, store.Mutate(context.Background(), uncheckedMutation(`UPDATE grants SET constraint_json = '{"other":{}}'`)))

	_, err := repository.GetGrant(context.Background(), id(11))
	assert.ErrorIs(t, err, ErrInvalidState)
	_, err = repository.ListGrants(context.Background(), GrantFilter{}, nil, 10)
	assert.ErrorIs(t, err, ErrInvalidState)
}

func TestValidateStartupRejectsOrphanGrant(t *testing.T) {
	root := filepath.Join(t.TempDir(), "gateway")
	require.NoError(t, os.Mkdir(root, 0o700))
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	store, err := storage.Initialize(context.Background(), ownership, testInstallationID)
	require.NoError(t, err)
	seedPrincipal(t, store, principalRow{id: id(1), displayName: "Agent"})
	seedGrant(t, store, grantRow{id: id(11), principalID: id(1), serverID: contract.SyntheticServerID})
	require.NoError(t, store.Close())

	database, err := sql.Open("sqlite3", "file:"+ownership.Layout().Database+"?_pragma=busy_timeout(2000)")
	require.NoError(t, err)
	require.NoError(t, database.Ping())
	_, err = database.Exec(`PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	_, err = database.Exec(`UPDATE grants SET principal_id = ?`, id(2))
	require.NoError(t, err)
	require.NoError(t, database.Close())

	store, err = storage.Open(context.Background(), ownership)
	require.NoError(t, err)
	repository, err := New(store, &fixedClock{now: testNow}, bytes.NewReader(make([]byte, 64)))
	require.NoError(t, err)
	assert.ErrorIs(t, repository.ValidateStartup(context.Background(), targetInspector{}), ErrInvalidState)
	require.NoError(t, store.Close())
	require.NoError(t, ownership.Close())
}

func TestValidateStartupRejectsCapacityOverflow(t *testing.T) {
	t.Run("principals", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			for index := 0; index < 129; index++ {
				if _, err := transaction.Exec(`INSERT INTO principals
					(id, display_name, state, visibility, revision, credential_revision, created_at, updated_at)
					VALUES (?, 'Agent', 'active', 'requestable', 1, 0, ?, ?)`, wideID(index), timestamp(testNow), timestamp(testNow)); err != nil {
					return err
				}
			}
			return nil
		}))
		assert.ErrorIs(t, repository.ValidateStartup(context.Background(), targetInspector{}), ErrInvalidState)
	})

	t.Run("grants", func(t *testing.T) {
		repository, store := newRepository(t, nil)
		seedPrincipal(t, store, principalRow{id: id(1), displayName: "Agent"})
		require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
			for index := 0; index < 4097; index++ {
				if _, err := transaction.Exec(`INSERT INTO grants
					(id, principal_id, effect, server_id, upstream_name, constraint_json, expires_at, created_at)
					VALUES (?, ?, 'allow', ?, 'tool', NULL, NULL, ?)`, wideID(index+1000), id(1), contract.SyntheticServerID, timestamp(testNow)); err != nil {
					return err
				}
			}
			return nil
		}))
		assert.ErrorIs(t, repository.ValidateStartup(context.Background(), targetInspector{}), ErrInvalidState)
	})
}

func TestValidateStartupMapsTargetInspectionFailureAndRequiresInspector(t *testing.T) {
	repository, _ := newRepository(t, nil)
	assert.ErrorIs(t, repository.ValidateStartup(context.Background(), nil), ErrInvalidState)
	err := repository.ValidateStartup(context.Background(), targetInspector{err: errors.New("S2 unavailable")})
	assert.ErrorIs(t, err, ErrStorageUnavailable)
}

func execMutation(statement string) func(*sql.Tx) error {
	return func(transaction *sql.Tx) error {
		_, err := transaction.Exec(statement)
		return err
	}
}

func uncheckedMutation(statement string) func(*sql.Tx) error {
	return func(transaction *sql.Tx) error {
		if _, err := transaction.Exec(`PRAGMA ignore_check_constraints = ON`); err != nil {
			return err
		}
		_, err := transaction.Exec(statement)
		return err
	}
}

func noMutation(*sql.Tx) error { return nil }

func timePointer(value time.Time) *time.Time { return &value }

func wideID(sequence int) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	encoded := []byte("01J60000000000000000000000")
	for index := len(encoded) - 1; index >= len(encoded)-5; index-- {
		encoded[index] = alphabet[sequence%32]
		sequence /= 32
	}
	return string(encoded)
}

func TestWideIDFixtureIsUniqueAndValid(t *testing.T) {
	seen := make(map[string]struct{})
	for index := 0; index < 5000; index++ {
		value := wideID(index)
		require.Len(t, value, 26)
		require.True(t, validOpaqueID(value), fmt.Sprintf("index %d", index))
		_, duplicate := seen[value]
		require.False(t, duplicate)
		seen[value] = struct{}{}
	}
}
