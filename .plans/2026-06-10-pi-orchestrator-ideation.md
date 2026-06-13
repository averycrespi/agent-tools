# Pi Orchestrator — Ideation (2026-06-10)

A layer above pi-dispatcher (`pd`) that decides _when_ and _with what_ to spawn agent runs:
reusable task templates, cron-like schedules, and external-event triggers (GitHub events, CI
failures, manual fire). Working name: `po` (pi-orchestrator). This is an ideation document,
not an implementation plan — it surveys prior art, proposes a shape, and lists open
questions.

## Positioning and boundaries

Today's stack has clean layers; `po` adds one more without disturbing them:

```
po   — decides WHEN to run and WITH WHAT (templates, triggers, dedup, caps, outcome routing)
pd   — runs ONE agent task (worktree, sandbox, supervisor, SQLite state, steer/stop/wait)
wt   — worktree lifecycle          sb — sandbox lifecycle
```

Hard boundary: `po` never touches Pi RPC, supervisors, or worktrees directly — it shells
into / imports `pd` the way `pd` imports the worktree-manager and sandbox-manager packages.
`pd` stays useful standalone. `po` owns: template definitions, trigger evaluation, dedup and
claims, concurrency/budget caps, retry/respawn policy, and routing outcomes to the human.

Equally important — what `po` is **not**:

- Not a merge bot. Agents may open draft PRs via broker-backed GitHub tooling; only humans merge.
  Every system surveyed keeps this line.
- Not a multi-agent collaboration framework (no inter-agent messaging, no role hierarchies
  a la Gas Town). One template fire = one independent `pd` task.
- Not a webhook SaaS. Local-first; favor polling over inbound webhooks (see Triggers).

## Prior art — what others built, and what to steal

| System                                                                  | Shape                                                                                                                                                                                                                                                                      | Steal                                                                                                                                                                                        |
| ----------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Claude Code Routines** (code.claude.com/docs/en/routines)             | Saved run template + N mixed triggers (cron, per-routine API token, GitHub event w/ field filters); branch-prefix containment; daily/hourly caps; infra-status decoupled from task success                                                                                 | The **object model**. Best-designed template+trigger schema in the wild. Also: "green = exited cleanly, not task succeeded" distinction.                                                     |
| **Codex Automations** (developers.openai.com/codex/app/automations)     | Thread automations (re-wake existing session) vs standalone (fresh run); Triage inbox that **auto-archives no-finding runs**                                                                                                                                               | The fresh-run vs resume dichotomy as a template field; auto-archive-empty as the anti-notification-fatigue default.                                                                          |
| **Copilot cloud agent automations** (June 2026)                         | Per-repo `{name, prompt, trigger, tool allowlist, model}`; emoji ack on issue assignment                                                                                                                                                                                   | Cheap "claimed" signal on the triggering artifact.                                                                                                                                           |
| **claude-code-action** (anthropics/claude-code-action)                  | GH Actions-based; `trigger_phrase`/`label_trigger`/`assignee_trigger`; `allowed_bots` empty by default (loop prevention); `branch_prefix`; sticky updated comment                                                                                                          | Actor filtering + branch-prefix exclusion to prevent self-triggering; per-trigger budget/tool policy (`--max-turns`) living in the trigger definition.                                       |
| **OpenHands resolver**                                                  | `fix-me` label = whole issue, `@mention` = just this comment; `MAX_ITER` cap; posts a comment on failure; PR review requested from the triggering human                                                                                                                    | Trigger granularity semantics; **route output back to whoever triggered it**; explicit failure comment, never silence.                                                                       |
| **Linear Agent Interaction SDK**                                        | Issues _assigned_ to humans, _delegated_ to agents; 10s ack-or-unresponsive; session states incl. `awaitingInput`, `stale`                                                                                                                                                 | The state vocabulary — `awaitingInput` and `stale` are states `pd`/`po` should model. Accountability stays human.                                                                            |
| **Terragon** (OSS snapshot: terragon-labs/terragon-oss; shut down 2026) | Full reference impl: tasks from web/CLI/@-mention/MCP; Automations = recurring or event-triggered; container + unique branch per task                                                                                                                                      | Founder's lessons: at ~30 tasks/day the bottleneck is 100% human review; **abandon-and-respawn with amended prompt beats iterative steering** — make respawn a first-class verb.             |
| **Baton** (mraza007/baton)                                              | Daemonless-ish poller→dispatcher→reconciler over a `WORKFLOW.md` (YAML frontmatter + Jinja2 prompt from issue fields, `{{ attempt }}` exposed); claim state machine; outcome-dependent retry (PR created = release; error = exp backoff; issue closed = release + cleanup) | The **runtime architecture** — closest existing thing to what `po` should be in Go. Polling via `gh` instead of webhooks fits local-first.                                                   |
| **Gas Town / Beads** (Yegge)                                            | 20-30 agents, role hierarchy, git-backed SQL-queryable work units                                                                                                                                                                                                          | Only two ideas at our scale: durable queryable work queue (we have SQLite) and a dedicated merge/integration serialization point.                                                            |
| **Vibe Kanban** (now community OSS)                                     | Kanban columns as run states; agents can enqueue cards via MCP                                                                                                                                                                                                             | Agents enqueueing work for agents (a `po fire` MCP tool / broker backend) is a cheap, powerful hook. Commercial lesson: pure-orchestration UIs died; local-first OSS survived.               |
| **Temporal/Inngest/Hatchet**                                            | Durable execution platforms                                                                                                                                                                                                                                                | Overkill (server + workers) for single-user, but their vocabulary is the checklist to reimplement over SQLite: per-key concurrency, debounce, priority, idempotency keys, persist-each-step. |
| **Heartbeat pattern** (community)                                       | Cron agent reads state file from last run, acts, writes back                                                                                                                                                                                                               | Optional per-template persistent state ("since-last-run" watermark) carried between runs.                                                                                                    |
| **Claude Code local scheduled tasks** (/loop, CronCreate)               | Deterministic jitter from task ID; **7-day auto-expiry**; no catch-up for missed fires                                                                                                                                                                                     | All three semantics — TTL on schedules is the best anti-zombie-cron guardrail; fire-once-when-able is right for a laptop that sleeps.                                                        |

Composite recommendation: **Routines' object model + Baton's poller/dispatcher/reconciler
runtime + Codex's triage-with-auto-archive output + a dedup/claim table in SQLite.**

## Proposed shape

### Template object model

Templates as files (versionable, reviewable) in `~/.config/po/templates/<name>.yaml`,
mirrored into SQLite on load for queryability. Strawman:

```yaml
name: nightly-audit
description: Run make audit across agent-tools, file findings
repo: ~/work/agent-tools # or git URL; base_branch: main
branch_prefix: pi/ # containment: pd pushes only under this prefix
prompt: |
  Run `make audit`. Fix trivial findings; summarize anything non-trivial.
  {{ .Event.Text }}                # appended freeform context on event/API fires
mode: fresh # fresh | resume (re-wake last session — vNext)
agent:
  model: ... # passthrough to pd run flags
  max_duration: 45m # hard cap passed through to pd run --max-duration
  max_turns: 50
triggers:
  - schedule: "0 2 * * *" # cron; jitter derived from template name; no catch-up
    ttl: 30d # auto-disable after TTL unless renewed
  - event: github.issue.labeled
    filters: { label: pi-fix, repo: averycrespi/agent-tools }
  - api: true # `po fire nightly-audit --text "..."` / MCP tool
success_check: "make -C {{ .Worktree }} test" # exit 0/1 after agent ends; optional
on_success: { action: draft-pr, notify: quiet } # quiet = inbox only, no push notif
on_failure: { action: keep-worktree, notify: push }
on_empty: archive # agent reports nothing actionable → silent
state_key: nightly-audit # optional heartbeat watermark carried between runs
```

Notable: a machine-checkable `success_check` exceeds every surveyed system (they all punt
to "make the prompt explicit about success"); `pd` already decouples cleanup/exit-code
state, so recording `infra_status` vs `outcome` separately fits the existing schema
philosophy.

### Triggers

1. **Schedules** — cron expressions evaluated by a `po tick` pass. Semantics stolen from
   Claude Code local scheduling: deterministic per-template jitter, no catch-up for missed
   fires (laptop slept → fire once when able), TTL auto-expiry.
2. **Events, by polling not webhooks** — local-first machines shouldn't run inbound HTTP
   from the internet. Poll via existing credentialed paths: GitHub MCP/broker tools for
   issues/PRs/CI runs, `git fetch` for branch changes. Baton proves polling is enough at
   personal scale. Each poller emits normalized events; filters (field + operator:
   equals/contains/regex/one-of — Routines' filter grammar) select them.
3. **Manual/API** — `po fire <template> [--text ...]`, and the same exposed as an MCP tool
   through mcp-broker so _agents can enqueue work for agents_ (Vibe Kanban's best idea) —
   gated by broker rules, naturally.

### Runtime: daemonless tick, like everything else in this repo

No long-lived daemon. `po tick` does one pass: poll sources → match triggers → dedup →
check caps → `pd run` for each fire → reconcile previous dispatches (read `pd` state,
run `success_check`, route outcomes). Run it from launchd/systemd-timer every N minutes
(we already document launchd for mcp-broker/gomod-proxy). `po daemon` (a loop around tick)
can come later if latency matters. This mirrors pd's daemonless stance and the Baton
reconciler pattern, and means a crashed `po` loses nothing — all state is in SQLite.

### State model (own SQLite DB, `~/.local/state/po/po.db`)

- `templates` (mirrored from files, with content hash → detect edits)
- `trigger_state` (per-trigger watermarks: last poll cursor, last fire time, TTL)
- `events` (normalized observed events; `dedup_key UNIQUE` + `expires_at`) — dedup keys are
  semantic (`repo+issue+label`) or delivery IDs; replays blocked; `pull_request.synchronize`
  storms debounced (only newest within window fires, superseding queued)
- `dispatches` (template + event → pd task ID, attempt counter, claim/lease, outcome,
  success_check result, routed-notification status)

The Linear-inspired state vocabulary for dispatches:
`queued → claimed → running → awaiting-input → succeeded | failed | empty | stale`.
`awaiting-input` (agent blocked on a question) and `stale` (supervisor gone) surface in
`po ps` and feed notifications.

### Guardrails (the part that makes autonomy safe)

Layered, all cheap over SQLite:

- **Caps**: global max concurrent dispatches (default 2-3 — Osmani's sweet spot); per-template
  max concurrent (default 1); daily fire cap per template and global; per-trigger hourly cap
  with overflow _dropped and logged_ (Routines' semantics).
- **Anti-loop**: actor filtering (events caused by `pi/`-prefixed branches or the agent's own
  PRs never trigger); n8n-style retry constraints — max 3 attempts per logical item per day,
  minimum gap between attempts, never retry non-retryable failures.
- **Outcome-dependent retry** (Baton): PR-created → release; clean-empty → done;
  infra error → exponential backoff; source issue closed → release + cleanup.
- **Circuit breaker**: if a template's last N dispatches all failed, auto-pause it and
  notify ("nightly-audit paused after 3 consecutive failures").
- **Review backpressure**: cap dispatches in `succeeded-awaiting-review`; when the human's
  review queue is full, new fires queue instead of spawning. The unanimous lesson (Terragon,
  Osmani) is that human review is the bottleneck — the orchestrator should respect it,
  not bury it.
- **Budget**: `max_duration`/`max_turns` per dispatch; optionally a daily token/cost budget once pd captures usage.

### Output routing

- Pull-based **inbox** first (`po inbox`, plus a tab in the pd dashboard or a sibling
  loopback UI): each dispatch lands as succeeded/failed/empty with diff/PR link.
- **Auto-archive empty results** (Codex Triage) — a nightly audit that found nothing
  makes no sound.
- Push notifications only for `failed`, `awaiting-input`, and template auto-pause —
  via the planned ntfy approver/agent-notify path (audit plan 6.4), with action buttons
  (respawn / steer / dismiss).
- **`po respawn <dispatch> [--amend "..."]`** as a first-class verb: clone the dispatch
  with an amended prompt, abandon the old worktree. Terragon's core lesson operationalized.

## Strawman CLI

```
po template list|show|lint <name>     # lint = validate YAML, check repo paths, dry-run filters
po tick [--once]                      # the engine; launchd runs this
po fire <template> [--text ...]       # manual/API trigger
po ps / po inbox / po log <dispatch>  # observe
po respawn <dispatch> [--amend ...]   # abandon-and-respawn
po pause|resume <template>            # manual circuit breaker
```

## Open questions

1. **Where does pd end and po begin on `mode: resume`?** Re-waking a previous Pi session
   (Codex thread automations) needs pd support for resuming from a recorded session file.
   Defer to vNext; `fresh` covers most templates.
2. **Event source plumbing**: poll GitHub directly vs. through broker-backed MCP tools (audited,
   rate-limited, but adds a hop)? Leaning: direct reads when already authenticated, broker for writes.
3. **Is `po` a new tool or a `pd` subsystem?** Separate tool fits the repo's
   one-tool-one-job pattern and keeps pd's scope honest; but they share SQLite idioms,
   auth-token plumbing, and the dashboard. Decide after prototyping `po tick` against the
   pd CLI surface — if the integration needs more than `pd run/ps/status --json`, that's
   pressure toward a shared package, not a merge.
4. **Template prompt injection surface**: event payloads (issue bodies!) interpolated into
   prompts are untrusted content entering an autonomous agent — the lethal-trifecta leg.
   Mitigations: template chooses what fields to interpolate (default: reference the issue
   by number and let the agent fetch it through gated tools, rather than inlining bodies);
   sandbox egress allowlist (audit plan 2.5) as the structural backstop. This deserves a
   DESIGN.md section of its own.
5. **Multi-sandbox**: today sb supports exactly one VM (`sb`). Concurrent dispatches share
   it. Fine for v1 (pd already does this); revisit if templates need different
   provisioning profiles.

## MVP slicing

1. **v0 — cron only**: templates with schedule triggers, `po tick` from launchd,
   `pd run` dispatch, dispatches table, inbox via `po ps`. No events, no success_check.
   (Strictly more useful than `cron + bash` because of dedup, caps, and state.)
2. **v0.5 — outcomes**: success_check, on_success/on_failure routing, respawn verb,
   circuit breaker, ntfy notifications for failures.
3. **v1 — events**: gh polling source (issues by label, failed CI runs on main), filter
   grammar, dedup/debounce, anti-loop actor filtering.
4. **vNext**: `po fire` as an MCP tool via broker; resume mode; review-backpressure
   queueing; dashboard tab; cost budgets (after pd usage capture).

## Prerequisites

`po` leans on a few platform capabilities that should be in place first: reliable `pd` terminal-state handling, `wt`/`pd` concurrency locks, human approval/notification for risky actions, and ideally a sandbox egress allowlist before any event-triggered autonomy, since event payloads are untrusted input.
