# Worktree Sync UX Reliability Plan

## Goal

Make `worktree-sync` easier to start, diagnose, and recover without weakening its explicit registration, fail-closed reconciliation, filesystem containment, or tmux ownership guarantees. Commands must report what actually happened, explain safe recovery, and remain useful in both interactive and automated workflows.

## Background / Repo Context

- `worktree-sync` is a Go 1.25 CLI/daemon module. Cobra construction is thin and centralized in `worktree-sync/cmd/root.go`; requests cross `worktree-sync/internal/app/wts.go` into orchestration in `worktree-sync/internal/service/service.go`.
- Configuration and atomic persistence live in `worktree-sync/internal/config/` and `worktree-sync/internal/state/`. Config schema version 3 already separates creation roots from complete allowed-root safety boundaries; this work does not require a config migration.
- Git, tmux, actions, reconciliation, and LaunchAgent behavior are isolated behind `worktree-sync/internal/git/`, `tmux/`, `actions/`, `reconcile/`, and `launchd/`. Preserve their finite deadlines, bounded capture, argv execution, ownership metadata, stable-ID rechecks, and fail-closed snapshot rules.
- `reconcile.Build` already provides the pure desired-state plan. The executor applies it with ownership rechecks, but current service output reports only counts.
- `cmd.run` intentionally prints non-empty service output before returning an error. Preserve this mechanism and use it consistently for partial success and check-style commands rather than introducing a speculative result framework.
- Existing tests provide fakes at the command controller, runner, Git, tmux, registry, reconcile, daemon, and LaunchAgent boundaries. Integration tests use real temporary Git repositories and a unique tmux socket.
- The decisions in this plan were selected explicitly during the UX audit walkthrough. In particular: registration stays explicit; attach does not reconcile automatically; setup skips exit zero; invalid config edits remain saved; and daemon reconciliation remains automatic.

## Acceptance Criteria

- **AC-1 — Truthful LaunchAgent lifecycle:** With `KeepAlive` preserved, all lifecycle commands serialize their full observe/mutate/rollback sequence under a fixed per-user lifecycle lock independent of XDG state (and acquire any XDG operation lock only after it). `stop` unloads a loaded job and leaves the plist installed; stopping an unloaded installed job or absent installation succeeds with an accurate no-op message. `start` loads an unloaded installed job, reports an already loaded job as a successful no-op, and fails with `run wts daemon install` when no plist exists. `restart` loads an installed-but-unloaded job, unloads and reloads a loaded job, and fails with install guidance when no plist exists. `status` uses owned-plist existence plus scoped `launchctl print` to distinguish running (loaded), stopped-but-installed, not installed, unsupported, and unavailable/error states. Uninstall remains scoped to the owned label/plist.
- **AC-2 — LaunchAgent environment consistency:** `wts daemon install` writes the effective absolute `XDG_CONFIG_HOME`, `XDG_STATE_HOME`, and `XDG_DATA_HOME` plus the managed `PATH` into the plist. Reinstall updates those values. Rendered XML escapes values, remains private, and points `wtsd` at the same config/state/data locations as the invoking `wts` process.
- **AC-3 — Safe daemon CLI:** `wtsd --help` exits successfully without loading config, acquiring locks, or reconciling. Unknown flags and all positional arguments fail before daemon startup. Running `wtsd` with no arguments preserves foreground startup and signal shutdown behavior.
- **AC-4 — Ownership-aware attach:** `wts attach` snapshots the dedicated socket, locates the selected repository’s session by complete ownership metadata, rechecks that metadata by stable session ID immediately before attaching, and attaches by ID. It refuses expected-name collisions with foreign/untagged sessions and suggests an explicit `wts reconcile --repo-id <id>` when no owned session exists. It never reconciles implicitly.
- **AC-5 — Contextual repository inference:** Repository-scoped commands distinguish: no registrations, an unregistered current Git repository, a directory outside registered worktrees, ambiguous matches, and failed/incomplete inspection. Messages include the exact appropriate `wts repo add`, primary-worktree registration, or `--repo-id` recovery step. Explicit `--repo-id` remains deterministic and omission still infers from the current registered primary or linked worktree.
- **AC-6 — Actionable human status:** Healthy repositories retain a concise one-line summary. Repositories requiring attention automatically show sorted, indented diagnostics for snapshot errors, conflicts, excluded/reported worktrees, prunable records, and failed actions with safe next steps. `--verbose` shows full human details. `--verbose` and `--json` are rejected together.
- **AC-7 — Reliable status automation:** `status --check` is combinable with human or JSON output, prints the complete report, and exits nonzero when any selected repository health is not `healthy` or any global diagnostic is `warning`/`error`; plain `status` remains reporting-only unless status cannot be produced. `unsupported` LaunchAgent state on non-macOS and `not_installed` are neutral; installed-but-stopped is a warning and unavailable/error state is an error. Status JSON follows the canonical version-2 schema in Wire Contracts, never serializes internal structs or raw `launchctl` output, correctly marks desired worktrees eligible, and has golden/contract tests.
- **AC-8 — Precise setup outcomes:** Manual setup reports `completed`, `skipped`, or `failed` with the worktree path where relevant. Skip reasons distinguish disabled policy, no configured actions, and already attempted (with `--rerun` guidance). Skips exit zero, execution failures exit nonzero, and stable internal reason codes are separate from human prose. Launch behavior remains consistent and never prints `launch_command`.
- **AC-9 — Consistent partial-success output:** Every mutating workflow returns non-empty output after any committed mutation followed by a later failure. Worktree create/remove, branch deletion, unregister, cleanup, reconcile, setup/launch, and LaunchAgent update/lifecycle paths report what changed, what failed, whether retry is safe, and an exact recovery command where one exists. Tests inject failures after mutation boundaries. The existing `(output, error)` controller contract remains.
- **AC-10 — Recoverable config editing:** `wts config edit` opens a private temporary copy of the existing raw bytes without requiring the live file to decode or validate. A successful editor exit atomically writes the exact edited bytes with private permissions, then validates. A valid edit reports success; an invalid edit remains live, reports `configuration updated but is invalid: <cause>`, exits nonzero, and tells the user to rerun edit. Editor failure or cancellation leaves the live file unchanged. Other commands continue to reject invalid configuration, and older versions still require explicit `config refresh`.
- **AC-11 — Useful bounded tool errors:** Failed Git and tmux operations include at most a 4 KiB, UTF-8-safe, whitespace-trimmed output tail by default, with a truncation marker when needed. A central sanitizer redacts URL userinfo, authorization/token/password-like values, and known sensitive patterns. Errors never include argv or configured launch commands. Arbitrary setup-command output remains hidden.
- **AC-12 — Predictable worktree lookup:** Remove, setup, and launch resolve `<path-or-branch>` by exact branch first, then by an existing path canonicalized relative to the current directory. Absolute, relative, and symlinked paths resolve to the enumerated worktree; missing paths fall through to the existing not-enumerated error. Primary-worktree and branch-safety checks remain unchanged.
- **AC-13 — Inspectable reconciliation:** `wts reconcile --help` prominently explains that path drift repair uses `respawn-window -k` and can terminate pane processes. Normal reconcile lists sorted operation outcomes, identifying creates, repairs, removals, failures, and which repairs respawned a pane. `--dry-run` supports current-repository, `--repo-id`, and `--all` selection, uses the same pure plan as apply, and performs no tmux mutation, action execution, provenance/ledger write, or cleanup. Apply retains operation locking, complete-snapshot deletion gates, and stable-ID ownership rechecks.
- **AC-14 — Registration allowlist opt-out:** `wts repo add --no-default-allowed-roots` registers with only the selected creation root in the complete repository allowlist. It conflicts with any `--allowed-worktree-root`. Existing no-flag inheritance and explicit allowed-root replacement semantics remain unchanged.
- **AC-15 — Focused repository root management:** The CLI provides `wts repo roots show`, `set-creation <path>`, `add-allowed <path>`, and `remove-allowed <path>`, each accepting optional `--repo-id` with current-worktree inference. Mutations run under the operation lock, canonicalize existing paths, deduplicate roots, preserve repository identity, and save atomically. Setting creation automatically permits that root and retains the former root in the allowlist. Removing the active creation root is rejected. Removing any root requires a complete fresh Git snapshot and is rejected when an enumerated live worktree still depends on that root; nested remaining allowed roots are handled component-safely. Root changes do not implicitly reconcile and output the explicit next command.
- **AC-16 — Focused doctor command:** `wts doctor` is non-mutating and works even when configuration is absent or invalid. It emits the fixed ordered checks and canonical version-1 schema in Wire Contracts, with statuses `ok`, `warning`, `error`, or `skipped`. Missing config/state/ledger/provenance and an absent dedicated tmux server are normal fresh-install states; no registrations and installed-but-stopped are warnings; missing tools, invalid/unreadable config or state, unavailable registered roots, ownership conflicts, and plist XDG drift are errors. Checks that require a valid config are `skipped` when syntax/version validation fails, while independent tool/path/launchd checks still run. `doctor --json` emits the complete report. Warnings exit zero; any error prints the complete report and exits nonzero.
- **AC-17 — Documentation, portability, and regression safety:** README, design, LaunchAgent guide, example plist, and command help accurately describe all changed behavior and explicit recovery steps. Race-enabled unit/integration tests, lint, formatting, vulnerability audit, root repository checks/builds, Linux cross-builds, CLI smoke tests, XML/plist validation, and a final review pass succeed. Real `launchctl` behavior is either manually smoke-tested on macOS with results recorded or explicitly reported as an unverified platform-specific gap.

## Non-Goals / Out of Scope

- Do not auto-register repositories, prompt interactively for registration, or add `worktree create --register`.
- Do not make `repo add` or `attach` reconcile implicitly.
- Do not add focused CLI setters for action arrays or setup/launch policies; complex behavior remains in JSON configuration.
- Do not change config schema version 3 or add a migration.
- Do not add a Linux service manager; `wtsd` remains a foreground daemon for external supervision outside macOS.
- Do not convert every mutating command to a new structured result framework or add JSON output to all commands in this effort.
- Do not require approval before daemon reconciliation, disable path-drift repair, weaken tmux ownership checks, or weaken complete-snapshot deletion gates.
- Do not expose setup subprocess output, launch command text, command argv (including setup executable names), secrets, or unbounded captured output. Human/JSON status must sanitize both new and legacy persisted action errors before rendering.
- Do not preserve the status JSON version-1 shape under version 2; the explicit version bump is the compatibility boundary.

## Constraints

- Use test-driven development for each meaningful behavior change: establish focused failing tests before implementation and keep fixes bounded.
- Preserve argv-based subprocess execution, finite deadlines, process-tree cancellation for noninteractive commands, interactive process-group behavior, bounded capture, and private atomic state/config writes.
- Every mutation of an existing tmux target must be authorized by full metadata and rechecked by stable ID immediately before each subprocess-level mutation, including each rename, respawn, metadata write, kill, window creation in an existing session, and each of launch’s two `send-keys` calls. Tests must change ownership between sub-mutations. Newly created IDs may be initialized/tagged and cleaned up only through the existing captured-ID rules. Attach is not a mutation but adopts the same recheck boundary.
- Destructive reconciliation fails closed on incomplete Git or tmux snapshots. Root removal itself mutates only validated config and requires a complete fresh Git snapshot proving every live worktree remains covered; any later daemon/manual reconciliation takes and validates its own Git/tmux snapshots. Dry-run output is advisory and cannot authorize a later apply.
- Global root defaults continue to be copied at registration; later global changes never alter existing repositories.
- Invalid config saves through `config edit` are an explicit UX decision. The command must be honest that the saved config is invalid, and the daemon must continue pausing mutations while config is invalid.
- Prefer standard-library parsing, sorting, redaction, path handling, and JSON/XML support. Add no dependency unless the standard library is demonstrably insufficient.
- Preserve `wts worktree` without aliases and continue using optional `--repo-id` for repository selection.
- Do not push. Follow repository commit/checkpoint conventions only when execution is explicitly requested.

## Chosen Approach

Deliver the work in coherent vertical groups while retaining the current architecture: Cobra remains a thin request adapter; service methods own workflows; package clients own subprocess details; pure reconciliation planning remains separate from mutation. Add typed report DTOs only where a public, versioned machine contract exists (`status` and `doctor`), and keep the existing output-before-error controller behavior for human partial-success reporting.

First harden executable and LaunchAgent boundaries, because their current contracts can start or restart processes unexpectedly. Next improve repository targeting and root management, then establish shared observation/reporting used by status and doctor. Add precise action/lifecycle outputs and config recovery, then expose reconcile plan/outcome detail. Finish by updating all user-facing docs and running the complete portability/security verification matrix.

## Design Decisions

- **D1 — Registration stays explicit:** Context detection supplies exact recovery commands but never persists a registration implicitly.
- **D2 — LaunchAgent lifecycle is stateful and truthful:** Keep `KeepAlive`; implement stop/start with unload/load and expose restart explicitly. Installed-but-stopped is a first-class status.
- **D3 — Plist captures effective XDG homes:** Persist absolute effective homes at install time; rerunning install is the update mechanism.
- **D4 — Attach is read-only and ownership-aware:** Resolve and recheck the owned session by metadata/ID; never attach to a name collision and never reconcile automatically.
- **D5 — Human reports use progressive disclosure:** Healthy status remains terse; attention states expand automatically; `--verbose` is exhaustive and `--check` supplies automation semantics.
- **D6 — Public JSON has package-owned DTOs:** Status moves to version 2 and doctor starts at version 1. Internal Git/tmux structs and raw `launchctl` text are not public schemas.
- **D7 — Setup skips are successful, explicit outcomes:** Disabled, unconfigured, and already-attempted states are visible but return zero, matching launch.
- **D8 — Partial success uses the existing interface:** Return completed-change output together with a non-nil error; do not introduce a broad result abstraction.
- **D9 — Config edit saves before validation:** Editor success commits exact bytes atomically, even when invalid; post-save validation determines the exit status and message.
- **D10 — Failure output is small and sanitized:** Use a 4 KiB tail with central redaction for Git/tmux only; never include argv or launch commands.
- **D11 — Branch names win lookup ambiguity:** Exact branch first, then canonical existing path.
- **D12 — Reconcile preview and apply share one planner:** Dry-run never mutates; apply independently re-snapshots/revalidates under the operation lock and does not consume a prior preview as authorization.
- **D13 — Root changes are narrow and guarded:** Focused commands mutate only creation/allowed roots, preserve identity, and refuse removal when current Git state is incomplete or still depends on a root.
- **D14 — Doctor complements status:** Doctor evaluates whether the installation can operate, including invalid config; status describes managed repository state.

## Wire Contracts

### Status JSON version 2

The canonical top-level shape is:

```json
{
  "version": 2,
  "repositories": [],
  "daemon": { "state": "unsupported" },
  "diagnostics": []
}
```

Each repository object contains all of these keys: `id`, `health`, `diagnostics`, `desired_worktrees`, `actual_managed_windows`, `conflicts`, `reported_worktrees`, `prunable_worktrees`, and `action_failures`. Arrays are always encoded as `[]`, never `null`.

- `health`: `healthy`, `attention`, `degraded`, or `conflict`, with precedence `conflict` > `degraded` > `attention` > `healthy`.
  - `conflict`: one or more ownership/naming conflicts.
  - `degraded`: an error diagnostic or incomplete/unavailable required snapshot/ledger.
  - `attention`: observation is complete but warnings, reported/excluded worktrees, prunable records, or action failures require review.
- A diagnostic contains `severity` (`warning` or `error`), stable `code`, sanitized `message`, and optional `recovery`.
- A desired worktree contains `path`, `head`, optional `branch`, `detached`, optional `locked`, `identity`, and `eligible` (true for every member of this array).
- An actual managed window contains `id`, `name`, `path`, `role`, and `identity`; internal metadata structs are not serialized.
- A reported worktree and a prunable worktree each contain `path` and stable `reason`.
- An action failure contains `worktree_identity`, optional `worktree_path`, `action` (`setup` or `launch`), UTC RFC3339Nano `attempted`, stable `error_code`, and sanitized `message`. It never contains the encoded ledger key, argv, executable name, raw output, or launch command.
- `conflicts` is a sorted string array retained as a direct operator-facing summary; machine diagnosis uses stable diagnostic codes.
- `daemon.state` is one of `running`, `stopped`, `not_installed`, `unsupported`, or `unavailable`, with optional sanitized `message`. `daemon` is always present.
- Top-level diagnostics cover global ledger/daemon/observation issues. Repository health does not absorb unrelated global diagnostics.

Stable status diagnostic codes are restricted to: `git_snapshot_failed`, `git_snapshot_incomplete`, `tmux_snapshot_failed`, `tmux_snapshot_incomplete`, `action_ledger_unavailable`, `session_name_conflict`, `multiple_owned_sessions`, `worktree_missing`, `worktree_identity_unavailable`, `worktree_outside_allowed_roots`, `worktree_prunable`, `setup_failed`, `launch_failed`, `daemon_stopped`, and `daemon_unavailable`. Additions require a deliberate status schema revision or documented backward-compatible extension; source packages must emit typed findings rather than renderers parsing human messages.

`reported_worktrees.reason` is one of `missing`, `git_identity_unavailable`, or `outside_allowed_roots`. `prunable_worktrees.reason` is `git_metadata_prunable`. `action_failures.action` is `setup`, `launch`, or `unknown` for an unclassifiable legacy/stale digest; `error_code` is one of `setup_execution_failed`, `launch_delivery_failed`, `ledger_persist_failed`, or `legacy_failure`. Human messages may add sanitized context but do not define machine semantics.

Sort repositories by ID; diagnostics by severity/code/message; desired/reported/prunable worktrees by path then identity/reason; actual windows by stable ID; conflicts lexically; and action failures by action/worktree identity/attempt time. Optional strings are omitted when empty. `status --check` writes the same complete human or JSON document to stdout, then returns a generic check failure on stderr/nonzero without corrupting JSON.

### Doctor JSON version 1

The canonical shape is:

```json
{
  "version": 1,
  "checks": [
    {
      "id": "tools.git",
      "status": "ok",
      "summary": "Git is available",
      "details": [],
      "recovery": ""
    }
  ]
}
```

Every check always contains `id`, `status`, `summary`, `details` (an array), and `recovery` (possibly empty). Status is `ok`, `warning`, `error`, or `skipped`. The fixed order is:

1. `tools.git`, `tools.tmux`
2. `paths.resolved`
3. `config.file`, `config.syntax`, `config.version`, `config.runtime`
4. `state.directory`, `state.action_ledger`, `state.provenance`
5. Per repository sorted by ID: `repository.<id>.primary`, `.common_git`, `.creation_root`, one `.allowed_root.<index>` per configured order, and `.git_snapshot`
6. `tmux.snapshot`, then `tmux.ownership.<repo-id>` sorted by repository ID
7. On macOS: `launchd.plist`, `launchd.lifecycle`, `launchd.environment`; elsewhere one `launchd.support` check with `skipped`

An absent config file is `ok` with defaults active; an empty registry is `warning`. Absent state directory/ledger/provenance and absent dedicated tmux server are `ok` fresh-install states. Invalid syntax makes version/runtime/repository checks `skipped`; an unsupported/old version makes runtime/repository checks `skipped`; runtime validation failure makes dependent repository checks `skipped`. Independent tools, paths, state, tmux, and launchd checks continue. Missing tools, unreadable/unsafe state, invalid config, unavailable registered roots, ownership conflicts, and plist XDG drift are errors. A present but stopped LaunchAgent is a warning; absent installation is `ok`. `doctor` exits nonzero iff at least one check is `error`.

## Implementation Notes

### 1. CLI and daemon process boundaries

- Extend `worktree-sync/cmd/root.go` and `cmd/root_test.go` with the settled command/flag surface, argument validation, mutual exclusions, help text, and request forwarding.
- Add a small reusable `wtsd` command constructor under `worktree-sync/cmd/` that accepts an injected run function. Keep `worktree-sync/cmd/wtsd/main.go` limited to signal context, environment wiring, logging, and command execution. Test that help and invalid arguments never call the run function.
- Preserve `SilenceUsage`, command-specific argument errors, and output-before-error behavior.

### 2. LaunchAgent lifecycle and environment

- Evolve `worktree-sync/internal/config.Paths` or add narrow helpers so launchd receives the effective XDG home directories directly rather than reconstructing them inconsistently.
- Update `worktree-sync/internal/launchd/launchd.go` and tests to render escaped XDG variables, model installed/loaded state, implement the exact idempotent transitions in AC-1, and keep operations scoped to `dev.agent-tools.worktree-sync`. Treat only the scoped `launchctl` not-found result as unloaded; any other observation failure is `unavailable`, not `stopped`.
- Serialize the fixed per-user LaunchAgent label with a lifecycle lock independent of XDG state, at `~/Library/Application Support/worktree-sync/launchd.lock` in a private `0700` directory. Acquire this fixed lock before any XDG-scoped operation lock and hold it through lifecycle observation, plist writes, launchctl changes, rollback, and final reporting. This prevents two `wts` processes using different XDG homes from interleaving changes to the same label/plist. Test same-XDG and cross-XDG concurrent lifecycle requests.
- Before changing an existing plist or job, classify scoped `launchctl print` as loaded, recognized not-found/unloaded, or unavailable. Any unavailable/unknown result aborts without changing plist or job. A new install also requires a known not-found state so it cannot overwrite an unobservable loaded label.
- Use this deterministic install/update rollback policy:
  - New install: atomically write the private `0600` plist, then bootstrap. If bootstrap fails, remove the newly written plist and report that installation failed with no job installed; if cleanup also fails, report both effects as partial success.
  - Update with prior plist: retain its bytes, mode, and loaded/unloaded state; atomically write the replacement; boot out the old loaded job if necessary; bootstrap the replacement. If replacement bootstrap fails, atomically restore the prior plist and restore its prior loaded/unloaded state. Report `update failed; previous LaunchAgent restored` when rollback succeeds. If any rollback step fails, report the exact installed/loaded state known and recovery commands.
  - Never report `installed`, `started`, or `restarted` before final state observation confirms the corresponding loaded state.
- Wire restart and typed lifecycle status through `worktree-sync/internal/service/service.go` and CLI help.

### 3. Repository context, worktree targeting, and attachment

- Add a narrow Git context probe in `worktree-sync/internal/git/` that discovers the canonical current common Git directory and primary worktree without applying primary-registration restrictions. Use it to distinguish unregistered, outside-Git, linked-worktree, and registered contexts without per-repository subprocess fan-out.
- Retain a safe fallback/diagnostic path when current context cannot be established; do not silently turn failed inspection into “unregistered.”
- Refactor worktree lookup in `internal/service` into one branch-first/canonical-path helper shared by remove/setup/launch.
- Extend `worktree-sync/internal/tmux/` with session ownership recheck and attach-by-ID behavior. In service attach, inspect name collisions as well as owned metadata before entering interactive tmux.

### 4. Registration defaults and existing root mutations

- Add `NoDefaultAllowedRoots` to registry add options and enforce CLI conflict with explicit allowed roots before service execution. Preserve automatic inclusion of the creation root.
- Add focused registry/service helpers for showing and mutating repository roots. Keep mutation orchestration under `operation.lock`, use `config.CanonicalExisting` and component-aware containment, validate the complete resulting config, and persist with existing private atomic save.
- Before removing an allowed root, take a complete fresh Git snapshot and reject any enumerated non-primary worktree that would cease to be beneath at least one remaining allowed root. This is more precise than rejecting every worktree beneath the removed root when a nested/overlapping retained root still covers it. The root command itself does not invoke reconciliation; after save, report `the running daemon will reconcile automatically; otherwise run wts reconcile --repo-id <id>`. The daemon’s config watch remains intentionally active and independently fail-closes on incomplete Git/tmux snapshots.
- Keep old creation roots allowed after `set-creation`; users may remove them separately after moving/removing dependent worktrees.

### 5. Shared observation model, status, and doctor

- Move status collection and rendering out of the monolithic service method into focused files under `worktree-sync/internal/service/` (or a narrow internal reporting package only if reuse clearly warrants it). Collection must preserve actual diagnostic causes instead of collapsing them into counts or silently dropping ledger errors.
- Define sorted public status-v2 and doctor-v1 DTOs separately from internal Git/tmux/state types. Use stable enums/reason codes, typed daemon state, and deliberately redacted diagnostic strings. Add golden JSON fixtures or exact schema assertions.
- Define status health precedence consistently: conflict/degraded conditions and any warning requiring operator action produce a non-healthy result; `--check` fails for any selected non-healthy report after printing it. Plain status reports health without using non-healthy state as a command execution error.
- Build doctor as an explicit fixed checklist, not a plugin framework. It must collect independent checks where possible so one invalid config or missing executable does not suppress the rest. It must not create directories, write state, reconcile, run setup/launch, or mutate launchd/tmux.
- Reuse safe observation helpers between status, cleanup reporting, and doctor only when their snapshot/validation semantics match; do not accidentally let a diagnostic snapshot authorize later mutation.

### 6. Action outcomes, partial success, config editing, and diagnostics

- Extend `worktree-sync/internal/actions.Result` with stable outcome/reason information for setup matching the existing launch behavior. Setup failures and persisted action results use action index/type plus stable reason codes, never executable names, argv, launch commands, or raw subprocess output. Keep automatic action ledger semantics unchanged, but sanitize legacy `ActionResult.Error` values at every human/JSON rendering boundary.
- Audit every mutation boundary in `worktree-sync/internal/service/service.go`, reconciliation execution, and LaunchAgent management. Preserve completed output across later errors, aggregate independent failures where safe, and make messages state safe retry behavior.
- Rework `config edit` to copy raw live bytes (or generate defaults when absent), invoke the editor, atomically publish exact bytes after successful editor exit, then call normal load/validation for feedback. Update the existing test that currently expects invalid edits to leave the live file unchanged.
- Add a central bounded diagnostic formatter near `worktree-sync/internal/exec/` or the Git/tmux clients. Sanitize before formatting, take the final 4 KiB without breaking UTF-8, and mark truncation. Update existing non-disclosure tests to assert credential/launch-command redaction and boundedness; setup output remains undisclosed.

Use this mutation-boundary matrix to prove AC-9. Rows with no post-commit step need an atomicity test but cannot produce partial success:

| Workflow                                     | Committed boundary                         | Injected later failure / required output                                                                                                          |
| -------------------------------------------- | ------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `config refresh`, `repo add`, root mutations | Atomic config save is final                | Save failure leaves prior config; no partial-success state                                                                                        |
| `config edit`                                | Raw atomic replacement                     | Post-save validation failure reports saved-invalid state and repair command                                                                       |
| `repo remove`                                | Each owned tmux kill                       | Later kill/config-save failure reports removed count, registration retained, safe retry                                                           |
| `worktree create`                            | Git worktree add                           | Identity/provenance/reconcile failure reports created path and next recovery action                                                               |
| `worktree remove`                            | Git worktree remove                        | Provenance-save/branch-delete failure reports worktree removal and branch/provenance state                                                        |
| `worktree setup`                             | Each copy/setup side effect                | Later action/ledger-save failure reports failed action index/type and warns side effects may exist                                                |
| `worktree launch`                            | Literal `send-keys`, then Enter            | Ownership/Enter/ledger-save failure reports exactly whether text was sent, Enter was sent, and whether attempt was recorded, without command text |
| `reconcile`                                  | Each tmux/action mutation and ledger prune | Later operation failure reports per-operation successes/failures and safe rerun guidance                                                          |
| `cleanup --prune-git`                        | Git prune                                  | Later resnapshot/second cleanup-mode failure preserves prune result and marks verification incomplete                                             |
| `cleanup --remove-orphaned-tmux`             | Each owned kill                            | Later ownership/kill failure reports removed count and safe rerun guidance                                                                        |
| `daemon install/update`                      | Plist replacement, bootout, bootstrap      | Follow deterministic rollback policy; otherwise report exact known plist/load state                                                               |
| `daemon uninstall`                           | Bootout, then plist removal                | Removal failure reports unloaded-but-installed state; bootout failure does not remove plist blindly                                               |
| `daemon start/stop/restart`                  | Each bootout/bootstrap                     | Later failure reports observed final state and exact safe retry/recovery command                                                                  |

### 7. Reconcile planning and operation outcomes

- Keep `reconcile.Build` as the single pure planner. Introduce a read-only service path that snapshots and renders the plan without invoking executor mutation, actions, provenance writes, ledger pruning, or cleanup.
- Enrich repair operations or outcomes enough to distinguish name/metadata repair from path repair that invokes `respawn-window -k`. Do not label every repair as process-terminating.
- Extend executor reports with sorted per-operation success/failure outcomes so normal reconcile output describes applied effects rather than merely echoing proposed operations. Refactor compound tmux operations so the expected full metadata is re-read by stable ID immediately before every subprocess-level mutation (rename, respawn, option write, kill, create-in-session, and both launch `send-keys` calls), with injected ownership changes tested between substeps. Preserve supplied snapshot reuse and complete-snapshot deletion checks.
- Acquire the operation lock for dry-run snapshot coherence, but never imply that a dry-run reserves or approves a future apply. Normal apply takes fresh snapshots in its own invocation.
- Ensure daemon logging remains bounded and useful when reconcile summaries become more detailed.

### 8. Suggested execution checkpoints

1. **Daemon boundary:** wtsd argument handling; launchd XDG/lifecycle/restart/status/rollback; focused tests plus README/LaunchAgent guide/example/help updates for this slice.
2. **Repository UX:** contextual inference, ownership-aware attach, path lookup, registration opt-out, and focused roots commands; focused tests plus README/design/help updates.
3. **Status observability:** typed status-v2 collection/rendering, `--verbose`/`--check`, action-error sanitization, and cleanup report reuse; schema/help/docs updated with the implementation.
4. **Doctor:** fixed-check collector, doctor-v1 human/JSON rendering, invalid-config independence, and platform semantics; schema/help/docs updated with the implementation.
5. **Action/config/diagnostic honesty:** setup outcomes, owning-command partial-success boundaries, save-then-validate config editing, and sanitized Git/tmux tails; focused tests and recovery docs updated together.
6. **Reconcile honesty:** dry-run/apply separation, operation effects, per-submutation ownership rechecks, and reconcile-owned partial-success paths; focused tests and safety/help/design docs updated together.
7. **Closure:** cross-slice integration tests, final documentation/example consistency, portability/audit matrix, manual macOS evidence or explicit gap, and final independent review.

Keep exactly one checkpoint in progress, run its focused deterministic tests before moving on, and commit only if the autonomous execution workflow requires checkpoints or the user explicitly asks. Checkpoint 7 is not permission to defer user-facing documentation from checkpoints 1–6.

## Documentation Impact

Update all affected user-facing sources in the same checkpoint as behavior:

- `worktree-sync/README.md`
  - Five-minute walkthrough and unregistered-repository troubleshooting.
  - Existing repository root management and allowlist opt-out.
  - Setup outcomes, status detail/check semantics, doctor, reconcile dry-run and pane-termination warning.
  - Config edit’s save-then-validate behavior and invalid-config recovery.
  - Foreground `wtsd` help and LaunchAgent start/stop/restart/XDG behavior.
  - Command reference and automation/JSON schema versions.
- `worktree-sync/DESIGN.md`
  - Owned stable-ID attach boundary, contextual identity inference, status/doctor responsibilities, partial-success convention, root mutation safety, dry-run versus apply, and deliberate invalid-config edit semantics.
- `worktree-sync/docs/launchd.md`
  - Installed/running/stopped states, real lifecycle commands, restart, persisted XDG homes, reinstall/update behavior, logs, and recovery from failed bootstrap.
- `worktree-sync/examples/launchd/dev.agent-tools.worktree-sync.plist`
  - Match generated PATH/XDG environment and lifecycle-relevant keys; validate as XML/plist.
- Cobra long help/examples in `worktree-sync/cmd/root.go` and the new wtsd command.

Do not add a new standalone documentation file; existing docs cover these topics.

## Testing / Verification

- **V1 — CLI contracts (AC-3, AC-6–8, AC-13–16):** Add command tests for every new command/flag, mutual exclusion, selector forwarding, help warning/example, setup messages, `--check` output-before-error, doctor JSON, and wtsd behavior. Inject the wtsd run function to prove help/invalid input never calls it, no arguments call it exactly once, and context cancellation reaches it. Run `go -C worktree-sync test ./cmd -count=1`.
- **V2 — LaunchAgent behavior (AC-1–2, AC-9):** Fake-runner tests assert exact scoped launchctl observation/lifecycle commands for every AC-1 transition, fixed lifecycle-lock serialization across same and different XDG homes, abort-with-no-change on unknown launchctl state, restart, plist XDG/XML escaping, installed plist mode `0600`, deterministic failed-bootstrap rollback/partial reporting, timeout handling, and non-macOS states. Validate generated and example plists with XML parsing; on macOS also use `plutil -lint`.
- **V3 — Repository targeting and roots (AC-4–5, AC-12, AC-14–15):** Unit tests cover current primary/linked/unregistered/outside-Git contexts, snapshot/probe failures, foreign name collision, ownership change before attach, stable-ID attachment, branch/path ambiguity, relative/symlink paths, default opt-out conflicts, root deduplication, creation-root retention, incomplete snapshots, nested roots, dependent worktrees, and atomic save failure.
- **V4 — Status and doctor schemas (AC-6–7, AC-16):** Golden/exact tests enforce the Wire Contracts fields, enum values, always-array rules, optional fields, timestamps, sort keys, and stdout/stderr/exit semantics. Cover every health/check status, diagnostic causes, ledger corruption and sanitized legacy action failures, desired eligibility, typed daemon state, `--verbose`, `--check`, invalid/missing/old config, absent state and tmux server, empty registry, foreign collisions, unavailable roots, XDG/plist drift, warnings versus errors, skipped dependent checks, and zero mutations.
- **V5 — Actions, config, diagnostics, and partial success (AC-8–11):** Use the mutation-boundary matrix, explicitly marking atomic-final rows, to cover every mutator. Tests include setup skip/failure reason codes, post-create provenance failures, post-remove branch/provenance failures, partial unregister and cleanup, reconcile partial apply, LaunchAgent failures, and output/recovery text. Config-edit tests prove invalid existing bytes reach the editor byte-for-byte, absent config initializes defaults, editor failure/cancellation leaves live bytes unchanged, valid and invalid editor output is saved exactly with `0600`, invalid save prints output then exits nonzero, and a second edit can repair it. Diagnostic tests cover bounded UTF-8 tails, truncation markers, credential redaction, sanitized legacy ledger errors, and argv/launch/setup non-disclosure.
- **V6 — Reconcile preview/apply (AC-13):** Planner/executor tests prove dry-run and apply derive the same sorted plan from the same snapshots, distinguish respawning repairs, and prove dry-run invokes no mutator/action/ledger path. Inject ownership changes between each compound sub-mutation, including launch’s two sends and repair rename/respawn/metadata steps, and prove the next subprocess is refused. Existing complete-snapshot deletion gates remain passing.
- **V7 — Real integration (AC-4–9, AC-12–16):** Extend tagged integration tests with real Git/tmux coverage for contextual linked-worktree inference, status/report details, JSON contracts, setup outcomes, stable-ID attach targeting where safely testable, path lookup, root commands, reconcile dry-run no-op versus apply, partial/degraded reporting, and doctor read-only behavior. Continue using unique sockets and temporary XDG homes.
- **V8 — Module and repository quality gates (AC-17):** Run:
  - `ASDF_GOLANG_VERSION=1.25.11 make -C worktree-sync audit`
  - `ASDF_GOLANG_VERSION=1.25.11 WTS_INTEGRATION=1 make -C worktree-sync integration-test`
  - `ASDF_GOLANG_VERSION=1.25.11 make check`
  - `ASDF_GOLANG_VERSION=1.25.11 make build`
  - `ASDF_GOLANG_VERSION=1.25.11 GOOS=linux GOARCH=amd64 go -C worktree-sync build ./cmd/wts ./cmd/wtsd`
  - `npx prettier --check worktree-sync/README.md worktree-sync/DESIGN.md worktree-sync/docs/launchd.md worktree-sync/examples/launchd/dev.agent-tools.worktree-sync.plist`
  - `git diff --check` and `git status -sb`
- **V9 — CLI/config smoke (AC-1–3, AC-5–7, AC-10, AC-13–16):** In temporary XDG homes, build binaries and exercise fresh/no-repo errors, explicit registration, default opt-out, roots show/update, invalid config edit then repair, status human/JSON/check, reconcile dry-run, doctor human/JSON, wtsd help/unknown args, and Linux LaunchAgent rejection. Assert no command exposes `launch_command`.
- **V10 — macOS lifecycle evidence (AC-1–2, AC-17):** When a macOS GUI launchd domain is available, smoke install → status → stop → stopped status → start → running status → restart → logs → uninstall, with custom temporary XDG homes and verification that the daemon reads the same config. If unavailable, report this exact gap; fake-runner and plist tests are necessary but not represented as real launchctl proof.
- **V11 — Final review (all ACs):** After deterministic checks pass, dispatch independent correctness/safety, CLI/automation, test-quality, and documentation reviewers. Resolve every blocker/important finding or report a concrete blocker with evidence. Map final command/artifact evidence to AC-1 through AC-17 before declaring completion.

## Risks and Mitigations

- **Launchctl semantics differ from assumptions:** Use exact scoped commands in fakes, typed installed/loaded state, idempotent output, rollback or explicit partial recovery on bootstrap failure, and real macOS smoke evidence when available.
- **Persisted XDG homes become stale:** `daemon install` is explicitly install-or-update; status/doctor compare plist values to current effective paths and tell users to reinstall.
- **Invalid config edit intentionally causes downtime:** Atomic writes prevent torn files; post-save validation returns nonzero and explicit repair guidance; daemon continues its existing fail-closed mutation pause.
- **Status JSON changes break consumers:** Bump to version 2, own the DTO independently of internal types, sort all collections, and add contract/golden tests. Document the compatibility boundary.
- **Redaction cannot prove arbitrary output secret-free:** Limit default raw tails to Git/tmux, sanitize centrally, omit argv, keep setup output hidden, and test common credential forms and launch-command non-disclosure.
- **Dry-run becomes mistaken for approval:** Document it as advisory, acquire a lock only for its own snapshot coherence, and require apply to re-snapshot/revalidate independently.
- **Root removal causes accidental tmux deletion:** Require a complete fresh Git snapshot and prove all live worktrees remain covered by retained roots before saving. Do not reconcile implicitly.
- **Doctor grows into a framework:** Keep an explicit fixed checklist and typed report; do not add plugin abstractions or automatic fixes.
- **Cross-cutting scope creates regression risk:** Implement in vertical checkpoints with focused RED/GREEN tests, run deterministic checks at every checkpoint, and retain the established safety invariants as explicit review criteria.

## Assumptions

- Git and tmux versions remain governed by the existing project requirements; doctor reports discovered versions but does not introduce new minimums.
- A 4 KiB diagnostic tail is sufficient for actionable Git/tmux failures and can be adjusted later without changing configuration or durable state.
- The existing action ledger and provenance formats remain unchanged; only reporting and error handling change.
- Status JSON version 1 has no requirement for a compatibility flag once version 2 is documented and emitted.
- Real macOS launchctl smoke testing may be unavailable in the execution environment; this is an explicitly reportable verification gap, not permission to claim it passed.

## Handoff Summary

Implement this plan as a TDD-driven set of vertical checkpoints. Preserve explicit registration, config v3, private atomic persistence, complete-snapshot deletion gates, and full metadata/stable-ID tmux authorization. Do not mark the work complete until every AC has concrete unit/integration/CLI/doc evidence, the full audit/portability matrix passes, and real launchctl testing is either recorded or named as the remaining platform gap.

Suggested autonomous objective:

```text
/goal Implement .plans/2026-07-19-worktree-sync-ux.md. Complete only after AC-1 through AC-17 are satisfied with concrete evidence; preserve all existing safety invariants and report any unavailable real-macOS launchctl verification explicitly.
```
