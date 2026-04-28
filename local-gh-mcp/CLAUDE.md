# local-gh-mcp

Stdio MCP server for GitHub operations via the gh CLI.

## Development

```bash
make build              # go build -o local-gh-mcp ./cmd/local-gh-mcp
make install            # go install ./cmd/local-gh-mcp
make test               # go test -race ./...
make lint               # go tool golangci-lint run ./...
make fmt                # go tool goimports -w .
make tidy               # go mod tidy && go mod verify
make audit              # tidy + fmt + lint + test + govulncheck
```

Run `make audit` before committing.

## Architecture

Stdio MCP server. No config, no state, no network listener.

Shells out to the host's `gh` binary for all operations. Validates `gh auth status` on startup — exits immediately if not authenticated.

```
cmd/local-gh-mcp/       CLI entry point (Cobra)
internal/
  exec/                  Runner interface for command execution
  gh/                    GitHub operations via exec.Runner
  tools/
    tools.go             Tool registration and dispatch
    context.go           Context tools (gh_whoami)
    pr.go                PR tool definitions and handlers
    issue.go             Issue tool definitions and handlers
    run.go               Workflow run tool definitions and handlers
    cache.go             Cache tool definitions and handlers
    search.go            Search tool definitions and handlers
    branch.go            Branch tools (gh_list_branches)
    release.go           Release tool definitions and handlers
  format/                Markdown formatting for tool output (authors, dates, truncation, diff summaries)
```

## Conventions

- Errors are wrapped with context: `fmt.Errorf("doing X: %w", err)`
- All external commands go through `exec.Runner` interface
- `cmd/` has no tests (thin wrappers); all internal packages have tests
- Command failures use `%s` with trimmed output; Go errors use `%w` for wrapping
- gosec nolint directives on os/exec are intentional for CLI
- `owner` and `repo` validated against `[a-zA-Z0-9._-]` pattern before use
- Repo targeting: `-R owner/repo` flag (not repo_path)
- Markdown output: all read tools (view, list, search, diff, check) return structured markdown instead of raw JSON. Write tools (create, comment, merge, edit, close, rerun, cancel, delete) return plain text confirmations.
- Body truncation: tools returning text bodies accept `max_body_length` param (default 2000, max 50000). Bodies exceeding the limit are cut on a whitespace boundary with `[truncated — showing N of M bytes]`.
- List truncation: list tools fetch limit+1 items so the formatter can detect overflow. When detected, the formatter slices to limit and appends `[showing first N <things> — more results available; increase limit or paginate]`. The lookahead only proves "at least one more exists", so the trailer must NOT report a fake total like "N of N+1" — use `writeListTruncationFooter` in `format/github.go` to keep wording consistent.
- Author flattening: all author objects rendered as `@login` or `@login [bot]` — never raw JSON.
- Author rendering: `FormatAuthor` strips a literal `[bot]` suffix from the login as a defensive fallback for endpoints that don't populate `is_bot`.
- Limits: default 30, max 100, clamped silently (derived from gh CLI defaults and GitHub API page size)
- User-controlled string args (search queries, run IDs, cache IDs) need `--` separator before positional args to prevent flag injection; integer args (PR/issue numbers via `strconv.Itoa`) are safe without it
- Stdio MCP server setup: `mcpserver.NewMCPServer()` + `srv.AddTool(tool, handler.Handle)` + `mcpserver.ServeStdio(srv)`
- `mcp-go` v0.45.0: use `req.GetArguments()` helper instead of `req.Params.Arguments`; JSON numbers arrive as `float64`, use type switch for int extraction
- Tool annotations: every `gomcp.Tool` in `internal/tools/*.go` must set `Annotations` to one of the four presets declared in `tools.go` (`annRead`, `annAdditive`, `annIdempotent`, `annDestructive`). Coverage enforced by `TestEveryToolHasOpenWorldHint`.
- Enums: parameters with a fixed value set declare `"enum": []string{...}` in the schema. Handler-side validation remains as defense-in-depth; the schema surfaces the set to agents without requiring a failed call first.
- Parse errors: all JSON unmarshal call sites route through `parseError` in `tools.go`, which logs the raw `gh` output at `slog.Error` and returns a terse message (`"internal error: unable to parse gh output; check server logs"`). Never surface the raw parser error to the agent.
- Resource-qualified IDs: tools accepting an integer ID declare `pr_number` or `issue_number`; string IDs use `run_id` or `cache_id`. Bare `number` or `id` properties are forbidden — `TestNoBareIDParameters` enforces this. Handler Go-local variables may keep the shorter `number` name since a `PR*` or `Issue*` call site is unambiguous.
