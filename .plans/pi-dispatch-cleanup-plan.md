# Pi Dispatch Cleanup Plan

## Goal

Make `pi-dispatch` lifecycle, removal, status, logging, and control behavior safer and more scriptable without expanding the artifact/template feature surface.

## Constraints

- Preserve the daemonless V1 architecture: `pd run` starts a detached hidden `pd supervisor`; no always-on daemon.
- Do not automatically remove worktrees on launch/sandbox failures; worktree cleanup is explicit via `pd rm --worktree`.
- Keep the existing broad template/CLI override surface unchanged.
- Keep the existing command wiring style for now, except removing the duplicate hidden `follow-up` command.
- Keep headless extension UI handling as-is: blocking dialogs are auto-cancelled; fire-and-forget UI requests remain Pi events.
- Do not add artifacts, summaries, dashboards, PR metadata, or report generation.
- Prefer worktree-manager APIs for worktree removal; do not raw-delete worktrees with `rm -rf`.

## Acceptance Criteria

- AC-1: `pd stop --force <task>` schedules force termination even when Pi RPC abort fails, while graceful non-force stop still reports abort errors.
- AC-2: Launch and supervisor startup failures after DB row creation end in terminal `failed` state with `ended_at`, `exit_code`, and `error_message`; no automatic worktree cleanup occurs.
- AC-3: Supervisor launch uses the current executable instead of PATH lookup and releases/detaches the child process intentionally.
- AC-4: `pd rm <task>` removes inactive task metadata/log/control artifacts, refuses active tasks, and `pd rm --worktree <task>` removes the associated worktree through worktree-manager before deleting DB state.
- AC-5: `pd status` JSON and text output include terminal run metadata when present; `pd ps` remains compact.
- AC-6: Running/starting/stopping task reconciliation uses PID existence plus a bounded control-socket `ping` check, with a starting grace period to avoid racing newly launched supervisors.
- AC-7: `pd logs -f` and `pd attach` follow stdout and stderr with source prefixes, while `pd events` remains the structured Pi event view.
- AC-8: Pi RPC event reading handles strict LF-delimited JSONL without `bufio.Scanner` token limits.
- AC-9: Mutation commands (`stop`, `steer`, `followup`, `rm`) emit JSON success responses under `--json`.

## Chosen Approach

Implement lifecycle hardening first, then removal/status/control UX improvements. This keeps the most correctness-critical pieces isolated and testable before adding command-facing behavior. The key trade-off is deferring a supervisor readiness handshake: using the current executable plus proper detach solves immediate fragility, while failure finalization and ping reconciliation provide enough observability for this pass.

## Documentation Impact

Update `pi-dispatch/README.md` for user-visible behavior changes:

- Document completed `pd rm` and `pd rm --worktree` semantics, including inactive-only refusal and explicit worktree cleanup.
- Document JSON success output for mutation commands.
- Document `pd status` terminal metadata fields.
- Adjust `logs -f` / `attach` wording to describe stdout+stderr prefixed following.
- If the `--pi-argv` supervisor help text remains visible only on hidden command, no README change is required; implementation should still fix the help text for accuracy.

No `DESIGN.md` update is required unless execution changes intended architecture beyond the agreed V1 lifecycle/control behavior.

## Assumptions / Open Questions

- Q1: Worktree removal should not delete the branch; `pd rm --worktree` should call worktree-manager removal with branch deletion disabled. Status: agreed by implication; confirm during implementation if worktree-manager API requires exposing a new package method.
- Q2: Log/control artifact cleanup should happen before DB deletion where possible; if worktree removal is requested and fails, DB/logs should remain so the command can be retried. Status: agreed.
- Q3: Starting grace for ping reconciliation can be a small fixed duration based on run `StartedAt`/task `UpdatedAt`, centralized as a constant. Status: implementation choice.

## Ordered Tasks

### T1: Add tests for agreed behavior

Covers: AC-1 through AC-9

- Add or update focused unit tests in `pi-dispatch/cmd/pd/*_test.go`, `pi-dispatch/internal/control/*_test.go`, `pi-dispatch/internal/pi/*_test.go`, `pi-dispatch/internal/process/*_test.go`, and `pi-dispatch/internal/store/*_test.go` as appropriate.
- Convert the existing stop-force test that expects no kill on abort failure to the new expected behavior.
- Add tests for status view terminal fields and compact `ps` behavior.
- Add tests for `rm` inactive refusal, metadata/log deletion, DB-last ordering where practical, and `--worktree` failure preserving DB state.
- Add tests for control timeout/ping and RPC long-line/LF framing.

### T2: Harden stop and supervisor failure finalization

Covers: AC-1, AC-2

- Update `applyStopRequest` in `pi-dispatch/cmd/pd/supervisor_impl.go` so force kill scheduling is independent of abort success.
- Remove `forceRequested` from `supervisorRunState` and related tests.
- Add a helper for supervisor terminal failure that records events and calls `CompleteRun` with error metadata.
- Use `StatusStarting` when task/run rows are created in `runTask`, then transition to running only once the supervisor starts.
- On launch failures after DB creation, call `CompleteRun(..., StatusFailed, ...)` before returning.
- Open required log files before starting Pi and fail via terminal metadata if any log open fails.
- Keep worktree cleanup out of launch failure paths.

### T3: Fix supervisor launch/detach

Covers: AC-3

- Change `pi-dispatch/internal/process/process.go` so `StartSupervisor` launches `os.Executable()` instead of literal `pd`.
- Update `internal/exec` process-start behavior to intentionally detach/release the supervisor child, using platform-appropriate process attributes if already supported by the package conventions.
- Keep the command shape as `supervisor --task-id ... --pi-argv ...`.
- Fix hidden supervisor `--pi-argv` help text to describe JSON-encoded argv, not NUL-separated argv.

### T4: Implement removal and remove duplicate follow-up command

Covers: AC-4, AC-9

- Remove the hidden duplicate `followUpCmd`; keep `followupCmd` with alias `follow-up` and one `RunE` assignment.
- Extend the `worktreeClient` abstraction and worktree-manager `pkg/worktree.Client` with a safe removal method if needed, delegating to workspace removal with branch deletion disabled.
- Implement `removeTask` in `pi-dispatch/cmd/pd/logs_impl.go` or split to a dedicated file if tests/readability justify it.
- Refuse removal for `starting`, `running`, and `stopping` tasks after reconciliation.
- For default `pd rm`, remove task log directory and stale control socket, then delete task/runs/events from SQLite last.
- For `pd rm --worktree`, remove the associated worktree through worktree-manager before deleting logs/control/DB state; if worktree removal fails, preserve DB/logs.
- Add store deletion method(s) that delete events/runs/task transactionally.
- Add `--json` success output for `rm`.

### T5: Improve status, mutation JSON, and control robustness

Covers: AC-5, AC-6, AC-9

- Extend `runView` in `pi-dispatch/cmd/pd/inspect_impl.go` with `ended_at`, `exit_code`, `error_message`, and `pi_session_file` fields.
- Print terminal metadata in text `pd status` only when present/relevant; keep `pd ps` unchanged.
- Add `control.OpPing`, server handling, and a central timeout for `control.Send`.
- Update reconciliation to require PID existence and ping success for mature `starting`/`running`/`stopping` tasks; keep a small starting grace window.
- Add JSON success responses for `stop`, `steer`, and `followup` when `--json` is set.

### T6: Improve logs/attach following

Covers: AC-7

- Replace single-file following with a helper that can follow stdout and stderr concurrently.
- Prefix followed lines with source labels such as `stdout` and `stderr`.
- Ensure initial non-following `pd logs` remains useful and does not duplicate confusing prefixes unless tests/documentation choose to standardize prefixes for all output.
- Keep `pd events` as the only structured event stream command.

### T7: Replace Pi RPC Scanner reader

Covers: AC-8

- Replace `bufio.Scanner` in `pi-dispatch/internal/pi/rpc.go` with an LF-delimited reader that strips optional trailing `\r` and does not impose Scanner token limits.
- Preserve raw event bytes for event logging.
- Add tests for long JSONL records and CRLF tolerance, matching Pi RPC documentation.

### T8: Update documentation

Covers: AC-4, AC-5, AC-7, AC-9

- Update `pi-dispatch/README.md` command docs/examples for `rm`, mutation JSON, status metadata, and logs/attach following behavior.
- Recheck whether `pi-dispatch/DESIGN.md` still matches intended behavior after implementation; update only if an architectural/design statement changes.

## Verification Checklist

- [ ] V1: `make -C /Users/avery/.local/share/wt/worktrees/agent-tools/agent-tools-dispatch2/pi-dispatch test` passes.
- [ ] V2: Targeted stop-force tests show force kill is scheduled even when abort fails.
- [ ] V3: Targeted launch/supervisor failure tests show terminal `failed` status with error metadata and no worktree cleanup call.
- [ ] V4: Targeted `rm` tests cover inactive removal, active refusal, `--worktree` success, and `--worktree` failure preserving DB/logs.
- [ ] V5: Targeted reconciliation tests cover PID gone, PID alive/socket ping success, PID alive/socket ping failure, and starting grace.
- [ ] V6: Targeted RPC reader tests cover large events and CRLF handling.
- [ ] V7: Confirm Documentation Impact was followed: README updates are present and DESIGN is updated only if needed.

## Known Issues / Follow-ups

- Supervisor ready handshake remains deferred.
- Artifact/report storage remains out of scope.
- Template boolean override semantics remain unchanged.
- Full stdout/stderr/event multiplexing in `attach` remains out of scope; events stay in `pd events`.
