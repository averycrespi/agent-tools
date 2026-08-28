import { useEffect, useState } from "preact/hooks";
import type { MutationCoordinator } from "./mutation";
import { InertJSON, StateNotice, StatusLabel } from "./primitives";
import { ServerEditor } from "./server-editor";
import type { SessionClient } from "./session";
import type {
  PanelSnapshot,
  ViewCoordinator,
  ViewReadContext,
  ViewSnapshot,
} from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
type JSONRecord = Record<string, unknown>;
type ListKind = "servers" | "descriptors" | "catalog";

function record(value: unknown, keys: readonly string[]): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  const result = value as JSONRecord;
  if (Object.keys(result).sort().join(",") !== [...keys].sort().join(","))
    throw new Error("invalid response");
  return result;
}
function optionalRecord(
  value: unknown,
  required: readonly string[],
  optional: readonly string[],
): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  const result = value as JSONRecord;
  const keys = Object.keys(result);
  if (
    required.some((key) => !Object.hasOwn(result, key)) ||
    keys.some((key) => !required.includes(key) && !optional.includes(key))
  )
    throw new Error("invalid response");
  return result;
}
function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("invalid response");
  return value;
}
function nullableText(value: unknown): string | null {
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
  const result = text(value);
  if (!gatewayID.test(result)) throw new Error("invalid response");
  return result;
}
function closed<T extends string>(value: unknown, allowed: readonly T[]): T {
  const result = text(value);
  if (!allowed.includes(result as T)) throw new Error("invalid response");
  return result as T;
}
function array(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("invalid response");
  return value;
}
function cursor(value: unknown): string | null {
  const result = nullableText(value);
  if (result !== null && (result.length === 0 || result.length > 4096))
    throw new Error("invalid response");
  return result;
}
function jsonObject(value: unknown): unknown {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  return value;
}

interface LimitView {
  inUse: number;
  limit: number;
  saturated: boolean;
}
export interface ServerView {
  id: string;
  namespace: string;
  displayName: string;
  desiredState: "enabled" | "disabled" | "deleted";
  desiredRevision: string;
  transport: unknown | null;
  staticRevision: string;
  oauthClientRevision: string;
  oauthTokensRevision: string;
  credentialState: string;
  runtimeState: string;
  runtimeReason: string | null;
  runtimeID: string | null;
  reconciliation: LimitView;
  dispatch: LimitView;
  durableState: string;
  activeState: string;
  durableRevision: string | null;
  activeRevision: string | null;
  durableToolCount: number;
  activeToolCount: number;
  lastSuccessAt: string | null;
  traversal: LimitView;
  createdAt: string;
  updatedAt: string;
  deletedAt: string | null;
}
export interface DescriptorView {
  id: string;
  serverID: string;
  upstreamName: string;
  externalName: string;
  descriptor: unknown;
  fingerprint: string;
  catalogRevision: string;
  firstSeenAt: string;
  lastSeenAt: string;
  retiredAt: string | null;
}
interface CatalogView {
  activeState: "empty" | "current" | "degraded";
  activeGeneration: string;
  changedAt: string | null;
  issueCount: number;
}
interface Page<T> {
  items: T[];
  nextCursor: string | null;
}

function decodeLimit(value: unknown): LimitView {
  const item = record(value, ["in_use", "limit", "saturated"]);
  return {
    inUse: integer(item.in_use),
    limit: integer(item.limit),
    saturated: booleanValue(item.saturated),
  };
}
function decodeTransport(value: unknown): unknown | null {
  if (value === null) return null;
  const candidate = value as JSONRecord;
  const kind = closed(candidate.kind, ["stdio", "streamable_http"] as const);
  if (kind === "stdio") {
    const transport = record(value, [
      "kind",
      "executable",
      "arguments",
      "working_directory",
      "environment",
      "secret_environment",
    ]);
    text(transport.executable);
    text(transport.working_directory);
    array(transport.arguments).forEach(text);
    for (const field of [transport.environment, transport.secret_environment]) {
      const values = optionalRecord(field, [], Object.keys(field as object));
      Object.values(values).forEach(text);
    }
    return value;
  }
  const transport = record(value, [
    "kind",
    "url",
    "protocol_mode",
    "authentication",
  ]);
  text(transport.url);
  closed(transport.protocol_mode, ["modern", "legacy", "auto"] as const);
  const authentication = transport.authentication as JSONRecord;
  const mode = closed(authentication.mode, [
    "none",
    "bearer",
    "oauth",
  ] as const);
  if (mode === "none" || mode === "bearer") {
    record(transport.authentication, ["mode"]);
    return value;
  }
  const oauth = record(transport.authentication, [
    "mode",
    "registration",
    "trusted_origins",
    "request_offline_access",
  ]);
  array(oauth.trusted_origins).forEach(text);
  booleanValue(oauth.request_offline_access);
  const registration = oauth.registration as JSONRecord;
  const registrationMode = closed(registration.mode, [
    "static",
    "dynamic",
  ] as const);
  if (registrationMode === "dynamic") {
    const dynamic = record(oauth.registration, ["mode", "issuer"]);
    nullableText(dynamic.issuer);
  } else {
    const fixed = record(oauth.registration, [
      "mode",
      "issuer",
      "client_id",
      "token_endpoint_auth_method",
    ]);
    nullableText(fixed.issuer);
    text(fixed.client_id);
    closed(fixed.token_endpoint_auth_method, [
      "none",
      "client_secret_basic",
      "client_secret_post",
    ] as const);
  }
  return value;
}
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
] as const;

export function decodeServer(value: unknown): ServerView {
  const server = record(value, [
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
  const revisions = record(server.credential_revisions, [
    "static_credential",
    "oauth_client",
    "oauth_tokens",
  ]);
  const runtime = record(server.runtime, [
    "state",
    "reason",
    "runtime_id",
    "reconciliation",
    "dispatch",
  ]);
  const runtimeReason = nullableText(runtime.reason);
  if (runtimeReason !== null) closed(runtimeReason, reasons);
  const catalog = record(server.catalog, [
    "durable_state",
    "active_state",
    "durable_revision",
    "active_revision",
    "durable_tool_count",
    "active_tool_count",
    "last_success_at",
    "traversal",
  ]);
  return {
    id: identifier(server.id),
    namespace: text(server.namespace),
    displayName: text(server.display_name),
    desiredState: closed(server.desired_state, [
      "enabled",
      "disabled",
      "deleted",
    ] as const),
    desiredRevision: text(server.desired_revision),
    transport: decodeTransport(server.transport),
    staticRevision: text(revisions.static_credential),
    oauthClientRevision: text(revisions.oauth_client),
    oauthTokensRevision: text(revisions.oauth_tokens),
    credentialState: closed(server.credential_state, [
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
    ] as const),
    runtimeState: closed(runtime.state, [
      "inactive",
      "activating",
      "active",
      "stopping",
      "retry_wait",
      "degraded",
      "authentication_required",
      "deleted",
    ] as const),
    runtimeReason,
    runtimeID: nullableText(runtime.runtime_id),
    reconciliation: decodeLimit(runtime.reconciliation),
    dispatch: decodeLimit(runtime.dispatch),
    durableState: closed(catalog.durable_state, [
      "empty",
      "current",
      "stale",
      "unavailable",
      "retired",
    ] as const),
    activeState: closed(catalog.active_state, [
      "absent",
      "refreshing",
      "current",
      "stale",
      "unavailable",
    ] as const),
    durableRevision: nullableText(catalog.durable_revision),
    activeRevision: nullableText(catalog.active_revision),
    durableToolCount: integer(catalog.durable_tool_count),
    activeToolCount: integer(catalog.active_tool_count),
    lastSuccessAt: nullableText(catalog.last_success_at),
    traversal: decodeLimit(catalog.traversal),
    createdAt: text(server.created_at),
    updatedAt: text(server.updated_at),
    deletedAt: nullableText(server.deleted_at),
  };
}
function decodeServerPage(value: unknown): Page<ServerView> {
  const page = record(value, ["items", "next_cursor"]);
  return {
    items: array(page.items).map(decodeServer),
    nextCursor: cursor(page.next_cursor),
  };
}
function decodeDescriptor(value: unknown): DescriptorView {
  const item = record(value, [
    "id",
    "server_id",
    "upstream_name",
    "external_name",
    "descriptor",
    "fingerprint",
    "catalog_revision",
    "first_seen_at",
    "last_seen_at",
    "retired_at",
  ]);
  const descriptor = optionalRecord(
    item.descriptor,
    ["name", "inputSchema", "annotations"],
    ["title", "description", "outputSchema"],
  );
  text(descriptor.name);
  if (descriptor.title !== undefined) text(descriptor.title);
  if (descriptor.description !== undefined) text(descriptor.description);
  jsonObject(descriptor.inputSchema);
  if (descriptor.outputSchema !== undefined)
    jsonObject(descriptor.outputSchema);
  const annotations = record(descriptor.annotations, [
    "title",
    "readOnlyHint",
    "destructiveHint",
    "idempotentHint",
    "openWorldHint",
  ]);
  nullableText(annotations.title);
  for (const key of [
    "readOnlyHint",
    "destructiveHint",
    "idempotentHint",
    "openWorldHint",
  ])
    booleanValue(annotations[key]);
  return {
    id: identifier(item.id),
    serverID: identifier(item.server_id),
    upstreamName: text(item.upstream_name),
    externalName: text(item.external_name),
    descriptor: item.descriptor,
    fingerprint: text(item.fingerprint),
    catalogRevision: text(item.catalog_revision),
    firstSeenAt: text(item.first_seen_at),
    lastSeenAt: text(item.last_seen_at),
    retiredAt: nullableText(item.retired_at),
  };
}
function decodeDescriptorPage(value: unknown): Page<DescriptorView> {
  const page = record(value, ["items", "next_cursor"]);
  return {
    items: array(page.items).map(decodeDescriptor),
    nextCursor: cursor(page.next_cursor),
  };
}
function decodeCatalogPage(value: unknown): {
  catalog: CatalogView;
  page: Page<DescriptorView>;
} {
  const root = record(value, ["catalog", "items", "next_cursor"]);
  const summary = record(root.catalog, [
    "active_state",
    "active_generation",
    "changed_at",
    "issue_count",
  ]);
  return {
    catalog: {
      activeState: closed(summary.active_state, [
        "empty",
        "current",
        "degraded",
      ] as const),
      activeGeneration: text(summary.active_generation),
      changedAt: nullableText(summary.changed_at),
      issueCount: integer(summary.issue_count),
    },
    page: {
      items: array(root.items).map(decodeDescriptor),
      nextCursor: cursor(root.next_cursor),
    },
  };
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
async function json(response: Response): Promise<unknown> {
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
async function staleCursor(response: Response): Promise<boolean> {
  if (
    response.status !== 409 ||
    response.headers.get("Content-Type") !== "application/problem+json"
  )
    return false;
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 64 * 1024) return false;
  try {
    const problem = record(JSON.parse(body) as unknown, [
      "status",
      "code",
      "title",
    ]);
    return (
      problem.status === 409 &&
      text(problem.code) === "stale_cursor" &&
      text(problem.title).length > 0
    );
  } catch {
    return false;
  }
}

interface ServerReadsSnapshot {
  viewKey: string;
  servers: readonly ServerView[];
  serverNext: string | null;
  server: ServerView | undefined;
  serverETag: string | undefined;
  descriptors: readonly DescriptorView[];
  descriptorNext: string | null;
  descriptor: DescriptorView | undefined;
  catalog: CatalogView | undefined;
  catalogItems: readonly DescriptorView[];
  catalogNext: string | null;
  loadingMore: boolean;
  restarted: boolean;
}
type ReadResult =
  | {
      kind: "servers";
      viewKey: string;
      page: Page<ServerView>;
      append: boolean;
      restarted: boolean;
    }
  | { kind: "server"; viewKey: string; server: ServerView; etag: string }
  | {
      kind: "descriptors";
      viewKey: string;
      page: Page<DescriptorView>;
      append: boolean;
      restarted: boolean;
    }
  | { kind: "descriptor"; viewKey: string; descriptor: DescriptorView }
  | {
      kind: "catalog";
      viewKey: string;
      catalog: CatalogView;
      page: Page<DescriptorView>;
      append: boolean;
      restarted: boolean;
    };
type Listener = (snapshot: ServerReadsSnapshot) => void;

function emptySnapshot(viewKey = ""): ServerReadsSnapshot {
  return {
    viewKey,
    servers: [],
    serverNext: null,
    server: undefined,
    serverETag: undefined,
    descriptors: [],
    descriptorNext: null,
    descriptor: undefined,
    catalog: undefined,
    catalogItems: [],
    catalogNext: null,
    loadingMore: false,
    restarted: false,
  };
}
function listPath(
  kind: ListKind,
  viewKey: string,
  next: string | null,
): string {
  const query = new URLSearchParams();
  query.set("limit", "50");
  if (kind === "descriptors") query.set("retired", "include");
  if (next !== null) query.set("cursor", next);
  if (kind === "servers") return `/api/v1/servers?${query.toString()}`;
  if (kind === "catalog") return `/api/v1/catalog?${query.toString()}`;
  const match =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=descriptors$/.exec(
      viewKey,
    );
  if (match === null) throw new Error("invalid descriptor location");
  return `/api/v1/servers/${match[1]!}/descriptors?${query.toString()}`;
}

export class ServerReadsController {
  private readonly views: ViewCoordinator;
  private readonly listeners = new Set<Listener>();
  private value = emptySnapshot();
  private continuation: { kind: ListKind; cursor: string } | undefined;
  private continuationPending = false;

  constructor(session: SessionClient, views: ViewCoordinator) {
    this.views = views;
    views.registerPanel({
      id: "server-reads",
      matches: (key) =>
        key === "#/servers" ||
        key === "#/catalog" ||
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(key) ||
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\?tab=descriptors$/.test(
          key,
        ) ||
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\/descriptors\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(
          key,
        ),
      invalidations: ["servers", "catalog"],
      read: (context) => this.read(context),
      publish: (result) => this.publish(result),
    });
    session.registerProtectedState(() => {
      this.continuation = undefined;
      this.continuationPending = false;
      this.value = emptySnapshot();
      this.emit();
    });
  }
  snapshot(): ServerReadsSnapshot {
    return this.value;
  }
  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    listener(this.value);
    return () => this.listeners.delete(listener);
  }
  async loadMore(kind: ListKind): Promise<void> {
    const next =
      kind === "servers"
        ? this.value.serverNext
        : kind === "descriptors"
          ? this.value.descriptorNext
          : this.value.catalogNext;
    if (this.continuationPending || next === null) return;
    this.continuationPending = true;
    this.continuation = { kind, cursor: next };
    this.value = { ...this.value, loadingMore: true };
    this.emit();
    try {
      await this.views.refreshPanel("server-reads");
    } finally {
      this.continuationPending = false;
      this.value = { ...this.value, loadingMore: false };
      this.emit();
    }
  }
  private async read(context: ViewReadContext): Promise<ReadResult> {
    if (context.viewKey !== this.value.viewKey) {
      this.continuation = undefined;
      this.value = emptySnapshot(context.viewKey);
      this.emit();
    }
    const descriptorItem =
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/descriptors\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
        context.viewKey,
      );
    if (descriptorItem !== null) {
      const response = await get(
        context,
        `/api/v1/servers/${descriptorItem[1]!}/descriptors/${descriptorItem[2]!}`,
      );
      return {
        kind: "descriptor",
        viewKey: context.viewKey,
        descriptor: decodeDescriptor(await json(response)),
      };
    }
    const serverItem = /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
      context.viewKey,
    );
    if (serverItem !== null) {
      const response = await get(context, `/api/v1/servers/${serverItem[1]!}`);
      const server = decodeServer(await json(response));
      const etag = response.headers.get("ETag");
      if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
        throw new Error("invalid server ETag");
      return { kind: "server", viewKey: context.viewKey, server, etag };
    }
    const kind: ListKind =
      context.viewKey === "#/servers"
        ? "servers"
        : context.viewKey === "#/catalog"
          ? "catalog"
          : "descriptors";
    const continuation = this.continuation;
    this.continuation = undefined;
    const next = continuation?.kind === kind ? continuation.cursor : null;
    let response = await get(context, listPath(kind, context.viewKey, next));
    let restarted = false;
    if (next !== null && (await staleCursor(response))) {
      restarted = true;
      response = await get(context, listPath(kind, context.viewKey, null));
    }
    if (kind === "servers")
      return {
        kind,
        viewKey: context.viewKey,
        page: decodeServerPage(await json(response)),
        append: next !== null && !restarted,
        restarted,
      };
    if (kind === "descriptors")
      return {
        kind,
        viewKey: context.viewKey,
        page: decodeDescriptorPage(await json(response)),
        append: next !== null && !restarted,
        restarted,
      };
    const catalog = decodeCatalogPage(await json(response));
    return {
      kind,
      viewKey: context.viewKey,
      catalog: catalog.catalog,
      page: catalog.page,
      append: next !== null && !restarted,
      restarted,
    };
  }
  private publish(result: ReadResult): void {
    if (result.kind === "server")
      this.value = {
        ...this.value,
        server: result.server,
        serverETag: result.etag,
        restarted: false,
      };
    else if (result.kind === "descriptor")
      this.value = {
        ...this.value,
        descriptor: result.descriptor,
        restarted: false,
      };
    else if (result.kind === "servers")
      this.value = {
        ...this.value,
        servers: result.append
          ? [...this.value.servers, ...result.page.items]
          : result.page.items,
        serverNext: result.page.nextCursor,
        restarted: result.restarted,
      };
    else if (result.kind === "descriptors")
      this.value = {
        ...this.value,
        descriptors: result.append
          ? [...this.value.descriptors, ...result.page.items]
          : result.page.items,
        descriptorNext: result.page.nextCursor,
        restarted: result.restarted,
      };
    else
      this.value = {
        ...this.value,
        catalog: result.catalog,
        catalogItems: result.append
          ? [...this.value.catalogItems, ...result.page.items]
          : result.page.items,
        catalogNext: result.page.nextCursor,
        restarted: result.restarted,
      };
    this.emit();
  }
  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}

function DataHeader({
  view,
  onRefresh,
}: {
  view: ViewSnapshot;
  onRefresh: () => void;
}) {
  return (
    <div class="refresh-controls">
      <StatusLabel state={view.freshness}>Data {view.freshness}</StatusLabel>
      <button data-testid="manual-refresh" type="button" onClick={onRefresh}>
        Refresh visible data
      </button>
    </div>
  );
}
function ReadPanel({
  panel,
  children,
}: {
  panel: PanelSnapshot | undefined;
  children: preact.ComponentChildren;
}) {
  if (panel?.status === "error" && panel.hasValue !== true)
    return <StateNotice state="error" title="Read unavailable" />;
  if (panel === undefined || (panel.status === "loading" && !panel.hasValue))
    return <StateNotice state="loading" title="Loading authoritative data" />;
  return <>{children}</>;
}
function ServerTabs({
  serverID,
  current,
}: {
  serverID: string;
  current: string;
}) {
  const tabs = [
    ["overview", "Overview", `#/servers/${serverID}`],
    ["operations", "Operations", `#/servers/${serverID}?tab=operations`],
    ["oauth", "OAuth", `#/servers/${serverID}?tab=oauth`],
    ["credentials", "Credentials", `#/servers/${serverID}?tab=credentials`],
    ["descriptors", "Descriptors", `#/servers/${serverID}?tab=descriptors`],
  ] as const;
  return (
    <nav class="subnav" aria-label="Server sections">
      {tabs.map(([name, label, href]) => (
        <a
          key={name}
          href={href}
          aria-current={current === name ? "page" : undefined}
        >
          {label}
        </a>
      ))}
    </nav>
  );
}
function ServerRows({ items }: { items: readonly ServerView[] }) {
  return (
    <div class="audit-records">
      {items.map((server) => (
        <article class="audit-record" data-testid="server-row" key={server.id}>
          <div class="audit-record-heading">
            <div>
              <span class="panel-code">{server.namespace}</span>
              <h3>
                <a href={`#/servers/${server.id}`}>{server.displayName}</a>
              </h3>
            </div>
            <StatusLabel
              state={
                server.desiredState === "deleted" ? "unavailable" : "current"
              }
            >
              desired {server.desiredState}
            </StatusLabel>
          </div>
          <p>
            <strong>Runtime</strong> {server.runtimeState} ·{" "}
            <strong>Credential</strong> {server.credentialState}
          </p>
          <p>
            <strong>durable {server.durableState}</strong> revision{" "}
            {server.durableRevision ?? "none"} ·{" "}
            <strong>active {server.activeState}</strong> revision{" "}
            {server.activeRevision ?? "none"}
          </p>
        </article>
      ))}
    </div>
  );
}
function CatalogRows({
  items,
  degraded,
}: {
  items: readonly DescriptorView[];
  degraded: boolean;
}) {
  return (
    <div class="audit-records">
      {items.map((descriptor) => (
        <article
          class="audit-record"
          data-testid="catalog-row"
          key={descriptor.id}
        >
          <div class="audit-record-heading">
            <div>
              <span class="panel-code">{descriptor.externalName}</span>
              <h3>
                <a
                  href={`#/servers/${descriptor.serverID}/descriptors/${descriptor.id}`}
                  data-tool-name={descriptor.upstreamName}
                >
                  {descriptor.upstreamName}
                </a>
              </h3>
            </div>
            <StatusLabel state={degraded ? "warning" : "current"}>
              Administrative evidence
            </StatusLabel>
          </div>
          <p>
            Recorded server{" "}
            <a href={`#/servers/${descriptor.serverID}`}>
              {descriptor.serverID}
            </a>{" "}
            · durable source revision {descriptor.catalogRevision}
          </p>
          <p>
            Process publication is not an authorization or callability claim.
          </p>
        </article>
      ))}
    </div>
  );
}
function DescriptorRows({ items }: { items: readonly DescriptorView[] }) {
  return (
    <div class="audit-records">
      {items.map((descriptor) => (
        <article
          class="audit-record"
          data-testid="descriptor-row"
          key={descriptor.id}
        >
          <div class="audit-record-heading">
            <div>
              <span class="panel-code">{descriptor.externalName}</span>
              <h3>
                <a
                  href={`#/servers/${descriptor.serverID}/descriptors/${descriptor.id}`}
                  data-tool-name={descriptor.upstreamName}
                >
                  {descriptor.upstreamName}
                </a>
              </h3>
            </div>
            <StatusLabel
              state={descriptor.retiredAt === null ? "current" : "unavailable"}
            >
              {descriptor.retiredAt === null
                ? "current evidence"
                : "retired evidence"}
            </StatusLabel>
          </div>
          <p>
            Recorded server{" "}
            <a href={`#/servers/${descriptor.serverID}`}>
              {descriptor.serverID}
            </a>{" "}
            · durable catalog revision {descriptor.catalogRevision} ·
            fingerprint {descriptor.fingerprint}
          </p>
          <p>
            Durable descriptor evidence is not proof of process publication or
            callability.
          </p>
        </article>
      ))}
    </div>
  );
}

export function ServerReads({
  controller,
  view,
  destination,
  mutations,
  onRefresh,
}: {
  controller: ServerReadsController;
  view: ViewSnapshot;
  destination: "servers" | "catalog";
  mutations: MutationCoordinator;
  onRefresh: () => void;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  const panel = view.panels["server-reads"];
  const descriptorItem =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/descriptors\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.exec(
      view.viewKey,
    );
  const serverItem = /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
    view.viewKey,
  );
  const descriptorList =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=descriptors$/.exec(
      view.viewKey,
    );
  const otherTab =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=([^&]+)$/.exec(
      view.viewKey,
    );
  if (view.viewKey === "#/servers/new")
    return (
      <div class="domain-view" data-testid="server-create-view">
        <a href="#/servers">Back to server inventory</a>
        <ServerEditor
          mutations={mutations}
          onRefresh={onRefresh}
          decodeServerValue={decodeServer}
        />
      </div>
    );
  if (destination === "catalog")
    return (
      <div class="domain-view" data-testid="catalog-view">
        <DataHeader view={view} onRefresh={onRefresh} />
        <section class="panel domain-panel" aria-labelledby="catalog-title">
          <div class="panel-heading">
            <div>
              <span class="panel-code">PROCESS CATALOG</span>
              <h2 id="catalog-title">Active administrative catalog</h2>
            </div>
            <StatusLabel
              state={
                snapshot.catalog?.activeState === "degraded"
                  ? "warning"
                  : "current"
              }
            >
              Catalog {snapshot.catalog?.activeState ?? "loading"}
            </StatusLabel>
          </div>
          <ReadPanel panel={panel}>
            {snapshot.catalog !== undefined && (
              <>
                <p>
                  Process generation {snapshot.catalog.activeGeneration} ·
                  changed {snapshot.catalog.changedAt ?? "never"} · issues{" "}
                  {snapshot.catalog.issueCount}
                </p>
                <p>
                  {snapshot.catalog.activeState === "degraded"
                    ? "Degraded administrative evidence does not establish routability."
                    : "Process-local administrative publication; do not infer authorization or future callability."}
                </p>
                <CatalogRows
                  items={snapshot.catalogItems}
                  degraded={snapshot.catalog.activeState === "degraded"}
                />
                {snapshot.catalogNext !== null && (
                  <button
                    data-testid="load-more-catalog"
                    type="button"
                    disabled={snapshot.loadingMore}
                    onClick={() => void controller.loadMore("catalog")}
                  >
                    Load more catalog tools
                  </button>
                )}
              </>
            )}
          </ReadPanel>
        </section>
      </div>
    );
  if (view.viewKey === "#/servers")
    return (
      <div class="domain-view" data-testid="servers-view">
        <DataHeader view={view} onRefresh={onRefresh} />
        <section class="panel domain-panel" aria-labelledby="servers-title">
          <div class="panel-heading">
            <div>
              <span class="panel-code">SERVER INVENTORY</span>
              <h2 id="servers-title">
                Desired, runtime, credential, and catalog state
              </h2>
            </div>
            <div>
              <span class="classification">DURABLE + PROCESS</span>
              <a href="#/servers/new" data-testid="server-create-link">
                Create server
              </a>
            </div>
          </div>
          <ReadPanel panel={panel}>
            {snapshot.servers.length === 0 ? (
              <StateNotice state="empty" title="No server identities" />
            ) : (
              <ServerRows items={snapshot.servers} />
            )}
            {snapshot.restarted && (
              <p class="bounded-note">
                A stale cursor restarted this traversal from the first page;
                stale pages were discarded.
              </p>
            )}
            {snapshot.serverNext !== null && (
              <button
                data-testid="load-more-servers"
                type="button"
                disabled={snapshot.loadingMore}
                onClick={() => void controller.loadMore("servers")}
              >
                Load more servers
              </button>
            )}
          </ReadPanel>
        </section>
      </div>
    );
  if (descriptorItem !== null)
    return (
      <div class="domain-view" data-testid="descriptor-detail">
        <ServerTabs serverID={descriptorItem[1]!} current="descriptors" />
        <DataHeader view={view} onRefresh={onRefresh} />
        <section
          class="panel domain-panel"
          aria-labelledby="descriptor-detail-title"
        >
          <div class="panel-heading">
            <div>
              <span class="panel-code">DURABLE DESCRIPTOR</span>
              <h2 id="descriptor-detail-title">Descriptor evidence</h2>
            </div>
          </div>
          <ReadPanel panel={panel}>
            {snapshot.descriptor !== undefined && (
              <>
                <StatusLabel
                  state={
                    snapshot.descriptor.retiredAt === null
                      ? "current"
                      : "unavailable"
                  }
                >
                  {snapshot.descriptor.retiredAt === null
                    ? "Current durable evidence"
                    : "Historical evidence; not callable"}
                </StatusLabel>
                <p>
                  Durable catalog revision {snapshot.descriptor.catalogRevision}
                </p>
                <p>
                  Recorded server{" "}
                  <a href={`#/servers/${snapshot.descriptor.serverID}`}>
                    {snapshot.descriptor.serverID}
                  </a>{" "}
                  · <a href="#/catalog">active catalog</a>
                </p>
                <p>
                  First seen {snapshot.descriptor.firstSeenAt} · last seen{" "}
                  {snapshot.descriptor.lastSeenAt} · retired{" "}
                  {snapshot.descriptor.retiredAt ?? "no"}
                </p>
                <InertJSON
                  value={snapshot.descriptor.descriptor}
                  label="Normalized durable tool descriptor"
                />
              </>
            )}
          </ReadPanel>
        </section>
      </div>
    );
  if (descriptorList !== null)
    return (
      <div class="domain-view" data-testid="descriptor-list">
        <ServerTabs serverID={descriptorList[1]!} current="descriptors" />
        <DataHeader view={view} onRefresh={onRefresh} />
        <section
          class="panel domain-panel"
          aria-labelledby="descriptor-list-title"
        >
          <div class="panel-heading">
            <div>
              <span class="panel-code">DURABLE HISTORY</span>
              <h2 id="descriptor-list-title">
                Current and retired descriptors
              </h2>
            </div>
            <a href="#/catalog">Active catalog</a>
          </div>
          <p>
            Durable descriptor evidence survives process withdrawal and restart.
            It is not proof of process publication or callability.
          </p>
          <ReadPanel panel={panel}>
            <DescriptorRows items={snapshot.descriptors} />
            {snapshot.restarted && (
              <p class="bounded-note">
                A stale cursor restarted this traversal; prior pages were
                discarded.
              </p>
            )}
            {snapshot.descriptorNext !== null && (
              <button
                data-testid="load-more-descriptors"
                type="button"
                disabled={snapshot.loadingMore}
                onClick={() => void controller.loadMore("descriptors")}
              >
                Load more descriptors
              </button>
            )}
          </ReadPanel>
        </section>
      </div>
    );
  if (serverItem !== null)
    return (
      <div class="domain-view" data-testid="server-detail">
        <ServerTabs serverID={serverItem[1]!} current="overview" />
        <DataHeader view={view} onRefresh={onRefresh} />
        <section
          class="panel domain-panel"
          aria-labelledby="server-detail-title"
        >
          <div class="panel-heading">
            <div>
              <span class="panel-code">SERVER RECORD</span>
              <h2 id="server-detail-title">
                {snapshot.server?.displayName ?? "Server detail"}
              </h2>
            </div>
            <a href="#/catalog">Active catalog</a>
          </div>
          <ReadPanel panel={panel}>
            {snapshot.server !== undefined && (
              <div class="fact-grid">
                <article class="fact-card">
                  <span class="panel-code">DESIRED</span>
                  <h3>{snapshot.server.desiredState}</h3>
                  <p>Desired revision {snapshot.server.desiredRevision}</p>
                  <p>Namespace {snapshot.server.namespace}</p>
                </article>
                <article class="fact-card">
                  <span class="panel-code">RUNTIME</span>
                  <h3>{snapshot.server.runtimeState}</h3>
                  <p>Reason {snapshot.server.runtimeReason ?? "none"}</p>
                  <p>Runtime identity {snapshot.server.runtimeID ?? "none"}</p>
                </article>
                <article class="fact-card">
                  <span class="panel-code">CREDENTIAL</span>
                  <h3>{snapshot.server.credentialState}</h3>
                  <p>
                    Static {snapshot.server.staticRevision} · OAuth client{" "}
                    {snapshot.server.oauthClientRevision} · tokens{" "}
                    {snapshot.server.oauthTokensRevision}
                  </p>
                </article>
                <article class="fact-card">
                  <span class="panel-code">CATALOG</span>
                  <h3>Active catalog {snapshot.server.activeState}</h3>
                  <p>
                    Durable catalog revision{" "}
                    {snapshot.server.durableRevision ?? "none"}
                  </p>
                  <p>
                    Active process revision{" "}
                    {snapshot.server.activeRevision ?? "none"}
                  </p>
                  <p>
                    Durable evidence is not proof of process publication or
                    callability.
                  </p>
                </article>
              </div>
            )}
          </ReadPanel>
        </section>
        {snapshot.server !== undefined &&
          snapshot.server.desiredState !== "deleted" &&
          snapshot.serverETag !== undefined && (
            <ServerEditor
              mutations={mutations}
              server={snapshot.server}
              etag={snapshot.serverETag}
              onRefresh={onRefresh}
              decodeServerValue={decodeServer}
            />
          )}
      </div>
    );
  if (otherTab !== null)
    return (
      <div class="domain-view">
        <ServerTabs serverID={otherTab[1]!} current={otherTab[2]!} />
        <StateNotice state="unavailable" title="Workflow not yet available" />
      </div>
    );
  return (
    <StateNotice
      state="unavailable"
      title="Server workflow not yet available"
    />
  );
}
