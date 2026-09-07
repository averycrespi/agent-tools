# Release verification and acceptance evidence

Audience: Release owners and maintainers preparing release evidence

Purpose: Prepare, run, and adopt exact-revision acceptance evidence without turning release acceptance into a development loop.

Authority: Normative maintainer evidence procedure

This guide owns the purpose-based verification DAG, clean-revision acceptance, report adoption, native and external evidence classification, and failure discipline. The Makefile and generated help are authoritative for available target definitions; this guide explains ownership and composition. Freezing a candidate, completing external qualification, and adopting a report are release-owner actions; coding agents should perform them only when the requested workflow authorizes those externally consequential steps.

See [maintainer and agent guidance](../../CLAUDE.md) for package ownership and editing invariants, the [DESIGN overview](../../DESIGN.md) for normative security and compatibility boundaries, and [frontend development](frontend-development.md) for the separate live-reload and production-asset workflows.

## Purpose-based verification DAG

The public verification interface is organized by evidence purpose:

- `test-unit` owns race-enabled dependency-light contract, parser, discovery, credential-authority, event, and lifecycle tests at count one, without initializing SQLite or launching Gateway processes.
- `test-integration` owns component, real SQLite/filesystem, and compatibility boundaries at count one, including both ordinary and integration-tagged component tests in one execution.
- `test-harness` owns runner, fixture, report, selector, and native-classifier self-tests at count one. These are not product E2E or native-provider evidence.
- `test-material` owns deterministic credential-material composition at count one.
- `test-serve-temporary` owns the disposable runner lifecycle and cleanup.
- `test-e2e` owns nonbrowser real-binary and E2E-tagged composition-provider behavior at count one, excluding harness self-tests.
- `test-security` owns source, secret-sink, durable-artifact, and privacy evidence at count one.
- `test-stress` repeats only the five named stress scenarios at its configured repeat count.
- `test-keyring-native` owns deterministic material checks plus typed native-provider evidence.
- Browser workflow, privacy, visual, accessibility, and cross-browser leaves each have one owner; `test-browser` is their developer-facing aggregate.
- Frontend typecheck, deterministic generated assets, supply-chain guards, and vulnerability audit remain separate explicit owners.

`test` aggregates `test-unit`, `test-integration`, `test-harness`, `test-material`, and `test-serve-temporary`. It is a developer convenience, not a release leaf. `test-browser` is also an aggregate: it expands all five browser leaves through one inventory/planner invocation, retaining separate race/count-one Go test commands, per-leaf deadlines, Gateway builds, and cleanup. Final acceptance selects the harness and temporary leaves directly; material executes only inside its native wrapper, never a second time as a direct final leaf.

`accept` invokes disjoint leaves directly and never invokes `test`, `test-browser`, `audit`, or another aggregate that would repeat evidence. The complete nonbrowser E2E suite runs once. Only the five named stress scenarios repeat; migration, retention, protocol, browser, and real-binary matrices remain count one. Transitive ownership keeps generated-asset verification from running under multiple names.

Run `make help` for exact target spelling and required variables. `make suite-inventory` emits source/package/name ownership with build tags and platform applicability. From `mcp-gateway/`, `go run ./test/acceptance/cmd suite-plan <owner>` emits the checked executable plan; `suite-plan test-browser` shows the aggregate's five disjoint leaf commands. Source/package ownership and Go build constraints generate exact selectors; Make, CI, and acceptance use that single planner rather than handwritten test-name registries. It rejects unknown/conflicting tags, missing owners, empty leaves, and omitted or duplicate/foreign selection. Platform-inapplicable identities remain visible rather than disappearing. Runnable examples and fuzz seeds are included. Do not preserve removed aliases in scripts; update CI to the purpose-based owner.

## CI mapping

| CI intent                 | Owners                                                                                                          |
| ------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Fast source feedback      | formatting, nonmutating Gateway verification, frontend typecheck                                                |
| Ordinary correctness      | `test-unit`, `test-integration`, `test-harness`, and `test-material`                                            |
| Runtime compatibility     | complete `test-e2e`                                                                                             |
| Browser behavior          | disjoint workflow, privacy, visual, accessibility, and cross-browser leaves                                     |
| Security and supply chain | `test-security`, direct Go vulnerability scan, frontend supply-chain verification, frontend vulnerability audit |
| Scheduling sensitivity    | `test-stress` only; never repeat unrelated packages                                                             |
| Platform capability       | `test-keyring-native` with explicit typed classification                                                        |
| Release candidate         | external qualification followed by one clean `accept` run                                                       |

Repository CI runs Gateway lint independently of its unit, integration, harness/material, E2E, and temporary-runner leaves when Gateway is affected; all selected jobs remain mandatory in Required. Tool/role-owned compatible Go caches use fresh run/attempt save keys so newly filled dependency, build, and linter material can be retained. Cache reuse is setup optimization, not exact-revision evidence; `main`, manual, and scheduled runs select every tool. The stable required gate and conservative path-selection policy are described in the [repository CI guide](../../../README.md#ci). The Gateway E2E job first warms the exact `e2e` command build cache under a separate five-minute setup bound, so cold module downloads and compilation do not consume the harness's 30-second per-process build budget. The harness still builds and owns its disposable binary; no prebuilt artifact is injected and no runtime or test deadline is relaxed. This development profile does not replace browser, native, security, or other final-acceptance owners.

Pull requests may run individual leaves in separate jobs, but the final acceptance profile remains the authority for exact multiplicity and report composition. Do not wrap leaf jobs in an aggregate that causes the same package or browser workflow to execute twice.

The root repository aggregate deliberately excludes Gateway acceptance. The Gateway `accept` profile includes `make check-other-tools` once as its disjoint `repository-other-tools` leaf; do not wrap `accept` in another aggregate or run that leaf separately as part of the same evidence set.

## Harness safety invariants

Shared tests use mutex-safe fake time and finite deterministic entropy, real owner-only `0700` temporary data roots with symlink/type/owner/mode validation, and a streaming canary scanner that detects cross-buffer leaks without returning the canary in errors.

The common real-binary runner requires a positive timeout and per-stream byte cap, captures stdout and stderr separately, reports truncation and exit status, owns an identity-revalidated process group, applies bounded TERM/KILL/reap cleanup when its context expires, and can signal a bounded started process for lifecycle tests. The single E2E harness and acceptance executor inherit that ownership through an outer cleanup ledger and fail on surviving processes, listeners, or temporary roots. Nested suite executors clean their owned command groups but leave the inherited ledger to the outer acceptance owner; cleaning that ledger inside a leaf would terminate its still-live parent. Component-specific fault hooks, protocol fixtures, and barriers remain with their owning packages.

The retention E2E owner seeds the real 65,536-row boundary with one set-based transaction, then exercises real Gateway startup, one call, eviction, backup, events, shutdown, and private-artifact scanning. Its stopped artifact observation uses a read-only connection and the existing 65,537-row overflow bound rather than reconstructing storage authority a second time. Artifact observation is not startup/integrity evidence: production initialization, startup validation, and the dedicated integrity/migration/fault owners remain unchanged.

`TestServeFirstSignalDeadlineRetainsUncleanMarker` owns the real compiled graceful-shutdown deadline, exit 7, listener closure, verified process cleanup, unclean marker, and recovery. `TestCLIServePostStartFailureOutput` in the CLI package owns human/JSON terminal-problem formatting and singular acknowledgement without waiting through that deadline again. `TestCLIServeOutputLifecycle` retains real-binary human/JSON startup and pre-start failure output; separate E2E owners retain second-signal forcing, active transport cancellation, and late-completion fencing.

## Freeze the candidate

Before producing release evidence:

1. Finish focused repairs and integration checks.
2. Commit every tracked definition and behavior change.
3. Require a clean worktree and record the candidate revision.
4. Freeze Make, npm, runner, profile, manifest, schema, and executable definitions.
5. Prepare candidate-bound external evidence.
6. Run `make qualify-external-evidence`, then run `make accept REPORT=/absolute/path/report.json` once.

The report records the exact clean revision, command and profile hashes, immutable definition inputs, command timings, timeout/termination facts, artifacts, and cleanup results. Any tracked change after evidence preparation creates a new candidate and invalidates candidate-bound evidence.

## Native and external evidence

The native wrapper checks self-test mode before selecting real material, and destructive-isolation eligibility before selecting native-provider tests. Forced classifier self-tests never launch those suites. Real wrapper execution runs `test-material` once, then selects only native-tagged executable identities rather than reselecting ordinary keyring or material tests. Evidence command identities name the suite runner; old raw-command classifications are historical and rejected.

Native keyring evidence is typed `passed`, `skipped`, or `failed`. `skipped` is an explicit additive gap, never success. `failed` blocks. Do not enable a destructive native prerequisite on a non-disposable user account merely to remove a gap.

External browser and accessibility evidence is prepared for the exact candidate, executable digest, checklist, and artifact set. Run `make qualify-external-evidence` to create any missing deterministic templates and validate their schema, paths, digests, provenance, and policy classification before acceptance. Existing evidence is never overwritten. An available probe leaves a pending template for a named operator to complete with real evidence; a permitted unavailable probe can qualify immediately.

A deterministic probe may emit only policy-permitted typed unavailability. Required blocking evidence cannot be synthesized. Additive unavailable evidence remains visible in the report; an available environment or human claim requires real artifacts and provenance. If required blocking evidence is unavailable, stop the release rather than writing a pass.

Never carry an external sidecar forward after the candidate revision, executable, checklist, profile, or manifest changes.

## Report compatibility

Reports from superseded report definitions are incompatible with the current acceptance profile. They remain historical files only and are not upgraded, relabeled, or silently adopted.

A report parser must reject a mismatched profile, schema, revision, definition hash, command set, native classification, external sidecar, or cleanup record. Product API and durable-data compatibility do not imply compatibility of maintainer-facing acceptance artifacts.

## Failure discipline

Treat acceptance as evidence production, not as the debugging loop.

If a full run fails:

1. Identify the failing leaf and build the narrowest deterministic reproduction.
2. Reproduce the relevant concurrency, process, filesystem, browser, or timing boundary.
3. Run only the affected named scenario at higher count when repetition is justified.
4. Run the affected package or leaf once normally.
5. Commit the correction at a clean checkpoint.
6. Refresh candidate-bound native or external evidence if definitions or revision changed.
7. Run full acceptance again only after the narrow owner is stable.

Do not rerun `accept` unchanged after a failure. Do not raise timeouts instead of diagnosing the boundary, repeat package-wide fixtures, suppress vulnerability findings, fabricate unavailable evidence, or leave surviving processes/listeners/temp roots for another run.

A second full failure in the same area requires reassessing the reproduction and ownership before another final attempt.

## Report adoption

`make adopt-acceptance-report REPORT=/absolute/path/report.json ADOPTION=/absolute/path/adoption.json` performs no-check adoption of one already-produced report. It reparses and hashes the immutable artifact, verifies the same candidate revision and clean worktree, rechecks profile/command/manifest definitions, native and external classifications, blocking results, and cleanup evidence, then writes a distinct adoption artifact.

Adoption does not rerun product checks and never converts a failure, unavailable blocking cell, stale candidate, dirty revision, or mismatched definition into success. Use it only when no tracked state or acceptance definition changed after report production.

Historical reports remain auditable through version control, but only a report for the exact current candidate and definition set can be adopted.
