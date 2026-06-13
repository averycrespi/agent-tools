# pi-orchestrator

`po` is a local workflow layer above `pi-dispatcher` (`pd`). V1 runs explicit workflow definitions from a configured workflow directory, validates typed inputs, creates durable workflow runs, delegates each step to `pd`, validates required artifacts, and provides `pd`-aligned inspection and control commands.

## Status

Initial implementation is in progress. The first supported surface is workflow definition loading and validation for `po list`, `po show`, and `po lint`.
