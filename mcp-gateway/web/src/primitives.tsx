import type { ComponentChildren, RefObject } from "preact";
import { useEffect, useMemo, useRef, useState } from "preact/hooks";

export type OperationalState =
  | "current"
  | "stale"
  | "loading"
  | "reconnecting"
  | "empty"
  | "error"
  | "warning"
  | "unavailable";

const stateSymbols: Readonly<Record<OperationalState, string>> = {
  current: "✓",
  stale: "↻",
  loading: "…",
  reconnecting: "↻",
  empty: "—",
  error: "!",
  warning: "△",
  unavailable: "×",
};

export function StatusLabel({
  state,
  children,
  testID,
}: {
  state: OperationalState;
  children: ComponentChildren;
  testID?: string;
}) {
  return (
    <span
      class={`status-label ${state}`}
      data-state={state}
      data-testid={testID}
    >
      <span class="status-symbol" aria-hidden="true">
        {stateSymbols[state]}
      </span>
      <span>{children}</span>
    </span>
  );
}

export function StateNotice({
  state,
  title,
  children,
}: {
  state: Exclude<OperationalState, "current">;
  title: string;
  children?: ComponentChildren;
}) {
  const urgent = state === "error" || state === "unavailable";
  return (
    <section
      class={`state-notice ${state}`}
      role={urgent ? "alert" : "status"}
      aria-live={urgent ? "assertive" : "polite"}
      aria-atomic="true"
    >
      <StatusLabel state={state}>{title}</StatusLabel>
      {children !== undefined && <div class="state-detail">{children}</div>}
    </section>
  );
}

export function InertJSON({
  value,
  label = "JSON value",
}: {
  value: unknown;
  label?: string;
}) {
  let serialized: string;
  try {
    serialized = JSON.stringify(value, null, 2) ?? "null";
  } catch {
    serialized = "Unable to display this JSON value.";
  }
  return (
    <pre class="inert-json" aria-label={label} tabindex={0}>
      <code>{serialized}</code>
    </pre>
  );
}

export function ComparisonTable({
  caption,
  children,
}: {
  caption: string;
  children: ComponentChildren;
}) {
  return (
    <div class="table-region" role="region" aria-label={caption} tabindex={0}>
      <table>
        <caption class="visually-hidden">{caption}</caption>
        {children}
      </table>
    </div>
  );
}

export interface CollectionColumn<T> {
  key: string;
  label: string;
  render: (item: T) => ComponentChildren;
  sortValue?: (item: T) => string | number;
  class?: string;
}

export function CollectionTable<T>({
  caption,
  items,
  columns,
  rowKey,
  rowTestID,
  filterLabel,
  filterValue,
  emptyTitle = "No results",
  hasMore = false,
  loadingMore = false,
  onLoadMore,
  loadMoreLabel = "Load more",
}: {
  caption: string;
  items: readonly T[];
  columns: readonly CollectionColumn<T>[];
  rowKey: (item: T) => string;
  rowTestID?: string;
  filterLabel?: string;
  filterValue?: (item: T) => string;
  emptyTitle?: string;
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  loadMoreLabel?: string;
}) {
  const [filter, setFilter] = useState("");
  const [sort, setSort] = useState<{
    key: string;
    direction: "ascending" | "descending";
  }>();
  const visible = useMemo(() => {
    const needle = filter.trim().toLocaleLowerCase();
    const filtered =
      needle === "" || filterValue === undefined
        ? [...items]
        : items.filter((item) =>
            filterValue(item).toLocaleLowerCase().includes(needle),
          );
    if (sort === undefined) return filtered;
    const column = columns.find((candidate) => candidate.key === sort.key);
    if (column?.sortValue === undefined) return filtered;
    return filtered.sort((left, right) => {
      const a = column.sortValue!(left);
      const b = column.sortValue!(right);
      const order =
        typeof a === "number" && typeof b === "number"
          ? a - b
          : String(a).localeCompare(String(b));
      return sort.direction === "ascending" ? order : -order;
    });
  }, [columns, filter, filterValue, items, sort]);
  const changeSort = (key: string) =>
    setSort((current) => ({
      key,
      direction:
        current?.key === key && current.direction === "ascending"
          ? "descending"
          : "ascending",
    }));
  return (
    <div class="collection-table">
      {filterLabel !== undefined && filterValue !== undefined && (
        <label class="table-filter">
          <span>{filterLabel}</span>
          <input
            type="search"
            value={filter}
            onInput={(event) => setFilter(event.currentTarget.value)}
          />
        </label>
      )}
      <ComparisonTable caption={caption}>
        <thead>
          <tr>
            {columns.map((column) => (
              <th
                key={column.key}
                scope="col"
                class={column.class}
                aria-sort={
                  sort?.key === column.key ? sort.direction : undefined
                }
              >
                {column.sortValue === undefined ? (
                  column.label
                ) : (
                  <button
                    class="sort-button"
                    type="button"
                    onClick={() => changeSort(column.key)}
                  >
                    {column.label}
                    <span aria-hidden="true">
                      {sort?.key === column.key
                        ? sort.direction === "ascending"
                          ? " ↑"
                          : " ↓"
                        : " ↕"}
                    </span>
                  </button>
                )}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {visible.map((item) => (
            <tr key={rowKey(item)} data-testid={rowTestID}>
              {columns.map((column, index) =>
                index === 0 ? (
                  <th
                    key={column.key}
                    scope="row"
                    class={column.class}
                    data-label={column.label}
                  >
                    {column.render(item)}
                  </th>
                ) : (
                  <td
                    key={column.key}
                    class={column.class}
                    data-label={column.label}
                  >
                    {column.render(item)}
                  </td>
                ),
              )}
            </tr>
          ))}
        </tbody>
      </ComparisonTable>
      {visible.length === 0 && <StateNotice state="empty" title={emptyTitle} />}
      {hasMore && onLoadMore !== undefined && (
        <button type="button" disabled={loadingMore} onClick={onLoadMore}>
          {loadingMore ? "Loading…" : loadMoreLabel}
        </button>
      )}
    </div>
  );
}

interface FieldControlAttributes {
  id: string;
  required?: true;
  "aria-describedby"?: string;
  "aria-invalid"?: true;
}

export function FormField({
  id,
  label,
  hint,
  error,
  optional = false,
  required = false,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  optional?: boolean;
  required?: boolean;
  children: (attributes: FieldControlAttributes) => ComponentChildren;
}) {
  const descriptions = [
    hint === undefined ? undefined : `${id}-hint`,
    error === undefined ? undefined : `${id}-error`,
  ]
    .filter((value): value is string => value !== undefined)
    .join(" ");
  const attributes: FieldControlAttributes = { id };
  if (required) attributes.required = true;
  if (descriptions !== "") attributes["aria-describedby"] = descriptions;
  if (error !== undefined) attributes["aria-invalid"] = true;
  return (
    <div class="form-field">
      <label for={id}>
        {label}
        {optional && <span class="optional-label"> (optional)</span>}
      </label>
      {hint !== undefined && (
        <span class="field-hint" id={`${id}-hint`}>
          {hint}
        </span>
      )}
      {children(attributes)}
      {error !== undefined && (
        <span class="field-error" id={`${id}-error`} role="alert">
          {error}
        </span>
      )}
    </div>
  );
}

export function TypedConfirmationDialog({
  id,
  open,
  title,
  consequence,
  expected,
  value,
  confirmLabel,
  returnFocus,
  onValue,
  onConfirm,
  onCancel,
}: {
  id: string;
  open: boolean;
  title: string;
  consequence: ComponentChildren;
  expected: string;
  value: string;
  confirmLabel: string;
  returnFocus: RefObject<HTMLElement>;
  onValue: (value: string) => void;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const node = dialog.current;
    if (node === null) return;
    if (open && !node.open) node.showModal();
    if (!open && node.open) node.close();
  }, [open]);

  return (
    <dialog
      ref={dialog}
      class="confirmation-dialog"
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-consequence`}
      onCancel={(event) => {
        event.preventDefault();
        onCancel();
      }}
      onClose={() => returnFocus.current?.focus()}
    >
      <form method="dialog">
        <span class="panel-code">TYPE TO CONFIRM</span>
        <h2 id={`${id}-title`}>{title}</h2>
        <div id={`${id}-consequence`} class="dialog-consequence">
          {consequence}
        </div>
        <FormField
          id={`${id}-value`}
          label={`Type ${expected} to confirm`}
          hint="The immutable namespace must match exactly."
        >
          {(attributes) => (
            <input
              {...attributes}
              data-testid={`${id}-value`}
              value={value}
              autoComplete="off"
              spellcheck={false}
              onInput={(event) => onValue(event.currentTarget.value)}
            />
          )}
        </FormField>
        <div class="dialog-actions">
          <button type="button" data-testid={`${id}-cancel`} onClick={onCancel}>
            Cancel
          </button>
          <button
            class="danger-action"
            type="button"
            data-testid={`${id}-submit`}
            disabled={value !== expected}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </form>
    </dialog>
  );
}

export function ConfirmationDialog({
  id,
  open,
  title,
  consequence,
  confirmLabel,
  destructive = false,
  returnFocus,
  onConfirm,
  onCancel,
}: {
  id: string;
  open: boolean;
  title: string;
  consequence: ComponentChildren;
  confirmLabel: string;
  destructive?: boolean;
  returnFocus: RefObject<HTMLElement>;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const node = dialog.current;
    if (node === null) return;
    if (open && !node.open) node.showModal();
    if (!open && node.open) node.close();
  }, [open]);

  const restoreFocus = () => {
    returnFocus.current?.focus();
  };

  return (
    <dialog
      ref={dialog}
      class="confirmation-dialog"
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-consequence`}
      onCancel={(event) => {
        event.preventDefault();
        onCancel();
      }}
      onClose={restoreFocus}
    >
      <form method="dialog">
        <span class="panel-code">CONFIRM</span>
        <h2 id={`${id}-title`}>{title}</h2>
        <div id={`${id}-consequence`} class="dialog-consequence">
          {consequence}
        </div>
        <div class="dialog-actions">
          <button type="button" data-testid={`${id}-cancel`} onClick={onCancel}>
            Cancel
          </button>
          <button
            class={destructive ? "danger-action" : "primary-action"}
            type="button"
            data-testid={`${id}-submit`}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </form>
    </dialog>
  );
}
