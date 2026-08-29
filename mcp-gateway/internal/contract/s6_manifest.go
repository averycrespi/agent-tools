package contract

import "fmt"

const S6ManifestVersion = 1

type S6CapabilityRow struct {
	ID             string
	Operation      string
	WebControl     string
	CLIUses        []string
	Mechanics      string
	WebScenario    string
	CLIScenario    string
	Implementation []string
}

type S6DocumentationRow struct {
	ID      string
	Subject string
	Targets []string
}

var s6AcceptanceEvidenceManifest = []AcceptanceEvidence{
	{Criterion: "AC-1", Evidence: []string{"browser-visual", "browser-a11y", "capability-matrix"}},
	{Criterion: "AC-2", Evidence: []string{"browser-protocol", "browser-workflows", "privacy"}},
	{Criterion: "AC-3", Evidence: []string{"capability-matrix", "browser-workflows", "cli-e2e"}},
	{Criterion: "AC-4", Evidence: []string{"invocation-contract", "invocation-integration", "browser-workflows", "cli-e2e"}},
	{Criterion: "AC-5", Evidence: []string{"browser-workflows", "integration-compat", "stress"}},
	{Criterion: "AC-6", Evidence: []string{"security-privacy", "browser-workflows", "cli-e2e", "static-supply-chain"}},
	{Criterion: "AC-7", Evidence: []string{"browser-workflows", "cli-e2e"}},
	{Criterion: "AC-8", Evidence: []string{"browser-a11y", "browser-visual", "browser-cross", "external-evidence"}},
	{Criterion: "AC-9", Evidence: []string{"cli-e2e", "capability-matrix"}},
	{Criterion: "AC-10", Evidence: []string{"frontend", "static-supply-chain", "security-privacy"}},
	{Criterion: "AC-11", Evidence: []string{"validation-inventory", "integration-compat", "documentation", "accept-s6"}},
}

var s6ClauseCounts = []int{8, 8, 4, 9, 7, 6, 4, 12, 7, 7, 10, 8}

var s6SectionTasks = [][]string{
	{"T20", "T22", "T37", "T50", "T51"}, {"T3", "T5", "T10", "T11", "T12", "T13"},
	{"T19", "T20", "T22"}, {"T23", "T24", "T25", "T26", "T27", "T28", "T38", "T39", "T40", "T41", "T42", "T43"},
	{"T29", "T30", "T31", "T32", "T46", "T47", "T48"}, {"T33", "T34", "T49"},
	{"T35", "T36", "T44", "T45", "T56"}, {"T6", "T7", "T8", "T9", "T21"},
	{"T4", "T12", "T13"}, {"T15", "T18", "T26", "T30", "T35", "T42", "T44", "T47", "T52"},
	{"T16", "T17", "T18", "T19", "T38", "T39", "T40", "T41", "T42", "T43", "T44", "T45", "T46", "T47", "T48", "T49"},
	{"T10", "T14", "T50", "T51", "T53"},
}

func S6AcceptanceEvidenceManifest() []AcceptanceEvidence {
	result := make([]AcceptanceEvidence, len(s6AcceptanceEvidenceManifest))
	for index, entry := range s6AcceptanceEvidenceManifest {
		result[index] = entry
		result[index].Evidence = append([]string(nil), entry.Evidence...)
	}
	return result
}

func S6ClauseEvidenceManifest() []ClauseEvidence {
	result := make([]ClauseEvidence, 0, 90)
	for section, count := range s6ClauseCounts {
		for clause := 1; clause <= count; clause++ {
			result = append(result, ClauseEvidence{
				Clause:   fmt.Sprintf("RB-%d.%d", section+1, clause),
				Tasks:    append([]string(nil), s6SectionTasks[section]...),
				Evidence: []string{"task", "milestone-gate", "accept-s6"},
			})
		}
	}
	return result
}

var s6CapabilityRows = []S6CapabilityRow{
	{ID: "status", Operation: "Detailed status", WebControl: "Overview/System", CLIUses: []string{"status"}, Mechanics: "GET status", Implementation: []string{"T19", "T20", "T22"}},
	{ID: "admin-credential-read", Operation: "Admin credential list/get", WebControl: "System / Admin credentials", CLIUses: []string{"admin-credential list", "admin-credential get ID"}, Mechanics: "cursor/limit; bodyless", Implementation: []string{"T35", "T44"}},
	{ID: "admin-credential-create", Operation: "Admin credential create", WebControl: "System / Admin credentials / Create", CLIUses: []string{"admin-credential create --file PATH [--secret-output NEW_PATH]"}, Mechanics: "one-time sink; inherited body", Implementation: []string{"T35", "T44"}},
	{ID: "admin-credential-revoke", Operation: "Admin credential revoke", WebControl: "credential detail", CLIUses: []string{"admin-credential revoke ID"}, Mechanics: "exact {}; confirmation", Implementation: []string{"T35", "T44"}},
	{ID: "backup-read", Operation: "Backup list/get", WebControl: "System / Backups", CLIUses: []string{"backup list", "backup get BACKUP_ID"}, Mechanics: "cursor/limit; bodyless", Implementation: []string{"T36", "T45"}},
	{ID: "backup-create", Operation: "Backup create", WebControl: "System / Backups / Create", CLIUses: []string{"backup create"}, Mechanics: "exact {}; idempotency key", Implementation: []string{"T36", "T45"}},
	{ID: "backup-delete", Operation: "Backup delete", WebControl: "backup detail", CLIUses: []string{"backup delete BACKUP_ID"}, Mechanics: "exact {}; confirmation", Implementation: []string{"T36", "T45"}},
	{ID: "server-read", Operation: "Server list/get", WebControl: "Servers", CLIUses: []string{"server list", "server get ID"}, Mechanics: "cursor/limit; bodyless", Implementation: []string{"T23", "T38"}},
	{ID: "server-create", Operation: "Server create", WebControl: "Servers / Create", CLIUses: []string{"server create --file PATH"}, Mechanics: "body; idempotency key", Implementation: []string{"T24", "T39"}},
	{ID: "server-update", Operation: "Server update", WebControl: "server detail / Edit", CLIUses: []string{"server update ID --etag ETAG --file PATH"}, Mechanics: "nonempty patch; ETag", Implementation: []string{"T24", "T39"}},
	{ID: "server-delete", Operation: "Server delete", WebControl: "server detail / Delete", CLIUses: []string{"server delete ID --etag ETAG"}, Mechanics: "exact {}; typed namespace/--yes; ETag", Implementation: []string{"T28", "T40"}},
	{ID: "operation-read", Operation: "Operation list/get", WebControl: "server detail / Operations", CLIUses: []string{"server operation list ID", "server operation get ID OPERATION_ID"}, Mechanics: "cursor/limit; bodyless", Implementation: []string{"T25", "T41"}},
	{ID: "operation-start", Operation: "Explicit operation", WebControl: "server detail action", CLIUses: []string{"server operation start ID --etag ETAG --file PATH"}, Mechanics: "closed body; ETag; idempotency key", Implementation: []string{"T25", "T41"}},
	{ID: "server-credential", Operation: "Credential replacement", WebControl: "server detail / Credentials", CLIUses: []string{"server credential replace ID --etag ETAG --file PATH"}, Mechanics: "write-only; ETag; confirmation; no replay", Implementation: []string{"T26", "T42"}},
	{ID: "auth-flow-read", Operation: "Auth-flow list/get", WebControl: "server detail / OAuth", CLIUses: []string{"server auth-flow list ID", "server auth-flow get ID FLOW_ID"}, Mechanics: "cursor/limit; bodyless", Implementation: []string{"T27", "T43"}},
	{ID: "auth-flow-start", Operation: "Auth-flow start", WebControl: "server detail / OAuth / Authorize", CLIUses: []string{"server auth-flow start ID --etag ETAG [--open]"}, Mechanics: "exact {}; ETag; one-time URL sink", Implementation: []string{"T27", "T43"}},
	{ID: "auth-flow-cancel", Operation: "Auth-flow cancel", WebControl: "flow detail / Cancel", CLIUses: []string{"server auth-flow cancel ID FLOW_ID"}, Mechanics: "exact {}; eligible; confirmation", Implementation: []string{"T27", "T43"}},
	{ID: "descriptor-read", Operation: "Descriptor list/get", WebControl: "server detail / Descriptors", CLIUses: []string{"server descriptor list ID", "server descriptor get ID TOOL_ID"}, Mechanics: "retired/cursor/limit; bodyless", Implementation: []string{"T23", "T38"}},
	{ID: "catalog-list", Operation: "Active catalog list", WebControl: "Catalog", CLIUses: []string{"catalog list"}, Mechanics: "cursor/limit; bodyless", Implementation: []string{"T23", "T38"}},
	{ID: "principal-read", Operation: "Principal list/get", WebControl: "Access / Principals", CLIUses: []string{"principal list", "principal get ID"}, Mechanics: "cursor/limit; bodyless", Implementation: []string{"T29", "T46"}},
	{ID: "principal-create", Operation: "Principal create", WebControl: "Access / Principals / Create", CLIUses: []string{"principal create --file PATH"}, Mechanics: "inherited body", Implementation: []string{"T29", "T46"}},
	{ID: "principal-update", Operation: "Principal update", WebControl: "principal detail / Edit", CLIUses: []string{"principal update ID --etag ETAG --file PATH"}, Mechanics: "patch; ETag; confirmation", Implementation: []string{"T29", "T46"}},
	{ID: "principal-credential-issue", Operation: "Agent credential issue", WebControl: "principal detail / Credential", CLIUses: []string{"principal credential issue ID --etag ETAG [--secret-output NEW_PATH]"}, Mechanics: "exact {}; ETag; one-time sink; confirmation", Implementation: []string{"T30", "T47"}},
	{ID: "principal-credential-revoke", Operation: "Agent credential revoke", WebControl: "principal detail / Credential", CLIUses: []string{"principal credential revoke ID --etag ETAG"}, Mechanics: "exact {}; ETag; confirmation", Implementation: []string{"T30", "T47"}},
	{ID: "grant-read", Operation: "Grant list/get", WebControl: "Access / Grants", CLIUses: []string{"grant list", "grant get ID"}, Mechanics: "filters/cursor/limit; bodyless", Implementation: []string{"T31", "T48"}},
	{ID: "grant-create", Operation: "Grant create", WebControl: "Access / Grants / Create", CLIUses: []string{"grant create --file PATH"}, Mechanics: "complete body", Implementation: []string{"T31", "T48"}},
	{ID: "grant-delete", Operation: "Grant delete", WebControl: "grant detail", CLIUses: []string{"grant delete ID"}, Mechanics: "bodyless; confirmation", Implementation: []string{"T32", "T48"}},
	{ID: "grant-request-read", Operation: "Grant-request list/get", WebControl: "Requests", CLIUses: []string{"grant-request list", "grant-request get REQUEST_ID"}, Mechanics: "filters/cursor/limit; bodyless", Implementation: []string{"T33", "T49"}},
	{ID: "grant-request-approve", Operation: "Grant-request approve", WebControl: "request detail / Review", CLIUses: []string{"grant-request approve REQUEST_ID --etag ETAG --file PATH"}, Mechanics: "narrowing; ETag; confirmation; no replay", Implementation: []string{"T34", "T49"}},
	{ID: "grant-request-reject", Operation: "Grant-request reject", WebControl: "request detail / Review", CLIUses: []string{"grant-request reject REQUEST_ID --etag ETAG --file PATH"}, Mechanics: "reason; ETag; confirmation; no replay", Implementation: []string{"T34", "T49"}},
	{ID: "invocation-read", Operation: "Invocation list/get", WebControl: "Invocations", CLIUses: []string{"invocation list", "invocation get INVOCATION_ID"}, Mechanics: "filters/cursor/limit; bodyless", Implementation: []string{"T21", "T19"}},
}

func S6CapabilityManifest() []S6CapabilityRow {
	result := make([]S6CapabilityRow, len(s6CapabilityRows))
	for index, row := range s6CapabilityRows {
		result[index] = row
		result[index].CLIUses = append([]string(nil), row.CLIUses...)
		result[index].Implementation = append([]string(nil), row.Implementation...)
		result[index].WebScenario = "browser." + row.ID
		result[index].CLIScenario = "cli." + row.ID
	}
	return result
}

func S6LifecycleCapabilityManifest() []S6CapabilityRow {
	return []S6CapabilityRow{
		{ID: "web-exchange", Operation: "Browser bearer exchange", WebControl: "Sign in", Mechanics: "POST exchange", WebScenario: "browser.exchange", Implementation: []string{"T5", "T11"}},
		{ID: "web-bootstrap", Operation: "Browser session bootstrap", WebControl: "Application bootstrap", Mechanics: "POST current session", WebScenario: "browser.bootstrap", Implementation: []string{"T3", "T5", "T11"}},
		{ID: "web-logout", Operation: "Browser logout", WebControl: "Sign out", Mechanics: "POST logout", WebScenario: "browser.logout", Implementation: []string{"T5", "T11"}},
		{ID: "web-events", Operation: "Browser invalidations", WebControl: "Application refresh", Mechanics: "POST event stream", WebScenario: "browser.events", Implementation: []string{"T4", "T12"}},
		{ID: "cli-bearer", Operation: "CLI bearer acquisition", CLIUses: []string{"--admin-bearer-file PATH", "--admin-bearer-stdin"}, Mechanics: "owner-only file/stdin/prompt", CLIScenario: "cli.bearer", Implementation: []string{"T16", "T17"}},
		{ID: "cli-initialize", Operation: "Stopped initialize", CLIUses: []string{"initialize"}, Mechanics: "offline compatibility", CLIScenario: "cli.initialize", Implementation: []string{"T55"}},
		{ID: "cli-admin-reset", Operation: "Stopped admin reset", CLIUses: []string{"admin-reset"}, Mechanics: "offline compatibility", CLIScenario: "cli.admin-reset", Implementation: []string{"T55"}},
		{ID: "cli-restore", Operation: "Stopped restore", CLIUses: []string{"restore"}, Mechanics: "offline compatibility", CLIScenario: "cli.restore", Implementation: []string{"T55"}},
	}
}

func S6DocumentationManifest() []S6DocumentationRow {
	rows := make([]S6DocumentationRow, 0, 40)
	for _, capability := range s6CapabilityRows {
		rows = append(rows, S6DocumentationRow{ID: "capability-" + capability.ID, Subject: capability.Operation, Targets: []string{"mcp-gateway/README.md", "command-help"}})
	}
	for _, topic := range []string{"web-sign-in-recovery", "online-cli-authentication", "one-time-sinks", "invocation-outcomes", "refresh-polling", "offline-recovery", "frontend-development", "browser-accessibility", "keyring-limitations"} {
		targets := []string{"mcp-gateway/README.md", "mcp-gateway/DESIGN.md", "mcp-gateway/CLAUDE.md"}
		if topic == "web-sign-in-recovery" {
			targets = append(targets, "README.md")
		}
		if topic == "frontend-development" {
			targets = append(targets, "CLAUDE.md")
		}
		rows = append(rows, S6DocumentationRow{ID: "topic-" + topic, Subject: topic, Targets: targets})
	}
	return rows
}
