package oauth

import (
	"errors"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
)

type diagnosticFailure struct {
	cause      error
	reason     contract.PublicReason
	httpStatus *int
}

func (failure *diagnosticFailure) Error() string { return failure.cause.Error() }
func (failure *diagnosticFailure) Unwrap() error { return failure.cause }
func (failure *diagnosticFailure) OperatorDetail() diagnostics.Detail {
	detail := diagnostics.Snapshot("oauth", "response", "", failure.cause)
	if failure.httpStatus != nil {
		detail.Native = "HTTP status " + strconv.Itoa(*failure.httpStatus)
	}
	return detail
}

func newDiagnosticFailure(cause error, reason contract.PublicReason, status int) error {
	failure := &diagnosticFailure{cause: cause, reason: reason}
	if status >= 100 && status <= 599 {
		failure.httpStatus = &status
	}
	return failure
}

func oauthDiagnostic(flowID string, stage contract.OAuthDiagnosticStage, cause error) contract.OAuthDiagnostic {
	diagnostic := contract.OAuthDiagnostic{CorrelationID: flowID, Stage: stage, Reason: contract.ReasonOAuthRejected}
	if errors.Is(cause, ErrCallbackUnavailable) {
		diagnostic.Reason = contract.ReasonConfigurationInvalid
	}
	var failure *diagnosticFailure
	if errors.As(cause, &failure) {
		diagnostic.Reason = failure.reason
		diagnostic.HTTPStatus = failure.httpStatus
	}
	return diagnostic
}

func reasonForHTTPStatus(status int) contract.PublicReason {
	switch status {
	case 401, 403:
		return contract.ReasonAuthenticationRejected
	case 429:
		return contract.ReasonResourceLimit
	default:
		return contract.ReasonProtocolInvalid
	}
}
