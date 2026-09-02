import { useEffect, useState } from "preact/hooks";
import type { PrincipalDirectory } from "./principals";
import {
  BinaryToggle,
  CollectionTable,
  InertJSON,
  sentenceCase,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { SessionClient } from "./session";
import { UserTime } from "./time";
import type {
  PanelSnapshot,
  ViewCoordinator,
  ViewReadContext,
  ViewSnapshot,
} from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
type JSONRecord = Record<string, unknown>;
function record(value: unknown, keys: readonly string[]): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  const result = value as JSONRecord;
  if (Object.keys(result).sort().join(",") !== [...keys].sort().join(","))
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
function id(value: unknown): string {
  const result = text(value);
  if (!gatewayID.test(result)) throw new Error("invalid response");
  return result;
}
function closed(value: unknown, values: readonly string[]): string {
  const result = text(value);
  if (!values.includes(result)) throw new Error("invalid response");
  return result;
}
function values(value: unknown): unknown[] {
  if (!Array.isArray(value)) throw new Error("invalid response");
  return value;
}
function cursor(value: unknown): string | null {
  const result = nullableText(value);
  if (result !== null && (result.length === 0 || result.length > 512))
    throw new Error("invalid response");
  return result;
}

export interface InvocationTargetView {
  kind: "downstream" | "gateway";
  serverID: string;
  toolID: string;
  upstreamName: string;
  descriptorRevision: string;
  descriptorFingerprint: string;
}
export interface InvocationAuthorizationView {
  decision: "allow" | "deny" | "block";
  revision: string;
  evaluatedAt: string;
  grantID: string | null;
}
export interface InvocationSummaryView {
  id: string;
  principalID: string;
  credentialID: string;
  credentialFingerprint: string;
  credentialRevision: string;
  admittedAt: string;
  admissionClass: string;
  requestedName: string | null;
  target: InvocationTargetView | null;
  authorization: InvocationAuthorizationView | null;
  outcome: string;
  basis: string;
  completedAt: string | null;
}
export interface InvocationPageView {
  items: InvocationSummaryView[];
  nextCursor: string | null;
}
interface InvocationItemView extends InvocationSummaryView {
  redactedArguments: unknown;
}

function decodeTarget(value: unknown): InvocationTargetView | null {
  if (value === null) return null;
  const target = record(value, [
    "kind",
    "server_id",
    "tool_id",
    "upstream_name",
    "descriptor_revision",
    "descriptor_fingerprint",
  ]);
  return {
    kind: closed(target.kind, [
      "downstream",
      "gateway",
    ]) as InvocationTargetView["kind"],
    serverID: id(target.server_id),
    toolID: id(target.tool_id),
    upstreamName: text(target.upstream_name),
    descriptorRevision: text(target.descriptor_revision),
    descriptorFingerprint: text(target.descriptor_fingerprint),
  };
}
function decodeAuthorization(
  value: unknown,
): InvocationAuthorizationView | null {
  if (value === null) return null;
  const authorization = record(value, [
    "decision",
    "revision",
    "evaluated_at",
    "grant_id",
  ]);
  const grantID = nullableText(authorization.grant_id);
  if (grantID !== null) id(grantID);
  return {
    decision: closed(authorization.decision, [
      "allow",
      "deny",
      "block",
    ]) as InvocationAuthorizationView["decision"],
    revision: text(authorization.revision),
    evaluatedAt: text(authorization.evaluated_at),
    grantID,
  };
}
function decodeSummary(
  value: unknown,
  item = false,
): InvocationSummaryView & { redactedArguments?: unknown } {
  const keys = [
    "id",
    "principal_id",
    "credential_id",
    "credential_fingerprint",
    "credential_revision",
    "admitted_at",
    "admission_class",
    "requested_name",
    "target",
    "authorization",
    "outcome",
  ];
  if (item) keys.push("redacted_arguments");
  const summary = record(value, keys);
  const outcome = record(summary.outcome, ["class", "basis", "completed_at"]);
  const result: InvocationSummaryView & { redactedArguments?: unknown } = {
    id: id(summary.id),
    principalID: id(summary.principal_id),
    credentialID: id(summary.credential_id),
    credentialFingerprint: text(summary.credential_fingerprint),
    credentialRevision: text(summary.credential_revision),
    admittedAt: text(summary.admitted_at),
    admissionClass: closed(summary.admission_class, [
      "invalid_params",
      "unknown_tool",
      "invalid_arguments",
      "authorization_unavailable",
      "evaluated",
    ]),
    requestedName: nullableText(summary.requested_name),
    target: decodeTarget(summary.target),
    authorization: decodeAuthorization(summary.authorization),
    outcome: closed(outcome.class, [
      "invalid_params",
      "unknown_tool",
      "invalid_arguments",
      "authorization_unavailable",
      "deny",
      "block",
      "prestart_failure",
      "succeeded",
      "downstream_failure",
      "outcome_unknown",
    ]),
    basis: closed(outcome.basis, [
      "admission",
      "policy",
      "terminal",
      "missing_terminal",
    ]),
    completedAt: nullableText(outcome.completed_at),
  };
  if (item) result.redactedArguments = summary.redacted_arguments;
  return result;
}
export function decodeInvocationPage(value: unknown): InvocationPageView {
  const page = record(value, ["items", "next_cursor"]);
  return {
    items: values(page.items).map((item) => decodeSummary(item)),
    nextCursor: cursor(page.next_cursor),
  };
}
function decodeInvocationItem(value: unknown): InvocationItemView {
  const decoded = decodeSummary(value, true);
  if (!("redactedArguments" in decoded)) throw new Error("invalid response");
  return decoded as InvocationItemView;
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
async function problemCode(
  response: Response,
  expectedStatus: number,
): Promise<string | undefined> {
  if (
    response.status !== expectedStatus ||
    response.headers.get("Content-Type") !== "application/problem+json"
  )
    return undefined;
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 64 * 1024) return undefined;
  try {
    const problem = record(JSON.parse(body) as unknown, [
      "status",
      "code",
      "title",
    ]);
    if (problem.status !== expectedStatus || text(problem.title).length === 0)
      return undefined;
    return text(problem.code);
  } catch {
    return undefined;
  }
}
function listPath(viewKey: string, nextCursor: string | null): string {
  const query = new URLSearchParams();
  query.set("limit", "50");
  const separator = viewKey.indexOf("?");
  if (separator !== -1) {
    const allowed = new Set([
      "principal_id",
      "server_id",
      "requested_name",
      "admission_class",
      "decision",
      "outcome",
    ]);
    for (const [key, value] of new URLSearchParams(
      viewKey.slice(separator + 1),
    ))
      if (allowed.has(key)) query.set(key, value);
  }
  if (nextCursor !== null) query.set("cursor", nextCursor);
  return `/api/v1/invocations?${query.toString()}`;
}

type ReadResult =
  | { kind: "list"; viewKey: string; page: InvocationPageView; append: boolean }
  | { kind: "item"; viewKey: string; item: InvocationItemView }
  | { kind: "missing"; viewKey: string };
export interface InvocationsSnapshot {
  viewKey: string;
  live: boolean;
  updatesAvailable: boolean;
  items: readonly InvocationSummaryView[];
  nextCursor: string | null;
  item: InvocationItemView | undefined;
  missing: boolean;
  loadingOlder: boolean;
}
type Listener = (snapshot: InvocationsSnapshot) => void;

export class InvocationsController {
  private readonly views: ViewCoordinator;
  private readonly listeners = new Set<Listener>();
  private value: InvocationsSnapshot = {
    viewKey: "",
    live: true,
    updatesAvailable: false,
    items: [],
    nextCursor: null,
    item: undefined,
    missing: false,
    loadingOlder: false,
  };
  private continuation: string | null = null;
  private continuationPending = false;
  constructor(session: SessionClient, views: ViewCoordinator) {
    this.views = views;
    views.registerPanel({
      id: "invocations",
      matches: (key) =>
        key === "#/invocations" ||
        key.startsWith("#/invocations?") ||
        /^#\/invocations\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(key),
      invalidations: ["invocations"],
      onInvalidation: () => {
        if (this.value.live) return true;
        if (!this.value.updatesAvailable) {
          this.value = { ...this.value, updatesAvailable: true };
          this.emit();
        }
        return false;
      },
      read: (context) => this.read(context),
      publish: (result) => this.publish(result),
    });
    session.registerProtectedState(() => {
      this.continuation = null;
      this.continuationPending = false;
      this.value = {
        viewKey: "",
        live: true,
        updatesAvailable: false,
        items: [],
        nextCursor: null,
        item: undefined,
        missing: false,
        loadingOlder: false,
      };
      this.emit();
    });
  }
  snapshot(): InvocationsSnapshot {
    return this.value;
  }
  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    listener(this.value);
    return () => this.listeners.delete(listener);
  }
  setLive(live: boolean): void {
    this.value = { ...this.value, live, updatesAvailable: false };
    this.emit();
    if (live) {
      this.continuation = null;
      void this.views.refreshPanel("invocations");
    }
  }
  refresh(): void {
    this.continuation = null;
    void this.views.refreshPanel("invocations");
  }
  async loadOlder(): Promise<void> {
    if (
      this.continuationPending ||
      this.value.nextCursor === null ||
      !this.value.viewKey.startsWith("#/invocations")
    )
      return;
    this.continuationPending = true;
    this.continuation = this.value.nextCursor;
    this.value = { ...this.value, loadingOlder: true };
    this.emit();
    try {
      await this.views.refreshPanel("invocations");
    } finally {
      this.continuationPending = false;
      this.value = { ...this.value, loadingOlder: false };
      this.emit();
    }
  }
  private async read(context: ViewReadContext): Promise<ReadResult> {
    if (context.viewKey !== this.value.viewKey) {
      this.continuation = null;
      this.value = {
        viewKey: context.viewKey,
        live: this.value.live,
        updatesAvailable: false,
        items: [],
        nextCursor: null,
        item: undefined,
        missing: false,
        loadingOlder: false,
      };
      this.emit();
    }
    const itemMatch = /^#\/invocations\/([0-7][0-9A-HJKMNP-TV-Z]{25})$/.exec(
      context.viewKey,
    );
    if (itemMatch !== null) {
      const response = await get(
        context,
        `/api/v1/invocations/${itemMatch[1]}`,
      );
      if ((await problemCode(response, 404)) === "not_found")
        return { kind: "missing", viewKey: context.viewKey };
      return {
        kind: "item",
        viewKey: context.viewKey,
        item: decodeInvocationItem(await json(response)),
      };
    }
    const continuation = this.continuation;
    this.continuation = null;
    let response = await get(context, listPath(context.viewKey, continuation));
    if (
      continuation !== null &&
      (await problemCode(response, 409)) === "stale_cursor"
    ) {
      response = await get(context, listPath(context.viewKey, null));
      return {
        kind: "list",
        viewKey: context.viewKey,
        page: decodeInvocationPage(await json(response)),
        append: false,
      };
    }
    return {
      kind: "list",
      viewKey: context.viewKey,
      page: decodeInvocationPage(await json(response)),
      append: continuation !== null,
    };
  }
  private publish(result: ReadResult): void {
    if (result.kind === "missing")
      this.value = {
        ...this.value,
        viewKey: result.viewKey,
        item: undefined,
        missing: true,
      };
    else if (result.kind === "item")
      this.value = {
        ...this.value,
        viewKey: result.viewKey,
        item: result.item,
        missing: false,
      };
    else if (!this.value.live && !(result.append && this.continuationPending))
      this.value = { ...this.value, updatesAvailable: true };
    else
      this.value = {
        ...this.value,
        viewKey: result.viewKey,
        items: result.append
          ? [...this.value.items, ...result.page.items]
          : result.page.items,
        nextCursor: result.page.nextCursor,
        item: undefined,
        missing: false,
        updatesAvailable: false,
      };
    this.emit();
  }
  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}

export function invocationTargetLabel(
  target: InvocationTargetView | null,
  requestedName?: string | null,
): string {
  if (requestedName !== undefined && requestedName !== null)
    return requestedName;
  if (target === null) return "Not resolved";
  return target.kind === "gateway"
    ? `mcp_gateway.${target.upstreamName}`
    : target.upstreamName;
}
function InvocationFacts({
  item,
  principalNames,
}: {
  item: InvocationSummaryView;
  principalNames: ReadonlyMap<string, string>;
}) {
  return (
    <dl class="fact-grid">
      <div>
        <dt>ID</dt>
        <dd>{item.id}</dd>
      </div>
      <div>
        <dt>Principal</dt>
        <dd>
          <a href={`#/principals/${item.principalID}`}>
            {principalNames.get(item.principalID) ?? item.principalID}
          </a>
        </dd>
      </div>
      <div>
        <dt>Tool</dt>
        <dd>
          {item.target?.kind === "downstream" ? (
            <a
              href={`#/servers/${item.target.serverID}/descriptors/${item.target.toolID}`}
            >
              {invocationTargetLabel(item.target, item.requestedName)}
            </a>
          ) : (
            invocationTargetLabel(item.target, item.requestedName)
          )}
        </dd>
      </div>
      <div>
        <dt>Authorization decision</dt>
        <dd>
          {item.authorization === null
            ? "Not evaluated"
            : sentenceCase(item.authorization.decision)}
        </dd>
      </div>
      {item.authorization?.grantID !== null &&
        item.authorization?.grantID !== undefined && (
          <div>
            <dt>Grant</dt>
            <dd>
              <a href={`#/grants/${item.authorization.grantID}`}>
                Grant {item.authorization.grantID}
              </a>
            </dd>
          </div>
        )}
      {item.target?.kind === "downstream" && (
        <div>
          <dt>Server</dt>
          <dd>
            <a href={`#/servers/${item.target.serverID}`}>
              Server {item.target.serverID}
            </a>
          </dd>
        </div>
      )}
      <div>
        <dt>Admitted</dt>
        <dd>
          <UserTime value={item.admittedAt} />
        </dd>
      </div>
      <div>
        <dt>Completed</dt>
        <dd>
          <UserTime
            value={item.completedAt}
            fallback="No terminal timestamp retained"
          />
        </dd>
      </div>
      <div>
        <dt>Outcome basis</dt>
        <dd>{sentenceCase(item.basis)}</dd>
      </div>
      <div>
        <dt>Credential</dt>
        <dd>
          {item.credentialID} · revision {item.credentialRevision} · fingerprint{" "}
          {item.credentialFingerprint}
        </dd>
      </div>
    </dl>
  );
}
export function Invocations({
  controller,
  principals,
  view,
}: {
  controller: InvocationsController;
  principals: PrincipalDirectory;
  view: ViewSnapshot;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  const [principalNames, setPrincipalNames] = useState(principals.snapshot());
  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  useEffect(() => principals.subscribe(setPrincipalNames), [principals]);
  const panel: PanelSnapshot | undefined = view.panels.invocations;
  const detail = /^#\/invocations\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(
    view.viewKey,
  );
  return (
    <div class="invocations-view" data-testid="invocations-view">
      {detail ? (
        <InvocationDetail
          snapshot={snapshot}
          panel={panel}
          principalNames={principalNames}
        />
      ) : (
        <InvocationList
          snapshot={snapshot}
          panel={panel}
          setLive={(live) => controller.setLive(live)}
          loadOlder={() => void controller.loadOlder()}
          principalNames={principalNames}
        />
      )}
    </div>
  );
}
function InvocationList({
  snapshot,
  panel,
  setLive,
  loadOlder,
  principalNames,
}: {
  snapshot: InvocationsSnapshot;
  panel: PanelSnapshot | undefined;
  setLive: (live: boolean) => void;
  loadOlder: () => void;
  principalNames: ReadonlyMap<string, string>;
}) {
  return (
    <section class="panel domain-panel" aria-label="Invocations">
      <div class="collection-toolbar live-collection-toolbar">
        <label for="invocation-live-mode">Live mode</label>
        <BinaryToggle
          attributes={{ id: "invocation-live-mode" }}
          checked={snapshot.live}
          enabledLabel="Live updates on"
          disabledLabel="Live updates paused"
          onChange={setLive}
        />
        {snapshot.updatesAvailable && (
          <StatusLabel state="warning">Updates available</StatusLabel>
        )}
      </div>
      {panel?.status === "error" && panel.hasValue !== true ? (
        <StateNotice state="error" title="Invocation list unavailable" />
      ) : snapshot.items.length === 0 ? (
        <StateNotice state="empty" title="No retained invocations match" />
      ) : (
        <CollectionTable
          caption="Invocation history"
          items={snapshot.items}
          rowKey={(item) => item.id}
          rowTestID="invocation-row"
          filters={[
            {
              key: "tool",
              label: "Tool",
              type: "text",
              value: (item) =>
                invocationTargetLabel(item.target, item.requestedName),
            },
            {
              key: "principal",
              label: "Principal",
              type: "text",
              value: (item) => principalNames.get(item.principalID) ?? "",
              literalValues: (item) => [item.principalID],
            },
            {
              key: "decision",
              label: "Decision",
              type: "select",
              value: (item) => item.authorization?.decision ?? "not_evaluated",
              options: [
                { value: "allow", label: "Allow" },
                { value: "deny", label: "Deny" },
                { value: "block", label: "Block" },
                { value: "not_evaluated", label: "Not evaluated" },
              ],
            },
            {
              key: "outcome",
              label: "Outcome",
              type: "select",
              value: (item) => item.outcome,
              options: [
                { value: "succeeded", label: "Succeeded" },
                { value: "downstream_failure", label: "Downstream failure" },
                { value: "outcome_unknown", label: "Outcome unknown" },
                { value: "deny", label: "Deny" },
                { value: "block", label: "Block" },
                { value: "invalid_params", label: "Invalid parameters" },
                { value: "unknown_tool", label: "Unknown tool" },
                { value: "invalid_arguments", label: "Invalid arguments" },
                {
                  value: "authorization_unavailable",
                  label: "Authorization unavailable",
                },
                { value: "prestart_failure", label: "Prestart failure" },
              ],
            },
          ]}
          hasMore={snapshot.nextCursor !== null}
          loadingMore={snapshot.loadingOlder}
          onLoadMore={loadOlder}
          loadMoreLabel="Load older invocations"
          columns={[
            {
              key: "invocation",
              label: "Invocation",
              render: (item) => (
                <a href={`#/invocations/${item.id}`}>{item.id}</a>
              ),
            },
            {
              key: "tool",
              label: "Tool",
              render: (item) =>
                invocationTargetLabel(item.target, item.requestedName),
            },
            {
              key: "principal",
              label: "Principal",
              render: (item) => (
                <a href={`#/principals/${item.principalID}`}>
                  {principalNames.get(item.principalID) ?? item.principalID}
                </a>
              ),
            },
            {
              key: "decision",
              label: "Decision",
              render: (item) =>
                item.authorization === null
                  ? "Not evaluated"
                  : sentenceCase(item.authorization.decision),
            },
            {
              key: "outcome",
              label: "Outcome",
              render: (item) => (
                <StatusLabel
                  state={
                    item.outcome === "succeeded"
                      ? "current"
                      : item.outcome === "outcome_unknown"
                        ? "warning"
                        : "empty"
                  }
                >
                  {sentenceCase(item.outcome)}
                </StatusLabel>
              ),
            },
            {
              key: "admitted",
              label: "Admitted",
              render: (item) => <UserTime value={item.admittedAt} />,
            },
          ]}
        />
      )}
    </section>
  );
}
function InvocationDetail({
  snapshot,
  panel,
  principalNames,
}: {
  snapshot: InvocationsSnapshot;
  panel: PanelSnapshot | undefined;
  principalNames: ReadonlyMap<string, string>;
}) {
  if (snapshot.missing)
    return (
      <StateNotice
        state="unavailable"
        title="Invocation evidence is no longer retained"
      >
        <p data-testid="invocation-missing">
          A missing or evicted item does not prove it never existed or never
          executed. It is not safe-to-retry evidence.
        </p>
      </StateNotice>
    );
  if (snapshot.item === undefined)
    return panel?.status === "error" ? (
      <StateNotice state="error" title="Invocation detail unavailable" />
    ) : (
      <StateNotice state="loading" title="Loading invocation detail" />
    );
  const item = snapshot.item;
  return (
    <div class="domain-view">
      <nav class="detail-navigation" aria-label="Invocation navigation">
        <a href="#/invocations">Back to invocations</a>
      </nav>
      <section
        class="panel domain-panel"
        data-testid="invocation-detail"
        aria-labelledby="invocation-detail-title"
      >
        <div class="panel-heading">
          <div>
            <h1 id="invocation-detail-title" tabindex={-1}>
              Invocation {item.id}
            </h1>
          </div>
          <StatusLabel
            state={item.outcome === "outcome_unknown" ? "warning" : "current"}
          >
            {sentenceCase(item.outcome)}
          </StatusLabel>
        </div>
        <InvocationFacts item={item} principalNames={principalNames} />
        {item.basis === "missing_terminal" && (
          <StateNotice state="warning" title="Audit completion is unknown">
            <p>
              Missing terminal evidence does not prove nonexecution and is not
              safe-to-retry evidence. Gateway does not automatically replay
              invocations; an explicit caller retry can duplicate an effect.
            </p>
            {item.target?.kind === "gateway" && (
              <p>
                This is a Gateway-owned local target. Missing terminal evidence
                is not proof of downstream handoff.
              </p>
            )}
          </StateNotice>
        )}
        <h3>Fixed-redacted arguments</h3>
        <InertJSON
          value={item.redactedArguments}
          label="Fixed-redacted invocation arguments"
        />
      </section>
    </div>
  );
}
