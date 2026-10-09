package keyring

import "github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"

// The outer caller snapshots after releasing provider/coordinator admissions.
// Only safe resource identities/predicates are retained here, never material.
type custodyFailure struct {
	public    error
	cause     error
	operation string
	resource  string
	predicate string
}

func (failure *custodyFailure) Error() string { return failure.public.Error() }
func (failure *custodyFailure) Unwrap() error { return failure.public }
func (failure *custodyFailure) OperatorDetail() diagnostics.Detail {
	detail := diagnostics.Snapshot("encrypted-custody", failure.operation, failure.resource, failure.cause)
	if detail.Explanation != "" {
		detail.Explanation = failure.predicate + "; " + detail.Explanation
	} else {
		detail.Explanation = failure.predicate
	}
	return detail
}

func custodyError(public error, operation, resource, predicate string, cause error) error {
	return &custodyFailure{public: public, cause: cause, operation: operation, resource: resource, predicate: predicate}
}
