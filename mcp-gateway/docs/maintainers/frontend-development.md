# Frontend development

Audience: Maintainers developing the Gateway web application

Purpose: Run trusted live reload without changing the production asset boundary.

See [Administrative control plane](../design/administrative-control-plane.md) for the normative production browser and development-proxy trust boundaries, and [maintainer and agent guidance](../../CLAUDE.md) for package ownership and editing invariants.

Use the development server to work on the authored TypeScript, Preact, and CSS with Vite live reload. The workflow uses two independently owned loopback processes. The development server is a separate trusted local process, not a mode of the production Gateway binary.

## Start both processes

Install repository dependencies first. From the repository root, install and initialize Gateway if needed:

```bash
make -C mcp-gateway install
mcp-gateway initialize
```

Start Gateway in one terminal. The default authority is `http://127.0.0.1:8210`:

```bash
mcp-gateway serve
```

Start the frontend development server in a second terminal:

```bash
npm run ui:dev
```

Wait for its `ready` line, then open `http://127.0.0.1:5173`. Gateway remains independently owned: the development command neither starts nor restarts it.

## Use a disposable feature-branch Gateway

To test the current checkout without reading or changing the default Gateway installation, start the repository-maintained temporary runner from the repository root:

```bash
make -C mcp-gateway serve-temporary
```

The runner builds the checkout with the existing `e2e` in-memory keyring, initializes the binary under a fresh owner-only temporary root, and serves on `127.0.0.1:8211`. It prints the temporary administrator bearer path for browser sign-in. In another terminal, point the frontend development server at that Gateway:

```bash
MCP_GATEWAY_UI_GATEWAY=http://127.0.0.1:8211 npm run ui:dev
```

Use `make -C mcp-gateway serve-temporary TEMPORARY_LISTEN=127.0.0.1:PORT` when that authority is occupied, and set `MCP_GATEWAY_UI_GATEWAY` to the matching origin. Press `Ctrl-C` in the Gateway terminal to stop it and remove its binary, data directory, administrator bearer, and in-memory keyring.

This path is for isolated feature development. Because keyring values are process-local, it does not test the native operating-system keyring or credential persistence across Gateway restarts. Use ordinary builds and the release-verification owners when those behaviors are under review.

## Startup selectors

The command reads exactly two optional environment selectors:

| Selector                 | Default                 | Contract                  |
| ------------------------ | ----------------------- | ------------------------- |
| `MCP_GATEWAY_UI_LISTEN`  | `127.0.0.1:5173`        | Frontend listen authority |
| `MCP_GATEWAY_UI_GATEWAY` | `http://127.0.0.1:8210` | Fixed Gateway origin      |

Both selectors require canonical numeric IPv4 `127/8` addresses and explicit ports from `1` to `65535`. The Gateway selector additionally requires the literal `http://` scheme. Hostnames such as `localhost`, abbreviated or zero-padded addresses, omitted ports, HTTPS, user information, paths, queries, and fragments are rejected. The command does not accept arguments or pass-through Vite options.

To use different loopback addresses or ports:

```bash
MCP_GATEWAY_UI_LISTEN=127.0.0.2:5174 \
MCP_GATEWAY_UI_GATEWAY=http://127.0.0.1:8211 \
npm run ui:dev
```

The listener is deliberately loopback-only. Do not expose it through a wildcard bind, port forward, reverse proxy, or shared remote development host.

## Trust, cookies, and OAuth

Treat the development server as a trusted local process. The browser sends the administrator bearer and authenticated API traffic to the frontend origin; its fixed proxy forwards only segment-bounded `/api/v1/` traffic to the selected Gateway. It does not proxy `/mcp`, `/oauth/callback`, assets, arbitrary paths, or API WebSocket upgrades. Asset and HMR requests are handled locally and are not forwarded to Gateway.

Gateway's `Set-Cookie` response is preserved as a host-only session cookie for the frontend origin. The proxy preserves the cookie and CSRF headers for API requests and rewrites the one exact frontend `Origin` to the configured Gateway origin. Changing the frontend host creates a different browser cookie boundary and requires signing in there. Bearers, cookies, CSRF values, request bodies, and response bodies are intentionally absent from development logs and temporary state, but the local process can observe traffic while it is running; do not use it on an untrusted account or machine.

OAuth callback remains on the Gateway origin. Configure and open the callback as `http://127.0.0.1:8210/oauth/callback` for the default Gateway, not on port `5173`. The development proxy never becomes callback authority.

## Live reload and production assets

The development server serves `web/index.html` and authored modules directly. CSS changes use state-preserving HMR; module changes may trigger a full navigation and then restore the existing authenticated session through the normal bootstrap. Vite cache and output use a temporary development root that is removed on graceful shutdown.

Production is separate and deterministic:

```bash
npm run ui:build
npm run ui:verify-generated
npm run ui:verify-supply-chain
```

Only the production build writes the fixed embedded allowlist under `mcp-gateway/internal/api/static`. `npm run ui:dev` does not generate or update those assets, and the production binary has no Node, source-serving, proxy, or HMR runtime dependency.

## Shutdown and troubleshooting

Press `Ctrl-C` in the frontend terminal and the Gateway terminal to stop each independently. Stop the frontend gracefully so it can close Vite and remove its temporary root.

- **Frontend address is already in use:** startup fails with an `already in use` error before serving. Stop the existing listener or choose another canonical `MCP_GATEWAY_UI_LISTEN` authority.
- **Gateway is unavailable:** the shell can still load, but proxied API requests fail locally with status `502`. Start Gateway and verify `MCP_GATEWAY_UI_GATEWAY`; the development server does not supervise or retry Gateway mutations.
- **A selector is rejected:** use a full numeric `127.x.x.x` address, an explicit valid port, and `http://` for the Gateway selector. Remove all command-line arguments after `ui:dev`.
- **The browser returns to sign-in:** confirm both selectors still name the intended origins. A session cookie belongs to the selected frontend host, and Gateway restart or authority revocation can invalidate the session.
- **OAuth completion fails:** confirm the provider redirects directly to the Gateway `/oauth/callback` origin rather than the frontend development origin.

## Visual verification

Human maintainers and coding agents must use a real browser to exercise each affected state for every change that can affect rendered UI or browser interaction before reporting the work complete. Prefer Playwright so the interaction and evidence are reproducible. Inspect the rendered result at representative desktop and narrow viewports; include loading, empty, error, confirmation, overflow, or populated states when the change can affect them.

Use semantic snapshots to verify content, controls, and accessible state. Take and actually inspect screenshots to verify layout, hierarchy, spacing, clipping, overlap, wrapping, responsive behavior, and unintended visual regressions. Running browser tests, generating screenshots, or validating screenshot dimensions and hashes is not visual inspection by itself.

Watch affected navigation, loading, refresh, polling, and mutation transitions in the browser as well as their settled screenshots. Check that tab bars, headings, controls, and established content do not flash, jump, reorder, briefly claim staleness before current state is known, or retain data from a superseded location. First load may use a loading state; during background refresh, matching prior content and dimensions should remain when safe with the prescribed stale indication. A settled screenshot cannot prove visual stability by itself.

Record the states and viewports inspected in the change or agent completion report, along with any console or network failure relevant to the change. If authentication or the local runtime blocks browser verification, report that gap explicitly rather than implying the UI was inspected. Do not retain screenshots containing administrator bearers, one-time secrets, or other sensitive values.

## Manual exploratory audits

The browser suites create route-local mocked data and tear it down after each run; they do not provide a persistent populated demonstration installation. For a manual audit, use an isolated real Gateway and populate only the states under review. From the repository root:

```bash
AUDIT_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/mcp-gateway-ui-audit.XXXXXX")
chmod 700 "$AUDIT_ROOT"

mcp-gateway initialize \
  --data-dir "$AUDIT_ROOT/data" \
  --secret-output "$AUDIT_ROOT/admin-bearer"

mcp-gateway serve \
  --data-dir "$AUDIT_ROOT/data" \
  --listen 127.0.0.1:8211
```

In a second terminal:

```bash
MCP_GATEWAY_UI_LISTEN=127.0.0.1:5174 \
MCP_GATEWAY_UI_GATEWAY=http://127.0.0.1:8211 \
npm run ui:dev
```

Prefer `127.0.0.1` unless the audit deliberately covers alternate loopback addresses. Some local proxy configurations exempt only that address and may intercept another valid `127/8` address. Verify both listeners and their startup logs before opening the browser; the development server can load its shell while Gateway API requests fail with `502`.

The administrator bearer remains in the owner-only file. Read it only at the browser handoff, never place it in an environment variable or command argument, and remove the complete temporary installation after stopping both processes. A saved Playwright storage state can reuse the authenticated browser session across route and viewport sweeps, but it is credential-bearing temporary material and requires the same cleanup.

A small set of UI operations reaches most populated states without a downstream fixture:

1. Create a principal. Creation also adds its Default Gateway grant.
2. Issue its agent credential to inspect confirmation and one-time-secret behavior.
3. Create an exact grant against Gateway self-service tools.
4. Create a disabled, unauthenticated HTTP server with a syntactically valid non-routable endpoint such as `https://example.invalid/mcp`. This exposes the server detail tabs without initiating downstream work.
5. Create a backup and an additional administrator credential to populate their System tables and detail panels.

Exercise sign-in failure and success, sign-out, dirty-navigation protection, destructive confirmations, filters and reset, empty and populated collections, light and dark themes, and mobile navigation. Inspect at least desktop, narrow mobile, and 320px widths.

For every affected collection, inspect column priority and alignment, link prominence, row-action placement, default ordering, one-line behavior, full-value recovery, visible result counts, and the empty and populated filter states. Confirm that every active constraint is visible and clearable and that compact filter controls and responsive transformations preserve meaning. Document-level overflow checks are insufficient: compare interactive-control rectangles with their clipping container and confirm that truncated values can be recovered.

Compare affected resource details with an established peer page. Verify the title and identity hierarchy, applicable status and next action or historical evidence, section order, technical metadata, action placement, and any contextual return navigation; duplicated controls or facts across tabs are defects unless each occurrence serves a distinct task. Check that display names and qualified IDs serve their distinct recognition and identity roles, state labels use consistent plain language and capitalization, timestamps use the shared localized presentation, and implementation terms appear only when operationally necessary. For creation and destructive workflows, inspect input, review, confirmation, submitting, success, rejection, and uncertain states as applicable, and confirm that primary, secondary, and destructive actions remain visually distinct and consistently placed.

When Playwright CLI cannot find its configured browser channel, use the repository-pinned Playwright API from the repository root rather than installing an unpinned browser:

```bash
node --input-type=module - <<'EOF'
import { chromium } from "@playwright/test";
const browser = await chromium.launch({ headless: true });
// Create an isolated context, exercise the local UI, and close the browser.
await browser.close();
EOF
```

Full-page screenshots can reposition fixed off-screen elements, including the skip link, while assembling the image. Confirm any apparent fixed-element or focus defect with a viewport screenshot, `document.activeElement`, computed styles, and element rectangles before recording it. Mask one-time values when capturing their dialog layout, or delete the screenshot immediately.

## Grant authoring verification

For the shared direct-grant and additive-approval editor, exercise namespace/literal tool identity, Known/Unknown versus loading/unavailable recognition, partial and nested escaped-pointer suggestions, manual values outside enums, explicit type overrides, and both EQUALS/MATCHES transitions. Verify that MATCHES keeps a disabled String control, clears values on operator changes, and warns rather than rejects non-string schema paths. Switch tools and servers with a populated draft and delayed schema responses; guidance may change but rows, values, and explicit types must not.

Inspect compact desktop rows and narrow reflow with long pointers, errors, and manual fallback, including open suggestion lists in light and dark themes. Exercise actual tool/path selection using arrow keys and Enter and by clicking options; verify Escape dismissal, Tab without selection, visible active-option scrolling, arbitrary manual input, neutral empty fields, and stable recognition badge dimensions. A datalist-option count alone is not autocomplete evidence. Confirm Expiry follows constraints, per-row boilerplate, operator-help disclosure, and editing serialization are absent, and contextual warnings remain available. Check the read-only serialized policy in final review: equality-only creation and approvals of locked v1 requests must emit v2 without normalizing number tokens or changing pattern bytes. Unconstrained policies remain null. The browser workflow leaf owns direct creation and adjudication regressions; the visual and accessibility leaves remain separate evidence owners.

## Browser scenario ownership

`web/tests/browser-coordinator.ts` owns the bounded input protocol, browser/context lifecycle, scenario dispatch, and shared request, Origin, console, and HttpOnly-cookie checks. Domain scenarios live under `web/tests/browser/`: lifecycle, privacy/presentation, system, access, server, and development. Shared browser helpers, pure fixture builders, and production-owner foundation exercises have separate modules; scenarios never import the coordinator. The restart scenario receives the existing bounded input reader explicitly.

Each invocation retains a fresh browser process, BrowserContext, collectors, and cleanup; its Go scenario owner supplies the independently owned Gateway fixture and credentials. Scenario-local routes, privacy scans, screenshots, and assertions remain with their domain owner. Deliberate process-survival and restart cases remain independent. Browser-process sharing is not enabled: measure launch work separately before accepting the additional lifecycle/isolation burden. Browser leaves remain independently runnable through the checked suite planner, and their immutable evidence definitions include the coordinator and scenario modules. `make test-browser` shares only inventory/planner startup across its five leaves; each retains a separate Go test process and fresh Gateway build. A measured immutable-binary prototype increased five-leaf setup cost after source/toolchain validation was included, so binary sharing is not enabled. Use `make test-browser TEST_JSON=1` to capture Go timing events for the same five commands; filter Make banners separately from the event stream.

## Focused verification

Run the fast development checks directly:

```bash
npm run ui:test-dev
```

The combined development owner runs the Node proxy matrix and both real-Gateway Chromium workflows once:

```bash
make -C mcp-gateway test-frontend-development
```

Production bundle, static handler, and browser acceptance remain separate owners; the development target does not nest them. Follow [release verification](release-verification.md) when composing those owners for a clean candidate.
