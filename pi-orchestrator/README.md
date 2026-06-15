# pi-orchestrator

`po` coordinates durable Pi workflows above `pi-dispatcher` (`pd`). It validates typed inputs, creates a shared workflow worktree, runs ordered steps through backing `pd` tasks, validates required artifacts, and preserves workflow-level state for inspection and control.

## Workflow definitions

Workflow files are loaded from `~/.config/po/workflows` by default, or from `--workflow-dir`. The filename stem must match `name`.

Supported V1 fields are intentionally narrow:

```yaml
name: pr-review
description: Review a pull request
repo: "{{ .Inputs.repo }}"
inputs:
  repo:
    type: string
    required: true
  pr_number:
    type: integer
    required: true
agents:
  reviewer:
    model: gpt-5.1-codex
    skills: [review]
steps:
  - id: review
    agent: reviewer
    prompt: |
      Review PR #{{ .Inputs.pr_number }}.
      Write findings to {{ artifact_path "findings" }}.
    artifacts:
      - name: findings
        path: findings.md
        required: true
```

Inputs are flat and typed as `string`, `integer`, or `boolean`, with `required`, `default`, and `enum` validation. Step prompts can render `.Inputs` and the `artifact_path "name"` helper for any uniquely named artifact declared by the workflow, including artifacts from previous steps.

## Commands

```bash
po config path
po config refresh
po config edit
po list
po show <workflow>
po lint [--all|<workflow>]
po run <workflow> --input key=value
po ps
po status <run-id>
po wait <run-id> [--timeout 5m]
po logs <run-id>
po stop <run-id>...
po cleanup [--dry-run] [--include-unknown] [--all|<run-id>...]
po rm [--include-unknown] [--all|<run-id>...]
po dashboard [--host 127.0.0.1] [--port 8400] [--no-open]
po mcp
po token rotate
```

`po run` validates inputs before side effects, verifies the configured artifact parent is visible inside the sandbox at the same absolute path, creates one workflow worktree, creates a workflow artifact root, persists the run in SQLite, and records workflow metadata. Workflow steps are executed serially by the supervisor core and each executable step is represented by a backing `pd` task/run.

`po cleanup [--all|<run-id>...]` is best-effort and idempotent for terminal workflow runs. It delegates workflow worktree cleanup to each backing `pd` task so the dispatcher records cleanup state, then removes workflow artifacts and marks artifact metadata removed. If a run has no backing `pd` task IDs, `po` falls back to removing the workflow worktree directly. With `--all`, cleanup skips suspicious `unknown` runs by default; add `--include-unknown` to include them in bulk cleanup. Explicit run IDs may target `unknown` runs without the extra flag.

`po rm [--all|<run-id>...]` is best-effort and idempotent for terminal workflow runs. It delegates metadata/log removal to each backing `pd` task before forgetting workflow metadata; already-missing workflow or dispatcher metadata is treated as removed. With `--all`, rm skips suspicious `unknown` runs by default; add `--include-unknown` to include them in bulk removal. Explicit run IDs may target `unknown` runs without the extra flag.

`po mcp` starts a stdio MCP server for trusted local clients. It accepts no positional arguments, uses stdout only for MCP JSON-RPC, and exposes read-only inspection tools; it does not start, stop, reconcile, clean up, remove, or contact workflow supervisors.

## Configuration, state, and artifacts

Config file: `~/.config/po/config.json`.

```json
{
  "database_path": "~/.local/state/po/po.db",
  "workflow_dir": "~/.config/po/workflows",
  "artifact_parent_dir": "~/.local/state/po/artifacts"
}
```

`po config refresh` creates the file with defaults filled in, writing paths resolved to their actual XDG defaults. Set `database_path`, `workflow_dir`, or `artifact_parent_dir` to move the SQLite database, workflow definitions, or per-run artifact parent directory. A leading `~` is expanded. `--workflow-dir` overrides the configured workflow directory for one invocation; `PO_WORKFLOW_DIR` and `PO_ARTIFACT_PARENT_DIR` override the corresponding config fields.

Other state follows XDG paths by default:

- Dashboard auth token: `~/.config/po/auth-token`
- Run logs: `~/.local/state/po/runs/<run-id>`
- Artifacts: `~/.local/state/po/artifacts/<run-id>`

The artifact parent must be outside the workflow worktree and already mounted writable into the sandbox at the same absolute path.

## Dashboard

`po dashboard` serves a loopback-only HTTP dashboard, prints an authenticated URL, and opens it by default unless `--no-open` is set. To keep the dashboard running in the background on macOS, see the [launchd guide](docs/launchd.md). Visiting the printed token URL sets an HttpOnly `po-auth` cookie for `/dashboard/`; APIs also accept a bearer token for scripts. Dashboard routes are read-only:

- `GET /dashboard/`
- `GET /dashboard/api/runs`
- `GET /dashboard/api/runs/{id}`
- `GET /dashboard/api/runs/{id}/logs`
- `GET /dashboard/events`
- `GET /dashboard/favicon.svg`

Mutation methods are rejected. Rotate the dashboard auth token with `po token rotate`. `po` token state is separate from `pd` token state.

## MCP

`po mcp` is stdio-only and inherits the launching user's local filesystem permissions. Configure it only for trusted local MCP clients. The v1 tool surface is read-only/local (`ReadOnlyHint=true`, `OpenWorldHint=false`):

- `list_workflows` summarizes workflow definitions from the configured workflow directory, including validation status, inputs, agents, steps, artifact declarations, and source paths without prompt bodies.
- `get_workflow` returns one named workflow definition under the workflow directory, including raw YAML and parsed prompts because that definition content was explicitly requested.
- `list_workflow_runs` returns a default-bounded, offset/limit-paginated page of persisted run summaries and progress metadata without raw workflow YAML or inputs JSON.
- `get_workflow_run` returns persisted run, step, artifact, cleanup, backing `pd` ID, and local path metadata without raw workflow YAML, inputs JSON, prompt text, or unbounded event/log content.
- `get_workflow_run_logs` and `get_step_logs` return bounded offset/limit windows for supervisor logs and backing `pd` stdout/stderr logs, with size, next offset, and truncation metadata.

Unlike `po status` and `po logs`, MCP inspection displays persisted SQLite state as-is and does not reconcile stale supervisor process state.
