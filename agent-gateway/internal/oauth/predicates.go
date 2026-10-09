package oauth

import (
	"fmt"
	"net/url"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

// Callers supply fixed field/rule names and locally validated scalar facts, not
// parser errors, metadata members, token values or endpoint query strings.
func protocolPredicate(public error, operation, facts string, values ...any) error {
	return diagnostics.WithDetail(public, diagnostics.Detail{Component: "oauth", Operation: operation, Explanation: public.Error() + "; " + fmt.Sprintf(facts, values...)})
}

type identifierFailure struct {
	cause   error
	role    string
	ordinal int
}

func (failure *identifierFailure) Error() string { return failure.cause.Error() }
func (failure *identifierFailure) Unwrap() error { return failure.cause }
func (failure *identifierFailure) OperatorDetail() diagnostics.Detail {
	detail := diagnostics.Snapshot("oauth", "validate metadata URL", "", failure.cause)
	detail.Explanation = fmt.Sprintf("role=%s ordinal=%d; %s", failure.role, failure.ordinal, detail.Explanation)
	return detail
}
func identifierContext(cause error, role string, ordinal int) error {
	return &identifierFailure{cause: cause, role: role, ordinal: ordinal}
}

type metadataContextFailure struct {
	cause           error
	role, authority string
	attempt, status int
	fallback        bool
}

func (failure *metadataContextFailure) Error() string { return failure.cause.Error() }
func (failure *metadataContextFailure) Unwrap() error { return failure.cause }
func (failure *metadataContextFailure) OperatorDetail() diagnostics.Detail {
	detail := diagnostics.Snapshot("oauth", "fetch "+failure.role, failure.authority, failure.cause)
	detail.Explanation = fmt.Sprintf("role=%s method=GET attempt=%d http_status=%d fallback_eligible=%t; ", failure.role, failure.attempt, failure.status, failure.fallback) + detail.Explanation
	return detail
}
func metadataFailure(cause error, role, rawURL string, attempt, status int, fallback bool) error {
	authority := "unknown"
	if parsed, err := url.Parse(rawURL); err == nil {
		authority = diagnostics.Text(parsed.Host, 160)
	}
	return &metadataContextFailure{cause: cause, role: role, authority: authority, attempt: attempt, status: status, fallback: fallback}
}
