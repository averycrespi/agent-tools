# pi-orchestrator Design

`po` owns workflow definitions, workflow runs, step runs, workflow artifacts, and workflow-run supervision. Each executable step is delegated to `pd`, which remains responsible for individual Pi agent task runs and task-run supervision.

V1 supervision is serial: at most one backing `pd` task run is active for a workflow run at a time. All step runs in one workflow run share a single workflow worktree, and artifacts are stored outside that worktree under a configured artifact parent directory that is mounted into the sandbox at the same absolute path.

Workflow definitions are intentionally narrow: `name`, `description`, `repo`, flat typed `inputs`, named `agents`, and `steps` with `id`, `agent`, optional `needs`, `prompt`, and declared `artifacts`.
