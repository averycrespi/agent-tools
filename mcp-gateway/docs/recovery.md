# Backup, restore, and recovery

Audience: Operators responsible for Gateway recovery

Purpose: Create backups and perform restore or stopped-process recovery safely.

This guide owns backup lifecycle, restore verification, administrator reset, stopped-process requirements, compatibility, and uncertain recovery failures. Generated `mcp-gateway backup --help`, `mcp-gateway restore --help`, and `mcp-gateway admin-reset --help` own command syntax.

## Guide boundary

- See [DESIGN](../DESIGN.md) for normative storage, backup, migration, and authority-invalidation semantics.
- See [CLI and local administration](cli-local-administration.md) for installation paths, output, and one-time secret sinks.
- Return to the [Gateway README](../README.md) for ordinary startup and status checks.

This guide explains recovery procedures without weakening stopped-process, compatibility, or authority-invalidation requirements.
