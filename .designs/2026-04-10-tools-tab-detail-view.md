# Tools Tab Detail View

**Date:** 2026-04-10
**Scope:** `mcp-broker` dashboard — tools tab
**Status:** Design approved, ready for implementation planning

## Problem

The dashboard's Tools tab currently shows tools grouped by provider prefix, rendering only the tool name and one-line description (`internal/dashboard/index.html:1013-1056`). There is no way to inspect what arguments a tool takes, which is the most common thing an operator wants to know when auditing what a backend exposes.

The `server.Tool` struct (`internal/server/manager.go:13-17`) already captures the MCP input schema as a `map[string]any`, and `/api/tools` already serializes it — the data is on the wire, just not rendered.

## Goals

- Let the user click a tool row to see its description and input parameters in more detail.
- Render parameter info as readable API-style docs, not raw JSON Schema.
- Ship as a frontend-only change with minimum risk and scope.

## Non-goals (deferred)

- Output schema and MCP annotations (`readOnlyHint`, `destructiveHint`, etc.) — would require plumbing new fields through `stdio.go` and `http.go` backends. Worth revisiting once the base detail view lands.
- Tool search / filtering.
- Copy-to-clipboard on schemas.
- Showing per-tool audit stats (call counts, last-used) next to each tool.

## Design

### 1. Interaction model

Tools tab keeps its current structure: provider groups (collapsible), tools listed inside. The change is at the tool-row level.

- Each `.tool-item` becomes a real `<button type="button">` so it is focusable and Enter/Space activate it. `aria-expanded` tracks state.
- A chevron (▸ collapsed, ▾ expanded) is added as a prefix to telegraph interactivity. This reuses the same visual vocabulary as the provider headers.
- Name + description remain visible in the collapsed state, unchanged — scanning still works without expanding anything.
- Clicking a row expands an inline detail panel directly below it, pushing later rows down. No modal, no overlay.
- **Only one tool can be expanded at a time within the whole tab.** Clicking a second tool collapses the first. This mirrors the existing `toggleAuditDetail` pattern (`index.html:1072+`).
- Switching tabs resets `expandedToolName = null` at the top of `loadTools()` so re-opening the tab starts clean.

### 2. Detail panel content

The expanded `<div class="tool-detail">` is inserted as a sibling after the clicked row, styled as a nested panel (slightly darker background, left-border accent to visually group it with its parent).

Content is built entirely from `tool.inputSchema` on the existing `/api/tools` payload — no new network calls.

**Parameter table.** `renderInputSchema(schema)` walks `schema.properties` and emits a compact table:

| name      | type                                | req                       | description                            |
| --------- | ----------------------------------- | ------------------------- | -------------------------------------- |
| mono font | string / integer / array\<x\> / ... | ✓ if in `schema.required` | from `.description`, em-dash if absent |

Rows are sorted: required first, then alphabetical.

**Type cell logic** (`schemaTypeLabel`):

- Scalar `type` → show it verbatim (`string`, `integer`, `boolean`, ...).
- `type: "array"` with a scalar `items.type` → show `array<items.type>`.
- `enum` present → append `(one of: a, b, c)` to the description column.
- Missing `type` → `any`.
- Anything else unparseable (`oneOf`, `anyOf`, nested objects, `$ref`) → show `object` in the type cell.

**Raw schema escape hatch.** Below the table, render a `<details>` disclosure labeled "view raw schema" containing a `<pre>` JSON dump of the full schema. This preserves full fidelity for complex schemas without cluttering the simple cases.

**Empty case.** If `inputSchema` is null/missing/has no `properties`, render:

```
<div class="tool-schema-empty">This tool takes no arguments.</div>
```

instead of the table. Every tool remains clickable — no two-tier visual state for the user to learn.

### 3. File changes

Everything lives in `internal/dashboard/index.html`. No Go changes, no new files, no API changes.

**CSS** (near `/* Tools tab */` around line 345):

- `.tool-item` gains `cursor: pointer`, chevron styles, and `<button>` resets.
- `.tool-item.expanded` rotates the chevron.
- `.tool-detail` — inline panel: `var(--bg-base)` background, 2px left border in `var(--text-secondary)`, padding `0.75rem 1rem 0.75rem 2.5rem` to align under the tool row.
- `.tool-schema-table` — compact table, mono font, small padding, border tokens shared with `.audit-table`.
- `.tool-schema-empty` — muted italic text.
- `.tool-schema-raw` — styled `<details>`/`<summary>` for the raw-schema disclosure.

**JS** (in the `// --- Tools ---` block around line 1013):

- `loadTools()` renders each tool as a `<button class="tool-item" onclick="toggleTool(this, '<escaped-name>')">`, with chevron + name + description. The schema is stashed on the element as a `data-schema` attribute (`esc(JSON.stringify(schema))`) to avoid a separate lookup map.
- Add module-scoped `var expandedToolName = null;`.
- Add `toggleTool(btn, name)`:
  1. Collapse any previously expanded tool: remove its `.tool-detail` sibling, clear `.expanded`.
  2. If `name !== expandedToolName`, parse `btn.dataset.schema`, build the detail HTML via `renderInputSchema(schema)`, insert after the button, set `expandedToolName = name`.
  3. Otherwise clear `expandedToolName = null`.
- Add `renderInputSchema(schema)`: pure function, returns HTML string. Handles the empty case, builds the sorted table, appends the raw-schema disclosure.
- Add `schemaTypeLabel(prop)` helper for the type-cell logic.
- At the top of `loadTools()`, reset `expandedToolName = null` so tab re-entry starts clean.

### 4. Edge cases

- `inputSchema` is `null`, `{}`, or missing `properties` → empty-state panel.
- `properties` present but `required` missing → treat every field as optional.
- Property with no `type` → cell shows `any`.
- Property with no `description` → em-dash.
- All untrusted strings (tool names, descriptions, schema fields) continue to flow through the existing `esc()` helper. Raw-schema `<pre>` uses `esc(JSON.stringify(...))`.
- `data-schema` attribute is set via `esc(JSON.stringify(schema))` so embedded quotes can't break the HTML.
- Long descriptions wrap normally in the table cell; no truncation.
- `oneOf` / `anyOf` / `$ref` → parser doesn't expand them; row shows `object` and the raw-schema disclosure remains available for full fidelity.

## Risk

Very low. Pure additive frontend change in a single embedded HTML file:

- No Go code changes → no new tests needed; existing `dashboard_test.go` coverage of the API handler is unaffected.
- No API contract changes.
- No new dependencies.
- No persistence, no state beyond a single in-page variable.
- Worst case is a visual glitch — easy to iterate on.

## Alternatives considered

- **Modal popup** — more room for content but adds focus-trap, scroll-lock, and escape-to-close complexity for no real benefit here.
- **Side drawer** — nice for browsing many tools in sequence but introduces a new layout primitive the codebase doesn't otherwise use.
- **Two-pane layout** — always-on detail view, but reshapes the whole tab and eats horizontal space on narrower windows.
- **Pretty JSON block instead of a parameter table** — simpler to implement but pushes JSON Schema reading onto every user. The table + raw-schema disclosure gets both audiences.
- **Plumbing output schema + annotations now** — rejected as scope creep; annotations (especially `destructiveHint`) are arguably the most valuable follow-up and can be added without disturbing this design.
