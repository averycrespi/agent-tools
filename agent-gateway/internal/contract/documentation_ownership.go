package contract

const DocumentationOwnershipManifestVersion = 2

type DocumentationGuide struct {
	ID       string
	Path     string
	Audience string
	Purpose  string
}

type DocumentationCommandFamily struct {
	ID             string
	CommandPath    string
	CanonicalOwner string
	HelpInvocation string
}

type DocumentationSecurityContract struct {
	ID             string
	CanonicalOwner string
	HelpFamilies   []string
}

var documentationGuides = []DocumentationGuide{
	{ID: "docs.guide.installation.safety", Path: "docs/operators/installation-safety.md", Audience: "Operators maintaining existing Gateway installations", Purpose: "Select the existing installation and retain post-migration safety and recovery artifacts."},
	{ID: "docs.guide.launchd", Path: "docs/operators/launchd.md", Audience: "Gateway operators using a logged-in macOS desktop", Purpose: "Install, verify, and manage a per-user LaunchAgent"},
	{ID: "docs.guide.cli.local.administration", Path: "docs/operators/administration.md", Audience: "Gateway operators and automation authors", Purpose: "Run local administration safely through the public CLI."},
	{ID: "docs.guide.server.configuration", Path: "docs/operators/upstream-servers.md", Audience: "Gateway operators configuring upstream MCP servers", Purpose: "Configure servers, credentials, and OAuth without broadening trust."},
	{ID: "docs.guide.access.policy", Path: "docs/operators/access-control.md", Audience: "Gateway administrators managing agent access", Purpose: "Manage principals, credentials, grants, and grant requests."},
	{ID: "docs.guide.invocation.evidence", Path: "docs/operators/invocation-evidence.md", Audience: "Operators investigating governed tool calls", Purpose: "Interpret invocation evidence, redaction, and unknown outcomes."},
	{ID: "docs.guide.recovery", Path: "docs/operators/backup-and-recovery.md", Audience: "Operators responsible for Gateway recovery", Purpose: "Create backups and perform restore or stopped-process recovery safely."},
	{ID: "docs.guide.frontend.development", Path: "docs/maintainers/frontend-development.md", Audience: "Maintainers developing the Gateway web application", Purpose: "Run trusted live reload without changing the production asset boundary."},
	{ID: "docs.guide.release.verification", Path: "docs/maintainers/release-verification.md", Audience: "Release owners and maintainers preparing release evidence", Purpose: "Prepare, run, and adopt exact-revision acceptance evidence without turning release acceptance into a development loop."},
}

var documentationCommandFamilies = []DocumentationCommandFamily{
	{ID: "docs.command.http", CommandPath: "http credential", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "agent-gateway http credential --help"},
	{ID: "docs.command.service", CommandPath: "service", CanonicalOwner: "docs/operators/launchd.md", HelpInvocation: "agent-gateway service --help"},
	{ID: "docs.command.audit", CommandPath: "audit", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "agent-gateway audit --help"},
	{ID: "docs.command.admin.credential", CommandPath: "admin credential", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "agent-gateway admin credential --help"},
	{ID: "docs.command.admin.reset", CommandPath: "admin reset", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpInvocation: "agent-gateway admin reset --help"},
	{ID: "docs.command.backup", CommandPath: "backup", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpInvocation: "agent-gateway backup --help"},
	{ID: "docs.command.catalog", CommandPath: "mcp catalog", CanonicalOwner: "docs/operators/upstream-servers.md", HelpInvocation: "agent-gateway mcp catalog --help"},
	{ID: "docs.command.grant", CommandPath: "mcp grant", CanonicalOwner: "docs/operators/access-control.md", HelpInvocation: "agent-gateway mcp grant --help"},
	{ID: "docs.command.grant.request", CommandPath: "mcp grant-request", CanonicalOwner: "docs/operators/access-control.md", HelpInvocation: "agent-gateway mcp grant-request --help"},
	{ID: "docs.command.initialize", CommandPath: "initialize", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "agent-gateway initialize --help"},
	{ID: "docs.command.invocation", CommandPath: "mcp invocation", CanonicalOwner: "docs/operators/invocation-evidence.md", HelpInvocation: "agent-gateway mcp invocation --help"},
	{ID: "docs.command.principal", CommandPath: "principal", CanonicalOwner: "docs/operators/access-control.md", HelpInvocation: "agent-gateway principal --help"},
	{ID: "docs.command.storage", CommandPath: "storage", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpInvocation: "agent-gateway storage --help"},
	{ID: "docs.command.serve", CommandPath: "serve", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "agent-gateway serve --help"},
	{ID: "docs.command.server", CommandPath: "mcp server", CanonicalOwner: "docs/operators/upstream-servers.md", HelpInvocation: "agent-gateway mcp server --help"},
	{ID: "docs.command.status", CommandPath: "status", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "agent-gateway status --help"},
}

var documentationSecurityContracts = []DocumentationSecurityContract{
	{ID: "docs.security.admin.authority", CanonicalOwner: "docs/operators/administration.md", HelpFamilies: []string{"initialize", "admin credential"}},
	{ID: "docs.security.local.control", CanonicalOwner: "docs/operators/administration.md", HelpFamilies: []string{"serve", "status"}},
	{ID: "docs.security.one.time.sinks", CanonicalOwner: "docs/operators/administration.md", HelpFamilies: []string{"admin credential", "principal", "mcp server"}},
	{ID: "docs.security.server.credentials.oauth", CanonicalOwner: "docs/operators/upstream-servers.md", HelpFamilies: []string{"mcp server"}},
	{ID: "docs.security.principal.policy", CanonicalOwner: "docs/operators/access-control.md", HelpFamilies: []string{"principal", "mcp grant", "mcp grant-request"}},
	{ID: "docs.security.invocation.uncertainty", CanonicalOwner: "docs/operators/invocation-evidence.md", HelpFamilies: []string{"mcp invocation"}},
	{ID: "docs.security.backup.recovery", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpFamilies: []string{"admin reset", "backup", "storage"}},
	{ID: "docs.security.frontend.trust", CanonicalOwner: "docs/maintainers/frontend-development.md", HelpFamilies: []string{"serve"}},
}

func DocumentationGuideManifest() []DocumentationGuide {
	return append([]DocumentationGuide(nil), documentationGuides...)
}

func DocumentationCommandManifest() []DocumentationCommandFamily {
	return append([]DocumentationCommandFamily(nil), documentationCommandFamilies...)
}

func DocumentationSecurityManifest() []DocumentationSecurityContract {
	rows := make([]DocumentationSecurityContract, len(documentationSecurityContracts))
	for index, row := range documentationSecurityContracts {
		rows[index] = row
		rows[index].HelpFamilies = append([]string(nil), row.HelpFamilies...)
	}
	return rows
}
