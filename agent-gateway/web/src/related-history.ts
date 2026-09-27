import { parseFragment, type Destination } from "./location.ts";
import type { SessionClient } from "./session.ts";
import type { ViewCoordinator, ViewReadContext } from "./view.ts";

export class RelatedHistoryChanged extends Error {}

export interface RelatedPage<T> {
  items: T[];
  next: string | null;
  generation?: string;
  notice?: string;
}
export interface RelatedSnapshot<T> extends RelatedPage<T> {
  key: string;
  loaded: boolean;
  loading: boolean;
  error: boolean;
}
const empty = <T>(key = ""): RelatedSnapshot<T> => ({
  key,
  items: [],
  next: null,
  loaded: false,
  loading: false,
  error: false,
});

// Related evidence has an independent panel: a failed read cannot hide its parent.
export class RelatedHistory<T> {
  private value = empty<T>();
  private cursor: string | null = null;
  private listeners = new Set<(value: RelatedSnapshot<T>) => void>();
  private views: ViewCoordinator;
  readonly id: string;
  constructor(
    session: SessionClient,
    views: ViewCoordinator,
    id: string,
    destination: Destination,
    read: (
      context: ViewReadContext,
      cursor: string | null,
      generation?: string,
    ) => Promise<RelatedPage<T>>,
  ) {
    this.views = views;
    this.id = id;
    views.registerPanel({
      id,
      matches: (key) => {
        const location = parseFragment(key);
        return (
          location?.destination === destination &&
          location.segments.length === 2
        );
      },
      invalidations: [],
      shouldRefresh: (reason) =>
        ["navigation", "manual", "panel"].includes(reason),
      read: async (context) => {
        if (this.value.key !== context.viewKey) {
          this.value = empty<T>(context.viewKey);
          this.cursor = null;
        }
        const cursor = context.reason === "panel" ? this.cursor : null;
        this.cursor = null;
        this.value = { ...this.value, loading: true };
        this.emit();
        try {
          const previous = this.value;
          const page = await read(context, cursor, previous.generation);
          if (page.items.length > 50)
            throw new Error("Related page exceeds limit");
          if (
            this.value.generation &&
            page.generation !== this.value.generation
          )
            return {
              ...empty<T>(context.viewKey),
              error: true,
              notice:
                "History changed. Refresh the page before reading related evidence.",
            };
          const combined = cursor
            ? [...previous.items, ...page.items]
            : page.items;
          const capped =
            combined.length > 500 ||
            (combined.length === 500 && page.next !== null);
          return {
            ...page,
            key: context.viewKey,
            items: combined.slice(0, 500),
            next: capped ? null : page.next,
            ...(capped
              ? {
                  notice:
                    "Showing the first 500 related records. Refresh to restart at the newest evidence.",
                }
              : {}),
            loaded: true,
            loading: false,
            error: false,
          };
        } catch (error) {
          if (context.signal.aborted) throw error;
          if (error instanceof RelatedHistoryChanged)
            return {
              ...empty<T>(context.viewKey),
              error: true,
              notice:
                "History changed. Reload this detail page to restart related history.",
            };
          return { ...this.value, loading: false, error: true };
        }
      },
      publish: (value) => {
        this.value = value;
        this.emit();
      },
    });
    session.registerProtectedState(() => {
      this.cursor = null;
      this.value = empty<T>();
      this.emit();
    });
  }
  snapshot(): RelatedSnapshot<T> {
    return this.value;
  }
  subscribe(listener: (value: RelatedSnapshot<T>) => void): () => void {
    this.listeners.add(listener);
    listener(this.value);
    return () => this.listeners.delete(listener);
  }
  refresh(): void {
    if (this.value.loading) return;
    this.cursor = null;
    void this.views.refreshPanel(this.id);
  }
  more(): void {
    if (
      this.value.loading ||
      !this.value.next ||
      this.value.items.length >= 500
    )
      return;
    this.cursor = this.value.next;
    void this.views.refreshPanel(this.id);
  }
  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}
