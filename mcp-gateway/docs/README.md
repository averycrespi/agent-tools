# MCP Gateway documentation

Choose documentation by the work you are doing. The same product and security contracts apply whether the reader is a human maintainer or a coding agent.

## Operate Gateway

Start with the [Gateway README](../README.md) for installation and the quick start, then use the focused procedure for the task:

- [Administrator CLI and local administration](operators/administration.md) — installation roots, startup, authentication, output, confirmation, and retry discipline.
- [Upstream server configuration](operators/upstream-servers.md) — servers, credentials, OAuth, operations, and catalogs.
- [Access control](operators/access-control.md) — principals, agent credentials, grants, and grant requests.
- [Invocation evidence](operators/invocation-evidence.md) — retained evidence, redaction, and unknown outcomes.
- [Backup and recovery](operators/backup-and-recovery.md) — backups, verification, restore, and administrator reset.

Generated `mcp-gateway --help` and subcommand help are the exact command and flag reference. Operator guides own safe procedures and interpretation; they do not redefine product semantics.

## Change Gateway

Human maintainers and coding agents should begin with [maintainer and agent guidance](../CLAUDE.md), then use the focused workflow when applicable:

- [Frontend development](maintainers/frontend-development.md) — trusted live reload, visual inspection, and focused frontend checks.
- [Release verification](maintainers/release-verification.md) — exact-revision acceptance evidence and report adoption for release owners.

`AGENTS.md` links to `CLAUDE.md` so compatible coding agents discover the same repository-local constraints. Maintainer guides explain development procedures; product behavior remains owned by the design documentation.

## Understand product behavior

[DESIGN](../DESIGN.md) is the normative architecture index. Its domain chapters under [`design/`](design/) own intended behavior, invariants, lifecycle, and failure semantics.

Authority is divided deliberately:

| Need                                             | Authority                                                 |
| ------------------------------------------------ | --------------------------------------------------------- |
| Product introduction and quick start             | [`README.md`](../README.md)                               |
| Operator procedure and interpretation            | [`operators/`](operators/)                                |
| Intended product behavior                        | [`DESIGN.md`](../DESIGN.md) and [`design/`](design/)      |
| Exact routes, values, and closed wire vocabulary | `internal/contract`                                       |
| Exact CLI syntax                                 | Generated `mcp-gateway --help`                            |
| Repository editing constraints                   | [`AGENTS.md`](../AGENTS.md) / [`CLAUDE.md`](../CLAUDE.md) |
| Development and release procedure                | [`maintainers/`](maintainers/)                            |

When documents disagree, use the conflict rules in [DESIGN](../DESIGN.md#documentation-authority) and correct the conflicting sources together.
