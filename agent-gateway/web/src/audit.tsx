import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import {
  auditFilterOptions,
  auditActions,
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
  destinationPaths,
  parseFragment,
  serializeLocation,
  type Destination,
  type ResolvedLocation,
} from "./location";
import {
  CollectionTable,
  LoadedHistorySummary,
  TableIdentity,
  FormField,
  sentenceCase,
  StateNotice,
  StatusLabel,
  useDebouncedInput,
  type OperationalState,
} from "./primitives";
import { parseProblem, type SessionClient } from "./session";
import { UserTime } from "./time";
import { RelatedHistory, RelatedHistoryChanged } from "./related-history";
import type { ViewCoordinator, ViewReadContext, ViewSnapshot } from "./view";

const replacementNotice =
  "Audit history may have been replaced by restore. Newer local events may have been discarded. Previous-history state was discarded; these histories must not be combined.";
const targetRoutes: Readonly<Record<string, readonly [string, Destination]>> = {
  server: ["mcp/servers", "servers"],
  principal: ["principals", "principals"],
  grant: ["mcp/grants", "grants"],
  grant_request: ["mcp/grant-requests", "requests"],
};
function outcomeState(outcome: string): OperationalState {
  return outcome === "succeeded"
    ? "current"
    : outcome === "failed"
      ? "error"
      : outcome === "pending" || outcome === "rejected"
        ? "neutral"
        : "warning";
}
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
  olderError: boolean;
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
    olderError: false,
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
  return `/api/v2/audit-events?${values.toString()}`;
}
export class AuditController {
  readonly related: RelatedHistory<AuditSummary>;
  private value = empty();
  private readonly listeners = new Set<(value: AuditSnapshot) => void>();
  private continuation: string | null = null;
  private knownGeneration: string | undefined;
  private readonly unavailableTargets = new Set<string>();
  constructor(
    session: SessionClient,
    private readonly views: ViewCoordinator,
  ) {
    this.related = new RelatedHistory(
      session,
      views,
      "audit-related",
      "audit",
      async (context, cursor, generation) => {
        const id = parseFragment(context.viewKey)!.segments[1];
        const expected = generation ?? this.knownGeneration;
        const parent = await readResponse(
          context,
          `/api/v2/audit-events/${id}${expected ? `?generation=${expected}` : ""}`,
        );
        if (parent.problem === "audit_history_replaced")
          throw new RelatedHistoryChanged();
        if (parent.problem) throw new Error("Related history unavailable");
        const item = decodeAuditItem(parent.value);
        if (item.event.id !== id) throw new Error("Mismatched parent");
        const result = await readResponse(
          context,
          listPath(
            { filter_correlation_id: item.event.correlation_id },
            cursor,
            item.history.generation,
          ),
        );
        if (result.problem) throw new RelatedHistoryChanged();
        const page = decodeAuditPage(result.value);
        if (page.history.generation !== item.history.generation)
          throw new RelatedHistoryChanged();
        if (
          page.items.some(
            (row) => row.correlation_id !== item.event.correlation_id,
          )
        )
          throw new Error("Mismatched correlation");
        return {
          items: [...page.items],
          next: page.next_cursor,
          generation: page.history.generation,
        };
      },
    );
    views.registerPanel({
      id: "audit",
      matches: (key) => parseFragment(key)?.destination === "audit",
      invalidations: [],
      read: async (context) => {
        const cursor = this.continuation;
        try {
          return await this.read(context);
        } catch (error) {
          if (
            cursor !== null &&
            !context.signal.aborted &&
            this.value.viewKey === context.viewKey &&
            this.value.nextCursor === cursor
          )
            return { ...this.value, loadingOlder: false, olderError: true };
          throw error;
        }
      },
      publish: (result) => {
        if (
          result.history !== undefined &&
          result.history.generation !== this.knownGeneration
        )
          this.unavailableTargets.clear();
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
      this.unavailableTargets.clear();
      this.emit();
    });
  }
  listTarget(item: AuditSummary): string | undefined {
    const route = targetRoutes[item.target.type];
    const key = `${item.target.type}/${item.target.id}`;
    if (
      route === undefined ||
      this.unavailableTargets.has(key) ||
      this.value.items.some(
        (event) =>
          event.target.type === item.target.type &&
          event.target.id === item.target.id &&
          event.action === "delete" &&
          event.outcome === "succeeded",
      )
    )
      return undefined;
    return `#/${destinationPaths[route[1]]}/${item.target.id}`;
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
    this.value = { ...this.value, loadingOlder: true, olderError: false };
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
        `/api/v2/audit-events/${id}${expected === undefined ? "" : `?generation=${expected}`}`,
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
    const route = targetRoutes[item.target.type];
    const key = `${item.target.type}/${item.target.id}`;
    const unavailable = () => {
      if (
        !context.signal.aborted &&
        this.views.snapshot().viewKey === context.viewKey
      )
        this.unavailableTargets.add(key);
    };
    if (route === undefined)
      return { targetLink: undefined, targetUnavailable: false };
    try {
      const response = await get(
        context,
        `/api/v2/${route[0]}/${item.target.id}`,
      );
      if (response.status === 404) {
        unavailable();
        return { targetLink: undefined, targetUnavailable: false };
      }
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
      if ("deleted_at" in resource && resource.deleted_at !== null) {
        unavailable();
        return { targetLink: undefined, targetUnavailable: false };
      }
      if (
        !context.signal.aborted &&
        this.views.snapshot().viewKey === context.viewKey
      )
        this.unavailableTargets.delete(key);
      return {
        targetLink: `#/${destinationPaths[route[1]]}/${item.target.id}`,
        targetUnavailable: false,
      };
    } catch {
      unavailable();
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
      <details>
        <summary>Retention details</summary>
        <dl class="detail-facts">
          <div>
            <dt>Oldest retained boundary</dt>
            <dd>
              {value.oldest_retained === null ? (
                "None — history is empty"
              ) : (
                <>
                  <UserTime value={value.oldest_retained.timestamp} /> ·
                  sequence {value.oldest_retained.sequence}
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
      </details>
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
const filterLabel = (key: string) =>
  key === "category"
    ? "Event"
    : key === "actor_type"
      ? "Performer type"
      : sentenceCase(key).replace(/\bid\b/g, "ID");
const idFilters = ["credential_id", "target_id", "correlation_id"];
function compactQuery(query: Readonly<Record<string, string>>) {
  return Object.fromEntries(
    Object.entries(query).filter(([, value]) => value !== ""),
  );
}
function dateError(
  draft: Readonly<Record<string, string>>,
): string | undefined {
  const from = draft.filter_from || undefined;
  const until = draft.filter_until || undefined;
  if ((from === undefined) !== (until === undefined))
    return "Choose both From and Until, or clear both.";
  if (from === undefined || until === undefined) return undefined;
  if (from >= until) return "Until must be later than From.";
  if (!validAuditQuery({ filter_from: from, filter_until: until }))
    return "Choose valid dates and a time range of at most 366 days.";
  return undefined;
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
  const applied = useRef(resolved.location.query);
  const ownNavigation = useRef<string>();

  useLayoutEffect(() => {
    applied.current = resolved.location.query;
    // Our own valid field update must not erase unrelated invalid drafts.
    if (ownNavigation.current !== resolved.canonicalFragment) {
      setDraft({ ...resolved.location.query });
    }
    ownNavigation.current = undefined;
  }, [resolved.canonicalFragment]);
  const apply = (patch: Record<string, string>) => {
    const query = compactQuery({ ...applied.current, ...patch });
    const fragment = serializeLocation({ ...resolved.location, query });
    if (
      !validAuditQuery(query) ||
      parseFragment(fragment) === undefined ||
      fragment ===
        serializeLocation({ ...resolved.location, query: applied.current })
    )
      return;
    applied.current = query;
    ownNavigation.current = fragment;
    navigate(fragment);
  };
  useDebouncedInput(draft, (settled) => {
    const patch: Record<string, string> = {};
    for (const key of idFilters) {
      const name = `filter_${key}`;
      const value = settled[name] ?? "";
      if (validAuditQuery(compactQuery({ [name]: value }))) patch[name] = value;
    }
    apply(patch);
  });
  const dates = dateError(draft);
  const errors: Record<string, string> = {};
  for (const key of idFilters) {
    const value = draft[`filter_${key}`] ?? "";
    if (!validAuditQuery(compactQuery({ [`filter_${key}`]: value })))
      errors[key] =
        "Enter a complete 26-character Gateway ID, or clear this field.";
  }
  if (dates !== undefined) errors.from = errors.until = dates;
  const pending =
    serializeLocation({ ...resolved.location, query: compactQuery(draft) }) !==
    serializeLocation({ ...resolved.location, query: applied.current });
  const clear = () => {
    setDraft({});
    applied.current = {};
    ownNavigation.current = "#/audit-log";
    navigate("#/audit-log");
  };
  const field = (key: string) => {
    const name = `filter_${key}`;
    const choices = auditFilterOptions(key, draft.filter_category);
    const timeBound = key === "from" || key === "until";
    const label =
      key === "from"
        ? "From (inclusive, local time)"
        : key === "until"
          ? "Until (exclusive, local time)"
          : filterLabel(key);
    const appliedValue = applied.current[name] ?? "";
    const hint = [
      key === "credential_id"
        ? "Matches the performing operator or a known system initiator."
        : "",
      (draft[name] ?? "") !== appliedValue
        ? `Current results: ${appliedValue === "" ? "Any" : timeBound ? localAuditTime(appliedValue) : appliedValue}.`
        : "",
    ]
      .filter(Boolean)
      .join(" ");
    return (
      <FormField
        key={key}
        id={`audit-${key}`}
        label={label}
        {...(hint === "" ? {} : { hint })}
        {...(errors[key] === undefined ? {} : { error: errors[key] })}
      >
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
                const next = {
                  ...draft,
                  [name]: Number.isFinite(timestamp)
                    ? new Date(timestamp).toISOString().replace("Z", "000000Z")
                    : value,
                };
                setDraft(next);
                if (timeBound && dateError(next) === undefined)
                  apply({
                    filter_from: next.filter_from ?? "",
                    filter_until: next.filter_until ?? "",
                  });
              }}
            />
          ) : (
            <select
              {...attributes}
              value={draft[name] ?? ""}
              onChange={(event) => {
                const patch = { [name]: event.currentTarget.value };
                if (
                  key === "category" &&
                  !auditFilterOptions("action", patch[name])?.includes(
                    draft.filter_action ?? "",
                  )
                )
                  patch.filter_action = "";
                setDraft({ ...draft, ...patch });
                apply(patch);
              }}
            >
              <option value="">Any</option>
              {choices.map((choice) => (
                <option key={choice} value={choice}>
                  {choice === "principal" ? "Agent" : sentenceCase(choice)}
                </option>
              ))}
            </select>
          )
        }
      </FormField>
    );
  };
  return (
    <form
      class="audit-filters"
      aria-label="Filter audit history"
      onSubmit={(event) => event.preventDefault()}
    >
      {resolved.location.query.filter_correlation_id && (
        <div class="inline-actions">
          Related events:{" "}
          <code>{resolved.location.query.filter_correlation_id}</code>
          <button
            type="button"
            onClick={() => {
              setDraft({ ...draft, filter_correlation_id: "" });
              apply({ filter_correlation_id: "" });
            }}
          >
            Remove correlation filter
          </button>
        </div>
      )}
      <div class="audit-filter-grid">
        <div class="audit-filter-group audit-date-bounds">
          {field("from")}
          {field("until")}
        </div>
        <FormField id="audit-event" label="Event">
          {(attributes) => (
            <select
              {...attributes}
              value={
                draft.filter_category
                  ? `${draft.filter_category}.${draft.filter_action || "*"}`
                  : draft.filter_action
                    ? `*.${draft.filter_action}`
                    : ""
              }
              onChange={(event) => {
                const [category = "", action = ""] =
                  event.currentTarget.value.split(".");
                const patch = {
                  filter_category: category === "*" ? "" : category,
                  filter_action: action === "*" ? "" : action,
                };
                setDraft({ ...draft, ...patch });
                apply(patch);
              }}
            >
              <option value="">Any event</option>
              {Object.entries(auditActions)
                .toSorted(([a], [b]) => a.localeCompare(b))
                .map(([category, actions]) => (
                  <optgroup label={sentenceCase(category)}>
                    <option value={`${category}.*`}>
                      {category} · All actions
                    </option>
                    {actions.map((action) => (
                      <option value={`${category}.${action}`}>
                        {category}.{action}
                      </option>
                    ))}
                  </optgroup>
                ))}
              <optgroup label="Action across categories">
                {auditFilterOptions("action")!.map((action) => (
                  <option value={`*.${action}`}>Any category · {action}</option>
                ))}
              </optgroup>
            </select>
          )}
        </FormField>
        <div
          class="audit-filter-group"
          role="group"
          aria-label="Performer filters"
        >
          {field("actor_type")}
          {field("credential_id")}
        </div>
        <div
          class="audit-filter-group"
          role="group"
          aria-label="Target filters"
        >
          {field("target_type")}
          {field("target_id")}
        </div>
        {field("outcome")}
      </div>
      {pending && (
        <p role="status">
          Draft changes are not yet applied.{" "}
          {Object.keys(errors).length > 0
            ? "Check the marked fields; valid independent changes still apply."
            : "Text filters apply after a short pause."}
        </p>
      )}
      <div class="audit-filter-actions">
        <button
          type="button"
          disabled={
            Object.keys(compactQuery(draft)).length === 0 &&
            Object.keys(applied.current).length === 0
          }
          onClick={clear}
        >
          Clear filters
        </button>
      </div>
    </form>
  );
}
function RelatedAudit({
  controller,
  resolved,
  view,
  selected,
}: {
  controller: RelatedHistory<AuditSummary>;
  selected: AuditEvent;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
}) {
  const [value, setValue] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setValue), [controller]);
  const current = value.key === view.viewKey ? value : undefined;
  return (
    <section
      aria-label="Related events"
      class="panel domain-panel related-history"
    >
      <div class="panel-heading">
        <h2>Related events</h2>
        <button
          type="button"
          disabled={current?.loading}
          onClick={() => controller.refresh()}
        >
          Refresh related events
        </button>
      </div>
      {current?.error && (
        <StateNotice state="error" title="Related events unavailable">
          Previously loaded events may be stale. Refresh to try again.
        </StateNotice>
      )}
      {current?.notice && (
        <StateNotice state="warning" title={current.notice} />
      )}
      {!current?.loaded && !current?.error && (
        <StateNotice state="loading" title="Loading related events…" />
      )}
      {current?.loaded && (
        <CollectionTable
          caption="Related control-plane events"
          layout="activity"
          rowHeaderKey="event"
          rowKey={(row) => row.id}
          items={(current.items.some((row) => row.id === selected.id)
            ? current.items
            : [...current.items, selected]
          ).toSorted((a, b) =>
            BigInt(a.sequence) > BigInt(b.sequence)
              ? -1
              : BigInt(a.sequence) < BigInt(b.sequence)
                ? 1
                : 0,
          )}
          emptyTitle="No related events recorded"
          localStale={current.error}
          columns={[
            {
              key: "time",
              label: "Time",
              role: "time",
              render: (row) => <UserTime value={row.timestamp} />,
            },
            {
              key: "event",
              label: "Event",
              role: "identity",
              render: (row) => (
                <TableIdentity
                  primary={
                    <>
                      <a
                        aria-current={
                          row.id === resolved.location.segments[1]
                            ? "page"
                            : undefined
                        }
                        href={serializeLocation({
                          ...resolved.location,
                          segments: ["audit", row.id],
                        })}
                      >
                        {row.category}.{row.action}
                      </a>
                      {row.id === resolved.location.segments[1] && (
                        <div class="muted">Selected event</div>
                      )}
                      <span class="table-secondary">
                        Sequence {row.sequence}
                      </span>
                    </>
                  }
                  secondary={row.id}
                />
              ),
            },
            {
              key: "actor",
              label: "Performer",
              role: "text",
              render: (row) => (
                <>
                  <TableIdentity
                    primary={sentenceCase(row.actor.type)}
                    secondary={row.actor.credential?.id}
                  />
                  {row.initiator !== null && (
                    <span class="table-secondary">
                      Initiated by{" "}
                      <span class="technical-value">{row.initiator.id}</span>
                    </span>
                  )}
                </>
              ),
            },
            {
              key: "target",
              label: "Target",
              role: "relation",
              render: (row) => (
                <TableIdentity
                  primary={
                    row.currentTargetName ||
                    (row.target.type === "principal"
                      ? "Agent"
                      : sentenceCase(row.target.type))
                  }
                  secondary={row.target.id}
                />
              ),
            },
            {
              key: "phase",
              label: "Phase",
              role: "text",
              render: (row) => sentenceCase(row.phase),
            },
            {
              key: "outcome",
              label: "Outcome",
              role: "status",
              render: (row) => (
                <StatusLabel state={outcomeState(row.outcome)}>
                  {sentenceCase(row.outcome)}
                </StatusLabel>
              ),
            },
          ]}
        />
      )}
      <div class="collection-pagination">
        {current?.loaded && (
          <>
            <LoadedHistorySummary
              count={current.items.length}
              singular="event"
              plural="events"
              matching={false}
              stale={current.error}
            />
            {!current.items.some((row) => row.id === selected.id) && (
              <span class="table-filter-summary">Plus the selected event</span>
            )}
          </>
        )}
        {current?.next && current.items.length < 500 && (
          <button
            type="button"
            disabled={current.loading}
            onClick={() => controller.more()}
          >
            Load more related events
          </button>
        )}
      </div>
    </section>
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
            <a
              href={serializeLocation({
                ...resolved.location,
                segments: ["audit"],
              })}
            >
              Back to Audit Log
            </a>
          </nav>
          <header class="detail-context" data-testid="detail-context">
            <div class="detail-context-heading">
              <h1 tabindex={-1}>
                {snapshot.item
                  ? `${sentenceCase(snapshot.item.action)} ${snapshot.item.category === "principal" ? "agent" : snapshot.item.category.replaceAll("_", " ")}`
                  : "Audit event"}
              </h1>
              {snapshot.item !== undefined && (
                <StatusLabel state={outcomeState(snapshot.item.outcome)}>
                  {sentenceCase(snapshot.item.outcome)}
                </StatusLabel>
              )}
            </div>
            <p class="technical-value">{resolved.location.segments[1]}</p>
          </header>
        </>
      ) : null}
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
      {detail ? (
        snapshot.item !== undefined ? (
          <>
            <section class="detail-section" aria-label="Audit event detail">
              <div class="panel-heading">
                <h2>Event details</h2>
              </div>
              <h3>Event and attribution</h3>
              <dl class="detail-facts">
                <div>
                  <dt>Event</dt>
                  <dd>
                    {snapshot.item.category}.{snapshot.item.action}
                  </dd>
                </div>
                <div>
                  <dt>Correlation ID</dt>
                  <dd class="technical-value">
                    {snapshot.item.correlation_id}
                  </dd>
                </div>
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
                    {snapshot.item.target.type === "principal"
                      ? "Agent"
                      : sentenceCase(snapshot.item.target.type)}
                    :{" "}
                    {snapshot.targetLink === undefined ? (
                      snapshot.item.currentTargetName || snapshot.item.target.id
                    ) : (
                      <a href={snapshot.targetLink}>
                        {snapshot.item.currentTargetName ||
                          snapshot.item.target.id}
                      </a>
                    )}
                    {snapshot.item.currentTargetName && (
                      <span class="table-identifier">
                        {snapshot.item.target.id}
                      </span>
                    )}
                  </dd>
                </div>
              </dl>
              {(snapshot.item.detail.reason !== null ||
                snapshot.item.detail.problem !== null) && (
                <>
                  <h3>Recorded diagnostics</h3>
                  <dl class="detail-facts">
                    {snapshot.item.detail.reason !== null && (
                      <div>
                        <dt>Reason</dt>
                        <dd>{snapshot.item.detail.reason}</dd>
                      </div>
                    )}
                    {snapshot.item.detail.problem !== null && (
                      <div>
                        <dt>Problem</dt>
                        <dd>{snapshot.item.detail.problem}</dd>
                      </div>
                    )}
                  </dl>
                </>
              )}
              <p>
                Credential attribution does not identify a named human. Pending
                or unknown outcomes do not prove success, rollback, or
                permission to replay.
              </p>
              {snapshot.targetUnavailable && (
                <StateNotice state="warning" title="Current target unavailable">
                  <p>
                    Audit evidence remains available. The resource link could
                    not be verified.
                  </p>
                </StateNotice>
              )}
            </section>
            <RelatedAudit
              controller={controller.related}
              selected={snapshot.item}
              resolved={resolved}
              view={view}
            />
          </>
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
          <Filters resolved={resolved} navigate={navigate} />
          {snapshot.history === undefined && panel?.status !== "error" ? (
            <StateNotice state="loading" title="Loading audit history" />
          ) : snapshot.history !== undefined &&
            snapshot.items.length === 0 &&
            panel?.status !== "error" ? (
            <StateNotice
              state="empty"
              title={
                Object.keys(resolved.location.query).length > 0
                  ? "No matching audit events"
                  : "No audit events yet"
              }
            >
              {Object.keys(resolved.location.query).length > 0 ? (
                <button type="button" onClick={() => navigate("#/audit-log")}>
                  Clear filters
                </button>
              ) : (
                <p>
                  Retained history is bounded; this does not establish that no
                  earlier actions occurred.
                </p>
              )}
            </StateNotice>
          ) : snapshot.items.length > 0 ? (
            <CollectionTable
              caption="Control-plane audit history"
              layout="activity"
              rowHeaderKey="event"
              items={snapshot.items}
              rowKey={(item) => item.id}
              rowTestID="audit-row"
              columns={[
                {
                  key: "time",
                  label: "Time",
                  role: "time",
                  render: (item) => <UserTime value={item.timestamp} />,
                },
                {
                  key: "event",
                  label: "Event",
                  role: "identity",
                  render: (item) => (
                    <TableIdentity
                      primary={
                        <a
                          href={serializeLocation({
                            ...resolved.location,
                            segments: ["audit", item.id],
                          })}
                        >
                          {item.category}.{item.action}
                        </a>
                      }
                      secondary={item.id}
                    />
                  ),
                },
                {
                  key: "actor",
                  label: "Performer",
                  role: "relation",
                  render: (item) => (
                    <>
                      {sentenceCase(item.actor.type)}
                      {item.actor.credential !== null && (
                        <span class="table-identifier">
                          {item.actor.credential.id}
                        </span>
                      )}
                      {item.initiator !== null && (
                        <span class="table-secondary">
                          Initiated by{" "}
                          <span class="technical-value">
                            {item.initiator.id}
                          </span>
                        </span>
                      )}
                    </>
                  ),
                },
                {
                  key: "target",
                  label: "Target",
                  role: "relation",
                  render: (item) => (
                    <>
                      {item.currentTargetName ||
                        (item.target.type === "principal"
                          ? "Agent"
                          : sentenceCase(item.target.type))}
                      <span class="table-identifier">
                        {controller.listTarget(item) === undefined ? (
                          item.target.id
                        ) : (
                          <a href={controller.listTarget(item)}>
                            {item.target.id}
                          </a>
                        )}
                      </span>
                    </>
                  ),
                },
                {
                  key: "outcome",
                  label: "Outcome",
                  role: "status",
                  render: (item) => (
                    <>
                      <StatusLabel state={outcomeState(item.outcome)}>
                        {sentenceCase(item.outcome)}
                      </StatusLabel>
                      <span class="table-secondary">
                        {sentenceCase(item.phase)}
                      </span>
                    </>
                  ),
                },
              ]}
              hasMore={snapshot.nextCursor !== null}
              loadingMore={snapshot.loadingOlder}
              onLoadMore={() => void controller.loadOlder()}
              loadMoreLabel="Load older audit events"
              historySummary
              historyMatching={Object.keys(resolved.location.query).length > 0}
              itemNames={{ singular: "event", plural: "events" }}
              localStale={panel?.status === "error" || snapshot.olderError}
            />
          ) : null}
          {snapshot.history !== undefined && snapshot.items.length === 0 && (
            <LoadedHistorySummary
              count={0}
              singular="event"
              plural="events"
              matching={Object.keys(resolved.location.query).length > 0}
              stale={panel?.status === "error"}
            />
          )}
          {snapshot.olderError && (
            <p role="alert">
              Older audit results unavailable. Loaded rows were retained; use
              Load older audit events to retry.
            </p>
          )}
        </section>
      )}
      {snapshot.history !== undefined && <History value={snapshot.history} />}
    </div>
  );
}
