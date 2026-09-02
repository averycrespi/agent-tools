const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
type JSONRecord = Record<string, unknown>;

export type OperationKind =
  | "activate"
  | "reload"
  | "retry"
  | "refresh_catalog"
  | "credential_replace"
  | "disable"
  | "delete"
  | "disconnect_credentials";
export type ExplicitOperationKind =
  | "reload"
  | "retry"
  | "refresh_catalog"
  | "disconnect_credentials";
export type OperationState =
  | "scheduled"
  | "running"
  | "succeeded"
  | "failed"
  | "cancelled"
  | "superseded"
  | "interrupted";
export interface ServerOperationView {
  id: string;
  serverID: string;
  kind: OperationKind;
  targetDesiredRevision: string;
  targetStaticRevision: string;
  targetOAuthClientRevision: string;
  targetOAuthTokensRevision: string;
  state: OperationState;
  reason: string | null;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}
export interface OperationPage {
  items: ServerOperationView[];
  nextCursor: string | null;
}

const kinds: readonly OperationKind[] = [
  "activate",
  "reload",
  "retry",
  "refresh_catalog",
  "credential_replace",
  "disable",
  "delete",
  "disconnect_credentials",
];
const states: readonly OperationState[] = [
  "scheduled",
  "running",
  "succeeded",
  "failed",
  "cancelled",
  "superseded",
  "interrupted",
];
const reasons = new Set([
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
]);
function record(value: unknown, keys: readonly string[]): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid operation response");
  const item = value as JSONRecord;
  if (Object.keys(item).sort().join(",") !== [...keys].sort().join(","))
    throw new Error("invalid operation response");
  return item;
}
function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("invalid operation response");
  return value;
}
function nullableText(value: unknown): string | null {
  if (value !== null && typeof value !== "string")
    throw new Error("invalid operation response");
  return value;
}
function identifier(value: unknown): string {
  const result = text(value);
  if (!gatewayID.test(result)) throw new Error("invalid operation response");
  return result;
}
function closed<T extends string>(value: unknown, values: readonly T[]): T {
  const result = text(value);
  if (!values.includes(result as T))
    throw new Error("invalid operation response");
  return result as T;
}
function cursor(value: unknown): string | null {
  const result = nullableText(value);
  if (result !== null && (result.length === 0 || result.length > 512))
    throw new Error("invalid operation response");
  return result;
}
export function decodeOperation(value: unknown): ServerOperationView {
  const item = record(value, [
    "id",
    "server_id",
    "kind",
    "target_desired_revision",
    "target_credential_revisions",
    "state",
    "reason",
    "created_at",
    "started_at",
    "finished_at",
  ]);
  const revisions = record(item.target_credential_revisions, [
    "static_credential",
    "oauth_client",
    "oauth_tokens",
  ]);
  const reason = nullableText(item.reason);
  if (reason !== null && !reasons.has(reason))
    throw new Error("invalid operation response");
  return {
    id: identifier(item.id),
    serverID: identifier(item.server_id),
    kind: closed(item.kind, kinds),
    targetDesiredRevision: text(item.target_desired_revision),
    targetStaticRevision: text(revisions.static_credential),
    targetOAuthClientRevision: text(revisions.oauth_client),
    targetOAuthTokensRevision: text(revisions.oauth_tokens),
    state: closed(item.state, states),
    reason,
    createdAt: text(item.created_at),
    startedAt: nullableText(item.started_at),
    finishedAt: nullableText(item.finished_at),
  };
}
export function decodeOperationPage(value: unknown): OperationPage {
  const root = record(value, ["items", "next_cursor"]);
  if (!Array.isArray(root.items)) throw new Error("invalid operation response");
  return {
    items: root.items.map(decodeOperation),
    nextCursor: cursor(root.next_cursor),
  };
}
export async function decodeOperationMutation(
  response: Response,
): Promise<ServerOperationView> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid operation response");
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 1024 * 1024)
    throw new Error("invalid operation response");
  const root = record(JSON.parse(body) as unknown, ["operation"]);
  return decodeOperation(root.operation);
}
export function operationIsTerminal(operation: ServerOperationView): boolean {
  return operation.state !== "scheduled" && operation.state !== "running";
}
