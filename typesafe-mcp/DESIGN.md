# TypeSafe MCP design

## Intent

Give agents behind Agent Gateway a minimal stateless inference adapter: exactly `evaluate` and `list_models`. Agents supply their own state, questions and rubrics. The server does not prescribe evaluations or turn probabilities into authorization, Boolean decisions, or actions.

## Architecture

- `cmd/typesafe-mcp`: Cobra stdio entry point, environment credential loading and signal cancellation.
- `internal/server`: strict mcp-go catalog and runtime validation, bounded NDJSON input, fixed-result fallback and silent SDK diagnostics.
- `internal/provider`: shared JSON Schema contracts, bounded JSON validation, request admission, one-shot HTTP and response correspondence checks.

One evaluation contains every named Choice/Score/Noul question. The default model is `jev-latest`; explicit version IDs are passed through without discovery or allowlisting. Responses preserve provider model, answer IDs, distributions, legends, optional confidence and token counts. Unknown contract fields and malformed/mismatched responses fail closed. Structured descriptions and null criteria remain valid; state/instructions must be non-null string/object/array values. Schema validation is applied both by MCP and by the provider boundary.

## Trust boundaries

Only startup `TYPESAFE_API_KEY` provides credentials. Production destinations are fixed HTTPS TypeSafe endpoints; tool arguments cannot alter destination, headers, credential source, limits or transport. A private HTTP/1 transport disables proxies, cookies, compression and connection reuse; request replay bodies are unset. No redirects or retries occur. Internal tests can inject transports, and binary fixtures substitute only the destination constant through an isolated build overlay.

No payload-bearing logs exist. Errors expose closed categories, status codes, and parsed retry guidance, not raw upstream text or transport diagnostics. Submitted content crosses the TypeSafe boundary and can be retained by Gateway as redacted argument evidence. Tool annotations reflect quota consumption and external disclosure, not merely local state mutation.

Failures carry version-1 closed diagnostic metadata under MCP result `_meta["io.github.averycrespi.agent-tools/failure"]`. Required `category` distinguishes authentication, rate limiting, timeout, JSON decoding and response-contract rejection; `phase` distinguishes admission, exchange, response status, decoding and validation. Optional integer `http_status` is 100–599 and `retry_after_seconds` is 0–86400. Retry-After dates become a bounded remaining delta; malformed, duplicate, past or excessive guidance is omitted. Provider error bodies and raw transport errors are never included. Agent Gateway validates this independent-module wire contract and labels it server-reported, not observed fact. It does not authorize a retry or establish nonexecution. Existing safe text remains available to direct MCP clients.

## Resource and lifecycle contract

The [README limits](README.md#fixed-resource-limits-and-failures) are the concrete v1 contract. HTTP dispatch has four immediate permits and one 30-second context/client deadline covering connection and response. Cancellation propagates to the request; every permit remains held until the exchange returns. There is no inference queue in the provider. The SDK uses four workers and four queued protocol calls; its synchronous overflow path still passes provider admission.

Before SDK decoding, a bounded reader accepts only complete newline-terminated JSON frames, rejects duplicates and excessive nesting, and caps frame memory. Provider requests and responses have tighter byte/depth limits. Structured output avoids a duplicate escaped text copy and remains below Gateway's frame ceiling. No persistent application state or network listener exists. Signal cancellation shuts down serving; uncertain remote execution is never replayed.

## Verification ownership

Ordinary race tests own schema/input/output, mixed primitives, correspondence, credential/transport policy, one-attempt failures, limits and cancellation. The `integration`-tagged `TestIntegrationStdioAndGatewayCatalog` owns the real CLI and disposable HTTP fixture, process deadlines/reaping and actual checked-out Gateway catalog normalization. A temporary lexical child module permits testing Gateway internals without adding a production dependency or editing Gateway code. CI selects this integration owner whenever Gateway is selected. No fixture proves live provider behavior; live qualification requires separate approval.
