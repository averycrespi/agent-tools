package contract

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestS6DocumentationDrift(t *testing.T) {
	t.Parallel()

	documents := map[string][]string{
		"../../../README.md": {
			"embedded browser control plane", "complete online CLI", "six fixed local self-service handlers", "only an explicit fresh call can use a resulting grant",
		},
		"../../../CLAUDE.md": {
			"S6 manifest-controlled task, milestone, and final owners", "npm run ui:verify-supply-chain", "npm run ui:audit",
		},
		"../../README.md": {
			"Web application, recovery, and accessibility", "Online commands use public HTTP only", "The retained projection distinguishes", "poll every two visible seconds",
			"browser_one_time_display", "stopped-process `initialize`", "npm run ui:verify-supply-chain", "Qualification treats Linux Chromium", "go-keyring` v0.2.7 is context-free",
		},
		"../../DESIGN.md": {
			"Closed S1–S6 contract", "complete public-HTTP online CLI command tree", "`internal/controlclient` is the sole online CLI transport owner",
			"Automated accessibility qualification", "grant_requests", "sole online schema-10 DML owner", "no-check adopter",
		},
		"../../CLAUDE.md": {
			"implemented S1–S6 routes", "online S4 invocation SQL belongs to `internal/invocation`", "online schema-10 request identity",
			"browser, visual, accessibility, and external-evidence tiers", "npm run ui:verify-supply-chain", "exactly-once effects", "guaranteed secret detection",
		},
	}
	prohibited := []string{
		"Product UI, exactly-once guarantees, and broader S6 workflows are not implemented",
		"later S6 capabilities are not yet available",
		"remaining domain product workflows are pending",
		"remaining domain product workflows are later slices",
		"Closed S1–S5 contract",
		"accept-s5` is the sole final blocking owner",
		"accept-s5` is the sole current blocking owner",
		"Repository and handler adoption follow as one bounded S6 slice",
		"future S4 admission mutation",
		"Product workflows beyond the typed embedded shell",
		"the executable does not yet accept production agent authority",
		"production MCP authentication is unavailable",
		"No active downstream route is callable through `/mcp`",
		"S5/S6 workflows are not implemented",
		"There is no audit read API",
	}
	for path, required := range documents {
		contents, err := os.ReadFile(path)
		require.NoError(t, err, path)
		text := string(contents)
		for _, phrase := range required {
			require.Contains(t, text, phrase, "%s: missing current S6 claim", path)
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
