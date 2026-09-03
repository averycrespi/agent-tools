import { useEffect, useState } from "preact/hooks";
import type { MutationCoordinator } from "./mutation";
import {
  CollectionTable,
  ComparisonTable,
  sentenceCase,
  StateNotice,
  StatusLabel,
} from "./primitives";
import {
  authFlowIsTerminal,
  decodeAuthFlow,
  decodeAuthFlowPage,
  type AuthFlowPage,
  type ServerAuthFlowView,
} from "./server-auth-flow-model";
import { ServerAuthFlows } from "./server-auth-flows";
import { ServerCredentials } from "./server-credentials";
import { ServerDestructiveActions } from "./server-destructive";
import { ServerEditor } from "./server-editor";
import {
  decodeOperation,
  decodeOperationPage,
  operationIsTerminal,
  type OperationPage,
  type ServerOperationView,
} from "./server-operation-model";
import { ServerOperations } from "./server-operations";
import type { SessionClient } from "./session";
import { CopyableValue } from "./sinks-ui";
import type { SensitiveSinkCoordinator } from "./sinks";
import { UserTime } from "./time";
import type {
  PanelSnapshot,
  ViewCoordinator,
  ViewReadContext,
  ViewSnapshot,
} from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
type JSONRecord = Record<string, unknown>;
type ListKind =
  | "servers"
  | "descriptors"
  | "catalog"
  | "operations"
  | "authFlows";

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
interface CatalogDescriptorView extends DescriptorView {
  serverDisplayName: string;
  serverCatalogState: string;
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
function decodeDescriptor(value: unknown): DescriptorView;
function decodeDescriptor(value: unknown, catalog: true): CatalogDescriptorView;
function decodeDescriptor(
  value: unknown,
  catalog = false,
): DescriptorView | CatalogDescriptorView {
  const keys = [
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
  ];
  if (catalog) keys.push("server_display_name", "server_catalog_state");
  const item = record(value, keys);
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
  const decoded: DescriptorView = {
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
  return catalog
    ? {
        ...decoded,
        serverDisplayName: text(item.server_display_name),
        serverCatalogState: closed(item.server_catalog_state, [
          "refreshing",
          "current",
          "stale",
          "unavailable",
        ] as const),
      }
    : decoded;
}
function decodeDescriptorPage(value: unknown): Page<DescriptorView> {
  const page = record(value, ["items", "next_cursor"]);
  return {
    items: array(page.items).map((item) => decodeDescriptor(item)),
    nextCursor: cursor(page.next_cursor),
  };
}
function decodeCatalogPage(value: unknown): {
  catalog: CatalogView;
  page: Page<CatalogDescriptorView>;
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
      items: array(root.items).map((item) => decodeDescriptor(item, true)),
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
  catalogItems: readonly CatalogDescriptorView[];
  catalogNext: string | null;
  operations: readonly ServerOperationView[];
  operationNext: string | null;
  operation: ServerOperationView | undefined;
  authFlows: readonly ServerAuthFlowView[];
  authFlowNext: string | null;
  authFlow: ServerAuthFlowView | undefined;
  readVersion: number;
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
      kind: "operations";
      viewKey: string;
      server: ServerView;
      etag: string;
      page: OperationPage;
      append: boolean;
      restarted: boolean;
    }
  | {
      kind: "operation";
      viewKey: string;
      server: ServerView;
      etag: string;
      operation: ServerOperationView;
    }
  | {
      kind: "authFlows";
      viewKey: string;
      server: ServerView;
      etag: string;
      page: AuthFlowPage;
      append: boolean;
      restarted: boolean;
    }
  | {
      kind: "authFlow";
      viewKey: string;
      server: ServerView;
      etag: string;
      flow: ServerAuthFlowView;
    }
  | {
      kind: "descriptors";
      viewKey: string;
      server: ServerView;
      etag: string;
      page: Page<DescriptorView>;
      append: boolean;
      restarted: boolean;
    }
  | {
      kind: "descriptor";
      viewKey: string;
      server: ServerView;
      etag: string;
      descriptor: DescriptorView;
    }
  | {
      kind: "catalog";
      viewKey: string;
      catalog: CatalogView;
      page: Page<CatalogDescriptorView>;
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
    operations: [],
    operationNext: null,
    operation: undefined,
    authFlows: [],
    authFlowNext: null,
    authFlow: undefined,
    readVersion: 0,
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
  if (kind === "operations" || kind === "authFlows") {
    const match =
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=(?:activity|authentication|status)$/.exec(
        viewKey,
      );
    if (match === null) throw new Error("invalid server history location");
    const resource = kind === "operations" ? "operations" : "auth-flows";
    return `/api/v1/servers/${match[1]!}/${resource}?${query.toString()}`;
  }
  const match = /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=tools$/.exec(
    viewKey,
  );
  if (match === null) throw new Error("invalid descriptor location");
  return `/api/v1/servers/${match[1]!}/descriptors?${query.toString()}`;
}

function serverIDFromViewKey(viewKey: string): string | undefined {
  return /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})(?:[/?]|$)/.exec(
    viewKey,
  )?.[1];
}

function serverPanelID(viewKey: string): string {
  if (viewKey === "#/catalog") return "catalog-reads";
  if (/\?tab=activity$|\/operations\//.test(viewKey))
    return "server-operation-reads";
  if (/\/auth-flows\//.test(viewKey)) return "server-oauth-reads";
  if (/\?tab=tools$|\/descriptors\//.test(viewKey))
    return "server-descriptor-reads";
  return "server-overview-reads";
}

export class ServerReadsController {
  private readonly views: ViewCoordinator;
  private readonly listeners = new Set<Listener>();
  private value = emptySnapshot();
  private continuation: { kind: ListKind; cursor: string } | undefined;
  private continuationPending = false;

  constructor(session: SessionClient, views: ViewCoordinator) {
    this.views = views;
    const register = (
      id: string,
      matches: (key: string) => boolean,
      invalidations: ReadonlyArray<
        "servers" | "server_operations" | "server_auth_flows" | "catalog"
      >,
      polling = false,
    ) =>
      views.registerPanel<ReadResult>({
        id,
        matches,
        invalidations,
        ...(polling
          ? {
              pollMilliseconds: 2000,
              shouldPoll: () =>
                (this.value.operation !== undefined &&
                  !operationIsTerminal(this.value.operation)) ||
                (this.value.authFlow !== undefined &&
                  !authFlowIsTerminal(this.value.authFlow)) ||
                this.value.authFlows.some((flow) => !authFlowIsTerminal(flow)),
            }
          : {}),
        read: (context) =>
          this.read(
            context,
            id === "server-operation-reads"
              ? "operations"
              : id === "server-oauth-reads"
                ? "authFlows"
                : id === "server-descriptor-reads"
                  ? "descriptors"
                  : undefined,
          ),
        publish: (result) => this.publish(result),
      });
    register(
      "server-overview-reads",
      (key) =>
        key === "#/servers" ||
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}(?:\?tab=(?:authentication|settings|status))?$/.test(
          key,
        ),
      ["servers", "catalog"],
    );
    register(
      "server-operation-reads",
      (key) =>
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\?tab=activity$/.test(key) ||
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\/operations\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(
          key,
        ),
      ["servers", "server_operations", "catalog"],
      true,
    );
    register(
      "server-oauth-reads",
      (key) =>
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\?tab=authentication$/.test(
          key,
        ) ||
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\/auth-flows\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(
          key,
        ),
      ["servers", "server_auth_flows", "catalog"],
      true,
    );
    register(
      "server-descriptor-reads",
      (key) =>
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\?tab=tools$/.test(key) ||
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\/descriptors\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(
          key,
        ),
      ["catalog"],
    );
    register("catalog-reads", (key) => key === "#/catalog", ["catalog"]);
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
          : kind === "operations"
            ? this.value.operationNext
            : kind === "authFlows"
              ? this.value.authFlowNext
              : this.value.catalogNext;
    if (this.continuationPending || next === null) return;
    this.continuationPending = true;
    this.continuation = { kind, cursor: next };
    this.value = { ...this.value, loadingMore: true };
    this.emit();
    try {
      await this.views.refreshPanel(
        kind === "authFlows"
          ? "server-oauth-reads"
          : kind === "operations"
            ? "server-operation-reads"
            : serverPanelID(this.value.viewKey),
      );
    } finally {
      this.continuationPending = false;
      this.value = { ...this.value, loadingMore: false };
      this.emit();
    }
  }
  private async read(
    context: ViewReadContext,
    forcedKind?: "operations" | "authFlows" | "descriptors",
  ): Promise<ReadResult> {
    if (context.viewKey !== this.value.viewKey) {
      this.continuation = undefined;
      const serverID = serverIDFromViewKey(context.viewKey);
      const server =
        serverID !== undefined && this.value.server?.id === serverID
          ? this.value.server
          : undefined;
      this.value = { ...emptySnapshot(context.viewKey), server };
      this.emit();
    }
    const authFlowItem =
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/auth-flows\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
        context.viewKey,
      );
    if (authFlowItem !== null) {
      const [serverResponse, flowResponse] = await Promise.all([
        get(context, `/api/v1/servers/${authFlowItem[1]!}`),
        get(
          context,
          `/api/v1/servers/${authFlowItem[1]!}/auth-flows/${authFlowItem[2]!}`,
        ),
      ]);
      const server = decodeServer(await json(serverResponse));
      const etag = serverResponse.headers.get("ETag");
      if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
        throw new Error("invalid server ETag");
      const flow = decodeAuthFlow(await json(flowResponse));
      if (flow.serverID !== authFlowItem[1] || flow.id !== authFlowItem[2])
        throw new Error("auth flow route mismatch");
      return {
        kind: "authFlow",
        viewKey: context.viewKey,
        server,
        etag,
        flow,
      };
    }
    const operationItem =
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/operations\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
        context.viewKey,
      );
    if (operationItem !== null) {
      const [serverResponse, operationResponse] = await Promise.all([
        get(context, `/api/v1/servers/${operationItem[1]!}`),
        get(
          context,
          `/api/v1/servers/${operationItem[1]!}/operations/${operationItem[2]!}`,
        ),
      ]);
      const server = decodeServer(await json(serverResponse));
      const etag = serverResponse.headers.get("ETag");
      if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
        throw new Error("invalid server ETag");
      const operation = decodeOperation(await json(operationResponse));
      if (
        operation.serverID !== operationItem[1] ||
        operation.id !== operationItem[2]
      )
        throw new Error("operation route mismatch");
      return {
        kind: "operation",
        viewKey: context.viewKey,
        server,
        etag,
        operation,
      };
    }
    const descriptorItem =
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/descriptors\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
        context.viewKey,
      );
    if (descriptorItem !== null) {
      const [serverResponse, descriptorResponse] = await Promise.all([
        get(context, `/api/v1/servers/${descriptorItem[1]!}`),
        get(
          context,
          `/api/v1/servers/${descriptorItem[1]!}/descriptors/${descriptorItem[2]!}`,
        ),
      ]);
      const server = decodeServer(await json(serverResponse));
      const etag = serverResponse.headers.get("ETag");
      if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
        throw new Error("invalid server ETag");
      return {
        kind: "descriptor",
        viewKey: context.viewKey,
        server,
        etag,
        descriptor: decodeDescriptor(await json(descriptorResponse)),
      };
    }
    const serverItem =
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})(?:\?tab=(?:authentication|settings|status))?$/.exec(
        context.viewKey,
      );
    if (serverItem !== null && forcedKind === undefined) {
      const response = await get(context, `/api/v1/servers/${serverItem[1]!}`);
      const server = decodeServer(await json(response));
      const etag = response.headers.get("ETag");
      if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
        throw new Error("invalid server ETag");
      return { kind: "server", viewKey: context.viewKey, server, etag };
    }
    const kind: ListKind =
      forcedKind ??
      (context.viewKey === "#/servers"
        ? "servers"
        : context.viewKey === "#/catalog"
          ? "catalog"
          : "descriptors");
    const continuation = this.continuation;
    this.continuation = undefined;
    const next = continuation?.kind === kind ? continuation.cursor : null;
    const serverResponsePromise =
      kind === "operations" || kind === "authFlows" || kind === "descriptors"
        ? get(
            context,
            `/api/v1/servers/${context.viewKey.slice("#/servers/".length, "#/servers/".length + 26)}`,
          )
        : undefined;
    let [response, historyServerResponse] = await Promise.all([
      get(context, listPath(kind, context.viewKey, next)),
      serverResponsePromise ?? Promise.resolve(undefined),
    ]);
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
    if (kind === "descriptors") {
      if (historyServerResponse === undefined)
        throw new Error("missing server descriptor response");
      const server = decodeServer(await json(historyServerResponse));
      const etag = historyServerResponse.headers.get("ETag");
      if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
        throw new Error("invalid server ETag");
      return {
        kind,
        viewKey: context.viewKey,
        server,
        etag,
        page: decodeDescriptorPage(await json(response)),
        append: next !== null && !restarted,
        restarted,
      };
    }
    if (kind === "operations" || kind === "authFlows") {
      if (historyServerResponse === undefined)
        throw new Error("missing server history response");
      const server = decodeServer(await json(historyServerResponse));
      const etag = historyServerResponse.headers.get("ETag");
      if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
        throw new Error("invalid server ETag");
      if (kind === "operations")
        return {
          kind,
          viewKey: context.viewKey,
          server,
          etag,
          page: decodeOperationPage(await json(response)),
          append: next !== null && !restarted,
          restarted,
        };
      return {
        kind,
        viewKey: context.viewKey,
        server,
        etag,
        page: decodeAuthFlowPage(await json(response)),
        append: next !== null && !restarted,
        restarted,
      };
    }
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
        readVersion: this.value.readVersion + 1,
        restarted: false,
      };
    else if (result.kind === "operation")
      this.value = {
        ...this.value,
        server: result.server,
        serverETag: result.etag,
        operation: result.operation,
        readVersion: this.value.readVersion + 1,
        restarted: false,
      };
    else if (result.kind === "operations")
      this.value = {
        ...this.value,
        server: result.server,
        serverETag: result.etag,
        operations: result.append
          ? [...this.value.operations, ...result.page.items]
          : result.page.items,
        operationNext: result.page.nextCursor,
        readVersion: this.value.readVersion + 1,
        restarted: result.restarted,
      };
    else if (result.kind === "authFlow")
      this.value = {
        ...this.value,
        server: result.server,
        serverETag: result.etag,
        authFlow: result.flow,
        readVersion: this.value.readVersion + 1,
        restarted: false,
      };
    else if (result.kind === "authFlows")
      this.value = {
        ...this.value,
        server: result.server,
        serverETag: result.etag,
        authFlows: result.append
          ? [...this.value.authFlows, ...result.page.items]
          : result.page.items,
        authFlowNext: result.page.nextCursor,
        readVersion: this.value.readVersion + 1,
        restarted: result.restarted,
      };
    else if (result.kind === "descriptor")
      this.value = {
        ...this.value,
        server: result.server,
        serverETag: result.etag,
        descriptor: result.descriptor,
        readVersion: this.value.readVersion + 1,
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
        server: result.server,
        serverETag: result.etag,
        descriptors: result.append
          ? [...this.value.descriptors, ...result.page.items]
          : result.page.items,
        descriptorNext: result.page.nextCursor,
        readVersion: this.value.readVersion + 1,
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
    ["status", "Status", `#/servers/${serverID}?tab=status`],
    ["tools", "Tools", `#/servers/${serverID}?tab=tools`],
    ["activity", "Operations", `#/servers/${serverID}?tab=activity`],
    [
      "authentication",
      "Authentication",
      `#/servers/${serverID}?tab=authentication`,
    ],
    ["settings", "Settings", `#/servers/${serverID}?tab=settings`],
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
interface ServerPresentation {
  label: string;
  state: "current" | "loading" | "warning" | "unavailable" | "empty";
  action?: string;
  href?: string;
}

function serverUsesOAuth(server: ServerView): boolean {
  if (typeof server.transport !== "object" || server.transport === null)
    return false;
  const transport = server.transport as JSONRecord;
  if (transport.kind !== "streamable_http") return false;
  const authentication = transport.authentication;
  return (
    typeof authentication === "object" &&
    authentication !== null &&
    !Array.isArray(authentication) &&
    (authentication as JSONRecord).mode === "oauth"
  );
}

function serverPresentation(server: ServerView): ServerPresentation {
  const root = `#/servers/${server.id}`;
  if (server.desiredState === "deleted")
    return { label: "Deleted", state: "unavailable" };
  if (server.desiredState === "disabled")
    return {
      label: "Disabled",
      state: "empty",
      action: "Configure",
      href: `${root}?tab=settings`,
    };
  if (
    server.runtimeState === "authentication_required" ||
    server.credentialState === "absent" ||
    server.credentialState === "reauthentication_required"
  )
    return {
      label: "Authorization required",
      state: "warning",
      action: serverUsesOAuth(server)
        ? "Authorize server"
        : "Manage credentials",
      href: `${root}?tab=authentication`,
    };
  if (
    server.credentialState === "locked" ||
    server.credentialState === "interaction_required" ||
    server.credentialState === "unavailable" ||
    server.credentialState === "unsupported"
  )
    return {
      label: "Authentication unavailable",
      state: "warning",
      action: "Manage credentials",
      href: `${root}?tab=authentication`,
    };
  if (
    server.runtimeState === "activating" ||
    server.runtimeState === "retry_wait" ||
    server.credentialState === "refreshing" ||
    server.credentialState === "disconnecting" ||
    server.credentialState === "cleanup_pending"
  )
    return {
      label: "Connecting",
      state: "loading",
      action: "View operations",
      href: `${root}?tab=activity`,
    };
  const capacitySaturated =
    server.reconciliation.saturated ||
    server.dispatch.saturated ||
    server.traversal.saturated;
  if (
    server.runtimeState === "active" &&
    server.activeState === "current" &&
    (server.credentialState === "ready" ||
      server.credentialState === "not_required") &&
    !capacitySaturated
  )
    return { label: "Ready", state: "current" };
  if (server.runtimeState !== "active")
    return {
      label: "Needs attention",
      state: "warning",
      action: "View operations",
      href: `${root}?tab=activity`,
    };
  if (server.activeState !== "current")
    return {
      label: "Needs attention",
      state: "warning",
      action: "View tools",
      href: `${root}?tab=tools`,
    };
  return {
    label: "Capacity saturated",
    state: "warning",
    action: "View status",
    href: `${root}?tab=status`,
  };
}

function serverExplanation(
  presentation: ServerPresentation,
  server: ServerView,
): string {
  if (presentation.label === "Authorization required")
    return presentation.action === "Authorize server"
      ? "Authorize this server to restore authenticated access."
      : "Provide valid credentials to restore authenticated access.";
  if (presentation.label === "Authentication unavailable")
    return "Credential storage requires operator attention before authentication can succeed.";
  if (presentation.label === "Disabled")
    return "This server will not connect until it is enabled.";
  if (presentation.label === "Connecting")
    return "The gateway is establishing or cleaning up the server connection.";
  if (presentation.label === "Deleted")
    return "This server is retained as historical evidence.";
  if (presentation.label === "Capacity saturated")
    return "One or more server capacity limits are saturated.";
  if (server.runtimeReason !== null)
    return `Runtime reported ${sentenceCase(server.runtimeReason)}.`;
  return `The catalog is ${sentenceCase(server.activeState)}.`;
}

function ServerNavigation({
  server,
  serverID,
  current,
}: {
  server: ServerView | undefined;
  serverID: string;
  current: string;
}) {
  if (server === undefined) return null;
  const presentation = serverPresentation(server);
  return (
    <>
      <header class="server-context" data-testid="server-context">
        <div class="server-context-heading">
          <h2 tabindex={-1}>{server.displayName}</h2>
          <StatusLabel state={presentation.state}>
            {presentation.label}
          </StatusLabel>
        </div>
      </header>
      <ServerTabs serverID={serverID} current={current} />
    </>
  );
}

function ServerRows({ items }: { items: readonly ServerView[] }) {
  return (
    <CollectionTable
      caption="Servers"
      items={items}
      rowKey={(server) => server.id}
      rowTestID="server-row"
      filters={[
        {
          key: "name",
          label: "Name or ID",
          type: "text",
          value: (server) => server.displayName,
          literalValues: (server) => [server.id],
        },
        {
          key: "namespace",
          label: "Namespace",
          type: "text",
          value: (server) => server.namespace,
        },
        {
          key: "status",
          label: "Status",
          type: "select",
          value: (server) => serverPresentation(server).label,
          options: [
            { value: "Ready", label: "Ready" },
            { value: "Connecting", label: "Connecting" },
            {
              value: "Authorization required",
              label: "Authorization required",
            },
            {
              value: "Authentication unavailable",
              label: "Authentication unavailable",
            },
            {
              value: "Capacity saturated",
              label: "Capacity saturated",
            },
            { value: "Disabled", label: "Disabled" },
            { value: "Deleted", label: "Deleted" },
            { value: "Needs attention", label: "Needs attention" },
          ],
        },
      ]}
      emptyTitle="No servers match these filters"
      columns={[
        {
          key: "name",
          label: "Name",
          sortValue: (server) => server.displayName,
          render: (server) => (
            <a class="primary-table-link" href={`#/servers/${server.id}`}>
              {server.displayName}
            </a>
          ),
        },
        {
          key: "id",
          label: "ID",
          sortValue: (server) => server.id,
          render: (server) => (
            <a href={`#/servers/${server.id}`}>{server.id}</a>
          ),
        },
        {
          key: "namespace",
          label: "Namespace",
          sortValue: (server) => server.namespace,
          render: (server) => server.namespace,
        },
        {
          key: "status",
          label: "Status",
          sortValue: (server) => serverPresentation(server).label,
          render: (server) => {
            const presentation = serverPresentation(server);
            return (
              <StatusLabel state={presentation.state}>
                {presentation.label}
              </StatusLabel>
            );
          },
        },
        {
          key: "tools",
          label: "Tools",
          sortValue: (server) => server.activeToolCount,
          render: (server) => server.activeToolCount,
        },
      ]}
    />
  );
}
function schemaType(schema: JSONRecord): string {
  if (typeof schema.type === "string") return schema.type;
  if (Array.isArray(schema.type))
    return schema.type
      .filter((value) => typeof value === "string")
      .join(" or ");
  return "value";
}

function schemaConstraints(schema: JSONRecord): string[] {
  const result: string[] = [];
  if (typeof schema.format === "string")
    result.push(`Format: ${schema.format}`);
  if (Array.isArray(schema.enum))
    result.push(`Allowed: ${schema.enum.map(String).join(", ")}`);
  if (Object.hasOwn(schema, "const"))
    result.push(`Constant: ${String(schema.const)}`);
  if (Object.hasOwn(schema, "default"))
    result.push(`Default: ${String(schema.default)}`);
  for (const [key, label] of [
    ["minimum", "Minimum"],
    ["maximum", "Maximum"],
    ["minLength", "Minimum length"],
    ["maxLength", "Maximum length"],
    ["pattern", "Pattern"],
  ] as const)
    if (typeof schema[key] === "string" || typeof schema[key] === "number")
      result.push(`${label}: ${String(schema[key])}`);
  return result;
}

function SchemaNode({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    return (
      <span class="schema-constraint">
        {value === true
          ? "Any value"
          : value === false
            ? "No value"
            : "Invalid schema"}
      </span>
    );
  const schema = value as JSONRecord;
  const properties =
    typeof schema.properties === "object" &&
    schema.properties !== null &&
    !Array.isArray(schema.properties)
      ? (schema.properties as JSONRecord)
      : {};
  const required = new Set(
    Array.isArray(schema.required)
      ? schema.required.filter(
          (item): item is string => typeof item === "string",
        )
      : [],
  );
  const constraints = schemaConstraints(schema);
  const combinations = ["oneOf", "anyOf", "allOf"]
    .map((key) => [key, schema[key]] as const)
    .filter((entry): entry is readonly [string, unknown[]] =>
      Array.isArray(entry[1]),
    );
  return (
    <>
      {typeof schema.description === "string" && <p>{schema.description}</p>}
      {constraints.map((constraint) => (
        <span class="schema-constraint" key={constraint}>
          {constraint}
        </span>
      ))}
      {Object.keys(properties).length > 0 && (
        <dl class="schema-fields">
          {Object.entries(properties).map(([name, candidate]) => {
            const property = candidate as JSONRecord;
            return (
              <div key={name}>
                <dt>
                  <code>{name}</code>
                  <span>{schemaType(property)}</span>
                  {required.has(name) && <strong>Required</strong>}
                </dt>
                <dd>
                  <SchemaNode value={property} depth={depth + 1} />
                </dd>
              </div>
            );
          })}
        </dl>
      )}
      {depth < 5 &&
        typeof schema.items === "object" &&
        schema.items !== null && (
          <div class="schema-nested">
            <strong>Items</strong>
            <SchemaNode value={schema.items} depth={depth + 1} />
          </div>
        )}
      {depth < 5 &&
        combinations.map(([kind, alternatives]) => (
          <div class="schema-nested" key={kind}>
            <strong>{kind}</strong>
            {alternatives.map((alternative, index) => (
              <div key={index}>
                <span class="schema-constraint">Option {index + 1}</span>
                <SchemaNode value={alternative} depth={depth + 1} />
              </div>
            ))}
          </div>
        ))}
      {typeof schema.$ref === "string" && (
        <span class="schema-constraint">Reference: {schema.$ref}</span>
      )}
      {Object.keys(properties).length === 0 &&
        constraints.length === 0 &&
        schema.items === undefined &&
        combinations.length === 0 &&
        schema.$ref === undefined && (
          <p class="bounded-note">No additional constraints.</p>
        )}
    </>
  );
}

function ToolSchema({ label, value }: { label: string; value: unknown }) {
  const schema = value as JSONRecord;
  const id = `${label.toLowerCase().replace(" ", "-")}-title`;
  return (
    <section class="tool-schema" aria-labelledby={id}>
      <div class="tool-schema-heading">
        <h3 id={id}>{label}</h3>
        <span>{schemaType(schema)}</span>
      </div>
      <SchemaNode value={schema} />
    </section>
  );
}

function CatalogRows({ items }: { items: readonly CatalogDescriptorView[] }) {
  return (
    <CollectionTable
      caption="Available tools"
      items={items}
      rowKey={(descriptor) => descriptor.id}
      rowTestID="catalog-row"
      filters={[
        {
          key: "tool",
          label: "Tool",
          type: "text",
          value: (descriptor) => descriptor.externalName,
        },
        {
          key: "server",
          label: "Server",
          type: "text",
          value: (descriptor) => descriptor.serverDisplayName,
        },
        {
          key: "status",
          label: "Status",
          type: "select",
          value: (descriptor) =>
            descriptor.serverCatalogState === "current" ? "available" : "issue",
          options: [
            { value: "available", label: "Available" },
            { value: "issue", label: "Catalog issue" },
          ],
        },
      ]}
      columns={[
        {
          key: "tool",
          label: "Tool",
          sortValue: (descriptor) => descriptor.externalName,
          render: (descriptor) => (
            <a
              href={`#/servers/${descriptor.serverID}/descriptors/${descriptor.id}`}
              data-tool-name={descriptor.upstreamName}
            >
              {descriptor.externalName}
            </a>
          ),
        },
        {
          key: "server",
          label: "Server",
          sortValue: (descriptor) => descriptor.serverDisplayName,
          render: (descriptor) => (
            <a href={`#/servers/${descriptor.serverID}?tab=tools`}>
              {descriptor.serverDisplayName}
            </a>
          ),
        },
        {
          key: "status",
          label: "Status",
          render: (descriptor) => {
            const available = descriptor.serverCatalogState === "current";
            return (
              <StatusLabel state={available ? "current" : "warning"}>
                {available ? "Available" : "Catalog issue"}
              </StatusLabel>
            );
          },
        },
      ]}
    />
  );
}
function DescriptorRows({ items }: { items: readonly DescriptorView[] }) {
  return (
    <CollectionTable
      caption="Server tools"
      items={items}
      rowKey={(descriptor) => descriptor.id}
      rowTestID="descriptor-row"
      filters={[
        {
          key: "tool",
          label: "Tool",
          type: "text",
          value: (descriptor) => descriptor.externalName,
        },
        {
          key: "status",
          label: "Status",
          type: "select",
          value: (descriptor) =>
            descriptor.retiredAt === null ? "available" : "retired",
          options: [
            { value: "available", label: "Available" },
            { value: "retired", label: "Retired" },
          ],
        },
      ]}
      initialSort={{ key: "last-seen", direction: "descending" }}
      columns={[
        {
          key: "tool",
          label: "Tool",
          sortValue: (descriptor) => descriptor.externalName,
          render: (descriptor) => (
            <a
              href={`#/servers/${descriptor.serverID}/descriptors/${descriptor.id}`}
              data-tool-name={descriptor.upstreamName}
            >
              {descriptor.externalName}
            </a>
          ),
        },
        {
          key: "status",
          label: "Status",
          sortValue: (descriptor) =>
            descriptor.retiredAt === null ? "available" : "retired",
          render: (descriptor) => (
            <StatusLabel
              state={descriptor.retiredAt === null ? "current" : "unavailable"}
            >
              {descriptor.retiredAt === null ? "Available" : "Retired"}
            </StatusLabel>
          ),
        },
        {
          key: "last-seen",
          label: "Last seen",
          sortValue: (descriptor) => descriptor.lastSeenAt,
          render: (descriptor) => <UserTime value={descriptor.lastSeenAt} />,
        },
      ]}
    />
  );
}

export function ServerReads({
  controller,
  view,
  destination,
  mutations,
  sinks,
  onRefresh,
  notify,
}: {
  controller: ServerReadsController;
  view: ViewSnapshot;
  destination: "servers" | "catalog";
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  onRefresh: () => void;
  notify: (message: string) => void;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  const panel = view.panels[serverPanelID(view.viewKey)];
  const overviewPanel = view.panels["server-overview-reads"];
  const operationPanel = view.panels["server-operation-reads"];
  const authFlowPanel = view.panels["server-oauth-reads"];
  const authenticationTab =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=authentication$/.exec(
      view.viewKey,
    );
  const authFlowItem =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/auth-flows\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.exec(
      view.viewKey,
    );
  const activityTab =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=activity$/.exec(
      view.viewKey,
    );
  const operationItem =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/operations\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.exec(
      view.viewKey,
    );
  const settingsTab =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=settings$/.exec(
      view.viewKey,
    );
  const statusTab =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=status$/.exec(
      view.viewKey,
    );
  const descriptorItem =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\/descriptors\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.exec(
      view.viewKey,
    );
  const serverItem = /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
    view.viewKey,
  );
  const descriptorList =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=tools$/.exec(view.viewKey);
  const otherTab =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=([^&]+)$/.exec(
      view.viewKey,
    );
  const statusServerID = statusTab?.[1] ?? serverItem?.[1];
  if (view.viewKey === "#/servers/new")
    return (
      <div class="domain-view" data-testid="server-create-view">
        <a href="#/servers">Back to server inventory</a>
        <ServerEditor
          mutations={mutations}
          onRefresh={onRefresh}
          notify={notify}
          decodeServerValue={decodeServer}
        />
      </div>
    );
  if (destination === "catalog")
    return (
      <div class="domain-view" data-testid="catalog-view">
        <section class="panel domain-panel" aria-labelledby="page-title">
          <ReadPanel panel={panel}>
            {snapshot.catalog !== undefined && (
              <>
                <CatalogRows items={snapshot.catalogItems} />
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
        <div class="collection-toolbar">
          <a
            class="button-link create-action"
            href="#/servers/new"
            data-testid="server-create-link"
          >
            Create server
          </a>
        </div>
        <section class="panel domain-panel" aria-labelledby="page-title">
          <ReadPanel panel={panel}>
            {snapshot.servers.length === 0 ? (
              <StateNotice state="empty" title="No servers" />
            ) : (
              <ServerRows items={snapshot.servers} />
            )}
            {snapshot.restarted && (
              <p class="bounded-note">
                The server list changed while loading. Current results replaced
                the stale pages.
              </p>
            )}
            {snapshot.serverNext !== null && (
              <button
                data-testid="load-more-servers"
                type="button"
                disabled={snapshot.loadingMore}
                onClick={() => void controller.loadMore("servers")}
              >
                {snapshot.loadingMore ? "Loading…" : "Load more"}
              </button>
            )}
          </ReadPanel>
        </section>
      </div>
    );
  if (authenticationTab !== null)
    return (
      <div class="domain-view" data-testid="server-authentication-view">
        <ServerNavigation
          server={snapshot.server}
          serverID={authenticationTab[1]!}
          current="authentication"
        />
        <ReadPanel panel={authFlowPanel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined && (
              <ServerAuthFlows
                mutations={mutations}
                sinks={sinks}
                server={snapshot.server}
                etag={snapshot.serverETag}
                readVersion={snapshot.readVersion}
                flows={snapshot.authFlows}
                flow={undefined}
                nextCursor={snapshot.authFlowNext}
                loadingMore={snapshot.loadingMore}
                restarted={snapshot.restarted}
                onLoadMore={() => void controller.loadMore("authFlows")}
                onRefresh={onRefresh}
                mode="action"
              />
            )}
        </ReadPanel>
        <ReadPanel panel={overviewPanel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined && (
              <ServerCredentials
                mutations={mutations}
                sinks={sinks}
                server={snapshot.server}
                etag={snapshot.serverETag}
                readVersion={snapshot.readVersion}
                onRefresh={onRefresh}
              />
            )}
        </ReadPanel>
        <ReadPanel panel={authFlowPanel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined && (
              <ServerAuthFlows
                mutations={mutations}
                sinks={sinks}
                server={snapshot.server}
                etag={snapshot.serverETag}
                readVersion={snapshot.readVersion}
                flows={snapshot.authFlows}
                flow={undefined}
                nextCursor={snapshot.authFlowNext}
                loadingMore={snapshot.loadingMore}
                restarted={snapshot.restarted}
                onLoadMore={() => void controller.loadMore("authFlows")}
                onRefresh={onRefresh}
                mode="history"
              />
            )}
        </ReadPanel>
      </div>
    );
  if (authFlowItem !== null) {
    const serverID = authFlowItem[1]!;
    return (
      <div class="domain-view" data-testid="server-auth-flows-view">
        <ServerNavigation
          server={snapshot.server}
          serverID={serverID}
          current="authentication"
        />
        <ReadPanel panel={panel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined && (
              <ServerAuthFlows
                mutations={mutations}
                sinks={sinks}
                server={snapshot.server}
                etag={snapshot.serverETag}
                readVersion={snapshot.readVersion}
                flows={snapshot.authFlows}
                flow={snapshot.authFlow}
                nextCursor={snapshot.authFlowNext}
                loadingMore={snapshot.loadingMore}
                restarted={snapshot.restarted}
                onLoadMore={() => void controller.loadMore("authFlows")}
                onRefresh={onRefresh}
              />
            )}
        </ReadPanel>
      </div>
    );
  }
  if (activityTab !== null)
    return (
      <div class="domain-view" data-testid="server-activity-view">
        <ServerNavigation
          server={snapshot.server}
          serverID={activityTab[1]!}
          current="activity"
        />
        <ReadPanel panel={operationPanel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined && (
              <ServerOperations
                mutations={mutations}
                server={snapshot.server}
                etag={snapshot.serverETag}
                readVersion={snapshot.readVersion}
                operations={snapshot.operations}
                operation={undefined}
                nextCursor={snapshot.operationNext}
                loadingMore={snapshot.loadingMore}
                restarted={snapshot.restarted}
                onLoadMore={() => void controller.loadMore("operations")}
              />
            )}
        </ReadPanel>
      </div>
    );
  if (operationItem !== null) {
    const serverID = operationItem[1]!;
    return (
      <div class="domain-view" data-testid="server-operations-view">
        <ServerNavigation
          server={snapshot.server}
          serverID={serverID}
          current="activity"
        />
        <ReadPanel panel={panel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined && (
              <ServerOperations
                mutations={mutations}
                server={snapshot.server}
                etag={snapshot.serverETag}
                readVersion={snapshot.readVersion}
                operations={snapshot.operations}
                operation={snapshot.operation}
                nextCursor={snapshot.operationNext}
                loadingMore={snapshot.loadingMore}
                restarted={snapshot.restarted}
                onLoadMore={() => void controller.loadMore("operations")}
              />
            )}
        </ReadPanel>
      </div>
    );
  }
  if (descriptorItem !== null)
    return (
      <div class="domain-view" data-testid="descriptor-detail">
        <ServerNavigation
          server={snapshot.server}
          serverID={descriptorItem[1]!}
          current="tools"
        />
        <section
          class="panel domain-panel"
          aria-labelledby="descriptor-detail-title"
        >
          <ReadPanel panel={panel}>
            {snapshot.descriptor !== undefined &&
              (() => {
                const descriptor = snapshot.descriptor;
                const document = descriptor.descriptor as JSONRecord;
                const annotations = document.annotations as JSONRecord;
                return (
                  <>
                    <nav class="detail-navigation" aria-label="Tool navigation">
                      <a href={`#/servers/${descriptor.serverID}?tab=tools`}>
                        Back to tools
                      </a>
                      <span aria-hidden="true">·</span>
                      <a href="#/catalog">Back to catalog</a>
                    </nav>
                    <div class="panel-heading tool-heading">
                      <div>
                        <h2 id="descriptor-detail-title">
                          {descriptor.externalName}
                        </h2>
                        {typeof document.description === "string" && (
                          <p>{document.description}</p>
                        )}
                      </div>
                      <StatusLabel
                        state={
                          descriptor.retiredAt === null
                            ? "current"
                            : "unavailable"
                        }
                      >
                        {descriptor.retiredAt === null
                          ? "Available"
                          : "Historical evidence; not callable"}
                      </StatusLabel>
                    </div>
                    <dl class="tool-metadata">
                      <div>
                        <dt>Catalog revision</dt>
                        <dd>{descriptor.catalogRevision}</dd>
                      </div>
                      <div>
                        <dt>First seen</dt>
                        <dd>
                          <UserTime value={descriptor.firstSeenAt} />
                        </dd>
                      </div>
                      <div>
                        <dt>Last seen</dt>
                        <dd>
                          <UserTime value={descriptor.lastSeenAt} />
                        </dd>
                      </div>
                      <div>
                        <dt>Retired</dt>
                        <dd>
                          <UserTime
                            value={descriptor.retiredAt}
                            fallback="No"
                          />
                        </dd>
                      </div>
                    </dl>
                    <div class="tool-annotations" aria-label="Tool behavior">
                      <span>
                        {annotations.readOnlyHint ? "Read only" : "May write"}
                      </span>
                      <span>
                        {annotations.destructiveHint
                          ? "Destructive"
                          : "Non-destructive"}
                      </span>
                      <span>
                        {annotations.idempotentHint
                          ? "Idempotent"
                          : "Not idempotent"}
                      </span>
                      <span>
                        {annotations.openWorldHint
                          ? "External access"
                          : "Closed world"}
                      </span>
                    </div>
                    <div class="tool-schema-grid">
                      <ToolSchema
                        label="Input schema"
                        value={document.inputSchema}
                      />
                      {document.outputSchema !== undefined && (
                        <ToolSchema
                          label="Output schema"
                          value={document.outputSchema}
                        />
                      )}
                    </div>
                  </>
                );
              })()}
          </ReadPanel>
        </section>
      </div>
    );
  if (descriptorList !== null)
    return (
      <div class="domain-view" data-testid="descriptor-list">
        <ServerNavigation
          server={snapshot.server}
          serverID={descriptorList[1]!}
          current="tools"
        />
        <section
          class="panel domain-panel"
          aria-labelledby="descriptor-list-title"
        >
          <div class="panel-heading">
            <h2 id="descriptor-list-title">Tools</h2>
            <a href="#/catalog">All available tools</a>
          </div>
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
                {snapshot.loadingMore ? "Loading…" : "Load more tools"}
              </button>
            )}
          </ReadPanel>
        </section>
      </div>
    );
  if (settingsTab !== null)
    return (
      <div class="domain-view" data-testid="server-settings-view">
        <ServerNavigation
          server={snapshot.server}
          serverID={settingsTab[1]!}
          current="settings"
        />
        <ReadPanel panel={panel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined &&
            snapshot.server.desiredState !== "deleted" && (
              <>
                <ServerEditor
                  mutations={mutations}
                  server={snapshot.server}
                  etag={snapshot.serverETag}
                  onRefresh={onRefresh}
                  notify={notify}
                  decodeServerValue={decodeServer}
                />
                <ServerDestructiveActions
                  mutations={mutations}
                  server={snapshot.server}
                  etag={snapshot.serverETag}
                  readVersion={snapshot.readVersion}
                  decodeServerValue={decodeServer}
                />
              </>
            )}
          {snapshot.server?.desiredState === "deleted" && (
            <StateNotice state="unavailable" title="Server deleted" />
          )}
        </ReadPanel>
      </div>
    );
  if (statusServerID !== undefined)
    return (
      <div class="domain-view" data-testid="server-status-view">
        <ServerNavigation
          server={snapshot.server}
          serverID={statusServerID}
          current="status"
        />
        <section
          class="panel domain-panel operator-status-view"
          aria-labelledby="server-status-title"
        >
          <div class="panel-heading">
            <h2 id="server-status-title">Server status</h2>
          </div>
          <ReadPanel panel={panel}>
            {snapshot.server !== undefined &&
              (() => {
                const server = snapshot.server;
                const presentation = serverPresentation(server);
                const capacities = [
                  ["Reconciliation", server.reconciliation],
                  ["Dispatch", server.dispatch],
                  ["Catalog traversal", server.traversal],
                ] as const;
                const saturated = capacities.filter(
                  ([, value]) => value.saturated,
                );
                const hasPrimaryIssue = presentation.action !== undefined;
                return (
                  <div class="operator-status-stack">
                    <section
                      class="operator-status-section"
                      aria-labelledby="server-issues-title"
                      data-testid="server-status-issues"
                    >
                      <div class="operator-status-section-heading">
                        <h3 id="server-issues-title">Needs attention</h3>
                        {(hasPrimaryIssue || saturated.length > 0) && (
                          <span>Operator action may be required</span>
                        )}
                      </div>
                      {server.desiredState === "deleted" && (
                        <StateNotice state="unavailable" title="Deleted">
                          <p>{serverExplanation(presentation, server)}</p>
                          {server.deletedAt !== null && (
                            <p>
                              Deleted <UserTime value={server.deletedAt} />
                            </p>
                          )}
                        </StateNotice>
                      )}
                      {hasPrimaryIssue && (
                        <StateNotice
                          state={
                            presentation.state === "current"
                              ? "warning"
                              : presentation.state
                          }
                          title={presentation.label}
                        >
                          <p>{serverExplanation(presentation, server)}</p>
                          {presentation.href !== undefined &&
                            presentation.href !==
                              `#/servers/${server.id}?tab=status` && (
                              <a href={presentation.href}>
                                {presentation.action}
                              </a>
                            )}
                        </StateNotice>
                      )}
                      {saturated.map(([label, capacity]) => (
                        <StateNotice
                          key={label}
                          state="warning"
                          title={`${label} capacity is saturated`}
                        >
                          <p>
                            {capacity.inUse} of {capacity.limit} slots are in
                            use. New work cannot be admitted until capacity is
                            released.
                          </p>
                        </StateNotice>
                      ))}
                      {!hasPrimaryIssue && saturated.length === 0 && (
                        <p>No current issues require operator action.</p>
                      )}
                    </section>

                    <section
                      class="operator-status-section"
                      aria-labelledby="server-operational-title"
                      data-testid="server-status-operational"
                    >
                      <h3 id="server-operational-title">Operational state</h3>
                      <dl class="operator-status-grid">
                        <div>
                          <dt>Runtime</dt>
                          <dd>
                            <strong>{sentenceCase(server.runtimeState)}</strong>
                            <span>
                              Desired {sentenceCase(server.desiredState)}
                            </span>
                            <span>
                              {server.runtimeReason === null
                                ? "No runtime issue reported"
                                : `Reason: ${sentenceCase(server.runtimeReason)}`}
                            </span>
                          </dd>
                        </div>
                        <div>
                          <dt>Authentication</dt>
                          <dd>
                            <strong>
                              {sentenceCase(server.credentialState)}
                            </strong>
                            {presentation.href !==
                              `#/servers/${server.id}?tab=authentication` && (
                              <a
                                href={`#/servers/${server.id}?tab=authentication`}
                              >
                                Manage authentication
                              </a>
                            )}
                          </dd>
                        </div>
                        <div>
                          <dt>Catalog</dt>
                          <dd>
                            <strong>{sentenceCase(server.activeState)}</strong>
                            <span>
                              {server.activeToolCount} active ·{" "}
                              {server.durableToolCount} durable tools
                            </span>
                            <span>
                              Last successful refresh{" "}
                              <UserTime value={server.lastSuccessAt} />
                            </span>
                            <a href={`#/servers/${server.id}?tab=tools`}>
                              View tools
                            </a>
                          </dd>
                        </div>
                      </dl>
                    </section>

                    <section
                      class="operator-status-details"
                      aria-labelledby="server-technical-details-title"
                      data-testid="server-status-details"
                    >
                      <h3 id="server-technical-details-title">
                        Technical details
                      </h3>
                      <dl class="technical-details-grid">
                        <div>
                          <dt>Server ID</dt>
                          <dd>
                            <CopyableValue
                              value={server.id}
                              label="server ID"
                              testID="server-id"
                            />
                          </dd>
                        </div>
                        <div>
                          <dt>Namespace</dt>
                          <dd>{server.namespace}</dd>
                        </div>
                        <div>
                          <dt>Runtime ID</dt>
                          <dd>{server.runtimeID ?? "—"}</dd>
                        </div>
                        <div>
                          <dt>Desired revision</dt>
                          <dd>{server.desiredRevision}</dd>
                        </div>
                        <div>
                          <dt>Static credential revision</dt>
                          <dd>{server.staticRevision}</dd>
                        </div>
                        <div>
                          <dt>OAuth client revision</dt>
                          <dd>{server.oauthClientRevision}</dd>
                        </div>
                        <div>
                          <dt>OAuth token revision</dt>
                          <dd>{server.oauthTokensRevision}</dd>
                        </div>
                        <div>
                          <dt>Durable catalog revision</dt>
                          <dd>{server.durableRevision ?? "—"}</dd>
                        </div>
                        <div>
                          <dt>Active catalog revision</dt>
                          <dd>{server.activeRevision ?? "—"}</dd>
                        </div>
                        <div>
                          <dt>Created</dt>
                          <dd>
                            <UserTime value={server.createdAt} />
                          </dd>
                        </div>
                        <div>
                          <dt>Updated</dt>
                          <dd>
                            <UserTime value={server.updatedAt} />
                          </dd>
                        </div>
                      </dl>
                    </section>
                  </div>
                );
              })()}
          </ReadPanel>
        </section>
      </div>
    );
  if (otherTab !== null)
    return (
      <div class="domain-view">
        <ServerNavigation
          server={snapshot.server}
          serverID={otherTab[1]!}
          current={otherTab[2]!}
        />
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
