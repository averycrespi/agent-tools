package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validAuditFixture() AuditEvent {
	return AuditEvent{AuditSummary: AuditSummary{
		ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Sequence: "1", Timestamp: "2026-09-05T12:00:00.000000000Z",
		Category: "grant_request", Action: "approve", Phase: "outcome", Outcome: "succeeded",
		Actor:         AuditActor{Type: AuditOperator, Credential: &AuditCredential{ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Fingerprint: "0123456789abcdef"}},
		CorrelationID: "01ARZ3NDEKTSV4RRFFQ69G5FAV", Target: AuditTarget{Type: "grant_request", ID: "01ARZ3NDEKTSV4RRFFQ69G5FAV"},
	}}
}

func TestAuditClosedEvidenceAndAttribution(t *testing.T) {
	require.NoError(t, ValidateAuditEvent(validAuditFixture()))
	for name, mutate := range map[string]func(*AuditEvent){
		"human actor":            func(e *AuditEvent) { e.Actor.Type = "human" },
		"missing credential":     func(e *AuditEvent) { e.Actor.Credential = nil },
		"invented performer":     func(e *AuditEvent) { e.Actor.Type = AuditSystem },
		"operator initiator":     func(e *AuditEvent) { e.Initiator = e.Actor.Credential },
		"offline initiator":      func(e *AuditEvent) { e.Initiator = e.Actor.Credential; e.Actor = AuditActor{Type: AuditOffline} },
		"attempt claims success": func(e *AuditEvent) { e.Phase = "attempt" },
		"outcome pending":        func(e *AuditEvent) { e.Outcome = "pending" },
		"unknown phase":          func(e *AuditEvent) { e.Phase = "complete" },
		"invocation duplication": func(e *AuditEvent) { e.Category = "invocation" },
		"request submission":     func(e *AuditEvent) { e.Action = "submit" },
		"housekeeping":           func(e *AuditEvent) { e.Category, e.Action = "admin_session", "expire" },
		"invalid sequence":       func(e *AuditEvent) { e.Sequence = "01" },
		"overflow sequence":      func(e *AuditEvent) { e.Sequence = "9223372036854775808" },
		"noncanonical time":      func(e *AuditEvent) { e.Timestamp = "2026-09-05T12:00:00Z" },
		"unknown reason":         func(e *AuditEvent) { reason := PublicReason("private diagnostic"); e.Detail.Reason = &reason },
	} {
		t.Run(name, func(t *testing.T) {
			value := validAuditFixture()
			mutate(&value)
			assert.ErrorIs(t, ValidateAuditEvent(value), ErrInvalidAudit)
		})
	}
	for _, actor := range []AuditActorType{AuditSystem, AuditOffline} {
		value := validAuditFixture()
		credential := value.Actor.Credential
		value.Actor = AuditActor{Type: actor}
		require.NoError(t, ValidateAuditEvent(value))
		if actor == AuditSystem {
			value.Initiator = credential
			require.NoError(t, ValidateAuditEvent(value))
		}
	}
	value := validAuditFixture()
	value.Phase, value.Outcome = "attempt", "pending"
	require.NoError(t, ValidateAuditEvent(value))
	code := string(ProblemConflict)
	value.Detail.Problem = &code
	assert.ErrorIs(t, ValidateAuditEvent(value), ErrInvalidAudit)
	value.Phase, value.Outcome = "outcome", "rejected"
	require.NoError(t, ValidateAuditEvent(value))
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	assert.Less(t, len(encoded), AuditDetailBytes)

	actions := AuditActions("grant_request")
	actions[0] = "submit"
	assert.Equal(t, []string{"approve", "reject"}, AuditActions("grant_request"))
}

func TestAuditFiltersAreClosedAndTimeBounded(t *testing.T) {
	require.NoError(t, ValidateAuditFilters(AuditFilters{}))
	require.NoError(t, ValidateAuditFilters(AuditFilters{From: "2026-01-01T00:00:00.000000000Z", Until: "2027-01-02T00:00:00.000000000Z"}))
	for _, filters := range []AuditFilters{
		{ActorType: "human"}, {CredentialID: "mgw_admin_canary"}, {Category: "invocation"}, {Category: "grant_request", Action: "submit"},
		{TargetType: "url"}, {TargetID: strings.Repeat("a", 27)}, {Outcome: "retrying"}, {CorrelationID: "request-body"},
		{From: "2026-01-01T00:00:00.000000000Z"}, {Until: "2026-01-01T00:00:00.000000000Z"},
		{From: "2026-01-01T00:00:00.000000000Z", Until: "2027-01-02T00:00:00.000000001Z"},
		{From: "2026-01-01T00:00:00.000000000Z", Until: "2026-01-01T00:00:00.000000000Z"},
	} {
		assert.ErrorIs(t, ValidateAuditFilters(filters), ErrInvalidAudit, "%+v", filters)
	}
}
