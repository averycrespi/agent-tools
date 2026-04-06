# Architecture

## Request Flow

```mermaid
flowchart TD
    Agent["MCP Client"] -->|"HTTP + Bearer token"| MCP["MCP Server"]
    CLI["Broker CLI"] -->|"HTTP + Bearer token"| MCP

    MCP --> Rules["Rules Engine"]

    Rules -->|allow| Proxy["Proxy"]
    Rules -->|deny| Error["Return Error"]
    Rules -->|require-approval| Approver["Approver"]

    Approver -->|approved| Proxy
    Approver -->|denied| Error

    Proxy -->|stdio| Git["Local MCP Server"]
    Proxy -->|HTTP / SSE| Remote["Remote MCP Server"]

    Git --> Response["Return Response"]
    Remote --> Response

    Error --> Audit["Auditor"]
    Response --> Audit

    style MCP fill:#4a90d9,color:#fff
    style Rules fill:#4a90d9,color:#fff
    style Approver fill:#4a90d9,color:#fff
    style Proxy fill:#4a90d9,color:#fff
    style Agent fill:#95a5a6,color:#fff
    style CLI fill:#95a5a6,color:#fff
    style Git fill:#9b59b6,color:#fff
    style Remote fill:#9b59b6,color:#fff
    style Audit fill:#4a90d9,color:#fff
    style Response fill:#7ed321,color:#fff
    style Error fill:#d0021b,color:#fff
```

### Pipeline stages

1. **Rules** -- Tool name matched against glob patterns, first match wins. Three verdicts: `allow`, `deny`, `require-approval`
2. **Approval** -- If required, the call blocks until a human approves or denies
3. **Proxy** -- Server Manager routes to the correct backend by tool prefix (e.g. `git.push` routes to the `git` backend)
4. **Audit** -- Every call is recorded in the audit log: tool name, arguments, verdict, approval decision, and success/error

### Entry points

- **MCP clients** connect directly to `/mcp` using standard MCP protocol over HTTP
- **Broker CLI** connects to the same `/mcp` endpoint, discovers tools at startup, and exposes them as shell commands with typed flags

### Backend providers

Providers are pluggable MCP servers connected via stdio, Streamable HTTP, or SSE. The broker discovers their tools on startup and re-exposes them with `<server>.<tool>` namespacing. Credentials stay on the host -- stdio providers like `local-git-mcp` shell out to already-authenticated host binaries.
