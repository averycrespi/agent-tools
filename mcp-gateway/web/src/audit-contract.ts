export function parseAuditJSON(text: string): unknown {
  const value: unknown = JSON.parse(text);
  const members = (source: string): number => {
    let quoted = false;
    let escaped = false;
    let count = 0;
    for (const character of source) {
      if (escaped) escaped = false;
      else if (quoted && character === "\\") escaped = true;
      else if (character === '"') quoted = !quoted;
      else if (!quoted && character === ":") count++;
    }
    return count;
  };
  // JSON.parse validates syntax; every duplicate loses at least one member on serialization.
  if (members(text) !== members(JSON.stringify(value)))
    throw new Error("Duplicate audit JSON member");
  return value;
}

export const auditActions: Readonly<Record<string, readonly string[]>> = {
  admin_credential: ["initialize", "create", "revoke", "rotate", "reset"],
  admin_session: ["sign_in", "logout"],
  backup: ["create", "delete", "restore"],
  server: ["create", "update", "delete", "reconcile"],
  server_credential: ["replace", "disconnect", "invalidate"],
  operation: [
    "request",
    "activate",
    "reload",
    "retry",
    "refresh_catalog",
    "credential_replace",
    "disable",
    "delete",
    "disconnect_credentials",
    "schedule",
    "start",
    "finish",
    "recover",
  ],
  oauth: [
    "create",
    "prepare",
    "authorize",
    "register",
    "publish_registration",
    "invalidate_registration",
    "await_callback",
    "begin_exchange",
    "exchange",
    "refresh",
    "install",
    "finish",
    "cancel",
    "expire",
    "supersede",
    "recover",
    "revoke",
  ],
  catalog: [
    "refresh",
    "commit",
    "publish",
    "retire",
    "invalidate",
    "fence",
    "withdraw",
  ],
  keyring: [
    "stage",
    "write",
    "commit",
    "activate",
    "fence",
    "delete",
    "cleanup",
  ],
  principal: ["create", "update"],
  agent_credential: ["issue", "revoke", "invalidate"],
  grant: ["create", "update", "delete"],
  grant_request: ["approve", "reject"],
  storage: ["migrate", "recover", "verify"],
};
export const auditActors = ["operator", "system", "offline_maintenance"];
export const auditOutcomes = [
  "pending",
  "succeeded",
  "rejected",
  "failed",
  "unknown",
];
export const auditTargets = [
  "installation",
  "admin_credential",
  "backup",
  "server",
  "operation",
  "auth_flow",
  "principal",
  "agent_credential",
  "grant",
  "grant_request",
  "descriptor",
];
const reasons = [
  "configuration_invalid",
  "resource_limit",
  "connectivity",
  "tls_failed",
  "protocol_unsupported",
  "protocol_invalid",
  "authentication_rejected",
  "credential_absent",
  "keyring_absent",
  "keyring_locked",
  "keyring_interaction_required",
  "keyring_unavailable",
  "keyring_unsupported",
  "oauth_rejected",
  "oauth_expired",
  "registration_expired",
  "process_exited",
  "output_limit",
  "stop_unconfirmed",
  "catalog_invalid",
  "catalog_limit",
  "catalog_stale",
  "superseded",
  "cancelled",
  "interrupted",
  "revocation_failed",
  "revocation_unsupported",
  "cleanup_pending",
];
const problems = [
  "malformed_request",
  "invalid_json",
  "invalid_cursor",
  "invalid_idempotency_key",
  "ambiguous_credentials",
  "invalid_oauth_state",
  "authentication_required",
  "credential_domain_mismatch",
  "forbidden_origin",
  "csrf_failed",
  "not_found",
  "method_not_allowed",
  "conflict",
  "idempotency_conflict",
  "body_too_large",
  "unsupported_media_type",
  "misdirected_request",
  "resource_limit",
  "storage_unavailable",
  "keyring_unavailable",
  "shutting_down",
  "invalid_server_configuration",
  "invalid_operation",
  "namespace_unavailable",
  "operation_conflict",
  "oauth_flow_active",
  "oauth_callback_unavailable",
  "stale_cursor",
  "audit_history_replaced",
  "stale_revision",
  "precondition_required",
  "downstream_unavailable",
  "invalid_principal",
  "invalid_grant",
  "stale_grant_revision",
  "grant_precondition_required",
  "stale_principal_revision",
  "principal_precondition_required",
  "authorization_unavailable",
  "invalid_grant_request",
  "grant_request_conflict",
  "stale_grant_request_revision",
  "grant_request_precondition_required",
  "admin_rotation_conflict",
  "stale_admin_authority",
  "admin_authority_precondition_required",
];
const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
export const auditFilterKeys = [
  "actor_type",
  "credential_id",
  "category",
  "action",
  "target_type",
  "target_id",
  "outcome",
  "correlation_id",
  "from",
  "until",
];
export function auditFilterOptions(
  key: string,
  category = "",
): readonly string[] | undefined {
  switch (key) {
    case "actor_type":
      return auditActors;
    case "category":
      return Object.keys(auditActions).sort();
    case "action":
      return category === ""
        ? [...new Set(Object.values(auditActions).flat())].sort()
        : auditActions[category];
    case "target_type":
      return auditTargets;
    case "outcome":
      return auditOutcomes;
    default:
      return undefined;
  }
}
function timestamp(value: unknown): string {
  if (
    typeof value !== "string" ||
    !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{9}Z$/.test(value) ||
    value < "1970" ||
    !Number.isFinite(Date.parse(value)) ||
    new Date(value).toISOString() !== value.slice(0, 23) + "Z"
  )
    throw new Error("Invalid audit timestamp");
  return value;
}
export function validAuditQuery(
  query: Readonly<Record<string, string>>,
): boolean {
  try {
    for (const [name, value] of Object.entries(query)) {
      if (
        !name.startsWith("filter_") ||
        !auditFilterKeys.includes(name.slice(7)) ||
        value === ""
      )
        return false;
      const key = name.slice(7);
      const options = auditFilterOptions(key, query.filter_category);
      if (options !== undefined && !options.includes(value)) return false;
      if (
        ["credential_id", "target_id", "correlation_id"].includes(key) &&
        !gatewayID.test(value)
      )
        return false;
      if (key === "from" || key === "until") timestamp(value);
    }
    const from = query.filter_from;
    const until = query.filter_until;
    if ((from === undefined) !== (until === undefined)) return false;
    if (from !== undefined && until !== undefined) {
      const nanos = (value: string) =>
        BigInt(Date.parse(value.slice(0, 19) + "Z")) * 1000000n +
        BigInt(value.slice(20, 29));
      const range = nanos(until) - nanos(from);
      if (range <= 0n || range > 366n * 86400n * 1000000000n) return false;
    }
    return true;
  } catch {
    return false;
  }
}
export interface AuditCredential {
  id: string;
  fingerprint: string;
}
export interface AuditSummary {
  id: string;
  sequence: string;
  timestamp: string;
  category: string;
  action: string;
  phase: string;
  outcome: string;
  actor: { type: string; credential: AuditCredential | null };
  initiator: AuditCredential | null;
  correlation_id: string;
  target: { type: string; id: string };
}
export interface AuditEvent extends AuditSummary {
  detail: { reason: string | null; problem: string | null };
}
export interface AuditHistory {
  generation: string;
  oldest_retained: { id: string; sequence: string; timestamp: string } | null;
  pruned: boolean;
}
export interface AuditPage {
  items: AuditSummary[];
  next_cursor: string | null;
  history: AuditHistory;
}
export interface AuditItem {
  event: AuditEvent;
  history: AuditHistory;
}
function record(
  value: unknown,
  keys: readonly string[],
): Record<string, unknown> {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    Object.keys(value).sort().join(",") !== [...keys].sort().join(",")
  )
    throw new Error("Invalid audit response");
  return value as Record<string, unknown>;
}
function matching(value: unknown, pattern: RegExp): string {
  if (typeof value !== "string" || !pattern.test(value))
    throw new Error("Invalid audit response");
  return value;
}
function closed(value: unknown, options: readonly string[]): string {
  if (typeof value !== "string" || !options.includes(value))
    throw new Error("Invalid audit response");
  return value;
}
function sequence(value: unknown): string {
  const result = matching(value, /^[1-9][0-9]{0,18}$/);
  if (BigInt(result) > 9223372036854775807n)
    throw new Error("Invalid audit sequence");
  return result;
}
function credential(value: unknown): AuditCredential | null {
  if (value === null) return null;
  const item = record(value, ["id", "fingerprint"]);
  return {
    id: matching(item.id, gatewayID),
    fingerprint: matching(item.fingerprint, /^[0-9a-f]{16}$/),
  };
}
function history(value: unknown): AuditHistory {
  const item = record(value, ["generation", "oldest_retained", "pruned"]);
  if (
    typeof item.pruned !== "boolean" ||
    (item.oldest_retained === null && item.pruned)
  )
    throw new Error("Invalid audit history");
  const boundary =
    item.oldest_retained === null
      ? null
      : record(item.oldest_retained, ["id", "sequence", "timestamp"]);
  return {
    generation: matching(item.generation, /^[0-9a-f]{64}$/),
    pruned: item.pruned,
    oldest_retained:
      boundary === null
        ? null
        : {
            id: matching(boundary.id, gatewayID),
            sequence: sequence(boundary.sequence),
            timestamp: timestamp(boundary.timestamp),
          },
  };
}
function summary(value: unknown, detail = false): AuditSummary | AuditEvent {
  const item = record(value, [
    "id",
    "sequence",
    "timestamp",
    "category",
    "action",
    "phase",
    "outcome",
    "actor",
    "initiator",
    "correlation_id",
    "target",
    ...(detail ? ["detail"] : []),
  ]);
  if (new TextEncoder().encode(JSON.stringify(item)).byteLength > 2048)
    throw new Error("Audit event too large");
  const actor = record(item.actor, ["type", "credential"]);
  const target = record(item.target, ["type", "id"]);
  const type = closed(actor.type, auditActors);
  const performer = credential(actor.credential);
  const initiator = credential(item.initiator);
  if (
    (type === "operator") !== (performer !== null) ||
    (initiator !== null && type !== "system")
  )
    throw new Error("Invalid audit attribution");
  const category = closed(item.category, Object.keys(auditActions));
  const phase = closed(item.phase, ["attempt", "outcome"]);
  const outcome = closed(item.outcome, auditOutcomes);
  if ((phase === "attempt") !== (outcome === "pending"))
    throw new Error("Invalid audit outcome");
  const result: AuditSummary = {
    id: matching(item.id, gatewayID),
    sequence: sequence(item.sequence),
    timestamp: timestamp(item.timestamp),
    category,
    action: closed(item.action, auditActions[category]!),
    phase,
    outcome,
    actor: { type, credential: performer },
    initiator,
    correlation_id: matching(item.correlation_id, gatewayID),
    target: {
      type: closed(target.type, auditTargets),
      id: matching(target.id, gatewayID),
    },
  };
  if (!detail) return result;
  const fields = record(item.detail, ["reason", "problem"]);
  const reason = fields.reason === null ? null : closed(fields.reason, reasons);
  const problem =
    fields.problem === null ? null : closed(fields.problem, problems);
  if (phase === "attempt" && (reason !== null || problem !== null))
    throw new Error("Invalid audit attempt");
  return { ...result, detail: { reason, problem } };
}
export function decodeAuditPage(value: unknown): AuditPage {
  const page = record(value, ["items", "next_cursor", "history"]);
  if (!Array.isArray(page.items) || page.items.length > 100)
    throw new Error("Invalid audit page");
  const items = page.items.map((item) => summary(item));
  const next =
    page.next_cursor === null
      ? null
      : matching(page.next_cursor, /^[\x21-\x7e]{1,2048}$/);
  const boundary = history(page.history);
  if (next !== null && items.length === 0)
    throw new Error("Empty audit continuation");
  for (let i = 0; i < items.length; i++) {
    if (
      boundary.oldest_retained === null ||
      BigInt(items[i]!.sequence) < BigInt(boundary.oldest_retained.sequence) ||
      (i > 0 && BigInt(items[i]!.sequence) >= BigInt(items[i - 1]!.sequence))
    )
      throw new Error("Invalid audit ordering");
  }
  return { items, next_cursor: next, history: boundary };
}
export function decodeAuditItem(value: unknown): AuditItem {
  const item = record(value, ["event", "history"]);
  const event = summary(item.event, true) as AuditEvent;
  const boundary = history(item.history);
  if (
    boundary.oldest_retained === null ||
    BigInt(event.sequence) < BigInt(boundary.oldest_retained.sequence)
  )
    throw new Error("Invalid audit boundary");
  return { event, history: boundary };
}
