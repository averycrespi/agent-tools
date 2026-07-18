# worktree-sync

Go CLI (`wts`) and foreground daemon (`wtsd`) that project registered Git worktrees into an isolated tmux server.

## Development

```bash
make build
make install
make test
make integration-test # requires git and tmux; uses temporary repositories/socket
make lint
make fmt
make tidy
make audit
```

## Architecture

```text
cmd/wts/             Thin administrative CLI
cmd/wtsd/            Thin foreground daemon
internal/config/     XDG config, validation, and atomic persistence
internal/state/      Private state, operation/singleton locks, action ledger
internal/git/        Repository validation and worktree snapshots
internal/tmux/       Dedicated-socket snapshots and owned mutations
internal/reconcile/  Desired/actual diff and fail-closed apply
internal/actions/    Copy/setup/launch policy and durable attempts
internal/daemon/     Periodic scans, watcher nudges, signal lifecycle
internal/launchd/    Per-user macOS LaunchAgent adapter
internal/service/    Human workflows and status aggregation
```

All subprocesses use `internal/exec.Runner`, caller cancellation, and a finite configured deadline. Keep commands thin and define narrow client interfaces at orchestration boundaries. Never authorize tmux mutation by a name: schema/repository/role/identity metadata is the ownership boundary. Git snapshots and tmux snapshots must both be complete before stale managed resources are deleted. The operation lock serializes lifecycle and reconcile apply phases; the daemon lock is separate.

`fsnotify` is only a latency nudge; periodic full reconciliation guarantees recovery. `gofrs/flock` provides Unix advisory locks. Tests use fake runners; integration tests must use a unique tmux socket and always clean it up.
