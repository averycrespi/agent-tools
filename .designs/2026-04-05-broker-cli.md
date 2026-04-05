# broker-cli Design

**Date:** 2026-04-05  
**Status:** Draft

## Overview

`broker-cli` is a dynamic CLI frontend for the MCP broker. It connects to the broker at startup, discovers all available tools via `tools/list`, and generates a command tree from the tool schemas at runtime. This lets agents that prefer CLI over native MCP interact with the broker using standard shell invocations.

## Primary User

AI agents that don't support native MCP (shell-based agents, exec-preferring agents). Output is designed to be machine-readable.

## Connection & Auth

Two environment variables are required:

- `MCP_BROKER_ENDPOINT` — broker URL (e.g. `http://localhost:8200`)
- `MCP_BROKER_AUTH_TOKEN` — bearer token for the broker

## Command Tree

On each invocation, `broker-cli` calls `tools/list` and builds a [cobra](https://github.com/spf13/cobra) command tree from the results before `Execute()` runs.

Tool names are dot-separated (`git.push`). The dot maps to the CLI hierarchy:

```
git.push        →  broker-cli git push
git.pull        →  broker-cli git pull
github.list_prs →  broker-cli github list-prs
```

Underscores in tool names are normalized to hyphens in CLI commands (`list_prs` → `list-prs`).

Each **namespace** (prefix before the dot) becomes an intermediate command listing the tools in that group. There is no namespace-level description from the broker — these are left generic. Each **tool** becomes a leaf command with flags derived from its JSON Schema.

## Help Messages

**Root:**
```
Usage:
  broker-cli <namespace> <command> [flags]

Available namespaces:
  git      git
  github   github

Flags:
  --no-cache      Bypass tool discovery cache
  --timeout int   Seconds to wait for approval (default: no timeout)
  -h, --help      Show help

Environment:
  MCP_BROKER_ENDPOINT    Broker URL (required)
  MCP_BROKER_AUTH_TOKEN  Bearer token (required)
```

**Namespace:**
```
Usage:
  broker-cli git <command> [flags]

Available commands:
  fetch       Fetch from a remote without merging
  pull        Pull and merge from a remote branch
  push        Push local commits to a remote

Flags:
  -h, --help  Show help
```

**Tool:**
```
Push local commits to a remote repository.

Usage:
  broker-cli git push [flags]

Flags:
  --remote string   Name of the remote to push to (required)
  --branch string   Branch to push (required)
  --force           Force push, overwriting remote history

  --param string    Set a field as raw JSON: --param 'key=value'
  --raw-input       Pass entire input as a JSON object, bypassing flags
  -h, --help        Show help
```

## Argument Mapping

JSON Schema input fields are mapped to CLI flags:

| Schema type | CLI flag |
|---|---|
| `string` | `--flag <value>` |
| `integer` / `number` | `--flag <number>` |
| `boolean` | `--flag` (presence = true) |
| `object` / `array` / complex | use `--param` or `--raw-input` |

- `required` fields from the schema are validated before the call — missing one exits with a clear error on stderr
- Field `description` from the schema becomes the flag's help text
- `--param key=<json>` sets a single field as raw JSON for complex types (arrays, objects)
- `--raw-input '<json>'` passes the entire input object as JSON, bypassing all flag parsing

## Execution Flow

1. Parse flags → build JSON input object from flag values
2. Merge any `--param` overrides
3. If `--raw-input` is provided, use it directly (bypasses steps 1-2)
4. POST `tools/call` to broker with the constructed input
5. If response is "pending approval": print `waiting for approval...` to stderr, poll every 2s until resolved
6. On denial: print denial reason to stderr, exit non-zero
7. On success: print result to stdout, exit 0

## Output Format

Output is always a JSON array on stdout. Each element is the parsed JSON value of a content block (or a raw string if the block is not valid JSON):

```bash
# Single JSON object result
[{"pushed": true, "commits": 3}]

# Multiple content blocks
[{"pr": 42, "url": "..."}, {"checks": "passing"}]

# Plain text result
["Successfully deleted branch"]
```

Errors always go to stderr as a JSON object:

```bash
{"error": "missing required flag: --branch"}         # validation error, exit 1
{"error": "denied: push requires maintainer approval"}  # approval denied, exit 1
{"error": "connection refused: broker unreachable"}   # connection error, exit 1
```

## Tool Discovery Caching

`tools/list` is called on every invocation. To avoid round-trip latency on tight agent loops, the tool list is cached to a temp file keyed by `MCP_BROKER_ENDPOINT` with a 30-second TTL. Use `--no-cache` to bypass.

## Shell Completion

`broker-cli completion bash/zsh/fish` generates a dynamic completion script that shells out to `broker-cli __complete` at tab-time (cobra's built-in dynamic completion mechanism). This works with the runtime-generated command tree.

## Module Structure

New Go module `broker-cli/` added to the monorepo under `go.work`. Follows existing conventions:

```
broker-cli/
  main.go
  cmd/
    root.go       # root command, discovery, cache
    dynamic.go    # command tree construction from tool schemas
    exec.go       # tools/call, approval polling
  client/
    client.go     # MCP HTTP client (tools/list, tools/call)
  Makefile
  CLAUDE.md
```
