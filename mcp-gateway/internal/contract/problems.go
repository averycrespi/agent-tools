package contract

type ProblemCode string

const (
	ProblemMalformedRequest                   ProblemCode = "malformed_request"
	ProblemInvalidJSON                        ProblemCode = "invalid_json"
	ProblemInvalidCursor                      ProblemCode = "invalid_cursor"
	ProblemInvalidIdempotencyKey              ProblemCode = "invalid_idempotency_key"
	ProblemAmbiguousCredentials               ProblemCode = "ambiguous_credentials"
	ProblemInvalidOAuthState                  ProblemCode = "invalid_oauth_state"
	ProblemAuthenticationRequired             ProblemCode = "authentication_required"
	ProblemCredentialDomainMismatch           ProblemCode = "credential_domain_mismatch" //nolint:gosec // Public problem code, not a credential.
	ProblemForbiddenOrigin                    ProblemCode = "forbidden_origin"
	ProblemCSRFFailed                         ProblemCode = "csrf_failed"
	ProblemNotFound                           ProblemCode = "not_found"
	ProblemMethodNotAllowed                   ProblemCode = "method_not_allowed"
	ProblemConflict                           ProblemCode = "conflict"
	ProblemIdempotencyConflict                ProblemCode = "idempotency_conflict"
	ProblemBodyTooLarge                       ProblemCode = "body_too_large"
	ProblemUnsupportedMediaType               ProblemCode = "unsupported_media_type"
	ProblemMisdirectedRequest                 ProblemCode = "misdirected_request"
	ProblemResourceLimit                      ProblemCode = "resource_limit"
	ProblemStorageUnavailable                 ProblemCode = "storage_unavailable"
	ProblemKeyringUnavailable                 ProblemCode = "keyring_unavailable"
	ProblemShuttingDown                       ProblemCode = "shutting_down"
	ProblemInvalidServerConfiguration         ProblemCode = "invalid_server_configuration"
	ProblemInvalidOperation                   ProblemCode = "invalid_operation"
	ProblemNamespaceUnavailable               ProblemCode = "namespace_unavailable"
	ProblemOperationConflict                  ProblemCode = "operation_conflict"
	ProblemOAuthFlowActive                    ProblemCode = "oauth_flow_active"
	ProblemStaleCursor                        ProblemCode = "stale_cursor"
	ProblemAuditHistoryReplaced               ProblemCode = "audit_history_replaced"
	ProblemStaleRevision                      ProblemCode = "stale_revision"
	ProblemPreconditionRequired               ProblemCode = "precondition_required"
	ProblemDownstreamUnavailable              ProblemCode = "downstream_unavailable"
	ProblemInvalidPrincipal                   ProblemCode = "invalid_principal"
	ProblemInvalidGrant                       ProblemCode = "invalid_grant"
	ProblemStaleGrantRevision                 ProblemCode = "stale_grant_revision"
	ProblemGrantPreconditionRequired          ProblemCode = "grant_precondition_required"
	ProblemStalePrincipalRevision             ProblemCode = "stale_principal_revision"
	ProblemPrincipalPreconditionRequired      ProblemCode = "principal_precondition_required"
	ProblemAuthorizationUnavailable           ProblemCode = "authorization_unavailable"
	ProblemInvalidGrantRequest                ProblemCode = "invalid_grant_request"
	ProblemGrantRequestConflict               ProblemCode = "grant_request_conflict"
	ProblemStaleGrantRequestRevision          ProblemCode = "stale_grant_request_revision"
	ProblemGrantRequestPreconditionRequired   ProblemCode = "grant_request_precondition_required"
	ProblemAdminRotationConflict              ProblemCode = "admin_rotation_conflict"
	ProblemStaleAdminAuthority                ProblemCode = "stale_admin_authority"
	ProblemAdminAuthorityPreconditionRequired ProblemCode = "admin_authority_precondition_required"
)

type Problem struct {
	Status int
	Code   ProblemCode
	Title  string
}

var problems = []Problem{
	{Status: 400, Code: ProblemMalformedRequest, Title: "The request is invalid."},
	{Status: 400, Code: ProblemInvalidJSON, Title: "The JSON body is invalid."},
	{Status: 400, Code: ProblemInvalidCursor, Title: "The cursor is invalid."},
	{Status: 400, Code: ProblemInvalidIdempotencyKey, Title: "The idempotency key is invalid."},
	{Status: 400, Code: ProblemAmbiguousCredentials, Title: "Multiple credential types were supplied."},
	{Status: 400, Code: ProblemInvalidOAuthState, Title: "The OAuth state is invalid or expired."},
	{Status: 401, Code: ProblemAuthenticationRequired, Title: "Authentication is required."},
	{Status: 403, Code: ProblemCredentialDomainMismatch, Title: "The credential is for a different authority."},
	{Status: 403, Code: ProblemForbiddenOrigin, Title: "The Origin is not accepted."},
	{Status: 403, Code: ProblemCSRFFailed, Title: "CSRF validation failed."},
	{Status: 404, Code: ProblemNotFound, Title: "The resource was not found."},
	{Status: 405, Code: ProblemMethodNotAllowed, Title: "The method is not allowed."},
	{Status: 409, Code: ProblemConflict, Title: "The request conflicts with current state."},
	{Status: 409, Code: ProblemIdempotencyConflict, Title: "The idempotency key conflicts with prior work."},
	{Status: 413, Code: ProblemBodyTooLarge, Title: "The request body is too large."},
	{Status: 415, Code: ProblemUnsupportedMediaType, Title: "The media type is not supported."},
	{Status: 421, Code: ProblemMisdirectedRequest, Title: "The Host is not accepted."},
	{Status: 429, Code: ProblemResourceLimit, Title: "The resource limit is reached."},
	{Status: 503, Code: ProblemStorageUnavailable, Title: "Storage is unavailable."},
	{Status: 503, Code: ProblemKeyringUnavailable, Title: "The credential provider is unavailable."},
	{Status: 503, Code: ProblemShuttingDown, Title: "The service is shutting down."},
	{Status: 400, Code: ProblemInvalidServerConfiguration, Title: "The server configuration is invalid."},
	{Status: 400, Code: ProblemInvalidOperation, Title: "The server operation is invalid."},
	{Status: 409, Code: ProblemNamespaceUnavailable, Title: "The server namespace is unavailable."},
	{Status: 409, Code: ProblemOperationConflict, Title: "The server has conflicting work."},
	{Status: 409, Code: ProblemOAuthFlowActive, Title: "The OAuth flow is already exchanging."},
	{Status: 409, Code: ProblemStaleCursor, Title: "The cursor snapshot is no longer available."},
	{Status: 412, Code: ProblemStaleRevision, Title: "The server revision is stale."},
	{Status: 428, Code: ProblemPreconditionRequired, Title: "The current server revision is required."},
	{Status: 503, Code: ProblemDownstreamUnavailable, Title: "The downstream server is unavailable."},
	{Status: 400, Code: ProblemInvalidPrincipal, Title: "The principal is invalid."},
	{Status: 400, Code: ProblemInvalidGrant, Title: "The grant is invalid."},
	{Status: 412, Code: ProblemStaleGrantRevision, Title: "The grant revision is stale."},
	{Status: 428, Code: ProblemGrantPreconditionRequired, Title: "The current grant revision is required."},
	{Status: 412, Code: ProblemStalePrincipalRevision, Title: "The principal revision is stale."},
	{Status: 428, Code: ProblemPrincipalPreconditionRequired, Title: "The current principal revision is required."},
	{Status: 503, Code: ProblemAuthorizationUnavailable, Title: "Authorization is unavailable."},
	{Status: 400, Code: ProblemInvalidGrantRequest, Title: "The grant request is invalid."},
	{Status: 409, Code: ProblemGrantRequestConflict, Title: "The grant request conflicts with current state."},
	{Status: 412, Code: ProblemStaleGrantRequestRevision, Title: "The grant request revision is stale."},
	{Status: 428, Code: ProblemGrantRequestPreconditionRequired, Title: "The current grant request revision is required."},
	{Status: 409, Code: ProblemAdminRotationConflict, Title: "The administrator credential rotation conflicts with current state."},
	{Status: 412, Code: ProblemStaleAdminAuthority, Title: "The administrator authority revision is stale."},
	{Status: 428, Code: ProblemAdminAuthorityPreconditionRequired, Title: "The administrator authority revision is required."},
	{Status: 409, Code: ProblemAuditHistoryReplaced, Title: "The audit history generation has changed."},
}

func Problems() []Problem {
	return append([]Problem(nil), problems...)
}

func ProblemForCode(code ProblemCode) (Problem, bool) {
	for _, problem := range problems {
		if problem.Code == code {
			return problem, true
		}
	}
	return Problem{}, false
}
