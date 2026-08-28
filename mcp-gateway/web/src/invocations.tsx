import { useEffect, useState } from "preact/hooks";
import {
  ComparisonTable,
  InertJSON,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { SessionClient } from "./session";
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
function listPath(
  viewKey: string,
  requestedName: string,
  nextCursor: string | null,
): string {
  const query = new URLSearchParams();
  query.set("limit", "50");
  const separator = viewKey.indexOf("?");
  if (separator !== -1)
    for (const member of viewKey.slice(separator + 1).split("&")) {
      const index = member.indexOf("=");
      query.set(member.slice(0, index), member.slice(index + 1));
    }
  if (requestedName !== "") query.set("requested_name", requestedName);
  if (nextCursor !== null) query.set("cursor", nextCursor);
  return `/api/v1/invocations?${query.toString()}`;
}

type ReadResult =
  | { kind: "list"; viewKey: string; page: InvocationPageView; append: boolean }
  | { kind: "item"; viewKey: string; item: InvocationItemView }
  | { kind: "missing"; viewKey: string };
export interface InvocationsSnapshot {
  viewKey: string;
  requestedName: string;
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
    requestedName: "",
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
      invalidations: [],
      pollMilliseconds: 5000,
      read: (context) => this.read(context),
      publish: (result) => this.publish(result),
    });
    session.registerProtectedState(() => {
      this.continuation = null;
      this.continuationPending = false;
      this.value = {
        viewKey: "",
        requestedName: "",
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
  setRequestedName(value: string): void {
    if (value.length > 128 || /[^\x20-\x7e]/.test(value)) return;
    this.value = { ...this.value, requestedName: value };
    this.continuation = null;
    this.emit();
    void this.views.refreshPanel("invocations");
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
        requestedName: "",
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
    let response = await get(
      context,
      listPath(context.viewKey, this.value.requestedName, continuation),
    );
    if (
      continuation !== null &&
      (await problemCode(response, 409)) === "stale_cursor"
    ) {
      response = await get(
        context,
        listPath(context.viewKey, this.value.requestedName, null),
      );
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
      };
    this.emit();
  }
  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}

function targetLabel(target: InvocationTargetView | null): string {
  if (target === null) return "Not resolved";
  return target.kind === "gateway"
    ? `gateway:${target.upstreamName}`
    : `${target.serverID}:${target.upstreamName}`;
}
function Identities({ item }: { item: InvocationSummaryView }) {
  return (
    <details>
      <summary>Recorded identities</summary>
      <dl class="audit-identities">
        <dt>Recorded principal</dt>
        <dd>{item.principalID}</dd>
        <dt>Recorded credential</dt>
        <dd>
          {item.credentialID} · revision {item.credentialRevision} · fingerprint{" "}
          {item.credentialFingerprint}
        </dd>
        <dt>Recorded target</dt>
        <dd>{targetLabel(item.target)}</dd>
        {item.authorization !== null && (
          <>
            <dt>Recorded authorization</dt>
            <dd>
              {item.authorization.decision} · revision{" "}
              {item.authorization.revision}
              {item.authorization.grantID === null
                ? ""
                : ` · grant ${item.authorization.grantID}`}
            </dd>
          </>
        )}
      </dl>
    </details>
  );
}
function RetentionNotice() {
  return (
    <StateNotice state="warning" title="Bounded invocation evidence">
      <p>
        Invocation history is a bounded recent window of at most 4,096 rows.
        FIFO eviction has no age guarantee; a missing or evicted item does not
        prove it never existed or never executed.
      </p>
    </StateNotice>
  );
}

export function Invocations({
  controller,
  view,
}: {
  controller: InvocationsController;
  view: ViewSnapshot;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  const [requestedName, setRequestedName] = useState(snapshot.requestedName);
  useEffect(
    () =>
      controller.subscribe((next) => {
        setSnapshot(next);
        setRequestedName(next.requestedName);
      }),
    [controller],
  );
  const panel: PanelSnapshot | undefined = view.panels.invocations;
  const detail = /^#\/invocations\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(
    view.viewKey,
  );
  return (
    <div class="invocations-view" data-testid="invocations-view">
      <div class="refresh-controls">
        <StatusLabel
          state={
            panel?.status === "error" ? "error" : (panel?.status ?? "loading")
          }
        >
          Data {panel?.status ?? "loading"}
        </StatusLabel>
        <button
          data-testid="manual-refresh"
          type="button"
          onClick={() => controller.refresh()}
        >
          Refresh visible data
        </button>
      </div>
      <RetentionNotice />
      {detail ? (
        <InvocationDetail snapshot={snapshot} panel={panel} />
      ) : (
        <InvocationList
          snapshot={snapshot}
          panel={panel}
          requestedName={requestedName}
          setRequestedName={setRequestedName}
          apply={() => controller.setRequestedName(requestedName)}
          loadOlder={() => void controller.loadOlder()}
        />
      )}
    </div>
  );
}
function InvocationList({
  snapshot,
  panel,
  requestedName,
  setRequestedName,
  apply,
  loadOlder,
}: {
  snapshot: InvocationsSnapshot;
  panel: PanelSnapshot | undefined;
  requestedName: string;
  setRequestedName: (value: string) => void;
  apply: () => void;
  loadOlder: () => void;
}) {
  return (
    <section class="panel domain-panel" aria-labelledby="invocation-list-title">
      <div class="panel-heading">
        <div>
          <span class="panel-code">AUDIT-02</span>
          <h2 id="invocation-list-title">Invocation evidence</h2>
        </div>
        <span class="classification">NEWEST FIRST</span>
      </div>
      <p>
        Filtered pages are independently coherent. Outcome changes can move
        records into or out of later pages; refresh starts a new traversal and
        traversals are never merged.
      </p>
      <form
        class="inline-filter"
        onSubmit={(event) => {
          event.preventDefault();
          apply();
        }}
      >
        <label for="requested-name-filter">Requested name (live only)</label>
        <input
          id="requested-name-filter"
          data-testid="requested-name-filter"
          value={requestedName}
          maxlength={128}
          onInput={(event) => setRequestedName(event.currentTarget.value)}
        />
        <button data-testid="apply-requested-name" type="submit">
          Apply live filter
        </button>
      </form>
      {panel?.status === "error" && panel.hasValue !== true ? (
        <StateNotice state="error" title="Invocation list unavailable" />
      ) : snapshot.items.length === 0 ? (
        <StateNotice state="empty" title="No retained invocations match" />
      ) : (
        <div class="invocation-records">
          {snapshot.items.map((item) => (
            <article
              class="audit-record"
              data-testid="invocation-row"
              key={item.id}
            >
              <div class="audit-record-heading">
                <a href={`#/invocations/${item.id}`}>
                  {item.requestedName ?? item.id}
                </a>
                <StatusLabel
                  state={
                    item.outcome === "outcome_unknown" ? "warning" : "current"
                  }
                >
                  {item.outcome}
                </StatusLabel>
              </div>
              <p>
                {item.admittedAt} · basis {item.basis} ·{" "}
                {targetLabel(item.target)}
              </p>
              <Identities item={item} />
            </article>
          ))}
        </div>
      )}
      {snapshot.nextCursor !== null && (
        <button
          data-testid="load-older-invocations"
          type="button"
          disabled={snapshot.loadingOlder}
          onClick={loadOlder}
        >
          {snapshot.loadingOlder ? "Loading older…" : "Load older invocations"}
        </button>
      )}
    </section>
  );
}
function InvocationDetail({
  snapshot,
  panel,
}: {
  snapshot: InvocationsSnapshot;
  panel: PanelSnapshot | undefined;
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
    <section
      class="panel domain-panel"
      data-testid="invocation-detail"
      aria-labelledby="invocation-detail-title"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">AUDIT-03</span>
          <h2 id="invocation-detail-title">Invocation detail</h2>
        </div>
        <StatusLabel
          state={item.outcome === "outcome_unknown" ? "warning" : "current"}
        >
          {item.outcome}
        </StatusLabel>
      </div>
      <Identities item={item} />
      <ComparisonTable caption="Recorded invocation evidence">
        <tbody>
          <tr>
            <th>Admitted</th>
            <td>{item.admittedAt}</td>
          </tr>
          <tr>
            <th>Admission</th>
            <td>{item.admissionClass}</td>
          </tr>
          <tr>
            <th>Requested name</th>
            <td>{item.requestedName ?? "Not resolved"}</td>
          </tr>
          <tr>
            <th>Outcome basis</th>
            <td>{item.basis}</td>
          </tr>
          <tr>
            <th>Completed</th>
            <td>{item.completedAt ?? "No terminal timestamp retained"}</td>
          </tr>
        </tbody>
      </ComparisonTable>
      {item.basis === "missing_terminal" && (
        <StateNotice state="warning" title="Audit completion is unknown">
          <p>
            Missing terminal evidence does not prove nonexecution and is not
            safe-to-retry evidence. Gateway does not automatically replay
            invocations; an explicit caller retry can duplicate an effect.
          </p>
          {item.target?.kind === "gateway" && (
            <p>
              This is a Gateway-owned local target. Missing terminal evidence is
              not proof of downstream handoff.
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
  );
}
