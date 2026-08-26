//go:build integration

package invocation

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInFlightAllowEvictionMakesTerminalAnnotationABenignMiss(t *testing.T) {
	_, audits, authority, _, credential := newAdmissionCoordinator(t, nil)
	lease, err := authority.Authenticate(context.Background(), credential.Bearer)
	require.NoError(t, err)
	defer lease.Release()
	started := make(chan struct{})
	releaseExecution := make(chan struct{})
	executions := 0
	service, err := newService(audits, authority, func(string) (callTarget, bool) {
		return serviceCallTarget(nil, func(context.Context) (executionLease, error) {
			return &serviceExecutionLease{execute: func(json.RawMessage) downstream.CallResult {
				executions++
				close(started)
				<-releaseExecution
				return downstream.CallResult{Response: downstream.Response{Result: json.RawMessage(`{"content":[]}`)}}
			}}, nil
		}), true
	})
	require.NoError(t, err)
	response := make(chan CallResponse, 1)
	go func() { response <- service.Call(context.Background(), lease, validCallParams()) }()
	<-started

	var inFlightID string
	require.NoError(t, audits.store.View(context.Background(), func(transaction *sql.Tx) error {
		return transaction.QueryRowContext(context.Background(), `SELECT id FROM invocations`).Scan(&inFlightID)
	}))
	inFlight, found, err := audits.Read(context.Background(), inFlightID)
	require.NoError(t, err)
	require.True(t, found)
	require.Nil(t, inFlight.TerminalClass)

	require.NoError(t, audits.store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		for index := 0; index < int(invocationLimit())-1; index++ {
			_, insertErr := transaction.ExecContext(context.Background(), `INSERT INTO invocations (
				id, principal_id, credential_id, credential_fingerprint, credential_revision,
				admitted_at, admission_class
			) VALUES (?, ?, ?, ?, ?, ?, 'invalid_params')`,
				invocationID(1000+index), inFlight.PrincipalID, inFlight.CredentialID,
				inFlight.CredentialFingerprint, inFlight.CredentialRevision, inFlight.AdmittedAt)
			if insertErr != nil {
				return insertErr
			}
		}
		return nil
	}))
	prepared, err := audits.Prepare(Admission{
		PrincipalID: inFlight.PrincipalID, CredentialID: inFlight.CredentialID,
		CredentialFingerprint: inFlight.CredentialFingerprint, CredentialRevision: inFlight.CredentialRevision,
		Class: contract.AdmissionInvalidParams,
	})
	require.NoError(t, err)
	require.NoError(t, audits.Insert(context.Background(), prepared), "the next insertion must evict the oldest in-flight ALLOW")
	_, found, err = audits.Read(context.Background(), inFlightID)
	require.NoError(t, err)
	assert.False(t, found)

	close(releaseExecution)
	result := <-response

	require.NotNil(t, result.Result)
	assert.Empty(t, result.ErrorCode)
	assert.Empty(t, result.InvocationID)
	assert.Equal(t, 1, executions)
	count, err := audits.Count(context.Background())
	require.NoError(t, err)
	assert.Equal(t, invocationLimit(), count)
	assert.False(t, audits.store.Latched())
}
