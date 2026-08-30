# Release verification and acceptance evidence

Audience: Maintainers producing release evidence

Purpose: Run and adopt exact-revision acceptance evidence.

This guide owns the purpose-based verification DAG, clean-revision acceptance, report adoption, native and external evidence classification, and failure discipline. The Makefile and generated help are authoritative for available target definitions; this guide explains ownership and composition.

See [maintainer guidance](../CLAUDE.md) for package ownership and editing invariants, [DESIGN](../DESIGN.md) for normative security and compatibility boundaries, and [frontend development](frontend-development.md) for the separate live-reload and production-asset workflows.

## Purpose-based verification DAG

The public verification interface is organized by evidence purpose:

- `test-unit` owns race-enabled ordinary and contract correctness at count one.
- `test-integration` owns real SQLite/filesystem and compatibility boundaries at count one.
- `test-e2e` owns the complete nonbrowser E2E real-binary suite at count one.
- `test-security` owns source, secret-sink, durable-artifact, and privacy evidence at count one.
- `test-stress` repeats only the five named stress scenarios at its configured repeat count.
- `test-keyring-native` owns deterministic material checks plus typed native-provider evidence.
- Browser workflow, privacy, visual, accessibility, and cross-browser leaves each have one owner; `test-browser` is their developer-facing aggregate.
- Frontend typecheck, deterministic generated assets, supply-chain guards, and vulnerability audit remain separate explicit owners.

`test` aggregates `test-unit` and `test-integration` only. It is a developer convenience, not a release leaf. `test-browser` is also an aggregate.

`accept` invokes disjoint leaves directly and never invokes `test`, `test-browser`, `audit`, or another aggregate that would repeat evidence. The complete nonbrowser E2E suite runs once. Only the five named stress scenarios repeat; migration, retention, protocol, browser, and real-binary matrices remain count one. Transitive ownership keeps generated-asset verification from running under multiple names.

Run `make help` for exact target spelling and required variables. Do not preserve removed aliases in scripts; update CI to the purpose-based owner.

## CI mapping

| CI intent                 | Owners                                                                                                          |
| ------------------------- | --------------------------------------------------------------------------------------------------------------- |
| Fast source feedback      | formatting, nonmutating Gateway verification, frontend typecheck                                                |
| Ordinary correctness      | `test-unit` and `test-integration`                                                                              |
| Runtime compatibility     | complete `test-e2e`                                                                                             |
| Browser behavior          | disjoint workflow, privacy, visual, accessibility, and cross-browser leaves                                     |
| Security and supply chain | `test-security`, direct Go vulnerability scan, frontend supply-chain verification, frontend vulnerability audit |
| Scheduling sensitivity    | `test-stress` only; never repeat unrelated packages                                                             |
| Platform capability       | `test-keyring-native` with explicit typed classification                                                        |
| Release candidate         | external qualification followed by one clean `accept` run                                                       |

Pull requests may run individual leaves in separate jobs, but the final acceptance profile remains the authority for exact multiplicity and report composition. Do not wrap leaf jobs in an aggregate that causes the same package or browser workflow to execute twice.

The root repository deliberately keeps Gateway acceptance separate from other-tool checks. Run the non-Gateway owner and Gateway release owner independently rather than nesting one inside the other.

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
