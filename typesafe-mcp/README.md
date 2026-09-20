# TypeSafe MCP

Stateless Go stdio MCP server exposing exactly `evaluate` and `list_models` for [TypeSafe](https://docs.typesafe.ai/api). No listener, database, saved rubrics, filesystem fetching, or action policy.

## Install and use

Requires the repository's Go toolchain (Go 1.26.6 or later).

```bash
make -C typesafe-mcp install
```

Run `typesafe-mcp` as an MCP subprocess. It reads its credential only from `TYPESAFE_API_KEY`; missing or blank values fail startup. Supply it through a secret manager, not tool arguments, command-line flags, or committed files. Stdout is protocol-only; no payloads, credentials, provider bodies, or results are logged. There is no endpoint override.

### Agent Gateway

Example secret-free server definition (replace absolute executable and working-directory paths for your host):

```json
{
  "namespace": "typesafe",
  "display_name": "TypeSafe inference",
  "enabled": false,
  "transport": {
    "kind": "stdio",
    "executable": "/absolute/path/to/typesafe-mcp",
    "arguments": [],
    "working_directory": "/absolute/working/directory",
    "environment": {},
    "secret_environment": { "TYPESAFE_API_KEY": "typesafe_api_key" }
  }
}
```

Gateway starts subprocesses with a clean environment, so an ambient shell export is not enough. Register the server and provision the `typesafe_api_key` slot through Gateway's separate write-only credential workflow, then enable it only when authorized. See [upstream servers](../agent-gateway/docs/operators/upstream-servers.md). Configuration examples do not authorize installation, credential changes, or paid calls.

## Tools

### `evaluate`

Arguments: required `state` (string/object/array), required nonempty `questions` map, optional nonblank `model` (at most 256 characters). State and instructions cannot be null. Every question has `type` and `instructions` (string/object/array):

- `choice`: required nonempty `criteria` map from labels to descriptions.
- `score`: required ordered `criteria` array with at least two levels; indices start at zero.
- `noul`: optional `criteria` object containing optional `true` and `false` descriptions.

Descriptions accept strings, JSON objects, arrays, or null. Arbitrary JSON remains supported inside state/instructions/descriptions; other contract fields are closed. Dynamic IDs and labels are preserved.

One call makes one authenticated `POST https://api.typesafe.ai/v1/systemone`, combining all primitives:

```json
{
  "state": { "message": "I was charged twice; please help today." },
  "questions": {
    "route": {
      "type": "choice",
      "instructions": { "task": "Choose the responsible team" },
      "criteria": {
        "billing": { "topics": ["charges", "refunds"] },
        "other": null
      }
    },
    "severity": {
      "type": "score",
      "instructions": ["Rate operational severity"],
      "criteria": ["Minor", { "meaning": "Significant financial impact" }]
    },
    "urgent": {
      "type": "noul",
      "instructions": "Is urgent action requested?",
      "criteria": { "true": "Explicit deadline", "false": null }
    }
  }
}
```

Structured MCP output contains provider-reported `model`, `answers` keyed by question ID, and `usage.input_tokens` / `usage.output_tokens`. Choice preserves `choice`, `probabilities`, and `confidence` when present; Score preserves fractional `score`, `legend`, `probabilities`, and optional `confidence`; Noul returns only its `noul` probability and type, never an invented Boolean/confidence. Missing, extra, mismatched, or malformed answers fail. No thresholds or downstream actions are applied.

The default `jev-latest` alias can change behavior. Set an explicit version such as `jev-1.13.0` for pinning; explicit IDs need not appear in model discovery. See [models](https://docs.typesafe.ai/models). Pinning does not promise provider availability or identical outputs.

`evaluate` is annotated **not read-only**, **not idempotent**, non-destructive, and open-world: it discloses data and spends quota even though it does not modify local state.

### `list_models`

Arguments: `{}`. Makes one authenticated `GET https://api.typesafe.ai/v1/models`. Structured output is `{"models":[{"name":"...","description":"...","release_date":"YYYY-MM-DD"}]}`. Listing is informational, not a model allowlist. Annotated read-only, idempotent, non-destructive, open-world.

## Fixed resource limits and failures

Limits are compiled in and cannot be disabled by tool arguments:

| Resource                             | Limit                                                      |
| ------------------------------------ | ---------------------------------------------------------- |
| HTTP exchange, including body        | 30 seconds (earlier caller cancellation wins)              |
| Dial / TLS handshake                 | 10 seconds each, within exchange deadline                  |
| Encoded arguments / upstream request | 256 KiB, including default model                           |
| Provider response                    | 512 KiB (read stops at limit + 1 byte)                     |
| HTTP response headers                | 32 KiB                                                     |
| Questions per evaluation             | 32                                                         |
| JSON nesting                         | 32 container levels for arguments and responses            |
| Model cards                          | 256                                                        |
| Concurrent upstream requests         | 4; excess rejected without dispatch                        |
| Stdio input frame                    | 1 MiB, newline required; 40 container levels               |
| Stdio tool workers / queued calls    | 4 / 4 (SDK fallback remains subject to upstream admission) |

Output uses one structured result plus a fixed text fallback, leaving substantial room beneath Gateway's 4 MiB frame limit. Duplicate JSON keys, invalid framing, and oversized/deep frames terminate the stdio session with a generic error. Invalid tool arguments are rejected before dispatch.

No redirects, automatic retries, connection reuse, ambient proxies, or replay—even for 429, 529, timeout, or connection failure. Errors use safe categories: authentication, validation, capacity, rate_limit, overload, redirect_rejected, deadline, canceled, transport, malformed_response, response_limit, or upstream. Valid numeric or HTTP-date `Retry-After` guidance is preserved; raw provider errors are discarded. Failed tool results also carry [versioned safe diagnostics](DESIGN.md#trust-boundaries) in MCP `_meta`, including distinct `timeout`, `json_decode`, and `response_contract` categories. Cooperating Gateways display these as unverified server reports, separately from their own failure observations; parsed retry deltas are bounded to one day. A cancellation/transport failure may have happened **after** inference spent quota. Any new attempt requires the caller to consider duplicate effects; do not infer nonexecution from an error.

## Privacy and confidence

Calls disclose state, questions, and rubrics to TypeSafe and may incur charges. Only submit data you are authorized to disclose. This server's lack of payload logging is not a confidentiality or provider zero-retention guarantee. Gateway can retain fixed-redacted argument captures; redaction does not detect every secret. Read [invocation evidence](../agent-gateway/docs/operators/invocation-evidence.md).

Confidence is model-reported uncertainty, not authorization, guaranteed correctness, or permission to take an action. Keep consequential policy and human approval outside this adapter.

## Verification

```bash
make -C typesafe-mcp build
make -C typesafe-mcp audit
make -C typesafe-mcp test-integration
make test-ci
make check
```

Tests use credential-free deterministic fixtures. The integration owner builds the real CLI with a test-only Go build overlay replacing the destination constant with a disposable local HTTP fixture; production sources/configuration remain fixed. It verifies stdio initialize/list/call, output cleanliness, and process cleanup, then executes this checkout's Gateway catalog traversal/normalization in a temporary probe module. No live Gateway installation is modified.

Fixture/schema evidence is **not** live TypeSafe compatibility or native Gateway qualification. Structured/null descriptions follow the [advanced guide](https://docs.typesafe.ai/primitives/advanced) and JavaScript [EntryType](https://docs.typesafe.ai/sdk/javascript/api/type-aliases/EntryType) / [ScoreCriteria](https://docs.typesafe.ai/sdk/javascript/api/type-aliases/ScoreCriteria); published SDKs differ on some nullable types. Live validation needs separate explicit consent and is never part of ordinary tests.
