# pi-orchestrator

`po` is a local workflow layer above `pi-dispatcher` (`pd`). V1 runs explicit YAML workflow definitions, validates typed inputs, creates a durable workflow run, delegates executable steps to `pd`, validates required artifacts, and exposes `pd`-aligned inspection/control commands.

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
po list
po show <workflow>
po lint <workflow>
po run <workflow> --input key=value
po ps
po status <run-id>
po wait <run-id> [--timeout 5m]
po logs <run-id>
po stop <run-id>
po cleanup [--dry-run] <run-id>
po rm <run-id>
po dashboard [--host 127.0.0.1] [--port 8400] [--no-open]
po token rotate
```

`po run` validates inputs before side effects, verifies the configured artifact parent is visible inside the sandbox at the same absolute path, creates one workflow worktree, creates a workflow artifact root, persists the run in SQLite, and records workflow metadata. Workflow steps are executed serially by the supervisor core and each executable step is represented by a backing `pd` task/run.

## State and artifacts

Defaults follow XDG paths:

- Config/workflows: `~/.config/po/workflows`
- Dashboard auth token: `~/.config/po/auth-token`
- SQLite database: `~/.local/state/po/po.db`
- Run logs: `~/.local/state/po/runs/<run-id>`
- Artifacts: `~/.local/state/po/artifacts/<run-id>`

`PO_WORKFLOW_DIR` and `PO_ARTIFACT_PARENT_DIR` can override workflow and artifact directories. The artifact parent must be outside the workflow worktree and already mounted writable into the sandbox at the same absolute path.

## Dashboard

`po dashboard` starts a loopback-only HTTP server, prints an authenticated URL, and opens it by default unless `--no-open` is set. To keep the dashboard running in the background on macOS, see the [launchd guide](docs/launchd.md). Visiting the printed token URL sets an HttpOnly `po-auth` cookie for `/dashboard/`; APIs also accept a bearer token for scripts. Dashboard routes are read-only:

- `GET /dashboard/`
- `GET /dashboard/api/runs`
- `GET /dashboard/api/runs/{id}`
- `GET /dashboard/api/runs/{id}/logs`
- `GET /dashboard/events`
- `GET /dashboard/favicon.svg`

Mutation methods are rejected. Rotate the `po` dashboard token with `po token rotate`. `po` token state is separate from `pd` token state.
