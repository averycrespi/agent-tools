import { parseFragment } from "./location.ts";
import { decodeGitTrafficPage, type GitTraffic } from "./git-contract.ts";
import { parseProblem, type SessionClient } from "./session.ts";
import type { ViewCoordinator, ViewReadContext } from "./view.ts";

export const olderResultsLost =
  "Showing the latest entries. Older results could not be continued.";
const empty = () => ({
  key: "",
  items: [] as GitTraffic[],
  next: null as string | null,
  live: true,
  paused: false,
  loaded: false,
  error: false,
  olderError: false,
  loadingOlder: false,
  notice: "",
});
export class GitTrafficController {
  private value = empty();
  private serial = 0;
  private olderPosition = false;
  private continuation: string | null = null;
  private listeners = new Set<(value: ReturnType<typeof empty>) => void>();
  private views: ViewCoordinator;
  constructor(session: SessionClient, views: ViewCoordinator) {
    this.views = views;
    views.registerPanel({
      id: "git-traffic",
      matches: (key) => {
        const l = parseFragment(key);
        return l?.destination === "git-traffic" && l.segments.length === 1;
      },
      invalidations: ["system_status"],
      shouldRefresh: (reason) =>
        !["invalidation", "reconnect", "poll"].includes(reason) ||
        (this.value.live && !this.value.paused),
      read: (context) => this.read(context),
      publish: (result) => {
        if (result.serial !== this.serial) return;
        this.value = result.value;
        this.emit();
      },
    });
    session.registerProtectedState(() => {
      this.serial++;
      this.olderPosition = false;
      this.continuation = null;
      this.value = empty();
      this.emit();
    });
  }
  snapshot() {
    return this.value;
  }
  subscribe(listener: (value: ReturnType<typeof empty>) => void) {
    this.listeners.add(listener);
    listener(this.value);
    return () => {
      this.listeners.delete(listener);
    };
  }
  private emit() {
    for (const listener of this.listeners) listener(this.value);
  }
  setLive(live: boolean) {
    this.views.cancelPanelRead("git-traffic");
    this.serial++;
    this.value = { ...this.value, live, loadingOlder: false };
    this.continuation = null;
    this.emit();
    if (!this.value.loaded || (live && !this.value.paused)) this.resume();
  }
  resume() {
    this.serial++;
    this.olderPosition = false;
    this.continuation = null;
    this.value = {
      ...this.value,
      paused: false,
      loadingOlder: false,
      notice: "",
    };
    this.emit();
    void this.views.refreshPanel("git-traffic");
  }
  async older() {
    if (
      this.value.error ||
      this.value.loadingOlder ||
      this.value.next === null ||
      this.value.items.length >= 500 ||
      this.value.key !== this.views.snapshot().viewKey ||
      this.views.snapshot().panels["git-traffic"]?.refreshing
    )
      return;
    this.continuation = this.value.next;
    this.value = {
      ...this.value,
      paused: true,
      loadingOlder: true,
      olderError: false,
    };
    this.emit();
    await this.views.refreshPanel("git-traffic");
  }
  private async read(context: ViewReadContext) {
    const location = parseFragment(context.viewKey)!;
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
    const serial = this.serial;
    const previous = this.value;
    const cursor = context.reason === "panel" ? this.continuation : null;
    this.continuation = null;
    const active = () =>
      !context.signal.aborted &&
      serial === this.serial &&
      context.viewKey === this.views.snapshot().viewKey;
    const get = async (cursor: string | null) => {
      const params = new URLSearchParams({ limit: "50" });
      for (const [key, value] of Object.entries(location.query))
        params.set(key.slice(7), value);
      if (location.query.filter_repository)
        params.set(
          "search_locale",
          Intl.DateTimeFormat().resolvedOptions().locale,
        );
      if (cursor) params.set("cursor", cursor);
      const response = await fetch(`/api/v2/git/traffic?${params}`, {
        credentials: "same-origin",
        redirect: "error",
        signal: context.signal,
        headers: {
          Accept: "application/json",
          "X-CSRF-Token": context.csrfToken,
        },
      });
      if ((await context.sessionLost(response)) || !active())
        throw new Error("Superseded");
      const text = await response.text();
      if (text.length > 1024 * 1024 || !active())
        throw new Error("Unavailable");
      const value: unknown = JSON.parse(text);
      if (
        response.status === 409 &&
        response.headers.get("Content-Type") === "application/problem+json" &&
        parseProblem(value)?.code === "stale_cursor"
      )
        return null;
      if (
        !response.ok ||
        response.headers.get("Content-Type") !== "application/json"
      )
        throw new Error("Unavailable");
      return decodeGitTrafficPage(value);
    };
    let append = cursor !== null;
    let notice =
      !append && this.olderPosition ? olderResultsLost : previous.notice;
    try {
      let page = await get(cursor);
      if (page === null && append) {
        append = false;
        notice =
          olderResultsLost +
          " Git history changed; previous results were discarded.";
        this.olderPosition = false;
        this.value = {
          ...previous,
          items: [],
          next: null,
          loaded: false,
          notice,
        };
        this.emit();
        page = await get(null);
      }
      if (page === null || !active()) throw new Error("Unavailable");
      if (
        append &&
        (previous.items.length + page.items.length > 500 ||
          page.items.some((row) =>
            previous.items.some(
              (prior) => prior.admission.id === row.admission.id,
            ),
          ))
      )
        throw new Error("Repeated evidence");
      this.olderPosition = append;
      return {
        serial,
        value: {
          ...this.value,
          items: append ? [...previous.items, ...page.items] : page.items,
          next: page.nextCursor,
          loaded: true,
          error: false,
          olderError: false,
          loadingOlder: false,
          notice,
        },
      };
    } catch (error) {
      if (!active()) throw error;
      return {
        serial,
        value: {
          ...this.value,
          error: !append,
          olderError: append,
          loadingOlder: false,
        },
      };
    }
  }
}
