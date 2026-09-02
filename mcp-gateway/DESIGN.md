# MCP Gateway Design

## Purpose

MCP Gateway is a single-user local service that provides a strict HTTP/MCP boundary, durable administrative authority, governed downstream invocation, and typed browser and CLI control planes. Every classified call is rejected safely or admitted through one durable audit transaction before one immediate attempt on a pinned downstream capability or fixed local handler. Gateway retains no queue, replay state, successful result, or raw downstream error.

This document is the normative architecture overview and index. The linked design chapters own detailed product behavior. Operator procedures belong to the focused guides under [`docs/`](docs/), while maintainer commands, package layout, and editing invariants belong to [`CLAUDE.md`](CLAUDE.md).

## Documentation authority

Normative behavior is divided by stable product domain:

| Domain                                                                                      | Normative chapter                                                           |
| ------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| Public routes, problems, limits, representations, and strict JSON                           | [Public contract](docs/design/public-contract.md)                           |
| Principals, credentials, grants, policy, and self-service requests                          | [Identity and authorization](docs/design/identity-and-authorization.md)     |
| SQLite durability, migration, mutation recovery, backup, and restore                        | [Storage and recovery](docs/design/storage-and-recovery.md)                 |
| Server authority, keyring cutover, runtimes, transports, OAuth, and catalogs                | [Downstream servers](docs/design/downstream-servers.md)                     |
| Governed calls, audit evidence, protocol eras, authentication, and MCP sessions             | [Invocation and MCP ingress](docs/design/invocation-and-ingress.md)         |
| Administrator authority, HTTP administration, browser and CLI clients, events, and shutdown | [Administrative control plane](docs/design/administrative-control-plane.md) |

The chapters are normative for product intent, invariants, ownership, lifecycle, and failure semantics. `internal/contract` is the executable authority for public routes, safe problems, media and protocol values, fixed limits, closed states and reasons, resource mechanics, approved secret sinks, and behavior manifests. Contract code, tests, and the owning design chapter must change together when behavior changes.

Operational guides explain safe procedures without redefining product semantics. In executable documentation manifests, “canonical owner” identifies the guide that owns a workflow or operator explanation, not a competing source of product semantics:

- [CLI and local administration](docs/cli-local-administration.md)
- [Server configuration and credentials](docs/server-configuration.md)
- [Principals, grants, and requests](docs/access-policy.md)
- [Invocation evidence and unknown outcomes](docs/invocation-evidence.md)
- [Backup, restore, and recovery](docs/recovery.md)
- [Frontend development](docs/frontend-development.md)
- [Release verification and acceptance evidence](docs/release-verification.md)

If summaries disagree, the owning normative chapter controls product intent and `internal/contract` controls its executable closed vocabulary. Generated CLI help and Make targets control command and target syntax respectively.

## Security model

- Bind only one configured numeric IPv4 loopback authority. Reject aliases, wildcard and non-loopback binds, alternate Host authorities, forwarding headers, trusted proxies, and CORS.
- Own every route and method explicitly. Authenticate production MCP requests before reading or classifying their bodies.
- Keep administrator and agent credentials, middleware, identifiers, and invalidation paths separate. Raw secrets may appear only at approved one-time sinks.
- Treat SQLite availability and integrity as security state. Security-critical writes fail closed, uncertain durability latches storage, and recovery is stopped-process only.
- Treat OS keyring support as an explicit typed capability with no plaintext fallback.
- Keep registries and admission controls bounded and nonblocking. Restart discards sessions, streams, subscribers, runtime publications, and in-flight work.
- Supply at most one automatic downstream attempt. Uncertain handoff never causes retry, reroute, or replay.

Exact authorities, limits, states, and failure vocabularies are owned by the relevant design chapters rather than repeated here.

## System boundaries and composition

`cmd/mcp-gateway` constructs one `composition` graph before opening the listener. Domain packages own their SQL, process-local state, transport, protocol, and lifecycle behavior behind narrow interfaces. The command root composes those owners but does not become an alternate authority.

Durable desired state remains separate from process-local runtime and active publication. Administrator authority remains separate from agent authority. Authentication remains separate from authorization; discovery remains separate from capability acquisition; audit admission completes before execution; and one-time secret ingress and output remain separate from reusable state.

Dependencies flow from the command composition root into domain packages. Domain packages do not import `cmd`, share authority across credential domains, introduce a cross-module internal library, or bypass the owning storage, keyring, network, process, or protocol boundary. The complete package inventory and dependency conventions live in [`CLAUDE.md`](CLAUDE.md).

## Platform choices

- Go module: `github.com/averycrespi/agent-tools/mcp-gateway`
- MCP protocol adapter: `github.com/modelcontextprotocol/go-sdk`
- SQLite: `github.com/ncruces/go-sqlite3`, with live connection settings explicitly verified
- Credential provider: `github.com/zalando/go-keyring`, behind an injectable typed adapter
- CLI: Cobra
- Browser application: strict TypeScript and Preact, built by Vite into a fixed embedded allowlist

The official MCP SDK does not own Gateway authentication, protocol downgrade decisions, limits, or lifecycle. SQLite defaults are not trusted in place of explicit per-connection setup and verification. Keyring errors are not collapsed into absence.

## Operational composition and compatibility

The executable exposes stopped-process initialization, administrator reset, current-generation verification, and verified backup replacement. The serving process provides the verified HTTP/control composition, embedded browser application, online CLI API, server reconciliation, and production MCP ingress.

Before opening the listener, composition validates that every required authority, repository, service, cursor, pager, and ingress adapter is present as one complete dependency graph. Readiness begins before downstream runtime reconstruction so a server-specific failure remains isolated.

Product compatibility includes public HTTP and MCP JSON, CLI domain command spellings, durable schema and backup lineage, fixed limits, and runtime behavior. Supported restore lineages and MCP protocol eras are defined in their owning chapters. Maintainer acceptance reports and external sidecars are definition-bound evidence artifacts rather than product interfaces; the release verification guide owns their procedures and compatibility rules.

## Non-goals

Gateway does not provide audit mutation or replay, held calls, automatic grant-request creation, automatic invocation replay, direct agent grant mutation, grant renewal, request notification, reviewer identity, pre-Streamable HTTP+SSE, or MCP list-change notifications.

It does not promise exactly-once downstream effects, infer rollback from missing evidence, persist runtime capabilities or sessions, expose raw secrets or dependency errors, or provide a plaintext credential fallback.
