package contract

type ControlPlaneCapability struct {
	ID          string
	Operation   string
	WebControl  string
	CLIUses     []string
	Mechanics   string
	WebScenario string
	CLIScenario string
}

var controlPlaneCapabilities = []ControlPlaneCapability{
	{ID: "http-grant-read", Operation: "HTTP grant list/get", WebControl: "HTTP / Grants", CLIUses: []string{"http grant list", "http grant get ID"}, Mechanics: "bounded query snapshot; safe selectors"},
	{ID: "http-grant-create", Operation: "HTTP grant create", WebControl: "HTTP / Grants / Create", CLIUses: []string{"http grant create --file PATH"}, Mechanics: "closed policy; entire credential containment; confirmation"},
	{ID: "http-grant-update", Operation: "HTTP grant update", WebControl: "HTTP grant / Edit", CLIUses: []string{"http grant update ID --file PATH [--etag ETAG]"}, Mechanics: "atomic replacement; exact ETag; no replay"},
	{ID: "http-grant-delete", Operation: "HTTP grant delete", WebControl: "HTTP grant / Delete", CLIUses: []string{"http grant delete ID [--etag ETAG]"}, Mechanics: "bodyless; exact ETag; confirmation"},
	{ID: "http-default", Operation: "HTTP principal default", WebControl: "Principal / HTTP access", CLIUses: []string{"http default get ID", "http default update ID --file PATH [--etag ETAG]"}, Mechanics: "separate compatible resource; exact ETag; default block"},
	{ID: "http-preview", Operation: "HTTP policy preview", WebControl: "HTTP / Grants / Test access", CLIUses: []string{"http test-access --file PATH"}, Mechanics: "policy-only evaluator; no DNS, dispatch, secret resolution or retained coordinates"},
	{ID: "http-credential-read", Operation: "HTTP credential list/get", WebControl: "HTTP / Credentials", CLIUses: []string{"http credential list", "http credential get ID"}, Mechanics: "cursor/limit; bodyless; safe metadata only"},
	{ID: "http-credential-create", Operation: "HTTP credential create", WebControl: "HTTP / Credentials / Create", CLIUses: []string{"http credential create --file PATH"}, Mechanics: "strict write-only file; confirmation; no replay"},
	{ID: "http-credential-update", Operation: "HTTP credential update", WebControl: "HTTP credential / Edit", CLIUses: []string{"http credential update ID --file PATH [--etag ETAG]"}, Mechanics: "complete safe metadata; transactional references; automatic or explicit ETag"},
	{ID: "http-credential-rotate", Operation: "HTTP credential rotate", WebControl: "HTTP credential / Rotate", CLIUses: []string{"http credential rotate ID --file PATH [--etag ETAG]"}, Mechanics: "strict write-only file; automatic or explicit ETag; confirmation; no replay"},
	{ID: "http-credential-delete", Operation: "HTTP credential delete", WebControl: "HTTP credential / Delete", CLIUses: []string{"http credential delete ID [--etag ETAG]"}, Mechanics: "exact {}; reject references; automatic or explicit ETag; confirmation"},
	{ID: "audit-read", Operation: "Administrative audit list/get", WebControl: "Activity / Administrative audit", CLIUses: []string{"audit list", "audit get AUDIT_EVENT_ID"}, Mechanics: "authoritative filters/cursor/limit; generation and retention; bodyless"},
	{ID: "status", Operation: "Detailed status", WebControl: "Overview/System", CLIUses: []string{"status"}, Mechanics: "GET status"},
	{ID: "admin-credential-read", Operation: "Admin credential list/get", WebControl: "System / Admin credentials", CLIUses: []string{"admin credential list", "admin credential get ID"}, Mechanics: "cursor/limit; bodyless"},
	{ID: "admin-credential-create", Operation: "Admin credential create/rotate", WebControl: "System / Admin credentials / Create and revoke", CLIUses: []string{"admin credential create [--expires-at RFC3339] [--secret-output NEW_PATH]", "admin credential rotate OLD_CREDENTIAL_ID --secret-output NEW_PATH"}, Mechanics: "direct lifetime; one-time sink; rotation durably verifies replacement before conditional targeted revoke; no replay"},
	{ID: "admin-credential-revoke", Operation: "Admin credential revoke", WebControl: "credential detail", CLIUses: []string{"admin credential revoke ID"}, Mechanics: "exact {}; confirmation"},
	{ID: "backup-read", Operation: "Backup list/get", WebControl: "System / Backups", CLIUses: []string{"backup list", "backup get BACKUP_ID"}, Mechanics: "cursor/limit; bodyless"},
	{ID: "backup-create", Operation: "Backup create", WebControl: "System / Backups / Create", CLIUses: []string{"backup create"}, Mechanics: "exact {}; idempotency key"},
	{ID: "backup-delete", Operation: "Backup delete", WebControl: "backup detail", CLIUses: []string{"backup delete BACKUP_ID"}, Mechanics: "exact {}; confirmation"},
	{ID: "server-read", Operation: "Server list/get", WebControl: "Servers", CLIUses: []string{"mcp server list", "mcp server get ID"}, Mechanics: "cursor/limit; bodyless"},
	{ID: "server-create", Operation: "Server create", WebControl: "Servers / Create", CLIUses: []string{"mcp server create --file PATH"}, Mechanics: "body; idempotency key"},
	{ID: "server-update", Operation: "Server update", WebControl: "server detail / Edit", CLIUses: []string{"mcp server update ID [--etag ETAG] [--display-name NAME] [--enable|--disable] [--file PATH]"}, Mechanics: "exclusive direct/file nonempty patch; automatic or explicit ETag"},
	{ID: "server-delete", Operation: "Server delete", WebControl: "server detail / Delete", CLIUses: []string{"mcp server delete ID [--etag ETAG]"}, Mechanics: "exact {}; typed namespace/--yes; automatic or explicit ETag"},
	{ID: "operation-read", Operation: "Operation list/get", WebControl: "server detail / Operations", CLIUses: []string{"mcp server operation list ID", "mcp server operation get ID OPERATION_ID"}, Mechanics: "cursor/limit; bodyless"},
	{ID: "operation-start", Operation: "Explicit operation", WebControl: "server detail action", CLIUses: []string{"mcp server operation start ID --kind KIND [--etag ETAG]"}, Mechanics: "closed direct kind; automatic or explicit ETag; idempotency key"},
	{ID: "server-credential", Operation: "Credential replacement", WebControl: "server detail / Credentials", CLIUses: []string{"mcp server credential replace ID --file PATH [--etag ETAG]"}, Mechanics: "strict write-only file; automatic or explicit ETag; confirmation; no replay"},
	{ID: "auth-flow-read", Operation: "Auth-flow list/get", WebControl: "server detail / OAuth", CLIUses: []string{"mcp server auth-flow list ID", "mcp server auth-flow get ID FLOW_ID"}, Mechanics: "cursor/limit; bodyless"},
	{ID: "auth-flow-start", Operation: "Auth-flow start", WebControl: "server detail / OAuth / Authorize", CLIUses: []string{"mcp server auth-flow start ID [--etag ETAG] [--open]"}, Mechanics: "exact {}; automatic or explicit ETag; one-time URL sink"},
	{ID: "auth-flow-cancel", Operation: "Auth-flow cancel", WebControl: "flow detail / Cancel", CLIUses: []string{"mcp server auth-flow cancel ID FLOW_ID"}, Mechanics: "exact {}; eligible; confirmation"},
	{ID: "descriptor-read", Operation: "Descriptor list/get", WebControl: "server detail / Descriptors", CLIUses: []string{"mcp server descriptor list ID", "mcp server descriptor get ID TOOL_ID"}, Mechanics: "retired/cursor/limit; bodyless"},
	{ID: "catalog-list", Operation: "Active catalog list", WebControl: "Catalog", CLIUses: []string{"mcp catalog list"}, Mechanics: "cursor/limit; bodyless"},
	{ID: "principal-read", Operation: "Principal list/get", WebControl: "Access / Principals", CLIUses: []string{"principal list", "principal get ID"}, Mechanics: "cursor/limit; bodyless"},
	{ID: "principal-create", Operation: "Principal create", WebControl: "Access / Principals / Create", CLIUses: []string{"principal create --display-name NAME --visibility VISIBILITY"}, Mechanics: "required direct display name and visibility"},
	{ID: "principal-update", Operation: "Principal update", WebControl: "principal detail / Edit", CLIUses: []string{"principal update ID [--etag ETAG] [--display-name NAME] [--visibility VISIBILITY] [--state STATE]"}, Mechanics: "nonempty direct patch; automatic or explicit ETag; confirmation"},
	{ID: "principal-credential-issue", Operation: "Agent credential issue/rotate", WebControl: "principal detail / Credential", CLIUses: []string{"principal credential issue ID [--etag ETAG] [--secret-output NEW_PATH]", "principal credential rotate ID [--etag ETAG] [--secret-output NEW_PATH]"}, Mechanics: "empty/occupied slot preflight; atomic rotation; one-time sink; no replay"},
	{ID: "principal-credential-revoke", Operation: "Agent credential revoke", WebControl: "principal detail / Credential", CLIUses: []string{"principal credential revoke ID [--etag ETAG]"}, Mechanics: "exact {}; automatic or explicit ETag; confirmation"},
	{ID: "grant-read", Operation: "Grant list/get", WebControl: "MCP / Grants", CLIUses: []string{"mcp grant list", "mcp grant get ID"}, Mechanics: "filters/cursor/limit; bodyless"},
	{ID: "grant-create", Operation: "Grant create", WebControl: "MCP / Grants / Create", CLIUses: []string{"mcp grant create --principal-id ID --effect EFFECT --server-id ID [--description TEXT] [--upstream-name NAME] [--expires-at RFC3339] [--read-only] [--file PATH]"}, Mechanics: "optional description; exclusive direct unconstrained/file complete body"},
	{ID: "grant-update", Operation: "Grant metadata update", WebControl: "grant detail / Description", CLIUses: []string{"mcp grant update ID --description TEXT [--etag ETAG]"}, Mechanics: "description only; automatic or explicit ETag; no replay"},
	{ID: "grant-delete", Operation: "Grant delete", WebControl: "grant detail", CLIUses: []string{"mcp grant delete ID"}, Mechanics: "bodyless; confirmation"},
	{ID: "grant-request-read", Operation: "Grant-request list/get", WebControl: "MCP / Access requests", CLIUses: []string{"mcp grant-request list", "mcp grant-request get REQUEST_ID"}, Mechanics: "filters/cursor/limit; bodyless"},
	{ID: "grant-request-approve", Operation: "Grant-request approve", WebControl: "request detail / Review", CLIUses: []string{"mcp grant-request approve REQUEST_ID --scope SCOPE --target TARGET [--description TEXT] [--etag ETAG] [--duration-seconds SECONDS] [--acknowledge-future-tools] [--read-only] [--file PATH]"}, Mechanics: "optional grant description; exclusive direct/file narrowing; automatic or explicit ETag; confirmation; no replay"},
	{ID: "grant-request-reject", Operation: "Grant-request reject", WebControl: "request detail / Review", CLIUses: []string{"mcp grant-request reject REQUEST_ID --reason REASON [--etag ETAG]"}, Mechanics: "direct closed reason; automatic or explicit ETag; confirmation; no replay"},
	{ID: "http-traffic-read", Operation: "HTTP traffic list/get", WebControl: "HTTP / Traffic", CLIUses: []string{"http traffic list", "http traffic get ID"}, Mechanics: "exact filters/cursor/limit; bodyless; historical bounded evidence; separate from MCP"},
	{ID: "invocation-read", Operation: "MCP invocation list/get", WebControl: "MCP / MCP invocations", CLIUses: []string{"mcp invocation list", "mcp invocation get INVOCATION_ID"}, Mechanics: "filters/cursor/limit; bodyless"},
}

func ControlPlaneCapabilityManifest() []ControlPlaneCapability {
	result := make([]ControlPlaneCapability, len(controlPlaneCapabilities))
	for index, row := range controlPlaneCapabilities {
		result[index] = row
		result[index].CLIUses = append([]string(nil), row.CLIUses...)
		result[index].WebScenario = "browser." + row.ID
		result[index].CLIScenario = "cli." + row.ID
	}
	return result
}

func ControlPlaneLifecycleManifest() []ControlPlaneCapability {
	return []ControlPlaneCapability{
		{ID: "web-exchange", Operation: "Browser bearer exchange", WebControl: "Sign in", Mechanics: "POST exchange", WebScenario: "browser.exchange"},
		{ID: "web-bootstrap", Operation: "Browser session bootstrap", WebControl: "Application bootstrap", Mechanics: "POST current session", WebScenario: "browser.bootstrap"},
		{ID: "web-logout", Operation: "Browser logout", WebControl: "Sign out", Mechanics: "POST logout", WebScenario: "browser.logout"},
		{ID: "web-events", Operation: "Browser invalidations", WebControl: "Application refresh", Mechanics: "POST event stream", WebScenario: "browser.events"},
		{ID: "cli-bearer", Operation: "CLI bearer acquisition", CLIUses: []string{"--admin-bearer-file PATH", "--admin-bearer-stdin"}, Mechanics: "owner-only explicit file/exclusive stdin/resolved default file", CLIScenario: "cli.bearer"},
		{ID: "cli-initialize", Operation: "Stopped initialize", CLIUses: []string{"initialize"}, Mechanics: "offline compatibility", CLIScenario: "cli.initialize"},
		{ID: "cli-admin-reset", Operation: "Stopped admin reset", CLIUses: []string{"admin reset"}, Mechanics: "stopped all-authority recovery", CLIScenario: "cli.admin-reset"},
		{ID: "cli-storage-verify", Operation: "Stopped current storage verification", CLIUses: []string{"storage verify"}, Mechanics: "exclusive ownership; recognized recovery only; no replacement", CLIScenario: "cli.storage-verify"},
		{ID: "cli-backup-restore", Operation: "Stopped backup restore", CLIUses: []string{"backup restore BACKUP_ID"}, Mechanics: "exclusive ownership; verified lineage; one-time replacement bearer; no replay", CLIScenario: "cli.backup-restore"},
	}
}
