package servers

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateGrantTargetTxDistinguishesSyntheticCurrentMissingAndDeleted(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	current := mustCreateServer(t, repository, "grant-current", false)
	deleted := mustCreateServer(t, repository, "grant-deleted", false)
	_, err := repository.Delete(context.Background(), deleted.ID, deleted.DesiredRevision)
	require.NoError(t, err)

	tests := []struct {
		name   string
		id     string
		kind   GrantTargetKind
		wanted error
	}{
		{name: "synthetic", id: contract.SyntheticServerID, kind: GrantTargetSynthetic},
		{name: "current server", id: current.ID, kind: GrantTargetServer},
		{name: "deleted server", id: deleted.ID, wanted: ErrNotFound},
		{name: "missing server", id: "01J60000000000000000000999", wanted: ErrNotFound},
		{name: "malformed server", id: "malformed", wanted: ErrNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
				kind, err := repository.ValidateGrantTargetTx(context.Background(), transaction, test.id)
				if test.wanted != nil {
					require.ErrorIs(t, err, test.wanted)
					return nil
				}
				require.NoError(t, err)
				assert.Equal(t, test.kind, kind)
				return nil
			}))
		})
	}
}

func TestLookupNamespaceTargetTxReturnsCurrentAndTombstoneFacts(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	current := mustCreateServer(t, repository, "request-current", false)
	deleted := mustCreateServer(t, repository, "request-deleted", false)
	_, err := repository.Delete(context.Background(), deleted.ID, deleted.DesiredRevision)
	require.NoError(t, err)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for _, test := range []struct {
			name      string
			namespace string
			want      NamespaceTarget
			wanted    error
		}{
			{name: "current", namespace: current.Namespace, want: NamespaceTarget{ID: current.ID, Namespace: current.Namespace, State: current.DesiredState}},
			{name: "deleted", namespace: deleted.Namespace, want: NamespaceTarget{ID: deleted.ID, Namespace: deleted.Namespace, State: contract.DesiredServerDeleted}},
			{name: "missing", namespace: "request-missing", wanted: ErrNotFound},
			{name: "synthetic", namespace: contract.SyntheticServerNamespace, wanted: ErrNotFound},
			{name: "malformed", namespace: "INVALID", wanted: ErrNotFound},
		} {
			t.Run(test.name, func(t *testing.T) {
				got, lookupErr := repository.LookupNamespaceTargetTx(context.Background(), transaction, test.namespace)
				if test.wanted != nil {
					require.ErrorIs(t, lookupErr, test.wanted)
					return
				}
				require.NoError(t, lookupErr)
				assert.Equal(t, test.want, got)
			})
		}
		return nil
	}))
	_, err = repository.LookupNamespaceTargetTx(context.Background(), nil, current.Namespace)
	require.ErrorIs(t, err, ErrStorageUnavailable)
}

func TestLookupStoredGrantNamespaceTxIncludesSyntheticCurrentAndDeleted(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	current := mustCreateServer(t, repository, "projected-current", false)
	deleted := mustCreateServer(t, repository, "projected-deleted", false)
	_, err := repository.Delete(context.Background(), deleted.ID, deleted.DesiredRevision)
	require.NoError(t, err)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		for _, test := range []struct {
			id, namespace string
			found         bool
		}{
			{id: contract.SyntheticServerID, namespace: contract.SyntheticServerNamespace, found: true},
			{id: current.ID, namespace: current.Namespace, found: true},
			{id: deleted.ID, namespace: deleted.Namespace, found: true},
			{id: "01J60000000000000000000999"},
		} {
			namespace, found, inspectErr := repository.LookupStoredGrantNamespaceTx(context.Background(), transaction, test.id)
			require.NoError(t, inspectErr)
			assert.Equal(t, test.namespace, namespace)
			assert.Equal(t, test.found, found)
		}
		return nil
	}))
	_, _, err = repository.LookupStoredGrantNamespaceTx(context.Background(), nil, current.ID)
	require.ErrorIs(t, err, ErrStorageUnavailable)
}

func TestStoredGrantTargetExistsTxAcceptsDeletedIdentityAndRejectsMissing(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	current := mustCreateServer(t, repository, "stored-current", false)
	deleted := mustCreateServer(t, repository, "stored-deleted", false)
	_, err := repository.Delete(context.Background(), deleted.ID, deleted.DesiredRevision)
	require.NoError(t, err)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		for _, test := range []struct {
			id     string
			exists bool
		}{
			{id: contract.SyntheticServerID, exists: true},
			{id: current.ID, exists: true},
			{id: deleted.ID, exists: true},
			{id: "01J60000000000000000000999", exists: false},
		} {
			exists, inspectErr := repository.StoredGrantTargetExistsTx(context.Background(), transaction, test.id)
			require.NoError(t, inspectErr)
			assert.Equal(t, test.exists, exists)
		}
		return nil
	}))
}

func TestValidateGrantTargetTxUsesCallerMutationWithoutNestedAdmission(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	server := mustCreateServer(t, repository, "grant-transaction", false)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		kind, err := repository.ValidateGrantTargetTx(context.Background(), transaction, server.ID)
		require.NoError(t, err)
		assert.Equal(t, GrantTargetServer, kind)
		return nil
	}))
}

func TestValidateGrantTargetTxMapsTransactionFailuresAndSyntheticCollision(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))

	_, err := repository.ValidateGrantTargetTx(context.Background(), nil, contract.SyntheticServerID)
	require.ErrorIs(t, err, ErrStorageUnavailable)

	var expired *sql.Tx
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		expired = transaction
		return nil
	}))
	_, err = repository.ValidateGrantTargetTx(context.Background(), expired, "01J60000000000000000000999")
	require.ErrorIs(t, err, ErrStorageUnavailable)

	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(context.Background(), `
			INSERT INTO server_identities (id, namespace, created_at)
			VALUES (?, 'synthetic_collision', ?)`, contract.SyntheticServerID, formatTime(testTime))
		return err
	}))
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		_, err := repository.ValidateGrantTargetTx(context.Background(), transaction, contract.SyntheticServerID)
		require.ErrorIs(t, err, ErrIdentityUnavailable)
		return nil
	}))
}

func TestNewIDRejectsReservedSyntheticIdentity(t *testing.T) {
	repository, _, _ := newRepositoryWithClock(t, &mutableClock{now: time.UnixMilli(0)}, bytes.NewReader(make([]byte, 10)))
	id, err := repository.NewID()
	require.ErrorIs(t, err, ErrIdentityUnavailable)
	assert.Empty(t, id)
}

func TestSyntheticIdentityCannotEnterServerRegistryOrAffectCapacityAndListing(t *testing.T) {
	repository, store, _ := newRepository(t, new(sequenceReader))
	beforeIdentities, beforeServers, err := repository.RegistryStatus(context.Background())
	require.NoError(t, err)
	beforePage, err := repository.ListServers(context.Background(), nil, 100)
	require.NoError(t, err)

	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		kind, err := repository.ValidateGrantTargetTx(context.Background(), transaction, contract.SyntheticServerID)
		require.NoError(t, err)
		assert.Equal(t, GrantTargetSynthetic, kind)
		return nil
	}))
	afterIdentities, afterServers, err := repository.RegistryStatus(context.Background())
	require.NoError(t, err)
	afterPage, err := repository.ListServers(context.Background(), nil, 100)
	require.NoError(t, err)
	assert.Equal(t, beforeIdentities, afterIdentities)
	assert.Equal(t, beforeServers, afterServers)
	assert.Equal(t, beforePage, afterPage)

	_, err = repository.Create(context.Background(), CreateRequest{
		ID: contract.SyntheticServerID,
		Definition: Definition{
			Namespace: "synthetic_explicit", DisplayName: "Synthetic", Enabled: false, Transport: testStdioTransport(),
		},
		Idempotency: idempotency("synthetic-explicit", "synthetic-explicit", ""),
	})
	assert.ErrorIs(t, err, ErrIdentityUnavailable)

	identities, servers, err := repository.RegistryStatus(context.Background())
	require.NoError(t, err)
	assert.Equal(t, beforeIdentities, identities)
	assert.Equal(t, beforeServers, servers)
}

func TestGrantTargetErrorsRemainDistinct(t *testing.T) {
	assert.False(t, errors.Is(ErrNotFound, ErrStorageUnavailable))
	assert.False(t, errors.Is(ErrIdentityUnavailable, ErrStorageUnavailable))
}
