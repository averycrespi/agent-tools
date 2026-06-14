# pi-orchestrator Design

`po` is the workflow coordination layer above `pd`: it owns workflow definitions, runs, step state, artifacts, and workflow-run supervision. `pd` remains responsible for individual Pi tasks and task-run supervision.

## Layering

- `po`: workflow YAML loading, input validation, workflow-run persistence, serial step orchestration, artifact validation, dashboard/read APIs.
- `pd`: individual task runs, Pi process supervision, task logs/events, and task lifecycle state.
- `wt`: workflow worktree creation.
- `sb`: sandbox lifecycle and sandbox-visible mounts used by `pd`.

`po` does not import `pd/internal/...`; it calls `pi-dispatcher/pkg/dispatcher` for backing task runs.

## Workflow model

V1 workflow definitions contain only `name`, `description`, `repo`, flat typed `inputs`, named `agents`, and ordered `steps` with `id`, `agent`, optional `needs`, `prompt`, and declared `artifacts`.

A workflow run creates exactly one worktree. All step runs use that same worktree. Step execution is serial and deterministic: ready steps are considered in workflow-file order, and V1 runs at most one backing `pd` task at a time.

Prompts render validated inputs and the `artifact_path "name"` helper. Artifact names are unique across a workflow so any step can reference any declared artifact path, including artifacts from previous steps. Artifact paths are relative to the workflow run artifact root. After a backing step succeeds, missing required artifacts fail the step and the workflow; dependent steps are recorded as `skipped`.

## State

`po` stores state in SQLite at `~/.local/state/po/po.db` by default. The schema tracks accepted run requests, workflow runs, step runs, and artifacts. Workflow run states are `starting`, `running`, `succeeded`, `failed`, `stopping`, `stopped`, and `unknown`. Step runs use the same states plus `skipped`. Inspection commands reconcile non-terminal workflow runs whose supervisor process is missing to `unknown` rather than leaving them indefinitely active.

## Artifacts

Each workflow run uses a per-run artifact root under the configured artifact parent. `po run` verifies the artifact parent exists on the host and is visible inside the sandbox at the same absolute path before creating the workflow run. `po cleanup` removes terminal workflow worktrees through `wt` safety semantics and removes `po`-owned artifact directories directly.

## Dashboard

The dashboard is loopback-only and uses `po`-owned local auth. The printed token URL sets an HttpOnly `po-auth` cookie, while bearer tokens remain available for scripted API calls. Its `/dashboard/api/...` JSON APIs and `/dashboard/events` SSE stream are read-only and expose workflow summaries, workflow details, step runs, artifacts, bounded supervisor log windows, supervisor log paths, and backing `pd` task/run IDs. Dashboard auth state is independent of `pd`.
