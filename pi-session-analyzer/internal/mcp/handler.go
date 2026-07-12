package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/robound"
	"github.com/averycrespi/agent-tools/pi-session-analyzer/internal/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

const maxResponseBytes = robound.MaxResponseBytes

var readOnlyAnnotations = gomcp.ToolAnnotation{ReadOnlyHint: gomcp.ToBoolPtr(true), DestructiveHint: gomcp.ToBoolPtr(false), IdempotentHint: gomcp.ToBoolPtr(true), OpenWorldHint: gomcp.ToBoolPtr(false)}

type Handler struct {
	reader   *store.Reader
	boundary *robound.Conn
}

func NewHandler(boundary *robound.Conn) *Handler {
	reader := store.NewReader(
		func(ctx context.Context, query string, args ...any) (store.Rows, error) {
			return boundary.QueryContext(ctx, query, args...)
		},
		func(ctx context.Context, query string, args ...any) store.Row {
			return boundary.QueryRowContext(ctx, query, args...)
		},
	)
	return &Handler{reader: reader, boundary: boundary}
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
	queryCtx, cancel := robound.WithTimeout(ctx)
	defer cancel()
	ctx = queryCtx
	args := req.GetArguments()
	var value any
	var err error
	switch req.Params.Name {
	case "list_sessions":
		value, err = h.reader.ListSessions(ctx, boundedInt(args, "limit", 100, 100), optionalString(args, "cwd_filter"))
	case "session_summary":
		var id string
		id, err = requiredString(args, "session_id")
		if err == nil {
			value, err = h.reader.SessionSummary(ctx, id)
		}
	case "top_failures":
		value, err = h.reader.TopFailures(ctx, store.FailureQuery{Limit: boundedInt(args, "limit", 50, 50), Detector: optionalString(args, "detector"), Classification: optionalString(args, "classification"), MinSeverity: optionalString(args, "min_severity")})
	case "get_conversation":
		var id string
		id, err = requiredString(args, "session_id")
		if err == nil {
			value, err = h.reader.Conversation(ctx, id, optionalString(args, "anchor_message_id"), boundedInt(args, "max_messages", 100, 100))
		}
	case "get_message":
		value, err = h.getMessage(ctx, args)
	case "run_select":
		var query string
		query, err = requiredString(args, "query")
		if err == nil {
			value, err = RunSelect(ctx, h.boundary, query)
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
	value, err := h.reader.Message(ctx, sessionID, messageID)
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
	return robound.MarshalCapped(value)
}
