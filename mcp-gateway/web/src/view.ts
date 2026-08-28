import { SessionClient, type ProtectedContext } from "./session.ts";

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
  | "grant_requests";

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
  pollMilliseconds?: number;
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
  private freshness: Freshness = "stale";
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
      this.markVisiblePanelsStale();
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
    this.markVisiblePanelsStale();
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
      if (panel.invalidations.includes(invalidation.kind)) {
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
        status: previous?.hasValue === true ? "stale" : "loading",
        hasValue: previous?.hasValue === true,
      });
    }
    this.freshness = this.streamConnected ? "stale" : "reconnecting";
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
      !this.visibility.isVisible()
    ) {
      return;
    }
    const timer = setTimeout(() => {
      this.pollTimers.delete(interval);
      if (!this.visibility.isVisible()) return;
      const due = this.visiblePanels()
        .filter((candidate) => candidate.pollMilliseconds === interval)
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
    this.freshness = "stale";
    this.markVisiblePanelsStale();
    this.emit();
  }

  private emit(): void {
    const snapshot = this.snapshot();
    for (const listener of this.listeners) listener(snapshot);
  }
}
