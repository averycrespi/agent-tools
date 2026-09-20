package provider

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// These bounds reserve room for Gateway's provenance envelope within 512 bytes.
const validationDiagnosticMaxBytes = 400
const validationMaxViolations = 3

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

var responsePaths = map[string][]string{
	"models_result":   {"$", "$.models", "$.models.[]", "$.models.[].name", "$.models.[].description", "$.models.[].release_date"},
	"evaluate_result": {"$", "$.model", "$.answers", "$.answers.*", "$.answers.*.type", "$.answers.*.choice", "$.answers.*.confidence", "$.answers.*.probabilities", "$.answers.*.probabilities.*", "$.answers.*.score", "$.answers.*.legend", "$.answers.*.legend.*", "$.answers.*.noul", "$.usage", "$.usage.input_tokens", "$.usage.output_tokens"},
}

// Return only compiled-in paths. No instance segment is ever copied to evidence.
func responsePath(schema string, location []string) string {
	for _, path := range responsePaths[schema] {
		parts := strings.Split(path, ".")[1:]
		if len(parts) != len(location) {
			continue
		}
		match := true
		for i, part := range parts {
			if part != "*" && part != "[]" && part != location[i] {
				match = false
				break
			}
		}
		if match {
			return path
		}
	}
	return "$"
}

func responseViolations(schema string, value any) []ValidationViolation {
	err := validators()[schema].Validate(value)
	if err == nil {
		return nil
	}
	var root *jsonschema.ValidationError
	if !errors.As(err, &root) {
		return []ValidationViolation{{Code: "constraint", Path: "$", Rule: "schema"}}
	}
	var violations []ValidationViolation
	var visit func(*jsonschema.ValidationError)
	visit = func(e *jsonschema.ValidationError) {
		path := responsePath(schema, e.InstanceLocation)
		add := func(code, rule string) {
			violations = append(violations, ValidationViolation{Code: code, Path: path, Rule: rule})
		}
		switch k := e.ErrorKind.(type) {
		case *kind.OneOf:
			// Only explain the selected, known answer variant; errors from other
			// alternatives are not evidence that a field is required for this answer.
			variant := answerVariant(value, e.InstanceLocation)
			if variant < 0 {
				answer := answerValue(value, e.InstanceLocation)
				fields, ok := answer.(object)
				if !ok {
					violations = append(violations, ValidationViolation{Code: "type", Path: path, Rule: "type", Expected: "object", Observed: jsonTypeName(answer)})
				} else if typ, present := fields["type"]; !present {
					violations = append(violations, ValidationViolation{Code: "missing", Path: path + ".type", Rule: "required"})
				} else if _, ok := typ.(string); !ok {
					violations = append(violations, ValidationViolation{Code: "type", Path: path + ".type", Rule: "type", Expected: "string", Observed: jsonTypeName(typ)})
				} else {
					add("constraint", "oneOf")
				}
				return
			}
			marker := "/oneOf/" + strconv.Itoa(variant)
			for _, child := range e.Causes {
				if strings.HasSuffix(child.SchemaURL, marker) || strings.Contains(child.SchemaURL, marker+"/") {
					visit(child)
				}
			}
			return
		case *kind.Required:
			for _, field := range k.Missing {
				location := append(append([]string{}, e.InstanceLocation...), field)
				violations = append(violations, ValidationViolation{Code: "missing", Path: responsePath(schema, location), Rule: "required"})
			}
		case *kind.Type:
			expected := strings.Join(k.Want, "|")
			violations = append(violations, ValidationViolation{Code: "type", Path: path, Rule: "type", Expected: expected, Observed: k.Got})
		case *kind.Pattern:
			if path == "$.models.[].release_date" {
				add("invalid_date", "date")
			} else {
				add("constraint", "pattern")
			}
		case *kind.AdditionalProperties:
			add("constraint", "additionalProperties")
		case *kind.MinLength:
			add("constraint", "minLength")
		case *kind.MinItems:
			add("constraint", "minItems")
		case *kind.MaxItems:
			add("constraint", "maxItems")
		case *kind.MinProperties:
			add("constraint", "minProperties")
		case *kind.Minimum:
			add("constraint", "minimum")
		case *kind.Maximum:
			add("constraint", "maximum")
		case *kind.Const:
			add("constraint", "const")
		default:
			if len(e.Causes) == 0 {
				add("constraint", "schema")
			}
		}
		for _, child := range e.Causes {
			visit(child)
		}
	}
	visit(root)
	if len(violations) == 0 {
		violations = append(violations, ValidationViolation{Code: "constraint", Path: "$", Rule: "schema"})
	}
	return violations
}

func answerValue(value any, location []string) any {
	if len(location) != 2 || location[0] != "answers" {
		return nil
	}
	root, _ := value.(object)
	answers, _ := root["answers"].(object)
	return answers[location[1]]
}

func jsonTypeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	default:
		return "object"
	}
}

func answerVariant(value any, location []string) int {
	answer, _ := answerValue(value, location).(object)
	switch answer["type"] {
	case "choice":
		return 0
	case "score":
		return 1
	case "noul":
		return 2
	}
	return -1
}

func validationFailure(schema string, violations []ValidationViolation) error {
	// Stable ordering and deduplication operate on masked evidence, never raw keys
	// or validator messages. Repeated array/map failures collapse to one entry.
	key := func(v ValidationViolation) string { b, _ := json.Marshal(v); return string(b) }
	sort.Slice(violations, func(i, j int) bool { return key(violations[i]) < key(violations[j]) })
	unique := make([]ValidationViolation, 0, len(violations))
	for _, v := range violations {
		if len(unique) == 0 || unique[len(unique)-1] != v {
			unique = append(unique, v)
		}
	}
	details := &ValidationDetails{Schema: schema, Version: 1, Violations: []ValidationViolation{}, Truncated: false}
	diagnostic := FailureDiagnostic{Version: 2, Category: "response_contract", Phase: "response_validation", Validation: details}
	for _, v := range unique {
		if len(details.Violations) == validationMaxViolations {
			break
		}
		details.Violations = append(details.Violations, v)
		encoded, _ := json.Marshal(diagnostic)
		if len(encoded) > validationDiagnosticMaxBytes {
			details.Violations = details.Violations[:len(details.Violations)-1]
			break
		}
	}
	details.Truncated = len(details.Violations) < len(unique)
	return &failure{message: "malformed_response: provider result violates response contract; no retry", diagnostic: diagnostic}
}
