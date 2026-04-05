# Mobile Approval via Telegram

**Date:** 2026-04-05  
**Status:** Design approved, ready for implementation

## Overview

Extend the MCP broker's approval system to support approval via Telegram on a smartphone. This is an opt-in addition — it does not replace the existing dashboard-based approval. Any configured approver (dashboard or Telegram) can resolve a pending request; first response wins.

---

## Architecture

### Multi-Approver Fan-Out

The broker currently holds a single `Approver`. This is replaced by a `MultiApprover` that wraps a slice of approvers, fans requests out concurrently, and returns on the first response.

```
Broker.Handle()
    └── MultiApprover.Review()
            ├── Dashboard.Review()         ← existing, always active
            └── TelegramApprover.Review()  ← new, opt-in via config
```

- Each approver runs in its own goroutine
- A shared `context.WithTimeout` (10 min default) wraps all goroutines
- First to return wins; all others are cancelled via context
- On timeout: auto-deny with `DenialReason: "timeout"`

`TelegramApprover` lives in `internal/telegram/`. It is only wired into `MultiApprover` if `telegram.enabled` is `true` in config.

---

## Components

### `internal/telegram/` (new)

Implements the `Approver` interface using the Telegram Bot API (polling, no webhook — keeps the broker fully outbound-only).

**`Review(ctx, tool, args)` flow:**
1. Format message: tool name, truncated args (≤200 chars of JSON, with `(truncated)` note), time remaining
2. Send message to configured `chat_id` with inline keyboard: ✅ Approve / ❌ Deny
3. Long-poll `/getUpdates` for a callback query response
4. On button tap: answer the callback (clears spinner), edit message to show outcome
5. On context cancellation (another approver resolved first): edit message to "↩️ Resolved elsewhere"
6. On timeout: edit message to "⏱️ Timed out"

**Example notification:**
```
🔧 github_gh_push

{"branch": "main", "repo": "agent-tools", "fo...} (truncated)

⏳ 9:42 remaining
```

### `internal/broker/multi.go` (new)

```go
type MultiApprover struct {
    approvers []Approver
    timeout   time.Duration
}

func (m *MultiApprover) Review(ctx context.Context, tool string, args map[string]any) (bool, string, error)
```

Returns `(approved bool, denialReason string, err error)`. Broker signature updated accordingly.

---

## Data Model Changes

### `audit.Record` — add `DenialReason`

```go
type Record struct {
    Timestamp    time.Time
    Tool         string
    Args         map[string]any
    Verdict      string
    Approved     *bool
    DenialReason string  // "": not applicable; "user": explicit deny; "timeout": timed out
    Error        string
}
```

### SQLite migration (run on startup)

```sql
ALTER TABLE audit ADD COLUMN denial_reason TEXT NOT NULL DEFAULT '';
```

Additive — existing rows default to `""`, no data loss.

### `dashboard.decidedRequest` — add `DenialReason string`

Populated when a request is resolved, so the UI can display the right badge.

---

## Dashboard Updates

### Countdown timer on pending requests

- `deadline` timestamp added to the `"new"` SSE event payload
- Browser computes remaining time client-side (no server-side ticking)
- Each pending request card displays a live countdown (e.g. `9:42`)

### Denial reason badges on decided requests

History list shows outcome badges:
- ✅ Approved
- ❌ Denied (user)
- ⏱️ Timed out

No new HTTP endpoints needed — `deadline` piggybacks on existing pending payload, `denial_reason` on existing decided payload.

---

## Configuration

### New fields in `config.json`

```json
{
  "approval_timeout_seconds": 600,
  "telegram": {
    "enabled": false,
    "token": "$TELEGRAM_BOT_TOKEN",
    "chat_id": "$TELEGRAM_CHAT_ID"
  }
}
```

- `approval_timeout_seconds`: global timeout across all approvers, defaults to `600`
- `telegram.enabled`: defaults to `false` — Telegram is inactive unless explicitly enabled
- `token` and `chat_id`: support `$ENV_VAR` interpolation (same pattern as server env config) to avoid storing secrets in the file

### `config.TelegramConfig` (new)

```go
type TelegramConfig struct {
    Enabled bool   `json:"enabled"`
    Token   string `json:"token"`
    ChatID  string `json:"chat_id"`
}
```

---

## Denial Reason Enum

Values used in `DenialReason`:

| Value | Meaning |
|-------|---------|
| `""` | Not applicable (approved, or verdict was allow/deny without human) |
| `"user"` | Human explicitly clicked Deny (dashboard or Telegram) |
| `"timeout"` | No response within `approval_timeout_seconds` |

---

## Implementation Notes

- Telegram polling uses `/getUpdates` with `timeout=55` (long-polling), filtering for `callback_query` updates scoped to the message sent for this request
- The broker's `Approver` interface signature changes from `(bool, error)` to `(bool, string, error)` to carry denial reason — both `Dashboard` and `TelegramApprover` updated accordingly
- `MultiApprover` is always used (even when only dashboard is active) for uniformity — it degrades gracefully to a single approver
