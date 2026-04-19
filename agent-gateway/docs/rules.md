# Rules

Rules are declarative HCL files that tell agent-gateway what to do with each intercepted request: pass it through with real credentials injected, block it outright, or hold it for human approval. This document is the reference for rule syntax and evaluation semantics.

## File layout

Rules live under `~/.config/agent-gateway/rules.d/` as `*.hcl` files. The loader concatenates every `*.hcl` file in the directory in lexical filename order. Within a file, rules are evaluated top-to-bottom. **First match wins.**

One file per upstream is the intended convention (`github.hcl`, `atlassian.hcl`, `anthropic.hcl`). Prefixing filenames with `00-`, `10-`, `20-` gives a predictable priority knob:

```
~/.config/agent-gateway/rules.d/
  00-deny.hcl          # global deny rules, evaluated first
  10-github.hcl
  10-atlassian.hcl
  20-anthropic.hcl
```

## Schema

```hcl
rule "github-issue-create" {
  agents = ["claude-review", "codex-sandbox"]  # optional; omit for all agents

  match {
    host   = "api.github.com"                  # glob
    method = "POST"                            # exact, uppercase
    path   = "/repos/*/*/issues"               # glob

    headers = {                                # name → RE2 regex; AND of all entries
      "X-GitHub-Api-Version" = "^2022-"
    }

    json_body {                                # optional; at most one body block
      jsonpath "$.title"     { matches = "^\\[bot\\]" }
      jsonpath "$.labels[*]" { matches = "^automation$" }
    }
  }

  verdict = "allow"                            # allow | deny | require-approval

  inject {                                     # allowed on allow / require-approval
    replace_header = {
      "Authorization" = "Bearer ${secrets.gh_bot}"
    }
    remove_header = ["X-Agent-Hint"]
  }
}
```

### Top-level attributes

| Attribute | Type            | Required | Notes                                                                |
| --------- | --------------- | -------- | -------------------------------------------------------------------- |
| `agents`  | list of strings | no       | Omit to apply to all agents. Empty list (`[]`) is a load-time error. |
| `verdict` | string          | yes      | `allow`, `deny`, or `require-approval`.                              |

### `match` block

Every criterion is optional; an absent criterion is a wildcard. All declared criteria must succeed (AND).

| Attribute | Type           | Semantics                                                                                       |
| --------- | -------------- | ----------------------------------------------------------------------------------------------- |
| `host`    | string glob    | `*` within a hostname segment, `**` across segments.                                            |
| `method`  | string         | Exact match, uppercase.                                                                         |
| `path`    | string glob    | Same glob syntax as `host`.                                                                     |
| `headers` | map of strings | Each value is a Go RE2 regex matched against the canonical header. Missing headers never match. |

At most **one** body matcher per rule. A body matcher only runs on requests that (a) carry a body and (b) have a matching `Content-Type`. Requests without a body — `GET`, `DELETE`, `HEAD`, and `POST`/`PUT` with `Content-Length: 0` — never match a body-matcher rule.

#### `json_body` (Content-Type: application/json)

```hcl
json_body {
  jsonpath "$.title"     { matches = "^\\[bot\\]" }
  jsonpath "$.labels[*]" { matches = "^automation$" }
}
```

Each `jsonpath` label is a JSONPath expression; `matches` is an RE2 regex applied to each extracted string. All `jsonpath` blocks must succeed (AND).

#### `form_body` (Content-Type: application/x-www-form-urlencoded)

```hcl
form_body {
  field "grant_type" { matches = "^client_credentials$" }
}
```

#### `text_body` (Content-Type: text/\*)

```hcl
text_body {
  matches = "deploy-token-v2"
}
```

Regex applied directly to the raw body.

### `inject` block

Valid on `allow` and `require-approval` verdicts. Two verbs:

| Attribute        | Type            | Semantics                                                   |
| ---------------- | --------------- | ----------------------------------------------------------- |
| `replace_header` | map of strings  | Create or overwrite each named header with the given value. |
| `remove_header`  | list of strings | Remove the named headers.                                   |

`replace_header` covers the common "strip the dummy and set the real" case in a single verb — the header is unconditionally overwritten whether it was present or not. `remove_header` exists for the strip-only case (headers the agent set that the upstream should never see). `remove_header` is applied after `replace_header`.

### Template expansion

Inside `replace_header` values, two namespaces are available:

- `${secrets.<name>}` — resolved against the secrets store at request time.
- `${agent.name}` — the calling agent's name.

Expansion happens **at injection time**, not at rule-load time. Secrets are always the current live value — rotating a secret takes effect on the next request with no restart.

Secret values are interpolated as opaque bytes. No re-expansion, no escaping, no recursive resolution. A secret whose value contains `${x}` or backslashes is inserted literally.

## Verdicts

| Verdict            | Behaviour                                                                                                                                                     |
| ------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `allow`            | Apply `inject`, forward to upstream, audit with `injection='applied'` and `outcome='forwarded'`.                                                              |
| `deny`             | Return `403 Forbidden` to the agent; audit with `outcome='blocked'`.                                                                                          |
| `require-approval` | Park the request, push an SSE event to the dashboard, block until the approver decides (`approved` → continue as `allow`; `denied` → `403`; timeout → `504`). |

### Unmatched requests

If no rule matches, the request passes through untouched. The dummy credential in the sandbox reaches the upstream and fails there. This is the **fail-safe default**: forgotten routes fail as unauthenticated rather than leaking real credentials.

### Credential resolution failure

A rule matches but the referenced secret cannot be resolved (doesn't exist, or exists only under a scope the caller can't access): the rule fails soft. Dummy credentials go upstream untouched. The audit row records `injection='failed'` and `error='secret_unresolved'`, and the dashboard renders a "missing secret" badge on the offending rule. `agent-gateway rules check` surfaces unresolved references as warnings.

## Agent scoping

The `agents` attribute controls who a rule applies to:

- **Omitted** → applies to all agents.
- **Non-empty list** → applies only to listed agent names.
- **Empty list (`agents = []`)** → load-time error. Use rule deletion to disable a rule.

Agent scoping is enforced at two points:

1. **CONNECT time.** If no rule matching the target host applies to the calling agent, the gateway tunnels the connection (no TLS decryption). The dashboard surfaces this as "tunneled hosts" so gaps in coverage are visible.
2. **Request evaluation.** After MITM, only rules whose `agents` list includes the caller participate in matching.

## Evaluation order

Evaluation is strictly first-match-wins:

1. Files are loaded in lexical filename order.
2. Within a file, rules are evaluated top to bottom.
3. No separate "pass evaluation" sort by verdict type — if ordering matters, express it with filename prefixes and within-file order.

## Reload

Rule files are picked up via `agent-gateway rules reload`, which sends `SIGHUP` to the daemon. On reload:

1. The whole `rules.d/` directory is re-parsed.
2. HCL syntax, glob compilation, regex compilation, and template syntax are validated.
3. On success: the rule set is swapped atomically. In-flight requests finish on the old set; new requests use the new set.
4. On failure: the error is logged to stderr and the previous rule set stays live. This is the **fail-safe reload** — invalid edits never break the running daemon.

`SIGHUP` also re-reads `config.hcl`, rebuilds agent/secret caches, invalidates the decrypted-secret LRU, and reloads the root CA (re-reads `ca.key`/`ca.pem` from disk and clears the leaf-cert cache so subsequent connections are signed under the new root).

## Two-phase validation

Rule validation is split across two phases so that references can be written before the referenced resources exist:

- **Load time (strict).** Template syntax is validated — do variables parse, are names well-formed (`secrets.<identifier>` or `agent.<field>`)? Invalid syntax rejects the reload.
- **Request time (lazy).** The referenced secret either resolves or doesn't. Unresolved → the rule fails soft (see "Credential resolution failure" above). Existence of agents and secrets is not checked at load time.

This lets you write a rule that references a secret before creating it, delete a secret that's still referenced, or temporarily remove an agent's scope — all without breaking the running daemon.

## Body buffering

Body matchers require the request body to be buffered. Two bounds apply:

- **Size.** `proxy_behavior.max_body_buffer` (default `1MiB`) caps how much is buffered.
- **Time.** `timeouts.body_buffer_read` (default `30s`) caps how long buffering may stall a request.

Exceeding either bound causes all body matchers to **auto-fail** (the rule does not match). The audit row records `error='body_matcher_bypassed:size'` or `:timeout`. This fail-soft behaviour means rule matching never stalls forever — a slow or oversized upload bypasses body matchers but the request is still forwarded.

## `rules check`

Validate rule files without running the daemon:

```bash
agent-gateway rules check
```

Reads from `$XDG_CONFIG_HOME/agent-gateway/rules.d/` and cross-references `${secrets.X}` references against the live state DB. Reports:

- Parse errors (exits non-zero).
- Unresolved `${secrets.X}` references (warnings only, does not affect exit code).

If the state DB is unavailable (e.g. on a fresh install before any `secret set`), every `${secrets.X}` reference becomes a warning — fail-open, so the check never blocks on missing infrastructure.

## Authoring tips

- **Start specific, widen as needed.** A narrow `match` block is safer than a broad one — the worst case for a too-broad rule is injecting real credentials into an unintended request.
- **Use the `match` block, not rule proliferation.** If you find yourself writing two rules that differ only in header values, fold them into one rule with a `headers = { … }` block.
- **Don't rely on body content to authorise.** Body matchers run after header matchers; if a request has bypassed the size/timeout cap, body matchers auto-fail. Use body matchers to narrow intent, not as the sole authorisation signal.
- **Keep rule intent visible at code review.** Express "only set the header if it's already present" as a `headers` match on the dummy value, not as an implicit injection behaviour.
