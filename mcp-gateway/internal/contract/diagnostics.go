package contract

import "time"

// Diagnostic bounds are independent of mandatory audit/storage admission.
const (
	DiagnosticQueueRecords  = 256
	DiagnosticRecordBytes   = 4 * 1024
	DiagnosticFlushDeadline = time.Second
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
	return []DiagnosticEvent{
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
		{Name: "diagnostic_loss", Level: "warn", RequiredFields: []string{"dropped", "invalid"}, Causes: []string{""}},
	}
}

func DiagnosticLevels() []string { return []string{"warn", "info", "debug"} }
