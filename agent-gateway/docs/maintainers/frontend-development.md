# Frontend development

Audience: Maintainers developing the Gateway web application

Purpose: Run trusted live reload without changing the production asset boundary.

See [Administrative control plane](../design/administrative-control-plane.md) for the normative production browser and development-proxy trust boundaries, and [maintainer and agent guidance](../../CLAUDE.md) for package ownership and editing invariants.

Use the development server to work on the authored TypeScript, Preact, and CSS with Vite live reload. The workflow uses two independently owned loopback processes. The development server is a separate trusted local process, not a mode of the production Gateway binary.

## Start both processes

Install repository dependencies first. From the repository root, install and initialize Gateway if needed:

```bash
make -C agent-gateway install
agent-gateway initialize
```

Start Gateway in one terminal. The default authority is `http://127.0.0.1:8210`:

```bash
agent-gateway serve
```

Start the frontend development server in a second terminal:

```bash
npm run ui:dev
```

Wait for its `ready` line, then open `http://127.0.0.1:5173`. Gateway remains independently owned: the development command neither starts nor restarts it.

## Use a disposable feature-branch Gateway

To test the current checkout without reading or changing the default Gateway installation, start the repository-only demo runner (Go on Linux or macOS required; no Python or Node needed for the demo itself):

```bash
make -C agent-gateway serve-demo
# Or a fresh empty installation, without fixtures or seeded domain records:
make -C agent-gateway serve-demo AGENT_GATEWAY_DEMO_DATASET=empty
```

`serve-demo` replaces `serve-temporary`; the old command and `TEMPORARY_LISTEN` are removed, not aliases. The shell entry point builds the Go demo executable into ignored `agent-gateway/.demo-bin/` and replaces itself with it; that nonsecret build artifact is reusable and remains after shutdown. The runner then builds the checkout with the existing `e2e` in-memory keyring, initializes a fresh owner-only account/data root, and serves on `127.0.0.1:8211`. Wait for **Demo Gateway ready**, which follows authenticated public verification of the selected dataset. Startup/seed errors never advertise a usable environment. It prints protected credential-file paths, not their contents. Read the administrator file only when entering it in the ordinary sign-in form; never retain a screenshot of that value.

Curated data includes:

- **Demo Workshop:** `demo_workshop.echo` (`text`, at most 256 characters), `demo_workshop.add` (`a`/`b`, finite numbers between -1,000,000 and 1,000,000), and `demo_workshop.controlled_error` (empty arguments; expected safe `downstream_failure`).
- **Demo Library:** `demo_library.lookup` with `document` set to `welcome` or `permissions`; it never reads arbitrary files.
- **Demo Explorer:** all demo tools. **Demo Reader:** echo and lookup; arithmetic is discoverable but calls are rejected until access is approved. An explicit deny hides the controlled-error tool from Reader discovery. **Demo Disabled:** its credential cannot authenticate.
- Five additional requestable principals, each with one pending request and no existing demo-server grants or DENYs, so approvals are independent:

  | Principal                | Pending request                                            |
  | ------------------------ | ---------------------------------------------------------- |
  | Demo Request Tool        | `demo_workshop.add`, no constraints or expiration          |
  | Demo Request Constraints | `demo_workshop.add`, argument `a` must equal `1`           |
  | Demo Request Duration    | `demo_workshop.add`, one hour from approval                |
  | Demo Request Server      | Entire `demo_workshop` server, all tools                   |
  | Demo Request Read-only   | Entire `demo_workshop` server, only tools marked read-only |

  Open **MCP → Requests** to approve as requested or customize; follow each resulting grant to inspect it. The runner prints a protected agent-bearer file path for each principal. Workshop `echo` and Library `lookup` explicitly declare `readOnlyHint=true`; Workshop `add` omits the hint and `controlled_error` declares false. After read-only approval, `echo` succeeds while the other Workshop tools remain blocked. These fixtures are all harmless; the mixed labels demonstrate filtering rather than actual write effects. Restart the demo for fresh pending requests.

- Real successful and controlled-error invocation history, plus the normal audit records emitted by those public mutations. No recurring synthetic activity runs after seeding.

Configure a test MCP client's existing authenticated HTTP transport for `http://127.0.0.1:8211/mcp`, reading the selected agent bearer from the printed protected file into its Authorization header at request time. Use modern protocol `2026-07-28` or the supported legacy handshake; do not put bearer values in command arguments, environment variables, URLs, or browser storage. These agent credentials have no administrator authority. The fixtures remain callable until shutdown; an unexpected fixture exit fails and closes the demo.

In another terminal, point the independently owned frontend development server at that Gateway:

```bash
AGENT_GATEWAY_UI_GATEWAY=http://127.0.0.1:8211 npm run ui:dev
```

Use `make -C agent-gateway serve-demo AGENT_GATEWAY_DEMO_LISTEN=127.0.0.1:PORT` when that authority is occupied, and set `AGENT_GATEWAY_UI_GATEWAY` to the matching origin. The script equivalent is `./agent-gateway/scripts/serve-demo.sh --dataset curated --listen 127.0.0.1:8211`; unknown datasets and noncanonical/nonloopback authorities are rejected before launch. Press `Ctrl-C` in the Gateway terminal to stop its Gateway and fixture process groups and remove its binary, data, account home, all credential files, and in-memory keyring. Interruption exits nonzero. Every launch generates fresh credentials/IDs/timestamps; a cleanup failure reports a retained root instead of claiming success. Stop Vite separately.

This path is for isolated feature development. Because keyring values are process-local, it does not test the native operating-system keyring or credential persistence across Gateway restarts. Use ordinary builds and the release-verification owners when those behaviors are under review.

### Demo supervision

The demo supervisor retains unreaped child identities when signalling owned groups. Bounds: build 300s; initialization/credential CLI 15s; startup/seeding 60s plus active bounded CLI; HTTP 3s; output 1 MiB/stream; reap 5s; final group-absence probe 2s. Required fixture exits fail the demo. Mutations are one-shot; readiness polling is read-only. Failed cleanup retains the root and ownership evidence. Race-enabled real-process tests belong only to `test-serve-demo`, disjoint from E2E/harness. All `test/demo` code, including its fixture entry point, uses the `e2e` tag. Each command has a `/bin/sh` group owner that reaps it, reports exit through a private pipe and remains alive until teardown. Arguments are positional, never shell interpolation; children cannot inherit control/status pipes. The parent fences the group before `Wait`; settled/recycled identities are never signalled. Linux/macOS use the same implementation without zombie inspection. Final-probe permission errors may retry within its bound but never prove absence. The lifecycle tests resolve and preserve both `GOCACHE` and `GOMODCACHE` before isolating `HOME`, then clear `GOPATH` to exercise CI's default-path behavior. Dependency downloads must not move into each disposable account. CI runs the demo owner on both platforms.

## Startup selectors

The command reads exactly two optional environment selectors:

| Selector                   | Default                 | Contract                  |
| -------------------------- | ----------------------- | ------------------------- |
| `AGENT_GATEWAY_UI_LISTEN`  | `127.0.0.1:5173`        | Frontend listen authority |
| `AGENT_GATEWAY_UI_GATEWAY` | `http://127.0.0.1:8210` | Fixed Gateway origin      |

The retired `MCP_GATEWAY_UI_LISTEN` and `MCP_GATEWAY_UI_GATEWAY` settings fail startup with replacement guidance, including when empty or supplied alongside their replacements. They are not aliases. Client provisioning exports canonical `AGENT_GATEWAY_ENDPOINT` / `AGENT_GATEWAY_AGENT_TOKEN` and the temporary `MCP_GATEWAY_ENDPOINT` / `MCP_GATEWAY_AGENT_TOKEN` aliases from one authority; these are separate, supported client settings. See [client migration and its consumer gate](../operators/access-control.md#existing-sandbox-migration-and-conflicts). See [developer cutover](release-verification.md#developer-source-and-tooling-cutover) for the full migration.

Both selectors require canonical numeric IPv4 `127/8` addresses and explicit ports from `1` to `65535`. The Gateway selector additionally requires the literal `http://` scheme. Hostnames such as `localhost`, abbreviated or zero-padded addresses, omitted ports, HTTPS, user information, paths, queries, and fragments are rejected. The command does not accept arguments or pass-through Vite options.

To use different loopback addresses or ports:

```bash
AGENT_GATEWAY_UI_LISTEN=127.0.0.2:5174 \
AGENT_GATEWAY_UI_GATEWAY=http://127.0.0.1:8211 \
npm run ui:dev
```

The listener is deliberately loopback-only. Do not expose it through a wildcard bind, port forward, reverse proxy, or shared remote development host.

## Trust, cookies, and OAuth

Treat the development server as a trusted local process. The browser sends the administrator bearer and authenticated API traffic to the frontend origin; its fixed proxy forwards only segment-bounded `/api/v2/` traffic to the selected Gateway. It does not proxy `/mcp`, `/oauth/callback`, assets, arbitrary paths, or API WebSocket upgrades. Asset and HMR requests are handled locally and are not forwarded to Gateway.

Gateway's `Set-Cookie` response is preserved as a host-only session cookie for the frontend origin. The proxy preserves the cookie and CSRF headers for API requests and rewrites the one exact frontend `Origin` to the configured Gateway origin. Changing the frontend host creates a different browser cookie boundary and requires signing in there. Bearers, cookies, CSRF values, request bodies, and response bodies are intentionally absent from development logs and temporary state, but the local process can observe traffic while it is running; do not use it on an untrusted account or machine.

OAuth callback remains on the Gateway origin by default: `http://127.0.0.1:8210/oauth/callback`, not port `5173`. A per-server `callback_uri` override uses a Gateway-owned temporary callback-only loopback listener; it is never proxied by Vite. The development proxy never becomes callback authority.

## Live reload and production assets

The development server serves `web/index.html` and authored modules directly. CSS changes use state-preserving HMR; module changes may trigger a full navigation and then restore the existing authenticated session through the normal bootstrap. Vite cache and output use a temporary development root that is removed on graceful shutdown.

Production is separate and deterministic:

```bash
npm run ui:build
npm run ui:verify-generated
npm run ui:verify-supply-chain
```

Only the production build writes the fixed embedded allowlist under `agent-gateway/internal/api/static`. `npm run ui:dev` does not generate or update those assets, and the production binary has no Node, source-serving, proxy, or HMR runtime dependency.

## Shutdown and troubleshooting

Press `Ctrl-C` in the frontend terminal and the Gateway terminal to stop each independently. Stop the frontend gracefully so it can close Vite and remove its temporary root.

- **Frontend address is already in use:** startup fails with an `already in use` error before serving. Stop the existing listener or choose another canonical `AGENT_GATEWAY_UI_LISTEN` authority.
- **Gateway is unavailable:** the shell can still load, but proxied API requests fail locally with status `502`. Start Gateway and verify `AGENT_GATEWAY_UI_GATEWAY`; the development server does not supervise or retry Gateway mutations.
- **A selector is rejected:** use a full numeric `127.x.x.x` address, an explicit valid port, and `http://` for the Gateway selector. Remove all command-line arguments after `ui:dev`.
- **The browser returns to sign-in:** confirm both selectors still name the intended origins. A session cookie belongs to the selected frontend host, and Gateway restart or authority revocation can invalidate the session.
- **OAuth completion fails:** confirm the provider redirects to the exact configured callback URI (or Gateway's default `/oauth/callback`), not the frontend development origin.

## Navigation implementation

The Agent Gateway shell in `web/src/main.tsx` owns one ordered navigation model: Overview, Principals, Audit Log, System; then MCP (Servers, Tools, Grants, Requests, Invocations). The first four links form an unlabeled group; MCP is the only named group. It renders the same groups and ordinary links in the desktop rail and narrow Menu disclosure. Separate groups with inset dividers and spacing; use small uppercase, letterspaced section labels so noninteractive headings are distinct from destination links. Preserve the existing visual language and keyboard/focus behavior; groups do not introduce editors, routes, state owners, or protocol placeholders.

Display names differ from canonical route keys: Tools uses `#/mcp/tools`, Requests `#/mcp/access-requests`, Invocations `#/mcp/invocations`, and Audit Log `#/audit-log`. Requests retains the page title **Access requests**; Invocations and Audit Log use their navigation labels as page titles. Invocations has no explanatory subtitle. Servers, Principals, and Grants use `#/mcp/servers`, `#/principals`, and `#/mcp/grants`. Keep `location.ts` as the sole grammar owner: it maps domain prefixes to logical destination-relative segments for existing page owners and serializes them back, with no legacy aliases or redirects. Retired `#/access/principals` and `#/activity/audit` locations are invalid, including their detail/create suffixes; this browser-only cleanup changes no API paths. Server status elides its default tab; Operations uses `tab=operations`. Preserve declared detail/filter/query state and update dynamic links and refresh matchers alongside literal links. MCP permission routes use `/api/v2/mcp/grants`, `/api/v2/mcp/grant-requests`, and `/api/v2/mcp/grant-constraints/validate`; the development proxy forwards them unchanged under its existing segment-bounded API policy. Retired access hashes and unnamespaced grant API paths have no compatibility dispatch. Access requests means MCP permission approval, not network traffic. See the [old-to-new mapping](../operators/administration.md#browser-location-cutover). Invocations uses the existing MCP invocation controller and `/api/v2/mcp/invocations` list/item resources; the development proxy forwards these under the same segment-bounded policy. Retired `/api/v2/invocations` and `#/activity/invocations` locations have no compatibility dispatch. Audit Log retains the separate shared audit, including system and offline maintenance events. Follow the [coordinated invocation cutover](../operators/administration.md#mcp-invocation-namespace-cutover) when upgrading bundled consumers. These labels change neither actor selection nor evidence coverage. Theme persistence, session clearing, refresh, pagination, mutation guards and one-time-secret handling stay with their existing shared owners.

The shell lifecycle browser scenario checks accessible group membership, link order, every destination's keyboard activation/current state, and narrow target reachability. Domain history scenarios cover filtered detail returns and evidence separation. Inspect both the open Menu and settled destinations at desktop, narrow and 320px widths after navigation changes.

## Table implementation

Follow the normative [table conventions](../design/administrative-control-plane.md#table-conventions) for column order, vocabulary, identity, sizing, status colors, and responsive behavior. Compose `CollectionTable` with an explicit `rowHeaderKey`, semantic column `role`s, and `layout="activity"` for history (resource is the default). Use `TableIdentity` for recognition plus secondary identifiers and `UserTime` for dates. Keep related-resource link destinations with their page owner. Use `additionalSorts` only to interpret legacy non-column sort URLs, not to expose dedicated ID-sort options. Desktop headers own sorting; the shared selector appears only in stacked narrow layouts. Column roles keep compact content intrinsic-sized and give spare space to identities and descriptions; do not add fixed width totals. Declare `loadedSubset` when local filtering/sorting covers only loaded history. Use `ComparisonTable` only for genuine cross-row comparison such as resource limits.

Add observable browser assertions beside the existing domain scenario, including primary/secondary identity, column order, semantic row heading, narrow sorting and action reachability, and full long-value recovery. Do not substitute source-string assertions or screenshots alone for interaction tests. An operation creation-time sort change also requires the repository/API ordering tests because browser-local sorting cannot establish global history order.

## Visual verification

Human maintainers and coding agents must use a real browser to exercise each affected state for every change that can affect rendered UI or browser interaction before reporting the work complete. Prefer Playwright so the interaction and evidence are reproducible. Inspect the rendered result at representative desktop and narrow viewports; include loading, empty, error, confirmation, overflow, or populated states when the change can affect them.

Use semantic snapshots to verify content, controls, and accessible state. Take and actually inspect screenshots to verify layout, hierarchy, spacing, clipping, overlap, wrapping, responsive behavior, and unintended visual regressions. Running browser tests, generating screenshots, or validating screenshot dimensions and hashes is not visual inspection by itself.

Watch affected navigation, loading, refresh, polling, and mutation transitions in the browser as well as their settled screenshots. Check that tab bars, headings, controls, and established content do not flash, jump, reorder, briefly claim staleness before current state is known, or retain data from a superseded location. First load may use a loading state; during background refresh, matching prior content and dimensions should remain when safe with the prescribed stale indication. A settled screenshot cannot prove visual stability by itself.

Record the states and viewports inspected in the change or agent completion report, along with any console or network failure relevant to the change. If authentication or the local runtime blocks browser verification, report that gap explicitly rather than implying the UI was inspected. Do not retain screenshots containing administrator bearers, one-time secrets, or other sensitive values.

## Manual exploratory audits

The browser suites create route-local mocked data and tear it down after each run. For a persistent interactive manual audit, use the curated demo above, or select `AGENT_GATEWAY_DEMO_DATASET=empty` and populate only the states under review. From the repository root:

```bash
make -C agent-gateway serve-demo
```

In a second terminal:

```bash
AGENT_GATEWAY_UI_LISTEN=127.0.0.1:5174 \
AGENT_GATEWAY_UI_GATEWAY=http://127.0.0.1:8211 \
npm run ui:dev
```

Prefer `127.0.0.1` unless the audit deliberately covers alternate loopback addresses. Some local proxy configurations exempt only that address and may intercept another valid `127/8` address. Verify both listeners and their startup logs before opening the browser; the development server can load its shell while Gateway API requests fail with `502`.

The administrator bearer remains in the owner-only file. Read it only at the browser handoff, never place it in an environment variable or command argument, and remove the complete temporary installation after stopping both processes. A saved Playwright storage state can reuse the authenticated browser session across route and viewport sweeps, but it is credential-bearing temporary material and requires the same cleanup.

A small set of UI operations reaches most populated states without a downstream fixture:

1. Create a principal. Creation also adds its ordinary Default Gateway access grant for the six fixed MCP self-service tools, not downstream tools. Check MCP discovery visibility wording in create/edit, review, details, and collection/filter states; visibility grants no access.
2. Issue its agent credential to inspect confirmation and one-time-secret behavior.
3. Create an exact grant against Gateway self-service tools.
4. Create a disabled, unauthenticated HTTP server with a syntactically valid non-routable endpoint such as `https://example.invalid/mcp`. This exposes the server detail tabs without initiating downstream work.
5. Create a backup and an additional administrator credential to populate their System tables and detail panels.

Exercise sign-in failure and success, sign-out, dirty-navigation protection, destructive confirmations, filters and reset, empty and populated collections, light and dark themes, and mobile navigation. Inspect at least desktop, narrow mobile, and 320px widths.

For every affected collection, inspect column priority and alignment, link prominence, row-action placement, default ordering, one-line behavior, full-value recovery, visible result counts, and the empty and populated filter states. Confirm that every active constraint is visible and clearable and that compact filter controls and responsive transformations preserve meaning. Document-level overflow checks are insufficient: compare interactive-control rectangles with their clipping container and confirm that truncated values can be recovered.

Compare affected resource details with an established peer page. Verify the title and identity hierarchy, applicable status and next action or historical evidence, section order, technical metadata, action placement, and any contextual return navigation; duplicated controls or facts across tabs are defects unless each occurrence serves a distinct task. Check that display names and qualified IDs serve their distinct recognition and identity roles, state labels use consistent plain language and capitalization, timestamps use the shared localized presentation, and implementation terms appear only when operationally necessary. For creation and destructive workflows, inspect input, review, confirmation, submitting, success, rejection, and uncertain states as applicable, and confirm that primary, secondary, and destructive actions remain visually distinct and consistently placed.

For forms, check create and edit contexts and each applicable choice: ordinary optional settings stay visible, dependent controls follow their triggering choice, and helper text identifies the expected input, its source, or the blank/default behavior without stale instructions. Exercise advanced disclosures by keyboard and pointer, verify Show/Hide labels and expanded state, confirm configured overrides are initially visible and validation errors reveal their controls, and ensure collapsing preserves values. For zero-or-more fields, exercise add, edit, remove, and empty rows through the shared list controls. In the server editor, verify that default scopes, explicitly no scopes, and populated scopes retain distinct review and request representations; switching the explicit-scopes toggle must preserve the draft without submitting inactive values. Confirm issuer is first inside Advanced, including issuer-only configured/error states. Check domain-specific add labels and readable confirmation labels. Inspect shared 16px form-action gaps for bearer, client-secret, local-secret, and grouped review actions in empty, populated, and error states. Verify OAuth client credentials precede the distinct authorization panel; zero client revision blocks a static confidential client with guidance, while aggregate credential warnings alone do not. Exercise approval and rejection independently, including rejection with invalid approval fields. Verify local date/time inputs serialize the selected instant to UTC, blank expiry remains non-expiring, and duration units serialize exact seconds without extending submitted access.

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

Pure session epochs, view generations, mutation coordination and injected sensitive-sink assertions execute once in `web/tests/foundations.test.ts` under `test-frontend-development-node`, not as browser-scenario preambles. That Node owner runs in CI Quality and release acceptance. Keep browser-dependent cookies, storage, Origin, clipboard permissions, popup interaction and rendering in their real-browser leaves.

The combined canary retains authenticated Overview in dark mode at 390 px with reduced motion: its axe scan, screenshot bounds/hash, overflow, same-origin assets and secret/storage checks cover that unique state. Dedicated visual inventory assertions (48 artifacts, 10 states, six rubric dimensions) belong only to the visual matrix. Privacy, visual and accessibility leaves cover other states, so their shared helpers are not evidence that the canary lifecycle is redundant. This cleanup removes no browser/Gateway lifecycle and claims no browser-launch reduction.

`web/tests/browser-coordinator.ts` owns the bounded input protocol, browser/context lifecycle, scenario dispatch, and shared request, Origin, console, and HttpOnly-cookie checks. Domain scenarios live under `web/tests/browser/`: lifecycle, privacy/presentation, system, access, server, and development. Shared browser helpers, pure fixture builders, and production-owner foundation exercises have separate modules; scenarios never import the coordinator. The restart scenario receives the existing bounded input reader explicitly.

Each invocation retains a fresh browser process, BrowserContext, collectors, and cleanup; its Go scenario owner supplies the independently owned Gateway fixture and credentials. Scenario-local routes, privacy scans, screenshots, and assertions remain with their domain owner. Deliberate process-survival and restart cases remain independent. Browser-process sharing is not enabled: measure launch work separately before accepting the additional lifecycle/isolation burden. Browser leaves remain independently runnable through the checked suite planner, and their immutable evidence definitions include the coordinator and scenario modules. `make test-browser` shares only inventory/planner startup across its five leaves; each retains a separate Go test process and fresh Gateway build. A measured immutable-binary prototype increased five-leaf setup cost after source/toolchain validation was included, so binary sharing is not enabled. Use `make test-browser AGENT_GATEWAY_TEST_JSON=1` to capture Go timing events for the same five commands; filter Make banners separately from the event stream.

## Focused verification

Run the fast development checks directly:

```bash
npm run ui:test-dev
```

The combined development owner runs the Node proxy matrix and both real-Gateway Chromium workflows once:

```bash
make -C agent-gateway test-frontend-development
```

Production bundle, static handler, and browser acceptance remain separate owners; the development target does not nest them. Follow [release verification](release-verification.md) when composing those owners for a clean candidate.
