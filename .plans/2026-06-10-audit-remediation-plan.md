# Agent-Tools Audit Remediation Plan (2026-06-10)

Source: full-repo audit (two deep code/design reviews of the then-current tool set) cross-referenced against
mid-2026 best practices for AI agent tooling (MCP spec 2025-11-25 + 2026-07-28 RC, OWASP MCP
Top 10 / Agentic Top 10 2026, Anthropic/OpenAI sandboxing patterns, parallel-agent
orchestration ecosystem).

Headline assessment: the stack is architecturally validated — host-side credential brokering
(local-git-mcp / local-gomod-proxy, plus official GitHub MCP behind mcp-broker), loopback-only binding, glob rules +
approval + SQLite audit in mcp-broker are exactly the patterns the industry converged on in
2025-26 (Claude Code web's credential-injecting git proxy, Docker Sandboxes, Infisical
agent-vault). The gaps are: one real bug (GID), a class of missing timeouts, partial-failure
and concurrency handling in the workflow tools, no CI, and spec-readiness work for MCP
2026-07-28.

Each item below notes effort (S < 1h, M = hours, L = day+) and has a verify step — several
findings came from subagent review and should be re-confirmed at implementation time.

---

## Phase 1 — Bugs and correctness (do first)

### 1.1 sandbox-manager: missing GID injection [M] — RESOLVED

- Lima's `user:` template supports UID but not GID, so this is doc drift rather than an
  implementable template fix.
- Updated sandbox-manager docs to state that the VM user preserves the host UID only, and that
  Lima does not support setting the VM user's primary GID in the template.
- If group ownership on writable mounts becomes a practical issue, address it through
  provisioning or Lima mount configuration rather than `user.gid` template rendering.

### 1.2 Subprocess/HTTP timeouts everywhere [M]

A hung `git push`, `go mod download`, or backend MCP server currently hangs the calling handler
indefinitely. One pattern, three tools:

- local-git-mcp: thread MCP request context with a deadline into `runner.Run()` — RESOLVED.
  Added a context-aware runner and configurable `--git-timeout` defaulting to 5 minutes per
  git command.
- local-gomod-proxy: wrap `go mod download` in a per-request timeout (long default, e.g.
  5-10 min — module downloads can legitimately be slow — but finite) — RESOLVED.
  Added a context-aware private command timeout and configurable `--download-timeout`
  defaulting to 10 minutes per private `go` command.
- mcp-broker: pass an `http.Client` with timeout to `NewStreamableHttpClient`
  (`internal/server/http.go:38-59`) — RESOLVED for Streamable HTTP backends via
  `http_timeout_seconds` defaulting to 120 seconds. Still consider a broader per-tool-call
  deadline on the proxy path (must not count human approval wait time for require-approval
  tools).
- Plan: add a shared convention (context deadline at the handler boundary, configurable via
  flag/config with sane default). Add a test per tool that a stuck subprocess/backend returns
  a timeout error instead of hanging (fake runner that blocks).

### 1.3 worktree-manager + pi-dispatcher: concurrency and partial failure [L]

- TOCTOU race in `workspace.go:126-132` (stat-then-`git worktree add`): SKIPPED for now.
  Two concurrent `wt add`/`pd run` calls on the same branch can race, but normal `pd run`
  usage generates unique branches, the failure is non-corrupting, and the edge case does not
  justify adding locking or idempotent recovery yet.
- Partial worktrees: RESOLVED. Missing configured files/scripts remain optional and are
  skipped, but failures while copying an existing configured file or running an existing setup
  script now return errors instead of being logged and ignored. Worktrees are intentionally
  left in place for inspection, and `wt add --reconfigure` explicitly reruns copy/setup for
  existing worktrees.
- pi-dispatcher launch failure path (`cmd/pd/run_impl.go:140-169`): RESOLVED after this plan
  was written. Current `failRunLaunch` transitions launch failures to terminal `failed` and
  runs cleanup for failures before a supervisor PID is returned; failures after a supervisor
  PID is returned intentionally skip cleanup because the supervisor may still own the task.
- Stale control sockets under `/tmp/pd/tasks/`: RESOLVED for reconciliation and `pd rm`.
  `pd rm` already removed sockets, and reconciliation now deletes a dead supervisor's stale
  socket before marking the task `unknown`.
- Verify: add tests for concurrent `wt add` (two goroutines, same branch), launch-failure
  state transition, and socket cleanup on reconcile — socket cleanup on reconcile is covered.

### 1.4 local-gomod-proxy: path containment on streamed files [S] — RESOLVED

- Implemented defense-in-depth validation that `go mod download -json` artifact paths are
  under the configured host `GOMODCACHE` before opening them.

---

## Phase 2 — Security hardening (broker-centric)

Aligned to OWASP MCP Top 10 and the MCP spec security-best-practices page.

### 2.1 mcp-broker: tool-description pinning / rug-pull detection [M]

- Tool poisoning via changed upstream tool descriptions is a named 2025-26 attack class
  (CVE-2025-54136). On startup, hash each backend tool's name+description+schema; persist
  hashes (SQLite or config dir). On change, log loudly and optionally flip the tool to
  `require-approval` until the operator re-pins (`mcp-broker pin <server>`).

### 2.2 mcp-broker: request limits and approval backpressure [M]

- Body-size limit on `/mcp` (e.g. `http.MaxBytesReader`, ~10MB default, configurable) — RESOLVED via `max_request_body_bytes` defaulting to 10 MiB.
- Bound concurrent pending approvals (semaphore); reject excess with a clear tool error —
  SKIPPED for now. The broker is single-user, loopback-bound, and already protected by body
  limits; approval queue caps can be revisited if flooding becomes a real operational issue.
- Optional: simple per-token rate limit — SKIPPED for now for the same single-user/loopback
  rationale.

### 2.3 mcp-broker: rules-engine ergonomics [S-M] — SKIPPED

- Decision: do not add regex linting or a `mcp-broker rules test` subcommand for now.
- Rationale: current rules are local single-user configuration, and adding warnings or CLI
  surface area is not worth the complexity until policy authoring becomes a recurring pain.

### 2.4 local-git-mcp: warn on `--allow-all-paths` [S] — RESOLVED

- Implemented a prominent startup warning and README note that `--allow-all-paths` disables
  repository path isolation.

### 2.5 sandbox-manager: network egress posture [L, design decision]

- The VM currently has unrestricted network access. Best practice (Claude Code sandboxing,
  Codex default-deny, lethal-trifecta reasoning) is to cut the exfiltration leg: agents read
  untrusted content, so egress should be allowlisted.
- Plan: add an optional egress allowlist mode — default-deny iptables/ipset (or nftables)
  provisioned into the VM from config (modeled on Anthropic's devcontainer
  `init-firewall.sh`), allowing host.lima.internal (broker, gomod-proxy), package registries,
  and configured domains. Lessons from the Claude Code allowlist bypass: canonicalize
  hostnames, allowlist valid DNS characters, don't blocklist.
- This is the single highest-value security improvement in the repo; it converts the stack
  from "credentials are isolated" to "exfiltration is also constrained".

### 2.6 Secrets-into-VM story [M, design decision] — DECLINED

- Decision: do not add warnings for sensitive-looking `sb` copy paths.
- Rationale: `copy_paths` is explicit user configuration. Warning on paths the user deliberately
  chose to copy would add noise without changing the trust model. The tool should not second-
  guess intentional provisioning choices.

---

## Phase 3 — MCP spec readiness (2025-11-25 now, 2026-07-28 RC soon)

### 3.1 Adopt current-spec features in local-git-mcp [M] — SKIPPED

- Decision: do not add `structuredContent`/`outputSchema` or revisit input-validation error
  classification for local-git-mcp right now.
- Rationale: current text-only results and annotations are sufficient for this tool's current
  usage; revisit when downstream agents demonstrably benefit from structured git remote/ref
  outputs or mcp-go exposes cleaner validation-error semantics.

### 3.2 mcp-broker: prepare for the 2026-07-28 stateless core [L, watch-and-plan] — SKIPPED

- Decision: do not create a tracking issue or add preparatory code for the 2026-07-28 RC now.
- Rationale: mcp-broker should follow stable `mcp-go` support rather than tracking RC details
  in this remediation plan. Revisit once `mcp-go` ships stable support for `Mcp-Method`/
  `Mcp-Name`, list cache metadata, and Trace Context propagation.

### 3.3 mcp-broker: elicitation-based approvals [M, optional] — SKIPPED

- Decision: do not add elicitation-based approvals now.
- Rationale: dashboard/Telegram approvals cover the current workflow, and elicitation support
  should wait until client/server support is mature enough to justify parallel approval paths.

---

## Phase 4 — Repo infrastructure

### 4.1 GitHub Actions CI [M] — SKIPPED

- Decision: do not add GitHub Actions CI for now.
- Rationale: local pre-commit hooks and `make lint` currently provide the desired gate, and
  externally hosted CI is not needed for this repository's current workflow.
- Revisit if the repository starts accepting external PRs or needs unattended remote quality
  gates.

### 4.2 Lint config consistency [S] — RESOLVED

- Added matching `.golangci.yml` configs to local-git-mcp and local-gomod-proxy, so all 6 Go
  tools now have lint config. Updated the "Adding a New Tool" checklist in root CLAUDE.md.

### 4.3 Doc drift fixes [S] — RESOLVED

- Updated worktree-manager docs to narrow idempotency claims to existing worktrees/windows and
  removal, with copy/setup documented as best-effort initial-creation steps.
- Updated sandbox-manager docs to state Lima preserves the host UID but does not support
  setting the VM user's primary GID in the template.
- Updated pi-dispatcher docs to say "no central daemon" instead of "daemonless".
- Optional future work: a tiny doc-drift make target that greps docs for referenced paths/flags
  and checks they exist (see new-tool idea 6.5 — could start as a 50-line script here).

### 4.4 Shared conventions without a shared module [S] — RESOLVED

- Decision: keep copy-paste rather than adding a shared internal module.
- Rationale: each tool remains an independent Go module, and small local interfaces/helpers like
  `exec.Runner` are not worth cross-tool coupling.
- Added a root CLAUDE.md convention: when adding subprocess execution, use a context-aware
  runner with finite deadlines and copy the nearest tool's established pattern.

---

## Phase 5 — Workflow-tool UX (what 2026 users expect)

The 2026 parallel-agent feature baseline is: review queues, completion gates, notifications,
status dashboards. Cheap wins first:

### 5.1 worktree-manager [M]

- `wt ls` (enumerate worktrees with branch + tmux-window liveness) and `wt status <branch>` —
  SKIPPED. Native `git worktree list`, `git status`, and tmux commands cover this well enough
  for current usage.
- `wt add --force` to replace a broken/partial worktree — REPLACED by `wt add --reconfigure`,
  which repairs configuration without deleting/recreating the worktree.
- Surface copy/setup failures as errors (ties into 1.3) — RESOLVED.

### 5.2 sandbox-manager [M]

- Capture and persist provisioning output (`sb create`/`sb provision` currently discard
  script stdout/stderr) → `sb logs`.
- `sb status --json` — RESOLVED. Human status remains unchanged; JSON output uses
  `running`, `stopped`, or `not_created`.
- Defer: multi-instance VMs, hot mount-add (recreate is acceptable for now; document it).
- Watch Lima v2.x (CNCF incubating, AI-sandbox focus: plugins, krunkit, native Lima MCP
  server) — evaluate whether the native MCP server overlaps or composes with mcp-broker.

### 5.3 pi-dispatcher [M-L]

- `pd cleanup --all` for batch cleanup of terminal tasks — SKIPPED. `pd cleanup` already supports
  multiple explicit task IDs, which is sufficient; avoid adding broad cleanup affordances for now.
- Optional max-runtime per task (`pd run --max-duration`) so a looping agent can't run
  forever; ties into supervisor stop path.
- Dashboard: token rotation currently strands running dashboards — have the dashboard reload
  the token or document restart requirement.
- Completion gate hook (see new-tool 6.2 — could live inside pd): on task terminal-success,
  optionally run a configured gate command (make audit, tests) in the worktree and record
  pass/fail in task state. This is the "agent finished → gate → human review" pattern the
  ecosystem converged on, and pd already owns the lifecycle hook point.

### 5.4 Observability across the stack [M]

- mcp-broker already has the audit DB — add OTel-style trace/correlation IDs to audit rows
  (cheap now, mandatory-ish when 2026-07-28 lands).
- pi-dispatcher: per-task token/cost capture if the Pi event stream exposes usage; surface in
  `pd ps`/dashboard.
- Optional `--log-file` (or env) writing structured logs to XDG state dir for the
  long-running services.

---

## Phase 6 — New tool candidates (ranked)

Ranked by fit with the existing stack, gap in the ecosystem, and personal value. Each would
follow the standard new-tool checklist in root CLAUDE.md.

### 6.1 local-registry-proxy: npm/PyPI analog of local-gomod-proxy [HIGH]

- Host-side single-binary caching proxy speaking the npm registry protocol and the PyPI
  simple API, loopback-only + basic auth + self-signed TLS (reuse gomod-proxy's exact
  skeleton: ValidateLoopbackAddr, credentials file, cert rotation). Package allowlist/
  denylist and optional version pinning for supply-chain control.
- Why: no agent-focused equivalent exists (Verdaccio is a heavyweight JS app); it directly
  extends a pattern this repo already proved; it pairs with the 2.5 egress allowlist
  (sandbox can reach only the proxies).
- Start with npm; PyPI second; same binary or sibling tool — decide at design time.

### 6.2 gate-runner: deterministic quality gate for finished agent work [HIGH]

- Watches for completed pi-dispatcher tasks (or invoked as `pd`'s completion hook), runs the
  repo's deterministic gates (fmt/lint/test/govulncheck) in the worktree, optionally one
  LLM review pass, then notifies and/or opens a draft PR via the broker's GitHub tools.
- Why: "agent finished → gate → notify → PR" is universally bespoke bash today; this repo
  owns every piece of the pipeline already. Could start as a pd subcommand (5.3) and
  graduate to a tool if it grows.

### 6.3 hook-policyd: HTTP-hook policy daemon + audit + allowlist miner [HIGH]

- Single Go binary serving Claude Code's HTTP hook protocol (shipped Jan 2026): declarative
  policy file (allow/deny/ask with regex + path scoping), structured audit log (same SQLite
  pattern as mcp-broker), and an `allowlist suggest` subcommand that mines the audit log /
  transcripts for repeatedly-approved commands and emits settings.json permission rules for
  review.
- Why: the serving side of HTTP hooks is a green field; it gives one policy/audit plane for
  both MCP calls (broker) and local tool calls (hooks). Biggest design question: one merged
  audit story across broker + hooks ("what did the agent do yesterday") — that merger is
  itself the differentiator.

### 6.4 agent-notify: fleet notification/approval multiplexer [MEDIUM]

- Multiplexes events from pi-dispatcher tasks, broker approval requests, and hook "ask"
  decisions into one ntfy.sh topic with per-agent action buttons; button presses route back
  to the right approver (broker approval API, pd steer/stop, hook response).
- Why: single-session remote control is solved (Happy, official Remote Control); fleet-level
  "agent #2 is blocked, approve from phone" is not. Natural once 6.3 exists; the broker's
  MultiApprover interface is already the right seam (a NtfyApprover next to Telegram).
- Cheapest first step: add an ntfy approver to mcp-broker (S effort) before building the
  standalone multiplexer.

### 6.5 doc-drift: deterministic doc freshness checker [MEDIUM]

- Language-agnostic Go CLI: files referenced in README/DESIGN/CLAUDE.md exist; commands in
  fenced blocks resolve (`--help` probe or Makefile-target check); structure blocks match the
  tree. No LLM. Run in CI (4.1) and pre-commit.
- Why: only TS-specific (drift) and SaaS options exist; this repo's doc discipline is the
  ideal first customer; it would have caught the GID and idempotency drift mechanically.

### 6.6 sb snapshot/clone: fast sandbox spin-up [LOWER, exploratory]

- `sb snapshot` / `sb clone` using qcow2 backing files or Lima instance cloning for sub-10s
  fresh VMs (vs full provision). Differentiator for local-first parallel agents; depends on
  Lima 2.x capabilities — spike first.

### Explicitly not recommended now

- Session-transcript search (cass already dominates), CLAUDE.md _generators_ (evidence says
  generated context files hurt), generic MITM credential injector (Infisical agent-vault
  covers it; revisit only if a concrete need appears beyond git/gh/gomod/npm).

---

## Suggested sequencing

1. **Week 1 (correctness):** 1.1 GID, 1.2 timeouts.
2. **Week 2 (infra + concurrency):** 4.1 CI, 1.3 worktree/pd partial-failure work.
3. **Week 3-4 (security):** 2.1 tool pinning, 2.2 limits, 2.3 rules lint/test command;
   design spike for 2.5 egress allowlist.
4. **Then:** 5.x UX items opportunistically; pick ONE new tool (recommend 6.1
   local-registry-proxy or 6.2 gate-runner) and take it through the full
   README/DESIGN/CLAUDE.md lifecycle; create the 3.2 spec-tracking issue immediately
   (zero-cost) and revisit when mcp-go ships 2026-07-28 support.

## Verification notes

Findings marked CONFIRMED were re-checked directly. The rest came from deep subagent review
with file:line references; re-verify each at implementation time (especially line numbers,
which drift). Key claims worth re-confirming before coding: mcp-go client timeout options
(1.2) and Lima `user.gid` support (1.1).
