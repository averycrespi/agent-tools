package downstream

import (
	"encoding/json"
	"fmt"
	"mime"
	"strconv"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/diagnostics"
	"github.com/averycrespi/agent-tools/agent-gateway/internal/strictjson"
)

func negotiationPredicate(category error, field, rule, facts string) error {
	return diagnostics.WithDetail(category, diagnostics.Detail{Component: "downstream", Operation: "negotiate", Explanation: "field=" + field + " rule=" + rule + " " + facts, Effect: "inspect protocol mode and endpoint contract"})
}

// Failed decodes expose only allowlisted field names and kinds, never malformed
// member names, values or the decoder's potentially payload-bearing message.
func negotiationShape(raw json.RawMessage, spec map[string]string) error {
	value, err := strictjson.ParseValue(raw, strictjson.Options{MaxBytes: limit("downstream_mcp_body_bytes"), MaxDepth: int(limit("json_depth"))})
	if err != nil {
		return negotiationPredicate(ErrUnsupportedProtocol, "result", "bounded_unique_json", fmt.Sprintf("observed_bytes=%d allowed_bytes=%d allowed_depth=%d", len(raw), limit("downstream_mcp_body_bytes"), limit("json_depth")))
	}
	if value.Type != strictjson.ValueObject {
		return negotiationPredicate(ErrUnsupportedProtocol, "result", "kind", "observed="+string(value.Type)+" expected=object")
	}
	for _, member := range value.Object {
		expected, known := spec[member.Name]
		if !known {
			return negotiationPredicate(ErrUnsupportedProtocol, "unknown_member", "closed_object", "name=withheld")
		}
		if expected == "any" {
			continue
		}
		kind := string(member.Value.Type)
		matches := kind == expected
		if expected == "integer" {
			_, err := strconv.ParseInt(member.Value.Number, 10, 64)
			matches = kind == "number" && err == nil
		}
		if !matches {
			return negotiationPredicate(ErrUnsupportedProtocol, member.Name, "kind", "observed="+kind+" expected="+expected)
		}
	}
	return negotiationPredicate(ErrUnsupportedProtocol, "result", "structural_decode", "expected=declared_field_types_and_bounds")
}

func negotiationMedia(contentType string) string {
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "invalid"
	}
	switch media {
	case "application/json":
		return "json"
	case "text/event-stream":
		return "event_stream"
	case "text/plain":
		return "text"
	default:
		return "other"
	}
}

func negotiationContext(err error, method, version string, attempt int, wire WireResponse, rpcCode int64) error {
	if err == nil {
		return nil
	}
	detail := diagnostics.Snapshot("downstream", method, "", err)
	detail.Explanation = fmt.Sprintf("method=%s version=%s attempt=%d HTTP_status=%d media=%s RPC_code=%d session_count=%d fallback=not_selected; %s", method, version, attempt, wire.StatusCode, negotiationMedia(wire.ContentType), rpcCode, len(wire.SessionIDs), detail.Explanation)
	return diagnostics.WithDetail(err, detail)
}
