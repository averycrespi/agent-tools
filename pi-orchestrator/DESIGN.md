# pi-orchestrator Design

`po` owns workflow definitions, workflow runs, step runs, workflow artifacts, and workflow-run supervision. `pd` remains responsible for individual Pi agent task runs and task-run supervision.

## Layering

- `po`: workflow YAML loading, input validation, workflow-run persistence, serial step orchestration, artifact validation, dashboard/read APIs.
- `pd`: individual task runs, Pi process supervision, task logs/events, and task lifecycle state.
- `wt`: workflow worktree creation.
- `sb`: sandbox lifecycle and sandbox-visible mounts used by `pd`.

`po` does not import `pd/internal/...`; it calls `pi-dispatcher/pkg/dispatcher` for backing task runs.

## Workflow model

V1 workflow definitions contain only `name`, `description`, `repo`, flat typed `inputs`, named `agents`, and ordered `steps` with `id`, `agent`, optional `needs`, `prompt`, and declared `artifacts`.

A workflow run creates exactly one worktree. All step runs use that same worktree. Step execution is serial and deterministic: ready steps are considered in workflow-file order, and V1 runs at most one backing `pd` task at a time.

Prompts render validated inputs and the `artifact_path "name"` helper. Artifact paths are relative to the workflow run artifact root. After a backing step succeeds, missing required artifacts fail the step and the workflow; dependent steps are recorded as `skipped`.

## State

`po` stores state in SQLite at `~/.local/state/po/po.db` by default. The schema tracks accepted run requests, workflow runs, step runs, and artifacts. Workflow run states are `starting`, `running`, `succeeded`, `failed`, `stopping`, `stopped`, and `unknown`. Step runs use the same states plus `skipped`.

## Dashboard

The dashboard is loopback-only and uses `po`-owned local auth. Its JSON APIs are read-only and expose workflow summaries, workflow details, step runs, artifacts, supervisor log paths, and backing `pd` task/run IDs. Dashboard auth state is independent of `pd`.
