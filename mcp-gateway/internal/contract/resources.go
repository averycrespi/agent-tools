package contract

import "io/fs"

const (
	MediaTypeJSON        = "application/json"
	MediaTypeProblemJSON = "application/problem+json"
	MediaTypeEventStream = "text/event-stream"

	ModernProtocolVersion = "2026-07-28"
	LegacyProtocolVersion = "2025-11-25"

	AdminBearerPrefix = "mgw_admin_"
	AgentBearerPrefix = "mgw_agent_"
	SessionCookieName = "mcp_gateway_session"
	SessionValueBytes = 32
)

type ResourceMechanic struct {
	Pattern              string
	Method               string
	RequestSchema        string
	SuccessSchema        string
	SuccessStatuses      []int
	Cursor               bool
	Idempotency          bool
	Precondition         bool
	OptionalPrecondition bool
	ETag                 bool
	EventReplay          bool
}

var resourceMechanics = []ResourceMechanic{
	{Pattern: "/api/v1/admin-sessions", Method: "POST"},
	{Pattern: "/api/v1/admin-sessions/current", Method: "DELETE"},
	{Pattern: "/api/v1/admin-credentials", Method: "GET", Cursor: true},
	{Pattern: "/api/v1/admin-credentials", Method: "POST", RequestSchema: "AdminCredentialCreate", SuccessSchema: "CreatedAdminCredential", SuccessStatuses: []int{201}, OptionalPrecondition: true, ETag: true},
	{Pattern: "/api/v1/admin-credentials/{id}", Method: "DELETE"},
	{Pattern: "/api/v1/admin-credentials/{id}", Method: "GET"},
	{Pattern: "/api/v1/system-status", Method: "GET"},
	{Pattern: "/api/v1/backups", Method: "GET", Cursor: true},
	{Pattern: "/api/v1/backups", Method: "POST", Idempotency: true},
	{Pattern: "/api/v1/backups/{id}", Method: "DELETE"},
	{Pattern: "/api/v1/backups/{id}", Method: "GET"},
	{Pattern: "/api/v1/events", Method: "GET"},
	{Pattern: "/api/v1/servers", Method: "GET", RequestSchema: "ServerListQuery", SuccessSchema: "Page<Server>", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/servers", Method: "POST", RequestSchema: "ServerCreate", SuccessSchema: "ServerMutation", SuccessStatuses: []int{200, 201}, Idempotency: true, ETag: true},
	{Pattern: "/api/v1/servers/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "Server", SuccessStatuses: []int{200}, ETag: true},
	{Pattern: "/api/v1/servers/{id}", Method: "PATCH", RequestSchema: "ServerPatch", SuccessSchema: "ServerMutation", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
	{Pattern: "/api/v1/servers/{id}", Method: "DELETE", RequestSchema: "EmptyObject", SuccessSchema: "ServerMutation", SuccessStatuses: []int{200, 202}, Precondition: true, ETag: true},
	{Pattern: "/api/v1/servers/{id}/operations", Method: "GET", RequestSchema: "ServerOperationListQuery", SuccessSchema: "Page<ServerOperation>", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/servers/{id}/operations", Method: "POST", RequestSchema: "ServerOperationCreate", SuccessSchema: "ServerOperationMutation", SuccessStatuses: []int{200, 202}, Idempotency: true, Precondition: true},
	{Pattern: "/api/v1/servers/{id}/operations/{operation_id}", Method: "GET", RequestSchema: "None", SuccessSchema: "ServerOperation", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/servers/{id}/credential-replacements", Method: "POST", RequestSchema: "CredentialReplacement", SuccessSchema: "CredentialReplacementResult", SuccessStatuses: []int{202}, Precondition: true},
	{Pattern: "/api/v1/servers/{id}/auth-flows", Method: "GET", RequestSchema: "ServerAuthFlowListQuery", SuccessSchema: "Page<ServerAuthFlow>", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/servers/{id}/auth-flows", Method: "POST", RequestSchema: "EmptyObject", SuccessSchema: "AuthFlowCreation", SuccessStatuses: []int{201}, Precondition: true},
	{Pattern: "/api/v1/servers/{id}/auth-flows/{flow_id}", Method: "GET", RequestSchema: "None", SuccessSchema: "ServerAuthFlow", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/servers/{id}/auth-flows/{flow_id}", Method: "DELETE", RequestSchema: "EmptyObject", SuccessSchema: "Empty", SuccessStatuses: []int{204}},
	{Pattern: "/api/v1/catalog", Method: "GET", RequestSchema: "CatalogListQuery", SuccessSchema: "CatalogPage", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/servers/{id}/descriptors", Method: "GET", RequestSchema: "DescriptorListQuery", SuccessSchema: "Page<ToolDescriptor>|Page<ToolDescriptorSummary>", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/servers/{id}/descriptors/{tool_id}", Method: "GET", RequestSchema: "None", SuccessSchema: "ToolDescriptor", SuccessStatuses: []int{200}},
	{Pattern: "/oauth/callback", Method: "GET", RequestSchema: "OAuthCallbackQuery", SuccessSchema: "OAuthCallbackHTML", SuccessStatuses: []int{200, 400, 503}},
	{Pattern: "/api/v1/principals", Method: "GET", RequestSchema: "PrincipalListQuery", SuccessSchema: "Page<Principal>|QueryPage<Principal>", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/principals", Method: "POST", RequestSchema: "PrincipalCreate", SuccessSchema: "PrincipalCreation", SuccessStatuses: []int{201}, ETag: true},
	{Pattern: "/api/v1/principals/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "Principal", SuccessStatuses: []int{200}, ETag: true},
	{Pattern: "/api/v1/principals/{id}", Method: "PATCH", RequestSchema: "PrincipalPatch", SuccessSchema: "Principal", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
	{Pattern: "/api/v1/principals/{id}/credential", Method: "POST", RequestSchema: "EmptyObject", SuccessSchema: "AgentCredentialCreation", SuccessStatuses: []int{201}, Precondition: true, ETag: true},
	{Pattern: "/api/v1/principals/{id}/credential", Method: "DELETE", RequestSchema: "EmptyObject", SuccessSchema: "Principal", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
	{Pattern: "/api/v1/grants", Method: "GET", RequestSchema: "GrantListQuery", SuccessSchema: "Page<Grant>|QueryPage<Grant>|QueryPage<GrantTableItem>", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/grants", Method: "POST", RequestSchema: "GrantCreate", SuccessSchema: "Grant", SuccessStatuses: []int{201}},
	{Pattern: "/api/v1/grants/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "Grant", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/grants/{id}", Method: "DELETE", RequestSchema: "None", SuccessSchema: "Empty", SuccessStatuses: []int{204}},
	{Pattern: "/api/v1/grant-constraints/validate", Method: "POST", RequestSchema: "GrantConstraintValidation", SuccessSchema: "GrantConstraintValidationResult", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/grant-requests", Method: "GET", RequestSchema: "GrantRequestListQuery", SuccessSchema: "Page<GrantRequestSummary>", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/grant-requests/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "GrantRequest", SuccessStatuses: []int{200}, ETag: true},
	{Pattern: "/api/v1/grant-requests/{id}/approve", Method: "POST", RequestSchema: "GrantRequestApproval", SuccessSchema: "GrantRequest", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
	{Pattern: "/api/v1/grant-requests/{id}/reject", Method: "POST", RequestSchema: "GrantRequestRejection", SuccessSchema: "GrantRequest", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
	{Pattern: "/api/v1/admin-sessions/current", Method: "POST", RequestSchema: "EmptyObject", SuccessSchema: "AdminSessionBootstrap", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/events", Method: "POST", RequestSchema: "EmptyObject", SuccessSchema: "EventStream", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/audit-events", Method: "GET", RequestSchema: "AuditListQuery", SuccessSchema: "AuditPage", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/audit-events/{id}", Method: "GET", RequestSchema: "AuditItemQuery", SuccessSchema: "AuditItem", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/invocations", Method: "GET", RequestSchema: "InvocationListQuery", SuccessSchema: "InvocationPage", SuccessStatuses: []int{200}, Cursor: true},
	{Pattern: "/api/v1/invocations/{id}", Method: "GET", RequestSchema: "None", SuccessSchema: "Invocation", SuccessStatuses: []int{200}},
	{Pattern: "/api/v1/admin-authority", Method: "GET", RequestSchema: "None", SuccessSchema: "AdminAuthority", SuccessStatuses: []int{200}, ETag: true},
	{Pattern: "/api/v1/admin-credentials/{id}/rotation-completion", Method: "POST", RequestSchema: "AdminCredentialRotationCompletion", SuccessSchema: "AdminCredentialRotationResult", SuccessStatuses: []int{200}, Precondition: true, ETag: true},
}

func ResourceMechanics() []ResourceMechanic {
	result := make([]ResourceMechanic, len(resourceMechanics))
	for index, mechanic := range resourceMechanics {
		result[index] = mechanic
		result[index].SuccessStatuses = append([]int(nil), mechanic.SuccessStatuses...)
	}
	return result
}

type SecretSink string

const (
	SecretSinkControllingTerminal                SecretSink  = "controlling_terminal"
	SecretSinkOwnerOnlyFile                      SecretSink  = "owner_only_file"
	SecretSinkAdminCredentialReplacement         SecretSink  = "admin_credential_replacement" //nolint:gosec // Public sink name, not a credential.
	SecretSinkDCRClientSecret                    SecretSink  = "dcr_client_secret"            //nolint:gosec // Public sink name, not a credential.
	SecretSinkAuthorizationCodeTokenResponse     SecretSink  = "authorization_code_token_response"
	SecretSinkRefreshResponse                    SecretSink  = "refresh_response"
	SecretSinkAuthoritativeGenerationRefreshCopy SecretSink  = "authoritative_generation_refresh_copy"
	SecretSinkAgentCredentialCreation            SecretSink  = "agent_credential_creation" //nolint:gosec // Public sink name, not a credential.
	SecretSinkBrowserOneTimeDisplay              SecretSink  = "browser_one_time_display"
	SecretSinkUserInitiatedClipboard             SecretSink  = "user_initiated_clipboard"
	SecretOutputFileMode                         fs.FileMode = 0o600
	SecretOutputTerminator                                   = "\n"
)

func ApprovedSecretSinks() []SecretSink {
	return []SecretSink{
		SecretSinkControllingTerminal,
		SecretSinkOwnerOnlyFile,
		SecretSinkAdminCredentialReplacement,
		SecretSinkDCRClientSecret,
		SecretSinkAuthorizationCodeTokenResponse,
		SecretSinkRefreshResponse,
		SecretSinkAuthoritativeGenerationRefreshCopy,
		SecretSinkAgentCredentialCreation,
		SecretSinkBrowserOneTimeDisplay,
		SecretSinkUserInitiatedClipboard,
	}
}
