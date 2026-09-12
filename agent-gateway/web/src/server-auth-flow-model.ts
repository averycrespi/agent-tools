const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
type JSONRecord = Record<string, unknown>;

export type AuthFlowState =
  | "preparing"
  | "awaiting_callback"
  | "exchanging"
  | "succeeded"
  | "failed"
  | "expired"
  | "cancelled"
  | "superseded"
  | "interrupted";

export type OAuthDiagnosticStage =
  | "metadata_discovery"
  | "client_registration"
  | "authorization_request"
  | "callback_validation"
  | "token_exchange"
  | "credential_installation";

export interface OAuthDiagnosticView {
  correlationID: string;
  stage: OAuthDiagnosticStage;
  reason: string;
  httpStatus: number | null;
}

export interface ServerAuthFlowView {
  id: string;
  serverID: string;
  state: AuthFlowState;
  targetDesiredRevision: string;
  registrationRevision: string;
  createdAt: string;
  expiresAt: string;
  finishedAt: string | null;
  reason: string | null;
  diagnostic: OAuthDiagnosticView | null;
}

export interface AuthFlowPage {
  items: ServerAuthFlowView[];
  nextCursor: string | null;
}

export interface AuthFlowCreation {
  flow: ServerAuthFlowView;
  authorizationURL: string;
}

const states: readonly AuthFlowState[] = [
  "preparing",
  "awaiting_callback",
  "exchanging",
  "succeeded",
  "failed",
  "expired",
  "cancelled",
  "superseded",
  "interrupted",
];
const diagnosticStages: readonly OAuthDiagnosticStage[] = [
  "metadata_discovery",
  "client_registration",
  "authorization_request",
  "callback_validation",
  "token_exchange",
  "credential_installation",
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
    throw new Error("invalid auth flow response");
  const item = value as JSONRecord;
  if (Object.keys(item).sort().join(",") !== [...keys].sort().join(","))
    throw new Error("invalid auth flow response");
  return item;
}
function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("invalid auth flow response");
  return value;
}
function identifier(value: unknown): string {
  const result = text(value);
  if (!gatewayID.test(result)) throw new Error("invalid auth flow response");
  return result;
}
function revision(value: unknown): string {
  const result = text(value);
  if (!/^(?:0|[1-9][0-9]*)$/.test(result))
    throw new Error("invalid auth flow response");
  return result;
}
function nullableReason(value: unknown): string | null {
  if (value === null) return null;
  const result = text(value);
  if (!reasons.has(result)) throw new Error("invalid auth flow response");
  return result;
}
function nullableDiagnostic(value: unknown): OAuthDiagnosticView | null {
  if (value === null) return null;
  const item = record(value, [
    "correlation_id",
    "stage",
    "reason",
    "http_status",
  ]);
  const stage = text(item.stage);
  const reason = nullableReason(item.reason);
  const httpStatus = item.http_status;
  if (
    !diagnosticStages.includes(stage as OAuthDiagnosticStage) ||
    reason === null ||
    (httpStatus !== null &&
      (!Number.isInteger(httpStatus) ||
        typeof httpStatus !== "number" ||
        httpStatus < 100 ||
        httpStatus > 599))
  )
    throw new Error("invalid auth flow response");
  return {
    correlationID: identifier(item.correlation_id),
    stage: stage as OAuthDiagnosticStage,
    reason,
    httpStatus,
  };
}

function nullableText(value: unknown): string | null {
  if (value !== null && typeof value !== "string")
    throw new Error("invalid auth flow response");
  return value;
}
function cursor(value: unknown): string | null {
  const result = nullableText(value);
  if (result !== null && (result.length === 0 || result.length > 4096))
    throw new Error("invalid auth flow response");
  return result;
}
function closedState(value: unknown): AuthFlowState {
  const result = text(value);
  if (!states.includes(result as AuthFlowState))
    throw new Error("invalid auth flow response");
  return result as AuthFlowState;
}

export function decodeAuthFlow(value: unknown): ServerAuthFlowView {
  const item = record(value, [
    "id",
    "server_id",
    "flow_state",
    "target_desired_revision",
    "registration_revision",
    "created_at",
    "expires_at",
    "finished_at",
    "reason",
    "diagnostic",
  ]);
  return {
    id: identifier(item.id),
    serverID: identifier(item.server_id),
    state: closedState(item.flow_state),
    targetDesiredRevision: revision(item.target_desired_revision),
    registrationRevision: revision(item.registration_revision),
    createdAt: text(item.created_at),
    expiresAt: text(item.expires_at),
    finishedAt: nullableText(item.finished_at),
    reason: nullableReason(item.reason),
    diagnostic: nullableDiagnostic(item.diagnostic),
  };
}

export function decodeAuthFlowPage(value: unknown): AuthFlowPage {
  const page = record(value, ["items", "next_cursor"]);
  if (!Array.isArray(page.items)) throw new Error("invalid auth flow response");
  return {
    items: page.items.map(decodeAuthFlow),
    nextCursor: cursor(page.next_cursor),
  };
}

export async function decodeAuthFlowCreation(
  response: Response,
): Promise<AuthFlowCreation> {
  const body = new Uint8Array(await response.arrayBuffer());
  if (body.byteLength > 4 * 1024 * 1024)
    throw new Error("auth flow response too large");
  const creation = record(
    JSON.parse(
      new TextDecoder("utf-8", { fatal: true }).decode(body),
    ) as unknown,
    ["flow", "authorization_url"],
  );
  const authorizationURL = text(creation.authorization_url);
  if (authorizationURL.length === 0 || authorizationURL.length > 8192)
    throw new Error("invalid auth flow response");
  return { flow: decodeAuthFlow(creation.flow), authorizationURL };
}

export function authFlowIsTerminal(flow: ServerAuthFlowView): boolean {
  return !["preparing", "awaiting_callback", "exchanging"].includes(flow.state);
}

export function authFlowCanCancel(flow: ServerAuthFlowView): boolean {
  return flow.state === "preparing" || flow.state === "awaiting_callback";
}
