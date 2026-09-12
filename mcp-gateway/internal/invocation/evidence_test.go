package invocation

import (
	"context"
	"strings"
	"testing"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/activity"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvidencePreparationOwnsCanonicalTargetSnapshot(t *testing.T) {
	for _, lateBinding := range []bool{false, true} {
		t.Run(map[bool]string{false: "prepare", true: "with admission"}[lateBinding], func(t *testing.T) {
			repository, _, _ := newInvocationRepository(t, nil, entropyBytes(64))
			input := testEvaluatedAdmission()
			var prepared PreparedAdmission
			var err error
			if lateBinding {
				prepared, err = repository.PrepareIdentity()
				require.NoError(t, err)
				prepared, err = prepared.WithAdmission(input)
			} else {
				prepared, err = repository.Prepare(input)
			}
			require.NoError(t, err)

			*input.MCP.Route.Target.UpstreamName = "changed"
			input.MCP.Route.Target.ServerID = invocationID(99)
			input.MCP.Route.DescriptorRevision = "99"
			*input.MCP.RequestedName = "changed.tool"
			input.MCP.RedactedArguments[2] = 'X'
			*input.Authorization.GrantID = invocationID(99)
			input.Authorization.AuthorizationRevision = "99"
			require.NoError(t, repository.Insert(context.Background(), prepared))
			item, err := repository.Get(context.Background(), prepared.InvocationID)
			require.NoError(t, err)
			require.NotNil(t, item.Target)
			assert.Equal(t, contract.InvocationTarget{
				Kind: contract.InvocationTargetDownstream, ServerID: invocationID(10), ToolID: invocationID(11),
				UpstreamName: "tool", DescriptorRevision: "2", DescriptorFingerprint: strings.Repeat("a", 64),
			}, *item.Target)
			assert.Equal(t, "namespace.tool", *item.RequestedName)
			assert.Equal(t, `{"value":1e0}`, string(item.RedactedArguments))
			require.NotNil(t, item.Authorization)
			assert.Equal(t, invocationID(70), *item.Authorization.GrantID)
			assert.Equal(t, "3", item.Authorization.Revision)
			assert.Equal(t, contract.InvocationOutcome{Class: contract.InvocationOutcomeUnknown, Basis: contract.InvocationBasisMissingTerminal}, item.Outcome)
		})
	}
}

func TestStoredEvidenceRejectsPartialNullableGroups(t *testing.T) {
	record := evidenceRecord()
	require.True(t, validStoredInvocation(record))
	for name, mutate := range map[string]func(*contract.InvocationAuditRecord){
		"server":                 func(r *contract.InvocationAuditRecord) { r.ServerID = nil },
		"tool":                   func(r *contract.InvocationAuditRecord) { r.ToolID = nil },
		"upstream":               func(r *contract.InvocationAuditRecord) { r.UpstreamName = nil },
		"descriptor revision":    func(r *contract.InvocationAuditRecord) { r.DescriptorRevision = nil },
		"descriptor fingerprint": func(r *contract.InvocationAuditRecord) { r.DescriptorFingerprint = nil },
		"decision":               func(r *contract.InvocationAuditRecord) { r.AuthorizationDecision = nil },
		"authorization revision": func(r *contract.InvocationAuditRecord) { r.AuthorizationRevision = nil },
		"evaluation time":        func(r *contract.InvocationAuditRecord) { r.EvaluatedAt = nil },
		"completion time":        func(r *contract.InvocationAuditRecord) { r.CompletedAt = nil },
		"terminal class":         func(r *contract.InvocationAuditRecord) { r.TerminalClass = nil },
	} {
		t.Run(name, func(t *testing.T) {
			broken := record
			mutate(&broken)
			_, _, ok := storedEvidence(broken)
			assert.False(t, ok, "partial evidence must not normalize into absence")
			assert.False(t, validStoredInvocation(broken))
		})
	}
}

func TestStoredEvidenceSeparatesCompletionAndMCPDetails(t *testing.T) {
	for _, serverID := range []string{invocationID(10), contract.SyntheticServerID} {
		record := evidenceRecord()
		record.ServerID = &serverID
		envelope, details, ok := storedEvidence(record)
		require.True(t, ok)
		assert.Equal(t, activity.Identity{InvocationID: record.InvocationID, AdmittedAt: record.AdmittedAt}, envelope.Identity)
		assert.Equal(t, record.PrincipalID, envelope.PrincipalID)
		require.NotNil(t, envelope.Completion)
		assert.Equal(t, activity.Completion{CompletedAt: *record.CompletedAt, Class: contract.TerminalSucceeded}, *envelope.Completion)
		require.NotNil(t, details.Route)
		assert.Equal(t, serverID, details.Route.Target.ServerID)
		assert.Equal(t, *record.UpstreamName, details.Route.Target.ToolName())
		assert.Equal(t, []byte(*record.RedactedArguments), details.RedactedArguments)
		projected, err := contract.ProjectInvocationAudit(record)
		require.NoError(t, err)
		kind := contract.InvocationTargetDownstream
		if serverID == contract.SyntheticServerID {
			kind = contract.InvocationTargetGateway
		}
		assert.Equal(t, kind, projected.Target.Kind)

		record.CompletedAt, record.TerminalClass = nil, nil
		envelope, _, ok = storedEvidence(record)
		require.True(t, ok)
		assert.Nil(t, envelope.Completion)
		projected, err = contract.ProjectInvocationAudit(record)
		require.NoError(t, err)
		assert.Equal(t, contract.InvocationBasisMissingTerminal, projected.Outcome.Basis)
		assert.Equal(t, contract.InvocationOutcomeUnknown, projected.Outcome.Class)
	}
}

func evidenceRecord() contract.InvocationAuditRecord {
	return contract.InvocationAuditRecord{
		Sequence: 1, InvocationID: invocationID(80), PrincipalID: invocationID(1), CredentialID: invocationID(2),
		CredentialFingerprint: "0123456789abcdef", CredentialRevision: "1",
		AdmittedAt: canonicalInvocationTime(invocationTestTime), AdmissionClass: contract.AdmissionEvaluated,
		RequestedName: evidencePointer("namespace.tool"), RedactedArguments: evidencePointer(`{"value":1e0}`),
		ServerID: evidencePointer(invocationID(10)), ToolID: evidencePointer(invocationID(11)), UpstreamName: evidencePointer("tool"),
		DescriptorRevision: evidencePointer("2"), DescriptorFingerprint: evidencePointer(strings.Repeat("a", 64)),
		AuthorizationDecision: evidencePointer(contract.DecisionAllow), AuthorizationRevision: evidencePointer("3"),
		EvaluatedAt: evidencePointer(canonicalInvocationTime(invocationTestTime)), GrantID: evidencePointer(invocationID(70)),
		CompletedAt: evidencePointer(canonicalInvocationTime(invocationTestTime)), TerminalClass: evidencePointer(contract.TerminalSucceeded),
	}
}

func evidencePointer[T any](value T) *T { return &value }
