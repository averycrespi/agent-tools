# Pi Orchestrator V1 Plan

## Goal

Add `pi-orchestrator` (`po`) as a local workflow layer above `pi-dispatcher` (`pd`). V1 runs explicit workflow definitions: `po run <workflow>` validates typed inputs, creates a durable workflow run, executes each workflow step through a backing `pd` task run, validates required artifacts, and exposes `pd`-aligned inspection/control commands.

The original ideation document remains the broader reference: `.plans/2026-06-10-pi-orchestrator-ideation.md`. This plan is the V1 implementation handoff.

## Background / Repo Context

- This repository is a monorepo of independent Go tools. New Go tools should mirror the existing module shape and be added to the root `Makefile` and `go.work`.
- Existing tool pattern to copy: `pi-dispatcher/`, including `cmd/<binary>/main.go`, Cobra command organization, `internal/store`, docs, `Makefile`, `.golangci.yml`, `README.md`, `DESIGN.md`, `CLAUDE.md`, and `AGENTS.md` symlink.
- Layering:
  - `po` owns workflows, workflow runs, steps, step runs, workflow artifacts, and workflow-run supervision.
  - `pd` owns individual agent task runs, task-run supervisors, task-run state, stop/wait/log behavior, sandbox execution, and worktree/sandbox integration.
  - `wt` owns worktree creation and setup.
  - `sb` owns sandbox lifecycle and sandbox command execution.
- `po` should integrate with `pd` through a stable `pi-dispatcher/pkg/...` API, not by shelling out to the `pd` CLI or importing `pd/internal/...` packages.
- `pd` already has the nearest implementation patterns for SQLite state, detached supervisors, lifecycle states, wait/stop/log controls, and CLI behavior. Relevant references:
  - `pi-dispatcher/DESIGN.md`
  - `pi-dispatcher/cmd/pd/run_impl.go`
  - `pi-dispatcher/internal/store/store.go`
- `sb` supports writable configured mounts with host/sandbox path identity. V1 artifacts should live outside the workflow worktree under `po` state and be mounted into sandboxed step runs at the same absolute path.

## Acceptance Criteria

- AC-1: The repository contains a new `pi-orchestrator/` Go module, included in `go.work` and the root `Makefile`, with the standard tool docs and command layout.
- AC-2: `po list`, `po show <workflow>`, and `po lint <workflow>` load workflow YAML files from the configured workflow directory and report validation errors for malformed definitions.
- AC-3: Workflow definitions support V1 fields only: `name`, `description`, `repo`, flat typed `inputs`, named `agents`, and `steps` with `id`, `agent`, optional `needs`, `prompt`, and declared `artifacts`.
- AC-4: `po run <workflow> --input k=v ...` validates inputs, rejects missing/invalid/unknown inputs, persists a run request and workflow run, creates exactly one worktree for the workflow run, and starts/adopts one workflow-run supervisor.
- AC-5: The workflow supervisor starts ready steps by creating backing `pd` task runs through a stable `pd/pkg/...` API, records each backing `pd` task run ID, and never creates per-step worktrees.
- AC-6: All step runs in a workflow run use the same workflow worktree.
- AC-7: Required artifact validation is enforced: when a backing `pd` task run succeeds but a required artifact is missing, the step fails, dependent steps are marked `skipped`, and the workflow run fails.
- AC-8: Workflow and step lifecycle states align with `pd`: workflow runs use `starting`, `running`, `succeeded`, `failed`, `stopping`, `stopped`, `unknown`; step runs use the same states plus `skipped`.
- AC-9: `po ps`, `po status <run>`, `po wait <run>`, `po logs <run>`, `po stop <run>`, and `po rm <run>` behave consistently with the corresponding `pd` concepts.
- AC-10: `po dashboard` starts a read-only loopback web UI, inspired by `pd dashboard`, for exploring workflow runs, steps, artifacts, workflow supervisor logs, and backing `pd` task/run metadata.
- AC-11: Dashboard routes are local-authenticated, expose read-only APIs, and do not mutate workflow, worktree, artifact, or `pd` task state.
- AC-12: `po wait <run>` blocks until terminal state and exits non-zero unless the workflow run succeeded.
- AC-13: `po logs <run>` shows workflow supervisor logs and pointers to the underlying `pd` task logs for each step.
- AC-14: `po stop <run>` stops the workflow supervisor and the currently running backing `pd` task run, then records the workflow run as stopped.
- AC-15: Deterministic tests cover workflow loading/validation, input validation, graph execution ordering, shared-worktree behavior, artifact validation failure, wait exit behavior, stop behavior, terminal run removal, and read-only dashboard API behavior.
- AC-16: Documentation explains the V1 workflow model, CLI commands, dashboard, state/artifact locations, and how `po` relates to `pd`.

## Non-Goals / Out of Scope

- Built-in scheduling, polling, webhooks, or event adapters.
- Workflow queues or deferred admission; `po run` either rejects or admits immediately.
- Any workflow YAML fields, CLI commands, state fields, API endpoints, or supervisor behaviors not explicitly listed in this V1 plan.
- Dashboard mutation actions; the dashboard is read-only.
- Merge-bot behavior or automatic PR merging.

## Constraints

- Use root-level workflow definition commands: `po list`, `po show`, and `po lint`; do not nest them under `po workflow`.
- Use `workflow` / `workflow run` / `step` / `step run` language in `po`. Keep `task` / `task run` language in `pd`.
- Mirror `pd` CLI and lifecycle wording where concepts overlap.
- Use one worktree per workflow run. All step runs in that workflow run share it.
- Store workflow artifacts outside the worktree under `po` state. Mount the artifact root into step sandboxes at the same absolute path.
- Keep V1 inputs flat and typed: `string`, `integer`, `boolean`, with `required`, `default`, and `enum` validation.
- Treat workflow input values and previous-step artifacts as untrusted content when rendered into prompts.
- Do not import `pd/internal/...` from `po`; expose the necessary dispatcher behavior through `pi-dispatcher/pkg/...`.
- Follow the monorepo Go conventions in `AGENTS.md` and existing tool `CLAUDE.md` files.

## Chosen Approach

Create a new `pi-orchestrator/` module that mirrors the existing Go tool shape. `po` owns workflow definition parsing, input validation, workflow-run state, step dependency evaluation, artifact path management, and a per-workflow-run supervisor. Each executable step is delegated to `pd` through a new stable package API. `pd` remains the only layer that starts and supervises Pi agent task runs.

V1 uses a simple admission model: `po run` validates the request and either fails before creating a workflow run or persists the run and launches/adopts its supervisor. There is no scheduler, queue, trigger adapter, or global daemon.

## V1 Workflow Definition

Workflow files live in the configured workflow directory, defaulting to:

```text
~/.config/po/workflows/<name>.yaml
```

V1 shape:

```yaml
name: pr-review
description: Review a pull request and write findings
repo: "{{ .Inputs.repo }}"

inputs:
  repo:
    type: string
    required: true
  pr_number:
    type: integer
    required: true
  head_sha:
    type: string
    required: false

agents:
  reviewer:
    model: gpt-5.1-codex
    skills: [review]
  verifier:
    model: gpt-5.1-codex
    skills: [review]

steps:
  - id: review
    agent: reviewer
    prompt: |
      Review PR #{{ .Inputs.pr_number }} in {{ .Inputs.repo }}.
      Write findings to {{ artifact_path "findings" }}.
    artifacts:
      - name: findings
        path: findings.md
        required: true

  - id: verify
    agent: verifier
    needs: [review]
    prompt: |
      Review the findings in {{ artifact_path "findings" }} for clarity.
      Write final notes to {{ artifact_path "final-notes" }}.
    artifacts:
      - name: final-notes
        path: final-notes.md
        required: true
```

Rules:

- `name` must match the filename stem or validation fails.
- `repo` is rendered from typed inputs and identifies the repository/worktree target for the workflow run.
- `inputs` are flat. Unknown `--input` keys fail validation.
- `agents` define named `pd` execution configurations. V1 should pass through only fields supported by the new `pd/pkg/...` API.
- Every step must reference an existing agent.
- `needs` defaults to empty.
- V1 supports explicit dependencies; cycles fail lint/run validation.
- `artifacts[].path` is relative to the workflow run artifact root. The template helper `artifact_path "name"` renders the absolute host/sandbox-identical path for the named artifact in the current workflow run.
- A required artifact must exist after the backing `pd` task run succeeds or the step fails.

## CLI Surface

```text
po list                                # list workflow definitions
po show <workflow>                     # show a workflow definition
po lint <workflow>                     # validate YAML and input schema
po run <workflow> [--input k=v ...]    # validate inputs, create/adopt workflow-run supervisor
po ps                                  # list workflow runs
po status <run>                        # show workflow run, steps, artifacts, backing pd task IDs
po wait <run>                          # block until terminal; non-zero unless succeeded
po logs <run>                          # workflow supervisor logs + pointers to pd step logs
po stop <run>                          # stop supervisor and current backing pd task run
po rm <run>                            # forget terminal workflow run metadata/logs
po dashboard                           # read-only loopback web UI for workflow runs and steps
```

CLI behavior should follow existing `pd` conventions for argument validation, error printing, exit codes, status/log display style, and dashboard serving behavior where practical.

## State Model

Use an independent SQLite database at:

```text
~/.local/state/po/po.db
```

Required V1 tables or equivalent persisted records:

- `workflows`: loaded workflow metadata and definition hash.
- `run_requests`: accepted `po run` requests with workflow name, validated typed inputs JSON, source, and requested-by metadata when available.
- `workflow_runs`: workflow run ID, workflow name/version snapshot, rendered repo/worktree metadata, state, timestamps, supervisor metadata, supervisor log path, and terminal outcome.
- `step_runs`: workflow run ID, step ID, selected agent, dependency state, backing `pd` task run ID, state, timestamps, and terminal outcome.
- `artifacts`: workflow run ID, step ID, artifact name, relative path, absolute path, required flag, existence status, and timestamps.

The store should also provide dashboard-oriented read queries that return workflow-run summaries and workflow-run detail with nested step runs, artifacts, supervisor log paths, and backing `pd` task/run metadata.

Do not include fields that only support out-of-scope behavior.

Lifecycle states:

```text
workflow: starting -> running -> succeeded | failed | stopping -> stopped | unknown
step:     starting -> running -> succeeded | failed | stopping -> stopped | skipped | unknown
```

State reconciliation should mirror `pd`: if inspection observes a non-terminal run whose supervisor is missing and cannot be explained as actively running, report/persist `unknown` rather than inventing a separate stale state.

## Dashboard Behavior

`po dashboard` should mirror the shape and safety model of `pd dashboard` one level higher:

- Start an on-demand loopback-only HTTP server, defaulting to a `po` port and supporting `--host`, `--port`, and `--no-open` flags.
- Print an authenticated URL and open the browser by default.
- Serve an embedded single-page UI plus read-only JSON APIs and polling-backed SSE snapshots.
- Use local auth equivalent to `pd dashboard` because workflow prompts, repository paths, artifact paths, and `pd` log paths can be sensitive.
- Show workflow-run summaries: workflow name, run ID, state, created/updated times, repo/worktree, current/terminal outcome, and step counts by state.
- Show workflow-run detail: validated inputs, rendered repo/worktree, step graph, each step's selected agent, state, required artifacts, backing `pd` task/run IDs, and links or paths to backing `pd` logs/events.
- Show bounded workflow supervisor log windows. Do not duplicate full `pd` logs; surface backing `pd` log pointers and enough metadata for the user to jump to `pd` when needed.
- Never expose stop/remove/retry/mutation actions. The dashboard reads persisted state as-is; CLI inspection/control remains responsible for reconciliation and mutation.

Suggested public dashboard surface, matching `pd` naming where practical:

```text
GET /dashboard/                       # embedded Explorer UI
GET /dashboard/api/runs               # workflow-run summaries
GET /dashboard/api/runs/{id}          # workflow-run detail with steps/artifacts/pd metadata
GET /dashboard/api/runs/{id}/logs     # bounded workflow supervisor log windows
GET /dashboard/events                 # polling-backed SSE snapshots
```

## Supervisor Behavior

A workflow-run supervisor owns exactly one workflow run.

Responsibilities:

- Load the persisted workflow-run snapshot and validated inputs.
- Ensure the workflow run has exactly one worktree.
- Ensure the workflow run artifact root exists outside the worktree.
- Ensure backing `pd` step runs can access the artifact root at the same absolute path inside the sandbox.
- Start ready step runs when dependencies have succeeded.
- Create backing `pd` task runs through the stable `pd/pkg/...` API.
- Persist each backing `pd` task run ID.
- Wait for or watch backing `pd` task terminal state.
- Validate required artifacts after a backing `pd` task succeeds.
- Mark dependent steps `skipped` when a required dependency fails.
- Mark the workflow run terminal when all reachable required steps are terminal.
- Write workflow supervisor logs separately from `pd` task logs.

Failure contract:

- If a backing `pd` task run fails, the step fails.
- If a backing `pd` task run succeeds but a required artifact is missing, the step fails.
- If a required step fails, dependent steps are skipped and the workflow fails.
- If all required executed steps succeed, the workflow succeeds.

## `pd/pkg` Integration Requirements

Expose a stable dispatcher API sufficient for `po` to create and observe backing task runs without importing `pd/internal/...` or scraping CLI output.

Required capabilities:

- Start a task run with:
  - existing workflow-owned worktree,
  - rendered prompt,
  - selected agent execution options,
  - cleanup behavior appropriate for a shared workflow worktree,
  - artifact root mount request/configuration.
- Return durable task/run identifiers and log locations.
- Query task-run state and result metadata.
- Wait for a task run to reach terminal state.
- Stop a running task run.

The API should preserve `pd` ownership: `pd` still creates task/run records, supervises Pi execution, manages task-run logs, and owns task-run lifecycle transitions.

## Artifact Handling

For each workflow run, create an artifact root under `po` state, for example:

```text
~/.local/state/po/runs/<workflow-run-id>/artifacts/
```

For each declared artifact, persist both the relative path and rendered absolute path. The artifact root must be mounted into step sandboxes at the same absolute path so prompts can safely refer to exact paths using `artifact_path`.

Do not store handoff artifacts inside the workflow worktree. The worktree should remain focused on repository changes and be safe to inspect with normal git tooling.

## Implementation Notes

Repo-level changes:

- Add `pi-orchestrator/` as a new Go module.
- Add `./pi-orchestrator` to `go.work`.
- Add `pi-orchestrator` to the root `Makefile` `TOOLS` list.

Suggested `pi-orchestrator/` package areas:

- `cmd/po`: Cobra CLI commands.
- `internal/config`: XDG config/state path resolution.
- `internal/workflow`: YAML loading, validation, typed input parsing, template rendering, dependency graph checks.
- `internal/store`: SQLite schema and persistence.
- `internal/supervisor`: per-workflow-run supervisor loop.
- `internal/artifact`: artifact path rendering and validation.
- `internal/dispatcher`: adapter around the new `pd/pkg/...` API.
- `internal/dashboard`: embedded read-only dashboard UI, API handlers, SSE snapshots, and bounded supervisor log windows.

Likely `pi-dispatcher/` changes:

- Add `pkg/...` API for creating, observing, waiting for, and stopping task runs.
- Refactor existing `pd run` logic so CLI and package API share core behavior instead of duplicating dispatch logic.
- Add support for using a caller-provided existing worktree.
- Add support for configuring the `po` artifact root mount for a task run.

Follow existing `pd` test/store patterns where possible. Keep implementation details small and explicit; do not introduce a generic workflow engine abstraction beyond what V1 needs.

## Documentation Impact

Add the standard docs for the new tool:

- `pi-orchestrator/README.md`: user-facing V1 overview, install/build command, workflow YAML example, CLI/dashboard reference, artifact/state locations, and relationship to `pd`.
- `pi-orchestrator/DESIGN.md`: source of truth for the V1 architecture, lifecycle behavior, and dashboard safety/API surface.
- `pi-orchestrator/CLAUDE.md`: development commands, package layout, and tool-specific conventions.
- `pi-orchestrator/AGENTS.md`: symlink to `CLAUDE.md`.

Update root docs only where needed to list the new tool:

- root `README.md`
- root `Makefile` tool list
- root `go.work`

## Testing / Verification

- V1: `go test ./...` inside `pi-orchestrator/` passes.
- V2: `go test ./...` inside `pi-dispatcher/` passes after adding/refactoring the public package API.
- V3: root `make build` succeeds and includes `pi-orchestrator`.
- V4: root `make test` succeeds or any unrelated pre-existing failure is documented with evidence.
- V5: focused CLI tests or integration tests prove:
  - invalid workflow YAML fails `po lint`,
  - missing/invalid/unknown inputs fail `po run`,
  - valid `po run` creates persisted run request/workflow run/step runs,
  - multi-step workflows use one shared workflow worktree,
  - required artifact absence fails the step and workflow,
  - dependent steps are marked `skipped` after dependency failure,
  - `po wait` exits non-zero for failed workflow runs,
  - `po stop` stops the current backing `pd` task run,
  - `po logs` reports supervisor log location and backing `pd` log pointers,
  - `po rm` removes terminal workflow-run metadata/log references according to documented semantics,
  - dashboard APIs return workflow-run summaries/details/log windows,
  - dashboard handlers reject unauthenticated requests,
  - dashboard routes do not expose mutating methods or actions.
- V6: documentation review confirms no V1 docs describe out-of-scope behavior as implemented.

## Risks and Mitigations

- Risk: `pd/pkg/...` API shape expands too much. Mitigation: expose only the capabilities needed by `po` V1 and keep `pd` lifecycle ownership intact.
- Risk: sharing one worktree across multiple steps creates cleanup ambiguity. Mitigation: workflow-run ownership is explicit; `po` owns the workflow worktree lifetime, and backing `pd` task runs must not clean it up independently.
- Risk: artifact paths differ between host and sandbox. Mitigation: require host/sandbox path identity for the artifact root and test prompt-rendered artifact paths.
- Risk: required artifact validation is mistaken for semantic success. Mitigation: document that V1 validates only process success plus declared artifact existence.
- Risk: command names collide semantically between workflow definitions and workflow runs. Mitigation: keep definition commands as `list/show/lint`; keep run inspection/control as `ps/status/wait/logs/stop/rm`.
- Risk: dashboard grows into a control surface. Mitigation: copy `pd dashboard`'s read-only safety model and test that dashboard routes expose no mutation actions.

## Assumptions

- A separate `pi-orchestrator/` tool is the intended shape, matching the monorepo's independent-tool convention.
- `pd` can be refactored to expose stable package APIs without changing existing `pd` CLI behavior.
- The first implementation can use local filesystem workflow definitions and local SQLite state only.
- The implementation can use fake or test dispatcher backends for deterministic unit tests where full Pi/sandbox execution would be too expensive.

## Handoff Summary

Implement `.plans/2026-06-13-pi-orchestrator-v1.md` as the V1 `po` tool. Keep scope limited to workflow YAML loading/validation, typed inputs, named agents, `po run`, per-workflow-run supervision, `pd`-backed step execution, required artifact validation, SQLite state, `pd`-aligned inspection/control commands, and a read-only `pd`-inspired dashboard. Complete only after every acceptance criterion is satisfied with concrete command output, tests, and documentation updates.
