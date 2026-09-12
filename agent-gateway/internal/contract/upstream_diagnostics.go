package contract

// Diagnostic reference numbers are a closed exception to the identity ban:
// they are process counters, not resource/credential identifiers, hashes,
// encodings, operation IDs, or durable evidence. Both counters saturate without
// reuse; zero omits correlation. Composition retains at most 1024 private keys
// for its graph lifetime, including deleted upstreams. Concurrent allocation
// uses a nonblocking projection lock and atomic counters; contention/capacity
// can omit a reference but cannot block or reject runtime work.
func DiagnosticUpstreamPhases() []string {
	return []string{"unknown", "reconciliation", "connection", "mcp_initialization", "tool_discovery", "credential_resolution", "oauth_authorization", "oauth_refresh", "cleanup", "oauth_metadata", "oauth_registration", "oauth_callback", "oauth_exchange", "oauth_installation"}
}

func DiagnosticUpstreamReasons() []string {
	return []string{"unknown", "none", "connectivity", "authentication_required", "authentication_rejected", "protocol_invalid", "protocol_unsupported", "resource_limit", "configuration_invalid", "keyring_unavailable", "stop_unconfirmed", "timeout", "cancelled", "superseded", "oauth_rejected", "cleanup_pending", "tls_failed", "credential_absent", "keyring_locked", "keyring_interaction_required", "oauth_expired", "registration_expired", "process_exited", "output_limit", "catalog_invalid", "catalog_limit", "catalog_stale"}
}

func DiagnosticUpstreamDispositions() []string {
	return []string{"unknown", "executing", "retry_scheduled", "operator_authentication_required", "healthy", "stopped", "cleanup_uncertain", "superseded", "cancelled"}
}
