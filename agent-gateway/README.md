# Agent Gateway

**Give agents useful access without handing them your service credentials.**

Agent Gateway is a local access-control service for three independent workflows:

- **MCP:** bring servers behind one endpoint; grant individual tools or constrained arguments, and let agents request access.
- **HTTP/HTTPS:** authorize requests with per-agent policy, inject upstream credentials, and intercept HTTPS with explicitly configured client trust.
- **Git:** use native smart-HTTPS Git with configured repositories, read/ref permissions and Git-only credentials. SSH, LFS and dumb HTTP are not supported.

Each agent has its own identity and one Gateway credential. Upstream credentials and OAuth stay with Gateway, encrypted in control SQLite with a separate owner-only master key. Manage access through the browser or CLI, inspect optional traffic history, and keep administrative audit separate from execution evidence.

Gateway works with your preferred client and sandbox. Sandbox Manager, Lima and a particular agent harness are not dependencies. It listens on loopback; VMs and containers need a separately trusted forwarding path. It is not an OS sandbox or network-enforced egress boundary.

## Installation

The Gateway **host requires Linux or macOS**, Go 1.26.9 or later, and GNU Make. No operating-system keyring is required. Clients can use other environments with compatible transports and trust configuration.

From the `agent-gateway` directory:

```bash
make install
```

This installs only `agent-gateway` into `$(go env GOPATH)/bin`; put that directory on your `PATH` before the commands below.

## Quick start

For a new installation, initialize the owner-only data directory and start the foreground service:

```bash
agent-gateway init
agent-gateway serve
```

`init` asks for confirmation and creates the protected `<data-dir>/master-key`, initial administrator authority and interception CA. It reports the administrator bearer file at `<data-dir>/admin-bearer` and exports the **public** CA at `<data-dir>/http-ca.pem`. Use `--confirm` for noninteractive setup. No service supervisor or client trust is installed.

In another terminal:

```bash
agent-gateway doctor --online
```

Open **http://127.0.0.1:8210/** and sign in with the administrator bearer from the protected file reported by `init`. Read it only for that handoff; do not put it in argv, environment variables, logs or saved browser settings. Never give this bearer to an agent. The CLI reads the protected file directly; [administrator authentication](docs/operators/administration.md#administrator-authentication) covers explicit/custom locations.

### Connect your first agent

For a first MCP tool call:

1. [Register an upstream server](docs/operators/upstream-servers.md#register-and-inspect-a-server). Supply any required credentials separately, enable it deliberately, and verify its active catalog.
2. [Create an agent](docs/operators/access-control.md#create-and-inspect-principals), then [issue its credential](docs/operators/access-control.md#issue-rotate-or-revoke-an-agent-credential) to a fresh private file. Creation grants only Gateway's six self-service tools, not downstream tools.
3. [Grant the intended tool](docs/operators/access-control.md#create-and-inspect-immutable-grants), using an exact upstream name and constraints where appropriate. Discovery visibility is not permission to call.
4. [Configure the MCP client](docs/operators/access-control.md#configure-an-agent-client-manually) at `http://127.0.0.1:8210/mcp`, loading the agent token through its supported secret mechanism. Start with `tools/list` or the read-only `mcp_gateway.get_identity`; then make one explicitly authorized downstream call.
5. Inspect [invocation evidence](docs/operators/invocation-evidence.md) if needed. Missing history does not prove nonexecution; an unknown outcome is not permission to retry.

## Common workflows

For HTTP or Git, reuse the agent identity and credential but configure **separate protocol permissions**:

| Task                          | Follow                                                                                                                                                                                                                                                                          |
| ----------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| MCP tools and access requests | [Upstreams](docs/operators/upstream-servers.md), [agents and MCP grants](docs/operators/access-control.md)                                                                                                                                                                      |
| HTTP/HTTPS requests           | [HTTP grants and policy preview](docs/operators/access-control.md#http-grants-and-test-access), [scoped credentials](docs/operators/administration.md#scoped-http-credentials), [client proxy and CA setup](docs/operators/http-proxy.md)                                       |
| Native smart-HTTPS Git        | [Git setup and first read-only check](docs/operators/git.md)                                                                                                                                                                                                                    |
| Investigate activity          | [MCP invocations](docs/operators/invocation-evidence.md), [HTTP traffic](docs/operators/administration.md#http-traffic-history), [Git traffic](docs/operators/git.md#git-traffic-history), [administrative audit](docs/operators/administration.md#control-plane-audit-history) |
| Back up or recover            | [Control backups, separate history export and stopped recovery](docs/operators/backup-and-recovery.md)                                                                                                                                                                          |

HTTP proxy listening is default-on at `127.0.0.1:8212`; client use and CA trust are explicit setup. Use `serve --clear-http-proxy-listen` for MCP-only startup. [Proxy setup](docs/operators/http-proxy.md) explains custom listeners and the narrow historical launchd exception. Supervise the foreground process with operator-owned [launchd](docs/operators/launchd.md) or [systemd](docs/operators/systemd.md) configuration.

To explore without your real credentials, run `make serve-demo` **from the `agent-gateway` directory**. It creates a disposable installation with encrypted custody and curated MCP/HTTP/Git examples, not OS sandbox isolation. [Demo instructions](docs/maintainers/frontend-development.md#use-a-disposable-feature-branch-gateway) own datasets, credentials and cleanup.

## Existing installations and upgrades

Preserve existing roots, credentials, tombstones and recovery artifacts. Do not reinitialize, rotate credentials or delete markers for a name change. Follow [installation safety](docs/operators/installation-safety.md) and [upgrade compatibility](docs/operators/upgrade-compatibility.md), including the [legacy custody escalation boundary](docs/operators/backup-and-recovery.md#legacy-custody-upgrade-boundary), before changing an existing installation.

Only legacy single-store installations need [stopped traffic migration](docs/operators/backup-and-recovery.md#migrate-existing-invocation-storage) to enable separated history; already-selected traffic stores must not be migrated again. Ordinary startup does not require that migration to serve security-ready with history disabled.

## Security and uncertain outcomes

- Loopback does not isolate untrusted processes running as the same OS user. Proxy environment settings are cooperative, not containment.
- Administrator, agent and upstream credentials have different authority. Raw secrets never belong in argv, ordinary configuration, logs or read APIs. Only [supported client exports](docs/operators/access-control.md#configure-an-agent-client-manually) and [declared stdio secret slots](docs/design/downstream-servers.md#direct-stdio-supervision) permit runtime environment delivery; no administrator environment fallback exists.
- MCP calls require current `ALLOW`; HTTP defaults/grants and Git policy are independent. An access-request approval never executes or resumes a call.
- Gateway does not automatically replay governed operations. `outcome_unknown` means an effect may already have occurred; inspect the authoritative system before deciding on a new caller-owned attempt. HTTP 200 is not proof of Git push success.
- [Safeguard the master key separately from backups](docs/operators/backup-and-recovery.md#encrypted-secret-storage). Format-4 control backups recover historical upstream secrets and CA but omit traffic; restore invalidates administrator and agent tokens.

## Documentation

Use the [documentation map](docs/README.md) for operator tasks and architecture; [DESIGN.md](DESIGN.md) owns intended behavior.

## Development

Maintainers and coding agents start with [CLAUDE.md](CLAUDE.md) for focused checks, ownership and editing constraints. [Frontend development](docs/maintainers/frontend-development.md) covers live reload, and [release verification](docs/maintainers/release-verification.md) owns exact-revision acceptance.
