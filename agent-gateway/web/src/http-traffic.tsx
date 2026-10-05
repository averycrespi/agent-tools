import { useEffect, useState } from "preact/hooks";
import { parseFragment, serializeLocation } from "./location";
import type { SessionClient } from "./session";
import type { PrincipalDirectory } from "./principals";
import type { ViewCoordinator, ViewReadContext, ViewSnapshot } from "./view";
import {
  BinaryToggle,
  CollectionTable,
  LoadedHistorySummary,
  TableIdentity,
  StateNotice,
  StatusLabel,
  InertJSON,
  sentenceCase,
  useDebouncedInput,
} from "./primitives";
import { UserTime } from "./time";
import { RelatedHistory, RelatedHistoryChanged } from "./related-history";
import { parseAuditJSON } from "./audit-contract";
import {
  decodeTrafficItem,
  decodeTrafficPage,
  destinationLabel,
  rejectionLabel,
  transferLabel,
  type Termination,
  trafficOutcome,
  trafficDecisionLabel,
  interceptionExplanation,
  type Rejection,
  type ConnectContext,
  trafficOptions,
  validHTTPTrafficQuery,
  type TrafficItem,
  type TrafficPage,
  type TrafficSummary,
  type TrafficTarget,
} from "./http-traffic-contract";

interface Snapshot {
  key: string;
  items: TrafficSummary[];
  next: string | null;
  item: TrafficItem | undefined;
  live: boolean;
  paused: boolean;
  loaded: boolean;
  error: boolean;
  olderError: boolean;
  missing: boolean;
  notice: string;
  loadingOlder: boolean;
}
const empty = (): Snapshot => ({
  key: "",
  items: [],
  next: null,
  item: undefined,
  live: true,
  paused: false,
  loaded: false,
  error: false,
  olderError: false,
  missing: false,
  notice: "",
  loadingOlder: false,
});
type Result = { key: string; serial: number; automatic: boolean } & (
  | { kind: "page"; page: TrafficPage; append: boolean }
  | { kind: "item"; item: TrafficItem }
  | { kind: "missing" }
  | { kind: "failure"; append: boolean }
);
async function get(context: ViewReadContext, path: string): Promise<Response> {
  const response = await fetch(path, {
    headers: { "X-CSRF-Token": context.csrfToken },
    credentials: "same-origin",
    redirect: "error",
    signal: context.signal,
  });
  if (await context.sessionLost(response)) throw new Error("Session lost.");
  return response;
}
async function json(response: Response): Promise<unknown> {
  if (
    response.status !== 200 ||
    response.headers.get("Content-Type") !== "application/json"
  )
    throw new Error("HTTP traffic unavailable.");
  const raw = await response.text();
  if (new TextEncoder().encode(raw).byteLength > 1024 * 1024)
    throw new Error("HTTP traffic response exceeds limit.");
  return parseAuditJSON(raw);
}
async function stale(response: Response): Promise<boolean> {
  if (
    response.status !== 409 ||
    response.headers.get("Content-Type") !== "application/problem+json"
  )
    return false;
  const raw = await response.text();
  if (raw.length > 4096) return false;
  const value = parseAuditJSON(raw);
  return (
    typeof value === "object" &&
    value !== null &&
    "code" in value &&
    value.code === "stale_cursor"
  );
}
function listPath(
  query: Readonly<Record<string, string>>,
  cursor: string | null,
): string {
  const params = new URLSearchParams({ limit: "50" });
  for (const [key, value] of Object.entries(query))
    params.set(key.slice(7), value);
  if (query.filter_principal)
    params.set("search_locale", Intl.DateTimeFormat().resolvedOptions().locale);
  if (cursor !== null) params.set("cursor", cursor);
  return `/api/v2/http/traffic?${params}`;
}
export class HTTPTrafficController {
  readonly related: RelatedHistory<TrafficSummary>;
  private value = empty();
  private serial = 0;
  private continuation: string | null = null;
  private listeners = new Set<(value: Snapshot) => void>();
  constructor(
    session: SessionClient,
    private views: ViewCoordinator,
  ) {
    this.related = new RelatedHistory(
      session,
      views,
      "http-related",
      "http-traffic",
      async (context, cursor) => {
        const id = parseFragment(context.viewKey)!.segments[1];
        const item = decodeTrafficItem(
          await json(await get(context, `/api/v2/http/traffic/${id}`)),
        );
        if (item.admission.id !== id) throw new Error("Mismatched parent");
        const decision = item.admission.decision as Record<
          string,
          unknown
        > | null;
        if (decision?.transport !== "intercept")
          return { items: [], next: null };
        const response = await get(
          context,
          listPath({ filter_connect_id: id! }, cursor),
        );
        if (await stale(response)) throw new RelatedHistoryChanged();
        const page = decodeTrafficPage(await json(response));
        if (page.items.some((row) => row.connect?.id !== id))
          throw new Error("Mismatched connection");
        return { items: page.items, next: page.nextCursor };
      },
    );
    views.registerPanel({
      id: "http-traffic",
      matches: (key) => parseFragment(key)?.destination === "http-traffic",
      invalidations: ["system_status"],
      shouldRefresh: (reason) =>
        !["invalidation", "reconnect", "poll"].includes(reason) ||
        parseFragment(this.views.snapshot().viewKey)?.segments.length === 2 ||
        (this.value.live && !this.value.paused),
      read: (context) => this.read(context),
      publish: (value) => this.publish(value),
    });
    session.registerProtectedState(() => {
      this.serial++;
      this.continuation = null;
      this.value = empty();
      this.emit();
    });
  }
  snapshot(): Snapshot {
    return this.value;
  }
  subscribe(listener: (value: Snapshot) => void): () => void {
    this.listeners.add(listener);
    listener(this.value);
    return () => this.listeners.delete(listener);
  }
  setLive(live: boolean): void {
    this.serial++;
    this.value = { ...this.value, live };
    this.emit();
    if (live && !this.value.paused) this.refresh();
  }
  refresh(): void {
    this.continuation = null;
    void this.views.refreshPanel("http-traffic");
  }
  resume(): void {
    this.serial++;
    this.value = { ...this.value, paused: false };
    this.emit();
    this.refresh();
  }
  async older(): Promise<void> {
    if (
      this.value.loadingOlder ||
      this.value.next === null ||
      this.value.items.length >= 500 ||
      this.value.key !== this.views.snapshot().viewKey ||
      this.views.snapshot().panels["http-traffic"]?.refreshing
    )
      return;
    this.serial++;
    const serial = this.serial;
    this.continuation = this.value.next;
    this.value = {
      ...this.value,
      paused: true,
      loadingOlder: true,
      olderError: false,
    };
    this.emit();
    try {
      await this.views.refreshPanel("http-traffic");
    } finally {
      if (serial === this.serial) {
        this.value = { ...this.value, loadingOlder: false };
        this.emit();
      }
    }
  }
  private async read(context: ViewReadContext): Promise<Result> {
    const location = parseFragment(context.viewKey);
    if (location?.destination !== "http-traffic")
      throw new Error("Invalid HTTP traffic location.");
    if (this.value.key !== context.viewKey) {
      this.serial++;
      this.continuation = null;
      this.value = {
        ...empty(),
        key: context.viewKey,
        live: this.value.live,
        paused: this.value.paused,
        notice:
          this.value.key === ""
            ? ""
            : "The previous traffic traversal was discarded. Reading the newest matching page.",
      };
      this.emit();
    }
    const basis = {
      key: context.viewKey,
      serial: this.serial,
      automatic: ["invalidation", "reconnect", "poll"].includes(
        context.reason ?? "",
      ),
    };
    const itemID = location.segments[1];
    if (itemID !== undefined) {
      const response = await get(context, `/api/v2/http/traffic/${itemID}`);
      if (response.status === 404) return { ...basis, kind: "missing" };
      const item = decodeTrafficItem(await json(response));
      if (item.admission.id !== itemID)
        throw new Error("Mismatched HTTP traffic identity.");
      return { ...basis, kind: "item", item };
    }
    const cursor = context.reason === "panel" ? this.continuation : null;
    this.continuation = null;
    let append = cursor !== null;
    try {
      let response = await get(context, listPath(location.query, cursor));
      if (append && (await stale(response))) {
        if (context.signal.aborted) throw new Error("Superseded.");
        append = false;
        this.value = {
          ...this.value,
          items: [],
          next: null,
          loaded: false,
          notice: "History changed. Restarted at the newest matching traffic.",
        };
        this.emit();
        response = await get(context, listPath(location.query, null));
      }
      const page = decodeTrafficPage(await json(response));
      if (page.items.length > 50) throw new Error("Invalid HTTP page size.");
      if (
        append &&
        page.items.some((item) =>
          this.value.items.some((prior) => prior.id === item.id),
        )
      )
        throw new Error("Repeated HTTP traffic evidence.");
      return { ...basis, kind: "page", page, append };
    } catch (error) {
      if (context.signal.aborted) throw error;
      return { ...basis, kind: "failure", append };
    }
  }
  private publish(result: Result): void {
    if (
      result.key !== this.value.key ||
      (result.automatic &&
        (result.serial !== this.serial ||
          !this.value.live ||
          this.value.paused) &&
        result.kind !== "item" &&
        result.kind !== "missing")
    )
      return;
    if (result.kind === "page")
      this.value = {
        ...this.value,
        items: result.append
          ? [...this.value.items, ...result.page.items]
          : result.page.items,
        next: result.page.nextCursor,
        loaded: true,
        error: false,
        olderError: false,
        loadingOlder: false,
      };
    else if (result.kind === "item")
      this.value = {
        ...this.value,
        item: result.item,
        error: false,
        missing: false,
      };
    else if (result.kind === "missing")
      this.value = { ...this.value, item: undefined, missing: true };
    else
      this.value = {
        ...this.value,
        error: !result.append,
        olderError: result.append,
        loadingOlder: false,
      };
    this.emit();
  }
  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}

export function HTTPTraffic({
  controller,
  principals,
  view,
  navigate,
}: {
  controller: HTTPTrafficController;
  principals: PrincipalDirectory;
  view: ViewSnapshot;
  navigate: (fragment: string) => void;
}) {
  const [snapshot, setSnapshot] = useState(controller.snapshot());
  const [names, setNames] = useState(principals.snapshot());
  useEffect(() => controller.subscribe(setSnapshot), [controller]);
  useEffect(() => principals.subscribe(setNames), [principals]);
  const location = parseFragment(view.viewKey);
  const query = location?.query ?? {};
  const current =
    snapshot.key === view.viewKey
      ? snapshot
      : { ...empty(), live: snapshot.live, paused: snapshot.paused };
  const link = (id?: string) =>
    serializeLocation({
      destination: "http-traffic",
      segments: id === undefined ? ["http-traffic"] : ["http-traffic", id],
      query,
    });
  const panel = view.panels["http-traffic"];
  if (location?.segments.length === 2)
    return (
      <div class="domain-view">
        <nav class="detail-navigation" aria-label="HTTP traffic navigation">
          <a href={link()}>Back to HTTP traffic</a>
        </nav>
        <header class="detail-context" data-testid="detail-context">
          <div class="detail-context-heading">
            <h1 id="http-traffic-page-title" tabindex={-1}>
              {current.item?.admission.target
                ? (current.item.admission.target as TrafficTarget).scheme
                  ? "HTTP request"
                  : "CONNECT exchange"
                : "HTTP traffic"}
            </h1>
            {current.item && (
              <TrafficRecordStatus
                item={current.item}
                stale={panel?.status === "error"}
              />
            )}
          </div>
          <div class="copyable-value">
            <code>{location.segments[1]}</code>
          </div>
        </header>
        {current.missing ? (
          <StateNotice state="empty" title="Traffic record unavailable">
            It may have been pruned. Missing evidence does not prove
            nonexecution.
          </StateNotice>
        ) : current.item === undefined ? (
          <StateNotice
            state={panel?.status === "error" ? "error" : "loading"}
            title={
              panel?.status === "error"
                ? "HTTP traffic unavailable"
                : "Loading HTTP traffic…"
            }
          />
        ) : (
          <>
            {panel?.status === "error" && (
              <StateNotice
                state="error"
                title="Refresh failed. This evidence may be stale."
              />
            )}
            <TrafficDetail item={current.item} link={link} />
            {(current.item.admission.decision as Record<string, unknown> | null)
              ?.transport === "intercept" && (
              <RelatedTraffic
                controller={controller.related}
                view={view}
                link={link}
              />
            )}
          </>
        )}
      </div>
    );
  return (
    <section
      class="panel domain-panel"
      aria-label="HTTP traffic history"
      data-testid="http-traffic-view"
    >
      <div class="collection-toolbar live-collection-toolbar">
        <label for="http-traffic-live-mode">Live mode</label>
        <BinaryToggle
          attributes={{ id: "http-traffic-live-mode" }}
          checked={current.live}
          showState={false}
          onChange={(live) => controller.setLive(live)}
        />
      </div>
      <TrafficFilters query={query} navigate={navigate} />
      {current.paused && (
        <div class="inline-actions">
          {current.live && (
            <StatusLabel state="warning">
              Live paused while viewing older results
            </StatusLabel>
          )}
          <button type="button" onClick={() => controller.resume()}>
            {current.live ? "Resume live" : "Return to newest"}
          </button>
        </div>
      )}
      {current.notice && <StateNotice state="warning" title={current.notice} />}
      {(current.error || panel?.status === "error") && (
        <StateNotice state="error" title="HTTP traffic unavailable">
          Previously loaded traffic may be stale.
        </StateNotice>
      )}
      {!current.loaded ? (
        !current.error && panel?.status !== "error" ? (
          <StateNotice state="loading" title="Loading HTTP traffic…" />
        ) : null
      ) : (
        <>
          <CollectionTable
            caption="HTTP traffic records"
            localStale={
              current.error || current.olderError || panel?.status === "error"
            }
            layout="activity"
            rowHeaderKey="destination"
            rowKey={(row) => row.id}
            items={current.items}
            itemNames={{
              singular: "traffic record",
              plural: "traffic records",
            }}
            emptyTitle={
              Object.keys(query).length
                ? "No matching HTTP traffic"
                : "No HTTP traffic yet"
            }
            columns={[
              {
                key: "admitted",
                role: "time",
                label: "Admitted",
                render: (row) => <UserTime value={row.admitted_at} />,
              },
              {
                key: "destination",
                role: "identity",
                label: "Destination",
                render: (row) => (
                  <TableIdentity
                    primary={
                      <a href={link(row.id)}>{destinationLabel(row.target)}</a>
                    }
                    secondary={row.id}
                  />
                ),
              },
              {
                key: "principal",
                role: "relation",
                label: "Agent",
                render: (row) => (
                  <TableIdentity
                    primary={
                      <a href={`#/agents/${row.principal_id}`}>
                        {names.get(row.principal_id) ?? "Agent"}
                      </a>
                    }
                    secondary={row.principal_id}
                  />
                ),
              },
              {
                key: "type",
                role: "status",
                label: "Type",
                render: (row) => sentenceCase(row.type),
              },
              {
                key: "decision",
                role: "text",
                label: "Decision",
                render: (row) => (
                  <>
                    <StatusLabel
                      state={row.decision === "allow" ? "current" : "neutral"}
                    >
                      {trafficDecisionLabel(row.decision, row.type)}
                    </StatusLabel>
                    {row.type === "invalid" && row.rejection !== undefined && (
                      <div>{rejectionLabel(row.rejection)}</div>
                    )}
                  </>
                ),
              },
              {
                key: "outcome",
                role: "status",
                label: "Outcome",
                render: (row) => (
                  <>
                    <StatusLabel
                      state={
                        trafficOutcome(row) === "succeeded"
                          ? "current"
                          : trafficOutcome(row) === "outcome_unknown"
                            ? "warning"
                            : [
                                  "not_dispatched",
                                  "interception_selected",
                                ].includes(trafficOutcome(row))
                              ? "neutral"
                              : "error"
                      }
                    >
                      {row.decision === "intercept"
                        ? "—"
                        : sentenceCase(trafficOutcome(row))}
                    </StatusLabel>
                    {row.type === "request" && row.decision === "allow" && (
                      <div>
                        {transferLabel(
                          row.completion_recorded,
                          row.termination,
                        )}
                      </div>
                    )}
                  </>
                ),
              },
            ]}
          />
          <div class="history-continuation">
            {current.loaded && (
              <LoadedHistorySummary
                count={current.items.length}
                singular="HTTP traffic record"
                plural="HTTP traffic records"
                matching={Object.keys(query).length > 0}
                stale={current.error || panel?.status === "error"}
              />
            )}
            {current.next !== null && current.items.length < 500 && (
              <button
                type="button"
                disabled={current.loadingOlder}
                onClick={() => void controller.older()}
              >
                {current.loadingOlder ? "Loading…" : "Load older"}
              </button>
            )}
          </div>
          {current.olderError && (
            <StateNotice state="error" title="Older traffic unavailable">
              Loaded records are retained. Use Load older to try this read
              again.
            </StateNotice>
          )}
          {current.items.length >= 500 && (
            <p>500 records loaded. Narrow the filters or return to newest.</p>
          )}
        </>
      )}
    </section>
  );
}
function TrafficFilters({
  query,
  navigate,
}: {
  query: Readonly<Record<string, string>>;
  navigate: (fragment: string) => void;
}) {
  const [draft, setDraft] = useState<Record<string, string>>({ ...query });
  const [error, setError] = useState(false);
  useEffect(() => {
    setDraft({ ...query });
    setError(false);
  }, [JSON.stringify(query)]);
  const apply = (next: Record<string, string>) => {
    const clean = Object.fromEntries(
      Object.entries(next).filter(([, value]) => value !== ""),
    );
    if (!validHTTPTrafficQuery(clean)) {
      setError(true);
      return;
    }
    setError(false);
    if (JSON.stringify(clean) !== JSON.stringify(query))
      navigate(
        serializeLocation({
          destination: "http-traffic",
          segments: ["http-traffic"],
          query: clean,
        }),
      );
  };
  useDebouncedInput(draft, apply);
  return (
    <>
      <div
        class="table-filters collection-query-filters"
        role="group"
        aria-label="HTTP traffic filters"
      >
        {[
          ["destination", "Destination host"],
          ["principal", "Agent"],
        ].map(([key, label]) => (
          <input
            type="search"
            aria-label={label}
            placeholder={label}
            value={draft[`filter_${key}`] ?? ""}
            onInput={(event) =>
              setDraft({
                ...draft,
                [`filter_${key}`]: event.currentTarget.value,
              })
            }
          />
        ))}
        {Object.entries(trafficOptions).map(([key, values]) => (
          <select
            aria-label={sentenceCase(key)}
            value={draft[`filter_${key}`] ?? ""}
            onChange={(event) => {
              const next = {
                ...draft,
                [`filter_${key}`]: event.currentTarget.value,
              };
              setDraft(next);
              apply(next);
            }}
          >
            <option value="">{sentenceCase(key)}: any</option>
            {values.map((value) => (
              <option value={value}>
                {key === "decision" && value === "intercept"
                  ? "Interception selected"
                  : sentenceCase(value)}
              </option>
            ))}
          </select>
        ))}
        <button
          type="button"
          onClick={() => {
            setDraft({});
            apply({});
          }}
        >
          Reset
        </button>
      </div>
      {(query.filter_connect_id || query.filter_principal_id) && (
        <div
          class="table-filters"
          role="group"
          aria-label="HTTP traffic context filters"
        >
          {query.filter_connect_id && (
            <span class="inline-actions">
              Related to CONNECT{" "}
              <a
                href={serializeLocation({
                  destination: "http-traffic",
                  segments: ["http-traffic", query.filter_connect_id],
                  query: {},
                })}
              >
                <code>{query.filter_connect_id}</code>
              </a>
              <button
                type="button"
                onClick={() => {
                  const next = { ...draft };
                  delete next.filter_connect_id;
                  setDraft(next);
                  apply(next);
                }}
              >
                Remove connection filter
              </button>
            </span>
          )}
          {query.filter_principal_id && (
            <span class="inline-actions">
              Exact agent ID: <code>{query.filter_principal_id}</code>
              <button
                type="button"
                onClick={() => {
                  const next = { ...draft };
                  delete next.filter_principal_id;
                  setDraft(next);
                  apply(next);
                }}
              >
                Remove exact agent filter
              </button>
            </span>
          )}
        </div>
      )}
      {error && (
        <StateNotice
          state="error"
          title="Use searches of at most 256 UTF-8 bytes without control characters."
        />
      )}
    </>
  );
}
function RelatedTraffic({
  controller,
  view,
  link,
}: {
  controller: RelatedHistory<TrafficSummary>;
  view: ViewSnapshot;
  link: (id?: string) => string;
}) {
  const [value, setValue] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setValue), [controller]);
  const current = value.key === view.viewKey ? value : undefined;
  return (
    <section
      class="panel domain-panel"
      aria-label="Requests on this connection"
    >
      <div class="panel-heading">
        <h2>Requests on this connection</h2>
        <button
          type="button"
          disabled={current?.loading}
          onClick={() => controller.refresh()}
        >
          Refresh requests
        </button>
      </div>
      {current?.notice && (
        <StateNotice state="warning" title={current.notice} />
      )}
      {current?.error && (
        <StateNotice state="error" title="Related requests unavailable">
          Previously loaded requests may be stale. Refresh to try again.
        </StateNotice>
      )}
      {!current?.loaded && !current?.error && (
        <StateNotice state="loading" title="Loading related requests…" />
      )}
      {current?.loaded && (
        <CollectionTable
          caption="Related HTTP requests"
          layout="activity"
          rowHeaderKey="request"
          rowKey={(row) => row.id}
          items={current.items}
          emptyTitle="No related requests recorded"
          localStale={current.error}
          columns={[
            {
              key: "time",
              label: "Admitted",
              role: "time",
              render: (row) => <UserTime value={row.admitted_at} />,
            },
            {
              key: "request",
              label: "Request",
              role: "identity",
              render: (row) => (
                <TableIdentity
                  primary={
                    <a href={link(row.id)}>{destinationLabel(row.target)}</a>
                  }
                  secondary={row.id}
                />
              ),
            },
            {
              key: "decision",
              label: "Decision",
              role: "status",
              render: (row) => (
                <StatusLabel
                  state={row.decision === "allow" ? "current" : "neutral"}
                >
                  {trafficDecisionLabel(row.decision, row.type)}
                </StatusLabel>
              ),
            },
            {
              key: "outcome",
              label: "Outcome",
              role: "status",
              render: (row) => (
                <StatusLabel
                  state={
                    trafficOutcome(row) === "succeeded"
                      ? "current"
                      : trafficOutcome(row) === "outcome_unknown"
                        ? "warning"
                        : ["not_dispatched", "interception_selected"].includes(
                              trafficOutcome(row),
                            )
                          ? "neutral"
                          : "error"
                  }
                >
                  {row.decision === "intercept"
                    ? "—"
                    : sentenceCase(trafficOutcome(row))}
                </StatusLabel>
              ),
            },
          ]}
        />
      )}
      <div class="collection-pagination">
        {current?.loaded && (
          <LoadedHistorySummary
            count={current.items.length}
            singular="request"
            plural="requests"
            matching={false}
            stale={current.error}
          />
        )}
        {current?.next && current.items.length < 500 && (
          <button
            type="button"
            disabled={current.loading}
            onClick={() => controller.more()}
          >
            Load more requests
          </button>
        )}
      </div>
    </section>
  );
}
function responseSourceLabel(source: unknown): string {
  return source === "gateway"
    ? "Gateway"
    : source === "upstream"
      ? "Upstream"
      : "Unavailable";
}
function TrafficRecordStatus({
  item,
  stale,
}: {
  item: TrafficItem;
  stale: boolean;
}) {
  const decision = item.admission.decision as Record<string, unknown> | null;
  const outcome =
    decision?.transport === "intercept"
      ? "interception_selected"
      : item.completion?.outcome
        ? String(item.completion.outcome)
        : decision?.allowed
          ? "outcome_unknown"
          : "not_dispatched";
  return (
    <StatusLabel
      state={
        stale
          ? "stale"
          : outcome === "succeeded"
            ? "current"
            : outcome === "outcome_unknown"
              ? "warning"
              : ["not_dispatched", "interception_selected"].includes(outcome)
                ? "neutral"
                : "error"
      }
    >
      {sentenceCase(outcome)}
    </StatusLabel>
  );
}

function TrafficDetail({
  item,
  link,
}: {
  item: TrafficItem;
  link: (id?: string) => string;
}) {
  const a = item.admission,
    d = a.decision as Record<string, unknown> | null,
    c = item.completion,
    termination = c?.termination as Termination | undefined,
    rejection = a.rejection as Rejection | undefined,
    connect = a.connect as ConnectContext | undefined,
    intercepted =
      d?.transport === "intercept" && d?.reason === "intercept_required",
    isConnect =
      a.target !== null && (a.target as TrafficTarget).scheme === undefined;
  return (
    <>
      <section class="detail-section">
        <h2>Admission</h2>
        <dl class="detail-facts">
          <div>
            <dt>Destination</dt>
            <dd>{destinationLabel(a.target as TrafficTarget | null)}</dd>
          </div>
          <div>
            <dt>Admitted</dt>
            <dd>
              <UserTime value={String(a.admitted_at)} />
            </dd>
          </div>
          <div>
            <dt>Decision time</dt>
            <dd>
              <UserTime value={String(a.evaluated_at)} />
            </dd>
          </div>
          <div>
            <dt>Reason</dt>
            <dd>
              {d === null
                ? rejectionLabel(rejection)
                : intercepted
                  ? "Interception selected"
                  : isConnect
                    ? d.allowed
                      ? "Opaque tunnel allowed"
                      : "CONNECT denied"
                    : sentenceCase(String(d.reason))}
            </dd>
          </div>
          <div>
            <dt>Transport</dt>
            <dd>{d === null ? "None" : sentenceCase(String(d.transport))}</dd>
          </div>
        </dl>
        {rejection !== undefined && (
          <details>
            <summary>Rejection codes</summary>
            <p>
              {rejection.stage} · {rejection.reason}
            </p>
          </details>
        )}
        {!isConnect && (
          <dl class="detail-facts">
            <div>
              <dt>Connection</dt>
              <dd>
                {connect === undefined ? (
                  "Unavailable"
                ) : (
                  <a href={link(connect.id)}>
                    CONNECT {connect.host}:{connect.port} ·{" "}
                    <code>{connect.id}</code>
                  </a>
                )}
              </dd>
            </div>
            {connect !== undefined && (
              <div>
                <dt>Destination inherited from CONNECT</dt>
                <dd>
                  {destinationLabel({ host: connect.host, port: connect.port })}
                </dd>
              </div>
            )}
          </dl>
        )}
        {connect !== undefined && (
          <p>Connection context does not validate the inner request target.</p>
        )}
        {d?.transport === "tunnel" && (
          <p>Opaque tunnel: inner HTTP requests are not visible.</p>
        )}
      </section>
      <section class="detail-section">
        <h2>Outcome</h2>
        <dl class="detail-facts">
          <div>
            <dt>Response source</dt>
            <dd>
              {responseSourceLabel(
                rejection === undefined ? c?.response_source : "gateway",
              )}
            </dd>
          </div>
        </dl>
        {rejection !== undefined && (
          <p>Gateway rejection. Response delivery is not recorded.</p>
        )}
        {c === null ? (
          <StateNotice
            state={d?.allowed ? "warning" : "neutral"}
            title={
              intercepted
                ? "Interception selected"
                : d?.allowed
                  ? "Unknown outcome"
                  : isConnect
                    ? "CONNECT denied"
                    : "Not dispatched"
            }
          >
            {intercepted
              ? interceptionExplanation
              : d?.allowed
                ? "Missing terminal evidence does not prove nonexecution or safe retry."
                : undefined}
          </StateNotice>
        ) : (
          <dl class="detail-facts">
            {Object.entries(c)
              .filter(
                ([key]) => key !== "response_source" && key !== "termination",
              )
              .map(([key, value]) => (
                <div>
                  <dt>{sentenceCase(key)}</dt>
                  <dd>
                    {key === "completed_at" ? (
                      <UserTime value={String(value)} />
                    ) : (
                      sentenceCase(String(value))
                    )}
                  </dd>
                </div>
              ))}
          </dl>
        )}
        {!isConnect && d?.allowed && (
          <dl class="detail-facts">
            <div>
              <dt>HTTP transfer</dt>
              <dd>{transferLabel(c !== null, termination)}</dd>
            </div>
            {termination !== undefined && (
              <>
                <div>
                  <dt>Observed stage</dt>
                  <dd>{sentenceCase(termination.stage)}</dd>
                </div>
                <div>
                  <dt>Observed condition</dt>
                  <dd>{sentenceCase(termination.condition)}</dd>
                </div>
                {termination.context !== undefined && (
                  <div>
                    <dt>Request context</dt>
                    <dd>{sentenceCase(termination.context)}</dd>
                  </div>
                )}
              </>
            )}
          </dl>
        )}
        {termination !== undefined && (
          <details>
            <summary>Transfer evidence limits</summary>
            <p>
              HTTP transfer evidence does not establish application success or
              who initiated cancellation.
            </p>
          </details>
        )}
        {!isConnect && c?.outcome === "outcome_unknown" && (
          <p>The request may have taken effect. Retrying may repeat effects.</p>
        )}
      </section>
      <section class="detail-section">
        <h2>Admission-time authority</h2>
        <dl class="detail-facts">
          {[
            ["Agent", a.principal],
            ["Agent credential", a.agent_credential],
          ].map(([label, value]) => {
            const ref = value as { id: string; revision: number };
            return (
              <div>
                <dt>{String(label)}</dt>
                <dd>
                  <code>{ref.id}</code> · revision {ref.revision}
                </dd>
              </div>
            );
          })}
          {d !== null && (
            <>
              <div>
                <dt>Policy revision</dt>
                <dd>{String(d.policy_revision)}</dd>
              </div>
              <div>
                <dt>Default</dt>
                <dd>
                  {sentenceCase(String(a.default))} · revision{" "}
                  {String(d.default_revision)}
                </dd>
              </div>
            </>
          )}
        </dl>
        <details>
          <summary>Exact authority references</summary>
          <InertJSON
            value={{ decision: a.decision, material: a.material }}
            label="Admission-time references"
          />
        </details>
        <details>
          <summary>Matched policy selectors</summary>
          <InertJSON value={a.grants} label="Recorded policy selectors" />
        </details>
      </section>
    </>
  );
}
