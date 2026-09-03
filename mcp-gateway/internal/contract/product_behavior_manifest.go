package contract

import "strings"

const ProductBehaviorManifestVersion = 1

type ProductBehavior struct {
	ID            string
	Kind          string
	EvidenceOwner string
}

type DocumentationBehavior struct {
	ID             string
	EvidenceOwner  string
	CanonicalOwner string
}

type EvidenceTier struct {
	ID string
}

var productCriterionIDs = []string{
	"interface.developer_first",
	"browser.authority_recovery",
	"operations.online_workflows",
	"invocation.reads_unknown_outcomes",
	"client.concurrency_uncertainty_refresh",
	"privacy.secret_boundaries",
	"operator.security_sensitive_actions",
	"browser.accessibility_responsive",
	"cli.automation_safety",
	"frontend.static_supply_chain",
	"compatibility.release_evidence",
}

var productClauseGroups = []struct {
	prefix string
	owner  string
	ids    []string
}{
	{prefix: "interface", owner: "tier.browser.workflows", ids: []string{
		"primary_navigation", "restorable_locations", "safe_fragment_content", "responsive_navigation", "operational_visual_language", "comparison_first_lists", "theme_persistence", "inert_untrusted_content",
	}},
	{prefix: "browser.session", owner: "tier.browser.workflows", ids: []string{
		"bearer_exchange", "bootstrap_csrf_exception", "bootstrap_precedence", "exchange_and_logout", "bootstrap_recovery", "authentication_epochs", "stale_one_time_values", "no_offline_queue",
	}},
	{prefix: "overview", owner: "tier.browser.workflows", ids: []string{
		"authoritative_facts", "complete_attention_counts", "storage_latch_guidance", "keyring_interaction_limit",
	}},
	{prefix: "server_catalog", owner: "tier.browser.workflows", ids: []string{
		"state_separation", "safe_server_registration", "etag_updates", "eligible_operations", "operation_polling", "write_only_credentials", "one_time_oauth_url", "active_vs_durable_catalog", "deletion_and_disconnect",
	}},
	{prefix: "access_policy", owner: "tier.browser.workflows", ids: []string{
		"principal_inventory", "principal_creation_defaults", "agent_credential_rotation", "immutable_grants", "closed_constraints", "grant_correction_order", "expired_and_default_grants",
	}},
	{prefix: "grant_request", owner: "tier.browser.workflows", ids: []string{
		"pending_collection", "evidence_item", "approval_narrowing", "rejection_contract", "conflict_and_uncertainty", "historical_approval",
	}},
	{prefix: "administration", owner: "tier.integration.compatibility", ids: []string{
		"admin_credentials", "backup_idempotency", "stopped_recovery_guidance", "offline_commands",
	}},
	{prefix: "invocation", owner: "tier.unit.contract", ids: []string{
		"read_only_routes", "closed_filters", "newest_first_cursor", "page_coherence", "summary_projection", "item_redacted_arguments", "synthetic_target_derivation", "outcome_derivation", "missing_terminal_unknown", "local_target_unknown", "bounded_retention", "polling_without_authority",
	}},
	{prefix: "client_refresh", owner: "tier.browser.workflows", ids: []string{
		"post_event_stream", "snapshot_refresh", "event_owner_mapping", "disconnect_recovery", "read_generation", "no_unsafe_replay", "mutation_feedback",
	}},
	{prefix: "secret_handling", owner: "tier.security.privacy", ids: []string{
		"sink_preparation", "browser_one_time_sinks", "cli_secret_sinks", "post_response_sink_loss", "write_only_inputs", "consequence_confirmations", "confirmation_matrix",
	}},
	{prefix: "cli", owner: "tier.e2e.complete", ids: []string{
		"command_tree", "loopback_transport", "admin_authority_sources", "output_and_pagination", "json_input_and_preconditions", "idempotency_recovery", "no_automatic_replay", "oauth_url_sink", "closed_exit_status", "operator_parity",
	}},
	{prefix: "accessibility", owner: "tier.browser.accessibility", ids: []string{
		"wcag_conformance", "semantic_structure", "focus_management", "distinct_states", "responsive_reflow", "reduced_motion", "visual_fixture", "combined_evidence",
	}},
}

var productCapabilityIDs = []string{
	"status", "admin-credential-read", "admin-credential-create", "admin-credential-revoke", "backup-read", "backup-create", "backup-delete", "server-read", "server-create", "server-update", "server-delete", "operation-read", "operation-start", "server-credential", "auth-flow-read", "auth-flow-start", "auth-flow-cancel", "descriptor-read", "catalog-list", "principal-read", "principal-create", "principal-update", "principal-credential-issue", "principal-credential-revoke", "grant-read", "grant-create", "grant-update", "grant-delete", "grant-request-read", "grant-request-approve", "grant-request-reject", "invocation-read",
}

var productLifecycleIDs = []string{
	"web-exchange", "web-bootstrap", "web-logout", "web-events", "cli-bearer", "cli-initialize", "cli-admin-reset", "cli-restore",
}

var securityBehaviorIDs = []string{
	"browser.storage", "browser.fragment_attributes", "browser.dom_inputs", "browser.one_time_display", "browser.clipboard", "browser.oauth_opener_referrer", "browser.authentication_epoch", "browser.sink_loss", "cli.argv_environment", "cli.output", "observability.logs_reports", "events.payloads", "invocation.audit_capture", "storage.sqlite_backups", "frontend.generated_assets", "browser.screenshots_reports", "process.output", "tests.artifacts",
}

var documentationTopicIDs = []string{
	"web-sign-in-recovery", "online-cli-authentication", "one-time-sinks", "invocation-outcomes", "refresh-polling", "offline-recovery", "frontend-development", "browser-accessibility", "keyring-limitations",
}

var predecessorBehaviorRows = []ProductBehavior{
	{ID: "cli.first_run.defaults", Kind: "predecessor", EvidenceOwner: "tier.e2e.complete"},
	{ID: "cli.authentication.selection", Kind: "predecessor", EvidenceOwner: "tier.e2e.complete"},
	{ID: "cli.output.human_json", Kind: "predecessor", EvidenceOwner: "tier.e2e.complete"},
	{ID: "cli.secret_sinks", Kind: "predecessor", EvidenceOwner: "tier.security.privacy"},
	{ID: "cli.uncertain_outcomes", Kind: "predecessor", EvidenceOwner: "tier.e2e.complete"},
	{ID: "cli.help_and_errors", Kind: "predecessor", EvidenceOwner: "tier.unit.contract"},
	{ID: "cli.compatibility", Kind: "predecessor", EvidenceOwner: "tier.integration.compatibility"},
	{ID: "cli.security_boundary", Kind: "predecessor", EvidenceOwner: "tier.security.privacy"},
	{ID: "cli.documentation", Kind: "predecessor", EvidenceOwner: "tier.unit.contract"},
	{ID: "frontend.command_interface", Kind: "predecessor", EvidenceOwner: "tier.frontend.static"},
	{ID: "frontend.style_live_reload", Kind: "predecessor", EvidenceOwner: "tier.browser.workflows"},
	{ID: "frontend.module_live_reload", Kind: "predecessor", EvidenceOwner: "tier.browser.workflows"},
	{ID: "frontend.proxy_boundary", Kind: "predecessor", EvidenceOwner: "tier.frontend.static"},
	{ID: "frontend.control_plane", Kind: "predecessor", EvidenceOwner: "tier.browser.workflows"},
	{ID: "frontend.streaming_uncertainty", Kind: "predecessor", EvidenceOwner: "tier.browser.workflows"},
	{ID: "frontend.privacy", Kind: "predecessor", EvidenceOwner: "tier.security.privacy"},
	{ID: "frontend.production_separation", Kind: "predecessor", EvidenceOwner: "tier.frontend.static"},
	{ID: "frontend.documentation", Kind: "predecessor", EvidenceOwner: "tier.unit.contract"},
}

var evidenceTierRows = []EvidenceTier{
	{ID: "tier.repository.format"},
	{ID: "tier.repository.verify"},
	{ID: "tier.frontend.static"},
	{ID: "tier.unit.contract"},
	{ID: "tier.integration.compatibility"},
	{ID: "tier.browser.workflows"},
	{ID: "tier.browser.visual"},
	{ID: "tier.browser.accessibility"},
	{ID: "tier.browser.cross"},
	{ID: "tier.e2e.complete"},
	{ID: "tier.security.privacy"},
	{ID: "tier.supply_chain.go"},
	{ID: "tier.supply_chain.frontend"},
	{ID: "tier.native.keyring"},
	{ID: "tier.repository.other_tools"},
	{ID: "tier.repository.diff"},
}

func ProductBehaviorManifest() []ProductBehavior {
	rows := make([]ProductBehavior, 0, 140)
	criterionOwners := []string{
		"tier.browser.visual", "tier.browser.workflows", "tier.browser.workflows", "tier.unit.contract", "tier.browser.workflows", "tier.security.privacy", "tier.browser.workflows", "tier.browser.accessibility", "tier.e2e.complete", "tier.frontend.static", "tier.unit.contract",
	}
	for index, id := range productCriterionIDs {
		rows = append(rows, ProductBehavior{ID: "product." + id, Kind: "criterion", EvidenceOwner: criterionOwners[index]})
	}
	for _, group := range productClauseGroups {
		for _, id := range group.ids {
			rows = append(rows, ProductBehavior{ID: "product." + group.prefix + "." + id, Kind: "clause", EvidenceOwner: group.owner})
		}
	}
	for _, id := range productCapabilityIDs {
		rows = append(rows, ProductBehavior{ID: "product.capability." + strings.ReplaceAll(id, "-", "."), Kind: "capability", EvidenceOwner: "tier.browser.workflows"})
	}
	for _, id := range productLifecycleIDs {
		owner := "tier.browser.workflows"
		if strings.HasPrefix(id, "cli-") {
			owner = "tier.e2e.complete"
		}
		rows = append(rows, ProductBehavior{ID: "product.lifecycle." + strings.ReplaceAll(id, "-", "."), Kind: "lifecycle", EvidenceOwner: owner})
	}
	return append([]ProductBehavior(nil), rows...)
}

func SecurityBehaviorManifest() []ProductBehavior {
	rows := make([]ProductBehavior, 0, len(securityBehaviorIDs))
	for _, id := range securityBehaviorIDs {
		rows = append(rows, ProductBehavior{ID: "security." + id, Kind: "security", EvidenceOwner: "tier.security.privacy"})
	}
	return rows
}

func DocumentationBehaviorManifest() []DocumentationBehavior {
	rows := make([]DocumentationBehavior, 0, 40)
	for _, id := range productCapabilityIDs {
		rows = append(rows, DocumentationBehavior{ID: "docs.capability." + strings.ReplaceAll(id, "-", "."), EvidenceOwner: "tier.unit.contract", CanonicalOwner: "administration"})
	}
	for _, id := range documentationTopicIDs {
		rows = append(rows, DocumentationBehavior{ID: "docs.topic." + strings.ReplaceAll(id, "-", "."), EvidenceOwner: "tier.unit.contract", CanonicalOwner: strings.ReplaceAll(id, "-", "_")})
	}
	return rows
}

func PredecessorBehaviorManifest() []ProductBehavior {
	return append([]ProductBehavior(nil), predecessorBehaviorRows...)
}

func EvidenceTierManifest() []EvidenceTier {
	return append([]EvidenceTier(nil), evidenceTierRows...)
}
