package mailboxmcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/averycrespi/agent-tools/agent-mailbox/internal/store"
	gomcp "github.com/mark3labs/mcp-go/mcp"
)

type Store interface {
	SendMessage(context.Context, store.SendMessageParams) (store.Message, bool, error)
	ListMessages(context.Context, store.ListMessagesParams) (store.ListMessagesResult, error)
	GetMessage(context.Context, string) (store.MessageDetail, error)
	AckMessage(context.Context, string, string) (store.Message, bool, error)
	ResolveMessageWithResolution(context.Context, string, string, string) (store.Message, bool, error)
}

type Handler struct {
	store Store
}

func NewHandler(st Store) *Handler { return &Handler{store: st} }

func (h *Handler) Tools() []gomcp.Tool {
	return []gomcp.Tool{
		{Name: "send_message", Description: "Send a durable mailbox message", InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{
			"sender": map[string]any{"type": "string"}, "subject": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "channel": map[string]any{"type": "string", "default": "inbox"}, "thread_id": map[string]any{"type": "string"}, "severity": map[string]any{"type": "string", "enum": []string{"info", "success", "warning", "error", "action_required"}, "default": "info"}, "requires_response": map[string]any{"type": "boolean", "default": false}, "idempotency_key": map[string]any{"type": "string"}}, Required: []string{"sender", "subject", "body"}}},
		{Name: "list_messages", Description: "List durable mailbox messages", InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{"new", "acknowledged", "resolved"}}, "channel": map[string]any{"type": "string"}, "sender": map[string]any{"type": "string"}, "severity": map[string]any{"type": "string", "enum": []string{"info", "success", "warning", "error", "action_required"}}, "requires_response": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer", "default": store.DefaultLimit, "maximum": store.MaxLimit}, "offset": map[string]any{"type": "integer", "default": 0}}}},
		{Name: "get_message", Description: "Get a mailbox message and lifecycle events", InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"id": map[string]any{"type": "string"}}, Required: []string{"id"}}},
		{Name: "ack_message", Description: "Acknowledge a mailbox message", InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"id": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string", "default": "user"}}, Required: []string{"id"}}},
		{Name: "resolve_message", Description: "Resolve a mailbox message", InputSchema: gomcp.ToolInputSchema{Type: "object", Properties: map[string]any{"id": map[string]any{"type": "string"}, "actor": map[string]any{"type": "string", "default": "user"}, "resolution": map[string]any{"type": "string"}}, Required: []string{"id"}}},
	}
}

func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	switch req.Params.Name {
	case "send_message":
		return h.sendMessage(ctx, req.GetArguments())
	case "list_messages":
		return h.listMessages(ctx, req.GetArguments())
	case "get_message":
		return h.getMessage(ctx, req.GetArguments())
	case "ack_message":
		return h.ackMessage(ctx, req.GetArguments())
	case "resolve_message":
		return h.resolveMessage(ctx, req.GetArguments())
	default:
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}
}

func (h *Handler) sendMessage(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	msg, created, err := h.store.SendMessage(ctx, store.SendMessageParams{Sender: stringArg(args, "sender"), Subject: stringArg(args, "subject"), Body: stringArg(args, "body"), Channel: stringArg(args, "channel"), ThreadID: stringArg(args, "thread_id"), Severity: store.Severity(stringArg(args, "severity")), RequiresResponse: boolArg(args, "requires_response"), IdempotencyKey: stringArg(args, "idempotency_key")})
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"message": msg, "created": created})
}

func (h *Handler) listMessages(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	requiresResponse, hasRequiresResponse := optionalBoolArg(args, "requires_response")
	p := store.ListMessagesParams{Status: store.Status(stringArg(args, "status")), Channel: stringArg(args, "channel"), Sender: stringArg(args, "sender"), Severity: store.Severity(stringArg(args, "severity")), Limit: intArg(args, "limit"), Offset: intArg(args, "offset")}
	if hasRequiresResponse {
		p.RequiresResponse = &requiresResponse
	}
	result, err := h.store.ListMessages(ctx, p)
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(result)
}

func (h *Handler) getMessage(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	id := stringArg(args, "id")
	if id == "" {
		return gomcp.NewToolResultError("id is required"), nil
	}
	detail, err := h.store.GetMessage(ctx, id)
	if err != nil {
		return notFoundOrError(err)
	}
	return jsonResult(detail)
}

func (h *Handler) ackMessage(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	return h.lifecycle(ctx, args, h.store.AckMessage)
}

func (h *Handler) resolveMessage(ctx context.Context, args map[string]any) (*gomcp.CallToolResult, error) {
	id := stringArg(args, "id")
	if id == "" {
		return gomcp.NewToolResultError("id is required"), nil
	}
	msg, changed, err := h.store.ResolveMessageWithResolution(ctx, id, stringArg(args, "actor"), stringArg(args, "resolution"))
	if err != nil {
		return notFoundOrError(err)
	}
	return jsonResult(map[string]any{"message": msg, "changed": changed})
}

func (h *Handler) lifecycle(ctx context.Context, args map[string]any, fn func(context.Context, string, string) (store.Message, bool, error)) (*gomcp.CallToolResult, error) {
	id := stringArg(args, "id")
	if id == "" {
		return gomcp.NewToolResultError("id is required"), nil
	}
	msg, changed, err := fn(ctx, id, stringArg(args, "actor"))
	if err != nil {
		return notFoundOrError(err)
	}
	return jsonResult(map[string]any{"message": msg, "changed": changed})
}

func jsonResult(payload any) (*gomcp.CallToolResult, error) {
	out, err := json.Marshal(payload)
	if err != nil {
		return gomcp.NewToolResultError("encode failed"), nil
	}
	return gomcp.NewToolResultText(string(out)), nil
}

func notFoundOrError(err error) (*gomcp.CallToolResult, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return gomcp.NewToolResultError("message not found"), nil
	}
	return gomcp.NewToolResultError(err.Error()), nil
}

func stringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	value, _ := optionalBoolArg(args, key)
	return value
}

func optionalBoolArg(args map[string]any, key string) (bool, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return false, false
	}
	v, ok := value.(bool)
	return v, ok
}

func intArg(args map[string]any, key string) int {
	value, ok := args[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
