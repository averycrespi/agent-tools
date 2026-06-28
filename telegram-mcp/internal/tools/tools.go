package tools

import (
	"context"
	"encoding/json"
	"fmt"

	gomcp "github.com/mark3labs/mcp-go/mcp"

	"github.com/averycrespi/agent-tools/telegram-mcp/internal/telegram"
)

var annAdditive = gomcp.ToolAnnotation{
	DestructiveHint: gomcp.ToBoolPtr(false),
	IdempotentHint:  gomcp.ToBoolPtr(false),
	OpenWorldHint:   gomcp.ToBoolPtr(true),
}

// TelegramClient defines the Telegram operations needed by MCP tool handlers.
type TelegramClient interface {
	SendMessage(ctx context.Context, req telegram.SendMessageRequest) (telegram.Message, error)
}

// Handler manages MCP tool definitions and dispatches calls to Telegram.
type Handler struct {
	telegram TelegramClient
}

// NewHandler creates a Handler with the given Telegram client.
func NewHandler(telegramClient TelegramClient) *Handler {
	return &Handler{telegram: telegramClient}
}

// Tools returns the MCP tool definitions.
func (h *Handler) Tools() []gomcp.Tool {
	return []gomcp.Tool{
		{
			Name:        "send_message",
			Description: "Send a text message to the configured Telegram chat",
			Annotations: annAdditive,
			InputSchema: gomcp.ToolInputSchema{
				Type: "object",
				Properties: map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "Message text to send. Maximum 4096 characters.",
					},
					"parse_mode": map[string]any{
						"type":        "string",
						"description": "Optional parse mode: plain, HTML, or MarkdownV2. Defaults to plain.",
						"enum":        []string{"plain", "HTML", "MarkdownV2"},
					},
					"disable_notification": map[string]any{
						"type":        "boolean",
						"description": "Send the message silently when true.",
					},
				},
				Required: []string{"message"},
			},
		},
	}
}

// Handle dispatches an MCP tool call to the appropriate Telegram operation.
func (h *Handler) Handle(ctx context.Context, req gomcp.CallToolRequest) (*gomcp.CallToolResult, error) {
	if req.Params.Name != "send_message" {
		return gomcp.NewToolResultError(fmt.Sprintf("unknown tool: %s", req.Params.Name)), nil
	}

	args := req.GetArguments()
	message, _ := args["message"].(string)
	if message == "" {
		return gomcp.NewToolResultError("message is required"), nil
	}
	parseMode, errResult := optionalString(args, "parse_mode")
	if errResult != nil {
		return errResult, nil
	}
	disableNotification, errResult := optionalBool(args, "disable_notification")
	if errResult != nil {
		return errResult, nil
	}

	msg, err := h.telegram.SendMessage(ctx, telegram.SendMessageRequest{
		Text:                message,
		ParseMode:           parseMode,
		DisableNotification: disableNotification,
	})
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}

	out, err := json.Marshal(map[string]any{
		"message_id": msg.MessageID,
		"chat_id":    msg.Chat.ID,
		"date":       msg.Date,
	})
	if err != nil {
		return gomcp.NewToolResultError(err.Error()), nil
	}
	return gomcp.NewToolResultText(string(out)), nil
}

func optionalString(args map[string]any, key string) (string, *gomcp.CallToolResult) {
	v, ok := args[key]
	if !ok || v == nil {
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", gomcp.NewToolResultError(fmt.Sprintf("%s must be a string", key))
	}
	return s, nil
}

func optionalBool(args map[string]any, key string) (bool, *gomcp.CallToolResult) {
	v, ok := args[key]
	if !ok || v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, gomcp.NewToolResultError(fmt.Sprintf("%s must be a boolean", key))
	}
	return b, nil
}
