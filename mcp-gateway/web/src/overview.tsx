import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import { decodeInvocationPage, type InvocationPageView } from "./invocations";
import { ComparisonTable, StateNotice, StatusLabel } from "./primitives";
import type { SessionClient } from "./session";
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
  "s2_idempotency_records",
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
export interface StatusView {
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
  attention: boolean;
}
interface ServerSummary {
  items: ServerView[];
  complete: boolean;
  restarted: boolean;
}
interface RequestView {
  id: string;
  principalID: string;
  target: string;
  createdAt: string;
}
interface RequestSummary {
  items: RequestView[];
  complete: boolean;
}
export interface OverviewSnapshot {
  status?: StatusView;
  servers?: ServerSummary;
  requests?: RequestSummary;
  invocations?: InvocationPageView;
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
  const root = record(value, [
    "process",
    "sqlite",
    "keyring",
    "limits",
    "backup",
    "protocols",
  ]);
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
    backupState: closed(backup.state, ["idle", "creating"]),
    lastBackupAt: nullableString(backup.last_completed_at),
    modernProtocol: stringValue(protocols.modern),
    legacyProtocol: stringValue(protocols.legacy),
    agentAuth: closed(protocols.agent_auth, [
      "deny_all",
      "principal_credentials",
    ]),
  };
}

function validateLimit(value: unknown): void {
  limit(value, "http_regular");
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
  ]);
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
  ]);
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
  const runtime = record(item.runtime, [
    "state",
    "reason",
    "runtime_id",
    "reconciliation",
    "dispatch",
  ]);
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
  validateLimit(runtime.reconciliation);
  validateLimit(runtime.dispatch);
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
  integer(catalog.active_tool_count);
  nullableString(catalog.last_success_at);
  validateLimit(catalog.traversal);
  stringValue(item.created_at);
  stringValue(item.updated_at);
  nullableString(item.deleted_at);
  const attention =
    desired === "enabled" &&
    (runtimeState !== "active" ||
      (credential !== "ready" && credential !== "not_required") ||
      activeCatalog !== "current");
  return {
    id,
    name: stringValue(item.display_name),
    desired,
    runtime: runtimeState,
    credential,
    catalog: activeCatalog,
    attention,
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
    policy.constraint !== null &&
    (typeof policy.constraint !== "object" || Array.isArray(policy.constraint))
  )
    throw new Error("invalid response");
  return target;
}
function decodeRequestPage(value: unknown): RequestSummary {
  const page = record(value, ["items", "next_cursor"]);
  const items = array(page.items).map((candidate): RequestView => {
    const item = record(candidate, [
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
      target: validatePolicy(item.requested_policy),
      createdAt: stringValue(item.created_at),
    };
  });
  return { items, complete: cursor(page.next_cursor) === null };
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
    const path = `/api/v1/servers?limit=100${next === null ? "" : `&cursor=${encodeURIComponent(next)}`}`;
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
    views: ViewCoordinator,
    setStorageLatched: (latched: boolean) => void,
  ) {
    const matches = (key: string) => key === "#/overview";
    views.registerPanel({
      id: "overview-status",
      matches,
      invalidations: ["system_status"],
      read: async (context) =>
        decodeStatus(
          await responseJSON(await get(context, "/api/v1/system-status")),
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
              "/api/v1/grant-requests?limit=100&state=pending",
            ),
          ),
        ),
      publish: (requests) => {
        this.value = { ...this.value, requests };
        this.emit();
      },
    });
    views.registerPanel({
      id: "overview-invocations",
      matches,
      invalidations: [],
      pollMilliseconds: 5000,
      read: async (context) =>
        decodeInvocationPage(
          await responseJSON(await get(context, "/api/v1/invocations?limit=5")),
        ),
      publish: (invocations) => {
        this.value = { ...this.value, invocations };
        this.emit();
      },
    });
    session.registerProtectedState(() => {
      this.value = {};
      setStorageLatched(false);
      this.emit();
    });
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

function capacityState(limit: LimitView): "saturated" | "pressure" | undefined {
  if (limit.saturated) return "saturated";
  if (limit.limit > 0 && BigInt(limit.inUse) * 5n >= BigInt(limit.limit) * 4n)
    return "pressure";
  return undefined;
}
function Panel({
  id,
  code,
  title,
  panel,
  children,
}: {
  id: string;
  code: string;
  title: string;
  panel: PanelSnapshot | undefined;
  children?: ComponentChildren;
}) {
  const status = panel?.status ?? "loading";
  return (
    <section
      class="panel overview-panel"
      data-testid={id}
      data-panel-status={status}
      aria-labelledby={`${id}-title`}
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">{code}</span>
          <h2 id={`${id}-title`}>{title}</h2>
        </div>
        <StatusLabel state={status === "error" ? "error" : status}>
          {status}
        </StatusLabel>
      </div>
      {status === "error" && panel?.hasValue !== true ? (
        <StateNotice state="error" title="Read unavailable">
          <p>Refresh after checking Gateway availability.</p>
        </StateNotice>
      ) : (
        children
      )}
    </section>
  );
}

export function Overview({
  controller,
  view,
  onRefresh,
}: {
  controller: OverviewController;
  view: ViewSnapshot;
  onRefresh: () => void;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  const panel = (id: string) => view.panels[id];
  const pressure =
    snapshot.status?.limits.filter(
      (item) => capacityState(item) !== undefined,
    ) ?? [];
  return (
    <div class="overview" data-testid="overview-grid">
      <div class="refresh-controls overview-refresh">
        <StatusLabel state={view.freshness}>Data {view.freshness}</StatusLabel>
        <button data-testid="manual-refresh" type="button" onClick={onRefresh}>
          Refresh
        </button>
      </div>
      <div class="overview-grid">
        <Panel
          id="overview-status"
          code="POSTURE-01"
          title="Operational posture"
          panel={panel("overview-status")}
        >
          {snapshot.status !== undefined && (
            <div class="overview-stack">
              <StatusLabel
                state={snapshot.status.ready ? "current" : "warning"}
              >
                Process {snapshot.status.processState}
              </StatusLabel>
              {snapshot.status.latched ? (
                <StateNotice state="error" title="Storage mutation is closed">
                  <p>
                    SQLite is latched. Inspect System for stopped recovery
                    guidance.
                  </p>
                </StateNotice>
              ) : (
                <StatusLabel state="current">
                  SQLite {snapshot.status.sqliteState}
                </StatusLabel>
              )}
              <StatusLabel
                state={
                  snapshot.status.keyring === "ready" ? "current" : "warning"
                }
              >
                Keyring {snapshot.status.keyring}
              </StatusLabel>
              {snapshot.status.keyring !== "ready" && (
                <p>
                  Keyring unavailable or interactive; later authority operations
                  may fail or require interaction.
                </p>
              )}
              {pressure.map((item) => (
                <p key={item.name}>
                  <strong>
                    {capacityState(item) === "saturated"
                      ? "Capacity saturated"
                      : "80% capacity pressure"}
                  </strong>{" "}
                  — {item.name}: {item.inUse} / {item.limit}
                </p>
              ))}
            </div>
          )}
        </Panel>
        <Panel
          id="overview-servers"
          code="SERVERS-01"
          title="Server attention"
          panel={panel("overview-servers")}
        >
          {snapshot.servers !== undefined &&
            (snapshot.servers.items.length === 0 ? (
              <p>
                No servers configured. <a href="#/servers/new">Create server</a>
              </p>
            ) : (
              <>
                <p>
                  <strong>
                    {snapshot.servers.complete
                      ? `${snapshot.servers.items.length} configured`
                      : `At least ${snapshot.servers.items.length} configured; count incomplete`}
                  </strong>
                </p>
                <ComparisonTable caption="Server operational posture">
                  <thead>
                    <tr>
                      <th>Server</th>
                      <th>Runtime</th>
                      <th>Credential</th>
                      <th>Catalog</th>
                    </tr>
                  </thead>
                  <tbody>
                    {snapshot.servers.items.map((item) => (
                      <tr key={item.id}>
                        <td>
                          <a href={`#/servers/${item.id}`}>{item.name}</a>
                          {item.attention && (
                            <>
                              <br />
                              <StatusLabel state="warning">
                                Needs operator attention
                              </StatusLabel>
                            </>
                          )}
                        </td>
                        <td>{item.runtime}</td>
                        <td>{item.credential}</td>
                        <td>{item.catalog}</td>
                      </tr>
                    ))}
                  </tbody>
                </ComparisonTable>
              </>
            ))}
        </Panel>
        <Panel
          id="overview-requests"
          code="REQUESTS-01"
          title="Pending requests"
          panel={panel("overview-requests")}
        >
          {snapshot.requests !== undefined && (
            <>
              <p>
                {snapshot.requests.items.length} pending{" "}
                {snapshot.requests.complete
                  ? "total"
                  : "shown; count incomplete"}
                .
              </p>
              <ul class="record-list">
                {snapshot.requests.items.map((item) => (
                  <li key={item.id}>
                    <a href={`#/requests/${item.id}`}>{item.target}</a>
                    <span>
                      Principal {item.principalID} · {item.createdAt}
                    </span>
                  </li>
                ))}
              </ul>
            </>
          )}
        </Panel>
        <Panel
          id="overview-invocations"
          code="AUDIT-01"
          title="Recent invocations"
          panel={panel("overview-invocations")}
        >
          {snapshot.invocations !== undefined && (
            <>
              <p>
                Newest retained summaries only
                {snapshot.invocations.nextCursor !== null
                  ? "; older retained records are not shown"
                  : ""}
                . Polling is not completion authority.
              </p>
              <ul class="record-list">
                {snapshot.invocations.items.map((item) => (
                  <li key={item.id}>
                    <a href={`#/invocations/${item.id}`}>
                      {item.requestedName ?? "Not resolved"}
                    </a>
                    <span>
                      {item.outcome} · {item.basis} · {item.admittedAt}
                    </span>
                    {item.basis === "missing_terminal" && (
                      <span class="warning-copy">
                        Missing terminal evidence does not prove nonexecution.
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </>
          )}
        </Panel>
      </div>
    </div>
  );
}
