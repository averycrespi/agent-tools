package contract

import (
	"encoding/json"
	"errors"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

const FailureDiagnosticMetaKey = "io.github.averycrespi.agent-tools/failure"
const FailureDiagnosticMaxBytes = 512

// FailureDiagnostics separates observations from untrusted, reporting-only claims.
type FailureDiagnostics struct {
	GatewayObserved FailureObservation       `json:"gateway_observed"`
	ServerReported  *ServerFailureDiagnostic `json:"server_reported,omitempty"`
}

type FailureObservation struct {
	Source string `json:"source"`
	Reason string `json:"reason"`
}

type ServerFailureDiagnostic struct {
	Version           int                `json:"version"`
	Category          string             `json:"category"`
	Phase             string             `json:"phase"`
	HTTPStatus        *int               `json:"http_status,omitempty"`
	RetryAfterSeconds *int               `json:"retry_after_seconds,omitempty"`
	Validation        *ValidationDetails `json:"validation,omitempty"`
}

func (d *ServerFailureDiagnostic) Valid() bool {
	if d == nil || (d.Version != 1 && d.Version != 2) {
		return false
	}
	if d.Version == 1 && d.Validation != nil {
		return false
	}
	if d.Version == 2 {
		if d.Category != "response_contract" || d.Phase != "response_validation" || d.HTTPStatus != nil || d.RetryAfterSeconds != nil || !d.Validation.valid() {
			return false
		}
		encoded, err := json.Marshal(d)
		if err != nil || len(encoded) > ValidationDiagnosticMaxBytes {
			return false
		}
	}
	switch d.Category {
	case "authentication", "rate_limit", "timeout", "canceled", "transport", "json_decode", "response_contract", "response_limit", "validation", "capacity", "overload", "redirect_rejected", "upstream":
	default:
		return false
	}
	switch d.Phase {
	case "admission", "exchange", "response_status", "response_decode", "response_validation":
	default:
		return false
	}
	return (d.HTTPStatus == nil || *d.HTTPStatus >= 100 && *d.HTTPStatus <= 599) && (d.RetryAfterSeconds == nil || *d.RetryAfterSeconds >= 0 && *d.RetryAfterSeconds <= 86400)
}

func (d *FailureDiagnostics) ValidFor(terminal InvocationTerminalClass) bool {
	if d == nil {
		return true
	}
	o := d.GatewayObserved
	valid := false
	switch o.Source {
	case "transport":
		valid = o.Reason == "prestart" && terminal == TerminalPrestartFailure || o.Reason == "handoff_uncertain" && terminal == TerminalOutcomeUnknown
	case "protocol":
		valid = (o.Reason == "invalid_response" || o.Reason == "rpc_error") && terminal == TerminalDownstreamFailure
	case "tool":
		valid = o.Reason == "reported_error" && terminal == TerminalDownstreamFailure
	case "result_validation":
		valid = o.Reason == "result_shape" && terminal == TerminalDownstreamFailure
	}
	return valid && (d.ServerReported == nil || o.Source == "tool" && d.ServerReported.Valid())
}

func ParseServerFailureDiagnostic(raw []byte) *ServerFailureDiagnostic {
	if !diagnosticObject(raw, "version", "category", "phase", "http_status", "retry_after_seconds", "validation") {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	if validation, ok := fields["validation"]; ok && !validationObject(validation) {
		return nil
	}
	var d ServerFailureDiagnostic
	if strictjson.Decode(raw, &d, diagnosticOptions()) != nil || !d.Valid() {
		return nil
	}
	return &d
}

func ParseFailureDiagnostics(raw []byte, terminal InvocationTerminalClass) (*FailureDiagnostics, error) {
	invalid := errors.New("invalid failure diagnostics")
	if !diagnosticObject(raw, "gateway_observed", "server_reported") {
		return nil, invalid
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || !diagnosticObject(fields["gateway_observed"], "source", "reason") {
		return nil, invalid
	}
	var d FailureDiagnostics
	if strictjson.Decode(raw, &d, diagnosticOptions()) != nil {
		return nil, invalid
	}
	if server, ok := fields["server_reported"]; ok {
		d.ServerReported = ParseServerFailureDiagnostic(server)
		if d.ServerReported == nil {
			return nil, invalid
		}
	}
	if !d.ValidFor(terminal) {
		return nil, invalid
	}
	return &d, nil
}

func diagnosticOptions() strictjson.Options {
	return strictjson.Options{MaxBytes: FailureDiagnosticMaxBytes, MaxDepth: 6, RejectUnknownMembers: true}
}

// Exact member names and non-null values avoid encoding/json's permissive field matching.
func diagnosticObject(raw []byte, allowed ...string) bool {
	value, err := strictjson.ParseValue(raw, diagnosticOptions())
	if err != nil || value.Type != strictjson.ValueObject {
		return false
	}
	for _, field := range value.Object {
		found := false
		for _, name := range allowed {
			if field.Name == name {
				found = true
				break
			}
		}
		if !found || field.Value.Type == strictjson.ValueNull {
			return false
		}
	}
	return true
}
