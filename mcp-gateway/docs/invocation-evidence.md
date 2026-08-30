# Invocation evidence and unknown outcomes

Audience: Operators investigating governed tool calls

Purpose: Interpret invocation evidence, redaction, and unknown outcomes.

This guide owns read-only invocation inspection, redacted evidence, terminal outcomes, transport certainty, and the operator response to unknown outcomes. Generated `mcp-gateway invocation --help` owns command syntax.

## Guide boundary

- See [DESIGN](../DESIGN.md) for normative admission, execution, audit-retention, and failure semantics.
- See [Access policy](access-policy.md) for principals, grants, and authorization decisions.
- See [CLI and local administration](cli-local-administration.md) for shared pagination and output behavior.

This guide explains operator interpretation without promising replay, exactly-once effects, or certainty that the evidence does not provide.
