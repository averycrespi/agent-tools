package contract

import "time"

// Diagnostic bounds are independent of mandatory audit/storage admission.
const (
	DiagnosticQueueRecords      = 256
	DiagnosticRecordBytes       = 4 * 1024
	DiagnosticFlushDeadline     = time.Second
	DiagnosticElapsedMaximum    = 24 * time.Hour
	DiagnosticSummaryInterval   = time.Minute
	DiagnosticSuppressionOwners = 2048
	DiagnosticReferenceOwners   = 1024
)

type DiagnosticEvent struct {
	Name           string
	Level          string
	RequiredFields []string
	OptionalFields []string
	Causes         []string
	Stages         []string
	Conditions     []string
}

// DiagnosticEvents is the closed version-one field inventory. Common required
// fields are schema_version, time, level, event and process_id. An empty cause
// means the cause field must be absent. Returned definitions have no global
// mutable state. The adapter tests enforce agreement with these declarations.
func DiagnosticEvents() []DiagnosticEvent {
	rejection := []string{"capacity", "expired", "cancelled", "stopped", "latched", "unavailable"}
	authorityFields := []string{"mutation_id", "owned", "waiting", "limit"}
	storageFields := []string{"mutation_id", "owned", "waiting", "limit", "writer_kind"}
	authorityConditions := []string{"limit=32"}
	storageConditions := []string{"limit=31", "waiting<=31", "non-foreign writer requires call_id"}
	stages := []string{"size_check", "identity_check", "intent_arm", "transaction_begin", "transaction_body", "transaction_commit", "transaction_rollback", "intent_cleanup"}
	events := []DiagnosticEvent{
		{Name: "startup", Level: "info", Causes: []string{""}},
		{Name: "readiness", Level: "info", Causes: []string{""}},
		{Name: "drain", Level: "info", Causes: []string{""}},
		{Name: "shutdown", Level: "info", RequiredFields: []string{"cause"}, OptionalFields: []string{"duration_ms"}, Causes: []string{"success"}},
		{Name: "lifecycle_failure", Level: "error", RequiredFields: []string{"cause"}, OptionalFields: []string{"duration_ms"}, Causes: []string{"unavailable"}},
		{Name: "invocation_admission", Level: "debug", RequiredFields: []string{"call_id", "cause"}, OptionalFields: []string{"invocation_id", "duration_ms"}, Causes: []string{"success", "rejected", "unavailable", "stopped"}, Conditions: []string{"success requires invocation_id", "unavailable/stopped forbid invocation_id"}},
		{Name: "execution_start", Level: "debug", RequiredFields: []string{"call_id", "invocation_id"}, Causes: []string{""}},
		{Name: "execution_result", Level: "debug", RequiredFields: []string{"call_id", "invocation_id", "cause"}, OptionalFields: []string{"duration_ms"}, Causes: []string{"success", "rejected", "unknown_outcome"}},
		{Name: "terminal_annotation", Level: "debug", RequiredFields: []string{"call_id", "invocation_id", "cause"}, OptionalFields: []string{"duration_ms"}, Causes: []string{"success", "unavailable"}},
		{Name: "authority_wait", Level: "debug", RequiredFields: authorityFields, OptionalFields: []string{"call_id"}, Causes: []string{""}, Conditions: authorityConditions},
		{Name: "authority_acquire", Level: "debug", RequiredFields: append([]string{"cause"}, authorityFields...), OptionalFields: []string{"call_id", "duration_ms"}, Causes: []string{"success"}, Conditions: authorityConditions},
		{Name: "authority_release", Level: "debug", RequiredFields: append([]string{"cause"}, authorityFields...), OptionalFields: []string{"call_id", "duration_ms"}, Causes: []string{"success"}, Conditions: authorityConditions},
		{Name: "authority_reject", Level: "debug", RequiredFields: append([]string{"cause"}, authorityFields...), OptionalFields: []string{"call_id", "duration_ms"}, Causes: rejection, Conditions: authorityConditions},
		{Name: "storage_wait", Level: "debug", RequiredFields: storageFields, OptionalFields: []string{"call_id"}, Causes: []string{""}, Conditions: storageConditions},
		{Name: "storage_acquire", Level: "debug", RequiredFields: append([]string{"cause"}, storageFields...), OptionalFields: []string{"call_id", "duration_ms"}, Causes: []string{"success"}, Conditions: storageConditions},
		{Name: "storage_release", Level: "debug", RequiredFields: append([]string{"cause"}, storageFields...), OptionalFields: []string{"call_id", "duration_ms"}, Causes: []string{"success"}, Conditions: storageConditions},
		{Name: "storage_reject", Level: "debug", RequiredFields: append([]string{"cause"}, storageFields...), OptionalFields: []string{"call_id", "duration_ms"}, Causes: rejection, Conditions: storageConditions},
		{Name: "durability_failure", Level: "error", RequiredFields: []string{"mutation_id", "writer_kind", "cause", "stage"}, OptionalFields: []string{"call_id", "duration_ms"}, Causes: []string{"latched"}, Stages: stages, Conditions: []string{"non-foreign writer requires call_id"}},
		{Name: "storage_latch", Level: "error", RequiredFields: []string{"mutation_id", "writer_kind", "cause", "stage"}, OptionalFields: []string{"call_id", "duration_ms"}, Causes: []string{"latched"}, Stages: stages, Conditions: []string{"non-foreign writer requires call_id"}},
		{Name: "reconciliation_displaced", Level: "info", OptionalFields: []string{"upstream_ref", "attempt_ref"}, Causes: []string{""}},
		{Name: "reconciliation_settlement_failure", Level: "warn", RequiredFields: []string{"cause"}, OptionalFields: []string{"upstream_ref", "attempt_ref"}, Causes: []string{"capacity", "unavailable", "stopped"}},
	}
	for _, definition := range []struct{ name, level string }{
		{"upstream_attempt_start", "debug"}, {"upstream_attempt_complete", "debug"},
		{"upstream_retry_scheduled", "debug"}, {"upstream_retry_reset", "debug"},
		{"upstream_unhealthy", "warn"}, {"upstream_recovered", "info"},
		{"oauth_required", "info"}, {"oauth_completed", "info"},
		{"oauth_expired", "warn"}, {"oauth_failed", "warn"},
		{"oauth_refresh_complete", "debug"}, {"oauth_refresh_failed", "warn"}, {"oauth_stage", "debug"}, {"catalog_poll_scheduled", "debug"},
	} {
		item := DiagnosticEvent{Name: definition.name, Level: definition.level, RequiredFields: []string{"phase", "reason", "disposition"}, OptionalFields: []string{"upstream_ref", "attempt_ref"}, Causes: []string{""}, Conditions: []string{"closed upstream phase/reason/disposition", "references are opaque non-reused process counters; absent means unavailable", "no resource identity or arbitrary metadata"}}
		switch definition.name {
		case "upstream_attempt_start", "upstream_attempt_complete":
			item.RequiredFields = append(item.RequiredFields, "attempt_ref")
			item.OptionalFields = []string{"upstream_ref"}
		}
		switch definition.name {
		case "upstream_attempt_complete", "oauth_refresh_complete", "oauth_refresh_failed":
			item.OptionalFields = append(item.OptionalFields, "duration_ms")
		}
		if definition.name == "upstream_retry_scheduled" {
			item.RequiredFields = append(item.RequiredFields, "retry_attempt", "delay_ms")
		}
		if definition.name == "catalog_poll_scheduled" {
			item.RequiredFields = append(item.RequiredFields, "delay_ms")
			item.Conditions = append(item.Conditions, "actual scheduled catalog delay > 0 and <= CatalogPollInterval; no retry_attempt")
		}
		if definition.name == "upstream_unhealthy" || definition.name == "oauth_refresh_failed" {
			item.OptionalFields = append(item.OptionalFields, "suppressed")
		}
		events = append(events, item)
	}
	return append(events, DiagnosticEvent{Name: "diagnostic_loss", Level: "warn", RequiredFields: []string{"dropped", "invalid"}, Causes: []string{""}})
}

func DiagnosticLevels() []string { return []string{"warn", "info", "debug"} }
