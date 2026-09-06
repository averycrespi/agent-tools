import { useLayoutEffect, useRef, useState } from "preact/hooks";
import {
  parseFragment,
  serializeLocation,
  type ResolvedLocation,
} from "./location.ts";
import {
  SessionClient,
  parseProblem,
  type ProtectedContext,
} from "./session.ts";

export type Freshness = "current" | "stale" | "reconnecting";
export type PanelStatus = "loading" | "current" | "stale" | "error";
export type InvalidationKind =
  | "admin_credentials"
  | "system_status"
  | "backups"
  | "servers"
  | "server_operations"
  | "server_auth_flows"
  | "catalog"
  | "authorization"
  | "grant_requests"
  | "invocations";

export interface Invalidation {
  kind: InvalidationKind;
  resourceID: string | null;
}

export interface ViewReadContext extends ProtectedContext {
  viewKey: string;
  generation: number;
}

export interface PanelSnapshot {
  status: PanelStatus;
  hasValue: boolean;
}

export interface ViewSnapshot {
  viewKey: string;
  generation: number;
  freshness: Freshness;
  panels: Readonly<Record<string, PanelSnapshot>>;
}

export interface ViewPanel<T> {
  id: string;
  matches: (viewKey: string) => boolean;
  invalidations: readonly InvalidationKind[];
  onInvalidation?: (invalidation: Invalidation) => boolean;
  pollMilliseconds?: number;
  shouldPoll?: () => boolean;
  read: (context: ViewReadContext) => Promise<T>;
  publish: (value: T) => void;
}

export interface VisibilitySource {
  isVisible: () => boolean;
  subscribe: (listener: () => void) => () => void;
}

export interface ViewCoordinatorOptions {
  request?: typeof fetch;
  visibility?: VisibilitySource;
  reconnectMilliseconds?: number;
}

type ViewListener = (snapshot: ViewSnapshot) => void;
type RegisteredPanel = ViewPanel<unknown>;

const invalidationKinds = new Set<InvalidationKind>([
  "admin_credentials",
  "system_status",
  "backups",
  "servers",
  "server_operations",
  "server_auth_flows",
  "catalog",
  "authorization",
  "grant_requests",
  "invocations",
]);
const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const coalesceMilliseconds = 250;

function defaultVisibility(): VisibilitySource {
  return {
    isVisible: () =>
      typeof document === "undefined" || document.visibilityState === "visible",
    subscribe: (listener) => {
      if (typeof document === "undefined") return () => {};
      document.addEventListener("visibilitychange", listener);
      return () => document.removeEventListener("visibilitychange", listener);
    },
  };
}

function exactKeys(value: object, keys: readonly string[]): boolean {
  return Object.keys(value).sort().join(",") === [...keys].sort().join(",");
}

export function parseInvalidation(value: unknown): Invalidation | undefined {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    !exactKeys(value, ["kind", "resource_id"]) ||
    !("kind" in value) ||
    typeof value.kind !== "string" ||
    !invalidationKinds.has(value.kind as InvalidationKind) ||
    !("resource_id" in value) ||
    (value.resource_id !== null &&
      (typeof value.resource_id !== "string" ||
        !gatewayID.test(value.resource_id)))
  ) {
    return undefined;
  }
  return {
    kind: value.kind as InvalidationKind,
    resourceID: value.resource_id as string | null,
  };
}

function joinSignals(
  first: AbortSignal,
  second: AbortSignal,
): { signal: AbortSignal; release: () => void } {
  const controller = new AbortController();
  const abort = () => controller.abort();
  if (first.aborted || second.aborted) controller.abort();
  else {
    first.addEventListener("abort", abort, { once: true });
    second.addEventListener("abort", abort, { once: true });
  }
  return {
    signal: controller.signal,
    release: () => {
      first.removeEventListener("abort", abort);
      second.removeEventListener("abort", abort);
    },
  };
}

export interface CollectionPage<T> {
  items: T[];
  nextCursor: string | null;
  totalCount: number;
  offset: number;
}

type CollectionSort = { key: string; direction: "ascending" | "descending" };

export interface CollectionControls {
  filterValues: Record<string, string>;
  sort: CollectionSort | undefined;
  changeFilter: (key: string, value: string) => void;
  changeSort: (key: string) => void;
  resetFilters: () => void;
  status: "loading" | "current" | "error";
  error: string | undefined;
  notice: string | undefined;
  totalCount: number;
  offset: number;
  hasPrevious: boolean;
  hasNext: boolean;
  previous: () => void;
  next: () => void;
}

class StaleCollectionCursor extends Error {}

export async function readCollectionPage<T>(
  session: SessionClient,
  route: string,
  decode: (value: unknown) => T,
  signal: AbortSignal,
): Promise<CollectionPage<T> | undefined> {
  return session.runProtected(async (context) => {
    const response = await fetch(route, {
      credentials: "same-origin",
      redirect: "error",
      signal: AbortSignal.any([signal, context.signal]),
      headers: {
        Accept: "application/json",
        "X-CSRF-Token": context.csrfToken,
      },
    });
    if (await context.sessionLost(response)) return undefined;
    const type = response.headers.get("Content-Type");
    if (type !== "application/json" && type !== "application/problem+json")
      throw new Error("Collection data is unavailable.");
    const value: unknown = await response.json();
    if (!response.ok) {
      if (
        response.status === 409 &&
        parseProblem(value)?.code === "stale_cursor"
      )
        throw new StaleCollectionCursor();
      throw new Error("Collection data is unavailable.");
    }
    if (
      typeof value !== "object" ||
      value === null ||
      !exactKeys(value, ["items", "next_cursor", "total_count", "offset"]) ||
      !("items" in value) ||
      !Array.isArray(value.items) ||
      value.items.length > 50 ||
      !("total_count" in value) ||
      typeof value.total_count !== "number" ||
      !Number.isSafeInteger(value.total_count) ||
      value.total_count < 0 ||
      !("offset" in value) ||
      typeof value.offset !== "number" ||
      !Number.isSafeInteger(value.offset) ||
      value.offset < 0 ||
      value.offset > value.total_count - value.items.length ||
      (value.items.length === 0 &&
        (value.total_count !== 0 || value.offset !== 0)) ||
      !("next_cursor" in value) ||
      (value.next_cursor !== null &&
        (typeof value.next_cursor !== "string" ||
          value.next_cursor.length === 0 ||
          value.next_cursor.length > 512))
    )
      throw new Error("Invalid collection response.");
    if (
      (value.next_cursor !== null) !==
      value.offset + value.items.length < value.total_count
    )
      throw new Error("Invalid collection response.");
    return {
      items: value.items.map(decode),
      nextCursor: value.next_cursor as string | null,
      totalCount: value.total_count,
      offset: value.offset,
    };
  });
}

export function useCollectionPage<T>(
  session: SessionClient,
  resolved: ResolvedLocation,
  view: ViewSnapshot,
  read: (
    query: Readonly<Record<string, string>>,
    cursor: string | null,
    signal: AbortSignal,
  ) => Promise<CollectionPage<T> | undefined>,
  navigate: (fragment: string) => void,
  initialSort?: CollectionSort,
): { items: T[]; controls: CollectionControls } {
  const epoch = session.snapshot().epoch;
  const key = `${epoch}:${resolved.canonicalFragment}`;
  const [history, setHistory] = useState<{
    key: string;
    cursors: (string | null)[];
    index: number;
    notice?: string;
  }>({ key, cursors: [null], index: 0 });
  const active =
    history.key === key ? history : { key, cursors: [null], index: 0 };
  const cursor = active.cursors[active.index] ?? null;
  const serial = useRef(0);
  const reader = useRef(read);
  reader.current = read;
  const [result, setResult] = useState<{
    key: string;
    cursor: string | null;
    page?: CollectionPage<T> | undefined;
    status: CollectionControls["status"];
    error?: string;
  }>({ key, cursor: null, status: "loading" });
  const [queryError, setQueryError] = useState<string>();
  useLayoutEffect(
    () =>
      session.registerProtectedState(() => {
        serial.current += 1;
        setHistory({ key: "", cursors: [null], index: 0 });
        setResult({ key: "", cursor: null, status: "loading" });
        setQueryError(undefined);
      }),
    [session],
  );
  useLayoutEffect(() => {
    const sequence = ++serial.current;
    const controller = new AbortController();
    if (view.viewKey !== resolved.canonicalFragment)
      return () => controller.abort();
    setQueryError(undefined);
    setHistory((previous) =>
      previous.key === key ? previous : { key, cursors: [null], index: 0 },
    );
    setResult((previous) => ({
      key,
      cursor,
      status: "loading",
      page:
        previous.key === key && previous.cursor === cursor
          ? previous.page
          : undefined,
    }));
    const current = () =>
      serial.current === sequence &&
      !controller.signal.aborted &&
      session.snapshot().epoch === epoch;
    void reader
      .current(resolved.location.query, cursor, controller.signal)
      .then((page) => {
        if (current() && page !== undefined)
          setResult({ key, cursor, page, status: "current" });
      })
      .catch((error: unknown) => {
        if (!current()) return;
        if (error instanceof StaleCollectionCursor && cursor !== null) {
          setHistory({
            key,
            cursors: [null],
            index: 0,
            notice:
              "The previous page expired or changed. Restarted at the first page.",
          });
          return;
        }
        setResult({
          key,
          cursor,
          status: "error",
          error: "Collection data is unavailable. Use Refresh to try again.",
        });
      });
    return () => {
      controller.abort();
      serial.current += 1;
    };
  }, [session, key, cursor, view.generation, view.viewKey]);
  const matching = result.key === key && result.cursor === cursor;
  const status =
    queryError !== undefined
      ? "error"
      : matching && view.viewKey === resolved.canonicalFragment
        ? result.status
        : "loading";
  const page = matching ? result.page : undefined;
  const query = resolved.location.query;
  const sort: CollectionSort | undefined =
    query.sort === undefined
      ? initialSort
      : {
          key: query.sort,
          direction:
            query.direction === "descending" ? "descending" : "ascending",
        };
  const changeQuery = (next: Record<string, string>) => {
    const fragment = serializeLocation({ ...resolved.location, query: next });
    if (parseFragment(fragment) === undefined) {
      setQueryError(
        "Filters must contain at most 256 UTF-8 bytes each, no control characters, and fit the location limit.",
      );
      return;
    }
    setQueryError(undefined);
    navigate(fragment);
  };
  const filterValues = Object.fromEntries(
    Object.entries(query)
      .filter(([name]) => name.startsWith("filter_"))
      .map(([name, value]) => [name.slice(7), value]),
  );
  const ready = status === "current";
  return {
    items: page?.items ?? [],
    controls: {
      filterValues,
      sort,
      status,
      error: queryError ?? (matching ? result.error : undefined),
      notice: active.notice,
      totalCount: page?.totalCount ?? 0,
      offset: page?.offset ?? 0,
      hasPrevious: ready && active.index > 0,
      hasNext: ready && page?.nextCursor != null,
      previous: () => {
        if (ready && active.index > 0)
          setHistory({ ...active, index: active.index - 1 });
      },
      next: () => {
        if (ready && page?.nextCursor != null)
          setHistory({
            ...active,
            cursors: [
              ...active.cursors.slice(0, active.index + 1),
              page.nextCursor,
            ],
            index: active.index + 1,
          });
      },
      changeFilter: (name, value) => {
        const next = { ...query };
        if (value.trim() === "") delete next[`filter_${name}`];
        else next[`filter_${name}`] = value;
        changeQuery(next);
      },
      changeSort: (name) =>
        changeQuery({
          ...query,
          sort: name,
          direction:
            sort?.key === name && sort.direction === "ascending"
              ? "descending"
              : "ascending",
        }),
      resetFilters: () =>
        changeQuery(
          Object.fromEntries(
            Object.entries(query).filter(
              ([name]) => !name.startsWith("filter_"),
            ),
          ),
        ),
    },
  };
}

export class ViewCoordinator {
  private readonly session: SessionClient;
  private readonly request: typeof fetch;
  private readonly visibility: VisibilitySource;
  private readonly reconnectMilliseconds: number;
  private readonly listeners = new Set<ViewListener>();
  private readonly panels = new Map<string, RegisteredPanel>();
  private readonly panelState = new Map<string, PanelSnapshot>();
  private readonly panelGenerations = new Map<string, number>();
  private readonly readControllers = new Map<string, AbortController>();
  private readonly pollTimers = new Map<
    number,
    ReturnType<typeof setTimeout>
  >();
  private readonly pendingInvalidations = new Set<string>();
  private readonly unsubscribeVisibility: () => void;
  private readonly unregisterProtectedState: () => void;
  private viewKey = "#/sign-in";
  private generation = 0;
  private freshness: Freshness = "reconnecting";
  private active = false;
  private streamConnected = false;
  private streamController: AbortController | undefined;
  private reconnectTimer: (() => void) | undefined;
  private invalidationTimer: ReturnType<typeof setTimeout> | undefined;

  constructor(session: SessionClient, options: ViewCoordinatorOptions = {}) {
    this.session = session;
    this.request = (options.request ?? fetch).bind(globalThis);
    this.visibility = options.visibility ?? defaultVisibility();
    this.reconnectMilliseconds = options.reconnectMilliseconds ?? 1000;
    this.unsubscribeVisibility = this.visibility.subscribe(() =>
      this.visibilityChanged(),
    );
    this.unregisterProtectedState = this.session.registerProtectedState(() =>
      this.reset(),
    );
  }

  snapshot(): ViewSnapshot {
    return {
      viewKey: this.viewKey,
      generation: this.generation,
      freshness: this.freshness,
      panels: Object.fromEntries(this.panelState),
    };
  }

  subscribe(listener: ViewListener): () => void {
    this.listeners.add(listener);
    listener(this.snapshot());
    return () => this.listeners.delete(listener);
  }

  registerPanel<T>(panel: ViewPanel<T>): () => void {
    if (this.panels.has(panel.id)) throw new Error("duplicate view panel");
    const registered = panel as RegisteredPanel;
    this.panels.set(panel.id, registered);
    this.panelState.set(panel.id, { status: "loading", hasValue: false });
    if (this.active && panel.matches(this.viewKey)) {
      void this.refresh([panel.id]);
    }
    return () => {
      this.panels.delete(panel.id);
      this.panelState.delete(panel.id);
      this.panelGenerations.delete(panel.id);
      this.abortRead(panel.id);
      this.clearPolls();
      for (const visiblePanel of this.visiblePanels())
        this.schedulePoll(visiblePanel);
      this.emit();
    };
  }

  activate(viewKey: string): void {
    if (!this.active) {
      this.active = true;
      this.viewKey = viewKey;
      this.abortReads();
      this.clearPolls();
      this.markVisiblePanelsLoading();
      this.connect();
      return;
    }
    this.navigate(viewKey);
    this.connect();
  }

  navigate(viewKey: string): void {
    if (this.viewKey === viewKey && this.generation !== 0) return;
    this.viewKey = viewKey;
    this.abortReads();
    this.clearPolls();
    this.markVisiblePanelsLoading();
    if (this.active) void this.refresh();
    else this.emit();
  }

  manualRefresh(): void {
    if (this.active) void this.refresh();
  }

  refreshPanel(panelID: string): Promise<void> {
    if (!this.active) return Promise.resolve();
    return this.refresh([panelID]);
  }

  invalidate(invalidation: Invalidation): void {
    if (!this.active) return;
    let matched = false;
    for (const panel of this.visiblePanels()) {
      if (
        panel.invalidations.includes(invalidation.kind) &&
        (panel.onInvalidation?.(invalidation) ?? true)
      ) {
        this.pendingInvalidations.add(panel.id);
        matched = true;
      }
    }
    if (!matched && this.panels.size === 0) matched = true;
    if (!matched || this.invalidationTimer !== undefined) return;
    this.invalidationTimer = setTimeout(() => {
      this.invalidationTimer = undefined;
      const panels = [...this.pendingInvalidations];
      this.pendingInvalidations.clear();
      void this.refresh(panels);
    }, coalesceMilliseconds);
  }

  close(): void {
    this.active = false;
    this.reset();
    this.unsubscribeVisibility();
    this.unregisterProtectedState();
    this.listeners.clear();
  }

  private async refresh(panelIDs?: readonly string[]): Promise<void> {
    if (!this.active) return;
    const selected =
      panelIDs === undefined
        ? this.visiblePanels()
        : panelIDs
            .map((id) => this.panels.get(id))
            .filter(
              (panel): panel is RegisteredPanel =>
                panel !== undefined && panel.matches(this.viewKey),
            );
    const generation = this.generation + 1;
    this.generation = generation;
    const viewKey = this.viewKey;
    for (const panel of selected) {
      this.abortRead(panel.id);
      this.panelGenerations.set(panel.id, generation);
      const previous = this.panelState.get(panel.id);
      this.panelState.set(panel.id, {
        status:
          previous?.hasValue === true
            ? this.streamConnected
              ? "current"
              : "stale"
            : "loading",
        hasValue: previous?.hasValue === true,
      });
    }
    this.freshness = this.streamConnected ? "current" : "reconnecting";
    this.emit();
    if (selected.length === 0) {
      if (this.streamConnected) this.freshness = "current";
      this.emit();
      return;
    }
    await Promise.all(
      selected.map((panel) => this.readPanel(panel, viewKey, generation)),
    );
    if (this.current(viewKey, generation) && this.streamConnected) {
      this.freshness = "current";
      this.emit();
    }
  }

  private async readPanel(
    panel: RegisteredPanel,
    viewKey: string,
    generation: number,
  ): Promise<void> {
    const controller = new AbortController();
    this.readControllers.set(panel.id, controller);
    try {
      const result = await this.session.runProtected(async (context) => {
        const joined = joinSignals(context.signal, controller.signal);
        try {
          return await panel.read({
            ...context,
            signal: joined.signal,
            viewKey,
            generation,
          });
        } finally {
          joined.release();
        }
      });
      if (
        result === undefined ||
        !this.panelCurrent(panel.id, viewKey, generation)
      )
        return;
      panel.publish(result);
      this.panelState.set(panel.id, {
        status: this.streamConnected ? "current" : "stale",
        hasValue: true,
      });
    } catch {
      if (
        !controller.signal.aborted &&
        this.panelCurrent(panel.id, viewKey, generation)
      ) {
        const previous = this.panelState.get(panel.id);
        this.panelState.set(panel.id, {
          status: "error",
          hasValue: previous?.hasValue === true,
        });
      }
    } finally {
      if (this.readControllers.get(panel.id) === controller)
        this.readControllers.delete(panel.id);
      if (this.panelCurrent(panel.id, viewKey, generation)) {
        this.schedulePoll(panel);
        this.emit();
      }
    }
  }

  private connect(): void {
    if (
      !this.active ||
      this.streamController !== undefined ||
      this.session.snapshot().lifecycle !== "authenticated"
    ) {
      return;
    }
    this.reconnectTimer?.();
    this.reconnectTimer = undefined;
    this.streamConnected = false;
    this.freshness = "reconnecting";
    this.markVisiblePanelsStale();
    this.emit();
    const streamController = new AbortController();
    this.streamController = streamController;
    void this.session
      .runProtected(async (context) => {
        const joined = joinSignals(context.signal, streamController.signal);
        try {
          const response = await this.request("/api/v1/events", {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "X-CSRF-Token": context.csrfToken,
            },
            body: "{}",
            credentials: "same-origin",
            redirect: "error",
            signal: joined.signal,
          });
          if (await context.sessionLost(response)) return;
          if (
            response.status !== 200 ||
            response.headers.get("Content-Type") !== "text/event-stream" ||
            response.body === null
          ) {
            throw new Error("event stream rejected");
          }
          this.streamConnected = true;
          void this.refresh();
          await this.consumeEvents(response.body, joined.signal);
        } finally {
          joined.release();
        }
      })
      .catch(() => undefined)
      .finally(() => {
        if (this.streamController !== streamController) return;
        this.streamController = undefined;
        this.streamConnected = false;
        if (
          !this.active ||
          this.session.snapshot().lifecycle !== "authenticated"
        )
          return;
        this.freshness = "reconnecting";
        this.markVisiblePanelsStale();
        this.emit();
        this.reconnectTimer = this.session.scheduleProtected(
          () => this.connect(),
          this.reconnectMilliseconds,
        );
      });
  }

  private async consumeEvents(
    stream: ReadableStream<Uint8Array>,
    signal: AbortSignal,
  ): Promise<void> {
    const reader = stream.getReader();
    const decoder = new TextDecoder("utf-8", { fatal: true });
    let buffer = "";
    let eventName = "";
    let data = "";
    try {
      while (!signal.aborted) {
        const result = await reader.read();
        if (result.done) return;
        buffer += decoder.decode(result.value, { stream: true });
        if (buffer.length > 64 * 1024) throw new Error("event frame too large");
        for (;;) {
          const newline = buffer.indexOf("\n");
          if (newline === -1) break;
          const line = buffer.slice(0, newline).replace(/\r$/, "");
          buffer = buffer.slice(newline + 1);
          if (line === "") {
            if (eventName === "invalidate" && data !== "") {
              let parsed: unknown;
              try {
                parsed = JSON.parse(data) as unknown;
              } catch {
                throw new Error("invalid event JSON");
              }
              const invalidation = parseInvalidation(parsed);
              if (invalidation === undefined) throw new Error("invalid event");
              this.invalidate(invalidation);
            }
            eventName = "";
            data = "";
          } else if (line.startsWith("event: ")) {
            eventName = line.slice(7);
          } else if (line.startsWith("data: ")) {
            if (data !== "") throw new Error("duplicate event data");
            data = line.slice(6);
          } else if (!line.startsWith(":")) {
            throw new Error("invalid event field");
          }
        }
      }
    } finally {
      await reader.cancel().catch(() => undefined);
      reader.releaseLock();
    }
  }

  private visiblePanels(): RegisteredPanel[] {
    return [...this.panels.values()].filter((panel) =>
      panel.matches(this.viewKey),
    );
  }

  private current(viewKey: string, generation: number): boolean {
    return (
      this.active && this.viewKey === viewKey && this.generation === generation
    );
  }

  private panelCurrent(
    panelID: string,
    viewKey: string,
    generation: number,
  ): boolean {
    return (
      this.active &&
      this.viewKey === viewKey &&
      this.panelGenerations.get(panelID) === generation
    );
  }

  private schedulePoll(panel: RegisteredPanel): void {
    const interval = panel.pollMilliseconds;
    if (
      !this.active ||
      interval === undefined ||
      interval <= 0 ||
      this.pollTimers.has(interval) ||
      !panel.matches(this.viewKey) ||
      panel.shouldPoll?.() === false ||
      !this.visibility.isVisible()
    ) {
      return;
    }
    const timer = setTimeout(() => {
      this.pollTimers.delete(interval);
      if (!this.visibility.isVisible()) return;
      const due = this.visiblePanels()
        .filter(
          (candidate) =>
            candidate.pollMilliseconds === interval &&
            candidate.shouldPoll?.() !== false,
        )
        .map((candidate) => candidate.id);
      if (due.length > 0) void this.refresh(due);
    }, interval);
    this.pollTimers.set(interval, timer);
  }

  private visibilityChanged(): void {
    if (!this.visibility.isVisible()) {
      this.clearPolls();
      return;
    }
    for (const panel of this.visiblePanels()) this.schedulePoll(panel);
  }

  private markVisiblePanelsStale(): void {
    for (const panel of this.visiblePanels()) {
      const previous = this.panelState.get(panel.id);
      this.panelState.set(panel.id, {
        status: previous?.hasValue === true ? "stale" : "loading",
        hasValue: previous?.hasValue === true,
      });
    }
  }

  private markVisiblePanelsLoading(): void {
    for (const panel of this.visiblePanels())
      this.panelState.set(panel.id, { status: "loading", hasValue: false });
  }

  private abortRead(panelID: string): void {
    this.readControllers.get(panelID)?.abort();
    this.readControllers.delete(panelID);
  }

  private abortReads(): void {
    for (const controller of this.readControllers.values()) controller.abort();
    this.readControllers.clear();
  }

  private clearPolls(): void {
    for (const timer of this.pollTimers.values()) clearTimeout(timer);
    this.pollTimers.clear();
  }

  private reset(): void {
    this.active = false;
    this.streamController?.abort();
    this.streamController = undefined;
    this.streamConnected = false;
    this.abortReads();
    this.clearPolls();
    this.reconnectTimer?.();
    this.reconnectTimer = undefined;
    if (this.invalidationTimer !== undefined)
      clearTimeout(this.invalidationTimer);
    this.invalidationTimer = undefined;
    this.pendingInvalidations.clear();
    this.freshness = "reconnecting";
    for (const panelID of this.panelState.keys())
      this.panelState.set(panelID, { status: "loading", hasValue: false });
    this.emit();
  }

  private emit(): void {
    const snapshot = this.snapshot();
    for (const listener of this.listeners) listener(snapshot);
  }
}
