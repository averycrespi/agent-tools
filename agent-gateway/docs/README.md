# Agent Gateway documentation

Choose documentation by the work you are doing. The same product and security contracts apply whether the reader is a human maintainer or a coding agent.

## Operate Gateway

Start with the [Gateway README](../README.md) for installation and the quick start, then use the focused procedure for the task:

- [Administrator CLI and local administration](operators/administration.md) — installation roots, startup, authentication, output, confirmation, and retry discipline.
- [Upgrade and compatibility](operators/upgrade-compatibility.md) — coordinated client/service cutovers, browser preference/session migration and retained durable identities.
- [Installation safety](operators/installation-safety.md) — existing-root selection, retained tombstones and recovery artifacts, and operator cleanup after migration retirement.
- [Run as a macOS launchd agent](operators/launchd.md) — per-user startup, verification, graceful maintenance, and native-keyring caveats.
- [Upstream server configuration](operators/upstream-servers.md) — servers, credentials, OAuth, operations, and catalogs.
- [Access control](operators/access-control.md) — principals, agent credentials, grants, and grant requests.
- [HTTP proxy setup](operators/http-proxy.md) — explicit activation, fresh client credentials and public CA trust.
- [Invocation evidence](operators/invocation-evidence.md) — retained evidence, redaction, and unknown outcomes.
- [Backup and recovery](operators/backup-and-recovery.md) — backups, verification, restore, and administrator reset.

Generated `agent-gateway --help` and subcommand help are the exact command and flag reference. Only `agent-gateway` is published; old standalone binaries and retired API/CLI grammar are unsupported. See [executable retirement and cleanup](operators/installation-safety.md#retired-executable-and-operator-cleanup) before reconciling installed artifacts. New installations use canonical naming; existing custom roots remain explicit selections. The migration capability is retired; retained tombstones still protect default selection, with no automatic relocation or cleanup. Operator guides own safe procedures and interpretation; they do not redefine product semantics.

## Change Gateway

Human maintainers and coding agents should begin with [maintainer and agent guidance](../CLAUDE.md), then use the focused workflow when applicable:

- [Frontend development](maintainers/frontend-development.md) — trusted live reload, visual inspection, and focused frontend checks.
- [Table conventions](design/browser-control-plane.md#table-conventions) — activity/resource column order, names, sizing, identity, status, and responsive behavior.
- [Release verification](maintainers/release-verification.md) — exact-revision acceptance evidence and report adoption for release owners.
- [Implementation evidence](maintainers/implementation-evidence.md) — producer-test provenance and historical measurements, not current-candidate qualification.

`AGENTS.md` links to `CLAUDE.md` so compatible coding agents discover the same repository-local constraints. Maintainer guides explain development procedures; product behavior remains owned by the design documentation.

## Understand product behavior

[DESIGN](../DESIGN.md) is the normative architecture index. Its domain chapters under [`design/`](design/) own intended behavior, invariants, lifecycle, and failure semantics.

The [browser control plane](design/browser-control-plane.md) owns normative browser behavior separately from server-side administration. The architecture index names every domain owner.

Authority is divided deliberately:

| Need                                             | Authority                                                 |
| ------------------------------------------------ | --------------------------------------------------------- |
| Product introduction and quick start             | [`README.md`](../README.md)                               |
| Operator procedure and interpretation            | [`operators/`](operators/)                                |
| Intended product behavior                        | [`DESIGN.md`](../DESIGN.md) and [`design/`](design/)      |
| Exact routes, values, and closed wire vocabulary | `internal/contract`                                       |
| Exact CLI syntax                                 | Generated `agent-gateway --help`                          |
| Repository editing constraints                   | [`AGENTS.md`](../AGENTS.md) / [`CLAUDE.md`](../CLAUDE.md) |
| Development and release procedure                | [`maintainers/`](maintainers/)                            |

When documents disagree, use the conflict rules in [DESIGN](../DESIGN.md#documentation-authority) and correct the conflicting sources together.
