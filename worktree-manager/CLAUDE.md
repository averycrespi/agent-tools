# worktree-manager

Go CLI tool that manages git worktree workspaces with tmux integration.

## Development

```bash
make test     # go test -race ./...
make lint     # go tool golangci-lint run
make fmt      # go tool goimports -w .
make build    # go build -o /tmp/bin/wt .
make audit    # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing to catch all issues.

## Tech Stack

- Go 1.25, Cobra, testify, slog
- golangci-lint v2 (errorlint, gocritic, gosec enabled)
- goimports for formatting

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

All external commands go through `exec.Runner`. Tests mock this interface — no real git/tmux calls in unit tests.

The `workspace` package defines its own `gitClient` and `tmuxClient` interfaces (interface segregation). The `cmd` layer is thin: it constructs dependencies in `root.go` and delegates to `workspace.Service`.

## Testing

- All internal packages have tests. `cmd/` has no tests (thin wrappers).
- Git and tmux clients use mock `exec.Runner` to verify exact command arguments.
- Workspace tests use mock git/tmux client interfaces.
- Config tests use `t.Setenv` and `t.TempDir` for isolation.

## Key Patterns

- **Idempotent operations**: `Add` skips existing worktrees/windows. `Remove` skips already-removed resources.
- **Error format**: Command failures use `%s` with trimmed output (human-readable). Go errors use `%w` for wrapping.
- **File/script operations are best-effort**: `copyFile` and `runSetupScripts` log warnings on failure, don't halt.
- **Tmux socket isolation**: All tmux calls use `-L wt` to avoid interfering with the user's default tmux.
- **nolint directives**: gosec findings on `os/exec`, file permissions, and `os.Open` are suppressed with explanatory comments — these are intentional for a CLI tool.
