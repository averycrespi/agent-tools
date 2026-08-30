package contract

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDocumentationContractDrift(t *testing.T) {
	t.Parallel()

	documents := map[string][]string{
		"../../../README.md": {
			"locally secure, deny-by-default service", "never queues or automatically replays", "mcp-gateway/docs/cli-local-administration.md",
		},
		"../../../CLAUDE.md": {
			"mcp-gateway/docs/release-verification.md", "Gateway release acceptance is a separate owner", "npm run ui:verify-supply-chain", "npm run ui:audit",
		},
		"../../README.md": {
			"## Current capabilities", "## Common workflows", "docs/cli-local-administration.md", "docs/recovery.md",
			"at most one automatic attempt", "deny by default", "Native keyring operations may prompt", "## Coexistence with MCP Broker",
		},
		"../../docs/cli-local-administration.md": {
			"$XDG_DATA_HOME/mcp-gateway", "Online administrator authentication never prompts", "Human output is the default", "The CLI never retries automatically",
		},
		"../../docs/recovery.md": {
			"Gateway must be stopped", "mcp-gateway restore --verify-current", "invalidates every restored agent credential", "does not rewrite the default `admin-bearer`",
		},
		"../../DESIGN.md": {
			"Public HTTP and data contract", "complete public-HTTP online CLI command tree", "`internal/controlclient` is the sole online CLI transport owner",
			"Automated accessibility qualification", "grant_requests", "sole online schema-10 DML owner", "no-check adopter",
		},
		"../../CLAUDE.md": {
			"`internal/contract` is the only source", "`internal/composition` is the sole production constructor", "`internal/remote` is the sole production downstream/OAuth HTTP client",
			"Online CLI commands acquire one selected administrator bearer", "docs/release-verification.md", "npm run ui:verify-supply-chain",
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

func TestCLIUsabilityDocumentationDrift(t *testing.T) {
	t.Parallel()

	readmeBytes, err := os.ReadFile("../../README.md")
	require.NoError(t, err)
	readme := string(readmeBytes)
	quickStart := strings.Index(readme, "## Quick start")
	currentCapabilities := strings.Index(readme, "## Current capabilities")
	require.NotEqual(t, -1, quickStart)
	require.NotEqual(t, -1, currentCapabilities)
	require.Less(t, quickStart, currentCapabilities)
	for _, phrase := range []string{"make install", "mcp-gateway initialize", "mcp-gateway serve", "mcp-gateway status"} {
		require.Contains(t, readme, phrase)
	}
	for _, detailed := range []string{"XDG_DATA_HOME", "--admin-bearer-stdin", "--verify-current", "schema 10"} {
		require.NotContains(t, readme, detailed)
	}

	cliBytes, err := os.ReadFile("../../docs/cli-local-administration.md")
	require.NoError(t, err)
	cli := string(cliBytes)
	for _, phrase := range []string{
		"`--data-dir` has highest precedence", "Online administrator authentication never prompts", "Human output is the default",
		"never accepted in argv or environment", "The CLI never retries automatically", "http://127.0.0.1:8210",
	} {
		require.Contains(t, cli, phrase)
	}
	recoveryBytes, err := os.ReadFile("../../docs/recovery.md")
	require.NoError(t, err)
	recovery := string(recoveryBytes)
	for _, phrase := range []string{"mcp-gateway restore --verify-current", "does not rewrite the default `admin-bearer`", "--admin-bearer-file"} {
		require.Contains(t, recovery, phrase)
	}
	for _, phrase := range []string{
		"the CLI prompts on the controlling terminal without echo",
		"no-echo controlling-terminal prompt",
		"Successful startup emits one safe JSON line",
		"Failures emit one safe JSON line and exit with status 1",
		"refuses JSON output",
	} {
		require.NotContains(t, readme, phrase)
	}

	designBytes, err := os.ReadFile("../../DESIGN.md")
	require.NoError(t, err)
	design := string(designBytes)
	for _, phrase := range []string{
		"Zero-argument `initialize`",
		"resolved default `<root>/admin-bearer`",
		"there is no prompt, argv, or environment fallback",
		"use the public canonical numeric-loopback HTTP API",
		"Human output is the default",
	} {
		require.Contains(t, design, phrase)
	}

	claudeBytes, err := os.ReadFile("../../CLAUDE.md")
	require.NoError(t, err)
	claude := string(claudeBytes)
	require.Contains(t, claude, "There is no prompt, argv, or environment fallback")
	require.Contains(t, claude, "`--data-dir` selects credential location")
}

func TestReadmeRelativeLinksResolve(t *testing.T) {
	t.Parallel()

	contents, err := os.ReadFile("../../README.md")
	require.NoError(t, err)
	links := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`).FindAllStringSubmatch(string(contents), -1)
	require.NotEmpty(t, links)
	for _, match := range links {
		target := match[1]
		if strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		target, _, _ = strings.Cut(target, "#")
		_, err := os.Stat(filepath.Join("../..", target))
		require.NoError(t, err, "README link %q", match[1])
	}
}

func TestDesignArchitectureDataAndProtocolAreCurrent(t *testing.T) {
	t.Parallel()

	document, err := os.ReadFile("../../DESIGN.md")
	require.NoError(t, err)
	text := string(document)

	for _, heading := range []string{
		"## System boundaries and composition",
		"## Public HTTP and data contract",
		"### Request mechanics",
		"### Principal and grant mechanics",
		"### Server and catalog vocabulary",
		"### Strict JSON and policy constraints",
		"### Self-service grant requests",
		"## Storage durability and recovery",
		"### Principal credentials, grants, and policy",
		"### Security mutation and stopped recovery",
		"### Backup and generation replacement",
		"## Server and catalog authority",
		"## Direct stdio supervision",
		"## Downstream protocol and remote transport",
		"### Governed invocation and audit evidence",
		"### OAuth authority and catalog publication",
		"## MCP ingress and governed invocation",
	} {
		require.Contains(t, text, heading)
	}

	ownedRanges := [][2]string{
		{"## Purpose", "## Administrative authority and sessions"},
		{"## Keyring capability and generation cutover", "## Verification and release evidence"},
		{"## MCP ingress and governed invocation", "## Operational composition and compatibility"},
	}
	phaseResidue := regexp.MustCompile(`(?i)\bS[1-6](?:\.\d+)?\b|\bimplemented\b|\bfoundation\b|\btask owner\b|\bmilestone owner\b`)
	for _, bounds := range ownedRanges {
		start := strings.Index(text, bounds[0])
		end := strings.Index(text, bounds[1])
		require.NotEqual(t, -1, start, bounds[0])
		require.Greater(t, end, start, bounds[1])
		owned := strings.ReplaceAll(text[start:end], "`mcp-gateway/s2`", "")
		require.Empty(t, phaseResidue.FindString(owned), "%s", bounds[0])
	}

	behaviorClaims := map[string]string{
		"product.server_catalog.state_separation":          "durable desired authority remains distinct from process-local runtime and active catalog state",
		"product.server_catalog.active_vs_durable_catalog": "Only exactly current active generations are discoverable and callable",
		"product.access_policy.immutable_grants":           "Ordinary grants are immutable rows",
		"product.access_policy.closed_constraints":         "Constraints compile only the closed",
		"product.invocation.missing_terminal_unknown":      "Missing terminal evidence means the outcome is unknown",
		"product.invocation.local_target_unknown":          "Local post-commit uncertainty returns `tool_unavailable`",
		"product.invocation.bounded_retention":             "post-insert maximum of 4,096",
		"security.invocation.audit_capture":                "Argument capture uses one fixed recursive key redactor",
		"security.storage.sqlite_backups":                  "SQLite uses application ID `MGW1`",
		"security.process.output":                          "no raw process output",
	}
	manifestIDs := make(map[string]struct{})
	for _, behavior := range ProductBehaviorManifest() {
		manifestIDs[behavior.ID] = struct{}{}
	}
	for _, behavior := range SecurityBehaviorManifest() {
		manifestIDs[behavior.ID] = struct{}{}
	}
	for id, claim := range behaviorClaims {
		require.Contains(t, manifestIDs, id)
		require.Contains(t, text, claim, id)
	}
}

func TestDesignAdministrationBrowserOperationsAndReleaseAreCurrent(t *testing.T) {
	t.Parallel()

	document, err := os.ReadFile("../../DESIGN.md")
	require.NoError(t, err)
	text := string(document)

	for _, heading := range []string{
		"## Administrative authority and sessions",
		"## Verification and release evidence",
		"## HTTP administration and control-plane clients",
		"### Browser production and development boundaries",
		"### Browser state and workflow ownership",
		"### CLI authority, output, and recovery",
		"## Events, limits, and shutdown",
		"## Operational composition and compatibility",
		"## Non-goals",
	} {
		require.Contains(t, text, heading)
	}

	currentDesign := strings.ReplaceAll(text, "`mcp-gateway/s2`", "")
	chronology := regexp.MustCompile(`(?i)\bS[1-6](?:\.\d+)?\b|\bAC-[0-9]+\b|\bimplemented\b|\bfoundation\b|\btask owner\b|\bmilestone owner\b|planned.{0,20}executable`)
	require.Empty(t, chronology.FindString(currentDesign))

	for _, guide := range []string{
		"docs/cli-local-administration.md",
		"docs/server-configuration.md",
		"docs/access-policy.md",
		"docs/invocation-evidence.md",
		"docs/recovery.md",
		"docs/frontend-development.md",
		"docs/release-verification.md",
	} {
		require.Equal(t, 1, strings.Count(text, "("+guide+")"), guide)
	}

	for _, claim := range []string{
		"Bearer exchange reserves one of 128 in-memory session slots",
		"Epoch changes abort tracked requests/streams",
		"never replay a mutation",
		"The checked-in bundle contains no external or inline active content",
		"OAuth URLs are text, never links",
		"All polling pauses while hidden",
		"Linux Chromium is blocking",
		"there is no prompt, argv, or environment fallback",
		"No command polls, refetches a precondition",
		"`accept` composes disjoint leaves directly",
		"Reports from superseded definitions are incompatible",
		"`skipped` remains additive",
		"no-check adopter",
		"clean unchanged revision",
		"A second signal invokes immediate forced exit",
		"Product compatibility preserves public HTTP and MCP JSON",
	} {
		require.Contains(t, text, claim)
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
