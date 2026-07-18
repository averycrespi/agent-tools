# Worktree Sync Plan

## Goal

Add a new Go tool, `worktree-sync`, that continuously projects registered Git repository/worktree state into an isolated tmux environment: one session per repository, one stable base window for the primary repository, and one managed window per eligible linked worktree. Humans use the `wts` CLI for registration, worktree lifecycle, status, cleanup, attach, configuration, and daemon management; the portable `wtsd` daemon also notices worktrees created or removed by other host or sandbox processes without requiring an agent API.

## Background / Repo Context

- The existing `worktree-manager` (`wt`) synchronously creates deterministic worktrees and tmux windows, but it has no daemon, worktree enumeration, tmux reconciliation, or safe managed-object metadata. Its behavior and package boundaries are documented in `worktree-manager/DESIGN.md`, `worktree-manager/README.md`, and `worktree-manager/CLAUDE.md`.
- `wt` currently uses lossy branch sanitization and repo basenames for paths/session/window names. `worktree-sync` must not repeat collisions such as `a/b` versus `a-b` or two repositories with the same basename.
- This is a new sibling tool rather than a replacement for `worktree-manager`. Existing `wt` paths and resources must remain untouched unless a user explicitly registers an existing worktree root with `wts`.
- Repo-level tool additions must follow the checklist in root `CLAUDE.md`: add the independent Go module and standard tool files, update the root `Makefile`, `go.work`, `README.md`, root `CLAUDE.md`, and `assets/tool-relationships.svg`, add the `AGENTS.md -> CLAUDE.md` symlink, run `go mod tidy`, and structurally and visually verify the SVG.
- Use the repository’s established thin-Cobra-command and injected-runner patterns, but use context-aware subprocess execution with finite deadlines. `local-gomod-proxy/internal/exec/exec.go` is the better daemon runner model than the non-context-aware runner in `worktree-manager`.
- Reuse the long-running process patterns in `mcp-broker/cmd/mcp-broker/serve.go`: structured stderr logging, signal cancellation, bounded shutdown, and deterministic tests. Reuse XDG configuration/state conventions from `mcp-broker/internal/config/` and `local-gomod-proxy/internal/state/`.
- Follow `mcp-broker/docs/launchd.md` and `mcp-broker/examples/launchd/` for user LaunchAgent shape and documentation, but implement installation and lifecycle commands in `wts` rather than requiring manual plist rendering.
- There is no Go standard-library filesystem notification API. A narrow dependency such as `fsnotify` is justified for event nudges; correctness must not depend on notification delivery.

## Acceptance Criteria

### Tool and configuration

- **AC-1:** `worktree-sync/` is an independent Go module that builds and installs both `wts` and `wtsd`, has the repository-standard Makefile/lint/docs layout, and is included in every root-level tool index and architecture diagram required by root `CLAUDE.md`.
- **AC-2:** `wts repo add` registers only an existing primary, non-bare Git worktree; canonicalizes the repository root, common Git directory, and allowed worktree roots; rejects duplicate IDs, duplicate repository identities, linked-worktree registration, and explicitly supplied roots that do not already exist or cannot be resolved safely. When no root is supplied, registration creates the default XDG worktree root with private permissions before canonicalizing it. Repo IDs use a tmux-safe, non-lossy validated character set, default to the repository basename, and require an explicit alternative on collision.
- **AC-3:** Configuration uses the XDG `worktree-sync` namespace, is validated before atomic replacement, and supports global reconcile/debounce/command-timeout defaults plus per-repo ID, primary root, allowed worktree roots, optional launch command, copy/setup actions, and separate explicit-create versus passive-detection policies. The default managed worktree root is under the XDG data directory; any existing root, including a current `wt` root, can be registered explicitly.

### Reconciliation and ownership

- **AC-4:** For each successfully enumerated registered repo, reconciliation derives one complete desired-state snapshot from `git worktree list --porcelain -z`. The primary worktree maps only to the base window. A linked worktree maps to a managed window only when its canonical existing path is component-wise contained under an allowed canonical root. Detached and locked live worktrees are supported; missing/prunable worktrees and worktrees outside allowed roots are reported but do not receive windows.
- **AC-5:** `wtsd` maintains a dedicated `wts` tmux socket and a session named `wts-<repo-id>` for every registered repository. Each session has one tagged base window rooted at the primary repo and one tagged window rooted at each eligible linked worktree. Readable branch/path slugs are used, with a deterministic short suffix only for colliding names; detached worktrees receive a readable deterministic fallback name.
- **AC-6:** Sessions/windows created by the tool carry tmux user-option ownership metadata including a schema version, repository identity, object role, and worktree identity. Reconciliation mutates or kills only objects whose metadata matches the registered repository. Untagged/manual windows are always preserved, even when their names collide with desired managed names. An untagged or foreign-owned session-name collision is surfaced as a conflict and is never adopted or overwritten automatically.
- **AC-7:** After a successful Git and tmux snapshot, reconciliation creates missing managed resources, repairs managed names/cwds/metadata, removes duplicate managed projections, and kills tagged worktree windows whose worktrees are no longer in the eligible desired set. Killing a managed window causes it to be recreated; killing or renaming a scratch window has no effect. A registered repo’s base session/window remains present even when it has no linked worktrees.
- **AC-8:** A failed/partial Git enumeration, config validation, path canonicalization, or tmux listing for a repository causes no destructive reconciliation for that repository. All subprocesses honor context cancellation and finite deadlines. Daemon and explicit reconciliation are serialized across processes, event bursts are coalesced, and only one `wtsd` instance may run for a state directory.
- **AC-9:** `wtsd` performs a full reconciliation at startup and on a configurable interval. Filesystem notifications for the config, registered Git administrative paths, and allowed worktree roots only trigger debounced early full reconciliations. Missed/overflowed watcher events, watcher errors, daemon downtime, and daemon restarts recover through a later full reconciliation.

### Human workflows and safety

- **AC-10:** `wts` provides coherent commands for config inspection/edit/validation; repo add/list/remove; worktree path/create/remove (`rm` alias); attach; human-readable and stable JSON status; explicit reconcile; cleanup reporting/application; and daemon install/uninstall/start/stop/status. Commands infer the repo from the current primary/worktree path where unambiguous and otherwise require a repo ID.
- **AC-11:** `wts worktree create` uses the configured canonical root and collision-safe path, checks out an existing local branch or creates a branch from an optional start point, records the operation as explicit before exposing it to reconciliation, and applies only explicitly enabled setup/launch policies. `wts worktree remove` (alias `rm`) uses Git’s normal safety checks, supports explicit safe/force branch-delete flags, and never deletes a branch unless one of those flags is supplied.
- **AC-12:** Worktrees created by ordinary `git worktree add` under an allowed root are detected without `wts`, MCP, Lima APIs, or sandbox-specific configuration. Their tmux windows appear after an event nudge or periodic scan and disappear after Git no longer enumerates them as eligible.
- **AC-13:** Automatic daemon reconciliation never invokes `git worktree remove`, branch deletion, recursive filesystem deletion, or Git pruning. `wts cleanup` is dry-run reporting only. `wts cleanup --prune-git <repo-id>` explicitly re-enumerates that registered repo under the operation lock and then runs repository-wide `git worktree prune` for records Git still considers prunable; `wts cleanup --remove-orphaned-tmux <repo-id>` re-snapshots tmux and removes only orphaned resources carrying that repo’s valid ownership metadata. The actions may be combined, are never implied by a generic `--apply`, and report the before/after scope. Existing worktree-directory removal remains the explicit responsibility of `wts worktree remove`/`rm` or Git.
- **AC-14:** Setup actions are trusted per-repo configuration, not read from an unregistered repo-local config. Copy actions do not overwrite existing destinations. Setup commands have explicit argv, cwd, environment inheritance/overrides, timeout, and cancellation semantics. Passive actions are disabled by default and run only for repos that separately opt in. A persistent, atomically written action ledger prevents successful or failed actions from rerunning on every reconcile; a new worktree identity, changed action definition, or explicit rerun makes them eligible again. Setup failure is visible in status, does not cause an infinite retry loop, and does not suppress creation of the worktree’s inspection window. Launch runs only when a corresponding managed window is newly created and its explicit/passive policy permits it.
- **AC-15:** Removing a repo through `wts repo remove` stops future management and explicitly removes only that repo’s tagged base/worktree resources. If untagged scratch windows remain, their session is preserved; otherwise the now-empty owned session may be removed. The command never changes Git worktrees or branches.

### Daemon installation and portability

- **AC-16:** The reconciliation/config/Git/tmux/daemon core compiles on macOS and Linux and contains no Lima-specific API, path translation, or assumptions. Documentation states only that sandbox-created worktrees work when the sandbox exposes registered paths transparently and consistently to the host.
- **AC-17:** On macOS, `wts daemon install` idempotently installs a per-user LaunchAgent for the absolute `wtsd` binary path, with an explicit PATH, `RunAtLoad`, `KeepAlive`, and separate logs. Start/stop/status/uninstall affect only the tool’s own launchd label; uninstall does not modify Git or tmux. `wtsd` also runs directly in the foreground on supported Unix systems.

## Non-Goals / Out of Scope

- Replacing or modifying the existing `wt` CLI, config, tmux socket, sessions, or default worktree directory.
- MCP, HTTP, or other agent-specific APIs. Agents use ordinary Git against correctly shared paths.
- Lima integration, host/sandbox path translation, mount management, or sandbox discovery.
- Automatic repository discovery. Only explicitly registered repositories are managed.
- Automatic deletion of Git worktrees, directories, branches, commits, or uncommitted changes.
- Automatic adoption of pre-existing tmux sessions/windows based only on names.
- Windows support, systemd packaging, or other service-manager installers in v1.
- Remote branch discovery/tracking policy beyond ordinary explicit Git start points.
- Arbitrary tmux layout/pane orchestration or ownership of manual scratch windows.

## Constraints

- Use Go 1.25 and keep `worktree-sync` an independent module at `github.com/averycrespi/agent-tools/worktree-sync`.
- Prefer the Go standard library; add dependencies only where the standard library has no suitable behavior. Cobra is established for CLIs, filesystem notification justifies a narrow watcher dependency, and a Unix advisory-lock package is acceptable if required for correct cross-process locking.
- All Git, tmux, launchctl, editor, and configured setup subprocesses go through injected, context-aware runners with finite deadlines. Tests must not require real subprocesses unless explicitly marked as integration tests.
- Thin command wrappers delegate to internal services. Keep config, Git enumeration, tmux operations, reconciliation, daemon scheduling, action execution/state, and launchd integration independently testable.
- Canonicalize paths using filesystem and Git evidence, then use component-aware containment; never use raw string-prefix checks. Registration rejects ambiguous/nonexistent roots rather than storing unverifiable paths.
- Treat user config as trusted host configuration but repository contents and branch names as untrusted data. Never interpolate branch/path/config values into an implicit shell command. Launch text sent to an interactive tmux shell is explicitly documented as trusted configuration.
- Use atomic state/config writes and restrictive permissions. Do not hold a lock while waiting indefinitely on a subprocess.
- Reconciliation is idempotent and convergent, not transactional across Git and tmux. Fail closed for deletion and repair on incomplete snapshots; report degraded state and retry later.
- Preserve existing user work in the repository. Do not commit unless the execution workflow explicitly requires it; never push without explicit user instruction.

## Chosen Approach

Build a new tool suite with a shared internal core and two entry points:

- `wts` is the human control surface. It edits the registry/config safely, creates/removes worktrees explicitly, reports desired/actual/drift state, attaches to tmux, invokes one-shot reconciliation/cleanup, and manages the macOS LaunchAgent.
- `wtsd` is a foreground portable daemon. It owns only scheduling, watcher nudges, a singleton lock, logging, and calls into the same reconciler used by `wts reconcile`.

The desired state comes from Git, not directory scanning: for each registered primary repository, parse a single NUL-delimited porcelain snapshot and filter linked worktrees through canonical allowed-root checks. The actual state comes from the dedicated tmux socket and explicit tmux user-option markers. Reconcile the two only after both snapshots needed for an operation succeed. This makes Git/worktree existence authoritative while retaining a strict ownership boundary around automatic tmux mutations.

Correctness comes from periodic full-state reconciliation. Filesystem events merely enqueue a debounced earlier run. A shared operation lock serializes apply phases across `wtsd`, `wts reconcile`, and explicit `wts` lifecycle commands; a separate singleton lock prevents duplicate daemons. External Git operations remain safe because every apply decision is checked against a fresh complete snapshot and destructive tmux actions fail closed.

Optional setup/launch behavior remains per-repo and policy-separated as clarified. Explicit `wts` creation records provenance while holding the operation lock, preventing the daemon from misclassifying it as passive. Passive actions require explicit opt-in. An action ledger keyed by repository identity, worktree identity, action-definition digest, and trigger class provides once-per-definition semantics without retry storms.

## Design Decisions

- **D1 — Separate tool:** Create `worktree-sync`; do not retrofit `worktree-manager`.
- **D2 — Hybrid control:** Git operations may originate from `wts`, ordinary Git, a human, or an agent. The daemon observes all eligible Git-listed worktrees and repairs tmux drift.
- **D3 — Explicit registry:** Repositories and allowed worktree roots are allowlisted through `wts repo add`; there is no background repo discovery.
- **D4 — Git snapshot is desired state:** Use `git worktree list --porcelain -z`, not filesystem directory enumeration, branch enumeration, or expected-path existence.
- **D5 — Flexible acceptance, canonical creation:** `wts create/path` uses a standard XDG data-root layout, while the reconciler accepts any eligible Git worktree under any registered allowed root.
- **D6 — Stable identities, readable names:** Validated repo IDs identify sessions. Worktree identity is based on canonical repository/worktree Git identity rather than branch title. Names are display projections and may be repaired without losing identity.
- **D7 — Metadata is the ownership boundary:** Dedicated socket isolation is helpful but insufficient. Tmux user options determine which resources the tool may mutate; names alone never authorize mutation.
- **D8 — Base plus linked windows:** The primary repo gets one persistent tagged base window. Only eligible linked worktrees get worktree windows.
- **D9 — Manual tmux state survives:** Untagged windows are outside daemon ownership. Session/window name conflicts fail visibly rather than triggering adoption.
- **D10 — Full scan plus event nudges:** Periodic scans and startup reconciliation guarantee recovery; notifications only improve latency.
- **D11 — Non-destructive daemon:** Automatic behavior is limited to owned tmux resources and configured setup/launch side effects. Git/filesystem cleanup is explicit.
- **D12 — Setup policy is explicit and durable:** Explicit and passive triggers are separately configured; passive defaults off; attempts are persisted and do not loop.
- **D13 — Portable core, launchd adapter:** Core logic has no Lima or macOS coupling. Only service installation is macOS-specific in v1.
- **D14 — Two executables:** Install `wts` and `wtsd`; the LaunchAgent invokes `wtsd` directly while `wts` remains the administrative CLI.

## Implementation Notes

### Module scaffold and package shape

Create `worktree-sync/` by mirroring repository conventions, not by copying `worktree-manager` behavior wholesale:

- `cmd/wts/` and `cmd/wtsd/` contain minimal entry points.
- `cmd/` contains thin Cobra command construction for the `wts` command tree.
- Suggested internal boundaries are `config`, `registry`, `state`, `exec`, `git`, `tmux`, `naming`, `reconcile`, `actions`, `daemon`, and `launchd`. Merge boundaries when the resulting package is trivial, but do not put domain behavior in Cobra commands.
- The per-tool Makefile must build/install/test/lint/audit both binaries. Copy `.golangci.yml` from the nearest Go tool and adapt only justified lint exceptions.
- Write `README.md`, `DESIGN.md`, and `CLAUDE.md` for their distinct audiences, and create the sibling `AGENTS.md` symlink.

### Configuration, registry, and state

- Use `~/.config/worktree-sync/config.json` (or `XDG_CONFIG_HOME`) for human-editable global/per-repo configuration.
- Use `~/.local/share/worktree-sync/worktrees/` (or `XDG_DATA_HOME`) as the canonical creation root. `repo add` creates this default root with private permissions when absent; explicitly supplied roots must already exist.
- Use `~/.local/state/worktree-sync/` (or `XDG_STATE_HOME`) for locks, action ledger, and other runtime state; enforce private directory/file modes.
- Make `wts config validate` authoritative. `config edit` locks the config, edits a temporary copy, validates after the editor exits, and atomically replaces the live file only on success. `config refresh` merges newly introduced default fields without replacing user values, then validates and atomically writes. Daemon reloads valid config for reconciliation; if the current config is invalid, report the error and pause all tmux/setup/launch mutations until a valid config is available. The last valid snapshot may support diagnostics only, never reconciliation against stale intent.
- Repo registration resolves symlinks, verifies main/common Git directories, records canonical paths, and validates each allowed root. Repo IDs should be deliberately constrained so tmux naming does not require a lossy second mapping.
- Config and ledger updates use temp-file + fsync/close + rename semantics and cross-process locking where multiple `wts`/`wtsd` processes can write.

### Git and path model

- Parse NUL-delimited porcelain output as records; include tests for spaces, newlines, Unicode, detached HEAD, lock reasons, prune reasons, and paths sharing lexical prefixes.
- Resolve each live worktree’s canonical path and Git administrative identity. Use that identity to distinguish deleting/recreating a worktree at the same path and to key action attempts.
- Treat the registered primary worktree specially. Reject registration from a linked worktree to keep the base-session contract unambiguous.
- `wts worktree path/create` chooses `<data-root>/worktrees/<repo-id>/<branch-slug>` and adds a stable short suffix if that path would collide with another identity. `create` supports an existing local branch or a new branch with an optional start point.
- `remove` delegates safety to `git worktree remove`; expose separate safe and force branch-deletion flags, and report partial outcomes without pretending the whole operation rolled back.

### Tmux model

- Always pass `-L <socket-name>`. Production construction fixes the socket name to `wts`; only dependency-injected tests may substitute a unique socket so integration tests cannot touch a user’s real `wts` server.
- Create the repo session and base window together with cwd set to the primary root, then tag both immediately. Capture stable tmux IDs from command output and use IDs, not titles, for subsequent mutations.
- Store user options sufficient to distinguish managed session, base window, and worktree window and to support future metadata schema migration.
- Enumerate sessions/windows and metadata in machine-readable tmux formats. Never parse presentation-oriented `tmux ls` output.
- Compute all desired display names as a set so collision suffixes are deterministic. If a new collision changes a previously unique name, rename the tagged managed windows accordingly.
- Preserve untagged windows. If an untagged window occupies a desired title, choose the deterministic suffixed managed title rather than renaming the manual window.
- On repo removal, delete only matching tagged windows. Kill the session only when no untagged/foreign window remains.

### Reconciler and daemon

- Keep snapshot, diff, and apply separable so most reconciliation tests use pure desired/actual fixtures and fake Git/tmux clients.
- Apply creates/repairs before stale deletion where possible. Recheck or use stable IDs around mutations so an external Git/tmux change cannot redirect an operation.
- Skip stale deletion for a repo unless its relevant Git and tmux snapshots were complete. Keep failures isolated per repo so one broken registration does not block healthy repos.
- Use a bounded interval, event debounce, and command timeout with conservative defaults documented in `DESIGN.md` and config reference. Tests should use injected clocks/triggers rather than sleeps.
- Watch config, common Git worktree metadata, and allowed roots. Rebuild watcher registrations after valid config changes and after reconciliation discovers changed Git administrative paths. Treat notification overflow/error as a reason to enqueue a full scan, never as state itself.
- Handle SIGINT/SIGTERM with context cancellation and bounded shutdown. Log startup, config load, reconcile summaries, per-repo degradation/conflicts, watcher failure, and shutdown using `slog`; do not log copied file contents, environment values, or command secrets.

### Setup and launch actions

- Represent setup commands as argv arrays rather than implicit shell strings. Resolve cwd explicitly to the worktree and apply bounded contexts. Environment overrides are explicit config layered over inherited host environment.
- Model copy actions as registered-root-relative source and worktree-relative destination pairs. Resolve and validate every existing source/destination ancestor against the canonical source/worktree root, reject symlink escapes, and create the destination exclusively/atomically so an existing file is never overwritten.
- The operation lock covers provenance recording for `wts create`, so the daemon cannot observe an unclassified explicit worktree between Git creation and state recording.
- Record action result, definition digest, trigger (`explicit` or `passive`), and worktree identity. Do not retry a failed digest automatically; expose an explicit rerun/reconfigure path.
- Create the tmux inspection window even when setup fails. Run an allowed launch only after the managed window exists, and only on creation/recreation of that window—not on every periodic scan.
- Because launch text is interpreted by the interactive shell in the tmux pane, document that launch commands are trusted host config. Quote/send it as one literal tmux argument rather than building a host shell command.

### CLI and status contract

Organize commands by resource and keep help/errors command-specific:

- `wts config path|edit|validate|refresh`
- `wts repo add|list|remove`
- `wts worktree path|create|remove|setup|launch` (`rm` aliases `remove`)
- `wts attach`
- `wts status [--json]`
- `wts reconcile`
- `wts cleanup [--prune-git <repo-id>] [--remove-orphaned-tmux <repo-id>]`
- `wts daemon install|uninstall|start|stop|status`

Exact non-safety aliases/flags may follow Cobra conventions during implementation, but the capabilities and safety defaults above are contractual. All `wts daemon` management subcommands are specifically for the macOS per-user LaunchAgent and return a clear unsupported-platform error elsewhere; portable foreground operation is `wtsd` itself, not an unmanaged background/PID mode. JSON status must have an explicit version and deterministic ordering and include repo health, desired worktrees, actual managed tmux resources, unmanaged conflicts, stale/prunable Git entries, action failures, and daemon/LaunchAgent state where available.

### launchd and portability

- Keep plist rendering and `launchctl` execution behind an interface with deterministic tests. Use a per-user label such as `dev.agent-tools.worktree-sync` and install only under `~/Library/LaunchAgents` with safe file permissions.
- Resolve and embed the absolute sibling `wtsd` path during installation. Include a deterministic PATH adequate for Git/tmux while documenting that launchd does not load shell profiles.
- Put stdout/stderr logs under `~/Library/Logs/` and document that launchd does not rotate them.
- Return a clear unsupported-platform error for launchd commands elsewhere; `wtsd`, one-shot reconcile, status, registry, Git lifecycle, and tmux behavior remain portable Unix functionality.
- Verify Linux compilation to prevent accidental Darwin coupling. No core package should import Lima or call `limactl`.

### Monorepo integration

- Add `worktree-sync` to root `Makefile` and `go.work`.
- Update root `README.md` overview, install example, and tool section.
- Update root `CLAUDE.md` / `AGENTS.md` structure list.
- Update `assets/tool-relationships.svg` to show `worktree-sync` observing host-visible Git repositories/worktrees and controlling its isolated tmux resources. Do not depict an MCP or direct sandbox-control dependency.
- Validate the SVG as XML, render it to a temporary PNG, inspect the image with the image reader, and iterate on visual defects before completion.

## Documentation Impact

- **`worktree-sync/README.md`:** user-facing quick start, prerequisites, command reference, config examples, lifecycle behavior, safety boundary, migration/registration of existing `wt` roots, and troubleshooting.
- **`worktree-sync/DESIGN.md`:** authoritative desired-state/actual-state model, identities and metadata, reconciliation algorithm, failure semantics, setup/launch trust model, locking, daemon lifecycle, and portability boundary.
- **`worktree-sync/CLAUDE.md` plus `AGENTS.md` symlink:** package/dependency layout, development commands, timeout/locking invariants, intentional dependencies, and testing conventions.
- **`worktree-sync/docs/launchd.md`:** install/manage/log/troubleshooting behavior and minimal launchd environment.
- **`worktree-sync/examples/launchd/`:** reviewed plist example matching the generated service for users who want to inspect it.
- **Root `README.md`, `CLAUDE.md`, and `assets/tool-relationships.svg`:** index and architecture integration required for every new Go tool.
- Documentation must say that a transparently mounted sandbox requires no special integration: external worktree creation is supported when host and sandbox observe consistent registered paths. Do not advertise identical paths as a `wts` configuration feature unless Git itself requires them in the deployed mount arrangement.

## Testing / Verification

- **V1 (AC-1):** Run `go work sync`, `go mod tidy` in `worktree-sync/`, both binary builds, `make -C worktree-sync test`, `make -C worktree-sync lint`, and root `make build`/`make test`. Verify `go install` installs both `wts` and `wtsd`.
- **V2 (AC-2–AC-4):** Unit-test config/registry/path validation and NUL-delimited Git parsing with fake-runner outputs/fixtures, including main/linked/bare/detached/locked/prunable records, symlink and lexical-prefix containment, basename/ID collisions, default-root first creation, and pre-existing `wt` roots. Put coverage that creates temporary real Git repositories behind the bounded opt-in integration target used by V5.
- **V3 (AC-5–AC-9):** Table-test reconcile snapshot/diff/apply behavior with fakes: initial session/base/worktree creation; slug collisions; detached names; killed/renamed/duplicate managed windows; preserved scratch windows; foreign session collision; removed/out-of-root/missing worktrees; and each incomplete-snapshot failure proving no stale deletion. Race-test operation/singleton locking, event coalescing, cancellation, and timeout behavior.
- **V4 (AC-10–AC-15):** Cobra/service tests cover every command’s arguments and aliases, contextual repo inference, stable JSON ordering/schema, temporary-copy config editing/refresh, explicit-vs-passive policy, action-ledger retry suppression/rerun, safe and forced branch deletion, cleanup dry-run versus the two named/revalidated apply actions, copy-destination symlink escapes, and unregister behavior with scratch windows. Fake-runner assertions prove daemon/reconcile never issue prohibited Git/filesystem deletion commands.
- **V5 (AC-12):** Add an opt-in integration test or deterministic harness using temporary real Git repos and the dependency-injected unique tmux socket name reserved for tests. Start `wtsd` with short test intervals, run ordinary `git worktree add` without `wts`, assert the tagged window/cwd appears, run ordinary `git worktree remove`, assert only the tagged window disappears, and confirm an untagged scratch window survives. Bound waits with contexts and always clean up the temporary tmux server.
- **V6 (AC-16–AC-17):** Run `GOOS=linux go build ./cmd/wts ./cmd/wtsd` from the module. On macOS, test plist rendering/parsing, idempotent mocked launchctl transitions, unsupported-platform behavior, and manually verify install/start/status/stop/uninstall against only `dev.agent-tools.worktree-sync` when safe.
- **V7 (documentation and diagram):** Check every documented command/config field against CLI help/defaults. Run repo formatting checks. Parse `assets/tool-relationships.svg` as XML, render to PNG, inspect it visually for layout/contrast/edge defects, and retain no temporary render artifact in the repo.
- **V8 (final audit):** Run `make -C worktree-sync audit`, then root `make check` or `make audit` as environment/network availability permits. Report any unavailable external check explicitly; do not claim completion from unit tests alone.

## Risks and Mitigations

- **Accidental tmux data loss:** Name-based ownership could kill manual work. Mitigate with dedicated socket plus explicit metadata; mutate only matching tagged objects and test untagged collisions.
- **Missed filesystem events:** Mounts and watcher backends can omit/coalesce events. Mitigate with startup/periodic full scans and use events only as nudges.
- **Git/tmux races:** Agents or humans can mutate resources during reconciliation. Mitigate with complete snapshots, stable tmux IDs, process-local serialization, cross-process apply locking, idempotent retries, and fail-closed stale deletion.
- **Symlink/root escape:** Lexical path checks can manage unintended directories. Mitigate with canonical existing paths and component-aware containment tests.
- **Repeated or hostile setup execution:** Passive detection can trigger host actions. Mitigate with explicit per-repo opt-in, trusted host config only, argv execution, deadlines, action digests/ledger, no automatic failure retry, and clear docs.
- **Identity churn:** Branch renames and detached states make titles unstable. Mitigate by separating worktree identity from display name and using Git administrative identity/path rather than branch as the ownership key.
- **Config edits orphaning resources:** Hand-editing/removing registry entries can bypass cleanup. Mitigate by atomic validation, `wts repo remove` as the documented path, status/cleanup reporting for tagged orphan resources, and never broad name-based cleanup.
- **launchd environment mismatch:** Git/tmux may not be in PATH. Mitigate with explicit generated PATH, absolute daemon path, diagnostics, and foreground mode.
- **Scope expansion into a general workspace orchestrator:** Bound v1 to Git worktree/tmux projection, explicit lifecycle commands, and optional setup/launch; exclude panes, remote orchestration, agent APIs, and sandbox management.

## Assumptions

- Git and tmux versions available to users support `git worktree list --porcelain -z` and tmux user options/formats; document and validate concrete minimum versions during implementation based on the first commands used.
- Repo IDs are intentionally stable user-facing identifiers. Moving a registered primary repo is handled by an explicit registry update/re-registration workflow rather than silent identity guessing.
- A sandbox or other external process that creates worktrees does so in host-visible storage with Git metadata/path semantics that are valid from the host. `worktree-sync` does not translate paths.
- Passive setup and launch remain off unless a human explicitly enables them in host configuration for that repo.

## Handoff Summary

Implement this plan as a new `worktree-sync` Go module without changing existing `wt` behavior. Start with the identity/config/Git/tmux contracts and pure reconciliation tests, then add daemon scheduling/watchers, explicit lifecycle/actions, launchd integration, documentation, and monorepo indexes. Keep automatic deletion strictly limited to correctly tagged tmux projections, and require complete snapshots before removing even those.

Suggested objective:

```text
/goal Implement .plans/2026-07-18-worktree-sync.md. Complete only after every acceptance criterion is mapped to concrete test, command, documentation, or inspected UI/diagram evidence.
```
