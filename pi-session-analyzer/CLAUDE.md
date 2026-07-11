# pi-session-analyzer

## Development

```bash
make build
make test
make audit
```

Keep command code thin. Package flow is `cmd -> mcp/detect -> store -> ingest/scrub`. Scrub every free-text or JSON value in the store write boundary. Never commit real Pi session records; tests use synthetic fixtures only. Report output, reasoning, and cache-read usage separately rather than treating `totalTokens` as generated work.
