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

`po` stores configuration in `~/.config/po/config.json`. V1 config fields are `database_path`, `workflow_dir`, and `artifact_parent_dir`; `po config refresh` writes those fields with resolved XDG defaults. `--workflow-dir` overrides the configured workflow directory for one invocation, while `PO_WORKFLOW_DIR` and `PO_ARTIFACT_PARENT_DIR` override the corresponding config fields.

`po` stores state in SQLite at the configured `database_path`, defaulting to `~/.local/state/po/po.db`. The schema tracks accepted run requests, workflow runs, step runs, and artifacts. Workflow run states are `starting`, `running`, `succeeded`, `failed`, `stopping`, `stopped`, and `unknown`. Step runs use the same states plus `skipped`. Inspection commands reconcile non-terminal workflow runs whose supervisor process is missing to `unknown` rather than leaving them indefinitely active.

## Artifacts

Each workflow run uses a per-run artifact root under the configured artifact parent. `po run` verifies the artifact parent exists on the host and is visible inside the sandbox at the same absolute path before creating the workflow run. `po cleanup` removes terminal workflow worktrees through `wt` safety semantics and removes `po`-owned artifact directories directly.

## Dashboard

The dashboard is loopback-only and uses `po`-owned local auth. The printed token URL sets an HttpOnly `po-auth` cookie, while bearer tokens remain available for scripted API calls. Its `/dashboard/api/...` JSON APIs and `/dashboard/events` SSE stream are read-only and expose workflow summaries, workflow details, step runs, artifacts, bounded supervisor log windows, supervisor log paths, and backing `pd` task/run IDs. Dashboard auth state is independent of `pd`.

## MCP

`po mcp` serves a stdio-only MCP server for trusted local clients. It has no network listener and inherits the launching user's filesystem permissions. Its tools are annotated read-only/local and must not mutate SQLite, start or stop workflows, reconcile stale supervisor state, clean up or remove resources, contact supervisors, or call external services.

The v1 tools are `list_workflows`, `get_workflow`, `list_workflow_runs`, `get_workflow_run`, `get_workflow_run_logs`, and `get_step_logs`. Workflow-definition tools read only from the configured workflow directory. `list_workflows` reports validation status and summary metadata without prompt bodies; `get_workflow` may expose raw YAML and prompts only for the explicitly named definition under that directory.

Run-oriented MCP responses use explicit view structs rather than raw store structs. They expose persisted state, progress, cleanup fields, backing `pd` IDs, artifacts, and local path metadata already used by inspection surfaces, while omitting full workflow YAML snapshots, full inputs JSON, raw prompt text, environment values, and unbounded event/log content. Supervisor and step logs are exposed only through offset/limit bounded log tools with size, next-offset, and truncation metadata.
