package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
	"unicode/utf8"
)

const (
	defaultAPIBase = "https://api.telegram.org"
	maxValueLen    = 120
	pollTimeout    = 30
)

// ToolLister provides descriptions for known tools.
type ToolLister interface {
	ToolDescription(name string) string
}

// Approver sends approval requests via Telegram and polls for responses.
// It implements broker.Approver and requires no inbound connections — it only
// makes outbound HTTP calls to the Telegram Bot API.
type Approver struct {
	token   string
	chatID  string
	apiBase string
	client  *http.Client
	logger  *slog.Logger
	tools   ToolLister
}

// WithTools attaches a ToolLister so Review can include tool descriptions in messages.
func (a *Approver) WithTools(tl ToolLister) {
	a.tools = tl
}

// New creates a TelegramApprover for production use.
func New(token, chatID string, logger *slog.Logger) *Approver {
	return newWithBase(token, chatID, defaultAPIBase, &http.Client{Timeout: 40 * time.Second}, logger)
}

// newWithBase creates an Approver with a custom API base URL (used in tests).
func newWithBase(token, chatID, apiBase string, client *http.Client, logger *slog.Logger) *Approver {
	return &Approver{
		token:   token,
		chatID:  chatID,
		apiBase: apiBase,
		client:  client,
		logger:  logger,
	}
}

// Review sends a Telegram notification and blocks until the user taps Approve or
// Deny, the context is cancelled, or the deadline is reached.
// Returns (approved, denialReason, err). On context cancellation/timeout:
// returns (false, "timeout", nil) — the caller should not treat this as an error.
func (a *Approver) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error) {
	timeout := timeoutLabel(ctx)
	argsStr := formatArgs(args)
	desc := ""
	if a.tools != nil {
		if d := a.tools.ToolDescription(tool); d != "" {
			desc = "\n" + truncate(d, 120)
		}
	}
	text := fmt.Sprintf("🔧 <code>%s</code>%s\n\n<pre>%s</pre>\n\n⏳ %s", tool, desc, argsStr, timeout)

	msgID, err := a.sendMessage(ctx, text)
	if err != nil {
		if ctx.Err() != nil {
			return false, "timeout", nil
		}
		return false, "", fmt.Errorf("send telegram message: %w", err)
	}

	approved, denialReason, err := a.pollForDecision(ctx, msgID)

	// Best-effort: update the message to show the outcome.
	outcome := resolvedText(approved, denialReason, err, ctx, tool, argsStr)
	_ = a.editMessage(context.Background(), msgID, outcome)

	if err != nil {
		return false, "timeout", nil
	}
	return approved, denialReason, nil
}

func (a *Approver) pollForDecision(ctx context.Context, messageID int) (bool, string, error) {
	offset := 0
	for {
		if ctx.Err() != nil {
			return false, "", ctx.Err()
		}

		updates, err := a.getUpdates(ctx, offset)
		if err != nil {
			if ctx.Err() != nil {
				return false, "", ctx.Err()
			}
			if a.logger != nil {
				a.logger.Warn("telegram poll error", "error", err)
			}
			select {
			case <-time.After(2 * time.Second):
			case <-ctx.Done():
				return false, "", ctx.Err()
			}
			continue
		}

		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if update.CallbackQuery == nil {
				continue
			}
			if update.CallbackQuery.Message.MessageID != messageID {
				continue
			}
			_ = a.answerCallbackQuery(context.Background(), update.CallbackQuery.ID)
			if update.CallbackQuery.Data == "approve" {
				return true, "", nil
			}
			return false, "user", nil
		}
	}
}

func (a *Approver) apiURL(method string) string {
	return fmt.Sprintf("%s/bot%s/%s", a.apiBase, a.token, method)
}

type sendMessageReq struct {
	ChatID      string         `json:"chat_id"`
	Text        string         `json:"text"`
	ParseMode   string         `json:"parse_mode"`
	ReplyMarkup inlineKeyboard `json:"reply_markup"`
}

type inlineKeyboard struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type sendMessageResp struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

type getUpdatesResp struct {
	OK     bool     `json:"ok"`
	Result []Update `json:"result"`
}

// Update is a Telegram update object (only callback_query populated here).
type Update struct {
	UpdateID      int            `json:"update_id"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// CallbackQuery is a Telegram inline keyboard callback.
type CallbackQuery struct {
	ID      string  `json:"id"`
	Data    string  `json:"data"`
	Message Message `json:"message"`
}

// Message holds just the message_id we need for correlation.
type Message struct {
	MessageID int `json:"message_id"`
}

func (a *Approver) sendMessage(ctx context.Context, text string) (int, error) {
	req := sendMessageReq{
		ChatID:    a.chatID,
		Text:      text,
		ParseMode: "HTML",
		ReplyMarkup: inlineKeyboard{
			InlineKeyboard: [][]inlineButton{{
				{Text: "✅ Approve", CallbackData: "approve"},
				{Text: "❌ Deny", CallbackData: "deny"},
			}},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return 0, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL("sendMessage"), bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result sendMessageResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, err
	}
	if !result.OK {
		return 0, fmt.Errorf("telegram sendMessage failed")
	}
	return result.Result.MessageID, nil
}

func (a *Approver) getUpdates(ctx context.Context, offset int) ([]Update, error) {
	u := fmt.Sprintf("%s?offset=%d&timeout=%d&allowed_updates=[\"callback_query\"]",
		a.apiURL("getUpdates"), offset, pollTimeout)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	var result getUpdatesResp
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	if !result.OK {
		return nil, fmt.Errorf("telegram getUpdates failed")
	}
	return result.Result, nil
}

func (a *Approver) answerCallbackQuery(ctx context.Context, callbackQueryID string) error {
	body, _ := json.Marshal(map[string]string{"callback_query_id": callbackQueryID})
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL("answerCallbackQuery"), bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func (a *Approver) editMessage(ctx context.Context, messageID int, text string) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":    a.chatID,
		"message_id": messageID,
		"text":       text,
		"parse_mode": "HTML",
	})
	httpReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL("editMessageText"), bytes.NewReader(body))
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func timeoutLabel(ctx context.Context) string {
	deadline, ok := ctx.Deadline()
	if !ok {
		return "times out in unknown"
	}
	d := time.Until(deadline).Round(time.Minute)
	if d <= 0 {
		d = time.Minute
	}
	mins := int(d.Minutes())
	if mins == 1 {
		return "times out in 1 minute"
	}
	return fmt.Sprintf("times out in %d minutes", mins)
}

func truncate(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max]) + "…"
}

func formatArgs(args map[string]any) string {
	if len(args) == 0 {
		return "(no args)"
	}
	truncated := make(map[string]any, len(args))
	for k, v := range args {
		truncated[k] = truncateValue(v)
	}
	b, err := json.MarshalIndent(truncated, "", "  ")
	if err != nil {
		return "(error formatting args)"
	}
	return string(b)
}

// truncateValue marshals a single argument value and truncates it if too long.
// String values are truncated directly; other types are marshaled to JSON first.
func truncateValue(v any) any {
	s, ok := v.(string)
	if !ok {
		b, err := json.Marshal(v)
		if err != nil {
			return v
		}
		s = string(b)
	}
	if utf8.RuneCountInString(s) <= maxValueLen {
		return v
	}
	runes := []rune(s)
	return string(runes[:maxValueLen]) + "… (truncated)"
}

func resolvedText(approved bool, denialReason string, err error, ctx context.Context, tool, argsStr string) string {
	detail := fmt.Sprintf("\n\n🔧 <code>%s</code>\n<pre>%s</pre>", tool, argsStr)
	switch {
	case err != nil && ctx.Err() == context.DeadlineExceeded:
		return "⏱️ Timed out" + detail
	case err != nil && ctx.Err() == context.Canceled:
		return "↩️ Resolved elsewhere" + detail
	case err != nil:
		return "❌ Error" + detail
	case approved:
		return "✅ Approved" + detail
	case denialReason == "user":
		return "❌ Denied" + detail
	default:
		return "↩️ Resolved elsewhere" + detail
	}
}
