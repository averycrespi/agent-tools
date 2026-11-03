package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	recoveryPrincipalID  = "01J60000000000000000000011"
	recoveryCredentialID = "01J60000000000000000000012"
	recoveryTimestamp    = "2026-08-25T18:00:00.000000000Z"
)

func recoveryCandidate() AgentCredentialCandidate {
	return AgentCredentialCandidate{PrincipalID: recoveryPrincipalID, CredentialID: recoveryCredentialID, PrincipalRevision: 2, CredentialRevision: 1}
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
		owner := newOwnership(t)
		store, err := Initialize(t.Context(), owner, testInstallationID)
		require.NoError(t, err)
		called := false
		require.Error(t, store.MutateAgentCredentialCandidate(t.Context(), candidate, func(*sql.Tx) error { called = true; return nil }))
		require.False(t, called)
		require.False(t, store.Latched())
		require.NoError(t, store.Close())
	}
}

func TestHistoricalEvidenceRetainsCommittedCredentialWithoutCompensation(t *testing.T) {
	candidate := recoveryActionFromCandidate(recoveryCandidate())
	for _, action := range []*recoveryAction{nil, &candidate, {Action: "restore_keyring_authority_fence", Owner: recoveryPrincipalID, Kind: "oauth_tokens"}} {
		t.Run(func() string {
			if action == nil {
				return "generic"
			}
			return action.Action
		}(), func(t *testing.T) {
			owner := newOwnership(t)
			store, err := Initialize(t.Context(), owner, testInstallationID)
			require.NoError(t, err)
			require.NoError(t, store.Mutate(t.Context(), func(tx *sql.Tx) error { return insertRecoveryPrincipal(t.Context(), tx, 2, 1, recoveryCredentialID) }))
			require.NoError(t, store.Close())
			marker := newMutationMarker(owner.Layout(), nil)
			data, err := json.Marshal(markerDocument{InstallationID: testInstallationID, State: "armed", Recovery: action})
			require.NoError(t, err)
			// Both publication sides carry the same recognized historical decision.
			for _, path := range []string{marker.intent, marker.tombstone} {
				require.NoError(t, os.WriteFile(path, data, 0600))
			}
			for range 2 {
				reopened, err := Open(t.Context(), owner)
				require.NoError(t, err)
				require.False(t, reopened.Latched())
				require.NoError(t, reopened.View(t.Context(), func(tx *sql.Tx) error {
					var principalRevision, credentialRevision int64
					var credentialID string
					require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT revision,credential_revision,credential_id FROM principals WHERE id=?`, recoveryPrincipalID).Scan(&principalRevision, &credentialRevision, &credentialID))
					require.Equal(t, int64(2), principalRevision)
					require.Equal(t, int64(1), credentialRevision)
					require.Equal(t, recoveryCredentialID, credentialID)
					var fences int
					require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT count(*) FROM keyring_authority_fences`).Scan(&fences))
					require.Zero(t, fences)
					var compensations int
					require.NoError(t, tx.QueryRowContext(t.Context(), `SELECT count(*) FROM control_audit_events WHERE category='agent_credential' AND action='invalidate'`).Scan(&compensations))
					require.Zero(t, compensations)
					return nil
				}))
				require.NoError(t, reopened.Close())
				for _, path := range []string{marker.intent, marker.tombstone} {
					actual, err := os.ReadFile(path)
					require.NoError(t, err)
					require.Equal(t, data, actual)
				}
			}
		})
	}
}

func TestHistoricalEvidenceRejectsUnknownMalformedForeignAndConflictingSlots(t *testing.T) {
	base := recoveryActionFromCandidate(recoveryCandidate())
	encode := func(action *recoveryAction) []byte {
		data, err := json.Marshal(markerDocument{InstallationID: testInstallationID, State: "armed", Recovery: action})
		require.NoError(t, err)
		return data
	}
	mixed := base
	mixed.Owner = testInstallationID
	other := base
	other.CredentialID = "01J60000000000000000000013"
	for _, test := range []struct {
		name          string
		first, second []byte
	}{
		{"oversize", bytes.Repeat([]byte("x"), markerBytesMaximum+1), nil},
		{"malformed", []byte("{"), nil},
		{"unknown action", encode(&recoveryAction{Action: "unknown"}), nil},
		{"mixed action fields", encode(&mixed), nil},
		{"foreign", []byte(`{"installation_id":"01J60000000000000000000099","state":"armed"}`), nil},
		{"unknown member", []byte(`{"installation_id":"` + testInstallationID + `","state":"armed","extra":true}`), nil},
		{"duplicate member", []byte(`{"installation_id":"` + testInstallationID + `","state":"armed","state":"armed"}`), nil},
		{"conflicting actions", encode(&base), encode(&other)},
		{"generic and recovery mixed", encode(nil), encode(&base)},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := newOwnership(t)
			store, err := Initialize(t.Context(), owner, testInstallationID)
			require.NoError(t, err)
			require.NoError(t, store.Close())
			marker := newMutationMarker(owner.Layout(), nil)
			require.NoError(t, os.WriteFile(marker.intent, test.first, 0600))
			if test.second != nil {
				require.NoError(t, os.WriteFile(marker.tombstone, test.second, 0600))
			}
			_, err = Open(t.Context(), owner)
			require.ErrorIs(t, err, ErrStorageLatched)
			actual, err := os.ReadFile(marker.intent)
			require.NoError(t, err)
			assert.Equal(t, test.first, actual)
		})
	}
}

func insertRecoveryPrincipal(ctx context.Context, transaction *sql.Tx, principalRevision, credentialRevision int64, credentialID string) error {
	_, err := transaction.ExecContext(ctx, `INSERT INTO principals (id,display_name,state,visibility,revision,credential_revision,credential_id,credential_verifier,credential_fingerprint,credential_created_at,created_at,updated_at) VALUES (?, 'Agent', 'active', 'requestable', ?, ?, ?, ?, '0123456789abcdef', ?, ?, ?)`, recoveryPrincipalID, principalRevision, credentialRevision, credentialID, make([]byte, 32), recoveryTimestamp, recoveryTimestamp, recoveryTimestamp)
	return err
}
