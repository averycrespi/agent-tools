package authorization

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstraintAtomsPreserveDecodedScalarsAndLexicalNumbers(t *testing.T) {
	compiled, err := CompileConstraint([]byte(`{"equals":{"/n":1.0,"/s":"\u0061","/b":true,"/z":null}}`))
	require.NoError(t, err)
	atoms := compiled.Atoms()
	require.Len(t, atoms, 4)
	assert.Equal(t, ConstraintAtom{Pointer: "/n", Type: ConstraintNumber, Number: "1.0"}, atoms[0])
	assert.Equal(t, ConstraintAtom{Pointer: "/s", Type: ConstraintString, String: "a"}, atoms[1])
	assert.Equal(t, ConstraintAtom{Pointer: "/b", Type: ConstraintBoolean, Boolean: true}, atoms[2])
	assert.Equal(t, ConstraintAtom{Pointer: "/z", Type: ConstraintNull}, atoms[3])
	atoms[0].Pointer = "changed"
	assert.Equal(t, "/n", compiled.Atoms()[0].Pointer)
}

func TestDenyConflictTxUsesConservativeOwnerScopeAndExpiry(t *testing.T) {
	tests := []struct {
		name               string
		denyPrincipalOther bool
		denyServer         string
		denyUpstream       *string
		constraint         *json.RawMessage
		expiresAt          *time.Time
		proposedServer     string
		proposedUpstream   *string
		evaluatedAt        time.Time
		conflict           bool
	}{
		{name: "server deny overlaps exact", denyServer: id(51), proposedServer: id(51), proposedUpstream: stringPointer("echo"), evaluatedAt: testNow, conflict: true},
		{name: "exact deny overlaps server", denyServer: id(51), denyUpstream: stringPointer("echo"), proposedServer: id(51), evaluatedAt: testNow, conflict: true},
		{name: "same exact", denyServer: id(51), denyUpstream: stringPointer("echo"), proposedServer: id(51), proposedUpstream: stringPointer("echo"), evaluatedAt: testNow, conflict: true},
		{name: "different exact", denyServer: id(51), denyUpstream: stringPointer("other"), proposedServer: id(51), proposedUpstream: stringPointer("echo"), evaluatedAt: testNow},
		{name: "different server", denyServer: id(52), proposedServer: id(51), evaluatedAt: testNow},
		{name: "other principal", denyPrincipalOther: true, denyServer: id(51), proposedServer: id(51), evaluatedAt: testNow},
		{name: "expired at exact instant", denyServer: id(51), expiresAt: timePointer(testNow.Add(time.Hour)), proposedServer: id(51), evaluatedAt: testNow.Add(time.Hour)},
		{name: "future expiry", denyServer: id(51), expiresAt: timePointer(testNow.Add(time.Hour)), proposedServer: id(51), evaluatedAt: testNow, conflict: true},
		{name: "constrained false positive", denyServer: id(51), denyUpstream: stringPointer("echo"), constraint: rawConstraint(`{"equals":{"/x":false}}`), proposedServer: id(51), proposedUpstream: stringPointer("echo"), evaluatedAt: testNow, conflict: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, store := newRepository(t, nil)
			principal := mustCreatePrincipal(t, repository)
			denyPrincipal := principal.ID
			if test.denyPrincipalOther {
				denyPrincipal = mustCreatePrincipal(t, repository).ID
			}
			_, err := repository.CreateGrant(context.Background(), CreateGrantRequest{Description: stringPointer("Test grant"),
				PrincipalID: denyPrincipal, Effect: contract.GrantDeny, ServerID: test.denyServer,
				UpstreamName: test.denyUpstream, Constraint: test.constraint, ExpiresAt: test.expiresAt,
			}, allowCurrentTarget)
			require.NoError(t, err)

			require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
				conflict, inspectErr := repository.HasActiveDenyConflictTx(context.Background(), transaction, DenyConflictScope{
					PrincipalID: principal.ID, ServerID: test.proposedServer, UpstreamName: test.proposedUpstream,
				}, test.evaluatedAt)
				require.NoError(t, inspectErr)
				assert.Equal(t, test.conflict, conflict)
				var usable int
				require.NoError(t, transaction.QueryRowContext(context.Background(), `SELECT 1`).Scan(&usable))
				assert.Equal(t, 1, usable)
				return nil
			}))
		})
	}
}

func TestStoredPrincipalExistsTxUsesSuppliedTransaction(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		exists, err := repository.StoredPrincipalExistsTx(context.Background(), transaction, principal.ID)
		require.NoError(t, err)
		assert.True(t, exists)
		exists, err = repository.StoredPrincipalExistsTx(context.Background(), transaction, id(99))
		require.NoError(t, err)
		assert.False(t, exists)
		return nil
	}))
	_, err := repository.StoredPrincipalExistsTx(context.Background(), nil, principal.ID)
	assert.ErrorIs(t, err, ErrStorageUnavailable)
	_, err = repository.StoredPrincipalExistsTx(context.Background(), nil, "malformed")
	assert.ErrorIs(t, err, ErrInvalidInput)
}

func TestDenyConflictTxRejectsInvalidOrExpiredTransaction(t *testing.T) {
	repository, store := newRepository(t, nil)
	principal := mustCreatePrincipal(t, repository)
	scope := DenyConflictScope{PrincipalID: principal.ID, ServerID: id(51)}
	_, err := repository.HasActiveDenyConflictTx(context.Background(), nil, scope, testNow)
	require.ErrorIs(t, err, ErrInvalidInput)
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		_, inspectErr := repository.HasActiveDenyConflictTx(context.Background(), transaction, scope, time.Time{})
		require.ErrorIs(t, inspectErr, ErrInvalidInput)
		return nil
	}))
	var expired *sql.Tx
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		expired = transaction
		return nil
	}))
	_, err = repository.HasActiveDenyConflictTx(context.Background(), expired, scope, testNow)
	require.ErrorIs(t, err, ErrStorageUnavailable)
}

func rawConstraint(value string) *json.RawMessage {
	raw := json.RawMessage(value)
	return &raw
}
