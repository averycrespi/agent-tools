# Pi Orchestrator Root Artifacts Plan

## Goal

Redesign `po` workflow artifacts so artifacts are declared once at the workflow root, referenced in prompts through template data like inputs, and validated through concise per-step `produces` gates. This makes cross-step artifact consumption natural and establishes a schema foundation for later deterministic checks.

## Background / Repo Context

- `pi-orchestrator` coordinates durable Pi workflows above `pd`; its V1 workflow model currently includes root `inputs`, root `agents`, and ordered `steps` with step-local `artifacts` (`pi-orchestrator/DESIGN.md`, `pi-orchestrator/internal/workflow/definition.go`).
- Current YAML structurally attaches artifacts to steps: `Step` has `Artifacts []Artifact`, and `Artifact` has `name`, `path`, `required` (`pi-orchestrator/internal/workflow/definition.go`).
- Current validation already treats artifact names as workflow-global by rejecting duplicate names across all steps, and artifact paths must be relative and non-traversing (`pi-orchestrator/internal/workflow/definition.go`).
- Current prompt rendering already builds artifact paths from all steps and exposes them through `{{ artifact_path "name" }}`, so cross-step path references mostly work even though the declaration shape is step-scoped (`pi-orchestrator/internal/supervisor/supervisor.go`).
- Current completion validation only re-stats artifacts declared by the just-finished step and fails on missing required artifacts. It does not reject empty files, directories, or support generalized check results (`pi-orchestrator/internal/supervisor/supervisor.go`).
- Current persistence keys artifacts by `(workflow_run_id, step_id, name)` and dashboard/status APIs show artifacts with a `StepID`, so root-level artifacts require a store/API shape change (`pi-orchestrator/internal/store/store.go`, `pi-orchestrator/cmd/po/dashboard_mux_test.go`, `pi-orchestrator/cmd/po/inspect_command_test.go`).
- The project convention is that `DESIGN.md` is source of truth for intended behavior; update it deliberately when the intended design changes (`AGENTS.md`).

## Acceptance Criteria

- AC-1: Workflow definitions support root-level `artifacts:` alongside existing root `inputs`, `agents`, and `steps`; artifact names are unique, template-safe identifiers, artifact paths are relative, non-traversing file paths, and step-scoped artifact declarations are rejected as unsupported.
- AC-2: Step prompts can reference artifact paths with `{{ .Artifacts.<name> }}`; `{{ artifact_path "name" }}` is removed from the supported template API and workflows using it fail `po lint`/workflow validation before run admission, and supervisor parsing for persisted definitions reports a clear error.
- AC-3: Steps support a concise `produces:` object for artifact postconditions, where each entry is `produces.<name>: exists` or `produces.<name>: non_empty`; checks run only after the backing `pd` step reports success.
- AC-4: Artifact `produces` checks require regular files only. `exists` passes only when the artifact path exists and is a regular file; `non_empty` additionally requires file size greater than zero bytes and implies `exists`. Directories, missing files, and empty files fail the relevant check.
- AC-5: Failed `produces` checks mark the step failed, mark the workflow failed, and cause dependent steps to be skipped under existing dependency semantics, with outcomes that name the artifact and failed check.
- AC-6: Store, status, and dashboard representations treat artifacts as workflow-level metadata initialized for every declared root artifact at workflow-run creation or supervisor start, and record per-step check results/provenance derived from completed `produces` checks rather than from step ownership.
- AC-7: README and DESIGN examples use root-level artifacts, `.Artifacts.<name>` prompt references, and `produces` postconditions; old step-scoped artifact/helper examples are removed.
- AC-8: Focused tests cover validation, prompt rendering, supervisor `produces` success/failure, store/API/dashboard/status behavior, and clean rejection of old syntax.

## Non-Goals / Out of Scope

- Supporting directory artifacts.
- Supporting arbitrary artifact names that require `index` syntax or helper functions.
- Supporting pre-step artifact preconditions or implicit dependencies based on artifact references.
- Adding command/test execution checks beyond artifact file checks in this change.
- Preserving backward compatibility for old step-scoped artifact declarations or the `artifact_path` helper.
- Implementing parallel step execution or artifact locking.

## Constraints

- Artifact declarations move to workflow root as a clean breaking change.
- Artifact names must be template-safe identifiers, recommended validation pattern: `[A-Za-z_][A-Za-z0-9_]*`.
- Artifact paths remain relative to the per-run workflow artifact root and must not escape it.
- Artifacts are files only; runtime checks must reject directories.
- `produces` gates are postconditions only for V1: they do not control scheduling before a step starts.
- Step execution remains serial and deterministic in workflow-file order, subject to existing `needs` behavior.
- Keep repo conventions: commands delegate to internal packages, external commands go through runner interfaces, and update `DESIGN.md` when intended behavior changes.

## Chosen Approach

Introduce a root artifact catalog and separate it from per-step artifact production checks. Root `artifacts:` declares named file paths once, making artifacts a workflow-level resource like `inputs` and `agents`. Step prompts receive an `.Artifacts` map/object with absolute artifact paths under the run artifact root. Each step can declare `produces` checks for artifacts it is expected to produce or validate; the supervisor evaluates those checks after the backing step succeeds and stores both workflow-level artifact status and step-level check results.

This approach is preferred because it aligns the schema with the already workflow-global artifact namespace, removes misleading step ownership from artifact declarations, makes cross-step consumption explicit in prompts, and keeps the common artifact-gate syntax compact.

## Design Decisions

- D1: Use a clean breaking schema change. Old step-scoped `artifacts:` and `artifact_path` helper usage should be rejected rather than normalized or deprecated.
- D2: Use `.Artifacts.<name>` as the only prompt reference style for artifacts. Restrict artifact names to template-safe identifiers to avoid a second indexed syntax.
- D3: Use `produces:` as the step-level artifact postcondition container, with values limited initially to `exists` and `non_empty`. `non_empty` implies `exists`.
- D4: Treat all artifact checks as postconditions. Root artifact path rendering is always available, but workflow scheduling still depends on step order and any existing dependency semantics.
- D5: Treat artifacts as files only. Runtime `exists` checks must require a regular file; `non_empty` must require a regular file with size > 0 bytes.
- D6: Derive provenance from step `produces` checks. Root artifact metadata is workflow-level; step-level check results show which steps validated or produced artifacts.

## Proposed Workflow YAML Shape

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
artifacts:
  findings:
    path: findings.md
steps:
  - id: review
    agent: reviewer
    prompt: |
      Review PR #{{ .Inputs.pr_number }}.
      Write findings to {{ .Artifacts.findings }}.
    produces:
      findings: non_empty
```

## Implementation Notes

- Workflow definition and validation:
  - Modify `pi-orchestrator/internal/workflow/definition.go` so `Definition` has root `Artifacts map[string]Artifact` and `Step` no longer has `Artifacts`.
  - Add a `Produces map[string]ArtifactProduceCheck` or equivalent field under `Step`, where each value parses as `exists` or `non_empty`.
  - Add `artifacts` to the top-level allowlist and let `KnownFields(true)` reject old step-scoped `artifacts` naturally.
  - Validate artifact names with the template-safe identifier rule; consider applying the same helper to future root maps where dot-template access is expected.
  - Validate `produces` references point to declared root artifacts and each check value is one of `exists` or `non_empty`.
- Prompt rendering:
  - Replace `artifactPathsForWorkflow` step iteration with root artifact catalog iteration.
  - Execute templates with data containing both `Inputs` and `Artifacts`.
  - Add workflow-load or lint-time prompt template validation so unsupported functions such as `artifact_path` are caught by `po lint` before run admission; reuse the same template data shape as supervisor rendering so lint and run behavior match.
  - Remove the `artifact_path` FuncMap helper. Unknown helper usage should fail lint/workflow validation for current definitions and fail supervisor parsing with a clear wrapped step error for persisted definitions.
- Supervisor artifact checks:
  - Replace `artifactsForStep` / `missingRequiredArtifacts` with root artifact status refresh plus per-step `produces` check evaluation.
  - Evaluate `produces` only after the backing step succeeds. If the backing step fails/stops, preserve current behavior and do not mask that result with artifact-check failures.
  - Use `os.Stat` plus `Mode().IsRegular()` for file checks; use `Size() > 0` for `non_empty`.
  - Error/outcome strings should include the step id, artifact name, relative path, and check, e.g. `artifact findings failed non_empty check: file is empty`.
- Store/API/dashboard/status:
  - Replace or migrate the `artifacts` table so artifact rows are workflow-level, keyed by `(workflow_run_id, name)`, without `step_id` as ownership.
  - Initialize workflow-level artifact rows for all declared root artifacts during run admission or at supervisor start before the first step executes, so `po status` and dashboard can display declared artifacts even before a `produces` check runs. Prefer run admission if the workflow run already has its artifact root and definition snapshot available there; otherwise make supervisor initialization idempotent.
  - Add a table or fields for step-level check results, e.g. `(workflow_run_id, step_id, kind, target, check, passed, message, updated_at)`. Keep the schema generic enough to later represent command/test checks.
  - Update `GetWorkflowRunDetail`, dashboard JSON, status output, cleanup/rm metadata removal, and tests that currently expect artifact `StepID` ownership.
  - For existing local SQLite databases, implement a forward migration that can drop/recreate or transform artifact metadata as appropriate for this development-stage tool. Do not preserve old workflow definitions as valid.
- Documentation:
  - Update `pi-orchestrator/README.md` and `pi-orchestrator/DESIGN.md` examples and behavior text.
  - Search the repo for `artifact_path`, step-scoped `artifacts:`, and `required: true` artifact examples and update or remove them.

## Documentation Impact

Update `pi-orchestrator/README.md` and `pi-orchestrator/DESIGN.md` because workflow YAML, prompt rendering, artifact semantics, `produces` behavior, and dashboard/status artifact meaning change. No new standalone docs are required unless the implementation adds enough check syntax to outgrow the README.

## Testing / Verification

- V1 for AC-1/AC-2: Run `go test ./internal/workflow ./internal/supervisor ./cmd/po` from `pi-orchestrator` and verify tests cover root artifact parsing, identifier validation, old step-scoped artifact rejection, `.Artifacts.<name>` rendering, and `artifact_path` rejection through `po lint`/workflow validation and persisted-definition supervisor parsing.
- V2 for AC-3/AC-4/AC-5: Add supervisor tests where a fake runner succeeds while artifact files are missing, empty, directories, and non-empty regular files; verify step/workflow states and outcomes match expected `produces` results.
- V3 for AC-6: Run store, dashboard, inspect, cleanup/rm tests and verify workflow detail JSON/status output expose workflow-level artifacts and per-step `produces` check results without step-owned artifact rows.
- V4 for AC-7: Run `rg "artifact_path|artifacts:|required: true" pi-orchestrator` and inspect matches to ensure docs/examples/tests reflect the new model or intentionally test old-syntax rejection.
- V5 overall: Run `make test` in `pi-orchestrator`; if practical before handoff completion, run `make lint` or `make audit` and report exact results.

## Risks and Mitigations

- Risk: Persisted historical workflow snapshots using old syntax may fail if a supervisor resumes under the new parser. Mitigation: document this as an intentional clean break; tests should verify failures are clear rather than panics or unknown hangs.
- Risk: Removing `step_id` from artifact ownership can reduce dashboard clarity. Mitigation: add explicit step `produces` check result records so provenance is visible as “validated by step X” rather than encoded as ownership.
- Risk: `.Artifacts.<name>` can tempt users to infer dependency semantics from prompt references. Mitigation: docs must state references only render paths; execution order still requires `needs`.
- Risk: `produces` is intentionally artifact-specific, so future deterministic checks need sibling fields rather than being grouped under one generic step object. Mitigation: keep stored check results generic and add future step-level fields such as `checks:` when needed.
- Risk: Directory artifacts may be useful later for screenshots or multi-file reports. Mitigation: enforce files only now per product decision; directory support can be a future explicit artifact type.

## Assumptions

- `po` is early enough that a clean breaking workflow schema change is acceptable.
- Existing root artifact paths remain under the configured per-run artifact root and use the current artifact parent mount behavior.
- Step prompts should receive absolute artifact paths, matching current `artifact_path` behavior.
- `produces` checks are pass/fail gates for now; richer severity or warning-only checks are out of scope.

## Handoff Summary

Implement `.plans/2026-06-14-po-root-artifacts.md` in `pi-orchestrator`. Convert artifacts from step-scoped declarations to a root artifact catalog, expose paths through `.Artifacts.<name>`, add post-step `produces` checks for regular-file existence and non-empty content, update persistence/status/dashboard to separate workflow-level artifacts from step-level check results, and update README/DESIGN. Complete only after the acceptance criteria are verified with concrete test and documentation evidence.
