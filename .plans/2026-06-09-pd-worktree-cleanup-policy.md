# Pi Dispatch Worktree Cleanup Policy Plan

## Goal

Add an optional per-run automatic worktree cleanup policy to `pi-dispatch` so completed `pd` tasks can remove their associated worktree without deleting the branch. The feature should be safe by default, durable across the detached supervisor boundary, and observable in CLI/JSON/dashboard status.

## Background / Repo Context

- `pi-dispatch` is a daemonless background runner. `pd run` creates a headless worktree, creates SQLite task/run records, and starts a detached `pd supervisor`; the supervisor owns Pi RPC execution and terminal run recording (`pi-dispatch/DESIGN.md`, `pi-dispatch/cmd/pd/run_impl.go`, `pi-dispatch/cmd/pd/supervisor_impl.go`).
- Current manual cleanup is tied to task removal: `pd rm --worktree <task-id>` calls `worktree-manager` removal, then removes task state/logs/DB rows (`pi-dispatch/cmd/pd/logs_impl.go`). The new design should separate resource cleanup from task-state removal instead of preserving that coupling as the primary UX.
- `worktree-manager/pkg/worktree.Client.Remove` already preserves branches because it delegates to `workspace.Remove(repoRoot, branch, false, false)` (`worktree-manager/pkg/worktree/worktree.go`, `worktree-manager/internal/workspace/workspace.go`).
- `git worktree remove` is non-forced today (`worktree-manager/internal/git/git.go`), so dirty or otherwise unsafe worktrees are preserved by default. Auto-cleanup should keep that safety property.
- `pd` config currently only includes `database_path` (`pi-dispatch/internal/config/config.go`). Store migrations are additive and currently handled in `pi-dispatch/internal/store/store.go`.
- Dashboard is read-only and should expose persisted cleanup state only; it should not perform cleanup or stale reconciliation (`pi-dispatch/DESIGN.md`).

## Acceptance Criteria

- AC-1: `pd run` accepts a per-run worktree cleanup policy with values `never`, `on-success`, and `on-terminal`; the default remains `never` unless changed by config.
- AC-2: `pd` config supports a default cleanup policy, and `pd config refresh` writes the new field without breaking existing config files.
- AC-3: Cleanup intent and result are persisted durably so a detached supervisor can honor the launch-time policy and later inspection can explain what happened.
- AC-4: When policy is `on-success`, the supervisor attempts branch-preserving, non-forced worktree removal only after a successful terminal run and only after the Pi process has exited/been waited on.
- AC-5: When policy is `on-terminal`, the supervisor attempts branch-preserving, non-forced worktree removal after `succeeded`, `failed`, or `stopped` terminal completion, subject to the same safety rules.
- AC-6: Policy `never` preserves current automatic behavior: no automatic worktree removal occurs.
- AC-7: `pd cleanup <task-id>` performs explicit resource cleanup for a terminal task while preserving task metadata, logs, Pi event streams, DB rows, and branch.
- AC-8: `pd cleanup --dry-run <task-id>` reports what would be cleaned without mutating the worktree or cleanup state.
- AC-9: `pd rm <task-id>` removes only pd task state/logs/control-socket tracking and does not remove the worktree or branch.
- AC-10: Automatic and manual cleanup never delete the branch and never force-remove dirty or otherwise blocked worktrees.
- AC-11: Automatic cleanup failure or skip does not change the task/run terminal status, exit code, or `pd wait` success/failure semantics.
- AC-12: Automatic cleanup only removes a worktree that `pd run` created for that task; pre-existing/reused worktrees are not automatically removed.
- AC-13: Launch failures after task creation but before supervisor start honor the cleanup policy when safe; failures where a supervisor may be live do not remove the worktree prematurely.
- AC-14: `pd status`, `pd status --json`, `pd wait --json`, and Dashboard Overview expose cleanup policy/result/error enough to distinguish kept, removed, skipped, failed, and not requested states.
- AC-15: Existing task databases migrate safely with default cleanup policy/result values and existing tests continue to pass.

## Non-Goals / Out of Scope

- Do not add branch deletion to automatic cleanup.
- Do not add forced automatic cleanup for dirty worktrees.
- Do not implement a `pd gc` command in this change.
- Do not add forced cleanup or discard semantics to `pd cleanup`.
- Do not make dashboard or `pd ps` mutate worktrees opportunistically.
- Do not automatically remove task metadata, logs, Pi event streams, or DB rows when auto-cleanup or `pd cleanup` runs.
- Do not guarantee cleanup for stale `unknown` tasks caused by supervisor crash, host reboot, or external process death; those remain explicit manual cleanup cases.

## Constraints

- Preserve daemonless architecture: cleanup must happen in `pd run` launch-failure paths or the detached supervisor, not in a background daemon.
- Preserve existing safety model: branches survive, dirty worktrees survive, and cleanup is best-effort.
- Persist launch-time policy rather than reading current config in the supervisor, because config can change after `pd run` returns.
- Keep external worktree operations routed through the existing `worktreeClient` abstraction so tests can inject fakes.
- Follow repo documentation boundaries: update `pi-dispatch/README.md` and `pi-dispatch/DESIGN.md`; `worktree-manager` docs likely do not need updates because its semantics do not change.

## Chosen Approach

Implement option 1 as a first-class cleanup policy: `never`, `on-success`, and `on-terminal`. The policy is selected at launch by config default plus `pd run --cleanup-worktree <policy>` override, then persisted on the run/task state for the supervisor to honor.

Also split the manual command model into two distinct operations:

- `pd cleanup <task-id>` cleans task-owned external resources, starting with the worktree, while preserving pd history/logs and the branch.
- `pd rm <task-id>` removes pd task state/logs/control-socket tracking only, leaving the worktree and branch untouched.

Cleanup is a post-run/resource side effect, not part of agent success. The supervisor should first finish the Pi process, compute and persist the terminal task/run status, then attempt cleanup if the policy matches. It should record cleanup result separately. A cleanup failure should be visible but should not convert a successful agent run into a failed task.

The implementation should also track whether the worktree was newly created by this `pd run`. Automatic cleanup should only remove pd-owned worktrees to avoid surprising removal when `--branch` points at an existing worktree or when `worktree-manager` reused an existing deterministic path. Manual `pd cleanup` may target the associated task worktree explicitly, but should still use non-forced, branch-preserving removal and keep task history.

## Design Decisions

- D1: Use an enum policy, not a boolean. `never|on-success|on-terminal` is explicit, scriptable, and leaves room for clear config defaults without ambiguous flag semantics.
- D2: Default to `never`. This preserves current behavior and avoids surprising deletion of worktree directories containing task output.
- D3: Persist cleanup fields in SQLite. Detached supervisors must not depend on mutable config or in-memory launch state, and inspection needs durable cleanup evidence.
- D4: Treat cleanup as best-effort metadata, not task status. Agent terminal status and cleanup result answer different questions.
- D5: Use non-forced `worktree-manager` removal. Dirty/untracked/submodule-blocked worktrees should remain available for user review.
- D6: Track worktree ownership. Auto-cleanup should only clean worktrees created by the current task; manual `pd cleanup` is the explicit path for user-requested resource cleanup while preserving task history.
- D7: Separate cleanup from removal. `pd cleanup` manages task resources; `pd rm` forgets pd tracking state. The commands should not have overlapping side effects.
- D8: Keep Dashboard read-only. It should display cleanup fields from SQLite, not initiate or retry cleanup.

## Implementation Notes

- Config and flags:
  - Add a cleanup policy field to `pi-dispatch/internal/config.Config`, with default `"never"`.
  - Add validation for allowed values `never`, `on-success`, `on-terminal`.
  - Add `pd run --cleanup-worktree <policy>` in `pi-dispatch/cmd/pd/run_impl.go`, using config default unless overridden.
  - Consider shell completion only if nearby command patterns make it easy; not required unless existing tests expect completions.
- Store model:
  - Add durable cleanup fields to the appropriate persisted model. A practical shape is on `tasks` because worktree cleanup is task-level, but run-level is also acceptable if the implementation consistently exposes latest-run cleanup state. Suggested fields:
    - `worktree_cleanup_policy`
    - `worktree_created_by_pd` or equivalent ownership boolean
    - `worktree_cleanup_status` such as `not_requested`, `pending`, `removed`, `kept`, `skipped`, `failed`
    - `worktree_cleanup_error`
    - `worktree_cleanup_attempted_at`
    - `worktree_removed_at`
  - Add additive migration code in `pi-dispatch/internal/store/store.go` following the existing `ensureRunMetadataColumns` pattern.
  - Add store methods for recording cleanup attempts/results separately from `CompleteRun`.
- Ownership detection:
  - Before `wt.AddHeadless`, determine whether the deterministic worktree path already exists. The current public `worktreeClient` interface only has `AddHeadless` and `Remove`; it may need a `Path(repoRoot, branch)` method mirroring `worktree-manager/pkg/worktree.Client.Path`.
  - Persist `worktree_created_by_pd=true` only when the path did not exist before `AddHeadless` and creation succeeded.
  - If exact ownership is hard to prove for an edge case, choose the safe result: skip auto-cleanup and record why.
- Cleanup execution:
  - Add a helper in `cmd/pd` that evaluates `policy + terminal status + ownership`, calls `newWorktreeClient().Remove(repo, branch)` when eligible, and records the result.
  - Invoke this helper after normal supervisor terminal completion, after Pi process wait/kill handling has completed.
  - For launch failures after DB/task creation but before supervisor start, call the same helper when safe. Do not cleanup after a supervisor process may have started unless the launcher path has proven it is not running or has been terminated.
  - Bound cleanup if practical. If worktree-manager removal is not cancellable today, at minimum avoid introducing unbounded retry loops; a future timeout can be added through runner/context support.
- Command and CLI/JSON/Dashboard UX:
  - Add a dedicated `pd cleanup <task-id>` command. It should refuse active tasks unless the existing status/reconciliation rules prove the task is terminal. It should remove only the worktree resource, preserve the branch, preserve task state/logs, and record cleanup result.
  - Add `pd cleanup --dry-run <task-id>` to report eligibility, target worktree, branch-preservation, and likely action without mutating worktree or cleanup state.
  - Change `pd rm <task-id>` semantics to remove only pd tracking state/logs/control sockets. It must not remove the worktree or branch.
  - Remove the `pd rm --worktree` path from the clean command model; resource cleanup belongs to `pd cleanup`, while task-state removal belongs to `pd rm`.
  - Extend `pd status` human output to show cleanup policy/result and error when non-empty or non-default. If removed, make clear that the branch remains.
  - Extend JSON structs used by status/wait/dashboard to include cleanup fields.
  - Preserve existing `worktree_path` so users can see the original path even after removal.
- Tests to add/update:
  - `pi-dispatch/internal/config/config_test.go` for default config, JSON parsing, refresh output, and invalid policy handling if validation lives there.
  - `pi-dispatch/cmd/pd/run_overrides_test.go` or nearby run tests for CLI policy override and persisted launch metadata.
  - `pi-dispatch/internal/store/store_test.go` for schema defaults and migration from an old DB schema.
  - `pi-dispatch/cmd/pd/supervisor_impl_test.go` or a new focused test file for cleanup helper behavior: on-success, on-terminal, never, failed cleanup, skipped non-owned, branch-preserving remove call.
  - Add command tests for `pd cleanup`: successful cleanup preserves task row/logs, dirty/remove failure preserves task row/logs and records failure, dry-run performs no mutation, active tasks are refused.
  - Update `pd rm` tests to assert it removes only pd state/logs/control sockets and does not call worktree removal.
  - `pi-dispatch/internal/dashboard/dashboard_test.go` for cleanup fields in API/detail output if dashboard schema changes.

## Documentation Impact

- Update `pi-dispatch/README.md`:
  - Quick Start / run options to mention `pd run --cleanup-worktree on-success` and `on-terminal`.
  - Command docs to present `pd cleanup <task-id>` as resource cleanup and `pd rm <task-id>` as task-state removal.
  - Safety model text that currently says `pd` does not automatically remove worktrees.
  - Config JSON and configuration table/text to include cleanup default.
  - Explain that automatic cleanup and `pd cleanup` preserve branches, are non-forced, and record failures without failing or deleting task history.
- Update `pi-dispatch/DESIGN.md`:
  - V1 architecture lifecycle to include optional post-run cleanup.
  - State model to include cleanup policy/result fields.
  - Dashboard read-only section to clarify it displays cleanup state but does not mutate.
- No `worktree-manager` documentation update is required unless its public API or behavior changes; using its existing branch-preserving removal semantics is sufficient.

## Testing / Verification

- V1: Run `go test ./...` from `pi-dispatch/`; expected result: all tests pass, including new config/store/supervisor/status/dashboard coverage.
- V2: Run `make test` from `pi-dispatch/`; expected result: race-enabled package tests pass.
- V3: Run `make lint` from `pi-dispatch/`; expected result: no lint failures.
- V4: Manual or integration-style check with faked worktree client: `on-success` calls `Remove(repo, branch)` only for successful owned worktrees and records `removed`; `never` makes no remove call.
- V5: Manual or test check for dirty/removal failure: fake/remove error leaves task status `succeeded` or `failed` as originally computed, records cleanup `failed` with error, and preserves task metadata/logs.
- V6: Manual or test check for pre-existing worktree: policy requests cleanup but ownership is false, no remove call is made, and cleanup result explains it was skipped/kept.
- V7: Command behavior checks: `pd cleanup` removes only the worktree and keeps task history; `pd cleanup --dry-run` mutates nothing; `pd rm` removes only pd task state/logs/control socket and never calls worktree removal.
- V8: Documentation review: `pi-dispatch/README.md` and `pi-dispatch/DESIGN.md` accurately describe default behavior, policy values, command split, safety guarantees, and dashboard read-only behavior.

## Risks and Mitigations

- Risk: Auto-cleanup removes useful uncommitted task output. Mitigation: non-forced removal only, default `never`, ownership tracking, and visible cleanup failures/skips.
- Risk: Cleanup failure is mistaken for agent failure. Mitigation: separate cleanup result fields and keep terminal status/exit code unchanged.
- Risk: Existing databases break due to new fields. Mitigation: additive migrations with safe defaults and migration tests from the old schema.
- Risk: `--branch` reuses an existing worktree and auto-cleanup surprises the user. Mitigation: persist ownership and skip non-owned worktrees automatically.
- Risk: Supervisor crash or host reboot leaves cleanup undone. Mitigation: document auto-cleanup as best-effort; do not rely on a daemon or dashboard mutation.
- Risk: Cleanup after launch failure races with a live supervisor. Mitigation: only cleanup in launch-failure paths where supervisor was not started, or where the process has been proven stopped.

## Assumptions

- Policy names will be exactly `never`, `on-success`, and `on-terminal`.
- Default automatic cleanup remains disabled (`never`) to preserve current behavior.
- Automatic cleanup preserving branches means using existing `worktree-manager` branch-preserving remove semantics, not adding branch deletion controls to `pd run`.
- It is acceptable for automatic cleanup to skip pre-existing worktrees even when the user requested cleanup; manual `pd cleanup <task-id>` is available for explicit resource cleanup while preserving task history.

## Handoff Summary

Implement `.plans/2026-06-09-pd-worktree-cleanup-policy.md` by adding a persisted `pd run` cleanup policy, safe post-run cleanup execution, visible cleanup status in CLI/JSON/dashboard, and README/DESIGN updates. Complete only after every acceptance criterion is satisfied with concrete evidence from tests and documentation review.

Suggested `/goal` objective:

```text
Implement .plans/2026-06-09-pd-worktree-cleanup-policy.md. Complete only after every acceptance criterion is satisfied with concrete evidence from tests, status/JSON/dashboard behavior, and documentation updates.
```
