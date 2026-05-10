# Direct `wt` / `sb` Package Integration for `ad`

## Goal

Remove `ad`'s dependency on installed `wt` and `sb` binaries by exposing the narrow worktree-manager and sandbox-manager functionality that `ad` needs through public Go packages, then updating `ad` to call those packages directly.

`ad` should still use the same underlying behavior as the existing CLIs: `wt` owns worktree creation/setup semantics, and `sb` owns Lima sandbox lifecycle and command execution semantics. This change only removes the extra CLI binary boundary between the tools.

## Constraints

- Keep the public API minimal and `ad`-focused; do not expose the full CLI surface unless `ad` needs it.
- Preserve existing CLI behavior for `wt` and `sb`.
- Do not move broad internal implementation packages into `pkg/`; keep internals private behind small facades.
- `ad` will still depend on external tools required by the underlying managers, such as git, Lima/`limactl`, and Pi inside the sandbox.
- Preserve testability without relying on installed `wt`/`sb` binaries.
- Keep changes aligned with the current multi-module `go.work` layout.

## Acceptance Criteria

1. `agent-dispatch` no longer shells out to `wt` or `sb` for worktree or sandbox operations, and no production code references commands named `wt` or `sb` for those operations.
2. `worktree-manager` exposes a public package that lets `ad` create/configure a headless worktree and get its path using the existing internal workspace behavior.
3. `sandbox-manager` exposes a public package that lets `ad` create/provision the sandbox and run sandbox commands, including piped stdio execution for Pi RPC, using the existing internal sandbox behavior.
4. Existing `wt` and `sb` CLI commands continue to build and pass their tests.
5. `agent-dispatch` tests are updated to validate direct integration seams rather than CLI argv wrappers, and `agent-dispatch` builds/tests without requiring installed `wt`/`sb` binaries.
6. User-facing docs/design accurately state that `ad` no longer requires installed `wt`/`sb` binaries while still requiring the underlying runtime dependencies.

## Chosen Approach

### 1. Add narrow public facades

Add `pkg/` packages to the owning tools:

- `worktree-manager/pkg/worktree`
  - Public client constructor, likely `New(options ...Option) (*Client, error)` or `New() (*Client, error)`.
  - Methods:
    - `AddHeadless(repoRoot, branch string) (string, error)`
    - `Path(repoRoot, branch string) (string, error)`
  - Internally load `wt` config, create the same git/tmux/runner dependencies, and delegate to `internal/workspace.Service`.
  - Prefer a quiet default logger so `ad` does not unexpectedly emit `wt` logs.

- `sandbox-manager/pkg/sandbox`
  - Public client constructor, likely `New(options ...Option) (*Client, error)` or `New() (*Client, error)`.
  - Public `Process` interface matching the stdio/Wait/Kill methods that `ad` needs.
  - Methods:
    - `Create() error`
    - `Exec(workdir string, args ...string) ([]byte, error)`
    - `StartPiped(workdir string, args ...string) (Process, error)`
  - Internally load `sb` config, create the Lima client and runner, and delegate to `internal/sandbox.Service`.
  - `Exec` should preserve `sb exec --workdir` semantics by invoking the underlying Lima shell with the requested workdir.

Keep the existing `cmd/` packages using their current internal services unless switching them to the public facades makes the implementation smaller without changing behavior.

### 2. Update `ad` integration

Replace `agent-dispatch/internal/worktree` and `agent-dispatch/internal/sandbox` CLI-wrapper usage with direct package calls.

Recommended shape in `cmd/ad`:

- Define small local interfaces for the operations `runTask` and `runSupervisor` need.
- Add package-level factory variables or constructor helpers for production clients so tests can inject fakes.
- In `runTask`:
  - Create/use worktree client.
  - Call `AddHeadless(repo.Root, branch)` and use the returned worktree path directly.
  - Create/use sandbox client.
  - Call `Create()`.
  - Keep or relocate the worktree visibility check by running `test -d <worktreePath>` in the sandbox and preserving the current mount-guidance error message.
- In `runSupervisor`:
  - Start Pi with sandbox `StartPiped(task.WorktreePath, argv...)`.

After migration, remove obsolete `agent-dispatch/internal/worktree` and `agent-dispatch/internal/sandbox` packages and their CLI-argv tests, replacing them with tests around the injected interfaces/factories and visibility-check behavior.

### 3. Module dependencies

Update `agent-dispatch/go.mod` to require:

- `github.com/averycrespi/agent-tools/worktree-manager`
- `github.com/averycrespi/agent-tools/sandbox-manager`

The root `go.work` already includes all three modules, so local development should resolve them without replace directives.

Run `go mod tidy` in affected modules after implementation.

## Documentation Impact

Update these docs if the implementation follows this plan:

- `agent-dispatch/README.md`
  - Remove or soften any implication that installed `wt` and `sb` binaries are required by `ad`.
  - Clarify that `ad` still uses worktree-manager and sandbox-manager behavior directly and still requires Lima, git, and Pi availability/configuration.
- `agent-dispatch/DESIGN.md`
  - Update the V1 architecture and boundaries sections: `ad` calls public packages from `wt`/`sb` rather than shelling out to their CLIs.
- `worktree-manager/README.md` and/or `worktree-manager/DESIGN.md` if a public package section is appropriate.
- `sandbox-manager/README.md` and/or `sandbox-manager/DESIGN.md` if a public package section is appropriate.
- `AGENTS.md` / `CLAUDE.md` files only if the architecture guidance needs to mention the new public package layer.

## Assumptions / Open Questions

- The new public APIs are intended primarily for in-repo consumers like `agent-dispatch`, not as a fully stable external SDK.
- The public facades can expose a quiet default logger and optional logger customization if needed.
- The current worktree path and sandbox execution semantics should remain unchanged.
- `ad` should keep the current worktree mount visibility preflight and user-facing remediation text.

## Ordered Tasks

1. Add `worktree-manager/pkg/worktree` facade.
   - Construct internal dependencies with `internal/config.Load`, `internal/exec.NewOSRunner`, `internal/git.NewClient`, `internal/tmux.NewClient`, and `internal/workspace.NewService`.
   - Expose `AddHeadless` returning the resulting worktree path.
   - Expose `Path`.
   - Add focused tests using package-level constructors or injected test dependencies if needed.

2. Add `sandbox-manager/pkg/sandbox` facade.
   - Construct internal dependencies with `internal/config.Load`, `internal/exec.NewOSRunner`, `internal/lima.NewClient`, and `internal/sandbox.NewService`.
   - Expose `Create`, `Exec`, and `StartPiped`.
   - Define a public `Process` interface.
   - Add tests for workdir argument behavior and error propagation where feasible.

3. Update `agent-dispatch` to use the public packages.
   - Add module requirements.
   - Replace direct uses of `agent-dispatch/internal/worktree` and `agent-dispatch/internal/sandbox`.
   - Introduce small local interfaces/factory seams for tests.
   - Preserve current mount-visibility error guidance.

4. Remove obsolete `agent-dispatch` CLI-wrapper packages.
   - Delete `agent-dispatch/internal/worktree` and `agent-dispatch/internal/sandbox` after all references are gone.
   - Replace argv-focused tests with behavior/fake-client tests.

5. Update documentation.
   - Apply the Documentation Impact section.
   - Keep docs concise and avoid duplicating CLI docs in package docs.

6. Tidy and format affected modules.
   - Run `go mod tidy` in `agent-dispatch`, `worktree-manager`, and `sandbox-manager` as needed.
   - Run goimports/formatting on changed Go files.

## Verification Checklist

- `cd worktree-manager && make test`
- `cd sandbox-manager && make test`
- `cd agent-dispatch && make test`
- `cd worktree-manager && make build`
- `cd sandbox-manager && make build`
- `cd agent-dispatch && make build`
- Grep production `agent-dispatch` code for `"wt"` and `"sb"` command invocations and confirm none remain for worktree/sandbox manager operations.
- Confirm docs listed in Documentation Impact were updated, or explicitly note why a listed doc did not need changes.

## Known Issues / Follow-ups

- This does not remove all subprocess usage. The underlying managers still shell out to git, tmux where applicable, and `limactl`; that is expected.
- This does not create a stable external SDK guarantee for all `wt`/`sb` behavior. If external users need that later, design a broader API deliberately.
- The facades may make some `ad` unit tests less argv-centric; prefer fake client behavior tests over reproducing internals of `wt`/`sb` in `ad` tests.
