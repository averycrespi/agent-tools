package contract

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTrackedDocsDescribeCurrentS3Boundary(t *testing.T) {
	t.Parallel()

	documents := map[string][]string{
		"../../README.md": {
			"principal-filtered descriptor discovery", "Principals, credentials, and grants", "agent_credential_creation",
			"`tools/call` and every other non-lifecycle feature method return fixed `-32601 Method not found`; no active capability is acquired",
			"Backup restore preserves principals and grants but clears every restored agent credential",
		},
		"../../DESIGN.md": {
			"positive principal-credential authentication", "Implemented S3 request mechanics", "Agent discovery enumerates descriptor projections independently without acquiring capabilities",
			"S3 principal credentials authenticate production `/mcp`", "They do not enable a production capability consumer, tool invocation or invocation audit",
		},
		"../../CLAUDE.md": {
			"Immutable S1–S3 routes", "principal/credential/grant/current-catalog/raw-MCP helpers", "Production does authenticate current principal credentials",
			"do not infer `tools/call` routing, durable invocation/audit, Gateway-owned self-service tools, or product-UI behavior",
		},
	}
	prohibited := []string{
		"the executable does not yet accept production agent authority",
		"production MCP authentication is unavailable",
		"are not yet used for positive MCP authentication",
		"do not infer positive agent authentication",
		"no endpoint emits CORS headers or ETags",
		"sole S1/S2 vocabulary owner",
		"The current S2 checkpoint",
		"The final S1 binary",
		"cannot authorize, audit, persist, present, enumerate to agents",
	}
	for path, required := range documents {
		contents, err := os.ReadFile(path)
		require.NoError(t, err, path)
		text := string(contents)
		for _, phrase := range required {
			require.Contains(t, text, phrase, "%s: missing current S3 claim", path)
		}
		for _, phrase := range prohibited {
			require.NotContains(t, text, phrase, "%s: obsolete current-state claim", path)
		}
	}
}

func TestDesignDocumentsTheExecutableContract(t *testing.T) {
	t.Parallel()

	document, err := os.ReadFile("../../DESIGN.md")
	require.NoError(t, err)
	text := string(document)

	for _, route := range Routes() {
		require.Contains(t, text, "`"+route.Pattern+"`", route.Pattern)
		require.Contains(t, text, "`"+route.Allow()+"`", route.Pattern)
	}
	for _, problem := range Problems() {
		require.Contains(t, text, "`"+string(problem.Code)+"`", problem.Code)
		require.Contains(t, text, problem.Title, problem.Code)
	}
	for _, limit := range FixedLimits() {
		require.Contains(t, text, "`"+limit.Name+"`", limit.Name)
		require.Contains(t, text, strconv.FormatInt(limit.Maximum, 10), limit.Name)
	}
	for _, mechanic := range ResourceMechanics() {
		if mechanic.RequestSchema != "" {
			require.Contains(t, text, "`"+mechanic.RequestSchema+"`", mechanic.Pattern+" "+mechanic.Method)
		}
		if mechanic.SuccessSchema != "" {
			require.Contains(t, text, "`"+mechanic.SuccessSchema+"`", mechanic.Pattern+" "+mechanic.Method)
		}
	}
	for _, kind := range InvalidationKinds() {
		require.Contains(t, text, "`"+string(kind)+"`", kind)
	}
	for _, sink := range ApprovedSecretSinks() {
		require.Contains(t, text, "`"+string(sink)+"`", sink)
	}
	for _, value := range []string{
		DefaultAuthority,
		CanonicalOrigin,
		ModernProtocolVersion,
		LegacyProtocolVersion,
		MediaTypeJSON,
		MediaTypeProblemJSON,
		MediaTypeEventStream,
		"stdio", "streamable_http", "auto", "modern", "legacy", "none", "bearer", "oauth", "static", "dynamic",
		"client_secret_basic", "client_secret_post", "enabled", "disabled", "deleted", "inactive", "activating", "active", "stopping",
		"retry_wait", "degraded", "authentication_required", "activate", "reload", "retry", "refresh_catalog", "credential_replace", "disable",
		"delete", "disconnect_credentials", "scheduled", "running", "succeeded", "failed", "cancelled", "superseded", "interrupted", "not_required",
		"ready", "absent", "locked", "interaction_required", "unavailable", "unsupported", "refreshing", "reauthentication_required", "disconnecting",
		"cleanup_pending", "empty", "current", "stale", "retired", "preparing", "awaiting_callback", "exchanging", "expired", "include", "exclude", "only",
		"configuration_invalid", "resource_limit", "connectivity", "tls_failed", "protocol_unsupported", "protocol_invalid", "authentication_rejected",
		"credential_absent", "keyring_absent", "keyring_locked", "keyring_interaction_required", "keyring_unavailable", "keyring_unsupported", "oauth_rejected",
		"oauth_expired", "registration_expired", "process_exited", "output_limit", "stop_unconfirmed", "catalog_invalid", "catalog_limit", "catalog_stale",
		"revocation_failed", "revocation_unsupported", AgentBearerPrefix, SyntheticServerID, SyntheticServerNamespace,
		string(AgentAuthPrincipalCredentials), string(PrincipalActive), string(PrincipalDisabled), string(VisibilityRequestable), string(VisibilityAllowedOnly),
		string(VisibilityAll), string(GrantAllow), string(GrantDeny), string(GrantActive), string(GrantExpired), string(DecisionAllow), string(DecisionDeny), string(DecisionBlock),
	} {
		require.True(t, strings.Contains(text, "`"+value+"`") || strings.Contains(text, value), value)
	}
}
