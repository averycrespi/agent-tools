# Server configuration and credentials

Audience: Gateway operators configuring upstream MCP servers

Purpose: Configure servers, credentials, and OAuth without broadening trust.

This guide owns server configuration, durable catalog inspection, write-only static credentials, OAuth authorization, and runtime operations. Generated `mcp-gateway server --help` and `mcp-gateway catalog --help` own command syntax.

## Guide boundary

- See [DESIGN](../DESIGN.md) for normative transport, credential-authority, OAuth, catalog, and runtime semantics.
- See [CLI and local administration](cli-local-administration.md) for shared authentication and output behavior.
- Return to the [Gateway README](../README.md) for installation and common workflows.

This guide explains operator procedures without redefining the normative transport, credential, or runtime contracts.
