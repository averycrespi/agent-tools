import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
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
  useDebouncedInput,
  type OperationalState,
} from "./primitives";
import { parseProblem, type SessionClient } from "./session";
import { UserTime } from "./time";
import type { ViewCoordinator, ViewReadContext, ViewSnapshot } from "./view";

const replacementNotice =
  "Audit history may have been replaced by restore. Newer local events may have been discarded. Previous-history state was discarded; these histories must not be combined.";
const targetRoutes: Readonly<Record<string, readonly [string, string]>> = {
  server: ["servers", "servers"],
  principal: ["principals", "principals"],
  grant: ["grants", "grants"],
  grant_request: ["grant-requests", "requests"],
};
function outcomeState(outcome: string): OperationalState {
  return outcome === "succeeded"
    ? "current"
    : outcome === "failed"
      ? "error"
      : outcome === "pending"
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
  return `/api/v1/audit-events?${values.toString()}`;
}
export class AuditController {
  private value = empty();
  private readonly listeners = new Set<(value: AuditSnapshot) => void>();
  private continuation: string | null = null;
  private knownGeneration: string | undefined;
  private readonly unavailableTargets = new Set<string>();
  constructor(
    session: SessionClient,
    private readonly views: ViewCoordinator,
  ) {
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
    return `#/${route[1]}/${item.target.id}`;
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
        `/api/v1/${route[0]}/${item.target.id}`,
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
        targetLink: `#/${route[1]}/${item.target.id}`,
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
        <dl class="fact-grid">
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
const primaryFilters = ["category", "action", "outcome"];
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
  const [advanced, setAdvanced] = useState(
    Object.keys(draft).some((key) => !primaryFilters.includes(key.slice(7))),
  );
  useLayoutEffect(() => {
    applied.current = resolved.location.query;
    // Our own valid field update must not erase unrelated invalid drafts.
    if (ownNavigation.current !== resolved.canonicalFragment) {
      setDraft({ ...resolved.location.query });
      if (
        Object.keys(resolved.location.query).some(
          (key) => !primaryFilters.includes(key.slice(7)),
        )
      )
        setAdvanced(true);
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
  const activeAdvanced = Object.keys(applied.current).filter(
    (key) => !primaryFilters.includes(key.slice(7)),
  ).length;
  const pending =
    serializeLocation({ ...resolved.location, query: compactQuery(draft) }) !==
    serializeLocation({ ...resolved.location, query: applied.current });
  const clear = () => {
    setDraft({});
    applied.current = {};
    ownNavigation.current = "#/audit";
    navigate("#/audit");
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
          : sentenceCase(key).replace(/\bid\b/g, "ID");
    return (
      <FormField
        key={key}
        id={`audit-${key}`}
        label={label}
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
                  {sentenceCase(choice)}
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
      class="panel domain-panel audit-filters"
      aria-label="Filter audit history"
      onSubmit={(event) => event.preventDefault()}
    >
      <h2>Filter audit history</h2>
      <p>
        Filters apply automatically to all retained events, not just loaded
        rows.
      </p>
      <div class="audit-filter-grid">{primaryFilters.map(field)}</div>
      <details
        open={advanced}
        onToggle={(event) => setAdvanced(event.currentTarget.open)}
      >
        <summary>
          More filters{activeAdvanced > 0 ? ` · ${activeAdvanced} active` : ""}
          {Object.keys(errors).length > 0 ? " · check draft values" : ""}
        </summary>
        <p>
          Credential ID matches the performing operator or a known system
          initiator.
        </p>
        <div class="audit-filter-grid">
          {auditFilterKeys
            .filter((key) => !primaryFilters.includes(key))
            .map(field)}
        </div>
      </details>
      {pending && (
        <p role="status">
          Draft changes are not yet applied.{" "}
          {Object.keys(errors).length > 0
            ? "Check the fields under More filters; valid independent changes still apply."
            : "Text filters apply after a short pause."}
        </p>
      )}
      <p class="audit-applied" aria-label="Applied audit filters">
        Applied filters:{" "}
        {Object.entries(applied.current)
          .map(
            ([key, value]) =>
              `${sentenceCase(key.slice(7)).replace(/\bid\b/g, "ID")}: ${value}`,
          )
          .join(" · ") || "None"}
      </p>
      <div class="form-actions">
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
              Back to audit history
            </a>
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
      {detail ? (
        snapshot.item !== undefined ? (
          <section class="panel domain-panel" aria-label="Audit event detail">
            <div class="panel-heading">
              <h2>
                {snapshot.item.category}.{snapshot.item.action}
              </h2>
              <StatusLabel state={outcomeState(snapshot.item.outcome)}>
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
                  {sentenceCase(snapshot.item.target.type)}:{" "}
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
                <button type="button" onClick={() => navigate("#/audit")}>
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
              items={snapshot.items}
              rowKey={(item) => item.id}
              rowTestID="audit-row"
              columns={[
                {
                  key: "time",
                  label: "Time",
                  render: (item) => <UserTime value={item.timestamp} />,
                },
                {
                  key: "event",
                  label: "Event type",
                  render: (item) => (
                    <>
                      {item.category}.{item.action}
                    </>
                  ),
                },
                {
                  key: "id",
                  label: "Event ID",
                  render: (item) => (
                    <a
                      href={serializeLocation({
                        ...resolved.location,
                        segments: ["audit", item.id],
                      })}
                    >
                      {item.id}
                    </a>
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
                      {sentenceCase(item.target.type)}
                      <br />
                      {controller.listTarget(item) === undefined ? (
                        item.target.id
                      ) : (
                        <a href={controller.listTarget(item)}>
                          {item.target.id}
                        </a>
                      )}
                    </>
                  ),
                },
                {
                  key: "outcome",
                  label: "Outcome",
                  render: (item) => (
                    <>
                      <StatusLabel state={outcomeState(item.outcome)}>
                        {sentenceCase(item.outcome)}
                      </StatusLabel>
                      <br />
                      <small>{sentenceCase(item.phase)}</small>
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
