package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	gomcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/averycrespi/agent-tools/telegram-mcp/internal/telegram"
)

type mockTelegramClient struct {
	sendMessageFunc func(ctx context.Context, req telegram.SendMessageRequest) (telegram.Message, error)
}

func (m *mockTelegramClient) SendMessage(ctx context.Context, req telegram.SendMessageRequest) (telegram.Message, error) {
	if m.sendMessageFunc != nil {
		return m.sendMessageFunc(ctx, req)
	}
	return telegram.Message{MessageID: 42, Date: 1710000000, Chat: telegram.Chat{ID: 12345}}, nil
}

func TestToolDefinitions(t *testing.T) {
	h := NewHandler(&mockTelegramClient{})
	tools := h.Tools()
	require.Len(t, tools, 1)
	assert.Equal(t, "send_message", tools[0].Name)
	assert.Contains(t, tools[0].Description, "configured Telegram chat")
	assert.Equal(t, []string{"message"}, tools[0].InputSchema.Required)
}

func TestSendMessageHandlerSuccess(t *testing.T) {
	h := NewHandler(&mockTelegramClient{
		sendMessageFunc: func(ctx context.Context, req telegram.SendMessageRequest) (telegram.Message, error) {
			assert.Equal(t, "hello", req.Text)
			assert.Equal(t, "HTML", req.ParseMode)
			assert.True(t, req.DisableNotification)
			return telegram.Message{MessageID: 99, Date: 1710000000, Chat: telegram.Chat{ID: 12345}}, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "send_message"
	req.Params.Arguments = map[string]any{
		"message":              "hello",
		"parse_mode":           "HTML",
		"disable_notification": true,
	}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	require.False(t, result.IsError)
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content[0].(gomcp.TextContent).Text), &out))
	assert.Equal(t, float64(99), out["message_id"])
	assert.Equal(t, float64(12345), out["chat_id"])
}

func TestSendMessageHandlerForwardsContext(t *testing.T) {
	type contextKey string
	const key contextKey = "request-id"
	ctx := context.WithValue(context.Background(), key, "abc123")
	h := NewHandler(&mockTelegramClient{
		sendMessageFunc: func(ctx context.Context, req telegram.SendMessageRequest) (telegram.Message, error) {
			assert.Equal(t, "abc123", ctx.Value(key))
			return telegram.Message{MessageID: 42, Chat: telegram.Chat{ID: 12345}}, nil
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "send_message"
	req.Params.Arguments = map[string]any{"message": "hello"}

	_, err := h.Handle(ctx, req)

	require.NoError(t, err)
}

func TestSendMessageHandlerMissingMessage(t *testing.T) {
	h := NewHandler(&mockTelegramClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "send_message"
	req.Params.Arguments = map[string]any{}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestSendMessageHandlerRejectsMalformedOptionalArguments(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "parse_mode",
			args: map[string]any{"message": "hello", "parse_mode": true},
		},
		{
			name: "disable_notification",
			args: map[string]any{"message": "hello", "disable_notification": "true"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(&mockTelegramClient{
				sendMessageFunc: func(ctx context.Context, req telegram.SendMessageRequest) (telegram.Message, error) {
					t.Fatal("SendMessage should not be called for malformed arguments")
					return telegram.Message{}, nil
				},
			})
			req := gomcp.CallToolRequest{}
			req.Params.Name = "send_message"
			req.Params.Arguments = tt.args

			result, err := h.Handle(context.Background(), req)

			require.NoError(t, err)
			assert.True(t, result.IsError)
		})
	}
}

func TestSendMessageHandlerClientError(t *testing.T) {
	h := NewHandler(&mockTelegramClient{
		sendMessageFunc: func(ctx context.Context, req telegram.SendMessageRequest) (telegram.Message, error) {
			return telegram.Message{}, fmt.Errorf("telegram sendMessage failed")
		},
	})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "send_message"
	req.Params.Arguments = map[string]any{"message": "hello"}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, result.IsError)
}

func TestSendMessageAnnotation(t *testing.T) {
	h := NewHandler(&mockTelegramClient{})
	tool := h.Tools()[0]
	a := tool.Annotations
	require.NotNil(t, a.DestructiveHint)
	assert.False(t, *a.DestructiveHint)
	require.NotNil(t, a.OpenWorldHint)
	assert.True(t, *a.OpenWorldHint)
	require.NotNil(t, a.IdempotentHint)
	assert.False(t, *a.IdempotentHint)
	assert.Nil(t, a.ReadOnlyHint)
}

func TestUnknownTool(t *testing.T) {
	h := NewHandler(&mockTelegramClient{})
	req := gomcp.CallToolRequest{}
	req.Params.Name = "unknown"
	req.Params.Arguments = map[string]any{"message": "hello"}

	result, err := h.Handle(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, result.IsError)
}
