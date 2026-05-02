# Restrict local-git-mcp Repository Paths

## Goal

Change `local-git-mcp` from accepting any absolute git repository path to accepting only repositories under explicit host-side path prefixes supplied at startup.

This is a breaking change: callers must start the server with one or more allowed path prefixes, or with the explicit `--allow-all-paths` escape hatch to intentionally allow all host paths.

## Constraints

- Preserve the existing stdio MCP shape and tool names.
- Keep `repo_path` required and absolute for every tool call.
- Return path-denial failures as MCP tool error results, not process crashes.
- Fail fast at process startup when no allowed paths are provided.
- Keep all external git execution behind `internal/exec.Runner`.
- Use small, testable changes; avoid introducing config files or persistent state.

## Acceptance Criteria

1. Starting `local-git-mcp` without allowed paths and without `--allow-all-paths` exits non-zero before serving MCP and prints a clear error telling the user to provide at least one allowed path or pass `--allow-all-paths`.
2. Starting `local-git-mcp /some/root /other/root` allows tool calls whose absolute `repo_path` is exactly one of those roots or a descendant of either root.
3. A tool call with `repo_path` outside all configured roots returns an MCP tool error that includes the rejected path and the allowed path prefixes.
4. Existing git repository validation still runs after path-prefix validation, so allowed paths that are not git repositories still return the existing “not a git repository” style error.
5. Starting `local-git-mcp --allow-all-paths` preserves the old “any absolute git repo” behavior.
6. Starting with both `--allow-all-paths` and explicit allowed paths fails as ambiguous.
7. Unit tests cover startup argument validation and allowed/denied repo path validation, including sibling-prefix safety such as `/repo2` not matching allowed `/repo`.
8. README and DESIGN document the new required startup arguments, `--allow-all-paths`, and the breaking-change behavior.

## Chosen Approach

Add path allowlist enforcement to `internal/git.Client`, because that is where `ValidateRepo(repoPath)` already centralizes per-call repository validation for every MCP tool.

Implementation shape:

1. Add an allowlist field to `git.Client`, e.g. `allowedPaths []string`.
2. Change `git.NewClient` to accept allowed paths plus an allow-all boolean, normalize them, and return `(*Client, error)`.
3. Normalize allowed paths at startup:
   - reject an empty list unless allow-all is true;
   - reject allow-all combined with explicit paths;
   - require every allowed path to be absolute;
   - clean paths with `filepath.Clean`;
   - optionally deduplicate for clearer error messages.
4. In `ValidateRepo`, check the requested `repo_path` before running git:
   - require absolute as today;
   - clean the requested path;
   - allow if `--allow-all-paths` is configured;
   - allow if requested path equals an allowed root;
   - allow if `filepath.Rel(root, requested)` does not start with `..` and is not absolute;
   - otherwise return a clear error listing the allowed prefixes.
5. Keep the existing `git rev-parse --git-dir` validation after the allowlist check.
6. Update `cmd/local-git-mcp/root.go` to make allowed paths positional args (`local-git-mcp ALLOWED_PATH...`), add `--allow-all-paths`, validate ambiguous combinations, and pass both into `git.NewClient`.
7. Update tests in `internal/git/git_test.go`; add root command tests if feasible without starting stdio serving, or factor argument validation enough to unit test it directly.
8. Update README and DESIGN examples to show `local-git-mcp /shared/worktrees /another/root` and `local-git-mcp --allow-all-paths` for explicit unrestricted mode.

## Assumptions / Open Questions

- Assumption: allowed prefixes are host-side paths as seen by the process running `local-git-mcp`.
- Assumption: allowed paths themselves do not need to be git repositories; they are prefixes that may contain many repos.
- Assumption: allowed paths should be absolute. Relative allowed paths should be rejected at startup to avoid ambiguity.
- Recommended symlink policy: enforce on cleaned lexical paths first, not `EvalSymlinks`, to avoid surprising startup failures for valid mount paths. This is consistent with the user’s “shared path prefixes” framing, but it does not prevent a symlink under an allowed root from pointing outside that root.

## Ordered Tasks

1. Update `internal/git.Client` constructor and fields to carry normalized allowed path prefixes.
2. Implement normalization and prefix-check helpers in `internal/git`.
3. Update `ValidateRepo` to deny out-of-prefix paths before invoking git.
4. Update `cmd/local-git-mcp/root.go` Cobra args/usage, add `--allow-all-paths`, and wire allowed paths plus the flag into `git.NewClient`.
5. Update unit tests for constructor validation, prefix matching, `--allow-all-paths`, outside-path errors, ambiguous allow-all-plus-paths startup, and existing repo validation behavior.
6. Update README and DESIGN for the breaking startup contract.
7. Run formatting and tests for `local-git-mcp`.

## Verification Checklist

- `make fmt` from `local-git-mcp/`
- `make test` from `local-git-mcp/`
- Optional manual smoke check: `go run ./cmd/local-git-mcp` exits non-zero with the expected startup error.
- Optional manual smoke check: `go run ./cmd/local-git-mcp --allow-all-paths` starts serving MCP.

## Known Issues / Follow-ups

- Symlink escape hardening can be added later by resolving both allowed roots and `repo_path` with `filepath.EvalSymlinks`, but that may be less friendly with mounted paths that are not present when config is authored.
