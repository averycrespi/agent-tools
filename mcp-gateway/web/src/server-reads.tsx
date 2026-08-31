import { useEffect, useState } from "preact/hooks";
import type { MutationCoordinator } from "./mutation";
import {
  CollectionTable,
  ComparisonTable,
  InertJSON,
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
import type { SensitiveSinkCoordinator } from "./sinks";
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
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=(?:activity|diagnostics)$/.exec(
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
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}(?:\?tab=(?:authentication|settings|diagnostics))?$/.test(
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
        /^#\/servers\/[0-7][0-9A-HJKMNP-TV-Z]{25}\?tab=(?:activity|diagnostics)$/.test(
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
      this.value = emptySnapshot(context.viewKey);
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
    const serverItem =
      /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})(?:\?tab=(?:authentication|settings|diagnostics))?$/.exec(
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
      kind === "operations" || kind === "authFlows"
        ? get(
            context,
            `/api/v1/servers/${context.viewKey.slice("#/servers/".length, "#/servers/".length + 26)}`,
          )
        : undefined;
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
    if (kind === "operations" || kind === "authFlows") {
      const serverResponse = await serverResponsePromise!;
      const server = decodeServer(await json(serverResponse));
      const etag = serverResponse.headers.get("ETag");
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
    ["tools", "Tools", `#/servers/${serverID}?tab=tools`],
    ["activity", "Activity", `#/servers/${serverID}?tab=activity`],
    [
      "authentication",
      "Authentication",
      `#/servers/${serverID}?tab=authentication`,
    ],
    ["settings", "Settings", `#/servers/${serverID}?tab=settings`],
    ["diagnostics", "Diagnostics", `#/servers/${serverID}?tab=diagnostics`],
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
  action: string;
  href: string;
}

function serverPresentation(server: ServerView): ServerPresentation {
  const root = `#/servers/${server.id}`;
  if (server.desiredState === "deleted")
    return {
      label: "Deleted",
      state: "unavailable",
      action: "View diagnostics",
      href: `${root}?tab=diagnostics`,
    };
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
      action: "Authorize",
      href: `${root}?tab=authentication`,
    };
  if (
    server.runtimeState === "activating" ||
    server.runtimeState === "retry_wait" ||
    server.credentialState === "refreshing"
  )
    return {
      label: "Connecting",
      state: "loading",
      action: "View",
      href: root,
    };
  if (
    server.runtimeState === "active" &&
    server.activeState === "current" &&
    server.credentialState !== "locked" &&
    server.credentialState !== "unavailable"
  )
    return {
      label: "Ready",
      state: "current",
      action: server.activeToolCount > 0 ? "View tools" : "View",
      href: server.activeToolCount > 0 ? `${root}?tab=tools` : root,
    };
  return {
    label: "Needs attention",
    state: "warning",
    action: "Diagnose",
    href: `${root}?tab=diagnostics`,
  };
}

function ServerRows({ items }: { items: readonly ServerView[] }) {
  return (
    <CollectionTable
      caption="Servers"
      items={items}
      rowKey={(server) => server.id}
      rowTestID="server-row"
      filterLabel="Filter servers"
      filterValue={(server) =>
        `${server.displayName} ${server.namespace} ${serverPresentation(server).label}`
      }
      emptyTitle="No servers match this filter"
      columns={[
        {
          key: "server",
          label: "Server",
          sortValue: (server) => server.displayName,
          render: (server) => (
            <>
              <a href={`#/servers/${server.id}`}>{server.displayName}</a>
              <span class="table-secondary">{server.namespace}</span>
            </>
          ),
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
          render: (server) => server.activeToolCount || "—",
        },
        {
          key: "action",
          label: "Action",
          class: "action-column",
          render: (server) => {
            const presentation = serverPresentation(server);
            return <a href={presentation.href}>{presentation.action}</a>;
          },
        },
      ]}
    />
  );
}
function OAuthDiagnostics({ flows }: { flows: readonly ServerAuthFlowView[] }) {
  const diagnostics = flows.filter((flow) => flow.diagnostic !== null);
  if (diagnostics.length === 0)
    return <StateNotice state="empty" title="No retained OAuth failures" />;
  return (
    <ComparisonTable caption="OAuth failures">
      <thead>
        <tr>
          <th scope="col">Time</th>
          <th scope="col">Stage</th>
          <th scope="col">Reason</th>
          <th scope="col">HTTP</th>
          <th scope="col">Correlation</th>
        </tr>
      </thead>
      <tbody>
        {diagnostics.map((flow) => (
          <tr key={flow.id}>
            <td>{flow.finishedAt ?? flow.createdAt}</td>
            <td>{flow.diagnostic!.stage.replaceAll("_", " ")}</td>
            <td>{flow.diagnostic!.reason.replaceAll("_", " ")}</td>
            <td>{flow.diagnostic!.httpStatus ?? "—"}</td>
            <td>
              <a href={`#/servers/${flow.serverID}/auth-flows/${flow.id}`}>
                {flow.diagnostic!.correlationID}
              </a>
            </td>
          </tr>
        ))}
      </tbody>
    </ComparisonTable>
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
    <ComparisonTable caption="Available tools">
      <thead>
        <tr>
          <th scope="col">Tool</th>
          <th scope="col">Server</th>
          <th scope="col">Status</th>
        </tr>
      </thead>
      <tbody>
        {items.map((descriptor) => (
          <tr data-testid="catalog-row" key={descriptor.id}>
            <th scope="row">
              <a
                href={`#/servers/${descriptor.serverID}/descriptors/${descriptor.id}`}
                data-tool-name={descriptor.upstreamName}
              >
                {descriptor.upstreamName}
              </a>
              <span class="table-secondary">{descriptor.externalName}</span>
            </th>
            <td>
              <a href={`#/servers/${descriptor.serverID}`}>
                {descriptor.serverID}
              </a>
            </td>
            <td>
              <StatusLabel state={degraded ? "warning" : "current"}>
                {degraded ? "Catalog issue" : "Available"}
              </StatusLabel>
            </td>
          </tr>
        ))}
      </tbody>
    </ComparisonTable>
  );
}
function DescriptorRows({ items }: { items: readonly DescriptorView[] }) {
  return (
    <ComparisonTable caption="Server tools">
      <thead>
        <tr>
          <th scope="col">Tool</th>
          <th scope="col">Status</th>
          <th scope="col">Last seen</th>
        </tr>
      </thead>
      <tbody>
        {items.map((descriptor) => (
          <tr data-testid="descriptor-row" key={descriptor.id}>
            <th scope="row">
              <a
                href={`#/servers/${descriptor.serverID}/descriptors/${descriptor.id}`}
                data-tool-name={descriptor.upstreamName}
              >
                {descriptor.upstreamName}
              </a>
              <span class="table-secondary">{descriptor.externalName}</span>
            </th>
            <td>
              <StatusLabel
                state={
                  descriptor.retiredAt === null ? "current" : "unavailable"
                }
              >
                {descriptor.retiredAt === null ? "Available" : "Retired"}
              </StatusLabel>
            </td>
            <td>{descriptor.lastSeenAt}</td>
          </tr>
        ))}
      </tbody>
    </ComparisonTable>
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
  const diagnosticsTab =
    /^#\/servers\/([0-7][0-9A-HJKMNP-TV-Z]{25})\?tab=diagnostics$/.exec(
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
        <div class="collection-toolbar">
          <a
            class="button-link primary-action"
            href="#/servers/new"
            data-testid="server-create-link"
          >
            Create server
          </a>
        </div>
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
      </div>
    );
  if (authenticationTab !== null)
    return (
      <div class="domain-view" data-testid="server-authentication-view">
        <ServerTabs serverID={authenticationTab[1]!} current="authentication" />
        <ReadPanel panel={overviewPanel}>
          {snapshot.server !== undefined &&
            snapshot.serverETag !== undefined && (
              <>
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
                <ServerCredentials
                  mutations={mutations}
                  sinks={sinks}
                  server={snapshot.server}
                  etag={snapshot.serverETag}
                  readVersion={snapshot.readVersion}
                  onRefresh={onRefresh}
                />
              </>
            )}
        </ReadPanel>
      </div>
    );
  if (authFlowItem !== null) {
    const serverID = authFlowItem[1]!;
    return (
      <div class="domain-view" data-testid="server-auth-flows-view">
        <ServerTabs serverID={serverID} current="activity" />
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
        <ServerTabs serverID={activityTab[1]!} current="activity" />
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
  if (operationItem !== null) {
    const serverID = operationItem[1]!;
    return (
      <div class="domain-view" data-testid="server-operations-view">
        <ServerTabs serverID={serverID} current="activity" />
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
        <ServerTabs serverID={descriptorItem[1]!} current="tools" />
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
        <ServerTabs serverID={descriptorList[1]!} current="tools" />
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
        <ServerTabs serverID={settingsTab[1]!} current="settings" />
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
  if (diagnosticsTab !== null)
    return (
      <div class="domain-view" data-testid="server-diagnostics-view">
        <ServerTabs serverID={diagnosticsTab[1]!} current="diagnostics" />
        <section
          class="panel domain-panel"
          aria-labelledby="server-diagnostics-title"
        >
          <div class="panel-heading">
            <h2 id="server-diagnostics-title">Server diagnostics</h2>
          </div>
          <ReadPanel panel={panel}>
            {snapshot.server !== undefined && (
              <ComparisonTable caption="Internal server state">
                <tbody>
                  <tr>
                    <th scope="row">Desired state</th>
                    <td>{snapshot.server.desiredState}</td>
                  </tr>
                  <tr>
                    <th scope="row">Desired revision</th>
                    <td>{snapshot.server.desiredRevision}</td>
                  </tr>
                  <tr>
                    <th scope="row">Runtime</th>
                    <td>{snapshot.server.runtimeState}</td>
                  </tr>
                  <tr>
                    <th scope="row">Runtime reason</th>
                    <td>{snapshot.server.runtimeReason ?? "—"}</td>
                  </tr>
                  <tr>
                    <th scope="row">Runtime identity</th>
                    <td>{snapshot.server.runtimeID ?? "—"}</td>
                  </tr>
                  <tr>
                    <th scope="row">Credential revisions</th>
                    <td>
                      Static {snapshot.server.staticRevision}; OAuth client{" "}
                      {snapshot.server.oauthClientRevision}; tokens{" "}
                      {snapshot.server.oauthTokensRevision}
                    </td>
                  </tr>
                  <tr>
                    <th scope="row">Catalog</th>
                    <td>
                      Durable {snapshot.server.durableState} revision{" "}
                      {snapshot.server.durableRevision ?? "—"}; active{" "}
                      {snapshot.server.activeState} revision{" "}
                      {snapshot.server.activeRevision ?? "—"}
                    </td>
                  </tr>
                  <tr>
                    <th scope="row">Last catalog success</th>
                    <td>{snapshot.server.lastSuccessAt ?? "—"}</td>
                  </tr>
                  <tr>
                    <th scope="row">Deleted at</th>
                    <td>{snapshot.server.deletedAt ?? "—"}</td>
                  </tr>
                </tbody>
              </ComparisonTable>
            )}
          </ReadPanel>
        </section>
        <section
          class="panel domain-panel"
          aria-labelledby="oauth-diagnostics-title"
        >
          <div class="panel-heading">
            <h2 id="oauth-diagnostics-title">OAuth failures</h2>
          </div>
          <ReadPanel panel={authFlowPanel}>
            <OAuthDiagnostics flows={snapshot.authFlows} />
          </ReadPanel>
        </section>
      </div>
    );
  if (serverItem !== null)
    return (
      <div class="domain-view" data-testid="server-detail">
        <ServerTabs serverID={serverItem[1]!} current="overview" />
        <section
          class="panel domain-panel server-summary"
          aria-labelledby="server-detail-title"
        >
          <ReadPanel panel={panel}>
            {snapshot.server !== undefined &&
              (() => {
                const presentation = serverPresentation(snapshot.server);
                return (
                  <>
                    <div class="panel-heading">
                      <div>
                        <h2 id="server-detail-title">
                          {snapshot.server.displayName}
                        </h2>
                        <span class="table-secondary">
                          {snapshot.server.namespace}
                        </span>
                      </div>
                      <StatusLabel state={presentation.state}>
                        {presentation.label}
                      </StatusLabel>
                    </div>
                    <p>
                      {presentation.label === "Authorization required"
                        ? "Connect this server to continue."
                        : presentation.label === "Ready"
                          ? `${snapshot.server.activeToolCount} available ${snapshot.server.activeToolCount === 1 ? "tool" : "tools"}.`
                          : presentation.label === "Disabled"
                            ? "This server will not connect until it is enabled."
                            : "Review diagnostics for the latest internal state."}
                    </p>
                    <a
                      class="button-link primary-action"
                      href={presentation.href}
                    >
                      {presentation.action}
                    </a>
                  </>
                );
              })()}
          </ReadPanel>
        </section>
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
