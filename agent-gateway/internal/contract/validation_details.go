package contract

import (
	"encoding/json"
	"slices"
)

const ValidationDiagnosticMaxBytes = 400
const ValidationMaxViolations = 3

// ValidationDetails is a closed, unverified server claim, not execution authority.
type ValidationDetails struct {
	Schema     string                `json:"schema"`
	Version    int                   `json:"version"`
	Violations []ValidationViolation `json:"violations"`
	Truncated  bool                  `json:"truncated"`
}

type ValidationViolation struct {
	Code     string `json:"code"`
	Path     string `json:"path"`
	Rule     string `json:"rule"`
	Expected string `json:"expected,omitempty"`
	Observed string `json:"observed,omitempty"`
}

var validationPaths = map[string][]string{
	"models_result":   {"$", "$.models", "$.models.[]", "$.models.[].name", "$.models.[].description", "$.models.[].release_date"},
	"evaluate_result": {"$", "$.model", "$.answers", "$.answers.*", "$.answers.*.type", "$.answers.*.choice", "$.answers.*.confidence", "$.answers.*.probabilities", "$.answers.*.probabilities.*", "$.answers.*.score", "$.answers.*.legend", "$.answers.*.legend.*", "$.answers.*.noul", "$.usage", "$.usage.input_tokens", "$.usage.output_tokens"},
}

func (d *ValidationDetails) valid() bool {
	if d == nil || d.Version != 1 || len(d.Violations) == 0 || len(d.Violations) > ValidationMaxViolations {
		return false
	}
	paths, ok := validationPaths[d.Schema]
	if !ok {
		return false
	}
	previous := ""
	for _, v := range d.Violations {
		if !slices.Contains(paths, v.Path) || !v.valid() {
			return false
		}
		encoded, _ := json.Marshal(v)
		key := string(encoded)
		if key <= previous {
			return false
		}
		previous = key
	}
	return true
}

func (v ValidationViolation) valid() bool {
	if v.Code == "type" {
		return v.Rule == "type" && slices.Contains([]string{"null", "boolean", "object", "array", "number", "string", "integer", "null|string|array|object"}, v.Expected) && slices.Contains([]string{"null", "boolean", "object", "array", "number", "string", "integer"}, v.Observed)
	}
	if v.Expected != "" || v.Observed != "" {
		return false
	}
	switch v.Code {
	case "missing":
		return v.Rule == "required"
	case "invalid_date":
		return v.Rule == "date" && v.Path == "$.models.[].release_date"
	case "correspondence":
		return v.Rule == "correspondence" && v.Path == "$.answers"
	case "constraint":
		return slices.Contains([]string{"schema", "oneOf", "additionalProperties", "minLength", "minItems", "maxItems", "minProperties", "minimum", "maximum", "const", "pattern"}, v.Rule)
	}
	return false
}

// Explanation never contains downstream prose or instance values.
func (v ValidationViolation) Explanation() string {
	switch v.Code {
	case "missing":
		return "Required field is missing"
	case "type":
		return "Wrong value type"
	case "invalid_date":
		return "Invalid release date; expected YYYY-MM-DD or RFC 3339 timestamp"
	case "correspondence":
		return "Answers do not correspond to the submitted questions"
	default:
		return "Response does not satisfy the schema rule"
	}
}

func validationObject(raw []byte) bool {
	if !diagnosticObject(raw, "schema", "version", "violations", "truncated") {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) != 4 {
		return false
	}
	var violations []json.RawMessage
	if json.Unmarshal(fields["violations"], &violations) != nil {
		return false
	}
	for _, v := range violations {
		if !diagnosticObject(v, "code", "path", "rule", "expected", "observed") {
			return false
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(v, &fields) != nil {
			return false
		}
		var code string
		if json.Unmarshal(fields["code"], &code) != nil {
			return false
		}
		if code == "type" {
			if len(fields) != 5 {
				return false
			}
		} else if len(fields) != 3 {
			return false
		}
	}
	return true
}
