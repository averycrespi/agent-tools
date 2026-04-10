# MCP Broker Dashboard: Rules Tab

**Date:** 2026-04-10
**Status:** Design
**Scope:** `mcp-broker/internal/{rules,dashboard}`, `mcp-broker/cmd/mcp-broker/serve.go`

## Purpose

Add a **Rules** tab to the dashboard, positioned between _Tools_ and _Audit Log_,
that shows the currently configured rules and which discovered tools each rule
matches. The tab is read-only and exists to answer the debugging question:
"given this tool name, what verdict does it get and why?"

## Non-goals

- Editing, adding, reordering, or deleting rules from the UI
- Live matcher input (type-a-tool-name field)
- Dead-rule detection / warning UI
- Invalid-glob error surfacing
- Hot-reload of the config file
- SSE push updates for rules changes

## Architecture

### New interface

Mirrors the existing `ToolLister` / `AuditQuerier` pattern in
`internal/dashboard/dashboard.go`:

```go
// RulesLister provides the static list of configured rules.
type RulesLister interface {
    Rules() []config.RuleConfig
}
```

### Engine exposure

`*rules.Engine` already holds the rule slice internally. Add two methods:

```go
// Rules returns the configured rules (in order).
func (e *Engine) Rules() []config.RuleConfig

// EvaluateWithRule returns the verdict and the index of the matching rule.
// Returns (defaultVerdict, -1) when no rule matches.
func (e *Engine) EvaluateWithRule(tool string) (Verdict, int)
```

`EvaluateWithRule` becomes the single source of truth for matching. The
existing `Evaluate` method should be rewritten to call it and discard the
index, so we never have two glob-matching loops that can drift.

### Dashboard wiring

`dashboard.New` signature gains a fourth parameter:

```go
func New(tools ToolLister, rules RulesLister, auditor AuditQuerier, logger *slog.Logger) *Dashboard
```

In `cmd/mcp-broker/serve.go` (around line 109), the existing `*rules.Engine`
instance is passed as the new argument.

## API

**`GET /api/rules`** — returns a pre-computed view:

```json
{
  "rules": [
    {
      "index": 0,
      "tool": "github.*",
      "verdict": "allow",
      "matches": ["github.list_prs", "github.view_pr", "github.create_pr"]
    },
    {
      "index": 1,
      "tool": "*.delete",
      "verdict": "deny",
      "matches": []
    },
    {
      "index": 2,
      "tool": "*",
      "verdict": "require-approval",
      "matches": ["git.push", "fs.write"]
    }
  ],
  "unmatched": [],
  "default_verdict": "require-approval"
}
```

The handler:

1. Calls `rulesLister.Rules()` for the rule slice
2. Calls `toolLister.Tools()` for the current discovered tools
3. For each tool, calls `engine.EvaluateWithRule(tool.Name)` to get the rule index
4. Groups tools by rule index; tools that returned `-1` land in `unmatched`

Matching is computed server-side so browser JavaScript never re-implements
`filepath.Match` semantics.

With the default config (catchall `*`), `unmatched` will always be empty.
The field exists for configurations that remove the catchall.

## UI

### Tab button placement

In `internal/dashboard/index.html` around line 679:

```html
<button class="tab" onclick="switchTab('tools')" id="tab-tools">Tools</button>
<button class="tab" onclick="switchTab('rules')" id="tab-rules">Rules</button>
<button class="tab" onclick="switchTab('audit')" id="tab-audit">
  Audit Log
</button>
```

### Tab content

A vertical stack of rule cards:

```
┌─ Rules (evaluated first-match-wins) ──────────────────────┐
│                                                            │
│  #1  github.*                            [ allow ]   3     │
│      • github.list_prs                                     │
│      • github.view_pr                                      │
│      • github.create_pr                                    │
│                                                            │
│  #2  *.delete                            [ deny ]    0     │
│                                                            │
│  #3  *                        [ require-approval ]   2     │
│      • git.push                                            │
│      • fs.write                                            │
│                                                            │
└────────────────────────────────────────────────────────────┘
```

**Elements per card:**

- Index badge (`#1`, `#2`, ...) — makes first-match-wins ordering explicit
- Rule glob in monospace
- Verdict pill on the right — reuses existing verdict color semantics from
  the Audit Log tab (green/red/yellow)
- Match count chip, styled like `.provider-count` from the Tools tab
  (`index.html:390`)
- Flat list of matching tool names below the header (no collapsing —
  per-rule match lists are typically short, and hiding them would defeat the
  purpose of the tab)

Rules with zero matches simply render with an empty list below the header.
No special styling, warning, or "dead rule" annotation.

### Unmatched section

If `data.unmatched.length > 0`, render a final section below the rule cards:

```
┌─ Unmatched tools ──────────────────────────────────────────┐
│  These tools fall through to the default verdict:          │
│  require-approval                                          │
│                                                            │
│  • some.tool                                               │
│  • another.tool                                            │
└────────────────────────────────────────────────────────────┘
```

With the default catchall `*` in config, this section is never rendered.

### Loading

Follows the existing pattern in `switchTab` (`index.html:743`):

```js
if (name === "rules") loadRules();
```

Single `fetch('api/rules')` on tab switch. No SSE, no polling, no caching —
clicking the tab again re-fetches. This matches the Tools tab and is
acceptable because rules are static at startup and tool discovery settles
shortly after broker boot.

## Testing

### `internal/rules/rules_test.go`

- `TestEngine_Rules` — round-trip: constructing with a slice, calling `Rules()`
  returns the same slice
- `TestEngine_EvaluateWithRule` — table-driven, parallel to existing
  `TestEngine_Evaluate` but asserts both verdict and rule index, including
  the `-1` fall-through case

### `internal/dashboard/dashboard_test.go`

- `TestHandleRules` — table-driven cases:
  - Rules with matches
  - Rules with empty match list
  - No rules configured (empty slice)
  - Tools that fall through to `unmatched` (config without a catchall)
- Uses a fake `RulesLister` alongside the existing fake `ToolLister`

### No e2e test

The existing dashboard e2e suite does not exercise UI rendering; the JS in
`loadRules` is straightforward fetch-and-render and the server-side handler
is unit-tested. This matches the precedent set by the Tools tab.

## Implementation order

Each step is independently reviewable and can be committed separately:

1. **`internal/rules/rules.go`** — add `Rules()` and `EvaluateWithRule()`,
   rewrite `Evaluate()` to delegate. Update `rules_test.go`.
2. **`internal/dashboard/dashboard.go`** — add `RulesLister` interface,
   add the fourth constructor parameter, implement `handleRules`, register
   `GET /api/rules`. Update `dashboard_test.go`.
3. **`cmd/mcp-broker/serve.go`** — pass `engine` to `dashboard.New` at the
   existing call site (~line 109).
4. **`internal/dashboard/index.html`** — add the tab button, `#content-rules`
   div, CSS (reusing existing tokens), `loadRules()` JS, and `switchTab`
   dispatch.
5. **`make audit`** — tidy/fmt/lint/test/govulncheck pass before committing.
