# typesafe-mcp

Stateless TypeSafe inference over MCP stdio. See README for public contracts and DESIGN for intended behavior.

## Development

```bash
make build
make test               # race-enabled ordinary tests
make test-integration   # integration-tagged TestIntegration owner only
make lint
make fmt
make tidy
make audit              # tidy, fmt, lint, race tests, govulncheck
```

Run `make audit` before committing. Root `make test-ci` checks inventory and integration ownership. Ordinary tests must never require credentials, TypeSafe calls, or a live Gateway.

## Layout and invariants

- `cmd/typesafe-mcp/`: thin Cobra/environment/signal composition.
- `internal/server/`: strict MCP registration and bounded stdio reader.
- `internal/provider/`: schemas, safe validation, one-shot transport and limits.
- Preserve both mcp-go strict-input options; raw schemas explicitly close contract objects while dynamic maps and JSON data remain extensible.
- No tool-level destination, credential, header or limit controls. Never add an environment endpoint override; binary smoke uses a temporary build overlay only.
- Do not wrap raw upstream errors with `%w` into caller-visible errors; transport and provider messages can contain secrets. Use the existing safe category mapping.
- Never log input, output, credential values or provider bodies. Keep SDK diagnostics discarded.
- Every HTTP operation has a positive deadline and exactly one attempt; no automatic retry after ambiguous effects. Keep no-proxy/no-redirect/no-keepalive transport and unset replay bodies.
- Integration tests own subprocess and fixture cleanup with finite contexts. Keep build tag `integration`, `TestIntegration*` names, and explicit Make package selection aligned.
- Gateway compatibility probe uses exact checkout catalog code, not a copied policy or installed Gateway. No Gateway Go edits are necessary.
