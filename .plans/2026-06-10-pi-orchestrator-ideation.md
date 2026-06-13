# Pi Orchestrator — Ideation (2026-06-10)

A workflow layer above pi-dispatcher (`pd`) that accepts workflow run requests and stitches
one or more workflow steps together, each backed by a `pd` task run: reusable workflow
definitions, typed inputs, run creation, step orchestration, artifacts, and outcome routing.
External systems such as launchd/cron, GitHub pollers, MCP tools, or humans decide _when_ to
call `po run`.
Working name: `po` (pi-orchestrator). This is an ideation document, not an implementation
plan — it surveys prior art, proposes a shape, and lists open questions.

## Positioning and boundaries

Today's stack has clean layers; `po` adds one more without disturbing them:

```
po   — runs WORKFLOWS (run API, inputs, step graph, artifacts, caps, outcome routing)
pd   — runs TASKS (one agent task run: worktree, sandbox, supervisor, SQLite state, steer/stop/wait)
wt   — worktree lifecycle          sb — sandbox lifecycle
```

Hard boundary: `po` never touches Pi RPC, task supervisors, or worktree-manager directly — it
imports a stable `pd/pkg/...` execution API the way `pd` imports the worktree-manager and
sandbox-manager packages. The `pd` CLI remains a standalone user-facing wrapper over the
same primitives. `pd` should expose completion observability (`Wait`, `Watch`, terminal-state
queries, result metadata) so `po` can reconcile steps without scraping CLI status or adding
`po`-specific callbacks. `po` owns: workflow definitions, input validation, run admission,
step graph and artifact handoff, claims, concurrency/budget caps, respawn/recovery policy,
and routing outcomes to the human.

Vocabulary boundary: `pd` owns **tasks** and **task runs**. `po` owns **workflows**,
**workflow runs**, **steps**, and **step runs**. A `po` step run is executed by creating or
adopting a `pd` task run; avoid calling orchestrator work a "task" unless referring to the
underlying dispatcher object.

Equally important — what `po` is **not**:

- Not a merge bot. Agents may open draft PRs via broker-backed GitHub tooling; only humans merge.
  Every system surveyed keeps this line.
- Not a free-form multi-agent collaboration framework (no inter-agent messaging, role
  hierarchies, or autonomous delegation trees a la Gas Town). One workflow run = a bounded,
  deterministic graph of explicit steps connected by stored artifacts; each executable step is
  backed by a `pd` task run.
- Not a scheduler or webhook SaaS. Local-first; external launchd/cron jobs, pollers, MCP
  tools, or humans call `po run`; `po` validates, admits, and runs the workflow.

## Prior art — what others built, and what to steal

| System                                                                  | Shape                                                                                                                                                                                                                                                                      | Steal                                                                                                                                                                                        |
| ----------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Claude Code Routines** (code.claude.com/docs/en/routines)             | Saved run template + N mixed triggers (cron, per-routine API token, GitHub event w/ field filters); branch-prefix containment; daily/hourly caps; infra-status decoupled from task success                                                                                 | The trigger/filter ergonomics and run containment. Also: "green = exited cleanly, not task succeeded" distinction.                                                                            |
| **Codex Automations** (developers.openai.com/codex/app/automations)     | Thread automations (re-wake existing session) vs standalone (fresh run); Triage inbox that **auto-archives no-finding runs**                                                                                                                                               | The fresh-run vs resume dichotomy as a workflow/step field; auto-archive-empty as the anti-notification-fatigue default.                                                                      |
| **Copilot cloud agent automations** (June 2026)                         | Per-repo `{name, prompt, trigger, tool allowlist, model}`; emoji ack on issue assignment                                                                                                                                                                                   | Cheap "claimed" signal on the triggering artifact.                                                                                                                                           |
| **claude-code-action** (anthropics/claude-code-action)                  | GH Actions-based; `trigger_phrase`/`label_trigger`/`assignee_trigger`; `allowed_bots` empty by default (loop prevention); `branch_prefix`; sticky updated comment                                                                                                          | Actor filtering + branch-prefix exclusion to prevent self-triggering; per-trigger budget/tool policy (`--max-turns`) living in the trigger definition.                                       |
| **OpenHands resolver**                                                  | `fix-me` label = whole issue, `@mention` = just this comment; `MAX_ITER` cap; posts a comment on failure; PR review requested from the triggering human                                                                                                                    | Trigger granularity semantics; **route output back to whoever triggered it**; explicit failure comment, never silence.                                                                       |
| **Linear Agent Interaction SDK**                                        | Issues _assigned_ to humans, _delegated_ to agents; 10s ack-or-unresponsive; session states incl. `awaitingInput`, `stale`                                                                                                                                                 | Accountability stays human, but do **not** copy its interactive state vocabulary: `po` workflows are autonomous and mirror `pd`'s `unknown` for missing supervisors.                          |
| **Terragon** (OSS snapshot: terragon-labs/terragon-oss; shut down 2026) | Full reference impl: tasks from web/CLI/@-mention/MCP; Automations = recurring or event-triggered; container + unique branch per task                                                                                                                                      | Founder's lessons: at ~30 tasks/day the bottleneck is 100% human review; **abandon-and-respawn with amended prompt beats iterative steering** — make respawn a first-class verb.             |
| **Baton** (mraza007/baton)                                              | Daemonless-ish poller→dispatcher→reconciler over a `WORKFLOW.md` (YAML frontmatter + Jinja2 prompt from issue fields, `{{ attempt }}` exposed); claim state machine; outcome-dependent retry (PR created = release; error = exp backoff; issue closed = release + cleanup) | The **runtime architecture** — closest existing thing to what `po` should be in Go. Polling via `gh` instead of webhooks fits local-first.                                                   |
| **Gas Town / Beads** (Yegge)                                            | 20-30 agents, role hierarchy, git-backed SQL-queryable work units                                                                                                                                                                                                          | Only two ideas at our scale: durable queryable work queue (we have SQLite) and a dedicated merge/integration serialization point.                                                            |
| **Vibe Kanban** (now community OSS)                                     | Kanban columns as run states; agents can enqueue cards via MCP                                                                                                                                                                                                             | Agents enqueueing work for agents (a `po run` MCP tool / broker backend) is a cheap, powerful hook. Commercial lesson: pure-orchestration UIs died; local-first OSS survived.                |
| **Temporal/Inngest/Hatchet**                                            | Durable execution platforms                                                                                                                                                                                                                                                | Overkill (server + workers) for single-user, but their vocabulary is the checklist to reimplement over SQLite: per-key concurrency, debounce, priority, idempotency keys, persist-each-step. |
| **Heartbeat pattern** (community)                                       | Cron agent reads state file from last run, acts, writes back                                                                                                                                                                                                               | Optional per-workflow persistent state ("since-last-run" watermark) carried between runs.                                                                                                    |
| **Claude Code local scheduled tasks** (/loop, CronCreate)               | Deterministic jitter from task ID; **7-day auto-expiry**; no catch-up for missed fires                                                                                                                                                                                     | Good semantics for optional external schedule wrappers/docs, without putting scheduling inside the core workflow engine.                                                                      |

Composite recommendation: **Routines' request ergonomics + Baton's durable reconciler runtime
+ Codex's triage-with-auto-archive output + a workflow-run/step-run state model in SQLite.**

## Proposed shape

### Workflow object model

Workflows as files (versionable, reviewable) in `~/.config/po/workflows/<name>.yaml`,
mirrored into SQLite on load for queryability. A single-step workflow is the simplest case,
not a different primitive. Strawman:

```yaml
name: pr-review
description: Review a pull request and file findings
repo: "{{ .Inputs.repo }}" # or fixed path / git URL; base_branch can also be input-derived
branch_prefix: pi/ # containment: pd pushes only under this prefix
inputs:
  repo:
    type: string
    required: true
  pr_number:
    type: integer
    required: true
  head_sha:
    type: string
    required: false
agents:
  reviewer:
    model: ... # passthrough to pd execution options
    skills: [review]
  implementer:
    model: ...
    skills: [test-driven-development]
steps:
  - id: review
    agent: reviewer
    prompt: |
      Review PR #{{ .Inputs.pr_number }} in {{ .Inputs.repo }}.
      Write findings to {{ artifact_path "findings" }}.
    artifacts:
      - name: findings
        path: findings.md
        required: true
```

Concrete workflow runs are created by `po run`, which supplies validated inputs:

```sh
po run pr-review \
  --input repo=averycrespi/agent-tools \
  --input pr_number=123 \
  --input head_sha=abc123
```

Inputs are typed, validated, and safe to use in workflow control flow. Keep v1 inputs flat and
simple (`string`, `integer`, `boolean`; `required`, `default`, and `enum` are enough). External
adapters normalize events into these inputs before calling `po run`; core `po` should not
understand GitHub event schemas. Raw payload storage (`--payload-file`) and bulk input loading
(`--input-file`) are convenience features to add when adapter needs justify them, not v1
requirements. If raw payloads are stored later, they are audit/debug data and are not
automatically interpolated into prompts; event bodies, comments, and issue descriptions are
untrusted content. The safer default is to pass identifiers (`repo`, `issue_number`,
`pr_number`) and let the agent fetch needed context through gated tools.

Multi-step workflows add explicit dependencies and pass stored artifacts between steps:

```yaml
steps:
  - id: localize
    prompt: "Investigate issue {{ .Inputs.issue_number }} and write a plan artifact."
  - id: implement
    needs: [localize]
    prompt: "Implement {{ artifact \"localize.plan\" }}."
  - id: verify
    needs: [implement]
    prompt: "Review the implement step diff against the original issue."
```

Named `agents` let one workflow use different `pd` execution configurations per step while
keeping each step explicit about which agent shape it needs. The agent block is mostly
passthrough to `pd` (model, skills, limits, tool policy as `pd` grows that surface).

### Run requests, not built-in scheduling

`po` should not be a scheduler or event poller in v1. External systems decide when a workflow
should start and call the same run API:

1. **Schedules** — launchd/systemd-timer/cron invokes `po run <workflow> ...`. Jitter, TTL,
   and missed-run semantics can live in those wrappers or docs rather than the core workflow
   engine.
2. **Events** — GitHub/CI/issue pollers, broker-backed MCP tools, or local scripts normalize
   events into typed workflow inputs and call `po run --input ...`. Polling remains preferred
   over inbound webhooks for local-first use.
3. **Manual/API** — humans and agents call `po run <workflow> [--input ...]`; the same can be
   exposed as an MCP tool through mcp-broker so agents can enqueue workflows, gated by broker
   rules.

`po run` validates inputs, applies caps/backpressure, creates a durable workflow run, and
starts/adopts a workflow supervisor for that run. In v1, successful CLI output can simply be
the workflow run ID; validation and cap failures can be ordinary non-zero CLI errors with
clear messages. Structured run responses and idempotency/dedup keys can wait until trigger
adapter needs justify them.

### Runtime: per-workflow supervisor, not a global daemon

No global daemon and no user-facing periodic advance loop. Each accepted workflow run has a
workflow supervisor process, analogous to `pd`'s supervisor around one Pi task run. The
workflow supervisor owns one workflow run: start ready step runs by creating `pd` task runs,
wait for or watch task-run terminal state, collect artifacts, evaluate dependencies and
terminal outcomes, route final outcomes, and exit when the workflow reaches a terminal state.
`pd` supervisors continue to own individual Pi task-run lifecycles.

A crashed `po` supervisor loses no workflow state because every transition is persisted in
SQLite. V1 does not need a user-facing recovery command; inspection and wait commands should
reconcile missing supervisors to `unknown` the way `pd` does. A later recovery/adoption path
can restart supervisors after process death or reboot if the need is proven.

### Worktree ownership

V1 should use one worktree per workflow run. All step runs in a workflow run execute through
backing `pd` task runs that share the workflow worktree. This keeps code handoff simple: the
workflow run maps to one branch/diff/PR, and there is no cross-worktree merge, per-step
cleanup ambiguity, or branch fan-out to reconcile.

`po` owns the workflow-level decision that steps share one worktree and when final
cleanup/routing should happen. `pd` still owns the mechanics of creating, using, and cleaning
up worktrees through `wt`, but `pd/pkg` must support running a task against an existing
workflow-owned worktree with per-task cleanup disabled. Per-step worktrees can be added later
if a real workflow needs isolation or parallel write branches, but they are deliberately out
of scope for v1.

### Step completion and failure propagation

V1 should keep step semantics strict: every executable step is an agent-backed `pd` task run,
and each required step must completely succeed. A step run succeeds only when all configured
completion gates pass:

1. the backing `pd` task run reaches `succeeded`;
2. all required declared artifacts for the step exist.

If any gate fails, the step run is `failed`. A clean `pd` task exit is necessary but not
sufficient, because required artifacts are the step-to-step contract. Workflow success
requires every required step on the executed path to succeed; if a required step fails,
dependent steps are marked `skipped` and the workflow run fails.

Defer `success_check` from v1. Machine-checkable commands are valuable, but they add another
execution surface and can be introduced after the basic agent-step/artifact contract works.

No optional steps, semantic auto-retries, or hidden fix loops in v1. Recovery is explicit:
manual `respawn` or a workflow-authored later step once outcome/artifact contracts are solid.
Automatic retry can be added later for clearly classified infra failures, but semantic
failures should not be retried by default.

### State model (own SQLite DB, `~/.local/state/po/po.db`)

- `workflows` (mirrored from files, with content hash → detect edits)
- `run_requests` (accepted run API calls: workflow, typed inputs JSON, source, requested_by;
  dedup keys and raw payload pointers can be added when trigger adapter needs justify them)
- `workflow_runs` (workflow + run request → typed inputs snapshot, workflow worktree/branch,
  attempt counter, claim/lease, aggregate outcome, routed-notification status)
- `step_runs` (workflow_run + step ID → selected agent, backing pd task run ID, dependencies,
  attempt counter, infra status, step outcome)
- `artifacts` (workflow_run + step ID + name → stored text/file/metadata handoff between steps)
- `workflow_state` (optional per-workflow heartbeat/watermark state keyed by rendered `state_key`)

Lifecycle state should align with `pd` wherever the concepts overlap and remain separate
from semantic outcome. Workflow-run states:
`starting → running → succeeded | failed | stopping → stopped | unknown`. Step-run states:
`starting → running → succeeded | failed | stopping → stopped | skipped | unknown`.

No `queued` state in v0: `po run` either rejects the run request or admits it by creating a
workflow run and starting/adopting its supervisor. Deferred admission can be added later as
run-request metadata or an explicit queue if needed, but it should not complicate the initial
workflow-run lifecycle.

`awaiting-input` is intentionally not a `po` state: workflows are autonomous, and temporary
MCP broker approvals or tool waits are part of a running backing `pd` task run. `stale` is
also not a state; mirror `pd` and reconcile a missing supervisor for a non-terminal run to
`unknown`. `empty` is an outcome classification (`state=succeeded`, `outcome=empty`), not a
lifecycle state. Claim/lease ownership is metadata, not a user-visible state.

### Artifacts and step handoff

Artifacts are the durable handoff mechanism between workflow steps; do not rely on implicit
chat history. The authoritative storage lives outside the worktree under `po` state and is
mounted into the sandbox at the same absolute path. Because `sb` preserves mounted host paths
inside the sandbox, `po` can give the agent the same path that the workflow supervisor later
validates on the host:

```text
~/.local/state/po/runs/<workflow-run-id>/steps/<step-id>/artifacts/<name>
```

Prompts give agents artifact paths; the workflow supervisor validates and indexes the same
paths after the backing `pd` task run exits:

```yaml
steps:
  - id: localize
    prompt: |
      Investigate issue #{{ .Inputs.issue_number }}.
      Write the implementation plan to {{ artifact_path "plan" }}.
    artifacts:
      - name: plan
        path: plan.md
        type: markdown
        required: true
  - id: implement
    needs: [localize]
    prompt: |
      Read the plan at {{ artifact_path "localize.plan" }} and implement it.
```

Default handoff should pass artifact references/paths, not inline artifact contents, to avoid
large prompts, preserve exact bytes, and reduce prompt-injection risk from previous model
output. Inlining can be added later as an explicit opt-in.

`po` should also index automatic artifacts for each step run: backing `pd` task run ID,
lifecycle state, error, stdout/stderr log paths, Pi event log path, session file, and optional
step summary. Workflow-level automatic artifacts include the shared worktree path, branch,
diff, and PR metadata. Declared artifacts are workflow-authored files that later steps can
depend on; v0 only needs existence validation for required artifacts. JSON schema validation
and richer artifact types can come later.

`sb` already supports writable configured mounts with host/sandbox path identity. The missing
integration is at the `pd/pkg` boundary: `po` must be able to ensure/request the `po` state
artifact root is mounted before starting a backing `pd` task run. Avoid storing handoff
artifacts inside the worktree except as an emergency fallback: it pollutes git status, couples
artifacts to worktree cleanup, and breaks down when steps use different worktrees.

### Guardrails (the part that makes autonomy safe)

Layered, all cheap over SQLite:

- **Caps**: global max concurrent workflow step runs (default 2-3 — Osmani's sweet spot);
  per-workflow max concurrent workflow runs (default 1); daily run cap per workflow and
  global; optional per-source cap for noisy external pollers with overflow _dropped and
  logged_.
- **Anti-loop**: V1 keeps core `po` simple and relies on external trigger adapters plus branch
  prefix/actor filtering to avoid self-triggering. Future adapter hardening can add
  idempotency keys, debounce, or per-source duplicate suppression. V1 does not automatically
  retry semantic failures; future infra-only retries need explicit attempt caps, minimum gaps,
  and non-retryable failure classification.
- **Outcome-dependent routing** (Baton): PR-created → reviewable success; clean-empty → done;
  infra error → failed/unknown notification; source issue closed before run admission → reject
  or skip admission.
- **Circuit breaker**: if a workflow's last N runs all failed, auto-pause it and notify
  ("nightly-audit paused after 3 consecutive failures").
- **Review backpressure**: cap workflow runs with reviewable outcomes (for example,
  `state=succeeded`, `outcome=pr_created`); when the human's review queue is full, new run
  requests are rejected or deferred at the run-request layer instead of spawning. The unanimous
  lesson (Terragon, Osmani) is that human review is the bottleneck — the orchestrator should
  respect it, not bury it.
- **Budget**: defer richer budget controls. V1 can pass through only the `pd` agent options it already supports; daily token/cost budgets wait until `pd` captures usage.

### Output, logs, and control

V1 should start with `pd`-aligned pull inspection rather than a separate inbox. `po ps`,
`po status`, and `po wait` show workflow-run lifecycle state, semantic outcome, step
summaries, backing `pd` task run IDs, diff/PR links, and artifacts. `po logs <run>` shows
workflow supervisor logs plus pointers to the underlying `pd` task logs for each step; it does
not need to stream or duplicate all `pd` logs in v1.

`po wait <run>` should mirror `pd wait`: block until terminal and exit non-zero unless the
workflow run succeeded. `po stop <run>` should stop the workflow supervisor and the currently
running backing `pd` task run, then finalize the workflow as stopped. `po rm <run>` should
forget `po` metadata/logs for terminal workflow runs; cleanup semantics can mirror `pd` and
stay conservative.

Defer `po inbox`, dashboard tabs, push notifications, auto-archive behavior, and `po recover`
until the basic run/inspect/wait/stop loop is solid. `po respawn <run-or-step> [--amend "..."]`
remains a likely follow-on control verb, but is not required for the smallest v1.

## Strawman CLI

```
po list                                # list workflow definitions
po show <workflow>                     # show a workflow definition
po lint <workflow>                     # validate YAML and input schema
po run <workflow> [--input k=v ...]    # validate inputs, create/adopt workflow-run supervisor
po ps                                  # list workflow runs
po status <run>                        # show workflow run, steps, artifacts, backing pd task IDs
po wait <run>                          # block until terminal; non-zero unless succeeded
po logs <run>                          # workflow supervisor logs + pointers to pd step logs
po stop <run>                          # stop supervisor and current backing pd task run
po rm <run>                            # forget terminal workflow run metadata/logs

CLI surface should align with `pd` wherever concepts overlap to reduce mental load. The design
decision is the runtime shape: `po run` creates/adopts a workflow-run supervisor; there is no
core scheduler, no normal operator-facing `advance` command, no v1 `inbox`, and no v1
user-facing `recover`.
```

## Open questions

1. **Where does pd end and po begin on resume?** Re-waking a previous Pi session (Codex
   thread automations) needs pd support for resuming from a recorded session file. Defer to
   vNext; V1 starts each step as a fresh backing `pd` task run.
2. **Trigger adapters live where?** Core `po` should not own scheduling/polling, but examples
   or companion commands may be useful: launchd plist snippets, `po-github-poll`, or broker
   tools that normalize external events into `po run` inputs.
3. **Is `po` a new tool or a `pd` subsystem?** Separate tool fits the repo's
   one-tool-one-job pattern and keeps pd's scope honest; but they share SQLite idioms,
   auth-token plumbing, and the dashboard. The expected boundary is a stable `pd/pkg/...`
   API for execution primitives, not a merge and not long-term CLI scraping.
4. **Workflow prompt injection surface**: raw payloads (issue bodies!) are untrusted content
   entering an autonomous agent — the lethal-trifecta leg. Mitigations: typed inputs are the
   default prompt/control-flow surface; raw payloads are stored for audit/debug and only
   interpolated when explicitly requested. Prefer referencing an issue/PR by number and letting
   the agent fetch it through gated tools, rather than inlining bodies. Sandbox egress allowlist
   (audit plan 2.5) is the structural backstop. This deserves a DESIGN.md section of its own.
5. **Multi-sandbox**: today sb supports exactly one VM (`sb`). Concurrent workflow step runs
   share it. Fine for v1 (pd already does this); revisit if workflows need different
   provisioning profiles.

## MVP slicing

1. **V1 core**: workflow files with flat typed inputs, named agents, agent-backed steps,
   `po run` creating/adopting a workflow supervisor, one workflow worktree, one or more step
   runs backed by `pd` task runs, required artifact validation, run_requests/workflow_runs/
   step_runs/artifacts tables, and `pd`-aligned inspection/control (`ps/status/wait/logs/stop/rm`).
2. **V1 explicitly excludes**: built-in scheduling/polling, `success_check`, optional steps,
   semantic auto-retries, hidden fix loops, per-step worktrees, raw payload storage,
   `--input-file`, structured run responses, inbox/dashboard, push notifications, recovery
   command, respawn, circuit breaker, review backpressure, and cost budgets.
3. **vNext**: `success_check`; trigger adapters and MCP run tool; bounded DAG conveniences;
   inbox/dashboard views; respawn; circuit breaker; recovery/adoption command; push
   notifications; dedup/idempotency for trigger adapters; per-step worktree policies; resume
   mode; review-backpressure deferred admission; cost budgets after `pd` usage capture.

## Prerequisites

`po` leans on a few platform capabilities that should be in place first: reliable `pd` terminal-state handling, `wt`/`pd` concurrency locks, human approval/notification for risky actions, and ideally a sandbox egress allowlist before any event-triggered autonomy, since event payloads are untrusted input.
