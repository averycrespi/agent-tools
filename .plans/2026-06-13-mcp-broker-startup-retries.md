# MCP Broker Startup Retries Plan

## Goal

Improve mcp-broker startup resilience when configured backend MCP servers are temporarily unavailable. The broker should retry backend startup connection/discovery with bounded, configurable backoff, continue launching after exhausted retries, and make exhausted backend failures visible in the dashboard instead of only in logs.

## Background / Repo Context

- mcp-broker is the MCP proxy under `mcp-broker/`; tool-specific development commands are documented in `mcp-broker/CLAUDE.md`.
- Startup loads config in `cmd/mcp-broker/serve.go`, then creates the backend manager with `server.NewManager(ctx, cfg.Servers, cfg.ToolPatches, logger)` before wiring MCP tool handlers and the dashboard.
- `internal/server/manager.go` currently connects configured servers serially in `NewManager`. `connect` failures are logged with `"failed to connect to backend"` and skipped; `discover` then calls `ListTools` for connected backends, logs `"failed to list tools"`, and skips those failures.
- The design doc explicitly says failed backends are logged and skipped rather than blocking startup (`mcp-broker/DESIGN.md`, Server manager / design decisions). This behavior should remain true after retries are exhausted.
- Server-specific config lives in `config.ServerConfig` (`mcp-broker/internal/config/config.go`) and already contains per-backend transport behavior such as `http_timeout_seconds`. Existing per-server default behavior uses zero-value interpretation in server code, e.g. `httpBackendTimeout`.
- The dashboard currently knows only about tools through `ToolLister.Tools() []server.Tool` in `mcp-broker/internal/dashboard/dashboard.go`; `GET /api/tools` returns `{"tools": [...]}` sorted by tool name. It has no backend health/status model.
- The Tools tab in `mcp-broker/internal/dashboard/index.html` groups discovered tools client-side by the prefix before the first dot. A backend that failed during startup and discovered no tools is currently invisible in the UI.
- Relevant existing tests:
  - `mcp-broker/internal/config/config_test.go` covers config load/save/default behavior, including `http_timeout_seconds`.
  - `mcp-broker/internal/server/manager_test.go` covers discovery, tool patching, connect transport routing, and failed HTTP connect behavior.
  - `mcp-broker/internal/dashboard/dashboard_test.go` covers `/api/tools`, `/api/rules`, and dashboard API serialization.

## Acceptance Criteria

- AC-1: Backend startup connection and initial tool discovery use a bounded retry policy so transient startup timing failures can recover before the backend is marked unavailable.
- AC-2: Retry policy is configurable per backend in `config.ServerConfig`, with documented defaults and clear absent/zero/negative semantics that preserve backward compatibility as much as practical.
- AC-3: The broker still starts and serves dashboard/MCP endpoints when a backend exhausts retries; healthy backends remain usable.
- AC-4: Exhausted backend failures are visible in the dashboard Tools tab, including backend name, failed phase (`connect` or `list_tools`), attempt count, and a concise error summary that does not add new config-derived secret content beyond the errors already logged today.
- AC-5: The dashboard continues to show discovered tools grouped by backend, including successful backends with tools and failed backends with no tools.
- AC-6: Existing tool listing, rule matching, MCP registration, approval flow, and audit behavior remain unchanged for successfully connected backends.
- AC-7: README and DESIGN docs explain retry configuration, startup behavior after exhausted retries, dashboard status display, and the fact that a backend that exhausts retries requires broker restart for discovery unless future runtime recovery is added.

## Non-Goals / Out of Scope

- Do not add runtime/background reconnection after startup. If a backend exhausts startup retries and later becomes available, users should restart the broker.
- Do not dynamically register MCP tools after the broker has started serving. Current startup-time MCP handler registration from discovered tools remains the model.
- Do not change policy rules, approval semantics, audit schema, OAuth storage, or tool-call retry behavior.
- Do not fail the broker process solely because one backend is unavailable after retries.
- Do not add a global health-check service or external monitoring endpoint beyond the dashboard/API data needed for the UI.

## Constraints

- Follow mcp-broker conventions from `mcp-broker/CLAUDE.md`: wrap errors with context, keep loopback-only security unchanged, and do not add nil-logger call paths without guards/defaults.
- Keep failed-backend startup behavior non-fatal, matching the existing design intent.
- Bound startup delays. Retries must use a startup-specific per-attempt timeout so `http_timeout_seconds` does not multiply into multi-minute dashboard unavailability.
- Do not add config-derived secret content to dashboard status. Dashboard status may show a concise form of the same backend errors already logged today, but must not include raw env vars, headers, expanded command strings, or other config-derived details.
- Preserve JSON config compatibility. New fields should be optional pointer fields with `omitempty` so missing fields can use defaults while explicit zero can disable the corresponding behavior.
- Use deterministic tests; avoid sleeps except tightly bounded test synchronization where existing patterns already use them.

## Chosen Approach

Add per-server startup retry settings to `config.ServerConfig`, apply them inside `server.Manager` around each backend's `connect` and initial `ListTools` call, track backend status in the manager, and expose that status to the dashboard. The Tools tab should render backend status first, then discovered tool details, so failed backends are visible even when they have no tools.

The retry policy should be simple and bounded: configure startup retry count, fixed backoff, and a startup-specific per-attempt timeout. For this feature, prefer fixed backoff with optional future extensibility over exponential jitter: the user need is startup timing recovery, not high-scale distributed retry behavior. The startup attempt timeout is separate from `http_timeout_seconds`, which continues to govern normal backend requests after startup.

Because MCP tools are registered once at startup in `serve.go`, exhausted failures should remain visible but not auto-recover until restart. This avoids a larger dynamic registration/reconnection design.

## Design Decisions

- D1: Retry configuration is per backend, not top-level. Rationale: backend availability and startup timing differ by server, and `ServerConfig` already owns per-backend transport behavior such as `http_timeout_seconds`.
- D2: Retry both startup phases: transport/initialize `connect`, and initial `ListTools` discovery. Rationale: an HTTP server may accept a connection before being ready to answer MCP `tools/list`, and current failures in either phase result in a skipped backend.
- D3: Preserve non-fatal exhausted failures. Rationale: the existing design explicitly prefers starting with remaining backends over failing the whole broker.
- D4: Surface backend status through an explicit API/data model, not by inferring from tools. Rationale: failed backends have no tools, and the dashboard currently cannot distinguish “no tools configured” from “backend failed.”
- D5: Treat OAuth/auth-interactive startup failures as non-retryable. Rationale: retrying user-cancelled or authorization-required OAuth flows can repeatedly open browsers or callback listeners, which is worse UX than surfacing a clear failed backend status.
- D6: Use stable backend status ordering. Rationale: manager state is map-backed, and deterministic status output avoids flaky tests and jumpy dashboard rendering.
- D7: Keep dashboard error display simple. Rationale: current startup errors are already logged; this feature should not create a broad redaction subsystem unless implementation introduces new config-derived error content.
- D8: No runtime reconnect loop in this iteration. Rationale: dynamic MCP tool registration and long-lived backend lifecycle management are materially larger changes than startup timing retries.

## Implementation Notes

- Config:
  - Extend `mcp-broker/internal/config/config.go` `ServerConfig` with optional pointer fields so JSON unmarshalling can distinguish absent from explicit zero. Suggested names:
    - `startup_retry_count *int json:"startup_retry_count,omitempty"`
    - `startup_retry_backoff_ms *int json:"startup_retry_backoff_ms,omitempty"`
    - `startup_timeout_seconds *int json:"startup_timeout_seconds,omitempty"`
  - Define semantics explicitly in code/docs:
    - `startup_retry_count`: absent uses default, recommended `3`; explicit `0` disables retries and performs one attempt only; positive value is retries after the first attempt; negative value is invalid config and should fail startup with a clear error.
    - `startup_retry_backoff_ms`: absent uses default, recommended `1000`; explicit `0` disables delay between attempts; positive value is the fixed delay; negative value is invalid config and should fail startup with a clear error.
    - `startup_timeout_seconds`: absent uses default, recommended `10`; explicit `0` disables the startup-specific per-attempt timeout; positive value is the per-attempt timeout; negative value is invalid config and should fail startup with a clear error.
  - Document worst-case startup delay in terms of configured backends, attempts, startup timeout, and backoffs. `http_timeout_seconds` must remain the normal backend request timeout and must not be the only bound on startup attempts.
  - Add helper functions in `internal/server` or `internal/config` to centralize effective retry count/backoff/timeout calculation and validation, similar to `httpBackendTimeout` but with pointer-aware absent-vs-zero behavior.

- Server manager:
  - Add a `BackendStatus` type in `mcp-broker/internal/server/manager.go` or a sibling file. Include fields suitable for JSON/API use, such as:
    - `name`
    - `status`: `connected`, `failed`, possibly `disabled/no_tools` only if needed
    - `phase`: empty for success, `connect` or `list_tools` for failures
    - `attempts`
    - `error`: concise error summary
    - optionally `tool_count`
  - Add status storage to `Manager`, initialized for every configured backend so failed/no-tool backends can be shown.
  - Add `BackendStatuses() []BackendStatus` on `Manager`, returning a copy sorted by backend name.
  - Wrap `connect` in retry logic. Log each failed attempt at warning/error level with backend name, phase, attempt, max attempts, and error summary.
  - Apply the startup-specific timeout to each `connect` attempt and each initial `ListTools` attempt. Respect parent context cancellation as well.
  - Add or use a retry classifier so OAuth/auth-interactive failures are non-retryable. User-cancelled OAuth, authorization denial, callback failures, or other clearly interactive auth failures should fail that backend immediately and surface status rather than opening repeated browser/callback flows.
  - Retrying `ListTools` can be implemented by moving discovery for each backend into a per-backend helper that applies the same retry policy to `backend.ListTools(ctx)`. Existing HTTP/SSE unauthorized retry inside `ListTools` should remain unchanged and count as part of one startup attempt, not as an additional startup retry attempt.
  - If `ListTools` exhausts retries after a successful connect, close that backend, remove it from active `backends`, and mark status failed at phase `list_tools`. If `Close()` fails, log the close failure but still mark startup discovery failed.
  - Ensure context cancellation stops retry loops and records/logs a meaningful failure without hanging shutdown.
  - Add the smallest internal test seam needed for deterministic retry tests. Keep exported `NewManager(...)` unchanged unless there is a strong reason to change it. Avoid real multi-second sleeps in unit tests.
  - Dashboard status should not include config-derived details such as env vars, headers, or expanded command strings. It may show a concise/truncated form of the backend error already being logged today; no broad redaction subsystem is required unless implementation introduces new error content beyond existing logged errors.

- Dashboard API/UI:
  - Extend `ToolLister` or add a separate interface, e.g. `BackendStatusLister`, with `BackendStatuses() []server.BackendStatus`.
  - Update `dashboard.New` only if necessary; since the manager is already passed as the `tools` argument, a type assertion from `d.tools` to `BackendStatusLister` can minimize constructor churn.
  - Prefer extending `GET /api/tools` to return `{"tools": [...], "backends": [...]}` so the Tools tab can load one endpoint and existing consumers that only read `tools` remain compatible. Alternatively add `GET /api/backends`, but then update UI to fetch both endpoints.
  - Update `mcp-broker/internal/dashboard/index.html` Tools tab rendering:
    - render one provider group per configured backend, sorted by backend name, even when a backend has zero tools;
    - show backend name, status badge (`connected` / `failed`), and tool count in the provider header;
    - for failed backends, expanded body shows failed phase (`connect` or `list_tools`), attempts, concise error, and copy such as “Failed during startup after N attempts. Restart broker after fixing this backend.”;
    - for connected backends with zero tools, show “No tools discovered” rather than failed;
    - keep existing tool detail expansion behavior for successful tools;
    - make the browser SSE “Connected/Disconnected” label clearly remain dashboard event-stream status, not backend health.

- Docs:
  - Update `mcp-broker/README.md` configuration example and Servers table with retry fields, default behavior, explicit-zero disable semantics, and negative-value rejection.
  - Update `mcp-broker/DESIGN.md` Server manager section and design decisions to describe retry-before-skip and dashboard failure status.
  - Check `mcp-broker/docs/launchd.md` for startup/troubleshooting content; update if it discusses backend startup failures or logs.
  - Update `mcp-broker/CLAUDE.md` only if adding a new durable development convention/gotcha that future agents need.

## Documentation Impact

Documentation updates are required because this changes user-visible configuration and dashboard behavior:

- `mcp-broker/README.md`: add retry fields to sample config and Servers table; explain exhausted failures and restart requirement.
- `mcp-broker/DESIGN.md`: update Server manager startup behavior and dashboard Tools tab description.
- `mcp-broker/docs/launchd.md`: inspect and update troubleshooting/startup wording if relevant.
- `mcp-broker/CLAUDE.md`: update only if implementation introduces a durable development convention or gotcha future agents need.

## Testing / Verification

- V1 for AC-1/AC-2: Add unit tests for effective retry configuration defaults, explicit-zero disable behavior, positive custom values, and negative-value config errors. Run `cd mcp-broker && go test -race ./internal/config ./internal/server` or the equivalent absolute-path command from repo root.
- V2 for AC-1/AC-3: Add deterministic server manager tests with a fake/flaky connector or retry helper showing a backend succeeds after an initial connect failure and remains available; assert attempts and discovered tools without real multi-second sleeps.
- V3 for AC-1/AC-3/AC-4: Add server manager tests showing connect exhaustion and `ListTools` exhaustion are non-fatal, record failed `BackendStatus`, do not expose tools for the failed backend, close/remove discovery-failed backends, and leave healthy backends usable.
- V4 for AC-1: Add tests for startup-specific per-attempt timeout behavior and context cancellation. Verify `http_timeout_seconds` remains separate from startup timeout.
- V5 for AC-1: Add tests for retry classification showing OAuth/auth-interactive failures are non-retryable while ordinary transient startup failures can be retried.
- V6 for AC-4/AC-5: Add dashboard API tests in `internal/dashboard/dashboard_test.go` verifying `/api/tools` includes backend statuses, returns stable backend ordering, and serializes failed backend phase/attempt/error without breaking existing `tools` output.
- V7 for AC-4/AC-5: Verify dashboard Tools tab provider groups are sorted by backend name and represent failed, connected-with-tools, and connected-with-zero-tools backends. Use manual browser inspection if the repo has no practical JS test pattern.
- V8 for AC-6: Run `cd mcp-broker && make test` and ensure existing tests pass.
- V9 for AC-7: Review changed docs and, if docs mention commands/config examples, verify examples are syntactically valid JSON/Markdown.
- V10 optional integration check: Run `mcp-broker serve` with one healthy backend and one intentionally unavailable local HTTP backend configured with short retry/backoff/timeout; verify logs show retries, the broker still serves the dashboard promptly, and Tools tab shows the failed backend status.

## Risks and Mitigations

- Risk: Serial retries delay all healthy backends and dashboard availability. Mitigation: use a startup-specific per-attempt timeout, keep defaults conservative, use finite backoff, respect context cancellation, and document total worst-case behavior.
- Risk: Retrying OAuth-related errors could repeatedly open browser flows or callback servers. Mitigation: classify clearly interactive auth/OAuth failures as non-retryable and test the classifier.
- Risk: Dashboard error text leaks secrets. Mitigation: do not add config-derived details such as env vars, headers, or expanded command strings to backend status; keep displayed errors concise. Do not build a broad redaction subsystem unless implementation introduces new error content beyond existing logged errors.
- Risk: `ListTools` failure after connect leaves an initialized backend process/client running but unusable. Mitigation: close and remove the backend on discovery exhaustion, and test cleanup behavior.
- Risk: Ambiguous config semantics for zero values. Mitigation: use pointer config fields, centralize effective config helpers, and document/test absent, explicit zero, positive, and negative values.
- Risk: Users may expect recovery after the backend comes up later. Mitigation: dashboard copy and docs should state restart is required after exhausted startup retries.

## Assumptions

- Per-backend retry policy is acceptable and preferable to a global-only policy.
- A small default retry policy should be enabled for all backends to address common startup ordering races.
- Showing backend failure status on the existing Tools tab is the right first UI surface because failed startup most directly affects available tools.
- Runtime rediscovery is intentionally deferred to a future feature.

## Handoff Summary

Suggested objective for an autonomous implementer:

```text
/goal Implement .plans/2026-06-13-mcp-broker-startup-retries.md. Complete only after every acceptance criterion is satisfied with concrete evidence from tests, docs, and at least one startup/dashboard verification path.
```

Implement this as a bounded startup retry and status-reporting feature: add per-server retry/backoff/startup-timeout config with pointer-field absent-vs-zero semantics, retry connect and initial tools/list before marking a backend failed, keep the broker non-fatal after exhausted backend failures, make OAuth/auth-interactive failures non-retryable, expose stable backend status through the dashboard Tools tab, and update README/DESIGN docs. Completion evidence should map each acceptance criterion to tests or manual verification output.
