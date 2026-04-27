# Live Audit Feed Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use Skill(executing-plans) to implement this plan task-by-task.

**Goal:** Make the mcp-broker dashboard's audit tab show new audit records live as they're written, while preserving filter/pagination/detail-expansion as a stable historical view.

**Architecture:** `audit.Logger` gains a `Subscribe` mechanism that fans out successful inserts to non-blocking subscribers. The dashboard subscribes at startup and re-broadcasts a new `audit` event type over its existing `/events` SSE channel. The browser handles each event based on view state — prepending live on page 1 with no filter, otherwise incrementing a counter and showing a "return to live view" banner. A pause toggle lets the user freeze the live feed without filtering or paginating.

**Tech Stack:** Go 1.22+, `ncruces/go-sqlite3` (already in use), embedded HTML/JS dashboard (no framework, vanilla DOM + `EventSource`).

Design doc: [`.designs/2026-04-26-live-audit-feed.md`](../.designs/2026-04-26-live-audit-feed.md).

---

### Task 1: Audit logger subscribe mechanism

**Files:**

- Modify: `mcp-broker/internal/audit/audit.go`
- Test: `mcp-broker/internal/audit/audit_test.go`

**Acceptance Criteria:**

- `(*Logger).Subscribe(fn Subscriber) (unsubscribe func())` is exported, where `type Subscriber func(rec Record)`. Subscribers are invoked once per successful `Record()` insert, with the lock released, in registration order. Failed inserts notify nobody.
- `unsubscribe()` removes the subscriber and is safe to call concurrently with `Record()` and other `Subscribe`/`unsubscribe` calls. Calling it twice is a no-op.
- New tests cover: single subscriber receives the record; multiple subscribers each receive it; unsubscribed subscriber does not receive subsequent records; no notification on insert error (e.g., DB closed); empty subscriber list does not panic.

**Notes:**

- Add `subscribers []Subscriber` field on `Logger`, guarded by the existing `mu`.
- In `Record()`: perform the SQL insert under the lock as today; if successful, snapshot the subscriber slice while still holding the lock, release, then iterate the snapshot and invoke each subscriber. The snapshot avoids races where unsubscribe runs while the iteration is in flight.
- Do not call subscribers under the lock — a misbehaving subscriber must not stall the next insert.
- The `Subscriber` contract is "return quickly; hand off via channel for any real work." Document this with a one-line comment on the type.
- For the unsubscribe identity, use the address of a heap-allocated wrapper (e.g., `*subscriberEntry`) rather than function-pointer comparison (Go does not allow `==` on `func` values). Store `[]*subscriberEntry` internally.
- Do not modify the `Record` struct or the `QueryOpts` struct. This task is purely additive.

**Commit:** `feat(mcp-broker): add subscribe mechanism to audit logger`

---

### Task 2: Dashboard subscribes and broadcasts audit SSE events

**Files:**

- Modify: `mcp-broker/internal/dashboard/dashboard.go`
- Modify: `mcp-broker/cmd/mcp-broker/serve.go`
- Test: `mcp-broker/internal/dashboard/dashboard_test.go`

**Acceptance Criteria:**

- `Dashboard.OnAuditRecord(rec audit.Record)` exists and emits a JSON-marshalled `sseEvent{Type: "audit", Data: rec}` via the existing `d.broadcast(...)` fan-out. Marshalling matches the same shape `/api/audit` returns for a single record.
- `serve.go` calls `auditor.Subscribe(dashboard.OnAuditRecord)` after constructing the dashboard (when both auditor and dashboard are non-nil) and the returned `unsubscribe` is invoked on shutdown.
- New test in `dashboard_test.go` registers a subscriber-style listener (a fake SSE client channel via `handleEvents` or by directly inspecting `d.clients`) and asserts that calling `OnAuditRecord(rec)` produces an SSE frame whose JSON body has `"type":"audit"` and a `"data"` field whose `"tool"` and `"verdict"` fields match `rec`.

**Notes:**

- Add a small `auditEvent(rec audit.Record) []byte` helper alongside the existing `newRequestEvent`, `removedEvent`, `decidedEvent` helpers in `dashboard.go`.
- `OnAuditRecord` should be safe to call from any goroutine; `d.broadcast` already holds `d.mu` while iterating clients.
- Subscribe wiring lives in `serve.go`, not in `dashboard.New(...)`, because the dashboard package today does not import the concrete `*audit.Logger` (only the `AuditQuerier` interface). Keep that boundary: `OnAuditRecord` takes an `audit.Record` parameter, so `serve.go` can adapt the subscriber signature directly.
- Do not widen the `AuditQuerier` interface. The wire is `*audit.Logger.Subscribe(...)` → `Dashboard.OnAuditRecord` directly.
- The existing tests for `handleEvents` and `broadcast` should still pass unchanged.

**Commit:** `feat(mcp-broker): broadcast audit records over dashboard SSE`

---

### Task 3: Extract `renderAuditRow` helper in dashboard JS

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html` (audit tab `<script>` block, around lines 2071–2171)

**Acceptance Criteria:**

- A new function `renderAuditRow(rec, idx)` returns the HTML string for a single audit table row (the `<tr>...</tr>` currently built inline inside `loadAudit()`'s `records.forEach` callback). All existing classes (`row-error` / `row-denied` / `row-approved`, `verdict-allow` / `verdict-deny` / `verdict-require-approval`) and the `data-audit-idx` / `onclick="toggleAuditDetail(idx)"` attributes are preserved exactly.
- `loadAudit()` calls `renderAuditRow(rec, idx)` inside its `forEach` and concatenates the results — visible behavior of the audit tab is unchanged.
- `make test` and `make test-e2e` pass without modification (this task is a pure refactor).

**Notes:**

- This is a behavior-preserving refactor that creates a clean seam for Task 4 to call. Do it as its own commit so the diff is purely a code move.
- Manually verify in a browser: load the dashboard, switch to the audit tab, confirm rows render with the same colors, expand one row to confirm the detail toggle still works, exercise pagination and filter to confirm row classes still apply.
- Keep the function declaration alongside the other audit helpers (`debounceAudit`, `loadAudit`, `auditPrev`, `auditNext`).

**Commit:** `refactor(mcp-broker): extract renderAuditRow helper`

---

### Task 4: Live audit feed UI — status strip, prepend, pause, banner

**Files:**

- Modify: `mcp-broker/internal/dashboard/index.html`

**Acceptance Criteria:**

- A status strip is rendered between the existing `.audit-filters` row and `.audit-table-wrap`, with a constant vertical footprint across all states. Three visual states:
  - **Live:** `● Live` indicator + `[⏸ Pause]` button. Active when audit tab is shown, `auditOffset === 0`, the filter input is empty, and `auditPaused === false`.
  - **Paused:** `⏸ Paused — N new` indicator + `[▶ Resume]` button. Active when `auditPaused === true`. `N` reflects `auditNewCount` and updates live as events arrive.
  - **Out of live view:** clickable banner `N new records — return to live view`. Active when filtered or paginated. Clicking it clears the filter input, sets `auditOffset = 0`, calls `loadAudit()`, and resets `auditNewCount` to 0.
- New SSE handler branch: `if (msg.type === "audit") handleAuditEvent(msg.data);` is added to the existing `es.onmessage` dispatch. `handleAuditEvent` walks the state table from the design doc — prepending a row via `renderAuditRow` (with the bottom row dropped to keep the table at `auditLimit`) and applying the `audit-row-new` highlight class only when in the live-prepend state, otherwise incrementing `auditNewCount` and updating the strip. `auditTotal` increments on every received event so the pagination "Showing X–Y of N" line stays accurate.
- A `.audit-row-new` CSS rule applies a quiet background tint that fades to transparent over ~1s (e.g. `background-color: rgba(80, 180, 120, 0.10); transition: background-color 1s ease-out;`). The class is added on insert and removed on the next animation frame so the transition fires.
- The pause toggle button flips `auditPaused` and re-renders the strip. Resuming calls `loadAudit()` (clean refetch — no buffered-event splicing) and resets `auditNewCount`.
- When the user is on a tab other than `audit`, incoming `audit` events are ignored entirely (no badge, no buffered counter).

**Notes:**

- Add the two new state vars near the other audit state: `var auditPaused = false;` and `var auditNewCount = 0;`.
- Filter changes already trigger `debounceAudit` → `loadAudit()`; reset `auditNewCount` inside `loadAudit()` (alongside the existing `expandedAuditIdx = -1` reset) so any state transition that refetches also clears the counter.
- A single `updateAuditStrip()` function should compute the current state from `(activeTab, auditOffset, filterValue, auditPaused, auditNewCount)` and rewrite the strip's innerHTML. Call it on every state change.
- The matching check for "does this incoming record match the current filter" should use the same case-insensitive substring logic the server uses (`tool LIKE '%' || ? || '%'`). In JS: `rec.tool.toLowerCase().includes(filterValue.toLowerCase())`.
- Manual verification: run `make build && ./mcp-broker serve --config <test-config>`, exercise a few tool calls from a connected client, watch rows arrive on the audit tab; toggle pause, confirm new rows accumulate as a count and don't render; unpause and confirm refetch; set a filter, confirm the banner appears for non-matching events; click the banner, confirm reset.
- Keep all CSS additions inside the existing `<style>` block in `index.html`. Match the indentation and style-conventions of the surrounding code (camelCase JS, two-space indent, `var` declarations — yes, the file uses `var` throughout).

**Commit:** `feat(mcp-broker): live audit feed in dashboard`

---

### Task 5: Update DESIGN.md and README.md to describe live audit feed

**Files:**

- Modify: `mcp-broker/DESIGN.md` (audit tab description ~line 177; SSE description ~line 179)
- Modify: `mcp-broker/README.md` (file-map line ~316 mentions "audit viewer" — confirm whether to expand)

**Acceptance Criteria:**

- `DESIGN.md`'s "Dashboard" section describes the audit tab as having both a paginated historical view and a live feed of incoming records, gated by view state (page 1 + no filter + not paused) with a pause toggle and a "return to live view" banner. The SSE description mentions that audit events join the existing `new`/`removed`/`decided` event types over the same `/events` channel.
- `README.md` is reviewed; if no user-facing behavior change warrants a README update beyond the existing "audit viewer" mention, leave it. Otherwise, expand to mention the live feed.
- `mcp-broker/CLAUDE.md` is reviewed; no changes expected (no convention-level additions).

**Notes:**

- DESIGN.md is the spec — keep edits load-bearing. Don't restate what the design doc already covers; capture only the intent ("the audit tab shows live records under stable view conditions") and the SSE event-type addition.
- Avoid adding emoji; this codebase doesn't use them in docs.

**Commit:** `docs(mcp-broker): describe live audit feed in design`

---

<!-- Notes for the executor:
- Tasks 1 and 2 are independent of frontend changes; they can land before any UI work.
- Task 3 must land before Task 4 (Task 4 calls renderAuditRow).
- Run `make audit` (tidy + fmt + lint + test + govulncheck) before each Go-touching commit.
- For Task 4, prefer a manual browser smoke test over inventing E2E tests — the existing E2E coverage doesn't exercise the dashboard JS, and adding browser-test infrastructure is out of scope.
-->
