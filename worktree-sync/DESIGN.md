# Worktree Sync Design

## Purpose and boundary

`worktree-sync` projects explicitly registered Git worktree state into an isolated tmux environment. It is not a replacement for `worktree-manager`, a repository discovery service, an agent API, a sandbox controller, or a Git cleanup daemon. `wts` is the human control surface and `wtsd` is a foreground scheduler around the same reconciliation service.

The core supports macOS and Linux and has no Lima dependency or path translation. External worktree creation is supported when the host can use the registered paths and Git metadata consistently.

## Configuration and identity

Configuration is versioned JSON under the XDG `worktree-sync` namespace. The registry stores canonical primary roots, canonical common Git directories, stable user-selected repository IDs, one canonical worktree creation root, and a complete canonical allowlist. Global `default_worktree_creation_root` and `default_allowed_worktree_roots` seed newly registered repositories; default changes never alter existing registrations. `--worktree-root` replaces the default creation root, repeatable `--allowed-worktree-root` values replace the default additional allowlist, and `--no-default-allowed-roots` explicitly chooses creation-root-only registration. When no creation root is selected, registration creates and uses the private XDG data root. The creation root is automatically added to the repository allowlist, and allowed-root ordering has no creation semantics. Registration accepts only a primary non-bare worktree; a linked worktree is not a valid base.

Focused root commands mutate only creation/allowed roots under the operation lock. Setting a new creation root retains the former root in the allowlist. Allowed-root removal requires a complete fresh Git snapshot proving every live worktree remains covered by another root; the command never invokes reconciliation itself, while a running daemon independently observes the config change and applies the usual complete Git/tmux snapshot gates.

Configuration version 3 makes creation placement explicit. Refreshing version 2 derives each creation root from the former first allowed root while preserving the full allowlist. Version-1 policy migration also derives repository creation roots before validation. Migration writes through the same validated atomic config replacement as ordinary refresh.

Repository IDs are a deliberately constrained, non-lossy tmux-safe identity rather than sanitized display text. Repository identity is derived from the canonical common Git directory rather than persisted separately. Current-directory selection probes the current primary or linked worktree's canonical common Git directory and matches that identity directly, so unregistered and outside-Git contexts receive distinct recovery guidance without per-repository Git subprocesses. A linked worktree identity combines its canonical Git administrative directory with its filesystem administrative identity, so removing and recreating a worktree at the same path produces a new action/projection identity. Branch and path labels are display data only; lifecycle lookup prefers an exact branch and otherwise canonicalizes an existing path.

Config and durable state use private directories, mode-`0600` files, advisory locks, temporary-file writes, file sync, atomic rename, and parent-directory sync. Config editing validates a temporary copy before replacement. Invalid config is diagnostics-only: no stale intent is used for mutation.

## Desired state

For each repository, one successful `git worktree list --porcelain -z` parse produces a complete desired-state snapshot. The primary worktree maps only to a base window. A linked record is eligible only when:

1. it is live rather than prunable/missing;
2. its existing path canonicalizes successfully;
3. its Git administrative identity is available; and
4. its canonical path is component-wise contained by an allowed canonical root.

Detached and locked live records remain eligible. Outside-root, missing, and prunable records are status/report entries, not projections. Any unexpected parse/identity/canonicalization failure marks the snapshot incomplete.

Window names are readable branch/path slugs. Detached names begin with a short HEAD fallback. Desired names are computed as a set; only collisions receive a deterministic identity suffix. A manual window occupying a desired title is preserved and the managed title receives a suffix.

## Actual state and ownership

All tmux operations pass `-L wts` (tests inject a unique socket). Actual state is enumerated through stable tmux IDs and scope-specific option reads, not presentation output or names. Managed sessions and windows carry:

- `@wts-schema`
- `@wts-repository`
- `@wts-role` (`session`, `base`, or `worktree`)
- `@wts-identity`

All fields must match before mutation. An untagged or foreign-owned `wts-<repo-id>` collision is a conflict; it is never adopted or attached. Attach locates the fully owned session from a complete dedicated-socket snapshot, rechecks metadata by stable session ID, and attaches by ID. A missing session requires explicit reconciliation. Untagged windows remain manual even when their name or inherited session-format display resembles a managed object.

## Reconciliation

The reconciler separates snapshot, pure plan, and apply. For a complete Git and tmux snapshot it:

1. creates a missing session and base, tagging both immediately;
2. creates missing worktree windows and tags them immediately;
3. repairs owned names, pane working directories, and metadata by stable ID;
4. removes duplicate owned projections; and
5. removes owned worktree windows absent from the eligible desired set.

Creation precedes stale deletion. If tagging a newly created object fails, that just-created object is removed by its captured ID so it cannot become an unmanageable name collision. An absent-session creation returns the captured session ID; subsequent windows never target a raced name.

Incomplete Git state permits no repair, duplicate removal, stale removal, setup, or launch. An incomplete tmux snapshot permits no apply. Invalid configuration permits no snapshot/apply. These rules fail closed for destructive behavior while retaining convergent retry on a later scan.

The daemon and human lifecycle operations acquire one cross-process operation lock. A separate state-directory lock permits only one daemon. Locks are context-aware, and every non-interactive subprocess receives caller cancellation, bounded captured output, a killable Unix process group, and a finite configured timeout. Multi-repository scans reuse one complete tmux socket snapshot while stable-ID ownership is still rechecked immediately before mutation. Reconciliation is convergent rather than transactional across Git and tmux.

## Scheduling and recovery

`wtsd` performs a full reconciliation at startup and on the configured interval. It watches the config directory, registered common Git directories, and allowed worktree roots with `fsnotify`. Valid config reload rebuilds the watch set. Events, watcher errors, and event overflow only reset one debounce timer and enqueue an early full scan. Correctness never relies on event delivery, so periodic scans recover from coalescing, unavailable mounts, downtime, and restarts.

SIGINT and SIGTERM cancel in-flight work and stop the foreground loop. Structured `slog` records cover startup, trigger reason, reconcile summaries/degradation, watcher errors, and shutdown without logging copied contents, environments, or configured command secrets.

## Explicit lifecycle and provenance

`wts worktree create` runs under the operation lock, chooses a collision-safe path below the configured canonical `worktree_creation_root`, asks Git to check out an existing local branch or create one from an optional `--from` revision, re-snapshots its Git administrative identity, and atomically records explicit provenance before releasing the lock. Provenance includes repository, path, and worktree identity; path reuse cannot inherit an earlier explicit classification. Removal clears matching provenance and delegates dirty-worktree checks to Git. Branch deletion requires a separate safe or force flag.

Ordinary `git worktree add` has no provenance entry and is passive. The next event nudge or periodic full scan discovers it through Git and projects it if eligible.

## Setup, copy, and launch

Actions are trusted per-repository host configuration; repository-local files cannot define actions. Setup and launch each use a cumulative policy: `none`, `manual`, `wts-created`, or `all`. The default `manual` mode never automates actions; `wts-created` enables automation for CLI-created worktrees, and `all` also enables automation for externally discovered worktrees.

Copy actions use Go's rooted filesystem API for race-resistant containment of both source and destination, reject absolute/parent traversal, create private parents, fully write and sync a rooted temporary file, and publish it with a no-replace hard link so existing files and symlinks are never overwritten or exposed partially. Setup commands are argv arrays executed without a shell in a freshly revalidated worktree with inherited environment plus validated overrides and a bounded action/default timeout.

The action ledger is protected by its own advisory lock and keyed by repository identity, worktree identity, origin trigger, and a digest of the action definition. Manual and WTS-created attempts retain the legacy explicit ledger key so version-1 attempts do not run again after migration. Complete reconciliations remove entries for vanished worktree identities and obsolete definitions. Success and failure both suppress automatic retry; changed definitions, new worktree identities, or explicit `--rerun` are eligible. Setup runs after the inspection window exists, so failure never hides the worktree. Launch text is trusted shell input sent as one literal tmux argument and Enter. Automatic launch is considered only when a managed window was newly created and its trigger policy permits it.

## Status, cleanup, and unregister

Status separates collection from human and JSON rendering. Healthy human output is concise; non-healthy repositories expand diagnostics and recovery, while verbose output includes all desired worktrees and managed windows. Reporting-only status returns success even for attention states; `--check` prints the complete report before returning nonzero.

Version-2 JSON owns stable DTOs rather than serializing internal Git/tmux structures or raw launchctl text. All arrays are non-null and deterministically sorted. Repository health is `healthy`, `attention`, `degraded`, or `conflict`; typed diagnostics preserve snapshot, ownership, outside-root, prunable, ledger, action, and daemon causes. Failed-action rendering never exposes encoded ledger keys or configured commands. Daemon states are typed, with unsupported/not-installed neutral and stopped/unavailable represented as global diagnostics.

Automatic reconciliation never removes Git worktrees, branches, directories, or prunable metadata. `cleanup` without explicit action flags is reporting-only. `--prune-git <repo-id>` re-snapshots under the operation lock and invokes repository-wide `git worktree prune` only when Git still reports prunable records. `--remove-orphaned-tmux <repo-id>` re-snapshots both sides and kills only correctly owned identities absent from desired state; duplicate live projections remain the reconciler's responsibility.

Unregister snapshots tmux before changing config. It removes only correctly owned windows. If no manual window remains it kills the owned session by captured ID; otherwise the session and manual windows remain. A cleanup failure leaves the repository registered for a safe retry. Git state is never changed.

## LaunchAgent

The macOS adapter owns only `dev.agent-tools.worktree-sync`. Installation atomically writes a private per-user plist containing the absolute sibling `wtsd`, explicit PATH, effective absolute XDG homes, `RunAtLoad`, `KeepAlive`, and separate logs, then bootstraps the user domain. A fixed per-user lock outside XDG state serializes observation, update, rollback, start, stop, restart, and uninstall for the single label. Stop unloads while retaining the plist; start loads a stopped installation; restart is explicit. Replacement bootstrap failure restores the previous plist and loaded/stopped state or reports exact partial progress. Log inspection reads bounded history from only the fixed stdout and stderr paths; follow mode streams existing logs until caller cancellation. Unsupported platforms return a clear error; argument-validating `wtsd` remains the portable foreground execution mode.
