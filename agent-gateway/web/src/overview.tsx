import type { ComponentChildren } from "preact";
import { decodeGitRoutingResponse } from "./git-routing-contract";
import {
  activityWindows,
  activityHref,
  decodeProtocolActivity,
  decodeInventoryTotal,
  type ActivityWindow,
  type Protocol,
  type ProtocolActivity,
} from "./protocol-summary";
import {
  decodeHistoryHealth,
  decodeTrafficRecovery,
  trafficRecoveryKeys,
  type TrafficRecovery,
  historyHealthKeys,
  decodeDiagnosticHealth,
  decodeObservations,
  type HistoryHealth,
  type DiagnosticHealth,
  type Observations,
} from "./observation-health";
import { decodeDiagnosticCorrelation } from "./diagnostic-correlation";
import { decodeReadOnly, readOnlyKeys } from "./read-only";
import { useEffect, useState } from "preact/hooks";
import { type PrincipalDirectory } from "./principals";
import {
  sentenceCase,
  StateNotice,
  StatusLabel,
  FactStatus,
} from "./primitives";
import type { SessionClient } from "./session";

import { capacityState } from "./resource-utilization";
import { formatUserTime, UserTime } from "./time";
import type {
  PanelSnapshot,
  ViewCoordinator,
  ViewReadContext,
  ViewSnapshot,
} from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
export const limitNames = [
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
  "server_idempotency_records",
  "active_tools",
  "durable_tool_identities",
  "downstream_dispatch",
  "principals",
  "grants",
  "grant_requests",
  "grant_request_evidence_bytes",
] as const;

export type LimitName = (typeof limitNames)[number];
export interface LimitView {
  name: LimitName;
  inUse: number;
  limit: number;
  saturated: boolean;
}
export interface TrafficView {
  recovery?: TrafficRecovery | undefined;
  health?: HistoryHealth | undefined;
  state: string;
  ready: boolean;
  faulted: boolean;
  pressure: boolean;
  budgetBytes: number;
  databaseBytes: number;
  walBytes: number;
  quotaRefusals: number;
  prunedRecords: number;
  generation: string;
}
export interface HTTPProxyView {
  enabled: boolean;
  ready: boolean;
  caReady: boolean;
  authority?: string;
  connections: Omit<LimitView, "name">;
  work: Omit<LimitView, "name">;
  activeStreams: number;
  activeTunnels: number;
}
export interface StatusView {
  diagnostics?: DiagnosticHealth | undefined;
  observations?: Observations | undefined;
  endpoints?: { authority: string; api: string; mcp: string };
  httpProxy?: HTTPProxyView;
  traffic?: TrafficView;
  processState: string;
  ready: boolean;
  startedAt: string;
  sqliteState: string;
  schemaVersion: string;
  revision: string;
  latched: boolean;
  keyring: string;
  limits: LimitView[];
  backupState: string;
  lastBackupAt: string | null;
  modernProtocol: string;
  legacyProtocol: string;
  agentAuth: string;
}
interface ServerView {
  id: string;
  name: string;
  desired: string;
  runtime: string;
  credential: string;
  catalog: string;
  activeToolCount: number;
  attention: boolean;
  saturated: boolean;
}
interface ServerSummary {
  items: ServerView[];
  complete: boolean;
  restarted: boolean;
}
interface RequestView {
  id: string;
  principalID: string;
  principalName: string;
  serverName: string;
  target: string;
  createdAt: string;
}
interface RequestSummary {
  items: RequestView[];
  total: number;
  complete: boolean;
}
export interface OverviewSnapshot {
  window?: ActivityWindow;
  activity?: ProtocolActivity;
  inventory?: Partial<Record<string, number>>;
  status?: StatusView;
  servers?: ServerSummary;
  requests?: RequestSummary;
}

type Listener = (snapshot: OverviewSnapshot) => void;
type JSONRecord = Record<string, unknown>;

function record(value: unknown, keys: readonly string[]): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  const candidate = value as JSONRecord;
  if (Object.keys(candidate).sort().join(",") !== [...keys].sort().join(","))
    throw new Error("invalid response");
  return candidate;
}
function stringValue(value: unknown): string {
  if (typeof value !== "string") throw new Error("invalid response");
  return value;
}
function nullableString(value: unknown): string | null {
  if (value !== null && typeof value !== "string")
    throw new Error("invalid response");
  return value;
}
function booleanValue(value: unknown): boolean {
  if (typeof value !== "boolean") throw new Error("invalid response");
  return value;
}
function integer(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0)
    throw new Error("invalid response");
  return value;
}
function identifier(value: unknown): string {
  const id = stringValue(value);
  if (!gatewayID.test(id)) throw new Error("invalid response");
  return id;
}
function array(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("invalid response");
  return value;
}
function cursor(value: unknown): string | null {
  const result = nullableString(value);
  if (result !== null && (result.length === 0 || result.length > 4096))
    throw new Error("invalid response");
  return result;
}
function closed(value: unknown, values: readonly string[]): string {
  const result = stringValue(value);
  if (!values.includes(result)) throw new Error("invalid response");
  return result;
}

function limit(value: unknown, name: LimitName): LimitView {
  const item = record(value, ["in_use", "limit", "saturated"]);
  return {
    name,
    inUse: integer(item.in_use),
    limit: integer(item.limit),
    saturated: booleanValue(item.saturated),
  };
}
export function decodeStatus(value: unknown): StatusView {
  const hasTraffic =
    value !== null && typeof value === "object" && "traffic" in value;
  const hasProxy =
    value !== null && typeof value === "object" && "http_proxy" in value;
  const hasEndpoints =
    value !== null && typeof value === "object" && "endpoints" in value;
  const additions =
    value !== null && typeof value === "object"
      ? ["diagnostics", "observations"].filter((k) => k in value)
      : [];
  const root = record(value, [
    ...additions,
    ...(hasEndpoints ? ["endpoints"] : []),
    ...(hasProxy ? ["http_proxy"] : []),
    ...(hasTraffic ? ["traffic"] : []),
    "process",
    "sqlite",
    "keyring",
    "limits",
    "backup",
    "protocols",
  ]);
  let endpoints: StatusView["endpoints"];
  if (hasEndpoints) {
    const item = record(root.endpoints, ["authority", "api", "mcp"]);
    endpoints = {
      authority: stringValue(item.authority),
      api: closed(item.api, ["starting", "ready", "read_only", "draining"]),
      mcp: closed(item.mcp, [
        "starting",
        "ready",
        "disabled",
        "unavailable",
        "draining",
      ]),
    };
  }
  let traffic: TrafficView | undefined;
  if (hasTraffic) {
    const healthKeys =
      root.traffic &&
      typeof root.traffic === "object" &&
      "delivery" in root.traffic
        ? historyHealthKeys
        : [];
    const recoveryKeys =
      root.traffic &&
      typeof root.traffic === "object" &&
      "health" in root.traffic
        ? trafficRecoveryKeys
        : [];
    const item = record(root.traffic, [
      ...recoveryKeys,
      ...healthKeys,
      "state",
      "ready",
      "faulted",
      "pressure",
      "budget_bytes",
      "database_bytes",
      "wal_bytes",
      "quota_refusals",
      "pruned_records",
      "generation",
      "rolling_history",
      "unknown_completion_possible",
    ]);
    if (
      !booleanValue(item.rolling_history) ||
      !booleanValue(item.unknown_completion_possible)
    )
      throw new Error("invalid traffic history semantics");
    traffic = {
      health: decodeHistoryHealth(item),
      recovery: decodeTrafficRecovery(item),
      state: closed(item.state, [
        "opening",
        "ready",
        "unavailable",
        "faulted",
        "disabled",
      ]),
      ready: booleanValue(item.ready),
      faulted: booleanValue(item.faulted),
      pressure: booleanValue(item.pressure),
      budgetBytes: integer(item.budget_bytes),
      databaseBytes: integer(item.database_bytes),
      walBytes: integer(item.wal_bytes),
      quotaRefusals: integer(item.quota_refusals),
      prunedRecords: integer(item.pruned_records),
      generation: stringValue(item.generation),
    };
  }
  let httpProxy: HTTPProxyView | undefined;
  if (hasProxy) {
    const raw = root.http_proxy;
    const hasAuthority =
      raw !== null && typeof raw === "object" && "authority" in raw;
    const item = record(raw, [
      "enabled",
      "ready",
      "ca_ready",
      "connections",
      "work",
      "active_streams",
      "active_tunnels",
      ...(hasAuthority ? ["authority"] : []),
    ]);
    httpProxy = {
      enabled: booleanValue(item.enabled),
      ready: booleanValue(item.ready),
      caReady: booleanValue(item.ca_ready),
      ...(hasAuthority ? { authority: stringValue(item.authority) } : {}),
      connections: limit(item.connections, "http_regular"),
      work: limit(item.work, "http_regular"),
      activeStreams: integer(item.active_streams),
      activeTunnels: integer(item.active_tunnels),
    };
  }
  const process = record(root.process, ["state", "ready", "started_at"]);
  const sqlite = record(root.sqlite, [
    "state",
    "schema_version",
    "revision",
    "latched",
  ]);
  const keyring = record(root.keyring, ["capability"]);
  const limits = record(root.limits, limitNames);
  const backup = record(root.backup, ["state", "last_completed_at"]);
  const protocols = record(root.protocols, ["modern", "legacy", "agent_auth"]);
  return {
    diagnostics: decodeDiagnosticHealth(root.diagnostics),
    observations: decodeObservations(root.observations),
    ...(endpoints ? { endpoints } : {}),
    ...(traffic ? { traffic } : {}),
    ...(httpProxy ? { httpProxy } : {}),
    processState: closed(process.state, [
      "uninitialized",
      "starting",
      "ready",
      "storage_failed",
      "draining",
    ]),
    ready: booleanValue(process.ready),
    startedAt: stringValue(process.started_at),
    sqliteState: closed(sqlite.state, ["uninitialized", "ready", "latched"]),
    schemaVersion: stringValue(sqlite.schema_version),
    revision: stringValue(sqlite.revision),
    latched: booleanValue(sqlite.latched),
    keyring: closed(keyring.capability, [
      "ready",
      "absent",
      "locked",
      "interaction_required",
      "unavailable",
      "unsupported",
    ]),
    limits: limitNames.map((name) => limit(limits[name], name)),
    backupState: closed(backup.state, ["idle", "creating", "unavailable"]),
    lastBackupAt: nullableString(backup.last_completed_at),
    modernProtocol: stringValue(protocols.modern),
    legacyProtocol: stringValue(protocols.legacy),
    agentAuth: closed(protocols.agent_auth, [
      "deny_all",
      "principal_credentials",
    ]),
  };
}

function validateLimit(value: unknown): LimitView {
  return limit(value, "http_regular");
}
function validateTransport(value: unknown): void {
  const base = record(value, Object.keys(value as object));
  const kind = closed(base.kind, ["stdio", "streamable_http"]);
  if (kind === "stdio") {
    const item = record(value, [
      "kind",
      "executable",
      "arguments",
      "working_directory",
      "environment",
      "secret_environment",
    ]);
    stringValue(item.executable);
    stringValue(item.working_directory);
    array(item.arguments).forEach(stringValue);
    for (const objectValue of [item.environment, item.secret_environment]) {
      const values = record(objectValue, Object.keys(objectValue as object));
      Object.values(values).forEach(stringValue);
    }
    return;
  }
  const item = record(value, [
    "kind",
    "url",
    "protocol_mode",
    "authentication",
    ...(Object.hasOwn(base, "headers") ? ["headers"] : []),
  ]);
  if (item.headers !== undefined) {
    const headers = item.headers;
    if (
      headers === null ||
      typeof headers !== "object" ||
      Array.isArray(headers)
    )
      throw new Error("invalid HTTP headers");
    const values = record(headers, Object.keys(headers));
    Object.values(values).forEach(stringValue);
  }
  stringValue(item.url);
  closed(item.protocol_mode, ["modern", "legacy", "auto"]);
  const authentication = record(
    item.authentication,
    Object.keys(item.authentication as object),
  );
  const mode = closed(authentication.mode, ["none", "bearer", "oauth"]);
  if (mode === "none" || mode === "bearer") {
    record(item.authentication, ["mode"]);
    return;
  }
  const oauth = record(item.authentication, [
    "mode",
    "registration",
    "trusted_origins",
    "request_offline_access",
    ...["callback_uri", "auth_server_metadata_url", "scopes"].filter((key) =>
      Object.hasOwn(authentication, key),
    ),
  ]);
  if (oauth.callback_uri !== undefined) stringValue(oauth.callback_uri);
  if (oauth.auth_server_metadata_url !== undefined)
    stringValue(oauth.auth_server_metadata_url);
  if (oauth.scopes !== undefined) array(oauth.scopes).forEach(stringValue);
  array(oauth.trusted_origins).forEach(stringValue);
  booleanValue(oauth.request_offline_access);
  const registration = record(
    oauth.registration,
    Object.keys(oauth.registration as object),
  );
  const registrationMode = closed(registration.mode, ["static", "dynamic"]);
  if (registrationMode === "dynamic") {
    const dynamic = record(oauth.registration, ["mode", "issuer"]);
    nullableString(dynamic.issuer);
  } else {
    const fixed = record(oauth.registration, [
      "mode",
      "issuer",
      "client_id",
      "token_endpoint_auth_method",
    ]);
    nullableString(fixed.issuer);
    stringValue(fixed.client_id);
    stringValue(fixed.token_endpoint_auth_method);
  }
}
function decodeServer(value: unknown): ServerView {
  const item = record(value, [
    "id",
    "namespace",
    "display_name",
    "desired_state",
    "desired_revision",
    "transport",
    "credential_revisions",
    "credential_state",
    "runtime",
    "catalog",
    "created_at",
    "updated_at",
    "deleted_at",
  ]);
  const id = identifier(item.id);
  stringValue(item.namespace);
  stringValue(item.desired_revision);
  const desired = closed(item.desired_state, [
    "enabled",
    "disabled",
    "deleted",
  ]);
  if (desired === "deleted") {
    if (item.transport !== null) throw new Error("invalid response");
  } else {
    validateTransport(item.transport);
  }
  const revisions = record(item.credential_revisions, [
    "static_credential",
    "oauth_client",
    "oauth_tokens",
  ]);
  Object.values(revisions).forEach(stringValue);
  const credential = closed(item.credential_state, [
    "not_required",
    "ready",
    "absent",
    "locked",
    "interaction_required",
    "unavailable",
    "unsupported",
    "refreshing",
    "reauthentication_required",
    "disconnecting",
    "cleanup_pending",
  ]);
  const hasCorrelation =
    typeof item.runtime === "object" &&
    item.runtime !== null &&
    "diagnostic_correlation" in item.runtime;
  const runtime = record(item.runtime, [
    "state",
    "reason",
    "runtime_id",
    "reconciliation",
    "dispatch",
    ...(hasCorrelation ? ["diagnostic_correlation"] : []),
  ]);
  decodeDiagnosticCorrelation(runtime.diagnostic_correlation);
  const runtimeState = closed(runtime.state, [
    "inactive",
    "activating",
    "active",
    "stopping",
    "retry_wait",
    "degraded",
    "authentication_required",
    "deleted",
  ]);
  nullableString(runtime.reason);
  nullableString(runtime.runtime_id);
  const reconciliation = validateLimit(runtime.reconciliation);
  const dispatch = validateLimit(runtime.dispatch);
  const catalog = record(item.catalog, [
    "durable_state",
    "active_state",
    "durable_revision",
    "active_revision",
    "durable_tool_count",
    "active_tool_count",
    "last_success_at",
    "traversal",
  ]);
  closed(catalog.durable_state, [
    "empty",
    "current",
    "stale",
    "unavailable",
    "retired",
  ]);
  const activeCatalog = closed(catalog.active_state, [
    "absent",
    "refreshing",
    "current",
    "stale",
    "unavailable",
  ]);
  nullableString(catalog.durable_revision);
  nullableString(catalog.active_revision);
  integer(catalog.durable_tool_count);
  const activeToolCount = integer(catalog.active_tool_count);
  nullableString(catalog.last_success_at);
  const traversal = validateLimit(catalog.traversal);
  stringValue(item.created_at);
  stringValue(item.updated_at);
  nullableString(item.deleted_at);
  const saturated =
    reconciliation.saturated || dispatch.saturated || traversal.saturated;
  const attention =
    desired === "enabled" &&
    (runtimeState !== "active" ||
      (credential !== "ready" && credential !== "not_required") ||
      activeCatalog !== "current" ||
      saturated);
  return {
    id,
    name: stringValue(item.display_name),
    desired,
    runtime: runtimeState,
    credential,
    catalog: activeCatalog,
    activeToolCount,
    attention,
    saturated,
  };
}
function decodeServerPage(value: unknown): {
  items: ServerView[];
  next: string | null;
} {
  const page = record(value, ["items", "next_cursor"]);
  return {
    items: array(page.items).map(decodeServer),
    next: cursor(page.next_cursor),
  };
}
function validatePolicy(value: unknown): string {
  const policy = record(value, [
    ...readOnlyKeys(value),
    "scope",
    "target",
    "constraint",
    "duration_seconds",
    "future_tools_acknowledged",
  ]);
  closed(policy.scope, ["tool", "server"]);
  const target = stringValue(policy.target);
  nullableString(policy.duration_seconds);
  booleanValue(policy.future_tools_acknowledged);
  if (
    decodeReadOnly(policy) &&
    (policy.scope !== "server" ||
      policy.constraint !== null ||
      policy.future_tools_acknowledged !== true)
  )
    throw new Error("invalid response");
  if (
    policy.constraint !== null &&
    (typeof policy.constraint !== "object" || Array.isArray(policy.constraint))
  )
    throw new Error("invalid response");
  return target;
}
export function decodeRequestPage(value: unknown): RequestSummary {
  const page = record(value, ["items", "next_cursor", "total_count", "offset"]);
  const total = integer(page.total_count);
  if (integer(page.offset) !== 0) throw new Error("invalid queue offset");
  const next = cursor(page.next_cursor);
  const items = array(page.items).map((candidate): RequestView => {
    const row = record(candidate, [
      "request",
      "principal_display_name",
      "server_display_name",
      "resolved_server_id",
      "resolved_upstream_name",
    ]);
    const principalName = stringValue(row.principal_display_name);
    const serverName = stringValue(row.server_display_name);
    identifier(row.resolved_server_id);
    if (row.resolved_upstream_name !== null)
      stringValue(row.resolved_upstream_name);
    const item = record(row.request, [
      "id",
      "principal_id",
      "state",
      "revision",
      "requested_policy",
      "approved_policy",
      "approved_grant_id",
      "rejection_reason",
      "created_at",
      "updated_at",
      "closed_at",
    ]);
    closed(item.state, ["pending"]);
    stringValue(item.revision);
    if (
      item.approved_policy !== null ||
      item.approved_grant_id !== null ||
      item.rejection_reason !== null ||
      item.closed_at !== null
    )
      throw new Error("invalid response");
    stringValue(item.updated_at);
    return {
      id: identifier(item.id),
      principalID: identifier(item.principal_id),
      principalName,
      serverName,
      target: validatePolicy(item.requested_policy),
      createdAt: stringValue(item.created_at),
    };
  });
  if (
    items.length !== Math.min(total, 5) ||
    (next === null) !== (items.length === total) ||
    new Set(items.map((item) => item.id)).size !== items.length
  )
    throw new Error("invalid queue coverage");
  return { items, total, complete: next === null };
}
async function responseJSON(response: Response): Promise<unknown> {
  if (
    response.status !== 200 ||
    response.headers.get("Content-Type") !== "application/json"
  )
    throw new Error("read failed");
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 4 * 1024 * 1024)
    throw new Error("response too large");
  return JSON.parse(body) as unknown;
}
async function get(context: ViewReadContext, path: string): Promise<Response> {
  const response = await fetch(path, {
    method: "GET",
    headers: { "X-CSRF-Token": context.csrfToken },
    credentials: "same-origin",
    redirect: "error",
    signal: context.signal,
  });
  if (await context.sessionLost(response)) throw new Error("session lost");
  return response;
}
async function isStaleCursor(response: Response): Promise<boolean> {
  if (
    response.status !== 409 ||
    response.headers.get("Content-Type") !== "application/problem+json"
  )
    return false;
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 64 * 1024) return false;
  let value: unknown;
  try {
    value = JSON.parse(body) as unknown;
  } catch {
    return false;
  }
  try {
    const problem = record(value, ["status", "code", "title"]);
    return (
      integer(problem.status) === 409 &&
      stringValue(problem.code) === "stale_cursor" &&
      stringValue(problem.title).length > 0
    );
  } catch {
    return false;
  }
}
async function readServers(context: ViewReadContext): Promise<ServerSummary> {
  let items: ServerView[] = [];
  let next: string | null = null;
  let restarted = false;
  const seen = new Set<string>();
  for (let pageNumber = 0; pageNumber < 32; pageNumber += 1) {
    const path = `/api/v2/mcp/servers?limit=50${next === null ? "" : `&cursor=${encodeURIComponent(next)}`}`;
    const response = await get(context, path);
    if (next !== null && !restarted && (await isStaleCursor(response))) {
      items = [];
      next = null;
      restarted = true;
      seen.clear();
      continue;
    }
    if (response.status !== 200) {
      if (items.length > 0) return { items, complete: false, restarted };
      throw new Error("server read failed");
    }
    const decoded = decodeServerPage(await responseJSON(response));
    items.push(...decoded.items.filter((item) => item.desired !== "deleted"));
    next = decoded.next;
    if (next === null) return { items, complete: true, restarted };
    if (seen.has(next)) throw new Error("repeated cursor");
    seen.add(next);
  }
  return { items, complete: false, restarted };
}

export class OverviewController {
  private readonly listeners = new Set<Listener>();
  private value: OverviewSnapshot = {};
  constructor(
    session: SessionClient,
    private readonly views: ViewCoordinator,
    setStorageLatched: (latched: boolean) => void,
  ) {
    const matches = (key: string) => key === "#/overview";
    views.registerPanel({
      id: "overview-status",
      matches,
      invalidations: ["system_status"],
      read: async (context) =>
        decodeStatus(
          await responseJSON(await get(context, "/api/v2/system-status")),
        ),
      publish: (status) => {
        this.value = { ...this.value, status };
        setStorageLatched(status.latched);
        this.emit();
      },
    });
    views.registerPanel({
      id: "overview-servers",
      matches,
      invalidations: ["servers", "catalog"],
      read: readServers,
      publish: (servers) => {
        this.value = { ...this.value, servers };
        this.emit();
      },
    });
    views.registerPanel({
      id: "overview-requests",
      matches,
      invalidations: ["grant_requests"],
      read: async (context) =>
        decodeRequestPage(
          await responseJSON(
            await get(
              context,
              "/api/v2/mcp/grant-requests?limit=5&state=pending&sort=submitted&direction=ascending",
            ),
          ),
        ),
      publish: (requests) => {
        this.value = { ...this.value, requests };
        this.emit();
      },
    });
    views.registerPanel({
      id: "overview-activity",
      matches,
      invalidations: ["system_status", "invocations"],
      read: async (context) => {
        const window = this.value.window ?? "1h";
        const result = decodeProtocolActivity(
          await responseJSON(
            await get(context, `/api/v2/protocol-activity?window=${window}`),
          ),
        );
        if (result.window !== window)
          throw new Error("Activity window mismatch");
        return result;
      },
      publish: (activity) => {
        this.value = { ...this.value, activity };
        this.emit();
      },
    });
    for (const [key, path] of Object.entries({
      httpCredentials: "http/credentials",
      httpGrants: "http/grants",
      gitCredentials: "git/credentials",
      gitRepositories: "git/repositories",
      gitGrants: "git/grants",
      mcpGrants: "mcp/grants",
      routing: "git/routing-profile",
    })) {
      views.registerPanel({
        id: `overview-${key}`,
        matches,
        invalidations: ["authorization", "http_credentials", "system_status"],
        read: async (context) =>
          key === "routing"
            ? (
                await decodeGitRoutingResponse(
                  await get(context, `/api/v2/${path}`),
                )
              ).origins.length
            : decodeInventoryTotal(
                await responseJSON(
                  await get(context, `/api/v2/${path}?limit=1`),
                ),
              ),
        publish: (count) => {
          this.value = {
            ...this.value,
            inventory: { ...this.value.inventory, [key]: count },
          };
          this.emit();
        },
      });
    }
    session.registerProtectedState(() => {
      this.value = {};
      setStorageLatched(false);
      this.emit();
    });
  }
  setWindow(window: ActivityWindow): void {
    if (
      !activityWindows.includes(window) ||
      window === (this.value.window ?? "1h")
    )
      return;
    const { activity: _previous, ...rest } = this.value;
    this.value = { ...rest, window };
    this.emit();
    void this.views.refreshPanel("overview-activity");
  }
  snapshot(): OverviewSnapshot {
    return this.value;
  }
  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    listener(this.value);
    return () => this.listeners.delete(listener);
  }
  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}

function Panel({
  id,
  title,
  panel,
  children,
}: {
  id: string;
  title: string;
  panel: PanelSnapshot | undefined;
  children?: ComponentChildren;
}) {
  const status = panel?.status ?? "loading";
  const sharedSource = id === "overview-material" || id === "overview-capacity";
  return (
    <section
      class="panel overview-panel"
      data-testid={id}
      data-panel-status={status}
      aria-labelledby={`${id}-title`}
    >
      <div class="panel-heading">
        <div>
          <h2 id={`${id}-title`}>{title}</h2>
        </div>
        <StatusLabel state={status === "error" ? "error" : status}>
          {sentenceCase(status)}
        </StatusLabel>
      </div>
      {panel?.hasValue === true && status !== "current" && (
        <p
          class="overview-evidence"
          role={
            id === "overview-material" || id === "overview-capacity"
              ? undefined
              : "status"
          }
        >
          {status === "error" ? "Refresh failed." : "Data stale."} Showing the
          last read; current state is unknown.
        </p>
      )}
      {status === "error" && panel?.hasValue !== true ? (
        sharedSource ? (
          <p>System read unavailable.</p>
        ) : (
          <StateNotice state="error" title="Read unavailable">
            <p>Refresh after checking Gateway availability.</p>
          </StateNotice>
        )
      ) : status === "loading" && panel?.hasValue !== true ? (
        sharedSource ? (
          <p>Loading system data</p>
        ) : (
          <StateNotice state="loading" title="Loading current data" />
        )
      ) : (
        children
      )}
    </section>
  );
}

function attentionReason(server: ServerView): string {
  if (server.credential !== "ready" && server.credential !== "not_required")
    return `Credentials ${sentenceCase(server.credential).toLowerCase()}`;
  if (server.runtime !== "active")
    return `Runtime ${sentenceCase(server.runtime).toLowerCase()}`;
  if (server.catalog !== "current")
    return `Active catalog ${sentenceCase(server.catalog).toLowerCase()}`;
  return "Capacity saturated";
}

function ReadinessFact({
  label,
  value,
  current,
}: {
  label: string;
  value: string | undefined;
  current: boolean;
}) {
  const state =
    !current || value === undefined || value === "disabled"
      ? "neutral"
      : value === "ready"
        ? "current"
        : value === "starting" || value === "draining"
          ? "warning"
          : "unavailable";
  return (
    <div>
      <dt>{label}</dt>
      <dd>
        <StatusLabel state={state}>
          {value === undefined ? "Not reported" : sentenceCase(value)}
        </StatusLabel>
      </dd>
    </div>
  );
}

function WaitingTime({ value }: { value: string }) {
  const elapsed = Date.now() - new Date(value).getTime();
  const minutes = Math.floor(elapsed / 60000);
  const label =
    !Number.isFinite(elapsed) || elapsed < 0
      ? "Waiting time unavailable"
      : minutes < 1
        ? "Waiting less than a minute"
        : minutes < 60
          ? `Waiting about ${minutes} min`
          : minutes < 1440
            ? `Waiting about ${Math.floor(minutes / 60)} h`
            : `Waiting about ${Math.floor(minutes / 1440)} d`;
  return (
    <time dateTime={value} title={`Submitted ${formatUserTime(value)}`}>
      {label}
    </time>
  );
}

export function Overview({
  controller,
  view,
}: {
  controller: OverviewController;
  principals: PrincipalDirectory;
  view: ViewSnapshot;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  const panel = (id: string) => view.panels[id];
  const status = snapshot.status;
  const pools = [
    ...(status?.limits ?? [])
      .filter(
        (item) =>
          item.name === "mcp_work" || item.name === "downstream_dispatch",
      )
      .map((item) => ({
        ...item,
        label: item.name === "mcp_work" ? "MCP requests" : "Downstream calls",
      })),
    ...(status?.httpProxy
      ? [
          {
            ...status.httpProxy.connections,
            name: "http_regular" as const,
            label: "HTTP connections",
          },
          {
            ...status.httpProxy.work,
            name: "http_regular" as const,
            label: "HTTP requests",
          },
        ]
      : []),
  ];
  const pressure = pools
    .filter((item) => capacityState(item) !== undefined)
    .sort((a, b) => Number(b.saturated) - Number(a.saturated));
  const configuredServers = snapshot.servers?.items.length ?? 0;
  const activeTools =
    snapshot.servers?.items.reduce(
      (total, server) => total + BigInt(server.activeToolCount),
      0n,
    ) ?? 0;
  const serversNeedingAttention =
    snapshot.servers?.items.filter((server) => server.attention) ?? [];
  const current = (id: string) => panel(id)?.status === "current";
  const sourceNotice = (id: string) =>
    panel(id)?.hasValue && !current(id) ? (
      <p class="overview-evidence">
        {panel(id)?.status === "error" ? "Refresh failed." : "Data stale."}{" "}
        Showing the last read; current state is unknown.
      </p>
    ) : null;
  const inventory = (
    key: string,
    label: string,
    href: string,
    count: number | bigint | undefined = snapshot.inventory?.[key],
    complete = true,
    source = `overview-${key}`,
  ) => (
    <div>
      <dt>
        <a href={href}>{label}</a>
      </dt>
      <dd>
        {count === undefined ? (
          panel(source)?.status === "error" ? (
            "Unavailable"
          ) : (
            "Loading…"
          )
        ) : !complete ? (
          "Incomplete"
        ) : (
          <>
            <a href={href}>{count.toLocaleString()}</a>
            {!current(source) && (
              <span class="overview-evidence"> · stale</span>
            )}
          </>
        )}
      </dd>
    </div>
  );
  const activity = (protocol: Protocol) => {
    const summary = snapshot.activity;
    if (!summary)
      return (
        <p>
          {panel("overview-activity")?.status === "error"
            ? "Recent activity unavailable"
            : "Loading recent activity…"}
        </p>
      );
    if (!summary.counts)
      return <p class="overview-evidence">Recent history unavailable</p>;
    const counts = summary.counts[protocol];
    const outcomes = [
      ["success", protocol === "mcp" ? "Succeeded" : "HTTP success"],
      ...(protocol === "git"
        ? [
            ["reported_success", "Reported push success"],
            ["reported_partial", "Reported partial push"],
          ]
        : []),
      ...(protocol === "http" ? [["other", "Other responses"]] : []),
      ["failed", "Failed"],
      ["denied", "Denied"],
      ...(protocol === "mcp" ? [["rejected", "Not admitted"]] : []),
      ["unknown", "Unknown"],
      ["incomplete", "Incomplete"],
    ] as const;
    return (
      <div class="protocol-activity">
        <p class="overview-headline">
          <a href={activityHref(protocol, summary)}>
            <strong>{counts.total.toLocaleString()}</strong>{" "}
            {protocol === "http"
              ? "requests"
              : protocol === "git"
                ? "recorded operations"
                : "invocations"}
          </a>
        </p>
        <dl class="protocol-outcomes">
          {outcomes.map(([key, label]) => (
            <div key={key}>
              <dt>{label}</dt>
              <dd>{counts[key as keyof typeof counts].toLocaleString()}</dd>
            </div>
          ))}
        </dl>
        <p class="overview-context">
          {summary.coverage === "partial"
            ? "Partially retained history"
            : "Retained only · completeness unknown"}
          {!current("overview-activity")
            ? " · stale; current counts unknown"
            : ""}
        </p>
        {protocol === "git" && (
          <p class="overview-context">Discovery is not a completed push.</p>
        )}
      </div>
    );
  };
  return (
    <div class="overview" data-testid="overview-grid">
      <Panel
        id="overview-status"
        title="Gateway status"
        panel={panel("overview-status")}
      >
        {status !== undefined && (
          <div class="overview-stack">
            <dl class="overview-facts">
              <ReadinessFact
                label="Process"
                value={status.ready ? "ready" : status.processState}
                current={current("overview-status")}
              />
              <ReadinessFact
                label="Administration API"
                value={status.endpoints?.api}
                current={current("overview-status")}
              />
              <ReadinessFact
                label="MCP ingress"
                value={status.endpoints?.mcp}
                current={current("overview-status")}
              />
              <ReadinessFact
                label="HTTP proxy"
                value={
                  !status.httpProxy
                    ? undefined
                    : !status.httpProxy.enabled
                      ? "disabled"
                      : status.httpProxy.ready
                        ? "ready"
                        : "not_ready"
                }
                current={current("overview-status")}
              />
            </dl>
            {!status.ready && (
              <StateNotice
                state="warning"
                title={`Process ${sentenceCase(status.processState).toLowerCase()} · not ready`}
              >
                <p>
                  Gateway is not ready to accept work.{" "}
                  <a href="#/system">Inspect System</a>
                </p>
              </StateNotice>
            )}
            <a href="#/system">System status</a>
          </div>
        )}
      </Panel>
      <Panel
        id="overview-material"
        title="Storage and security"
        panel={panel("overview-status")}
      >
        {status !== undefined && (
          <div class="overview-stack">
            <dl class="overview-facts">
              <div>
                <dt>Control storage</dt>
                <dd>
                  <FactStatus
                    value={
                      status.latched ? "recovery_required" : status.sqliteState
                    }
                    current={current("overview-status")}
                  />
                </dd>
              </div>
              <div>
                <dt>Traffic storage</dt>
                <dd>
                  <FactStatus
                    value={
                      !status.traffic
                        ? undefined
                        : status.traffic.state === "ready" &&
                            status.traffic.pressure
                          ? "storage_pressure"
                          : status.traffic.state
                    }
                    current={current("overview-status")}
                  />
                </dd>
              </div>
              <div>
                <dt>Credential storage</dt>
                <dd>
                  <FactStatus
                    value={status.keyring}
                    current={current("overview-status")}
                  />
                </dd>
              </div>
              <div>
                <dt>Interception CA loaded</dt>
                <dd>
                  <StatusLabel
                    state={current("overview-status") ? "neutral" : "stale"}
                  >
                    {!status.httpProxy
                      ? "Not reported"
                      : status.httpProxy.caReady
                        ? "Yes"
                        : "No"}
                  </StatusLabel>
                </dd>
              </div>
              <div>
                <dt>Backup activity</dt>
                <dd>
                  <FactStatus
                    value={status.backupState}
                    current={current("overview-status")}
                  />
                </dd>
              </div>
              <div>
                <dt>Last backup</dt>
                <dd class="overview-backup-time">
                  <UserTime
                    value={status.lastBackupAt}
                    fallback="No completion reported"
                    compact
                  />
                </dd>
              </div>
            </dl>
            {(status.latched || status.sqliteState !== "ready") && (
              <StateNotice
                state="error"
                title={
                  status.latched
                    ? "Recovery required"
                    : `Control storage ${status.sqliteState}`
                }
              >
                <p>
                  {status.latched
                    ? "Changes cannot be saved."
                    : "Storage is not ready."}{" "}
                  <a href="#/system">Inspect storage status</a>
                </p>
              </StateNotice>
            )}
            {status.keyring !== "ready" && (
              <StateNotice
                state="warning"
                title={`Credential storage: ${sentenceCase(status.keyring).toLowerCase()}`}
              >
                <p>
                  Startup capability is not a live credential check.{" "}
                  <a href="#/system">Inspect keyring status</a>
                </p>
              </StateNotice>
            )}
            <a href="#/system">System status</a>
          </div>
        )}
      </Panel>
      <Panel
        id="overview-capacity"
        title="Resource usage"
        panel={panel("overview-status")}
      >
        {status !== undefined && (
          <div class="overview-stack">
            <p class="overview-context">In use / limit</p>
            <dl class="overview-facts overview-pools">
              {pools.map((item) => (
                <div key={item.label}>
                  <dt>{item.label}</dt>
                  <dd>
                    <strong>
                      {item.inUse.toLocaleString()} /{" "}
                      {item.limit.toLocaleString()}
                    </strong>
                    {item.limit === 0 ? " · N/A" : ""}
                    {item.saturated
                      ? " · Saturated"
                      : capacityState(item) === "pressure"
                        ? " · 80% pressure"
                        : ""}
                  </dd>
                </div>
              ))}
              {!status.httpProxy && (
                <div>
                  <dt>HTTP pools</dt>
                  <dd>Not reported</dd>
                </div>
              )}
            </dl>
            {pressure.length > 0 && (
              <div class="overview-capacity">
                <ul class="overview-conditions">
                  {pressure.slice(0, 3).map((item) => (
                    <li key={item.label}>
                      <strong>{item.label}</strong>: {item.inUse} / {item.limit}
                      {" — "}
                      {capacityState(item) === "saturated"
                        ? "Capacity saturated; additional work may be rejected."
                        : "80% capacity pressure; headroom for additional work is limited."}
                    </li>
                  ))}
                </ul>
                {pressure.length > 3 && (
                  <p>{pressure.length - 3} additional pool under pressure.</p>
                )}
              </div>
            )}
            <a href="#/system?tab=resource-limits">Resource limits</a>
          </div>
        )}
      </Panel>
      <div class="protocol-toolbar">
        <h2>Protocol activity</h2>
        <label>
          Recent activity{" "}
          <select
            aria-label="Recent activity window"
            value={snapshot.window ?? "1h"}
            onChange={(event) =>
              controller.setWindow(event.currentTarget.value as ActivityWindow)
            }
          >
            {activityWindows.map((window) => (
              <option key={window} value={window}>
                {window}
              </option>
            ))}
          </select>
        </label>
      </div>
      <section
        class="panel overview-panel protocol-card"
        aria-labelledby="overview-http-title"
      >
        <h2 id="overview-http-title">HTTP</h2>
        <dl class="overview-facts protocol-inventory">
          {inventory("httpCredentials", "Credentials", "#/http/credentials")}
          {inventory("httpGrants", "Grants", "#/http/grants")}
        </dl>
        {activity("http")}
      </section>
      <section
        class="panel overview-panel protocol-card"
        aria-labelledby="overview-git-title"
      >
        <h2 id="overview-git-title">Git</h2>
        <dl class="overview-facts protocol-inventory">
          {inventory("routing", "Routing origins", "#/git/routing")}
          {inventory("gitCredentials", "Credentials", "#/git/credentials")}
          {inventory("gitRepositories", "Repositories", "#/git/repositories")}
          {inventory("gitGrants", "Grants", "#/git/grants")}
        </dl>
        {activity("git")}
      </section>
      <section
        class="panel overview-panel protocol-card"
        data-testid="overview-mcp"
        aria-labelledby="overview-mcp-title"
      >
        <h2 id="overview-mcp-title">MCP</h2>
        <dl class="overview-facts protocol-inventory">
          {inventory(
            "servers",
            "Servers",
            "#/mcp/servers",
            snapshot.servers ? configuredServers : undefined,
            snapshot.servers?.complete,
            "overview-servers",
          )}
          {inventory(
            "tools",
            "Tools",
            "#/mcp/tools",
            snapshot.servers ? activeTools : undefined,
            snapshot.servers?.complete,
            "overview-servers",
          )}
          {inventory("mcpGrants", "Grants", "#/mcp/grants")}
        </dl>
        {activity("mcp")}
        <div
          class="protocol-attention"
          data-testid="overview-servers"
          data-attention={serversNeedingAttention.length > 0 ? "true" : "false"}
          data-panel-status={panel("overview-servers")?.status ?? "loading"}
        >
          {sourceNotice("overview-servers")}
          <h3>
            <a href="#/mcp/servers">MCP attention</a>
          </h3>
          {!snapshot.servers && (
            <p>
              {panel("overview-servers")?.status === "error"
                ? "Unavailable"
                : "Loading…"}
            </p>
          )}
          {snapshot.servers !== undefined && (
            <>
              <p class="overview-headline">
                <a href="#/mcp/servers">
                  <strong>{serversNeedingAttention.length}</strong> servers
                  flagged
                </a>
                {!current("overview-servers")
                  ? " · last known; current state unknown"
                  : !snapshot.servers.complete
                    ? " · loaded; incomplete"
                    : ""}
              </p>
              {!snapshot.servers.complete && (
                <p class="overview-evidence">
                  Server traversal incomplete; additional affected servers may
                  exist.
                </p>
              )}
              {serversNeedingAttention.length === 0 ? (
                <p>
                  {!current("overview-servers")
                    ? ""
                    : !snapshot.servers.complete
                      ? "No affected servers among those loaded."
                      : configuredServers === 0
                        ? "No servers configured."
                        : ""}
                </p>
              ) : (
                <details>
                  <summary>Servers needing attention</summary>
                  <ul class="overview-triage-list">
                    {serversNeedingAttention.slice(0, 5).map((item) => (
                      <li key={item.id} data-testid="overview-server-row">
                        <a href={`#/mcp/servers/${item.id}`}>{item.name}</a>
                        <p>{attentionReason(item)}</p>
                      </li>
                    ))}
                  </ul>
                </details>
              )}
              {serversNeedingAttention.length > 5 && (
                <p>5 shown; more need attention.</p>
              )}
            </>
          )}
        </div>
        <div
          class="protocol-attention"
          data-testid="overview-requests"
          data-attention={
            (snapshot.requests?.total ?? 0) > 0 ? "true" : "false"
          }
          data-panel-status={panel("overview-requests")?.status ?? "loading"}
        >
          {sourceNotice("overview-requests")}
          <h3>
            <a href="#/mcp/access-requests">Pending MCP requests</a>
          </h3>
          {!snapshot.requests && (
            <p>
              {panel("overview-requests")?.status === "error"
                ? "Unavailable"
                : "Loading…"}
            </p>
          )}
          {snapshot.requests !== undefined && (
            <>
              <p class="overview-headline">
                <a href="#/mcp/access-requests">
                  <strong>{snapshot.requests.total}</strong> pending
                </a>
                {current("overview-requests")
                  ? ""
                  : " · last known; current queue unknown"}
              </p>
              {snapshot.requests.items.length === 0 ? (
                <p>
                  {!current("overview-requests")
                    ? ""
                    : snapshot.requests.complete
                      ? ""
                      : "No pending requests loaded; queue incomplete."}
                </p>
              ) : (
                <details>
                  <summary>Oldest pending requests</summary>
                  <ol
                    class="overview-triage-list overview-decisions"
                    aria-label="Pending access requests, oldest first"
                  >
                    {snapshot.requests.items.slice(0, 5).map((item) => (
                      <li key={item.id} data-testid="overview-request-row">
                        <div class="overview-requester">
                          {item.principalName || `Agent ${item.principalID}`}
                        </div>
                        <a href={`#/mcp/access-requests/${item.id}`}>
                          Review access to {item.target}
                          {item.serverName ? ` · ${item.serverName}` : ""}
                        </a>
                        <p>
                          <WaitingTime value={item.createdAt} />
                        </p>
                      </li>
                    ))}
                  </ol>
                </details>
              )}
              {(!snapshot.requests.complete ||
                snapshot.requests.items.length > 5) &&
                snapshot.requests.items.length > 0 && (
                  <p>
                    {Math.min(5, snapshot.requests.items.length)} shown; more
                    pending.
                  </p>
                )}
            </>
          )}
        </div>
      </section>
    </div>
  );
}
