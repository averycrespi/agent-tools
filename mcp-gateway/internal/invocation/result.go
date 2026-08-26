package invocation

import (
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/url"
	"strconv"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/downstream"
	"github.com/averycrespi/agent-tools/mcp-gateway/internal/strictjson"
)

type ProjectedCallResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           *bool             `json:"isError,omitempty"`
}

type CallOutcome struct {
	Result        *ProjectedCallResult
	ErrorCode     contract.AgentCallErrorCode
	TerminalClass contract.InvocationTerminalClass
}

func (outcome CallOutcome) SafeString() string {
	return string(outcome.ErrorCode) + "/" + string(outcome.TerminalClass)
}

func ClassifyAdmission(committed bool, class contract.InvocationAdmissionClass, decision *contract.AuthorizationDecision) (contract.AgentCallErrorCode, bool) {
	if !committed {
		return contract.AuditUnavailable, false
	}
	if class == contract.AdmissionEvaluated && decision != nil && *decision == contract.DecisionAllow {
		return "", true
	}
	return contract.CallRejected, false
}

func SanitizeCallResult(result downstream.CallResult) CallOutcome {
	if result.Err != nil {
		switch result.Failure {
		case downstream.FailurePreStart:
			return failedOutcome(contract.ToolUnavailable, contract.TerminalPrestartFailure)
		case downstream.FailureResponseInvalid:
			return failedOutcome(contract.DownstreamFailure, contract.TerminalDownstreamFailure)
		default:
			return failedOutcome(contract.OutcomeUnknown, contract.TerminalOutcomeUnknown)
		}
	}
	if result.Failure != "" {
		return failedOutcome(contract.OutcomeUnknown, contract.TerminalOutcomeUnknown)
	}
	if result.Response.Error != nil {
		return failedOutcome(contract.DownstreamFailure, contract.TerminalDownstreamFailure)
	}
	projected, toolError, ok := projectCallResult(result.Response.Result)
	if !ok || toolError {
		return failedOutcome(contract.DownstreamFailure, contract.TerminalDownstreamFailure)
	}
	return CallOutcome{Result: projected, TerminalClass: contract.TerminalSucceeded}
}

func failedOutcome(code contract.AgentCallErrorCode, terminal contract.InvocationTerminalClass) CallOutcome {
	return CallOutcome{ErrorCode: code, TerminalClass: terminal}
}

func projectCallResult(raw json.RawMessage) (*ProjectedCallResult, bool, bool) {
	value, err := strictjson.ParseValue(raw, strictjson.Options{MaxBytes: resultLimit(), MaxDepth: jsonDepthLimit()})
	if err != nil || value.Type != strictjson.ValueObject {
		return nil, false, false
	}
	fields, ok := closedFields(value, "content", "structuredContent", "isError", "resultType", "_meta")
	if !ok {
		return nil, false, false
	}
	if resultType, present := fields["resultType"]; present && (resultType.Type != strictjson.ValueString || resultType.String != "complete") {
		return nil, false, false
	}
	content, present := fields["content"]
	if !present || content.Type != strictjson.ValueArray {
		return nil, false, false
	}
	projected := &ProjectedCallResult{Content: make([]json.RawMessage, len(content.Array))}
	for index, block := range content.Array {
		clean, valid := projectContent(block)
		if !valid {
			return nil, false, false
		}
		encoded, encodeErr := strictjson.EncodeCompact(clean)
		if encodeErr != nil {
			return nil, false, false
		}
		projected.Content[index] = json.RawMessage(encoded)
	}
	if structured, present := fields["structuredContent"]; present {
		encoded, encodeErr := strictjson.EncodeCompact(structured)
		if encodeErr != nil {
			return nil, false, false
		}
		projected.StructuredContent = json.RawMessage(encoded)
	}
	toolError := false
	if isError, present := fields["isError"]; present {
		if isError.Type != strictjson.ValueBoolean {
			return nil, false, false
		}
		value := isError.Boolean
		projected.IsError = &value
		toolError = value
	}
	encoded, err := json.Marshal(projected)
	if err != nil || int64(len(encoded)) > resultLimit() {
		return nil, false, false
	}
	return projected, toolError, true
}

func projectContent(value strictjson.Value) (strictjson.Value, bool) {
	if value.Type != strictjson.ValueObject {
		return strictjson.Value{}, false
	}
	probe, ok := closedFields(value,
		"type", "text", "data", "mimeType", "resource", "uri", "name", "title", "description", "size", "annotations", "icons", "_meta",
	)
	if !ok {
		return strictjson.Value{}, false
	}
	kind, ok := requiredString(probe, "type")
	if !ok {
		return strictjson.Value{}, false
	}
	var allowed map[string]bool
	switch kind {
	case "text":
		allowed = names("type", "text", "annotations", "_meta")
		if _, valid := requiredString(probe, "text"); !valid {
			return strictjson.Value{}, false
		}
	case "image", "audio":
		allowed = names("type", "data", "mimeType", "annotations", "_meta")
		data, valid := requiredString(probe, "data")
		if !valid || !validBase64(data) {
			return strictjson.Value{}, false
		}
		if _, valid := requiredString(probe, "mimeType"); !valid {
			return strictjson.Value{}, false
		}
	case "resource":
		allowed = names("type", "resource", "annotations", "_meta")
		resource, present := probe["resource"]
		if !present {
			return strictjson.Value{}, false
		}
		clean, valid := projectResource(resource)
		if !valid {
			return strictjson.Value{}, false
		}
		probe["resource"] = clean
	case "resource_link":
		allowed = names("type", "uri", "name", "title", "description", "mimeType", "size", "annotations", "icons", "_meta")
		uri, valid := requiredString(probe, "uri")
		if !valid || !validURI(uri) {
			return strictjson.Value{}, false
		}
		if _, valid := requiredString(probe, "name"); !valid {
			return strictjson.Value{}, false
		}
		for _, name := range []string{"title", "description", "mimeType"} {
			if !optionalString(probe, name) {
				return strictjson.Value{}, false
			}
		}
		if size, present := probe["size"]; present && !nonnegativeInteger(size) {
			return strictjson.Value{}, false
		}
		if icons, present := probe["icons"]; present {
			clean, valid := projectIcons(icons)
			if !valid {
				return strictjson.Value{}, false
			}
			probe["icons"] = clean
		}
	default:
		return strictjson.Value{}, false
	}
	for name := range probe {
		if !allowed[name] {
			return strictjson.Value{}, false
		}
	}
	if annotations, present := probe["annotations"]; present {
		clean, valid := projectAnnotations(annotations)
		if !valid {
			return strictjson.Value{}, false
		}
		probe["annotations"] = clean
	}
	return rebuildObject(value, probe, allowed), true
}

func projectResource(value strictjson.Value) (strictjson.Value, bool) {
	fields, ok := closedFields(value, "uri", "mimeType", "text", "blob", "_meta")
	if !ok {
		return strictjson.Value{}, false
	}
	uri, valid := requiredString(fields, "uri")
	if !valid || !validURI(uri) || !optionalString(fields, "mimeType") {
		return strictjson.Value{}, false
	}
	text, hasText := fields["text"]
	blob, hasBlob := fields["blob"]
	if hasText == hasBlob || hasText && text.Type != strictjson.ValueString || hasBlob && (blob.Type != strictjson.ValueString || !validBase64(blob.String)) {
		return strictjson.Value{}, false
	}
	return rebuildObject(value, fields, names("uri", "mimeType", "text", "blob", "_meta")), true
}

func projectAnnotations(value strictjson.Value) (strictjson.Value, bool) {
	fields, ok := closedFields(value, "audience", "priority", "lastModified", "_meta")
	if !ok {
		return strictjson.Value{}, false
	}
	if audience, present := fields["audience"]; present {
		if audience.Type != strictjson.ValueArray {
			return strictjson.Value{}, false
		}
		for _, role := range audience.Array {
			if role.Type != strictjson.ValueString || role.String != "user" && role.String != "assistant" {
				return strictjson.Value{}, false
			}
		}
	}
	if priority, present := fields["priority"]; present {
		if priority.Type != strictjson.ValueNumber {
			return strictjson.Value{}, false
		}
		number, err := strconv.ParseFloat(priority.Number, 64)
		if err != nil || number < 0 || number > 1 {
			return strictjson.Value{}, false
		}
	}
	if modified, present := fields["lastModified"]; present {
		if modified.Type != strictjson.ValueString {
			return strictjson.Value{}, false
		}
		if _, err := time.Parse(time.RFC3339, modified.String); err != nil {
			return strictjson.Value{}, false
		}
	}
	return rebuildObject(value, fields, names("audience", "priority", "lastModified", "_meta")), true
}

func projectIcons(value strictjson.Value) (strictjson.Value, bool) {
	if value.Type != strictjson.ValueArray {
		return strictjson.Value{}, false
	}
	clean := strictjson.Value{Type: strictjson.ValueArray, Array: make([]strictjson.Value, len(value.Array))}
	for index, icon := range value.Array {
		fields, ok := closedFields(icon, "src", "mimeType", "sizes", "theme", "_meta")
		if !ok {
			return strictjson.Value{}, false
		}
		source, valid := requiredString(fields, "src")
		if !valid || !validURI(source) || !optionalString(fields, "mimeType") {
			return strictjson.Value{}, false
		}
		if sizes, present := fields["sizes"]; present {
			if sizes.Type != strictjson.ValueArray {
				return strictjson.Value{}, false
			}
			for _, size := range sizes.Array {
				if size.Type != strictjson.ValueString {
					return strictjson.Value{}, false
				}
			}
		}
		if theme, present := fields["theme"]; present && (theme.Type != strictjson.ValueString || theme.String != "light" && theme.String != "dark") {
			return strictjson.Value{}, false
		}
		clean.Array[index] = rebuildObject(icon, fields, names("src", "mimeType", "sizes", "theme", "_meta"))
	}
	return clean, true
}

func closedFields(value strictjson.Value, allowed ...string) (map[string]strictjson.Value, bool) {
	if value.Type != strictjson.ValueObject {
		return nil, false
	}
	permitted := names(allowed...)
	fields := make(map[string]strictjson.Value, len(value.Object))
	for _, member := range value.Object {
		if !permitted[member.Name] {
			return nil, false
		}
		fields[member.Name] = member.Value
	}
	return fields, true
}

func rebuildObject(original strictjson.Value, fields map[string]strictjson.Value, allowed map[string]bool) strictjson.Value {
	result := strictjson.Value{Type: strictjson.ValueObject, Object: make([]strictjson.Member, 0, len(original.Object))}
	for _, member := range original.Object {
		if member.Name == "_meta" || !allowed[member.Name] {
			continue
		}
		result.Object = append(result.Object, strictjson.Member{Name: member.Name, Value: fields[member.Name]})
	}
	return result
}

func names(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func requiredString(fields map[string]strictjson.Value, name string) (string, bool) {
	value, present := fields[name]
	return value.String, present && value.Type == strictjson.ValueString
}

func optionalString(fields map[string]strictjson.Value, name string) bool {
	value, present := fields[name]
	return !present || value.Type == strictjson.ValueString
}

func validBase64(value string) bool {
	_, err := base64.StdEncoding.Strict().DecodeString(value)
	return err == nil
}

func validURI(value string) bool {
	limit, ok := contract.FixedLimitByName("resource_url_bytes")
	if !ok || int64(len(value)) > limit.Maximum {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.IsAbs()
}

func nonnegativeInteger(value strictjson.Value) bool {
	if value.Type != strictjson.ValueNumber {
		return false
	}
	var parsed big.Rat
	if _, ok := parsed.SetString(value.Number); !ok {
		return false
	}
	return parsed.IsInt() && parsed.Sign() >= 0 && parsed.Num().IsInt64()
}

func resultLimit() int64 {
	limit, ok := contract.FixedLimitByName("downstream_mcp_body_bytes")
	if !ok {
		return 0
	}
	return limit.Maximum
}

func jsonDepthLimit() int {
	limit, ok := contract.FixedLimitByName("json_depth")
	if !ok {
		return 0
	}
	return int(limit.Maximum)
}
