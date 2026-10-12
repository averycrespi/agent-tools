# Serve Gateway behind an HTTPS proxy

Audience: Gateway administrators

Purpose: Configure proxy-independent remote serving without changing local-only defaults.

For an initialized installation behind an operator-owned HTTPS reverse proxy:

```bash
agent-gateway serve --listen 0.0.0.0:8210 \
  --external-origin https://gateway.example \
  --clear-http-proxy-listen
```

Replace the example with your canonical public HTTPS origin: lowercase DNS hostname or canonical IP literal, optional nondefault port, and no trailing slash, credentials, query or fragment. Non-loopback administration requires this flag. Bare `serve` retains its loopback listeners and existing HTTP opt-out behavior. TLS terminates at your proxy, not Gateway; restrict the internal listener to trusted proxy/client networks. This is not network isolation or authorization to expose a live installation.

## Generic proxy contract

- Preserve the exact public **Host** (including a nondefault port), browser **Origin**, authorization, cookies, CSRF and MCP protocol headers. Do not rewrite browser origins to the internal HTTP address.
- Strip `Forwarded` and all `X-Forwarded-*` headers before forwarding. Gateway rejects them rather than trusting their values; no header selects scheme, public authority or client authorization.
- Disable caching and response buffering, especially on `/mcp` and `/api/v2/events`. Permit long-lived streams with suitable read/idle timeouts, flush frames promptly, and propagate cancellation.
- Disable automatic mutation retries/replay and MCP reconnect-and-replay. A disconnected request may already have executed; inspect the outcome before issuing another attempt.

This contract needs no vendor-specific headers or trusted-proxy CIDR discovery. The configured origin automatically permits its exact public Host and is the only trusted browser Origin. Session cookies are Secure, HttpOnly, host-only and SameSite=Strict; CSRF and separate administrator/agent credentials remain mandatory. Repeatable `--allowed-host internal.example` permits authenticated direct MCP/API access at internal aliases without granting browser Origin trust. Forwarding headers cannot add aliases.

## Local access and protocol separation

For wildcard binds, local health probes use `http://127.0.0.1:8210/livez` or `/readyz`, and a local CLI (including one invoked through `docker exec` in an operator-managed container) uses the same loopback endpoint and protected administrator bearer file. Use `--address http://127.0.0.1:<port>` for a custom port. This narrow internal access accepts no Origin or Cookie header and cannot sign in or serve the browser. A specific non-loopback bind does not also listen on loopback. Remote HTTPS CLI and external OAuth callbacks are not delivered by this configuration.

HTTP/Git forwarding is a **separate direct listener**, not a website route. Enable it explicitly with e.g. `--http-proxy-listen 0.0.0.0:8212` instead of the opt-out above, after [client/CA setup](http-proxy.md); protect the unencrypted client-to-proxy hop. Wildcard listeners conservatively reserve their ports against outgoing proxy requests. Container image/runtime qualification and live Debian/proxy adoption require separate evidence; source tests do not qualify them.

The [administrative control-plane design](../design/administrative-control-plane.md#http-administration) owns the normative Host, Origin, cookie and forwarding boundaries.
