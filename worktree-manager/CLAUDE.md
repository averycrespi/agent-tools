# worktree-manager

Go CLI tool (`wt`) for managing git worktree workspaces with tmux integration.

## Development

```bash
make build    # go build -o wt .
make test     # go test -race ./...
make lint     # go tool golangci-lint run
make fmt      # go tool goimports -w .
make tidy     # go mod tidy && go mod verify
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

```
cmd/           Cobra commands (thin wrappers, no logic)
internal/
  exec/        Runner interface abstracting os/exec
  config/      Config struct + XDG path functions
  git/         Git worktree client (uses exec.Runner)
  tmux/        Tmux session/window client (uses exec.Runner)
  workspace/   Orchestration service (uses git + tmux + config)
```

`workspace` defines its own `gitClient` and `tmuxClient` interfaces. `cmd/` constructs dependencies in `root.go` and delegates to `workspace.Service`.

## Conventions

- All external commands go through `exec.Runner` — tests mock this interface, no real git/tmux calls in unit tests
- `cmd/` has no tests (thin wrappers); all internal packages do
- Operations are idempotent: `Add` skips existing worktrees/windows, `Remove` skips already-removed resources
- File copy and setup script failures log warnings but don't halt (best-effort)
- Command failures use `%s` with trimmed output; Go errors use `%w` for wrapping
- All tmux calls use `-L wt` socket to avoid interfering with the user's default tmux
- gosec `nolint` directives on `os/exec`, file permissions, and `os.Open` are intentional for a CLI tool
