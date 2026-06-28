package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientSendMessageSuccess(t *testing.T) {
	var gotPath string
	var gotReq sendMessageReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"date":1710000000,"chat":{"id":12345}}}`))
	}))
	defer server.Close()

	client := NewClient("token", "12345", WithAPIBase(server.URL))
	msg, err := client.SendMessage(context.Background(), SendMessageRequest{
		Text:                "hello from an agent",
		ParseMode:           "HTML",
		DisableNotification: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "/bottoken/sendMessage", gotPath)
	assert.Equal(t, "12345", gotReq.ChatID)
	assert.Equal(t, "hello from an agent", gotReq.Text)
	assert.Equal(t, "HTML", gotReq.ParseMode)
	assert.True(t, gotReq.DisableNotification)
	assert.Equal(t, 42, msg.MessageID)
	assert.Equal(t, int64(12345), msg.Chat.ID)
}

func TestClientSendMessagePlainParseModeOmitsTelegramParseMode(t *testing.T) {
	var gotReq sendMessageReq
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":42,"date":1710000000,"chat":{"id":12345}}}`))
	}))
	defer server.Close()

	client := NewClient("token", "12345", WithAPIBase(server.URL))
	_, err := client.SendMessage(context.Background(), SendMessageRequest{Text: "hello", ParseMode: "plain"})

	require.NoError(t, err)
	assert.Empty(t, gotReq.ParseMode)
}

func TestNewClientRejectsMissingConfig(t *testing.T) {
	_, err := NewClient("", "12345").SendMessage(context.Background(), SendMessageRequest{Text: "hello"})
	assert.ErrorContains(t, err, "telegram bot token is required")

	_, err = NewClient("token", "").SendMessage(context.Background(), SendMessageRequest{Text: "hello"})
	assert.ErrorContains(t, err, "telegram chat_id is required")
}

func TestClientSendMessageRejectsInvalidRequests(t *testing.T) {
	client := NewClient("token", "12345")

	_, err := client.SendMessage(context.Background(), SendMessageRequest{})
	assert.ErrorContains(t, err, "message is required")

	_, err = client.SendMessage(context.Background(), SendMessageRequest{Text: "hello", ParseMode: "Markdown"})
	assert.ErrorContains(t, err, "parse_mode must be one of")

	_, err = client.SendMessage(context.Background(), SendMessageRequest{Text: string(make([]byte, MaxMessageLength+1))})
	assert.ErrorContains(t, err, "message exceeds Telegram limit")
}

func TestClientSendMessageReturnsTelegramErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request: chat not found"}`))
	}))
	defer server.Close()

	client := NewClient("token", "12345", WithAPIBase(server.URL))
	_, err := client.SendMessage(context.Background(), SendMessageRequest{Text: "hello"})

	assert.ErrorContains(t, err, "telegram sendMessage failed: Bad Request: chat not found")
}

func TestClientSendMessageTransportErrorDoesNotExposeToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	apiBase := server.URL
	server.Close()

	client := NewClient("super-secret-token", "12345", WithAPIBase(apiBase))
	_, err := client.SendMessage(context.Background(), SendMessageRequest{Text: "hello"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "call telegram sendMessage")
	assert.NotContains(t, err.Error(), "super-secret-token")
	assert.NotContains(t, err.Error(), "/botsuper-secret-token")
}

func TestClientSendMessageDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer server.Close()

	client := NewClient("token", "12345", WithAPIBase(server.URL))
	_, err := client.SendMessage(context.Background(), SendMessageRequest{Text: "hello"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode telegram sendMessage response")
}
