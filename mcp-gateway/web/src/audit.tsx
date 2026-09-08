import { useEffect, useState } from "preact/hooks";
import {
  auditFilterKeys,
  auditFilterOptions,
  decodeAuditItem,
  decodeAuditPage,
  parseAuditJSON,
  validAuditQuery,
  type AuditCredential,
  type AuditEvent,
  type AuditHistory,
  type AuditSummary,
} from "./audit-contract";
import {
  parseFragment,
  serializeLocation,
  type ResolvedLocation,
} from "./location";
import {
  CollectionTable,
  FormField,
  sentenceCase,
  StateNotice,
  StatusLabel,
} from "./primitives";
import { parseProblem, type SessionClient } from "./session";
import { UserTime } from "./time";
import type { ViewCoordinator, ViewReadContext, ViewSnapshot } from "./view";

const replacementNotice =
  "Audit history may have been replaced by restore. Newer local events may have been discarded. Previous-history state was discarded; these histories must not be combined.";
const staleNotice =
  "The audit cursor expired or history was pruned. The previous traversal was discarded and restarted at the newest matching page.";
export interface AuditSnapshot {
  viewKey: string;
  items: readonly AuditSummary[];
  nextCursor: string | null;
  history: AuditHistory | undefined;
  item: AuditEvent | undefined;
  missing: boolean;
  loadingOlder: boolean;
  notice: string | undefined;
  targetLink: string | undefined;
  targetUnavailable: boolean;
}
function empty(viewKey = ""): AuditSnapshot {
  return {
    viewKey,
    items: [],
    nextCursor: null,
    history: undefined,
    item: undefined,
    missing: false,
    loadingOlder: false,
    notice: undefined,
    targetLink: undefined,
    targetUnavailable: false,
  };
}
async function get(context: ViewReadContext, path: string): Promise<Response> {
  const response = await fetch(path, {
    method: "GET",
    headers: { Accept: "application/json", "X-CSRF-Token": context.csrfToken },
    credentials: "same-origin",
    redirect: "error",
    signal: context.signal,
  });
  if (await context.sessionLost(response)) throw new Error("Session lost");
  return response;
}
async function body(
  response: Response,
  maximum = 256 * 1024,
): Promise<unknown> {
  const reader = response.body?.getReader();
  if (reader === undefined) throw new Error("Audit body missing");
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let bytes = 0;
  let text = "";
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      bytes += chunk.value.byteLength;
      if (bytes > maximum) throw new Error("Audit response too large");
      text += decoder.decode(chunk.value, { stream: true });
    }
    return parseAuditJSON(text + decoder.decode());
  } finally {
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}
async function readResponse(
  context: ViewReadContext,
  path: string,
): Promise<{ value: unknown; problem: string | undefined }> {
  const response = await get(context, path);
  if (
    response.status === 200 &&
    response.headers.get("Content-Type") === "application/json"
  )
    return { value: await body(response), problem: undefined };
  if (response.headers.get("Content-Type") === "application/problem+json") {
    const problem = parseProblem(await body(response, 64 * 1024));
    if (
      problem?.status === response.status &&
      ((response.status === 409 &&
        ["stale_cursor", "audit_history_replaced"].includes(problem.code)) ||
        (response.status === 404 && problem?.code === "not_found"))
    )
      return { value: undefined, problem: problem.code };
  }
  throw new Error("Audit read unavailable");
}
function listPath(
  query: Readonly<Record<string, string>>,
  cursor: string | null,
  generation?: string,
): string {
  const values = new URLSearchParams({ limit: "50" });
  for (const [key, value] of Object.entries(query))
    values.set(key.slice(7), value);
  if (cursor !== null) values.set("cursor", cursor);
  if (generation !== undefined) values.set("generation", generation);
  return `/api/v1/audit-events?${values.toString()}`;
}
export class AuditController {
  private value = empty();
  private readonly listeners = new Set<(value: AuditSnapshot) => void>();
  private continuation: string | null = null;
  private knownGeneration: string | undefined;
  constructor(
    session: SessionClient,
    private readonly views: ViewCoordinator,
  ) {
    views.registerPanel({
      id: "audit",
      matches: (key) => parseFragment(key)?.destination === "audit",
      invalidations: [],
      read: (context) => this.read(context),
      publish: (result) => {
        this.value = result;
        this.knownGeneration =
          result.history?.generation ?? this.knownGeneration;
        this.emit();
      },
    });
    session.registerProtectedState(() => {
      this.value = empty();
      this.continuation = null;
      this.knownGeneration = undefined;
      this.emit();
    });
  }
  snapshot(): AuditSnapshot {
    return this.value;
  }
  subscribe(listener: (value: AuditSnapshot) => void): () => void {
    this.listeners.add(listener);
    listener(this.value);
    return () => this.listeners.delete(listener);
  }
  async loadOlder(): Promise<void> {
    if (
      this.value.loadingOlder ||
      this.value.nextCursor === null ||
      this.views.snapshot().viewKey !== this.value.viewKey
    )
      return;
    this.continuation = this.value.nextCursor;
    this.value = { ...this.value, loadingOlder: true };
    this.emit();
    const key = this.value.viewKey;
    const pending = this.views.refreshPanel("audit");
    const generation = this.views.snapshot().generation;
    try {
      await pending;
    } finally {
      if (
        this.value.viewKey === key &&
        this.views.snapshot().generation === generation
      ) {
        this.value = { ...this.value, loadingOlder: false };
        this.emit();
      }
    }
  }
  private discard(context: ViewReadContext, notice: string): void {
    if (
      context.signal.aborted ||
      this.views.snapshot().viewKey !== context.viewKey
    )
      return;
    this.value = { ...empty(context.viewKey), notice };
    this.emit();
  }
  private async read(context: ViewReadContext): Promise<AuditSnapshot> {
    const location = parseFragment(context.viewKey);
    if (location?.destination !== "audit")
      throw new Error("Invalid audit location");
    if (context.viewKey !== this.value.viewKey) {
      this.value = empty(context.viewKey);
      this.continuation = null;
      this.emit();
    }
    const previous = this.value;
    const expected = this.knownGeneration;
    const id = location.segments[1];
    if (id !== undefined) {
      const result = await readResponse(
        context,
        `/api/v1/audit-events/${id}${expected === undefined ? "" : `?generation=${expected}`}`,
      );
      if (result.problem === "audit_history_replaced") {
        this.discard(context, replacementNotice);
        return { ...empty(context.viewKey), notice: replacementNotice };
      }
      if (result.problem === "not_found")
        return { ...empty(context.viewKey), missing: true };
      if (result.problem !== undefined)
        throw new Error("Audit item unavailable");
      const item = decodeAuditItem(result.value);
      if (item.event.id !== id) throw new Error("Audit identity mismatch");
      if (expected !== undefined && expected !== item.history.generation)
        return { ...empty(context.viewKey), notice: replacementNotice };
      const evidence = {
        ...empty(context.viewKey),
        history: item.history,
        item: item.event,
      };
      // Optional live-target availability must not delay the retained evidence.
      if (
        !context.signal.aborted &&
        this.views.snapshot().viewKey === context.viewKey
      ) {
        this.value = evidence;
        this.knownGeneration = item.history.generation;
        this.emit();
      }
      return { ...evidence, ...(await this.target(context, item.event)) };
    }
    const cursor = this.continuation;
    this.continuation = null;
    let result = await readResponse(
      context,
      listPath(location.query, cursor, expected),
    );
    let append = cursor !== null;
    let notice = previous.notice;
    if (
      result.problem === "audit_history_replaced" ||
      (cursor !== null && result.problem === "stale_cursor")
    ) {
      notice =
        result.problem === "audit_history_replaced"
          ? replacementNotice
          : staleNotice;
      this.discard(context, notice);
      result = await readResponse(context, listPath(location.query, null));
      append = false;
    }
    if (result.problem !== undefined) throw new Error("Audit page unavailable");
    const page = decodeAuditPage(result.value);
    if (expected !== undefined && page.history.generation !== expected) {
      append = false;
      notice = replacementNotice;
    }
    if (
      append &&
      (previous.history?.generation !== page.history.generation ||
        previous.history.oldest_retained?.sequence !==
          page.history.oldest_retained?.sequence ||
        previous.items.length + page.items.length > 65536)
    )
      throw new Error("Audit continuation mismatch");
    if (
      append &&
      previous.items.length > 0 &&
      page.items.length > 0 &&
      BigInt(previous.items.at(-1)!.sequence) <= BigInt(page.items[0]!.sequence)
    )
      throw new Error("Audit continuation order mismatch");
    return {
      ...empty(context.viewKey),
      items: append ? [...previous.items, ...page.items] : page.items,
      nextCursor: page.next_cursor,
      history: page.history,
      notice,
    };
  }
  private async target(
    context: ViewReadContext,
    item: AuditEvent,
  ): Promise<Pick<AuditSnapshot, "targetLink" | "targetUnavailable">> {
    const routes: Record<string, [string, string]> = {
      server: ["servers", "servers"],
      principal: ["principals", "principals"],
      grant: ["grants", "grants"],
      grant_request: ["grant-requests", "requests"],
    };
    const route = routes[item.target.type];
    if (route === undefined)
      return { targetLink: undefined, targetUnavailable: false };
    try {
      const response = await get(
        context,
        `/api/v1/${route[0]}/${item.target.id}`,
      );
      if (response.status === 404)
        return { targetLink: undefined, targetUnavailable: false };
      if (
        response.status !== 200 ||
        response.headers.get("Content-Type") !== "application/json"
      )
        throw new Error("Target unavailable");
      const resource = await body(response, 4 * 1024 * 1024);
      if (
        typeof resource !== "object" ||
        resource === null ||
        !("id" in resource) ||
        resource.id !== item.target.id
      )
        throw new Error("Target identity mismatch");
      if ("deleted_at" in resource && resource.deleted_at !== null)
        return { targetLink: undefined, targetUnavailable: false };
      return {
        targetLink: `#/${route[1]}/${item.target.id}`,
        targetUnavailable: false,
      };
    } catch {
      return { targetLink: undefined, targetUnavailable: true };
    }
  }
  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}
function Credential({ value }: { value: AuditCredential | null }) {
  return value === null ? (
    <>None recorded</>
  ) : (
    <>
      {value.id} · fingerprint {value.fingerprint}
    </>
  );
}
function History({ value }: { value: AuditHistory }) {
  return (
    <section
      class="panel domain-panel audit-history"
      aria-label="Audit retention and continuity"
    >
      <div class="panel-heading">
        <h2>Retained history</h2>
        <StatusLabel state={value.pruned ? "warning" : "current"}>
          {value.pruned ? "Older events pruned" : "No pruning recorded"}
        </StatusLabel>
      </div>
      <p>
        Only the newest 65,536 events are retained. Restore may replace local
        history and discard newer events; this is not a permanent or complete
        record.
      </p>
      <dl class="fact-grid">
        <div>
          <dt>Oldest retained boundary</dt>
          <dd>
            {value.oldest_retained === null ? (
              "None — history is empty"
            ) : (
              <>
                <UserTime value={value.oldest_retained.timestamp} /> · sequence{" "}
                {value.oldest_retained.sequence}
                <br />
                {value.oldest_retained.id}
              </>
            )}
          </dd>
        </div>
        <div>
          <dt>History generation</dt>
          <dd>{value.generation}</dd>
        </div>
      </dl>
    </section>
  );
}
function localAuditTime(value: string): string {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 19);
}
function Filters({
  resolved,
  navigate,
}: {
  resolved: ResolvedLocation;
  navigate: (fragment: string) => void;
}) {
  const [draft, setDraft] = useState<Record<string, string>>({
    ...resolved.location.query,
  });
  const [error, setError] = useState<string>();
  useEffect(() => {
    setDraft({ ...resolved.location.query });
    setError(undefined);
  }, [resolved.canonicalFragment]);
  return (
    <form
      class="panel domain-panel audit-filters"
      aria-label="Filter audit history"
      onSubmit={(event) => {
        event.preventDefault();
        const query = Object.fromEntries(
          Object.entries(draft).filter(([, value]) => value !== ""),
        );
        const { filter_from: from, filter_until: until } = query;
        if ((from === undefined) !== (until === undefined)) {
          setError("Choose both From and Until, or clear both.");
          return;
        }
        if (from !== undefined && until !== undefined) {
          if (from >= until) {
            setError("Until must be later than From.");
            return;
          }
          if (Date.parse(until) - Date.parse(from) > 366 * 86400000) {
            setError("Choose a time range of at most 366 days.");
            return;
          }
        }
        const fragment = serializeLocation({ ...resolved.location, query });
        if (!validAuditQuery(query) || parseFragment(fragment) === undefined) {
          setError(
            "Check IDs, category/action and the selected dates and times.",
          );
          return;
        }
        setError(undefined);
        navigate(fragment);
      }}
    >
      <h2>Filter audit history</h2>
      <p>
        Filters apply to all retained events, not just loaded rows. Credential
        ID matches the performing operator or a known system initiator.
      </p>
      <div class="audit-filter-grid">
        {auditFilterKeys.map((key) => {
          const name = `filter_${key}`;
          const choices = auditFilterOptions(key, draft.filter_category);
          const timeBound = key === "from" || key === "until";
          const label =
            key === "from"
              ? "From (inclusive, local time)"
              : key === "until"
                ? "Until (exclusive, local time)"
                : sentenceCase(key).replace(/\bid\b/g, "ID");
          return (
            <FormField key={key} id={`audit-${key}`} label={label}>
              {(attributes) =>
                choices === undefined ? (
                  <input
                    {...attributes}
                    type={timeBound ? "datetime-local" : "text"}
                    step={timeBound ? "1" : undefined}
                    value={
                      timeBound
                        ? localAuditTime(draft[name] ?? "")
                        : (draft[name] ?? "")
                    }
                    maxLength={64}
                    onInput={(event) => {
                      const value = event.currentTarget.value;
                      const timestamp = timeBound ? Date.parse(value) : NaN;
                      setDraft({
                        ...draft,
                        [name]: Number.isFinite(timestamp)
                          ? new Date(timestamp)
                              .toISOString()
                              .replace("Z", "000000Z")
                          : value,
                      });
                    }}
                  />
                ) : (
                  <select
                    {...attributes}
                    value={draft[name] ?? ""}
                    onChange={(event) =>
                      setDraft({
                        ...draft,
                        [name]: event.currentTarget.value,
                        ...(key === "category" ? { filter_action: "" } : {}),
                      })
                    }
                  >
                    <option value="">Any</option>
                    {choices.map((choice) => (
                      <option value={choice}>{sentenceCase(choice)}</option>
                    ))}
                  </select>
                )
              }
            </FormField>
          );
        })}
      </div>
      {error !== undefined && (
        <StateNotice state="error" title="Invalid audit filters">
          <p>{error}</p>
        </StateNotice>
      )}
      <div class="form-actions">
        <button type="submit">Apply filters</button>
        <button
          type="button"
          onClick={() => {
            setDraft({});
            setError(undefined);
            navigate("#/audit");
          }}
        >
          Clear filters
        </button>
      </div>
    </form>
  );
}
export function Audit({
  controller,
  resolved,
  view,
  navigate,
}: {
  controller: AuditController;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
  navigate: (fragment: string) => void;
}) {
  const [value, setValue] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setValue), [controller]);
  const snapshot =
    value.viewKey === resolved.canonicalFragment
      ? value
      : empty(resolved.canonicalFragment);
  const panel = view.panels.audit;
  const detail = resolved.location.segments[1] !== undefined;
  return (
    <div class="domain-view audit-view" data-testid="audit-view">
      {detail ? (
        <>
          <nav class="detail-navigation" aria-label="Audit navigation">
            <a href="#/audit">Back to audit history</a>
          </nav>
          <header class="detail-context" data-testid="detail-context">
            <h1 tabindex={-1}>Audit event {resolved.location.segments[1]}</h1>
          </header>
        </>
      ) : (
        <Filters resolved={resolved} navigate={navigate} />
      )}
      {snapshot.notice !== undefined && (
        <StateNotice state="warning" title="Audit traversal changed">
          <p>{snapshot.notice}</p>
        </StateNotice>
      )}
      {panel?.status === "error" && (
        <StateNotice state="error" title="Audit read unavailable">
          <p>
            {snapshot.items.length > 0 || snapshot.item !== undefined
              ? "Previously loaded evidence is stale and may be partial. "
              : ""}
            Use Refresh to try again.
          </p>
        </StateNotice>
      )}
      {snapshot.history !== undefined && <History value={snapshot.history} />}
      {detail ? (
        snapshot.item !== undefined ? (
          <section class="panel domain-panel" aria-label="Audit event detail">
            <div class="panel-heading">
              <h2>
                {snapshot.item.category}.{snapshot.item.action}
              </h2>
              <StatusLabel
                state={
                  snapshot.item.outcome === "succeeded" ? "current" : "warning"
                }
              >
                {sentenceCase(snapshot.item.outcome)}
              </StatusLabel>
            </div>
            <dl class="fact-grid">
              <div>
                <dt>Sequence / phase</dt>
                <dd>
                  {snapshot.item.sequence} · {snapshot.item.phase}
                </dd>
              </div>
              <div>
                <dt>Timestamp</dt>
                <dd>
                  <UserTime value={snapshot.item.timestamp} />
                </dd>
              </div>
              <div>
                <dt>Performer</dt>
                <dd>
                  {sentenceCase(snapshot.item.actor.type)}
                  {snapshot.item.actor.credential !== null && (
                    <>
                      <br />
                      <Credential value={snapshot.item.actor.credential} />
                    </>
                  )}
                </dd>
              </div>
              <div>
                <dt>Initiating credential (not performer)</dt>
                <dd>
                  <Credential value={snapshot.item.initiator} />
                </dd>
              </div>
              <div>
                <dt>Target</dt>
                <dd>
                  {snapshot.item.target.type}:{" "}
                  {snapshot.targetLink === undefined ? (
                    snapshot.item.target.id
                  ) : (
                    <a href={snapshot.targetLink}>{snapshot.item.target.id}</a>
                  )}
                </dd>
              </div>
              <div>
                <dt>Correlation ID</dt>
                <dd>
                  <a
                    href={`#/audit?filter_correlation_id=${snapshot.item.correlation_id}`}
                  >
                    {snapshot.item.correlation_id}
                  </a>
                </dd>
              </div>
              <div>
                <dt>Reason</dt>
                <dd>{snapshot.item.detail.reason ?? "None recorded"}</dd>
              </div>
              <div>
                <dt>Problem</dt>
                <dd>{snapshot.item.detail.problem ?? "None recorded"}</dd>
              </div>
            </dl>
            <p>
              Credential attribution does not identify a named human. Pending or
              unknown outcomes do not prove success, rollback, or permission to
              replay. Only allowlisted safe detail is retained, never secrets,
              raw errors or invocation payloads.
            </p>
            {snapshot.targetUnavailable && (
              <StateNotice state="warning" title="Current target unavailable">
                <p>
                  Audit evidence remains available. The resource link could not
                  be verified.
                </p>
              </StateNotice>
            )}
          </section>
        ) : snapshot.missing ? (
          <StateNotice state="unavailable" title="Audit event not retained">
            <p>A missing event does not prove the action never occurred.</p>
          </StateNotice>
        ) : snapshot.notice === replacementNotice ? (
          <StateNotice
            state="unavailable"
            title="Previous-history detail discarded"
          >
            <p>Return to audit history to start a new traversal.</p>
          </StateNotice>
        ) : panel?.status !== "error" ? (
          <StateNotice state="loading" title="Loading audit event" />
        ) : null
      ) : (
        <section class="panel domain-panel" aria-label="Audit history">
          <div class="panel-heading">
            <h2>Control-plane events</h2>
            <span>Newest first · {snapshot.items.length} loaded</span>
          </div>
          <p class="audit-overflow-note">
            Scroll the table horizontally for performer, target and outcome, or
            open an event for full detail.
          </p>
          {snapshot.history === undefined && panel?.status !== "error" ? (
            <StateNotice state="loading" title="Loading audit history" />
          ) : snapshot.history !== undefined && snapshot.items.length === 0 ? (
            <StateNotice state="empty" title="No retained audit events match" />
          ) : snapshot.items.length > 0 ? (
            <CollectionTable
              caption="Control-plane audit history"
              items={snapshot.items}
              rowKey={(item) => item.id}
              rowTestID="audit-row"
              columns={[
                {
                  key: "event",
                  label: "Event",
                  render: (item) => (
                    <a href={`#/audit/${item.id}`}>
                      {item.category}.{item.action}
                      <br />
                      <small>{item.id}</small>
                    </a>
                  ),
                },
                {
                  key: "time",
                  label: "Time / sequence",
                  render: (item) => (
                    <>
                      <UserTime value={item.timestamp} />
                      <br />
                      {item.sequence}
                    </>
                  ),
                },
                {
                  key: "actor",
                  label: "Performer",
                  render: (item) => (
                    <>
                      {sentenceCase(item.actor.type)}
                      {item.actor.credential !== null && (
                        <>
                          <br />
                          {item.actor.credential.id}
                        </>
                      )}
                      {item.initiator !== null && (
                        <>
                          <br />
                          <small>Initiated by {item.initiator.id}</small>
                        </>
                      )}
                    </>
                  ),
                },
                {
                  key: "target",
                  label: "Target",
                  render: (item) => (
                    <>
                      {item.target.type}
                      <br />
                      {item.target.id}
                    </>
                  ),
                },
                {
                  key: "outcome",
                  label: "Phase / outcome",
                  render: (item) => (
                    <>
                      {item.phase}
                      <br />
                      <StatusLabel
                        state={
                          item.outcome === "succeeded" ? "current" : "warning"
                        }
                      >
                        {sentenceCase(item.outcome)}
                      </StatusLabel>
                    </>
                  ),
                },
              ]}
              hasMore={snapshot.nextCursor !== null}
              loadingMore={snapshot.loadingOlder}
              onLoadMore={() => void controller.loadOlder()}
              loadMoreLabel="Load older audit events"
            />
          ) : null}
          {snapshot.history !== undefined && (
            <p>
              {snapshot.nextCursor !== null
                ? "More matching retained events exist; loaded evidence is partial."
                : "End of this matching retained traversal — not a complete or permanent record."}
            </p>
          )}
        </section>
      )}
    </div>
  );
}
