package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp/syntax"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/runtimes"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/servers"
)

// Retain source-owned scalar predicates, but defer cause formatting until the
// coordinator has left traversal/storage/registry admissions.
type localFailure struct {
	cause error
	facts string
}

func (failure *localFailure) Error() string { return failure.cause.Error() }
func (failure *localFailure) Unwrap() error { return failure.cause }
func (failure *localFailure) OperatorDetail() diagnostics.Detail {
	detail := diagnostics.Snapshot("catalog", "tools/list", "", failure.cause)
	detail.Explanation = failure.facts + "; " + detail.Explanation
	return detail
}

func predicate(cause error, facts string, values ...any) error {
	return &localFailure{cause: cause, facts: fmt.Sprintf(facts, values...)}
}

func partialPublicationDetail(candidate runtimes.Candidate, runtime *downstream.Runtime, normalized NormalizedCandidate, status ActiveStatus) diagnostics.Detail {
	mode, era, revision := "unknown", "unknown", "unknown"
	if transport, err := servers.DecodeTransport(candidate.Server.Transport); err == nil {
		switch value := transport.(type) {
		case contract.StreamableHTTPTransport:
			mode = string(value.ProtocolMode)
		case contract.StdioTransport:
			mode = "auto"
		}
	}
	if runtime != nil {
		era = string(runtime.Era())
	}
	if status.Revision != nil {
		revision = *status.Revision
	}
	facts := fmt.Sprintf("revision=%s raw=%d accepted=%d rejected=%d omitted_samples=%d mode=%s era=%s; prior_active_retired rejected=%d omitted=%d unknown=%d", revision, normalized.RawCount, len(normalized.Tools), len(normalized.Issues), len(normalized.Issues)-len(normalized.Rejections), mode, era, status.RetiredRejected, status.RetiredMissing, status.RetiredUnknown)
	for _, sample := range normalized.Rejections {
		identity := fmt.Sprintf("page=%d item=%d", sample.Page, sample.Item)
		if sample.Name != "" {
			identity += " tool=" + diagnostics.Text(sample.Name, 48)
		}
		facts += "; " + identity + " " + strings.TrimSuffix(sample.Rule, "; tool descriptor is invalid")
	}
	return diagnostics.Detail{Component: "catalog", Operation: "publish accepted subset", Resource: diagnostics.Text(candidate.Server.DisplayName+" ("+candidate.Server.ID+")", 160), Explanation: diagnostics.Text(facts, 512), Effect: "durable=ack; activation=ack; subset=available"}
}

func jsonKind(value any) string {
	switch value.(type) {
	case nil:
		return "null_or_absent"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64:
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func isolationRule(raw json.RawMessage, namespace string) string {
	if int64(len(raw)) > fixedLimit("tool_descriptor_bytes") {
		return fmt.Sprintf("rule=tool_descriptor_bytes observed=%d allowed=%d", len(raw), fixedLimit("tool_descriptor_bytes"))
	}
	var object map[string]json.RawMessage
	if strictjson.Decode(raw, &object, strictjson.Options{MaxBytes: fixedLimit("tool_descriptor_bytes"), MaxDepth: 64}) != nil || object == nil {
		return "rule=unique_member_object_depth_64; malformed names/values withheld"
	}
	var name string
	if json.Unmarshal(object["name"], &name) != nil || !validToolName(name) {
		return "field=name rule=bounded_tool_identifier; invalid value withheld"
	}
	return fmt.Sprintf("rule=external_tool_name_bytes observed=%d allowed=%d", len(namespace)+1+len(name), fixedLimit("external_tool_name_bytes"))
}

// Compiler errors can contain schema values and arbitrary property names. Only
// reviewed fixed predicates and the regex engine's static code cross this seam.
func schemaCompilerFailure(err error) error {
	var regex *jsonschema.InvalidRegexError
	if errors.As(err, &regex) {
		var parsed *syntax.Error
		if errors.As(regex.Err, &parsed) {
			return predicate(ErrDescriptorInvalid, "keyword=pattern rule=valid_regex compiler=%s; pattern withheld", parsed.Code.String())
		}
		return predicate(ErrDescriptorInvalid, "keyword=pattern rule=valid_regex; pattern withheld")
	}
	var schema *jsonschema.SchemaValidationError
	if errors.As(err, &schema) {
		var validation *jsonschema.ValidationError
		if errors.As(schema.Err, &validation) {
			for n := 0; n < 16 && len(validation.Causes) > 0; n++ {
				validation = validation.Causes[0]
			}
			keyword := "schema"
			if validation.ErrorKind != nil {
				for _, part := range validation.ErrorKind.KeywordPath() {
					switch part {
					case "type", "enum", "pattern", "minimum", "maximum", "minLength", "maxLength", "required", "anyOf", "allOf", "oneOf", "not", "items", "additionalProperties":
						keyword = part
					}
				}
			}
			return predicate(ErrDescriptorInvalid, "keyword=%s rule=metaschema_validation; compiler values/locations withheld", keyword)
		}
	}
	return predicate(ErrDescriptorInvalid, "rule=schema_compilation; unsafe compiler values/locations withheld")
}
