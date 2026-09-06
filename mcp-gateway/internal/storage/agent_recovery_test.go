package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	gatewaypaths "github.com/averycrespi/agent-tools/mcp-gateway/internal/paths"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	recoveryPrincipalID  = "01J60000000000000000000011"
	recoveryCredentialID = "01J60000000000000000000012"
	recoveryTimestamp    = "2026-08-25T18:00:00.000000000Z"
)

func TestAgentCredentialCandidateMarkerIsClosedAndBounded(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
	require.NoError(t, err)
	candidate := AgentCredentialCandidate{
		PrincipalID: recoveryPrincipalID, CredentialID: recoveryCredentialID,
		PrincipalRevision: math.MaxInt64 - 1, CredentialRevision: math.MaxInt64 - 1,
	}
	err = store.MutateAgentCredentialCandidate(ctx, candidate, func(*sql.Tx) error { return nil })
	assert.ErrorIs(t, err, ErrStorageLatched)
	require.NoError(t, store.Close())

	contents, err := os.ReadFile(ownership.Layout().MutationMarker)
	require.NoError(t, err)
	assert.LessOrEqual(t, len(contents), markerBytesMaximum)
	var document markerDocument
	require.NoError(t, json.Unmarshal(contents, &document))
	require.NotNil(t, document.Recovery)
	assert.Equal(t, "invalidate_agent_credential_candidate", document.Recovery.Action)
	assert.Equal(t, recoveryPrincipalID, document.Recovery.PrincipalID)
	assert.Equal(t, recoveryCredentialID, document.Recovery.CredentialID)
	assert.Equal(t, int64(math.MaxInt64-1), document.Recovery.PrincipalRevision)
	assert.Equal(t, int64(math.MaxInt64-1), document.Recovery.CredentialRevision)
	assert.Empty(t, document.Recovery.Owner)
	assert.Empty(t, document.Recovery.Kind)
}

func TestAgentCredentialCandidateRejectsInvalidPayloadBeforeMutation(t *testing.T) {
	tests := []AgentCredentialCandidate{
		{},
		{PrincipalID: "bad", CredentialID: recoveryCredentialID, PrincipalRevision: 2, CredentialRevision: 1},
		{PrincipalID: recoveryPrincipalID, CredentialID: "bad", PrincipalRevision: 2, CredentialRevision: 1},
		{PrincipalID: recoveryPrincipalID, CredentialID: recoveryCredentialID, PrincipalRevision: 0, CredentialRevision: 1},
		{PrincipalID: recoveryPrincipalID, CredentialID: recoveryCredentialID, PrincipalRevision: 2, CredentialRevision: 0},
		{PrincipalID: recoveryPrincipalID, CredentialID: recoveryCredentialID, PrincipalRevision: math.MaxInt64, CredentialRevision: 1},
	}
	for _, candidate := range tests {
		ownership := newOwnership(t)
		store, err := Initialize(context.Background(), ownership, testInstallationID)
		require.NoError(t, err)
		called := false
		err = store.MutateAgentCredentialCandidate(context.Background(), candidate, func(*sql.Tx) error {
			called = true
			return nil
		})
		assert.Error(t, err)
		assert.False(t, called)
		assert.False(t, store.Latched())
		require.NoError(t, store.Close())
		require.NoError(t, ownership.Close())
	}
}

func TestAgentCredentialStoppedRecoveryInvalidatesOnlyExactCurrentCandidate(t *testing.T) {
	tests := []struct {
		name            string
		candidate       AgentCredentialCandidate
		insertPrincipal bool
		principalRev    int64
		credentialRev   int64
		credentialID    string
		wantPrincipal   int64
		wantCredential  int64
		wantID          *string
	}{
		{name: "current", candidate: recoveryCandidate(), insertPrincipal: true, principalRev: 2, credentialRev: 1, credentialID: recoveryCredentialID, wantPrincipal: 3, wantCredential: 2},
		{name: "stale revisions", candidate: recoveryCandidate(), insertPrincipal: true, principalRev: 3, credentialRev: 2, credentialID: recoveryCredentialID, wantPrincipal: 3, wantCredential: 2, wantID: stringValue(recoveryCredentialID)},
		{name: "different credential", candidate: recoveryCandidate(), insertPrincipal: true, principalRev: 2, credentialRev: 1, credentialID: "01J60000000000000000000013", wantPrincipal: 2, wantCredential: 1, wantID: stringValue("01J60000000000000000000013")},
		{name: "missing principal", candidate: recoveryCandidate()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			ownership := newOwnership(t)
			store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
			require.NoError(t, err)
			err = store.MutateAgentCredentialCandidate(ctx, test.candidate, func(transaction *sql.Tx) error {
				if !test.insertPrincipal {
					return nil
				}
				return insertRecoveryPrincipal(ctx, transaction, test.principalRev, test.credentialRev, test.credentialID)
			})
			assert.ErrorIs(t, err, ErrStorageLatched)
			require.NoError(t, store.Close())
			root := ownership.Layout().Root
			require.NoError(t, ownership.Close())

			_, err = VerifyCurrent(ctx, root)
			require.NoError(t, err)
			events := readStoppedAudit(t, root, contract.AuditFilters{Category: "agent_credential", Action: "invalidate"})
			if test.name == "current" {
				require.Len(t, events, 2)
				for _, event := range events {
					assert.Equal(t, contract.AuditOffline, event.Actor.Type)
					assert.Nil(t, event.Initiator)
					assert.Equal(t, recoveryCredentialID, event.Target.ID)
				}
			} else {
				assert.Empty(t, events)
			}
			if !test.insertPrincipal {
				return
			}
			principalRevision, credentialRevision, credentialID := readRecoveryPrincipal(t, root)
			assert.Equal(t, test.wantPrincipal, principalRevision)
			assert.Equal(t, test.wantCredential, credentialRevision)
			if test.wantID == nil {
				assert.Nil(t, credentialID)
			} else {
				require.NotNil(t, credentialID)
				assert.Equal(t, *test.wantID, *credentialID)
			}
		})
	}
}

func TestAgentCredentialRecoverySurvivesEveryPostCommitClearFault(t *testing.T) {
	for _, point := range []FaultPoint{
		FaultAfterCommit,
		FaultDisarmRename,
		FaultDisarmDirectorySync,
		FaultDisarmDelete,
		FaultDisarmFinalDirectorySync,
	} {
		t.Run(string(point), func(t *testing.T) {
			ctx := context.Background()
			ownership := newOwnership(t)
			store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{fault: failOnce(point)})
			require.NoError(t, err)
			err = store.MutateAgentCredentialCandidate(ctx, recoveryCandidate(), func(transaction *sql.Tx) error {
				return insertRecoveryPrincipal(ctx, transaction, 2, 1, recoveryCredentialID)
			})
			assert.ErrorIs(t, err, ErrStorageLatched)
			require.NoError(t, store.Close())
			root := ownership.Layout().Root
			require.NoError(t, ownership.Close())

			_, err = VerifyCurrent(ctx, root)
			require.NoError(t, err)
			principalRevision, credentialRevision, credentialID := readRecoveryPrincipal(t, root)
			assert.Equal(t, int64(3), principalRevision)
			assert.Equal(t, int64(2), credentialRevision)
			assert.Nil(t, credentialID)
		})
	}
}

func TestAgentCredentialRecoveryFailurePreservesMarker(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := initializeWithOptions(ctx, ownership, testInstallationID, testOptions{fault: failOnce(FaultAfterCommit)})
	require.NoError(t, err)
	err = store.MutateAgentCredentialCandidate(ctx, recoveryCandidate(), func(transaction *sql.Tx) error {
		if err := insertRecoveryPrincipal(ctx, transaction, 2, 1, recoveryCredentialID); err != nil {
			return err
		}
		_, err := transaction.Exec(`CREATE TRIGGER fail_agent_recovery BEFORE UPDATE OF credential_id ON principals BEGIN SELECT RAISE(ABORT, 'injected'); END`)
		return err
	})
	assert.ErrorIs(t, err, ErrStorageLatched)
	require.NoError(t, store.Close())
	root := ownership.Layout().Root
	markerPath := ownership.Layout().MutationMarker
	require.NoError(t, ownership.Close())

	_, err = VerifyCurrent(ctx, root)
	assert.ErrorIs(t, err, ErrStorageLatched)
	_, markerErr := os.Lstat(markerPath)
	assert.NoError(t, markerErr)
}

func TestAgentCredentialStoppedCleanupIsIdempotent(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Mutate(ctx, func(transaction *sql.Tx) error {
		return insertRecoveryPrincipal(ctx, transaction, 2, 1, recoveryCredentialID)
	}))
	recovery := recoveryActionFromCandidate(recoveryCandidate())
	require.NoError(t, invalidateAgentCredentialCandidate(ctx, store, &recovery))
	require.NoError(t, invalidateAgentCredentialCandidate(ctx, store, &recovery))
	var principalRevision, credentialRevision int64
	var credentialID sql.NullString
	require.NoError(t, store.View(ctx, func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT revision, credential_revision, credential_id FROM principals WHERE id = ?`, recoveryPrincipalID).Scan(&principalRevision, &credentialRevision, &credentialID)
	}))
	assert.Equal(t, int64(3), principalRevision)
	assert.Equal(t, int64(2), credentialRevision)
	assert.False(t, credentialID.Valid)
	require.NoError(t, store.Close())
}

func TestRecoveryMarkersRejectUnknownMixedAndDisagreeingActions(t *testing.T) {
	ctx := context.Background()
	ownership := newOwnership(t)
	store, err := Initialize(ctx, ownership, testInstallationID)
	require.NoError(t, err)
	require.NoError(t, store.Close())
	marker := newMutationMarker(ownership.Layout(), nil)
	base := recoveryActionFromCandidate(recoveryCandidate())

	t.Run("oversize", func(t *testing.T) {
		require.NoError(t, os.WriteFile(marker.intent, bytes.Repeat([]byte{'x'}, markerBytesMaximum+1), 0o600))
		_, err := marker.recovery(testInstallationID)
		assert.Error(t, err)
		require.NoError(t, os.Remove(marker.intent))
	})
	t.Run("foreign installation", func(t *testing.T) {
		contents, err := json.Marshal(markerDocument{InstallationID: "01J60000000000000000000099", State: "armed", Recovery: &base})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(marker.intent, contents, 0o600))
		recovery, err := marker.recovery(testInstallationID)
		require.NoError(t, err)
		assert.Nil(t, recovery)
		assert.Error(t, marker.clearVerified(testInstallationID))
		require.NoError(t, os.Remove(marker.intent))
	})
	t.Run("malformed recovery tombstone", func(t *testing.T) {
		require.NoError(t, os.WriteFile(marker.tombstone, []byte("{"), 0o600))
		_, err := marker.recovery(testInstallationID)
		assert.Error(t, err)
		require.NoError(t, os.Remove(marker.tombstone))
	})
	t.Run("unknown action", func(t *testing.T) {
		writeRecoveryMarker(t, marker.intent, recoveryAction{Action: "unknown"})
		_, err := marker.recovery(testInstallationID)
		assert.Error(t, err)
		require.NoError(t, os.Remove(marker.intent))
	})
	t.Run("mixed action", func(t *testing.T) {
		mixed := base
		mixed.Owner = testInstallationID
		mixed.Kind = "oauth_tokens"
		writeRecoveryMarker(t, marker.intent, mixed)
		_, err := marker.recovery(testInstallationID)
		assert.Error(t, err)
		require.NoError(t, os.Remove(marker.intent))
	})
	t.Run("duplicate member", func(t *testing.T) {
		contents := []byte(`{"installation_id":"` + testInstallationID + `","state":"armed","recovery":{"action":"invalidate_agent_credential_candidate","action":"invalidate_agent_credential_candidate","principal_id":"` + recoveryPrincipalID + `","credential_id":"` + recoveryCredentialID + `","principal_revision":2,"credential_revision":1}}`)
		require.NoError(t, os.WriteFile(marker.intent, contents, 0o600))
		_, err := marker.recovery(testInstallationID)
		assert.Error(t, err)
		require.NoError(t, os.Remove(marker.intent))
	})
	t.Run("unknown member", func(t *testing.T) {
		contents := []byte(`{"installation_id":"` + testInstallationID + `","state":"armed","recovery":{"action":"invalidate_agent_credential_candidate","principal_id":"` + recoveryPrincipalID + `","credential_id":"` + recoveryCredentialID + `","principal_revision":2,"credential_revision":1,"extra":true}}`)
		require.NoError(t, os.WriteFile(marker.intent, contents, 0o600))
		_, err := marker.recovery(testInstallationID)
		assert.Error(t, err)
		require.NoError(t, os.Remove(marker.intent))
	})
	t.Run("disagreement", func(t *testing.T) {
		writeRecoveryMarker(t, marker.intent, base)
		other := base
		other.CredentialID = "01J60000000000000000000013"
		writeRecoveryMarker(t, marker.tombstone, other)
		_, err := marker.recovery(testInstallationID)
		assert.Error(t, err)
		require.NoError(t, os.Remove(marker.intent))
		require.NoError(t, os.Remove(marker.tombstone))
	})
	require.NoError(t, ownership.Close())
}

func recoveryCandidate() AgentCredentialCandidate {
	return AgentCredentialCandidate{PrincipalID: recoveryPrincipalID, CredentialID: recoveryCredentialID, PrincipalRevision: 2, CredentialRevision: 1}
}

func insertRecoveryPrincipal(ctx context.Context, transaction *sql.Tx, principalRevision, credentialRevision int64, credentialID string) error {
	_, err := transaction.ExecContext(ctx, `INSERT INTO principals (
		id, display_name, state, visibility, revision, credential_revision,
		credential_id, credential_verifier, credential_fingerprint, credential_created_at,
		created_at, updated_at
	) VALUES (?, 'Agent', 'active', 'requestable', ?, ?, ?, ?, '0123456789abcdef', ?, ?, ?)`,
		recoveryPrincipalID, principalRevision, credentialRevision, credentialID, make([]byte, 32), recoveryTimestamp, recoveryTimestamp, recoveryTimestamp)
	return err
}

func readRecoveryPrincipal(t *testing.T, root string) (int64, int64, *string) {
	t.Helper()
	ownership, err := gatewaypaths.Acquire(root)
	require.NoError(t, err)
	defer func() { require.NoError(t, ownership.Close()) }()
	store, err := Open(context.Background(), ownership)
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var principalRevision, credentialRevision int64
	var credentialID sql.NullString
	require.NoError(t, store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRow(`SELECT revision, credential_revision, credential_id FROM principals WHERE id = ?`, recoveryPrincipalID).Scan(&principalRevision, &credentialRevision, &credentialID)
	}))
	if !credentialID.Valid {
		return principalRevision, credentialRevision, nil
	}
	return principalRevision, credentialRevision, &credentialID.String
}

func writeRecoveryMarker(t *testing.T, path string, recovery recoveryAction) {
	t.Helper()
	contents, err := json.Marshal(markerDocument{InstallationID: testInstallationID, State: "armed", Recovery: &recovery})
	require.NoError(t, err)
	require.LessOrEqual(t, len(contents), markerBytesMaximum)
	require.NoError(t, os.WriteFile(path, contents, 0o600))
}

func stringValue(value string) *string { return &value }
