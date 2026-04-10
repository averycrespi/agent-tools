# Tools Tab Detail View Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Add click-to-expand detail view to the MCP Broker dashboard's Tools tab so operators can see each tool's input parameters as an API-style table.

**Architecture:** Pure frontend change in a single embedded HTML file (`mcp-broker/internal/dashboard/index.html`). Reuses the existing inline-expansion pattern from the Audit tab. No Go changes, no API changes, no new dependencies. The `/api/tools` endpoint already ships `inputSchema` — we just render it.

**Tech Stack:** Vanilla JS / HTML / CSS embedded in `index.html` via `//go:embed`. Existing CSS custom properties (`--bg-base`, `--text-secondary`, etc.) and the `esc()` helper.

**Design doc:** `.designs/2026-04-10-tools-tab-detail-view.md`

**Note on TDD:** The dashboard frontend has no JS test harness (the Go `dashboard_test.go` tests only cover API handlers, which are unchanged here). Spinning up a JS testing framework for a single-file static-HTML change would be scope creep. Tasks are therefore structured as small, independently-reviewable commits with explicit acceptance criteria. The user has stated they will manually verify the final result in a browser after implementation — do not add that step to the plan.

---

### Task 1: Add CSS for clickable tool rows and detail panel

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html` (CSS block, insert after the existing `.tool-desc` rule at line 435, before `/* Audit tab */` at line 437)

**Step 1: Add the new CSS rules**

Insert the following CSS immediately after the existing `.tool-desc { ... }` rule (line 432-435). The existing `.tool-item` and `.tool-name` / `.tool-desc` rules stay as they are; the additions below extend and override only what's needed for the button + expanded state.

```css
/* Tool item becomes a button */
.tool-item {
  background: none;
  border: none;
  border-bottom: 1px solid var(--border);
  color: inherit;
  width: 100%;
  text-align: left;
  font: inherit;
  cursor: pointer;
}

.tool-item:focus-visible {
  outline: 2px solid var(--text-secondary);
  outline-offset: -2px;
}

.tool-item-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.tool-item .chevron {
  font-size: 0.625rem;
  color: var(--text-secondary);
  transform: rotate(-90deg);
  display: inline-block;
  width: 0.75rem;
  flex-shrink: 0;
}

.tool-item.expanded .chevron {
  transform: rotate(0deg);
}

.tool-item-text {
  display: flex;
  flex-direction: column;
  gap: 0.125rem;
  min-width: 0;
}

/* Detail panel */
.tool-detail {
  padding: 0.75rem 1rem 0.875rem 2.5rem;
  background: var(--bg-base);
  border-bottom: 1px solid var(--border);
  border-left: 2px solid var(--text-secondary);
}

.tool-detail-label {
  font-family: var(--font-sans);
  font-weight: 600;
  font-size: 0.6875rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-secondary);
  margin-bottom: 0.375rem;
}

.tool-schema-empty {
  font-family: var(--font-mono);
  font-size: 0.8125rem;
  color: var(--text-secondary);
  font-style: italic;
}

.tool-schema-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.8125rem;
}

.tool-schema-table th {
  text-align: left;
  padding: 0.375rem 0.625rem;
  font-family: var(--font-sans);
  font-weight: 600;
  font-size: 0.6875rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-secondary);
  border-bottom: 1px solid var(--border);
}

.tool-schema-table td {
  padding: 0.375rem 0.625rem;
  border-bottom: 1px solid var(--border);
  font-family: var(--font-mono);
  color: var(--text-primary);
  vertical-align: top;
}

.tool-schema-table tr:last-child td {
  border-bottom: none;
}

.tool-schema-table .col-name {
  color: var(--text-primary);
  white-space: nowrap;
}

.tool-schema-table .col-type {
  color: var(--text-secondary);
  white-space: nowrap;
}

.tool-schema-table .col-req {
  color: var(--green);
  text-align: center;
  width: 1.5rem;
}

.tool-schema-table .col-desc {
  color: var(--text-secondary);
  font-family: var(--font-sans);
  word-break: break-word;
}

.tool-schema-raw {
  margin-top: 0.75rem;
}

.tool-schema-raw summary {
  font-family: var(--font-sans);
  font-size: 0.6875rem;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-secondary);
  cursor: pointer;
  user-select: none;
}

.tool-schema-raw summary:hover {
  color: var(--text-primary);
}

.tool-schema-raw pre {
  margin-top: 0.5rem;
  background: var(--bg-panel);
  border: 1px solid var(--border);
  border-radius: 6px;
  padding: 0.5rem 0.625rem;
  font-family: var(--font-mono);
  font-size: 0.75rem;
  color: var(--text-secondary);
  overflow-x: auto;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}
```

**Why each rule exists** (so the engineer doesn't trim "unused" rules):

- `.tool-item` button reset — neutralizes the default `<button>` browser styling so the row still looks like the old `<div>`.
- `.tool-item:focus-visible` — keyboard users need a visible focus ring; omitted here it would be invisible on dark background.
- `.tool-item-row` + `.tool-item-text` — layout containers so the chevron sits beside the name/description stack.
- `.chevron` starts rotated -90° (pointing right) and rotates to 0° (down) when `.expanded` is present. No transition — matches the existing provider-header chevron feel (`.provider-header .chevron { transition: none; }` line 383).
- `.tool-detail` uses `var(--bg-base)` to sit visually deeper than the row, with a left border for the parent-child cue.
- `.tool-schema-table` is styled independently from `.audit-table` because they live in different tabs with different density needs.
- `.col-req` color is `var(--green)` for the ✓ mark; other cells are muted.
- `.tool-schema-raw` uses a native `<details>` element — no JS needed for the disclosure behavior.

**Step 2: Build the binary to confirm the file still embeds cleanly**

Run from the repo root:

```bash
cd mcp-broker && make build
```

Expected: clean build, no errors. The CSS additions are invisible until Task 3 wires up the HTML structure, but the file should still parse and embed.

**Step 3: Commit**

```bash
git add mcp-broker/internal/dashboard/index.html
git commit -m "feat(dashboard): add CSS for tool detail panel"
```

---

### Task 2: Add `renderInputSchema` and `schemaTypeLabel` helpers

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html` (JS block, insert immediately before the `// --- Tools ---` comment at line 1013)

**Step 1: Add the two helper functions**

These are pure functions. They take a schema object and return an HTML string. They are not wired up yet — Task 3 will call them from `toggleTool`.

Insert the following block immediately before the `// --- Tools ---` line (currently around line 1013):

```javascript
// --- Tool schema rendering ---

// schemaTypeLabel returns a human-readable type for a JSON Schema property.
// Handles scalars, typed arrays, and falls back to 'object' or 'any' for
// unparseable shapes (oneOf, anyOf, nested objects, $ref, etc.).
function schemaTypeLabel(prop) {
  if (!prop || typeof prop !== "object") return "any";
  if (typeof prop.type === "string") {
    if (
      prop.type === "array" &&
      prop.items &&
      typeof prop.items.type === "string"
    ) {
      return "array<" + prop.items.type + ">";
    }
    return prop.type;
  }
  if (Array.isArray(prop.type)) return prop.type.join("|");
  if (prop.oneOf || prop.anyOf || prop.allOf || prop.$ref) return "object";
  return "any";
}

// renderInputSchema builds the HTML for the detail panel body.
// Returns a string to be inserted into a .tool-detail div.
function renderInputSchema(schema) {
  var hasProps =
    schema &&
    typeof schema === "object" &&
    schema.properties &&
    typeof schema.properties === "object" &&
    Object.keys(schema.properties).length > 0;

  if (!hasProps) {
    return (
      '<div class="tool-detail-label">Inputs</div>' +
      '<div class="tool-schema-empty">This tool takes no arguments.</div>'
    );
  }

  var required = {};
  if (Array.isArray(schema.required)) {
    schema.required.forEach(function (k) {
      required[k] = true;
    });
  }

  // Sort: required first, then alphabetical.
  var keys = Object.keys(schema.properties).sort(function (a, b) {
    if (required[a] !== required[b]) return required[a] ? -1 : 1;
    return a < b ? -1 : a > b ? 1 : 0;
  });

  var rows = "";
  keys.forEach(function (key) {
    var prop = schema.properties[key] || {};
    var desc = prop.description ? String(prop.description) : "";
    if (Array.isArray(prop.enum)) {
      var enumStr = prop.enum
        .map(function (v) {
          return JSON.stringify(v);
        })
        .join(", ");
      desc = desc ? desc + " (one of: " + enumStr + ")" : "one of: " + enumStr;
    }
    rows +=
      "<tr>" +
      '<td class="col-name">' +
      esc(key) +
      "</td>" +
      '<td class="col-type">' +
      esc(schemaTypeLabel(prop)) +
      "</td>" +
      '<td class="col-req">' +
      (required[key] ? "\u2713" : "") +
      "</td>" +
      '<td class="col-desc">' +
      (desc ? esc(desc) : "\u2014") +
      "</td>" +
      "</tr>";
  });

  var html =
    '<div class="tool-detail-label">Inputs</div>' +
    '<table class="tool-schema-table">' +
    "<thead><tr>" +
    "<th>name</th><th>type</th><th>req</th><th>description</th>" +
    "</tr></thead>" +
    "<tbody>" +
    rows +
    "</tbody>" +
    "</table>" +
    '<details class="tool-schema-raw">' +
    "<summary>view raw schema</summary>" +
    "<pre>" +
    esc(JSON.stringify(schema, null, 2)) +
    "</pre>" +
    "</details>";

  return html;
}
```

**Why each piece matters:**

- `schemaTypeLabel` is split out so the type logic can evolve independently of the row renderer. Array-of-union (`items.type` being an array) falls through to `'object'` — acceptable fidelity loss, raw schema covers it.
- `required` is converted from the JSON Schema array form into a lookup object once — avoids `indexOf` inside the sort comparator.
- The sort comparator returns `-1` for required-before-optional, then alphabetical. Stable in modern JS engines.
- Enum values go through `JSON.stringify` so strings stay quoted (`"foo"` not `foo`), which matches how users read MCP schemas.
- All user-controlled strings (`key`, `prop.type`, `prop.description`, full schema dump) flow through the existing `esc()` helper (line 748) to prevent HTML injection from a malicious backend tool name or description.
- The empty state uses the same `.tool-detail-label` as the populated state for visual consistency.
- `<details>` is used instead of a custom toggle — zero JS, works with keyboard out of the box.

**Step 2: Build the binary to confirm the file still embeds cleanly**

```bash
cd mcp-broker && make build
```

Expected: clean build. These helpers are defined but unused until Task 3, which is fine — no warning or error.

**Step 3: Commit**

```bash
git add mcp-broker/internal/dashboard/index.html
git commit -m "feat(dashboard): add schema render helpers for tool detail"
```

---

### Task 3: Wire up clickable tool rows

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html` — the `loadTools()` function (currently lines 1014-1056) and `toggleProvider` helper area

**Step 1: Replace the `loadTools()` function body**

The current implementation renders each tool as a passive `<div class="tool-item">`. Replace it with a `<button>` that stashes the schema in `data-schema` and calls `toggleTool` on click. Also reset `expandedToolName` on tab re-entry so the state doesn't persist across tab switches.

Find the existing block (currently line 1013 through 1062):

```javascript
// --- Tools ---
function loadTools() {
  var list = document.getElementById("tools-list");
  var emptyEl = document.getElementById("tools-empty");

  fetch("api/tools")
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      var tools = data.tools || [];
      if (tools.length === 0) {
        list.innerHTML = "";
        emptyEl.style.display = "block";
        return;
      }
      emptyEl.style.display = "none";

      // Group by provider prefix
      var groups = {};
      tools.forEach(function (tool) {
        var parts = tool.name.split(".");
        var provider = parts.length > 1 ? parts[0] : "other";
        if (!groups[provider]) groups[provider] = [];
        groups[provider].push(tool);
      });

      var html = "";
      var providers = Object.keys(groups).sort();
      providers.forEach(function (provider) {
        var items = groups[provider];
        html += '<div class="provider-group">';
        html +=
          '<button class="provider-header collapsed" onclick="toggleProvider(this)">';
        html +=
          '<div class="provider-header-left"><span class="chevron">&#9660;</span> ' +
          esc(provider) +
          "</div>";
        html += '<span class="provider-count">' + items.length + "</span>";
        html += "</button>";
        html += '<div class="provider-capabilities hidden">';
        items.forEach(function (tool) {
          html += '<div class="tool-item">';
          html += '<div class="tool-name">' + esc(tool.name) + "</div>";
          if (tool.description)
            html +=
              '<div class="tool-desc">' + esc(tool.description) + "</div>";
          html += "</div>";
        });
        html += "</div></div>";
      });
      list.innerHTML = html;
    });
}

function toggleProvider(btn) {
  btn.classList.toggle("collapsed");
  var caps = btn.nextElementSibling;
  caps.classList.toggle("hidden");
}
```

Replace with:

```javascript
// --- Tools ---
var expandedToolName = null;

function loadTools() {
  var list = document.getElementById("tools-list");
  var emptyEl = document.getElementById("tools-empty");

  // Reset expansion state on tab (re-)entry so we start clean.
  expandedToolName = null;

  fetch("api/tools")
    .then(function (r) {
      return r.json();
    })
    .then(function (data) {
      var tools = data.tools || [];
      if (tools.length === 0) {
        list.innerHTML = "";
        emptyEl.style.display = "block";
        return;
      }
      emptyEl.style.display = "none";

      // Group by provider prefix
      var groups = {};
      tools.forEach(function (tool) {
        var parts = tool.name.split(".");
        var provider = parts.length > 1 ? parts[0] : "other";
        if (!groups[provider]) groups[provider] = [];
        groups[provider].push(tool);
      });

      var html = "";
      var providers = Object.keys(groups).sort();
      providers.forEach(function (provider) {
        var items = groups[provider];
        html += '<div class="provider-group">';
        html +=
          '<button class="provider-header collapsed" onclick="toggleProvider(this)">';
        html +=
          '<div class="provider-header-left"><span class="chevron">&#9660;</span> ' +
          esc(provider) +
          "</div>";
        html += '<span class="provider-count">' + items.length + "</span>";
        html += "</button>";
        html += '<div class="provider-capabilities hidden">';
        items.forEach(function (tool) {
          var schemaAttr = esc(JSON.stringify(tool.inputSchema || {}));
          html +=
            '<button type="button" class="tool-item" aria-expanded="false"' +
            ' data-tool-name="' +
            esc(tool.name) +
            '"' +
            ' data-schema="' +
            schemaAttr +
            '"' +
            ' onclick="toggleTool(this)">';
          html += '<div class="tool-item-row">';
          html += '<span class="chevron">&#9660;</span>';
          html += '<div class="tool-item-text">';
          html += '<div class="tool-name">' + esc(tool.name) + "</div>";
          if (tool.description)
            html +=
              '<div class="tool-desc">' + esc(tool.description) + "</div>";
          html += "</div>";
          html += "</div>";
          html += "</button>";
        });
        html += "</div></div>";
      });
      list.innerHTML = html;
    });
}

function toggleProvider(btn) {
  btn.classList.toggle("collapsed");
  var caps = btn.nextElementSibling;
  caps.classList.toggle("hidden");
}

function toggleTool(btn) {
  var name = btn.getAttribute("data-tool-name");

  // Collapse any currently-expanded tool.
  if (expandedToolName) {
    var prev = document.querySelector(".tool-item.expanded");
    if (prev) {
      prev.classList.remove("expanded");
      prev.setAttribute("aria-expanded", "false");
      var prevDetail = prev.nextElementSibling;
      if (prevDetail && prevDetail.classList.contains("tool-detail")) {
        prevDetail.remove();
      }
    }
  }

  // Second click on the same tool: just collapse.
  if (expandedToolName === name) {
    expandedToolName = null;
    return;
  }

  // Expand the clicked tool.
  var schema;
  try {
    schema = JSON.parse(btn.getAttribute("data-schema"));
  } catch (e) {
    schema = null;
  }

  var detail = document.createElement("div");
  detail.className = "tool-detail";
  detail.innerHTML = renderInputSchema(schema);

  btn.classList.add("expanded");
  btn.setAttribute("aria-expanded", "true");
  btn.parentNode.insertBefore(detail, btn.nextSibling);
  expandedToolName = name;
}
```

**Why the structure changed:**

- `<div class="tool-item">` → `<button type="button" class="tool-item">`. `type="button"` is critical — without it, buttons inside a form default to `type="submit"`. There's no form here today, but defense in depth is free.
- `aria-expanded` makes screen readers announce the state change. Updated in both directions by `toggleTool`.
- `data-tool-name` is stored alongside `data-schema` so `toggleTool` can compare against `expandedToolName` without re-parsing the schema on every click.
- `esc(JSON.stringify(...))` on `data-schema` is the key XSS defense: `JSON.stringify` produces a JSON string (safe in JS), then `esc` escapes `"`, `<`, `>`, `&` so the HTML attribute can't be broken out of.
- Chevron uses `&#9660;` (▼) just like provider-header does (line 1042) — the CSS rotates it via `transform`, not a character swap.
- The old `.tool-item` layout was a column of name + description; the new `.tool-item-row` lays chevron and text side-by-side, with the name/description still stacked inside `.tool-item-text`.
- `expandedToolName` is a module-scoped `var` (matches the style of `auditLimit`, `expandedAuditIdx`, etc. elsewhere in the same script).
- On second click (same tool), we collapse and return without rebuilding — simpler and cheaper.
- `JSON.parse` is wrapped in try/catch because `data-schema` goes through DOM round-trip and we don't want a single malformed schema to break the whole tab. On parse failure we pass `null` to `renderInputSchema`, which produces the empty-state panel.

**Step 2: Build the binary**

```bash
cd mcp-broker && make build
```

Expected: clean build.

**Step 3: Run the full test suite and lint to catch regressions**

```bash
cd mcp-broker && make audit
```

Expected: pass. No Go code changed, so this is a sanity check that nothing unrelated broke. If `make audit` fails on a pre-existing issue, escalate — do not paper over it.

**Step 4: Commit**

```bash
git add mcp-broker/internal/dashboard/index.html
git commit -m "feat(dashboard): expand tool rows to show input schema"
```

---

### Task 4: Update DESIGN.md Tools tab description

**Files:**

- Modify: `mcp-broker/DESIGN.md:125`

**Step 1: Update the Tools tab bullet**

Current (line 125):

```
- **Tools tab** — discovered tools grouped by server
```

Replace with:

```
- **Tools tab** — discovered tools grouped by server; click a tool to see its input schema
```

No other documentation mentions the Tools tab's behavior. `mcp-broker/CLAUDE.md`, `mcp-broker/ARCHITECTURE.md`, and `mcp-broker/README.md` either describe the dashboard at a higher level or don't mention it at all — they do not go stale with this change.

**Step 2: Commit**

```bash
git add mcp-broker/DESIGN.md
git commit -m "docs: note click-to-expand on dashboard Tools tab"
```

---

## Acceptance criteria (for reviewer, not to be added as a task)

A reviewer reading the final diff should be able to confirm, without running the binary:

1. Only `mcp-broker/internal/dashboard/index.html` and `mcp-broker/DESIGN.md` are touched.
2. `make audit` still passes in `mcp-broker/`.
3. Every tool name, description, schema key, enum value, type string, and raw schema JSON flows through `esc()` before reaching `innerHTML`.
4. `data-schema` is set via `esc(JSON.stringify(...))` — not raw interpolation.
5. `expandedToolName` is reset inside `loadTools()` at the top of the fetch callback, so stale state doesn't leak across tab switches.
6. `toggleTool` handles a missing/malformed `data-schema` gracefully (try/catch → empty state).
7. The button uses `type="button"` and keeps `aria-expanded` in sync with the visual state.
8. No new dependencies. No Go changes. No `/api/tools` contract changes.
