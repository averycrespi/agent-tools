import { useEffect, useState } from "preact/hooks";
import { parseFragment, serializeLocation } from "./location";
import type { SessionClient } from "./session";
import type { PrincipalDirectory } from "./principals";
import type { ViewCoordinator, ViewReadContext, ViewSnapshot } from "./view";
import {
  BinaryToggle,
  CollectionTable,
  TableIdentity,
  StateNotice,
  StatusLabel,
  InertJSON,
  sentenceCase,
  useDebouncedInput,
} from "./primitives";
import { UserTime } from "./time";
import { parseAuditJSON } from "./audit-contract";
import {
  decodeTrafficItem,
  decodeTrafficPage,
  destinationLabel,
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
  if (cursor !== null) params.set("cursor", cursor);
  return `/api/v2/http/traffic?${params}`;
}
export class HTTPTrafficController {
  private value = empty();
  private serial = 0;
  private continuation: string | null = null;
  private listeners = new Set<(value: Snapshot) => void>();
  constructor(
    session: SessionClient,
    private views: ViewCoordinator,
  ) {
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
              HTTP Traffic
            </h1>
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
            <TrafficDetail item={current.item} />
          </>
        )}
      </div>
    );
  return (
    <section
      class="panel domain-panel"
      aria-label="HTTP Traffic"
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
      {!current.loaded && !current.error && panel?.status !== "error" ? (
        <StateNotice state="loading" title="Loading HTTP traffic…" />
      ) : (
        <>
          <p>{current.items.length} HTTP traffic records loaded</p>
          <CollectionTable
            caption="HTTP traffic"
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
                      <a href={`#/principals/${row.principal_id}`}>
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
                role: "status",
                label: "Decision",
                render: (row) => (
                  <StatusLabel
                    state={row.decision === "allow" ? "current" : "neutral"}
                  >
                    {sentenceCase(row.decision)}
                  </StatusLabel>
                ),
              },
              {
                key: "outcome",
                role: "status",
                label: "Outcome",
                render: (row) => (
                  <StatusLabel
                    state={
                      row.outcome === "succeeded"
                        ? "current"
                        : row.outcome === "outcome_unknown"
                          ? "warning"
                          : row.outcome === "not_dispatched"
                            ? "neutral"
                            : "error"
                    }
                  >
                    {sentenceCase(row.outcome)}
                  </StatusLabel>
                ),
              },
            ]}
            hasMore={current.next !== null && current.items.length < 500}
            loadingMore={current.loadingOlder}
            onLoadMore={() => void controller.older()}
            loadMoreLabel="Load older"
          />
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
    <div
      class="table-filters collection-query-filters"
      role="group"
      aria-label="HTTP traffic filters"
    >
      {[
        ["principal_id", "Agent ID"],
        ["destination", "Destination host"],
      ].map(([key, label]) => (
        <input
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
            <option value={value}>{sentenceCase(value)}</option>
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
        Clear filters
      </button>
      {error && (
        <StateNotice
          state="error"
          title="Use an exact agent ID or canonical destination hostname."
        />
      )}
    </div>
  );
}
function TrafficDetail({ item }: { item: TrafficItem }) {
  const a = item.admission,
    d = a.decision as Record<string, unknown> | null,
    c = item.completion;
  return (
    <>
      <section class="panel domain-panel">
        <h2>Admission</h2>
        <dl class="fact-grid">
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
              {sentenceCase(d === null ? "invalid_request" : String(d.reason))}
            </dd>
          </div>
          <div>
            <dt>Transport</dt>
            <dd>{d === null ? "None" : sentenceCase(String(d.transport))}</dd>
          </div>
        </dl>
        {d?.transport === "tunnel" && (
          <p>Opaque tunnel: inner HTTP requests are not visible.</p>
        )}
      </section>
      <section class="panel domain-panel">
        <h2>Outcome</h2>
        {c === null ? (
          <StateNotice
            state={d?.allowed ? "warning" : "neutral"}
            title={d?.allowed ? "Unknown outcome" : "Not dispatched"}
          >
            {d?.allowed
              ? "Missing terminal evidence does not prove nonexecution or safe retry."
              : undefined}
          </StateNotice>
        ) : (
          <dl class="fact-grid">
            {Object.entries(c).map(([key, value]) => (
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
      </section>
      <section class="panel domain-panel">
        <h2>Admission-time authority</h2>
        <p>
          These references and matched policy selectors describe admission time,
          not current grants or credential authority.
        </p>
        <dl class="fact-grid">
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
