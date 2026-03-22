# MCP Broker Authentication Design

## Problem

The MCP broker runs as an HTTP server on localhost:8200. Any process on the machine can call its endpoints — including the `/mcp` tool gateway (which holds credentials and proxies tool calls) and the dashboard API (which can approve/deny tool calls). There is no authentication.

## Decision

Add bearer token authentication to all endpoints. A single random token is auto-generated on first run and stored in a file. Clients pass it via `Authorization: Bearer <token>` header. The browser dashboard uses a cookie set via a one-time token URL.

## Design

### Token Lifecycle

- **Generation:** On first startup, if no token file exists, generate 32 cryptographically random bytes (hex-encoded → 64-char string) and write to `~/.config/mcp-broker/auth-token` with `0600` permissions.
- **Loading:** On every startup, read the token file into memory. Fail startup if the file is missing or unreadable.
- **Regeneration:** `--regen-token` CLI flag to force-generate a new token (invalidates all existing sessions/configs).
- **No token is ever logged.** Startup prints: `Auth token loaded from <path>`.

### Auth Middleware

A single HTTP middleware wraps the entire `ServeMux`. It checks every request with this logic:

```
1. Path is /dashboard/unauthorized → ALLOW (static help page)
2. Authorization: Bearer <token> header present and valid → ALLOW
3. mcp-broker-auth cookie present and valid → ALLOW
4. Path starts with /dashboard/ and ?token=<token> query param is valid →
     SET COOKIE + REDIRECT to same path without query param
5. Path starts with /dashboard/ → REDIRECT to /dashboard/unauthorized
6. Otherwise (/mcp) → 401 Unauthorized (JSON-RPC error)
```

Token comparison uses `crypto/subtle.ConstantTimeCompare` to prevent timing attacks.

### MCP Endpoint (`/mcp`)

- **Auth method:** `Authorization: Bearer <token>` header only.
- **On failure:** HTTP 401 with JSON body.
- **Client config example:**
  ```json
  {
    "mcpServers": {
      "broker": {
        "type": "streamableHttp",
        "url": "http://localhost:8200/mcp",
        "headers": {
          "Authorization": "Bearer <token>"
        }
      }
    }
  }
  ```

### Dashboard (`/dashboard/`)

- **Auth methods:** Cookie or Bearer header (both accepted).
- **Cookie setup flow:**
  1. Broker prints full auth URL at startup: `Dashboard: http://localhost:8200/dashboard/?token=<token>`
  2. User visits this URL once in their browser.
  3. Middleware validates token, sets `mcp-broker-auth` cookie, redirects to `/dashboard/`.
  4. All subsequent requests use the cookie automatically.
- **Cookie properties:**
  - Name: `mcp-broker-auth`
  - Value: the token (acceptable for localhost HTTP)
  - `HttpOnly: true`, `SameSite: Strict`, `Path: /dashboard/`
  - No `Secure` flag (plain HTTP on localhost)
  - Expiry: 1 year
- **Unauthorized page:** `/dashboard/unauthorized` — static page explaining how to authenticate (visit the URL from startup output).

### Protected Endpoints

All endpoints require auth:

| Endpoint | Primary Auth | Purpose |
|----------|-------------|---------|
| `POST /mcp` | Bearer header | MCP tool gateway |
| `GET /dashboard/` | Cookie | Web UI |
| `GET /dashboard/events` | Cookie | SSE real-time updates |
| `POST /dashboard/api/decide` | Cookie / Bearer | Approve/deny tool calls |
| `GET /dashboard/api/pending` | Cookie / Bearer | List pending requests |
| `GET /dashboard/api/tools` | Cookie / Bearer | List discovered tools |
| `GET /dashboard/api/audit` | Cookie / Bearer | Query audit logs |
| `GET /dashboard/unauthorized` | **None** | Auth help page |

### Implementation Scope

- **New file:** `internal/auth/auth.go` — token generation, loading, middleware
- **Modified:** `cmd/mcp-broker/serve.go` — wrap mux with auth middleware, add `--regen-token` flag
- **Modified:** `internal/dashboard/dashboard.go` — add `/unauthorized` route
- **New file:** Token file at `~/.config/mcp-broker/auth-token`
- **No external dependencies.** Uses only `crypto/rand`, `crypto/subtle`, `net/http`, `encoding/hex`.

### What This Does NOT Cover

- TLS/HTTPS (not needed for localhost)
- User accounts or role-based access (single token = single user)
- Token rotation/expiry (manual regeneration via `--regen-token`)
- Rate limiting on auth failures (low priority for localhost)
