export const overviewLimitNames = [
  "http_regular",
  "http_control_auth",
  "http_admin",
  "http_health",
  "mcp_work",
  "mcp_streams",
  "admin_sessions",
  "legacy_sessions",
  "event_streams",
  "backup_work",
  "backup_records",
  "admin_credentials",
  "idempotency_records",
  "keyring_candidates",
  "keyring_work",
  "database_bytes",
  "server_identities",
  "servers",
  "downstream_runtimes",
  "server_reconciliations",
  "catalog_traversals",
  "oauth_flows",
  "oauth_callback_work",
  "s2_idempotency_records",
  "active_tools",
  "durable_tool_identities",
  "downstream_dispatch",
  "principals",
  "grants",
  "grant_requests",
  "grant_request_evidence_bytes",
] as const;

export function overviewStatusFixture() {
  const limits = Object.fromEntries(
    overviewLimitNames.map((name) => [
      name,
      { in_use: 0, limit: 64, saturated: false },
    ]),
  ) as Record<string, { in_use: number; limit: number; saturated: boolean }>;
  limits.servers = { in_use: 64, limit: 64, saturated: true };
  limits.database_bytes = {
    in_use: 858993460,
    limit: 1073741824,
    saturated: false,
  };
  return {
    process: {
      state: "storage_failed",
      ready: false,
      started_at: "2026-08-28T00:00:00Z",
    },
    sqlite: {
      state: "latched",
      schema_version: "10",
      revision: "7",
      latched: true,
    },
    keyring: { capability: "unavailable" },
    limits,
    backup: { state: "idle", last_completed_at: null },
    protocols: {
      modern: "2026-07-28",
      legacy: "2025-11-25",
      agent_auth: "principal_credentials",
    },
  };
}

export function overviewServer(id: string, name: string, state: string) {
  return {
    id,
    namespace: `namespace-${id.slice(-4)}`,
    display_name: name,
    desired_state: "enabled",
    desired_revision: "1",
    transport: {
      kind: "stdio",
      executable: "/usr/bin/true",
      arguments: [],
      working_directory: "/tmp",
      environment: {},
      secret_environment: {},
    },
    credential_revisions: {
      static_credential: "0",
      oauth_client: "0",
      oauth_tokens: "0",
    },
    credential_state: "not_required",
    runtime: {
      state,
      reason: state === "active" ? null : "transport_failure",
      runtime_id: state === "active" ? "runtime-1" : null,
      reconciliation: { in_use: 0, limit: 1, saturated: false },
      dispatch: { in_use: 0, limit: 4, saturated: false },
    },
    catalog: {
      durable_state: state === "active" ? "current" : "stale",
      active_state: state === "active" ? "current" : "stale",
      durable_revision: "1",
      active_revision: state === "active" ? "1" : null,
      durable_tool_count: 1,
      active_tool_count: state === "active" ? 1 : 0,
      last_success_at: "2026-08-28T00:00:00Z",
      traversal: { in_use: 0, limit: 4, saturated: false },
    },
    created_at: "2026-08-28T00:00:00Z",
    updated_at: "2026-08-28T00:00:00Z",
    deleted_at: null,
  };
}

export function overviewRequestFixture() {
  return {
    id: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    principal_id: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
    state: "pending",
    revision: "1",
    requested_policy: {
      scope: "server",
      target: "long-server",
      constraint: null,
      duration_seconds: null,
      future_tools_acknowledged: true,
    },
    approved_policy: null,
    approved_grant_id: null,
    rejection_reason: null,
    created_at: "2026-08-28T00:00:00Z",
    updated_at: "2026-08-28T00:00:00Z",
    closed_at: null,
  };
}

export function overviewInvocationFixture() {
  return {
    id: "01ARZ3NDEKTSV4RRFFQ69G5FAX",
    principal_id: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
    credential_id: "01ARZ3NDEKTSV4RRFFQ69G5FAY",
    credential_fingerprint: "0123456789abcdef",
    credential_revision: "1",
    admitted_at: "2026-08-28T00:00:00Z",
    admission_class: "evaluated",
    requested_name: `literal-<script>${"L".repeat(96)}`,
    target: {
      kind: "downstream",
      server_id: "01ARZ3NDEKTSV4RRFFQ69G5FA0",
      tool_id: "01ARZ3NDEKTSV4RRFFQ69G5FA3",
      upstream_name: "long-tool",
      descriptor_revision: "1",
      descriptor_fingerprint: "0123456789abcdef",
    },
    authorization: {
      decision: "allow",
      revision: "2",
      evaluated_at: "2026-08-28T00:00:01Z",
      grant_id: "01ARZ3NDEKTSV4RRFFQ69G5FA4",
    },
    outcome: {
      class: "outcome_unknown",
      basis: "missing_terminal",
      completed_at: null,
    },
  };
}

export const invocationIDs = {
  principal: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  server: "01ARZ3NDEKTSV4RRFFQ69G5FA0",
  credential: "01ARZ3NDEKTSV4RRFFQ69G5FAY",
  tool: "01ARZ3NDEKTSV4RRFFQ69G5FA3",
  grant: "01ARZ3NDEKTSV4RRFFQ69G5FA4",
  admission: "01ARZ3NDEKTSV4RRFFQ69G5FA5",
  policy: "01ARZ3NDEKTSV4RRFFQ69G5FA6",
  terminal: "01ARZ3NDEKTSV4RRFFQ69G5FA7",
  explicitUnknown: "01ARZ3NDEKTSV4RRFFQ69G5FA8",
  missing: "01ARZ3NDEKTSV4RRFFQ69G5FA9",
  stale: "01ARZ3NDEKTSV4RRFFQ69G5FAA",
} as const;

export function invocationTarget(kind: "downstream" | "gateway") {
  return {
    kind,
    server_id:
      kind === "gateway" ? "00000000000000000000000000" : invocationIDs.server,
    tool_id: invocationIDs.tool,
    upstream_name: kind === "gateway" ? "get_identity" : "allowed",
    descriptor_revision: "7",
    descriptor_fingerprint: "d".repeat(64),
  };
}

export function invocationAuthorization(decision: "allow" | "deny") {
  return {
    decision,
    revision: "9",
    evaluated_at: "2026-08-28T12:00:01Z",
    grant_id: decision === "allow" ? invocationIDs.grant : null,
  };
}

export function invocationFixture(
  id: string,
  basis: "admission" | "policy" | "terminal" | "missing_terminal",
  outcome: string,
  kind: "downstream" | "gateway" | null = null,
) {
  const evaluated = basis !== "admission";
  return {
    id,
    principal_id: invocationIDs.principal,
    credential_id: invocationIDs.credential,
    credential_fingerprint: "0123456789abcdef",
    credential_revision: "3",
    admitted_at: "2026-08-28T12:00:00Z",
    admission_class: evaluated ? "evaluated" : "invalid_params",
    requested_name: evaluated
      ? kind === "gateway"
        ? "mcp_gateway.get_identity"
        : "namespace.allowed"
      : null,
    target: kind === null ? null : invocationTarget(kind),
    authorization: !evaluated
      ? null
      : invocationAuthorization(basis === "policy" ? "deny" : "allow"),
    outcome: {
      class: outcome,
      basis,
      completed_at: basis === "terminal" ? "2026-08-28T12:00:02Z" : null,
    },
  };
}

export const serverReadIDs = {
  active: "01ARZ3NDEKTSV4RRFFQ69G5FB0",
  degraded: "01ARZ3NDEKTSV4RRFFQ69G5FB1",
  deleted: "01ARZ3NDEKTSV4RRFFQ69G5FB2",
  discarded: "01ARZ3NDEKTSV4RRFFQ69G5FB3",
  currentTool: "01ARZ3NDEKTSV4RRFFQ69G5FC0",
  retiredTool: "01ARZ3NDEKTSV4RRFFQ69G5FC1",
  durableTool: "01ARZ3NDEKTSV4RRFFQ69G5FC2",
  activeTool: "01ARZ3NDEKTSV4RRFFQ69G5FC3",
} as const;

export function serverReadFixture(
  id: string,
  options: {
    name: string;
    desired: "enabled" | "disabled" | "deleted";
    runtime: "active" | "degraded" | "authentication_required" | "deleted";
    credential:
      | "ready"
      | "reauthentication_required"
      | "not_required"
      | "cleanup_pending";
    durable: "current" | "stale" | "retired";
    active: "current" | "stale" | "unavailable" | "absent";
  },
) {
  return {
    id,
    namespace: `server-${id.slice(-2).toLowerCase()}`,
    display_name: options.name,
    desired_state: options.desired,
    desired_revision: options.desired === "deleted" ? "8" : "7",
    transport:
      options.desired === "deleted"
        ? null
        : {
            kind: "stdio",
            executable: "/usr/bin/example",
            arguments: ["--safe"],
            working_directory: "/srv/example",
            environment: { MODE: "read" },
            secret_environment: { TOKEN: "primary" },
          },
    credential_revisions: {
      static_credential: "2",
      oauth_client: "3",
      oauth_tokens: "4",
    },
    credential_state: options.credential,
    runtime: {
      state: options.runtime,
      reason:
        options.runtime === "authentication_required"
          ? "authentication_rejected"
          : options.runtime === "degraded"
            ? "catalog_stale"
            : null,
      runtime_id: options.runtime === "active" ? "runtime-safe-id" : null,
      reconciliation: { in_use: 0, limit: 1, saturated: false },
      dispatch: { in_use: 0, limit: 4, saturated: false },
    },
    catalog: {
      durable_state: options.durable,
      active_state: options.active,
      durable_revision: options.durable === "retired" ? "6" : "7",
      active_revision: options.active === "current" ? "7" : null,
      durable_tool_count: 2,
      active_tool_count: options.active === "absent" ? 0 : 1,
      last_success_at: "2026-08-28T12:00:00Z",
      traversal: { in_use: 0, limit: 4, saturated: false },
    },
    created_at: "2026-08-28T10:00:00Z",
    updated_at: "2026-08-28T12:00:00Z",
    deleted_at: options.desired === "deleted" ? "2026-08-28T12:30:00Z" : null,
  };
}

export function descriptorReadFixture(
  id: string,
  serverID: string,
  name: string,
  retired: boolean,
) {
  return {
    id,
    server_id: serverID,
    upstream_name: name,
    external_name: `server.${name}`,
    descriptor: {
      name,
      title: `Safe ${name}`,
      description: `Descriptor ${name}`,
      inputSchema: {
        type: "object",
        properties: {
          value: { type: "string" },
          tags: {
            type: "array",
            items: {
              type: "object",
              properties: { label: { type: "string" } },
            },
          },
        },
      },
      outputSchema: { type: "object" },
      annotations: {
        title: null,
        readOnlyHint: true,
        destructiveHint: false,
        idempotentHint: true,
        openWorldHint: false,
      },
    },
    fingerprint: "a".repeat(64),
    catalog_revision: retired ? "6" : "7",
    first_seen_at: "2026-08-28T10:00:00Z",
    last_seen_at: "2026-08-28T12:00:00Z",
    retired_at: retired ? "2026-08-28T12:30:00Z" : null,
  };
}
