package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultAPIBase   = "https://api.telegram.org"
	DefaultTimeout   = 15 * time.Second
	MaxMessageLength = 4096
)

// Client sends messages through the Telegram Bot API.
type Client struct {
	token   string
	chatID  string
	apiBase string
	http    *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithAPIBase overrides the Telegram API base URL. It is primarily useful for tests.
func WithAPIBase(apiBase string) Option {
	return func(c *Client) {
		c.apiBase = strings.TrimRight(apiBase, "/")
	}
}

// WithHTTPClient overrides the HTTP client used by the Telegram client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.http = httpClient
		}
	}
}

// NewClient creates a Telegram Bot API client for a single configured chat.
func NewClient(token, chatID string, opts ...Option) *Client {
	c := &Client{
		token:   token,
		chatID:  chatID,
		apiBase: DefaultAPIBase,
		http:    &http.Client{Timeout: DefaultTimeout},
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// SendMessageRequest describes a message to send to Telegram.
type SendMessageRequest struct {
	Text                string
	ParseMode           string
	DisableNotification bool
}

// Message is the subset of Telegram's Message response returned to MCP callers.
type Message struct {
	MessageID int  `json:"message_id"`
	Date      int  `json:"date"`
	Chat      Chat `json:"chat"`
}

// Chat is the subset of Telegram's Chat object needed by callers.
type Chat struct {
	ID int64 `json:"id"`
}

type sendMessageReq struct {
	ChatID              string `json:"chat_id"`
	Text                string `json:"text"`
	ParseMode           string `json:"parse_mode,omitempty"`
	DisableNotification bool   `json:"disable_notification,omitempty"`
}

type sendMessageResp struct {
	OK          bool    `json:"ok"`
	Description string  `json:"description"`
	Result      Message `json:"result"`
}

// SendMessage sends a text message to the configured Telegram chat.
func (c *Client) SendMessage(ctx context.Context, req SendMessageRequest) (Message, error) {
	if err := c.validate(req); err != nil {
		return Message{}, err
	}

	parseMode := req.ParseMode
	if parseMode == "plain" {
		parseMode = ""
	}
	body, err := json.Marshal(sendMessageReq{
		ChatID:              c.chatID,
		Text:                req.Text,
		ParseMode:           parseMode,
		DisableNotification: req.DisableNotification,
	})
	if err != nil {
		return Message{}, fmt.Errorf("marshal telegram sendMessage request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiURL("sendMessage"), bytes.NewReader(body))
	if err != nil {
		return Message{}, fmt.Errorf("create telegram sendMessage request: %s", sanitizeError(err, c.token))
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Message{}, fmt.Errorf("call telegram sendMessage: %s", sanitizeError(err, c.token))
	}
	defer func() { _ = resp.Body.Close() }()

	var result sendMessageResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return Message{}, fmt.Errorf("decode telegram sendMessage response: %w", err)
	}
	if !result.OK {
		if result.Description == "" {
			result.Description = resp.Status
		}
		return Message{}, fmt.Errorf("telegram sendMessage failed: %s", result.Description)
	}
	return result.Result, nil
}

func (c *Client) validate(req SendMessageRequest) error {
	if c.token == "" {
		return fmt.Errorf("telegram bot token is required")
	}
	if c.chatID == "" {
		return fmt.Errorf("telegram chat_id is required")
	}
	if strings.TrimSpace(req.Text) == "" {
		return fmt.Errorf("message is required")
	}
	if utf8.RuneCountInString(req.Text) > MaxMessageLength {
		return fmt.Errorf("message exceeds Telegram limit of %d characters", MaxMessageLength)
	}
	if !validParseMode(req.ParseMode) {
		return fmt.Errorf("parse_mode must be one of plain, HTML, MarkdownV2")
	}
	return nil
}

func validParseMode(parseMode string) bool {
	switch parseMode {
	case "", "plain", "HTML", "MarkdownV2":
		return true
	default:
		return false
	}
}

func (c *Client) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", c.apiBase, c.token, method)
}

func sanitizeError(err error, token string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if token == "" {
		return msg
	}
	return strings.ReplaceAll(msg, token, "<redacted>")
}
