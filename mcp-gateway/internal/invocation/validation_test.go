package invocation

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStartupRejectsCorruptInvocationRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*rawInvocation)
	}{
		{name: "sequence", mutate: func(row *rawInvocation) { row.sequence = 0 }},
		{name: "invocation ID", mutate: func(row *rawInvocation) { row.id = "bad" }},
		{name: "principal ID", mutate: func(row *rawInvocation) { row.principalID = "bad" }},
		{name: "credential revision", mutate: func(row *rawInvocation) { row.credentialRevision = 0 }},
		{name: "noncanonical admitted time", mutate: func(row *rawInvocation) { row.admittedAt = "2026-08-26T19:00:00Z" }},
		{name: "noncompact capture", mutate: func(row *rawInvocation) { row.redacted = `{ "value":1 }` }},
		{name: "route partial", mutate: func(row *rawInvocation) { row.toolID = "" }},
		{name: "evaluation before admission", mutate: func(row *rawInvocation) { row.evaluatedAt = canonicalInvocationTime(invocationTestTime.Add(-1)) }},
		{name: "block grant", mutate: func(row *rawInvocation) { row.decision = string(contract.DecisionBlock) }},
		{name: "terminal before admission", mutate: func(row *rawInvocation) {
			row.completedAt = canonicalInvocationTime(invocationTestTime.Add(-1))
			row.terminal = string(contract.TerminalSucceeded)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository, store, _ := newInvocationRepository(t, nil, entropyBytes(64))
			row := validRawInvocation()
			test.mutate(&row)
			require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
				_, err := transaction.ExecContext(context.Background(), `PRAGMA ignore_check_constraints = ON`)
				if err != nil {
					return err
				}
				return insertRawInvocation(context.Background(), transaction, row)
			}))
			err := repository.ValidateStartup(context.Background())
			assert.ErrorIs(t, err, ErrInvalidState)
		})
	}
}

func TestValidateStartupRejectsInvocationCapacityOverflow(t *testing.T) {
	repository, store, _ := newInvocationRepository(t, nil, entropyBytes(64))
	fixtures := make([]PreparedAdmission, int(invocationLimit())+1)
	for index := range fixtures {
		fixtures[index] = PreparedAdmission{
			InvocationID: invocationID(index + 100),
			AdmittedAt:   canonicalInvocationTime(invocationTestTime),
			admission: Admission{
				PrincipalID: invocationID(1), CredentialID: invocationID(2),
				CredentialFingerprint: "0123456789abcdef", CredentialRevision: "1",
				Class: contract.AdmissionInvalidParams,
			},
		}
	}
	require.NoError(t, store.Mutate(context.Background(), func(transaction *sql.Tx) error {
		return insertValidationFixtures(context.Background(), transaction, fixtures)
	}))
	assert.ErrorIs(t, repository.ValidateStartup(context.Background()), ErrInvalidState)
}

func insertValidationFixtures(ctx context.Context, transaction *sql.Tx, prepared []PreparedAdmission) error {
	const columns = `INSERT INTO invocations (
		id, principal_id, credential_id, credential_fingerprint, credential_revision,
		admitted_at, admission_class, requested_name, redacted_arguments,
		server_id, tool_id, upstream_name, descriptor_revision, descriptor_fingerprint,
		decision, authorization_revision, evaluated_at, grant_id
	) VALUES `
	const row = `(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	const batchSize = 50
	for start := 0; start < len(prepared); start += batchSize {
		end := min(start+batchSize, len(prepared))
		arguments := make([]any, 0, (end-start)*18)
		for _, admission := range prepared[start:end] {
			values, err := admissionSQLValues(admission)
			if err != nil {
				return err
			}
			arguments = append(arguments, values...)
		}
		if _, err := transaction.ExecContext(ctx, columns+strings.TrimSuffix(strings.Repeat(row+",", end-start), ","), arguments...); err != nil {
			return err
		}
	}
	return nil
}

type rawInvocation struct {
	sequence, credentialRevision, descriptorRevision, authorizationRevision int64
	id, principalID, credentialID, fingerprint, admittedAt, class           string
	requestedName, redacted, serverID, toolID, upstreamName                 string
	descriptorFingerprint, decision, evaluatedAt, grantID                   string
	completedAt, terminal                                                   string
}

func validRawInvocation() rawInvocation {
	return rawInvocation{
		sequence: 1, id: invocationID(90), principalID: invocationID(1), credentialID: invocationID(2), fingerprint: "0123456789abcdef", credentialRevision: 1,
		admittedAt: canonicalInvocationTime(invocationTestTime), class: string(contract.AdmissionEvaluated), requestedName: "namespace.tool", redacted: `{"value":1e0}`,
		serverID: invocationID(10), toolID: invocationID(11), upstreamName: "tool", descriptorRevision: 2, descriptorFingerprint: strings.Repeat("a", 64),
		decision: string(contract.DecisionAllow), authorizationRevision: 3, evaluatedAt: canonicalInvocationTime(invocationTestTime), grantID: invocationID(70),
	}
}

func insertRawInvocation(ctx context.Context, transaction *sql.Tx, row rawInvocation) error {
	_, err := transaction.ExecContext(ctx, `INSERT INTO invocations (
		insertion_sequence, id, principal_id, credential_id, credential_fingerprint,
		credential_revision, admitted_at, admission_class, requested_name, redacted_arguments,
		server_id, tool_id, upstream_name, descriptor_revision, descriptor_fingerprint,
		decision, authorization_revision, evaluated_at, grant_id, completed_at, terminal_class
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
		row.sequence, row.id, row.principalID, row.credentialID, row.fingerprint,
		row.credentialRevision, row.admittedAt, row.class, row.requestedName, row.redacted,
		row.serverID, row.toolID, row.upstreamName, row.descriptorRevision, row.descriptorFingerprint,
		row.decision, row.authorizationRevision, row.evaluatedAt, row.grantID, row.completedAt, row.terminal)
	return err
}
