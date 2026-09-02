# Frontend development

Audience: Maintainers developing the Gateway web application

Purpose: Run trusted live reload without changing the production asset boundary.

See [Administrative control plane](design/administrative-control-plane.md) for the normative production browser and development-proxy trust boundaries, and [maintainer guidance](../CLAUDE.md) for package ownership and editing invariants.

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

For every change that can affect rendered UI or browser interaction, use a real browser to exercise each affected state before reporting the work complete. Prefer Playwright so the interaction and evidence are reproducible. Inspect the rendered result at representative desktop and narrow viewports; include loading, empty, error, confirmation, overflow, or populated states when the change can affect them.

Use semantic snapshots to verify content, controls, and accessible state. Take and actually inspect screenshots to verify layout, hierarchy, spacing, clipping, overlap, wrapping, responsive behavior, and unintended visual regressions. Running browser tests, generating screenshots, or validating screenshot dimensions and hashes is not visual inspection by itself.

Record the states and viewports inspected in the completion report, along with any console or network failure relevant to the change. If authentication or the local runtime blocks browser verification, report that gap explicitly rather than implying the UI was inspected. Do not retain screenshots containing administrator bearers, one-time secrets, or other sensitive values.

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
