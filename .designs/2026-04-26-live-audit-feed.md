# Live Audit Feed in MCP Broker Dashboard

## Goal

The audit log tab in the mcp-broker dashboard shows a live feed of new records as they are written, while preserving the existing historical browsing experience (filtering, pagination, detail expansion).

## UX

One unified table. Live behavior is **conditional on view state**, never disrupts the user's current context.

### View states

| State                                                         | Behavior on incoming audit record                                                |
| ------------------------------------------------------------- | -------------------------------------------------------------------------------- |
| Audit tab inactive                                            | Ignore. (No tab badge — audit is informational, not actionable.)                 |
| Active, page 1, no filter, not paused                         | Prepend row with quiet highlight; drop bottom row to keep table at `auditLimit`. |
| Active, filter set, record matches filter, page 1, not paused | Same — prepend, drop bottom.                                                     |
| Active, filter set, record does _not_ match filter            | Increment "N new" counter; don't render.                                         |
| Active, paginated (`offset > 0`)                              | Increment counter; don't render.                                                 |
| Active, paused (toggle on)                                    | Increment counter; don't render.                                                 |

`auditTotal` updates live so the pagination "Showing X–Y of N" stays accurate without a refetch.

### Status strip (new UI element)

A dedicated row above the table, separate from the existing filter row.

```
[ Tool filter: [______] [Clear] ]              ← existing filter row
─────────────────────────────────────────
 ● Live                       [⏸ Pause]        ← new status strip (idle state)
─────────────────────────────────────────
 ┌─ existing audit table ─┐
```

States the strip can take:

- **Live, unpaused, page 1, no filter:** small `● Live` pill on the left, `[⏸ Pause]` button on the right. Quiet, blends in.
- **Paused (page 1, no filter):** `⏸ Paused — N new` indicator, `[▶ Resume]` button.
- **Filtered or paginated:** `N new records — return to live view` clickable banner. Clicking clears filter, sets `offset = 0`, calls `loadAudit()`.

The strip's vertical footprint is constant across states so nothing reflows.

### Quiet highlight

New rows get a subtle background tint (e.g. `rgba(80,180,120,0.10)`) that fades to transparent over ~1s via CSS transition. No flashing. Audit records arrive in bursts during agent activity; loud highlights become noise fast.

### Resume / banner click semantics

Resuming or clicking the banner does **not** splice buffered events into the table. It calls `loadAudit()` for a clean refetch. Simpler, no ordering hazards, no missed events from mid-paint. The new-count is reset.

## Backend wiring

### Observer pattern on `audit.Logger`

Add a subscription mechanism to `internal/audit/audit.go`:

```go
type Subscriber func(rec Record)

func (l *Logger) Subscribe(fn Subscriber) (unsubscribe func())
```

- Subscribers stored in a slice on `Logger`, guarded by the existing `mu`.
- After a successful `Record()` insert, release the lock, then iterate subscribers and invoke each. Subscribers run with no lock held — a slow subscriber must not stall the next insert.
- Subscribers are only notified on **successful** inserts. SQL errors short-circuit before notification.
- Unsubscribe is safe to call concurrently with `Record()`.

### Dashboard subscribes at startup

In `cmd/mcp-broker/serve.go`, after constructing the auditor and dashboard:

```go
unsubscribe := auditor.Subscribe(dashboard.OnAuditRecord)
defer unsubscribe()
```

- `Dashboard.OnAuditRecord(rec audit.Record)` marshals an SSE event of type `"audit"` and calls the existing `d.broadcast(...)` fan-out.
- Drop-on-full-buffer carries over: if a client's per-connection SSE channel is backed up, the live event is dropped _for that client only_. Other clients are unaffected. The user's next interaction triggers a `loadAudit()` refresh from the authoritative DB.

### Why this shape

- Audit logger owns "a record was written" — natural place for the hook.
- Broker pipeline (`internal/broker/broker.go`) is unchanged.
- Dashboard reuses its existing `/events` SSE channel — no new endpoint, no second `EventSource` on the client, no per-event-type subscription split.
- Subscription is wired through the concrete `*audit.Logger` (not the `AuditQuerier` interface), so test mocks aren't broken.

## SSE event shape

Reuse the existing `sseEvent{Type, Data}` envelope:

```json
{ "type": "audit", "data": <audit.Record> }
```

`Data` is the same `audit.Record` struct returned by `GET /api/audit` — the client renders it with the same row-rendering helper used by the REST path.

**Server does not filter.** Every connected client receives every audit event. Filtering is purely a client-side viewer concern. The user can change the filter input without reconnecting SSE.

## Frontend changes (`internal/dashboard/index.html`)

### New script-level state

```js
var auditPaused = false;
var auditNewCount = 0;
```

### SSE handler

The existing `es.onmessage` dispatch gains one branch:

```js
if (msg.type === "audit") handleAuditEvent(msg.data);
```

`handleAuditEvent(rec)` walks the state table above. If conditions allow live prepend, it builds a row with the existing renderer, inserts at the top of `tbody`, drops the last row, and applies the highlight class. Otherwise it increments `auditNewCount` and updates the strip.

### Refactor: extract row rendering

The row-building code currently inlined in `loadAudit()` (~lines 2105–2157) is extracted to `renderAuditRow(rec, idx)`. Both the REST path and the live-prepend path use the same renderer to prevent drift.

### Highlight class

```css
.audit-row-new td {
  background-color: rgba(80, 180, 120, 0.1);
  transition: background-color 1s ease-out;
}
```

Class added on insert, removed on next animation frame so the transition fires.

## Edge cases

- **Concurrency:** subscriber slice mutex-guarded; notification happens after lock release; subscribers expected to be non-blocking.
- **Dropped SSE events:** acceptable; user's next interaction (filter, pagination, unpause) triggers `loadAudit()` for a clean state.
- **Tab inactive:** events ignored client-side. No badge. Switching to the audit tab triggers `loadAudit()` (existing behavior).
- **Filter changes mid-stream:** `auditNewCount` resets via the existing `debounceAudit` → `loadAudit()` path.
- **Multiple dashboard clients:** logger has one subscriber (the dashboard); fan-out to N browsers happens inside `dashboard.broadcast`. Linear, not N×M.

## Testing

- **`audit_test.go`** — `Subscribe` fan-out, unsubscribe, no notification on insert error, no panic with empty subscriber list.
- **`dashboard_test.go`** — `OnAuditRecord` produces an `audit`-typed SSE event on the broadcast channel.
- Existing tests unchanged (no interface widening on `AuditQuerier`).
- **E2E (optional):** smoke test exercising broker → audit → dashboard SSE → client receives `audit` event. Not strictly required since unit tests cover the contract.

## Out of scope

- Event replay on SSE reconnect.
- Server-side filtering of the audit stream.
- Tab badge counter.
- Audit retention or eviction tied to the live feed.

These can be revisited if needed; none are blocking for the live-feed feature.
