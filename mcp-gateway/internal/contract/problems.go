package contract

type ProblemCode string

const (
	ProblemMalformedRequest         ProblemCode = "malformed_request"
	ProblemInvalidJSON              ProblemCode = "invalid_json"
	ProblemInvalidCursor            ProblemCode = "invalid_cursor"
	ProblemInvalidIdempotencyKey    ProblemCode = "invalid_idempotency_key"
	ProblemAmbiguousCredentials     ProblemCode = "ambiguous_credentials"
	ProblemInvalidOAuthState        ProblemCode = "invalid_oauth_state"
	ProblemAuthenticationRequired   ProblemCode = "authentication_required"
	ProblemCredentialDomainMismatch ProblemCode = "credential_domain_mismatch" //nolint:gosec // Public problem code, not a credential.
	ProblemForbiddenOrigin          ProblemCode = "forbidden_origin"
	ProblemCSRFFailed               ProblemCode = "csrf_failed"
	ProblemNotFound                 ProblemCode = "not_found"
	ProblemMethodNotAllowed         ProblemCode = "method_not_allowed"
	ProblemConflict                 ProblemCode = "conflict"
	ProblemIdempotencyConflict      ProblemCode = "idempotency_conflict"
	ProblemBodyTooLarge             ProblemCode = "body_too_large"
	ProblemUnsupportedMediaType     ProblemCode = "unsupported_media_type"
	ProblemMisdirectedRequest       ProblemCode = "misdirected_request"
	ProblemResourceLimit            ProblemCode = "resource_limit"
	ProblemStorageUnavailable       ProblemCode = "storage_unavailable"
	ProblemKeyringUnavailable       ProblemCode = "keyring_unavailable"
	ProblemShuttingDown             ProblemCode = "shutting_down"
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
