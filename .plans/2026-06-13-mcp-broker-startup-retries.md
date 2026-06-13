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
- AC-4: Exhausted backend failures are visible in the dashboard Tools tab, including backend name, failed phase (`connect` or `list_tools`), attempt count, and a sanitized/non-secret error summary.
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
- Bound startup delays. Retries must not create unbounded waits, and defaults should be conservative because backend startup is serial today.
- Do not display raw secrets in dashboard status. Backend config can include environment-expanded tokens in env vars, headers, URLs, or error strings.
- Preserve JSON config compatibility. New fields should be optional and `omitempty` where appropriate.
- Use deterministic tests; avoid sleeps except tightly bounded test synchronization where existing patterns already use them.

## Chosen Approach

Add per-server startup retry settings to `config.ServerConfig`, apply them inside `server.Manager` around each backend's `connect` and initial `ListTools` call, track backend status in the manager, and expose that status to the dashboard. The Tools tab should render backend status first, then discovered tool details, so failed backends are visible even when they have no tools.

The retry policy should be simple and bounded: configure a maximum retry count and a fixed or capped backoff duration in milliseconds/seconds. For this feature, prefer fixed backoff with optional future extensibility over exponential jitter: the user need is startup timing recovery, not high-scale distributed retry behavior. If exponential backoff is chosen during implementation, keep it capped and document the exact behavior.

Because MCP tools are registered once at startup in `serve.go`, exhausted failures should remain visible but not auto-recover until restart. This avoids a larger dynamic registration/reconnection design.

## Design Decisions

- D1: Retry configuration is per backend, not top-level. Rationale: backend availability and startup timing differ by server, and `ServerConfig` already owns per-backend transport behavior such as `http_timeout_seconds`.
- D2: Retry both startup phases: transport/initialize `connect`, and initial `ListTools` discovery. Rationale: an HTTP server may accept a connection before being ready to answer MCP `tools/list`, and current failures in either phase result in a skipped backend.
- D3: Preserve non-fatal exhausted failures. Rationale: the existing design explicitly prefers starting with remaining backends over failing the whole broker.
- D4: Surface backend status through an explicit API/data model, not by inferring from tools. Rationale: failed backends have no tools, and the dashboard currently cannot distinguish “no tools configured” from “backend failed.”
- D5: Sanitize dashboard error summaries. Rationale: config supports secret expansion and remote libraries may include sensitive request details in errors.
- D6: No runtime reconnect loop in this iteration. Rationale: dynamic MCP tool registration and long-lived backend lifecycle management are materially larger changes than startup timing retries.

## Implementation Notes

- Config:
  - Extend `mcp-broker/internal/config/config.go` `ServerConfig` with optional retry fields. Suggested names:
    - `startup_retry_count int json:"startup_retry_count,omitempty"`
    - `startup_retry_backoff_ms int json:"startup_retry_backoff_ms,omitempty"`
  - Define semantics explicitly in code/docs:
    - omitted or `0` count means use default retry count;
    - negative count disables retries or is clamped to zero only if this convention is documented and tested;
    - omitted or `0` backoff means use default backoff;
    - negative backoff should be treated safely, ideally as zero or default with tests.
  - Recommended default: small and conservative, e.g. 3 retries after the first attempt with 1s fixed backoff, unless implementation evidence suggests a different default. Keep total added delay acceptable for serial startup.
  - Add helper functions in `internal/server` or `internal/config` to centralize effective retry count/backoff calculation, similar to `httpBackendTimeout`.

- Server manager:
  - Add a `BackendStatus` type in `mcp-broker/internal/server/manager.go` or a sibling file. Include fields suitable for JSON/API use, such as:
    - `name`
    - `status`: `connected`, `failed`, possibly `disabled/no_tools` only if needed
    - `phase`: empty for success, `connect` or `list_tools` for failures
    - `attempts`
    - `error`: sanitized summary
    - optionally `tool_count`
  - Add status storage to `Manager`, initialized for every configured backend so failed/no-tool backends can be shown.
  - Add `BackendStatuses() []BackendStatus` on `Manager`, returning a stable sorted or copy-safe slice.
  - Wrap `connect` in retry logic. Log each failed attempt at warning/error level with backend name, phase, attempt, max attempts, and sanitized/structured error as appropriate.
  - Retrying `ListTools` can be implemented by moving discovery for each backend into a per-backend helper that applies the same retry policy to `backend.ListTools(ctx)`. If `ListTools` exhausts retries, close that backend if it will not be usable, remove it from active `backends`, and mark status failed at phase `list_tools`.
  - Ensure context cancellation stops retry loops and records/logs a meaningful failure without hanging shutdown.
  - Consider testability: inject a sleeper/backoff function or keep retry helpers small enough to test with zero backoff. Avoid real multi-second sleeps in unit tests.
  - Sanitize error summaries before storing status for dashboard. A minimal safe implementation can redact known token-like substrings and avoid including expanded headers/env. Prefer not to include command args/env in status error.

- Dashboard API/UI:
  - Extend `ToolLister` or add a separate interface, e.g. `BackendStatusLister`, with `BackendStatuses() []server.BackendStatus`.
  - Update `dashboard.New` only if necessary; since the manager is already passed as the `tools` argument, a type assertion from `d.tools` to `BackendStatusLister` can minimize constructor churn.
  - Prefer extending `GET /api/tools` to return `{"tools": [...], "backends": [...]}` so the Tools tab can load one endpoint and existing consumers that only read `tools` remain compatible. Alternatively add `GET /api/backends`, but then update UI to fetch both endpoints.
  - Update `mcp-broker/internal/dashboard/index.html` Tools tab rendering:
    - show a backend status section or status badges above each provider group;
    - include failed backends even when there are no tools;
    - keep existing tool detail expansion behavior for successful tools;
    - make the browser SSE “Connected/Disconnected” label clearly remain dashboard event-stream status, not backend health.
  - Add concise copy such as “Failed during startup after N attempts. Restart broker after fixing the backend.”

- Docs:
  - Update `mcp-broker/README.md` configuration example and Servers table with retry fields and default behavior.
  - Update `mcp-broker/DESIGN.md` Server manager section and design decisions to describe retry-before-skip and dashboard failure status.
  - Check `mcp-broker/docs/launchd.md` for startup/troubleshooting content; update if it discusses backend startup failures or logs.
  - Update `mcp-broker/CLAUDE.md` only if adding a new convention/gotcha that future agents need, such as sanitizing backend status errors.

## Documentation Impact

Documentation updates are required because this changes user-visible configuration and dashboard behavior:

- `mcp-broker/README.md`: add retry fields to sample config and Servers table; explain exhausted failures and restart requirement.
- `mcp-broker/DESIGN.md`: update Server manager startup behavior and dashboard Tools tab description.
- `mcp-broker/docs/launchd.md`: inspect and update troubleshooting/startup wording if relevant.
- `mcp-broker/CLAUDE.md`: update only if implementation introduces a durable development convention such as backend status sanitization requirements.

## Testing / Verification

- V1 for AC-1/AC-2: Add unit tests for effective retry configuration defaults/custom/zero/negative cases. Run `cd mcp-broker && go test -race ./internal/config ./internal/server` or the equivalent absolute-path command from repo root.
- V2 for AC-1/AC-3: Add server manager tests with a fake/flaky connector or retry helper showing a backend succeeds after an initial connect failure and remains available; assert attempts and discovered tools.
- V3 for AC-1/AC-3/AC-4: Add server manager tests showing connect exhaustion and `ListTools` exhaustion are non-fatal, record failed `BackendStatus`, do not expose tools for the failed backend, and leave healthy backends usable.
- V4 for AC-4/AC-5: Add dashboard API tests in `internal/dashboard/dashboard_test.go` verifying `/api/tools` includes backend statuses, sorts/stabilizes output, and serializes failed backend phase/attempt/error without breaking existing `tools` output.
- V5 for AC-4 security constraint: Add focused tests for error sanitization/redaction, including token-like values in URL query strings, bearer header-like text, and common secret names.
- V6 for AC-5: If practical, add a lightweight dashboard HTML/JS test only if the repo already has a pattern for it; otherwise rely on API tests plus manual browser inspection.
- V7 for AC-6: Run `cd mcp-broker && make test` and ensure existing tests pass.
- V8 for AC-7: Review changed docs and, if docs mention commands/config examples, verify examples are syntactically valid JSON/Markdown.
- V9 optional integration check: Run `mcp-broker serve` with one healthy backend and one intentionally unavailable local HTTP backend configured with short retry/backoff; verify logs show retries, the broker still serves the dashboard, and Tools tab shows the failed backend status.

## Risks and Mitigations

- Risk: Serial retries delay all healthy backends and dashboard availability. Mitigation: keep defaults conservative, use finite backoff, respect context cancellation, and document total worst-case behavior.
- Risk: Retrying OAuth-related errors could repeatedly open browser flows or callback servers. Mitigation: classify clearly non-transient/auth-interactive failures as non-retryable if practical, or avoid retrying after OAuth flow cancellation/authorization-required errors. At minimum, add tests or documentation for the chosen behavior.
- Risk: Dashboard error text leaks secrets. Mitigation: sanitize before storing API-visible status, test redaction, and avoid including raw config env/headers/args in status objects.
- Risk: `ListTools` failure after connect leaves an initialized backend process/client running but unusable. Mitigation: close and remove the backend on discovery exhaustion, and test cleanup behavior.
- Risk: Ambiguous config semantics for zero values. Mitigation: centralize effective config helpers and document/test absent, zero, negative, and custom values.
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

Implement this as a bounded startup retry and status-reporting feature: add per-server retry config, retry connect and initial tools/list before marking a backend failed, keep the broker non-fatal after exhausted failures, expose sanitized backend status through the dashboard Tools tab, and update README/DESIGN docs. Completion evidence should map each acceptance criterion to tests or manual verification output.
