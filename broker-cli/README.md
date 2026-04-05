# broker-cli

CLI frontend for the MCP broker. Connects to the broker, discovers available tools, and exposes them as subcommands — one per tool, grouped by namespace.

## Usage

```bash
export MCP_BROKER_ENDPOINT=http://localhost:8200
export MCP_BROKER_AUTH_TOKEN=<token>

broker-cli <namespace> <command> [flags]
```

## Examples

```bash
# List available namespaces and commands
broker-cli --help
broker-cli git --help

# Call a tool
broker-cli git push --remote origin --branch main

# Complex inputs via --raw-field or --raw-input
broker-cli github search-code --query "foo" --raw-field 'include_patterns=["*.go"]'
broker-cli github create-pr --raw-input '{"title":"Fix bug","body":"..."}'
```

## Output

All output is a JSON array on stdout. Errors are a JSON object on stderr.

```bash
[{"pushed": true, "commits": 3}]     # stdout on success
{"error": "missing required flag: --branch"}  # stderr on error, exit 1
```

## Flags

| Flag | Description |
|---|---|
| `--no-cache` | Bypass tool discovery cache |
| `--timeout <seconds>` | Timeout in seconds (default: no timeout) |
| `--raw-field key=<json>` | Set a field as raw JSON (per tool command) |
| `--raw-input <json>` | Pass entire input as JSON, bypassing flags (per tool command) |

## Environment

| Variable | Description |
|---|---|
| `MCP_BROKER_ENDPOINT` | Broker URL, e.g. `http://localhost:8200` (required) |
| `MCP_BROKER_AUTH_TOKEN` | Bearer token (required) |
