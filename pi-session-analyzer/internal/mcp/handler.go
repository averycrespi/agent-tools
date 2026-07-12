package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

const maxResponseBytes = 50000

var readOnlyAnnotations = gomcp.ToolAnnotation{ReadOnlyHint: gomcp.ToBoolPtr(true), DestructiveHint: gomcp.ToBoolPtr(false), IdempotentHint: gomcp.ToBoolPtr(true), OpenWorldHint: gomcp.ToBoolPtr(false)}

type Handler struct {
	store  *store.Store
	dbPath string
}

func NewHandler(database *store.Store, dbPath string) *Handler {
	return &Handler{store: database, dbPath: dbPath}
}

func (h *Handler) Tools() []gomcp.Tool {
	return []gomcp.Tool{
		tool("list_sessions", "List bounded indexed session summaries", map[string]any{"limit": integerProperty(1, 100), "cwd_filter": stringProperty()}, nil),
		tool("session_summary", "Get one session summary with usage, tool, goal, and finding state", map[string]any{"session_id": stringProperty()}, []string{"session_id"}),
		tool("top_failures", "List bounded findings ordered by severity and stable provenance", map[string]any{"limit": integerProperty(1, 50), "detector": stringProperty(), "classification": enumProperty("structural", "heuristic"), "min_severity": enumProperty("error", "warn", "info")}, nil),
		tool("get_conversation", "Get bounded source-order message and tool summaries", map[string]any{"session_id": stringProperty(), "anchor_message_id": stringProperty(), "max_messages": integerProperty(1, 100)}, []string{"session_id"}),
		tool("get_message", "Get one message and optionally project a dotted field", map[string]any{"session_id": stringProperty(), "message_id": stringProperty(), "path": stringProperty()}, []string{"session_id", "message_id"}),
		tool("run_select", "Run one bounded read-only SELECT or CTE", map[string]any{"query": stringProperty()}, []string{"query"}),
	}
}

func tool(name, description string, properties map[string]any, required []string) gomcp.Tool {
	return gomcp.Tool{Name: name, Description: description, Annotations: readOnlyAnnotations, InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: properties, Required: required}}
}
func stringProperty() map[string]any { return map[string]any{"type": "string"} }
func integerProperty(min, max int) map[string]any {
	return map[string]any{"type": "integer", "minimum": min, "maximum": max}
}
func enumProperty(values ...string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	args := req.GetArguments()
	var value any
	var err error
	switch req.Params.Name {
	case "list_sessions":
		value, err = h.store.ListSessions(ctx, boundedInt(args, "limit", 100, 100), optionalString(args, "cwd_filter"))
	case "session_summary":
		var id string
		id, err = requiredString(args, "session_id")
		if err == nil {
			value, err = h.store.SessionSummary(ctx, id)
		}
	case "top_failures":
		value, err = h.store.TopFailures(ctx, store.FailureQuery{Limit: boundedInt(args, "limit", 50, 50), Detector: optionalString(args, "detector"), Classification: optionalString(args, "classification"), MinSeverity: optionalString(args, "min_severity")})
	case "get_conversation":
		var id string
		id, err = requiredString(args, "session_id")
		if err == nil {
			value, err = h.store.Conversation(ctx, id, optionalString(args, "anchor_message_id"), boundedInt(args, "max_messages", 100, 100))
		}
	case "get_message":
		value, err = h.getMessage(ctx, args)
	case "run_select":
		var query string
		query, err = requiredString(args, "query")
		if err == nil {
			value, err = RunSelect(ctx, h.dbPath, query)
		}
	default:
		err = fmt.Errorf("unknown tool: %s", req.Params.Name)
	}
	return cappedResult(value, err), nil
}

func (h *Handler) getMessage(ctx context.Context, args map[string]any) (any, error) {
	sessionID, err := requiredString(args, "session_id")
	if err != nil {
		return nil, err
	}
	messageID, err := requiredString(args, "message_id")
	if err != nil {
		return nil, err
	}
	value, err := h.store.Message(ctx, sessionID, messageID)
	if err != nil {
		return nil, err
	}
	path := optionalString(args, "path")
	if path == "" {
		return value, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var projected any
	if err = json.Unmarshal(data, &projected); err != nil {
		return nil, err
	}
	for _, part := range strings.Split(path, ".") {
		object, ok := projected.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("projection path %q is not an object field", path)
		}
		projected, ok = object[part]
		if !ok {
			return nil, fmt.Errorf("projection path %q not found", path)
		}
	}
	return map[string]any{"path": path, "value": projected}, nil
}

func requiredString(args map[string]any, key string) (string, error) {
	value, ok := args[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required and must be a non-empty string", key)
	}
	return value, nil
}
func optionalString(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}
func boundedInt(args map[string]any, key string, fallback, max int) int {
	var n int
	switch value := args[key].(type) {
	case int:
		n = value
	case float64:
		n = int(value)
	default:
		return fallback
	}
	if n < 1 {
		return 1
	}
	if n > max {
		return max
	}
	return n
}

func cappedResult(value any, resultErr error) *gomcp.CallToolResult {
	if resultErr != nil {
		text := marshalCapped(map[string]any{"error": resultErr.Error()})
		return gomcp.NewToolResultError(text)
	}
	return gomcp.NewToolResultText(marshalCapped(value))
}
func marshalCapped(value any) string {
	budget := maxResponseBytes / 2
	bounded, truncated := boundedJSONValue(reflect.ValueOf(value), &budget)
	if truncated {
		bounded = map[string]any{"truncated": true, "value": bounded}
	}
	data, err := json.Marshal(bounded)
	if err != nil {
		return `{"error":"response serialization failed"}`
	}
	if len(data) > maxResponseBytes {
		return `{"truncated":true}`
	}
	return string(data)
}

func boundedJSONValue(value reflect.Value, budget *int) (any, bool) {
	if !value.IsValid() || ((value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) && value.IsNil()) {
		return nil, false
	}
	if value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		return boundedJSONValue(value.Elem(), budget)
	}
	if *budget <= 0 {
		return nil, true
	}
	switch value.Kind() {
	case reflect.String:
		text := value.String()
		if len(text) > *budget {
			text = text[:*budget]
			*budget = 0
			return text, true
		}
		*budget -= len(text)
		return text, false
	case reflect.Bool:
		*budget -= 5
		return value.Bool(), false
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		*budget -= 20
		return value.Int(), false
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		*budget -= 20
		return value.Uint(), false
	case reflect.Float32, reflect.Float64:
		*budget -= 24
		return value.Float(), false
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8 {
			return boundedJSONValue(reflect.ValueOf(string(value.Bytes())), budget)
		}
		out := make([]any, 0, min(value.Len(), *budget/8+1))
		truncated := false
		for i := 0; i < value.Len(); i++ {
			if *budget <= 0 {
				truncated = true
				break
			}
			*budget -= 2
			item, cut := boundedJSONValue(value.Index(i), budget)
			out = append(out, item)
			truncated = truncated || cut
		}
		return out, truncated
	case reflect.Map:
		out := map[string]any{}
		truncated := false
		for _, key := range value.MapKeys() {
			if key.Kind() != reflect.String || *budget <= 0 {
				truncated = true
				break
			}
			name := key.String()
			*budget -= len(name) + 4
			item, cut := boundedJSONValue(value.MapIndex(key), budget)
			out[name] = item
			truncated = truncated || cut
		}
		return out, truncated
	case reflect.Struct:
		out := map[string]any{}
		truncated := false
		typeInfo := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typeInfo.Field(i)
			if !field.IsExported() {
				continue
			}
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if *budget <= 0 {
				truncated = true
				break
			}
			*budget -= len(name) + 4
			item, cut := boundedJSONValue(value.Field(i), budget)
			out[name] = item
			truncated = truncated || cut
		}
		return out, truncated
	default:
		*budget -= 16
		return fmt.Sprint(value.Interface()), false
	}
}
