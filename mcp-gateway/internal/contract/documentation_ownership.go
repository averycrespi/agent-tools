package contract

const DocumentationOwnershipManifestVersion = 1

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
	{ID: "docs.guide.cli.local.administration", Path: "docs/operators/administration.md", Audience: "Gateway operators and automation authors", Purpose: "Run local administration safely through the public CLI."},
	{ID: "docs.guide.server.configuration", Path: "docs/operators/upstream-servers.md", Audience: "Gateway operators configuring upstream MCP servers", Purpose: "Configure servers, credentials, and OAuth without broadening trust."},
	{ID: "docs.guide.access.policy", Path: "docs/operators/access-control.md", Audience: "Gateway administrators managing agent access", Purpose: "Manage principals, credentials, grants, and grant requests."},
	{ID: "docs.guide.invocation.evidence", Path: "docs/operators/invocation-evidence.md", Audience: "Operators investigating governed tool calls", Purpose: "Interpret invocation evidence, redaction, and unknown outcomes."},
	{ID: "docs.guide.recovery", Path: "docs/operators/backup-and-recovery.md", Audience: "Operators responsible for Gateway recovery", Purpose: "Create backups and perform restore or stopped-process recovery safely."},
	{ID: "docs.guide.frontend.development", Path: "docs/maintainers/frontend-development.md", Audience: "Maintainers developing the Gateway web application", Purpose: "Run trusted live reload without changing the production asset boundary."},
	{ID: "docs.guide.release.verification", Path: "docs/maintainers/release-verification.md", Audience: "Release owners and maintainers preparing release evidence", Purpose: "Prepare, run, and adopt exact-revision acceptance evidence without turning release acceptance into a development loop."},
}

var documentationCommandFamilies = []DocumentationCommandFamily{
	{ID: "docs.command.admin.credential", CommandPath: "admin credential", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "mcp-gateway admin credential --help"},
	{ID: "docs.command.admin.reset", CommandPath: "admin reset", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpInvocation: "mcp-gateway admin reset --help"},
	{ID: "docs.command.backup", CommandPath: "backup", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpInvocation: "mcp-gateway backup --help"},
	{ID: "docs.command.catalog", CommandPath: "catalog", CanonicalOwner: "docs/operators/upstream-servers.md", HelpInvocation: "mcp-gateway catalog --help"},
	{ID: "docs.command.grant", CommandPath: "grant", CanonicalOwner: "docs/operators/access-control.md", HelpInvocation: "mcp-gateway grant --help"},
	{ID: "docs.command.grant.request", CommandPath: "grant-request", CanonicalOwner: "docs/operators/access-control.md", HelpInvocation: "mcp-gateway grant-request --help"},
	{ID: "docs.command.initialize", CommandPath: "initialize", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "mcp-gateway initialize --help"},
	{ID: "docs.command.invocation", CommandPath: "invocation", CanonicalOwner: "docs/operators/invocation-evidence.md", HelpInvocation: "mcp-gateway invocation --help"},
	{ID: "docs.command.principal", CommandPath: "principal", CanonicalOwner: "docs/operators/access-control.md", HelpInvocation: "mcp-gateway principal --help"},
	{ID: "docs.command.restore", CommandPath: "restore", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpInvocation: "mcp-gateway restore --help"},
	{ID: "docs.command.serve", CommandPath: "serve", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "mcp-gateway serve --help"},
	{ID: "docs.command.server", CommandPath: "server", CanonicalOwner: "docs/operators/upstream-servers.md", HelpInvocation: "mcp-gateway server --help"},
	{ID: "docs.command.status", CommandPath: "status", CanonicalOwner: "docs/operators/administration.md", HelpInvocation: "mcp-gateway status --help"},
}

var documentationSecurityContracts = []DocumentationSecurityContract{
	{ID: "docs.security.admin.authority", CanonicalOwner: "docs/operators/administration.md", HelpFamilies: []string{"initialize", "admin credential"}},
	{ID: "docs.security.local.control", CanonicalOwner: "docs/operators/administration.md", HelpFamilies: []string{"serve", "status"}},
	{ID: "docs.security.one.time.sinks", CanonicalOwner: "docs/operators/administration.md", HelpFamilies: []string{"admin credential", "principal", "server"}},
	{ID: "docs.security.server.credentials.oauth", CanonicalOwner: "docs/operators/upstream-servers.md", HelpFamilies: []string{"server"}},
	{ID: "docs.security.principal.policy", CanonicalOwner: "docs/operators/access-control.md", HelpFamilies: []string{"principal", "grant", "grant-request"}},
	{ID: "docs.security.invocation.uncertainty", CanonicalOwner: "docs/operators/invocation-evidence.md", HelpFamilies: []string{"invocation"}},
	{ID: "docs.security.backup.recovery", CanonicalOwner: "docs/operators/backup-and-recovery.md", HelpFamilies: []string{"admin reset", "backup", "restore"}},
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
