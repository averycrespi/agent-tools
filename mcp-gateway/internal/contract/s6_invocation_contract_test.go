package contract

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS6InvocationContract(t *testing.T) {
	assert.Equal(t, []InvocationTargetKind{InvocationTargetDownstream, InvocationTargetGateway}, InvocationTargetKinds())
	assert.Equal(t, []InvocationOutcomeClass{
		InvocationOutcomeInvalidParams, InvocationOutcomeUnknownTool, InvocationOutcomeInvalidArguments,
		InvocationOutcomeAuthorizationUnavailable, InvocationOutcomeDeny, InvocationOutcomeBlock,
		InvocationOutcomePrestartFailure, InvocationOutcomeSucceeded, InvocationOutcomeDownstreamFailure, InvocationOutcomeUnknown,
	}, InvocationOutcomeClasses())
	assert.Equal(t, []InvocationOutcomeBasis{InvocationBasisAdmission, InvocationBasisPolicy, InvocationBasisTerminal, InvocationBasisMissingTerminal}, InvocationOutcomeBases())
	for _, invalid := range []string{"", "EVALUATED", "pending", "unknown"} {
		_, err := ParseInvocationOutcomeClass(invalid)
		assert.Error(t, err)
		_, err = ParseInvocationOutcomeBasis(invalid)
		assert.Error(t, err)
		_, err = ParseInvocationTargetKind(invalid)
		assert.Error(t, err)
	}

	collection, ok := RouteForPath("/api/v1/invocations")
	require.True(t, ok)
	assert.Equal(t, []string{"GET"}, collection.Methods)
	assert.Equal(t, AuthorityAdmin, collection.Authority)
	item, ok := RouteForPath("/api/v1/invocations/01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.True(t, ok)
	assert.Equal(t, "/api/v1/invocations/{id}", item.Pattern)
	assert.Equal(t, AuthorityAdmin, item.Authority)

	mechanics := make(map[string]ResourceMechanic)
	for _, mechanic := range ResourceMechanics() {
		if mechanic.Pattern == collection.Pattern || mechanic.Pattern == item.Pattern {
			mechanics[mechanic.Pattern] = mechanic
		}
	}
	assert.Equal(t, ResourceMechanic{Pattern: collection.Pattern, Method: "GET", RequestSchema: "InvocationListQuery", SuccessSchema: "InvocationPage", SuccessStatuses: []int{200}, Cursor: true}, mechanics[collection.Pattern])
	assert.Equal(t, ResourceMechanic{Pattern: item.Pattern, Method: "GET", RequestSchema: "None", SuccessSchema: "Invocation", SuccessStatuses: []int{200}}, mechanics[item.Pattern])
	for _, code := range []ProblemCode{ProblemInvalidCursor, ProblemStaleCursor, ProblemNotFound} {
		_, ok := ProblemForCode(code)
		assert.True(t, ok, code)
	}

	summary := InvocationSummary{
		ID: "invocation", PrincipalID: "principal", CredentialID: "credential", CredentialFingerprint: "fingerprint",
		CredentialRevision: "2", AdmittedAt: "2026-08-28T00:00:00Z", AdmissionClass: AdmissionEvaluated,
		RequestedName: pointer("requested"), Target: &InvocationTarget{Kind: InvocationTargetDownstream, ServerID: "server", ToolID: "tool", UpstreamName: "upstream", DescriptorRevision: "3", DescriptorFingerprint: "descriptor"},
		Authorization: &InvocationAuthorization{Decision: DecisionAllow, Revision: "4", EvaluatedAt: "2026-08-28T00:00:01Z", GrantID: pointer("grant")},
		Outcome:       InvocationOutcome{Class: InvocationOutcomeSucceeded, Basis: InvocationBasisTerminal, CompletedAt: pointer("2026-08-28T00:00:02Z")},
	}
	encodedSummary, err := json.Marshal(summary)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedSummary), "redacted_arguments")
	var summaryMembers map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encodedSummary, &summaryMembers))
	assert.ElementsMatch(t, []string{"id", "principal_id", "credential_id", "credential_fingerprint", "credential_revision", "admitted_at", "admission_class", "requested_name", "target", "authorization", "outcome"}, mapKeys(summaryMembers))

	capture := json.RawMessage(`{"token":"[REDACTED]"}`)
	encodedItem, err := json.Marshal(Invocation{InvocationSummary: summary, RedactedArguments: capture})
	require.NoError(t, err)
	assert.Contains(t, string(encodedItem), `"redacted_arguments":{"token":"[REDACTED]"}`)
	page := InvocationPage{Items: []InvocationSummary{summary}, NextCursor: nil}
	encodedPage, err := json.Marshal(page)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedPage), "redacted_arguments")

	filters := InvocationFilters{
		PrincipalID: pointer("principal"), ServerID: pointer("server"), RequestedName: pointer("requested"),
		AdmissionClass: pointer(AdmissionEvaluated), Decision: pointer(DecisionAllow), Outcome: pointer(InvocationOutcomeSucceeded),
	}
	query := InvocationListQuery{Cursor: pointer("cursor"), Limit: 50, Filters: filters}
	binding := InvocationCursorBinding{Filters: filters, UpperSequence: 100, NextSequence: 75}
	assert.Equal(t, filters, query.Filters)
	assert.Equal(t, int64(100), binding.UpperSequence)
	assert.Equal(t, int64(75), binding.NextSequence)

	base := InvocationAuditRecord{
		InvocationID: "invocation", PrincipalID: "principal", CredentialID: "credential", CredentialFingerprint: "fingerprint",
		CredentialRevision: "2", AdmittedAt: "2026-08-28T00:00:00Z", AdmissionClass: AdmissionEvaluated,
	}
	for _, test := range []struct {
		name      string
		mutate    func(*InvocationAuditRecord)
		class     InvocationOutcomeClass
		basis     InvocationOutcomeBasis
		completed *string
	}{
		{name: "invalid params", mutate: admission(AdmissionInvalidParams), class: InvocationOutcomeInvalidParams, basis: InvocationBasisAdmission},
		{name: "unknown tool", mutate: admission(AdmissionUnknownTool), class: InvocationOutcomeUnknownTool, basis: InvocationBasisAdmission},
		{name: "invalid arguments", mutate: admission(AdmissionInvalidArguments), class: InvocationOutcomeInvalidArguments, basis: InvocationBasisAdmission},
		{name: "authorization unavailable", mutate: admission(AdmissionAuthorizationUnavailable), class: InvocationOutcomeAuthorizationUnavailable, basis: InvocationBasisAdmission},
		{name: "deny", mutate: evaluated(DecisionDeny, nil), class: InvocationOutcomeDeny, basis: InvocationBasisPolicy},
		{name: "block", mutate: evaluated(DecisionBlock, nil), class: InvocationOutcomeBlock, basis: InvocationBasisPolicy},
		{name: "prestart failure", mutate: evaluated(DecisionAllow, pointer(TerminalPrestartFailure)), class: InvocationOutcomePrestartFailure, basis: InvocationBasisTerminal, completed: pointer("2026-08-28T00:00:02Z")},
		{name: "succeeded", mutate: evaluated(DecisionAllow, pointer(TerminalSucceeded)), class: InvocationOutcomeSucceeded, basis: InvocationBasisTerminal, completed: pointer("2026-08-28T00:00:02Z")},
		{name: "downstream failure", mutate: evaluated(DecisionAllow, pointer(TerminalDownstreamFailure)), class: InvocationOutcomeDownstreamFailure, basis: InvocationBasisTerminal, completed: pointer("2026-08-28T00:00:02Z")},
		{name: "explicit unknown", mutate: evaluated(DecisionAllow, pointer(TerminalOutcomeUnknown)), class: InvocationOutcomeUnknown, basis: InvocationBasisTerminal, completed: pointer("2026-08-28T00:00:02Z")},
		{name: "missing terminal", mutate: evaluated(DecisionAllow, nil), class: InvocationOutcomeUnknown, basis: InvocationBasisMissingTerminal},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := base
			test.mutate(&record)
			projected, err := ProjectInvocationAudit(record)
			require.NoError(t, err)
			assert.Equal(t, test.class, projected.Outcome.Class)
			assert.Equal(t, test.basis, projected.Outcome.Basis)
			assert.Equal(t, test.completed, projected.Outcome.CompletedAt)
		})
	}

	for name, mutate := range map[string]func(*InvocationAuditRecord){
		"invalid admission":               func(record *InvocationAuditRecord) { record.AdmissionClass = "invalid" },
		"evaluated without authorization": func(record *InvocationAuditRecord) { record.AdmissionClass = AdmissionEvaluated },
		"partial target": func(record *InvocationAuditRecord) {
			record.AdmissionClass = AdmissionInvalidParams
			record.ServerID = pointer("server")
		},
		"partial terminal": func(record *InvocationAuditRecord) {
			evaluated(DecisionAllow, nil)(record)
			record.CompletedAt = pointer("time")
		},
		"terminal deny": func(record *InvocationAuditRecord) { evaluated(DecisionDeny, pointer(TerminalSucceeded))(record) },
	} {
		t.Run(name, func(t *testing.T) {
			record := base
			mutate(&record)
			_, err := ProjectInvocationAudit(record)
			assert.Error(t, err)
		})
	}

	captured := base
	admission(AdmissionInvalidParams)(&captured)
	captured.RedactedArguments = pointer(`{"safe":"value"}`)
	projectedCapture, err := ProjectInvocationAudit(captured)
	require.NoError(t, err)
	assert.JSONEq(t, `{"safe":"value"}`, string(projectedCapture.RedactedArguments))
	captured.RedactedArguments = pointer(`"[TRUNCATED]"`)
	projectedCapture, err = ProjectInvocationAudit(captured)
	require.NoError(t, err)
	assert.JSONEq(t, `"[TRUNCATED]"`, string(projectedCapture.RedactedArguments))
	captured.RedactedArguments = pointer(`["forbidden"]`)
	_, err = ProjectInvocationSummary(captured)
	require.NoError(t, err, "summary projection must not inspect item-only capture")
	_, err = ProjectInvocationAudit(captured)
	assert.Error(t, err)

	downstream := base
	evaluated(DecisionAllow, nil)(&downstream)
	setTarget(&downstream, "server")
	projected, err := ProjectInvocationAudit(downstream)
	require.NoError(t, err)
	require.NotNil(t, projected.Target)
	assert.Equal(t, InvocationTargetDownstream, projected.Target.Kind)
	gateway := downstream
	setTarget(&gateway, SyntheticServerID)
	projected, err = ProjectInvocationAudit(gateway)
	require.NoError(t, err)
	assert.Equal(t, InvocationTargetGateway, projected.Target.Kind)
}

func admission(class InvocationAdmissionClass) func(*InvocationAuditRecord) {
	return func(record *InvocationAuditRecord) { record.AdmissionClass = class }
}

func evaluated(decision AuthorizationDecision, terminal *InvocationTerminalClass) func(*InvocationAuditRecord) {
	return func(record *InvocationAuditRecord) {
		record.AdmissionClass = AdmissionEvaluated
		record.AuthorizationDecision = pointer(decision)
		record.AuthorizationRevision = pointer("4")
		record.EvaluatedAt = pointer("2026-08-28T00:00:01Z")
		if terminal != nil {
			record.TerminalClass = terminal
			record.CompletedAt = pointer("2026-08-28T00:00:02Z")
		}
	}
}

func setTarget(record *InvocationAuditRecord, serverID string) {
	record.ServerID = pointer(serverID)
	record.ToolID = pointer("tool")
	record.UpstreamName = pointer("upstream")
	record.DescriptorRevision = pointer("3")
	record.DescriptorFingerprint = pointer("descriptor")
}

func pointer[T any](value T) *T { return &value }
