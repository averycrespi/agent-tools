import { useEffect, useLayoutEffect, useState } from "preact/hooks";
import { parseFragment, serializeLocation } from "./location";
import { invocationOptions } from "./invocation-query";
import type { PrincipalDirectory } from "./principals";
import {
  BinaryToggle,
  CollectionTable,
  TableIdentity,
  useDebouncedInput,
  InertJSON,
  sentenceCase,
  StateNotice,
  StatusLabel,
  type OperationalState,
} from "./primitives";
import type { SessionClient } from "./session";
import { UserTime } from "./time";
import type {
  PanelSnapshot,
  ViewCoordinator,
  ViewReadContext,
  ViewSnapshot,
} from "./view";

function invocationState(outcome: string): OperationalState {
  if (outcome === "succeeded") return "current";
  if (outcome === "outcome_unknown") return "warning";
  if (outcome === "deny" || outcome === "block") return "neutral";
  return "error";
}

function AuthorizationDecision({
  decision,
}: {
  decision: InvocationAuthorizationView["decision"] | undefined;
}) {
  return (
    <StatusLabel state={decision === "allow" ? "current" : "neutral"}>
      {decision === undefined ? "Not evaluated" : sentenceCase(decision)}
    </StatusLabel>
  );
}

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
function listPath(
  filters: Readonly<Record<string, string>>,
  nextCursor: string | null,
): string {
  const query = new URLSearchParams({ limit: "50" });
  for (const [key, value] of Object.entries(filters))
    query.set(key.slice(7), value);
  if (filters.filter_tool || filters.filter_principal)
    query.set("search_locale", Intl.DateTimeFormat().resolvedOptions().locale);
  if (nextCursor !== null) query.set("cursor", nextCursor);
  return `/api/v1/invocations?${query.toString()}`;
}

type ReadResult =
  | {
      kind: "list";
      viewKey: string;
      page: InvocationPageView;
      append: boolean;
      automatic: boolean;
      serial: number;
    }
  | {
      kind: "failure";
      viewKey: string;
      append: boolean;
      automatic: boolean;
      serial: number;
    }
  | { kind: "item"; viewKey: string; item: InvocationItemView }
  | { kind: "missing"; viewKey: string };
export interface InvocationsSnapshot {
  viewKey: string;
  live: boolean;
  paused: boolean;
  successful: boolean;
  refreshError: boolean;
  olderError: boolean;
  notice: string | undefined;
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
    paused: false,
    successful: false,
    refreshError: false,
    olderError: false,
    notice: undefined,
    updatesAvailable: false,
    items: [],
    nextCursor: null,
    item: undefined,
    missing: false,
    loadingOlder: false,
  };
  private continuation: string | null = null;
  private continuationPending = false;
  private serial = 0;
  private continuationSerial = 0;
  constructor(session: SessionClient, views: ViewCoordinator) {
    this.views = views;
    views.registerPanel({
      id: "invocations",
      matches: (key) => parseFragment(key)?.destination === "invocations",
      invalidations: ["invocations", "authorization"],
      shouldRefresh: (reason) => {
        const automatic = ["invalidation", "reconnect", "poll"].includes(
          reason,
        );
        return (
          !automatic ||
          parseFragment(this.views.snapshot().viewKey)?.segments.length === 2 ||
          (this.value.live && !this.value.paused)
        );
      },
      onInvalidation: () => {
        if (this.value.live && !this.value.paused) return true;
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
      this.serial += 1;
      this.continuationSerial += 1;
      this.value = {
        viewKey: "",
        live: true,
        paused: false,
        successful: false,
        refreshError: false,
        olderError: false,
        notice: undefined,
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
    this.serial += 1;
    this.value = { ...this.value, live };
    this.emit();
    if (live && !this.value.paused) {
      this.continuation = null;
      void this.views.refreshPanel("invocations");
    }
  }
  resume(): void {
    this.serial += 1;
    this.value = { ...this.value, paused: false };
    this.emit();
    this.refresh();
  }
  refresh(): void {
    this.continuation = null;
    void this.views.refreshPanel("invocations");
  }
  async loadOlder(): Promise<void> {
    if (
      this.continuationPending ||
      this.value.nextCursor === null ||
      this.views.snapshot().viewKey !== this.value.viewKey ||
      this.views.snapshot().panels.invocations?.refreshing === true
    )
      return;
    this.serial += 1;
    const continuationSerial = ++this.continuationSerial;
    this.continuationPending = true;
    this.continuation = this.value.nextCursor;
    this.value = {
      ...this.value,
      paused: true,
      loadingOlder: true,
      olderError: false,
    };
    this.emit();
    try {
      await this.views.refreshPanel("invocations");
    } finally {
      if (continuationSerial === this.continuationSerial) {
        this.continuationPending = false;
        this.value = { ...this.value, loadingOlder: false };
        this.emit();
      }
    }
  }
  private async read(context: ViewReadContext): Promise<ReadResult> {
    const location = parseFragment(context.viewKey);
    if (location?.destination !== "invocations")
      throw new Error("Invalid invocation location");
    if (context.viewKey !== this.value.viewKey) {
      const returning =
        parseFragment(this.value.viewKey)?.segments.length === 2 &&
        location.segments.length === 1;
      this.serial += 1;
      this.continuationSerial += 1;
      this.continuationPending = false;
      this.continuation = null;
      this.value = {
        viewKey: context.viewKey,
        live: this.value.live,
        paused: this.value.paused,
        successful: false,
        refreshError: false,
        olderError: false,
        notice: returning
          ? "Returned to the newest matching invocations; the previous traversal was discarded."
          : undefined,
        updatesAvailable: false,
        items: [],
        nextCursor: null,
        item: undefined,
        missing: false,
        loadingOlder: false,
      };
      this.emit();
    }
    const itemID = location.segments[1];
    if (itemID !== undefined) {
      const response = await get(context, `/api/v1/invocations/${itemID}`);
      if ((await problemCode(response, 404)) === "not_found")
        return { kind: "missing", viewKey: context.viewKey };
      return {
        kind: "item",
        viewKey: context.viewKey,
        item: decodeInvocationItem(await json(response)),
      };
    }
    const continuation = context.reason === "panel" ? this.continuation : null;
    this.continuation = null;
    const serial = this.serial;
    const automatic = ["invalidation", "reconnect", "poll"].includes(
      context.reason ?? "navigation",
    );
    let append = continuation !== null;
    try {
      let response = await get(context, listPath(location.query, continuation));
      if (append && (await problemCode(response, 409)) === "stale_cursor") {
        if (context.signal.aborted) throw new Error("Superseded");
        append = false;
        this.value = {
          ...this.value,
          items: [],
          nextCursor: null,
          successful: false,
          notice:
            "The previous traversal expired. Restarted at the newest matching invocations.",
        };
        this.emit();
        response = await get(context, listPath(location.query, null));
      }
      return {
        kind: "list",
        viewKey: context.viewKey,
        page: decodeInvocationPage(await json(response)),
        append,
        automatic,
        serial,
      };
    } catch (error) {
      if (context.signal.aborted) throw error;
      return {
        kind: "failure",
        viewKey: context.viewKey,
        append,
        automatic,
        serial,
      };
    }
  }
  private publish(result: ReadResult): void {
    if (
      (result.kind === "list" || result.kind === "failure") &&
      result.automatic &&
      (result.serial !== this.serial || !this.value.live || this.value.paused)
    )
      return;
    if (result.kind === "failure") {
      this.value = {
        ...this.value,
        loadingOlder: false,
        olderError: result.append,
        refreshError: !result.append,
      };
      this.emit();
      return;
    }
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
    else
      this.value = {
        ...this.value,
        viewKey: result.viewKey,
        items: result.append
          ? [...this.value.items, ...result.page.items]
          : result.page.items,
        nextCursor: result.page.nextCursor,
        successful: true,
        refreshError: false,
        olderError: false,
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
        <dt>Invocation ID</dt>
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
          <AuthorizationDecision decision={item.authorization?.decision} />
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
  navigate,
}: {
  controller: InvocationsController;
  principals: PrincipalDirectory;
  view: ViewSnapshot;
  navigate: (fragment: string) => void;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  const [principalNames, setPrincipalNames] = useState(principals.snapshot());
  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  useEffect(() => principals.subscribe(setPrincipalNames), [principals]);
  const panel: PanelSnapshot | undefined = view.panels.invocations;
  const location = parseFragment(view.viewKey);
  const detail = location?.segments.length === 2;
  const query = location?.query ?? {};
  const listLink = serializeLocation({
    destination: "invocations",
    segments: ["invocations"],
    query,
  });
  const current =
    snapshot.viewKey === view.viewKey
      ? snapshot
      : {
          ...snapshot,
          items: [],
          item: undefined,
          successful: false,
          missing: false,
          refreshError: false,
          olderError: false,
          notice: undefined,
          nextCursor: null,
        };
  if (detail)
    return (
      <>
        <nav class="detail-navigation" aria-label="Invocation navigation">
          <a href={listLink}>Back to invocations</a>
        </nav>
        <InvocationDetail
          snapshot={current}
          panel={panel}
          principalNames={principalNames}
        />
      </>
    );
  return (
    <div class="invocations-view" data-testid="invocations-view">
      <InvocationList
        snapshot={current}
        panel={panel}
        query={query}
        navigate={navigate}
        resume={() => controller.resume()}
        refresh={() => controller.refresh()}
        setLive={(live) => controller.setLive(live)}
        loadOlder={() => void controller.loadOlder()}
        principalNames={principalNames}
      />
    </div>
  );
}
function InvocationList({
  snapshot,
  panel,
  setLive,
  loadOlder,
  principalNames,
  query,
  navigate,
  resume,
  refresh,
}: {
  query: Readonly<Record<string, string>>;
  navigate: (fragment: string) => void;
  resume: () => void;
  refresh: () => void;
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
          showState={false}
          onChange={setLive}
        />
        {snapshot.updatesAvailable && (
          <StatusLabel state="warning">Updates available</StatusLabel>
        )}
      </div>
      <InvocationFilters query={query} navigate={navigate} />
      {snapshot.paused && (
        <div class="inline-actions">
          {snapshot.live && (
            <StatusLabel state="warning">
              Live paused while viewing older results
            </StatusLabel>
          )}
          <button type="button" onClick={resume}>
            {snapshot.live ? "Resume live" : "Return to newest"}
          </button>
        </div>
      )}
      {snapshot.notice && (
        <StateNotice state="warning" title={snapshot.notice} />
      )}
      {snapshot.refreshError && (
        <StateNotice
          state="error"
          title={
            snapshot.successful
              ? "Refresh failed; shown results are stale"
              : "Invocation list unavailable"
          }
        >
          <button type="button" onClick={refresh}>
            Retry refresh
          </button>
        </StateNotice>
      )}
      {!snapshot.successful && !snapshot.refreshError ? (
        <StateNotice state="loading" title="Loading invocations" />
      ) : snapshot.items.length === 0 ? (
        snapshot.successful &&
        !snapshot.refreshError && (
          <StateNotice
            state="empty"
            title={
              Object.keys(query).length
                ? "No matching invocations"
                : "No retained invocations"
            }
          />
        )
      ) : (
        <CollectionTable
          caption="Invocation history"
          layout="activity"
          rowHeaderKey="tool"
          items={snapshot.items}
          rowKey={(item) => item.id}
          rowTestID="invocation-row"
          columns={[
            {
              key: "admitted",
              label: "Admitted",
              role: "time",
              render: (item) => <UserTime value={item.admittedAt} />,
            },
            {
              key: "tool",
              label: "Invocation",
              role: "identity",
              render: (item) => (
                <TableIdentity
                  primary={
                    item.target?.kind === "downstream" ? (
                      <a
                        href={`#/servers/${item.target.serverID}/descriptors/${item.target.toolID}`}
                      >
                        {invocationTargetLabel(item.target, item.requestedName)}
                      </a>
                    ) : (
                      invocationTargetLabel(item.target, item.requestedName)
                    )
                  }
                  secondary={
                    <a
                      aria-label={`Invocation ${item.id}`}
                      href={serializeLocation({
                        destination: "invocations",
                        segments: ["invocations", item.id],
                        query,
                      })}
                    >
                      {item.id}
                    </a>
                  }
                />
              ),
            },
            {
              key: "principal",
              label: "Principal",
              role: "relation",
              render: (item) => (
                <a href={`#/principals/${item.principalID}`}>
                  {principalNames.get(item.principalID) ?? item.principalID}
                </a>
              ),
            },
            {
              key: "decision",
              label: "Authorization",
              role: "status",
              render: (item) => (
                <AuthorizationDecision
                  decision={item.authorization?.decision}
                />
              ),
            },
            {
              key: "outcome",
              label: "Outcome",
              role: "status",
              render: (item) => (
                <StatusLabel state={invocationState(item.outcome)}>
                  {sentenceCase(item.outcome)}
                </StatusLabel>
              ),
            },
          ]}
        />
      )}
      {snapshot.successful && (
        <output class="table-filter-summary" aria-live="polite">
          {snapshot.items.length} {Object.keys(query).length ? "matching " : ""}
          {snapshot.items.length === 1 ? "invocation" : "invocations"} loaded
          {snapshot.refreshError ? " (stale)" : ""}
        </output>
      )}
      {snapshot.nextCursor !== null && (
        <div class="inline-actions">
          <button
            type="button"
            onClick={loadOlder}
            disabled={snapshot.loadingOlder || panel?.refreshing === true}
          >
            Load older invocations
          </button>
          {snapshot.olderError && (
            <span role="alert">
              Older results unavailable. Loaded rows were retained; use Load
              older invocations to retry.
            </span>
          )}
        </div>
      )}
    </section>
  );
}
function InvocationTextFilter({
  name,
  value,
  change,
}: {
  name: string;
  value: string;
  change: (value: string) => void;
}) {
  const [draft, setDraft] = useState(value);
  useLayoutEffect(() => setDraft(value), [value]);
  useDebouncedInput(draft, (next) => {
    if (next !== value) change(next);
  });
  return (
    <input
      type="search"
      aria-label={name}
      placeholder={`${name}…`}
      value={draft}
      onInput={(event) => setDraft(event.currentTarget.value)}
    />
  );
}
function InvocationFilters({
  query,
  navigate,
}: {
  query: Readonly<Record<string, string>>;
  navigate: (fragment: string) => void;
}) {
  const [error, setError] = useState<string>();
  const [reset, setReset] = useState(0);
  const apply = (next: Record<string, string>) => {
    const fragment = serializeLocation({
      destination: "invocations",
      segments: ["invocations"],
      query: next,
    });
    if (!parseFragment(fragment)) {
      setError(
        "Filters must fit 256 UTF-8 bytes each and contain no control characters.",
      );
      return;
    }
    setError(undefined);
    navigate(fragment);
  };
  const change = (key: string, value: string) => {
    const next = { ...query };
    if (value.trim() === "") delete next[`filter_${key}`];
    else next[`filter_${key}`] = value;
    apply(next);
  };
  return (
    <>
      <div
        class="table-filters collection-query-filters"
        role="group"
        aria-label="Invocation history filters"
      >
        {["tool", "principal"].map((key) => (
          <InvocationTextFilter
            key={`${key}:${reset}`}
            name={sentenceCase(key)}
            value={query[`filter_${key}`] ?? ""}
            change={(value) => change(key, value)}
          />
        ))}
        {(["decision", "outcome"] as const).map((key) => (
          <select
            key={key}
            aria-label={key === "decision" ? "Authorization" : "Outcome"}
            value={query[`filter_${key}`] ?? ""}
            onChange={(event) => change(key, event.currentTarget.value)}
          >
            <option value="">
              {key === "decision" ? "Authorization" : "Outcome"}: any
            </option>
            {invocationOptions[key].map(([value, label]) => (
              <option value={value}>{label}</option>
            ))}
          </select>
        ))}
        <button
          type="button"
          onClick={() => {
            setReset(reset + 1);
            apply({});
          }}
        >
          Clear filters
        </button>
      </div>
      {error && <StateNotice state="error" title={error} />}
    </>
  );
}
function RetainedArgumentCapture({ value }: { value: unknown }) {
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="invocation-argument-capture-title"
      data-testid="invocation-argument-capture"
    >
      <div class="panel-heading">
        <h2 id="invocation-argument-capture-title">
          Retained argument capture
        </h2>
      </div>
      <p>
        Gateway redacts values only for recognized sensitive field names. Other
        secrets may remain visible; inspect this capture only when operationally
        necessary.
      </p>
      {value === null ? (
        <p>No argument capture was retained.</p>
      ) : value === "[TRUNCATED]" ? (
        <p>
          The redacted capture exceeded 8 KiB; argument content was not
          retained.
        </p>
      ) : (
        <InertJSON value={value} label="Retained invocation argument capture" />
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
    <div class="domain-view" data-testid="invocation-detail">
      <header class="detail-context" data-testid="detail-context">
        <div class="detail-context-heading">
          <h1 id="invocation-page-title" tabindex={-1}>
            Invocation {item.id}
          </h1>
        </div>
      </header>
      <section
        class="panel domain-panel"
        aria-labelledby="invocation-detail-title"
      >
        <div class="panel-heading">
          <h2 id="invocation-detail-title">Invocation details</h2>
          <StatusLabel state={invocationState(item.outcome)}>
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
      </section>
      <RetainedArgumentCapture value={item.redactedArguments} />
    </div>
  );
}
