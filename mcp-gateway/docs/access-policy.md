# Principals, grants, and requests

Audience: Gateway administrators managing agent access

Purpose: Manage principals, credentials, grants, and grant requests.

This guide owns operator workflows for principal lifecycle, one-time agent credentials, immutable grants, constraints, and grant-request adjudication. Generated `mcp-gateway principal --help`, `mcp-gateway grant --help`, and `mcp-gateway grant-request --help` own command syntax.

## Guide boundary

- See [DESIGN](../DESIGN.md) for normative authorization, policy evaluation, and request-state semantics.
- See [CLI and local administration](cli-local-administration.md) for shared authentication, output, and consequence confirmation.
- See [Invocation evidence](invocation-evidence.md) for interpreting authorization and outcome evidence.

This guide explains operator procedures without restating the normative authorization and request contracts.
