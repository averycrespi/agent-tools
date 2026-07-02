# Clone GitHub Repo Tool Plan

## Goal

Add a GitHub-specific clone tool to `local-git-mcp` so sandboxed agents can ask the host-side MCP server to clone private GitHub repositories over SSH into an allowed parent directory, such as a user's `work` directory, without exposing credentials inside the sandbox.

## Background / Repo Context

- `local-git-mcp` is a stdio MCP server that shells out to the host `git` binary so operations can use host SSH keys and credential helpers. See `local-git-mcp/README.md` and `local-git-mcp/DESIGN.md`.
- Existing tools are repo-oriented and require `repo_path`, validated by `git.Client.ValidateRepo` before dispatch in `local-git-mcp/internal/tools/tools.go`.
- The new clone tool is different because the target repository path must not exist yet. It needs destination-parent validation instead of existing-repo validation.
- All git subprocesses must go through the context-aware `exec.Runner` and respect the configured per-command timeout. Existing command execution is centralized through `git.Client.runDir` in `local-git-mcp/internal/git/git.go`.
- Project conventions require `--` before user-controlled git positional arguments. This applies to `git clone -- <ssh-url> <target-path>`.
- Every MCP tool must set one of the annotation presets in `local-git-mcp/internal/tools/tools.go`; `TestEveryToolHasAnnotations` enforces this.

## Acceptance Criteria

- AC-1: `local-git-mcp` exposes a new MCP tool named `clone_github_repo` with required input fields `repository` and `destination_dir`.
- AC-2: `repository` accepts only GitHub repository slugs in `owner/repo` form and rejects arbitrary URLs, missing owner/repo parts, path traversal, whitespace, and malformed values with clear tool errors.
- AC-3: The tool always clones over SSH using `git@github.com:owner/repo.git`; it does not accept HTTPS URLs or generic remotes.
- AC-4: The tool derives the local target path as `<destination_dir>/<repo>` and fails with a clear error if that target path already exists.
- AC-5: `destination_dir` must be an absolute existing directory equal to or inside the configured allowed path prefixes, using the same allowlist policy concept as existing tools. Calls outside allowed paths are rejected before running `git`.
- AC-6: Symlinked `destination_dir` values are rejected before cloning so the tool cannot write outside the intended allowed tree through a symlink.
- AC-7: The clone command uses the existing context/timeout execution path and constructs safe git arguments with `--` before user-controlled positional arguments.
- AC-8: On success, the tool returns JSON text containing the cloned repo path, with shape `{"repo_path":"<derived-target-path>"}`.
- AC-9: After cloning, the implementation validates that the derived target path is a git repository before returning success.
- AC-10: Existing `push`, `pull`, `fetch`, `list_remote_refs`, and `list_remotes` behavior and tests remain compatible.
- AC-11: README and DESIGN docs describe the new tool, its GitHub-only SSH behavior, and the fact that clone uses `destination_dir` rather than `repo_path`.

## Non-Goals / Out of Scope

- Generic `git clone` for arbitrary remote URLs.
- HTTPS cloning.
- Custom target directory names.
- Branch selection, shallow clone depth, or submodule support.
- Retrying failed clone operations or adding special recovery beyond returning git's error output.
- Changing the semantics of existing repo-scoped tools beyond the minimal handler refactor needed to support clone.

## Constraints

- Tool input must stay consistent with existing style: snake_case fields and absolute host paths.
- Use the existing allowed-path startup policy; callers should be able to start `local-git-mcp /Users/.../work` and clone into descendants of that path.
- Do not overwrite existing directories or files.
- Do not use bare shell strings; build argv slices and execute through the existing runner abstraction.
- Preserve current timeout behavior for long-running or hung remote operations.
- Return errors as MCP tool error results, not Go handler errors, matching existing handler behavior.

## Chosen Approach

Add a GitHub-specific MCP tool named `clone_github_repo` with input:

```json
{
  "repository": "owner/repo",
  "destination_dir": "/absolute/allowed/parent"
}
```

The git layer should parse and validate the repository slug, derive the SSH URL and target path, validate that the destination parent is allowed and exists, fail if the target exists, run `git clone -- git@github.com:owner/repo.git <target>`, validate the clone with `git -C <target> rev-parse --git-dir`, and return the cleaned target path. The tools layer should marshal that path as JSON text: `{"repo_path":"..."}`.

This is preferred over accepting `remote_url` or final `target_path` because it makes the GitHub-only constraint explicit, prevents accidental generic clone behavior, and avoids path-trick complexity around caller-supplied target paths.

## Design Decisions

- D1: Tool name is `clone_github_repo`, not `clone`, because v1 is intentionally GitHub-only and SSH-only.
- D2: Input uses `repository` as `owner/repo`, not a full URL. This prevents HTTPS or arbitrary hosts from entering the API surface.
- D3: Input uses `destination_dir` as the parent directory, and the implementation derives the final repo directory from the repo name. No custom directory name in v1.
- D4: Existing tools should continue using `repo_path`; `clone_github_repo` should be the only tool in v1 that does not require `repo_path`.
- D5: Annotate `clone_github_repo` as `annAdditive` because it mutates local filesystem state and talks to GitHub, but must not overwrite existing data.
- D6: Return structured JSON as a text result, matching existing `list_remotes` and `list_remote_refs` behavior.

## Implementation Notes

- Modify `local-git-mcp/internal/git/git.go`:
  - Add a result type if useful, e.g. `CloneResult` with `RepoPath string`, or return the path string directly from the git client and let the tools layer wrap it.
  - Add a clone method such as `CloneGitHubRepo(ctx context.Context, repository, destinationDir string) (string, error)`.
  - Add helper logic for validating `owner/repo` slugs. Keep it strict and small; reject empty components, additional slashes, `.`/`..`, path separators inside components, whitespace, and shell/URL-shaped inputs.
  - Add helper logic for clone target validation. It should clean `destination_dir`, require it to be absolute, require it to be allowed, require it to exist and be a directory, reject it if it is a symlink, derive `<destination_dir>/<repo>`, and require the target path not to exist.
  - Use `os.Stat`/`os.Lstat` or equivalent for filesystem existence checks; no git subprocess should run before the allowlist and target-exists checks pass.
  - Add or reuse a runner helper so `git clone` gets the same request context and timeout. Since clone does not run inside an existing repo, either use `runner.Run` with a timeout helper or run from `destination_dir` using `runDir`; either is acceptable if tests prove timeout propagation and argv construction.
  - Run `git clone -- <ssh-url> <target-path>` and then validate with `git -C <target-path> rev-parse --git-dir` before returning.
- Modify `local-git-mcp/internal/tools/tools.go`:
  - Extend `GitClient` with the clone method.
  - Add the `clone_github_repo` tool schema with required `repository` and `destination_dir` fields and `annAdditive`.
  - Refactor `Handle` so `clone_github_repo` does not go through the current top-level `repo_path` requirement. Prefer per-case validation or an early clone case; keep existing repo-scoped behavior unchanged.
  - On success, return JSON text with `repo_path`.
- Modify tests:
  - `local-git-mcp/internal/git/git_test.go`: add tests for valid clone argv, SSH URL derivation, derived target path, target-exists failure, malformed repository rejection, relative/outside destination rejection, destination-not-directory rejection, symlinked destination rejection, clone command failure, and post-clone validation failure.
  - `local-git-mcp/internal/tools/tools_test.go`: extend `mockGitClient`, update expected tool names/count, test clone handler success JSON, missing required fields, clone error propagation, absence of `repo_path` requirement for clone, and `annAdditive` annotation.
  - If integration tests already exist for local remote operations, consider adding a focused integration test only if it can use a local fixture while still exercising the clone plumbing. Do not require real GitHub network access for normal tests.
- Update docs:
  - `local-git-mcp/README.md`: tool table, input/validation language, Quick Start implications, and the statement that existing repo tools require `repo_path` while clone requires `destination_dir`.
  - `local-git-mcp/DESIGN.md`: tools table, parameter details, validation/error handling, security notes, and testing section.
  - `local-git-mcp/CLAUDE.md` likely does not need changes unless new clone-specific gotchas are worth recording.

## Documentation Impact

Documentation updates are required because this changes the user-facing MCP tool list and invalidates the current README/DESIGN wording that all tools require `repo_path`. Update `local-git-mcp/README.md` and `local-git-mcp/DESIGN.md` as part of the implementation.

## Testing / Verification

- V1: Run `make test` from `local-git-mcp/`; expect all unit tests to pass with the race detector. This verifies handler behavior, git arg construction, validation, timeout plumbing, and no regressions for existing tools.
- V2: Run `make test-integration` from `local-git-mcp/`; expect integration tests to pass or explicitly report if no clone-specific integration test is practical without real GitHub credentials.
- V3: Run `make lint` from `local-git-mcp/`; expect no lint findings.
- V4: Run `make build` from `local-git-mcp/`; expect the binary to compile.
- V5: Run `make audit` from `local-git-mcp/` before final handoff if available in the environment; expect tidy, formatting, lint, tests, and vulnerability checks to pass.
- V6: Review `local-git-mcp/README.md` and `local-git-mcp/DESIGN.md` to confirm they no longer claim every tool requires `repo_path` and that `clone_github_repo` behavior is documented.
- V7: Optional manual MCP/tool-level smoke test: invoke `clone_github_repo` against a test GitHub repository or a private repo the operator has access to, with `destination_dir` inside the server's allowed prefix, and confirm the returned `repo_path` exists and is a git repo.

## Risks and Mitigations

- Risk: Refactoring handler validation could accidentally bypass `ValidateRepo` for existing tools. Mitigation: preserve or add tests proving repo-scoped tools still require and use validated `repo_path`.
- Risk: Repository slug validation could accidentally allow arbitrary URLs or path traversal. Mitigation: implement strict `owner/repo` parsing and table-driven tests for rejected inputs.
- Risk: Clone could overwrite or write into an unexpected location. Mitigation: validate absolute allowed destination parent, derive target path internally, and fail if target exists before running git.
- Risk: Symlinked destination directories can complicate allowlist guarantees. Mitigation: reject symlinked `destination_dir` values before cloning and cover that behavior with a unit test.
- Risk: Unit tests cannot prove private GitHub auth works. Mitigation: use unit tests for command construction and validation; keep any real GitHub clone as optional/manual verification because it depends on operator credentials and network access.

## Assumptions

- The host running `local-git-mcp` has working SSH authentication for GitHub.
- The configured allowed path prefix for the intended use case includes the desired parent directory, such as a `work` directory.
- Returning JSON as MCP text content is acceptable because existing structured outputs in this server already use JSON text.

## Handoff Summary

Implement `clone_github_repo` for `local-git-mcp` exactly as a GitHub-only SSH clone primitive: input `repository` plus `destination_dir`, derive the target path, refuse existing targets, run through the existing timeout-aware git runner, validate the resulting repo, and return `{"repo_path":"..."}`. Complete only after unit tests, lint/build checks, and README/DESIGN updates satisfy the acceptance criteria.

Suggested objective for `/goal`:

```text
Implement .plans/2026-07-02-clone-github-repo-local-git-mcp.md. Complete only after every acceptance criterion is satisfied with concrete evidence from tests, build/lint output, and documentation review.
```
