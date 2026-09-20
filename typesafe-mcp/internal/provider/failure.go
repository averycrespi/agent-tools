package provider

import "errors"

const FailureDiagnosticMetaKey = "io.github.averycrespi.agent-tools/failure"

// FailureDiagnostic contains only closed categories and bounded numeric claims.
// It is reporting evidence, never retry permission or execution certainty.
type FailureDiagnostic struct {
	Version           int                `json:"version"`
	Category          string             `json:"category"`
	Phase             string             `json:"phase"`
	HTTPStatus        *int               `json:"http_status,omitempty"`
	RetryAfterSeconds *int               `json:"retry_after_seconds,omitempty"`
	Validation        *ValidationDetails `json:"validation,omitempty"`
}

type failure struct {
	message    string
	diagnostic FailureDiagnostic
}

func (f *failure) Error() string { return f.message }

func safeFailure(message, category, phase string) error {
	return &failure{message: message, diagnostic: FailureDiagnostic{Version: 1, Category: category, Phase: phase}}
}

func Diagnostic(err error) (FailureDiagnostic, bool) {
	var f *failure
	if !errors.As(err, &f) {
		return FailureDiagnostic{}, false
	}
	return f.diagnostic, true
}
