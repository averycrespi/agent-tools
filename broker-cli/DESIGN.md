# broker-cli Design

## Motivation

Some AI agents speak MCP natively — but others work better with a CLI. `broker-cli` is an alternative frontend for the MCP broker, for agents that prefer to interact via shell commands instead of connecting as an MCP client.

Rather than maintaining a separate static CLI per tool, `broker-cli` discovers the broker's tools at runtime and builds its command tree dynamically. This means it stays in sync with the broker automatically: as tools are added or removed from the broker, the CLI reflects those changes without any code changes.

## Architecture

CLI-only binary. On startup, connects to the MCP broker's HTTP endpoint, discovers available tools via `tools/list`, and builds a cobra command tree before `Execute()` runs.

```
Agent ──shell──> broker-cli ──HTTP──> mcp-broker ──stdio──> backend MCP servers
```

### Startup flow

```
main()
  ├── rootCmd.ParseFlags(os.Args[1:])   # parse global flags before discovery
  ├── buildTree()                        # discover tools, build cobra tree
  │     ├── check cache ($TMPDIR, 30s TTL)
  │     ├── fallback: client.ListTools() via HTTP
  │     └── tree.Build(rootCmd, tools, exec)
  └── rootCmd.Execute()                 # cobra dispatches to tool command
```

Global flags must be parsed before `buildTree()` so that `--no-cache` is available during tool discovery.

### Command tree structure

Tool names like `git.push` map to `broker-cli git push`. The dot separates namespace from tool name; underscores in tool names normalize to hyphens.

```
broker-cli
├── git                  # namespace group ("git tools")
│   ├── git-push         # tool command
│   └── git-list-remotes
└── github
    ├── gh-create-pr
    └── gh-search-repos
```

Each tool command gets typed flags generated from the tool's JSON Schema (`string`, `boolean`, `integer`/`number`). Complex types (arrays, objects) not representable as scalar flags are handled via `--raw-field`. `--raw-input` bypasses flag parsing entirely.

### Tool discovery cache

The tool list is cached in `$TMPDIR/broker-cli-tools-<hash>.json` with a 30-second TTL. The hash is the first 8 bytes of SHA-256 over the endpoint URL, so different broker endpoints get separate cache files. Use `--no-cache` to bypass.

## Project Structure

```
broker-cli/
├── cmd/broker-cli/
│   ├── main.go          # Entry point: ParseFlags → buildTree → Execute
│   └── root.go          # Global flags, buildTree(), callTool(), writeError()
├── internal/
│   ├── cache/           # File-based tool list cache (SHA-256 keyed, 30s TTL)
│   ├── client/          # MCP HTTP client (mcp-go StreamableHttpClient + bearer auth)
│   ├── flags/           # JSON Schema → cobra flags mapper
│   ├── output/          # MCP content blocks → JSON array formatter
│   └── tree/            # Dynamic cobra command tree builder
├── Makefile
├── CLAUDE.md
├── DESIGN.md
└── README.md
```

## Flags Package

The `internal/flags` package maps a JSON Schema `properties` object to cobra flags:

| Schema type | Cobra flag type |
|-------------|----------------|
| `string` | `String` |
| `boolean` | `Bool` |
| `integer`, `number` | `Int64` |
| `array`, `object`, other | not registered; use `--raw-field` |

Flag names are normalized from snake_case to kebab-case (`repo_path` → `--repo-path`). The args map passed to the broker uses the original schema key (`repo_path`) since that's what the backend MCP server expects.

Required fields (from schema `required` array) get `(required)` appended to their flag description. If required flags are missing, `BuildArgs` returns an error listing all missing flags with their descriptions in a single message.

`--raw-field key=<json>` overrides individual fields with raw JSON values. `--raw-input <json>` passes the entire input as a JSON object, bypassing all flag parsing and required-field validation.

## Output Format

All output is a JSON array on stdout. Each MCP text content block is attempted as JSON; if it parses, the parsed value is included as-is. If not, the raw string is included. Non-text content blocks are skipped.

```
[{"pushed": true, "commits": 3}]       # stdout on success
{"error": "missing required flags..."}  # stderr on error, exit 1
```

Errors are a JSON object on stderr. Cobra's built-in error printing is silenced (`SilenceErrors: true`) so errors only appear once.

## Tech Stack

| Component | Library |
|-----------|---------|
| MCP protocol | [mcp-go](https://github.com/mark3labs/mcp-go) v0.45.0 |
| CLI | [cobra](https://github.com/spf13/cobra) |
| Testing | [testify](https://github.com/stretchr/testify) |

## Design Decisions

**Runtime discovery, not codegen.** The command tree is built at process startup from live tool metadata. This keeps broker-cli in sync with the broker without any build step or regeneration.

**Parse global flags before discovery.** `rootCmd.ParseFlags(os.Args[1:])` runs before `buildTree()` so that `--no-cache` is available during the discovery phase. Without this, the flag wouldn't be set in time.

**Tool list cache.** Discovering tools requires an HTTP round-trip. With a 30-second cache, repeated invocations (e.g., an agent calling multiple tools in sequence) only pay the discovery cost once. The TTL is short enough that tool list changes are picked up quickly.

**Kebab-case flags, snake_case args.** CLI convention is kebab-case (`--repo-path`). MCP tool schemas use snake_case (`repo_path`). Normalizing at flag registration time keeps the CLI idiomatic while passing the original key to the broker.

**`--raw-field` and `--raw-input` as per-command flags.** These are tool invocation concerns, not broker connection concerns. Keeping them on individual tool commands (rather than promoting to global flags) maintains a clean conceptual boundary: global flags control how to connect to the broker; per-command flags control how to call a specific tool.

**No timeout flag.** Shell's `timeout` command handles this more composably. A built-in flag would add complexity without adding capability.

**JSON array output.** Structured output makes broker-cli easy to pipe into `jq` or consume programmatically. A consistent envelope (`[]`) means callers always parse an array, regardless of how many content blocks the tool returned.
