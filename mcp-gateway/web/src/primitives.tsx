import type { ComponentChildren, RefObject } from "preact";
import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "preact/hooks";
import { parseFragment, serializeLocation } from "./location";
import type { CollectionControls } from "./view";

export function containsControlCharacters(value: string): boolean {
  return /\p{Cc}/u.test(value);
}

export function sentenceCase(value: string): string {
  const words = value.replaceAll("_", " ").replaceAll("-", " ");
  return words.replace(/^./, (letter) => letter.toLocaleUpperCase());
}

export type OperationalState =
  | "current"
  | "stale"
  | "loading"
  | "reconnecting"
  | "empty"
  | "error"
  | "warning"
  | "unavailable";

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
      {children}
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

export function CompactRecord({
  primaryLabel,
  primary,
  fields,
  warning,
  testID,
}: {
  primaryLabel: string;
  primary: ComponentChildren;
  fields: readonly { label: string; value: ComponentChildren }[];
  warning?: ComponentChildren;
  testID?: string;
}) {
  return (
    <li class="compact-record" data-testid={testID}>
      <div class="compact-record-primary">
        <span>{primaryLabel}</span>
        {primary}
      </div>
      <dl class="compact-record-fields">
        {fields.map((field) => (
          <div key={field.label}>
            <dt>{field.label}</dt>
            <dd>{field.value}</dd>
          </div>
        ))}
      </dl>
      {warning !== undefined && (
        <p class="warning-copy compact-record-warning">{warning}</p>
      )}
    </li>
  );
}

export interface CollectionColumn<T> {
  key: string;
  label: string;
  render: (item: T) => ComponentChildren;
  sortValue?: (item: T) => string | number;
  class?: string;
}

export type CollectionFilter<T> =
  | {
      key: string;
      label: string;
      type: "text";
      value: (item: T) => string;
      literalValues?: (item: T) => readonly string[];
      placeholder?: string;
    }
  | {
      key: string;
      label: string;
      type: "select";
      value: (item: T) => string;
      options: readonly { value: string; label: string }[];
    };

function normalizedSearchText(value: string): string {
  return value.normalize("NFKD").replace(/\p{M}/gu, "").toLocaleLowerCase();
}

function withinOneEdit(left: string, right: string): boolean {
  if (Math.abs(left.length - right.length) > 1) return false;
  if (left === right) return true;
  let leftIndex = 0;
  let rightIndex = 0;
  let edits = 0;
  while (leftIndex < left.length && rightIndex < right.length) {
    if (left[leftIndex] === right[rightIndex]) {
      leftIndex += 1;
      rightIndex += 1;
      continue;
    }
    edits += 1;
    if (edits > 1) return false;
    if (
      left.length === right.length &&
      left[leftIndex] === right[rightIndex + 1] &&
      left[leftIndex + 1] === right[rightIndex]
    ) {
      leftIndex += 2;
      rightIndex += 2;
    } else if (left.length > right.length) leftIndex += 1;
    else if (right.length > left.length) rightIndex += 1;
    else {
      leftIndex += 1;
      rightIndex += 1;
    }
  }
  return edits + (left.length - leftIndex) + (right.length - rightIndex) <= 1;
}

function searchMatches(value: string, query: string): boolean {
  const candidate = normalizedSearchText(value);
  return normalizedSearchText(query)
    .split(/\s+/u)
    .filter(Boolean)
    .every((token) => {
      if (candidate.includes(token)) return true;
      if (token.length < 4 || /\d/u.test(token)) return false;
      return candidate
        .split(/[^\p{L}\p{N}]+/u)
        .filter(Boolean)
        .some((word) => withinOneEdit(word, token));
    });
}

function collectionFilterValues<T>(
  filters: readonly CollectionFilter<T>[],
): Record<string, string> {
  const location = parseFragment(window.location.hash);
  if (location === undefined) return {};
  return Object.fromEntries(
    filters.flatMap((filter) => {
      const value = location.query[`filter_${filter.key}`];
      return value === undefined ? [] : [[filter.key, value]];
    }),
  );
}

function replaceCollectionFilter(key: string, value: string): void {
  const location = parseFragment(window.location.hash);
  if (location === undefined) return;
  const query = { ...location.query };
  const queryKey = `filter_${key}`;
  if (value.trim() === "") delete query[queryKey];
  else query[queryKey] = value;
  window.history.replaceState(
    null,
    "",
    serializeLocation({ ...location, query }),
  );
}

export function CollectionTable<T>({
  caption,
  items,
  columns,
  rowKey,
  rowTestID,
  filters = [],
  initialSort,
  emptyTitle = "No results",
  hasMore = false,
  loadingMore = false,
  onLoadMore,
  loadMoreLabel = "Load more",
  itemNames = { singular: "item", plural: "items" },
  remote,
}: {
  caption: string;
  items: readonly T[];
  columns: readonly CollectionColumn<T>[];
  rowKey: (item: T) => string;
  rowTestID?: string;
  filters?: readonly CollectionFilter<T>[];
  initialSort?: { key: string; direction: "ascending" | "descending" };
  emptyTitle?: string;
  hasMore?: boolean;
  loadingMore?: boolean;
  onLoadMore?: () => void;
  loadMoreLabel?: string;
  itemNames?: { singular: string; plural: string };
  remote?: CollectionControls;
}) {
  const [localFilters, setFilterValues] = useState<Record<string, string>>(() =>
    collectionFilterValues(filters),
  );
  const [localSort, setSort] = useState(initialSort);
  const filterValues = remote?.filterValues ?? localFilters;
  const sort = remote === undefined ? localSort : remote.sort;
  useEffect(() => {
    if (remote !== undefined) return;
    const synchronize = () => setFilterValues(collectionFilterValues(filters));
    window.addEventListener("hashchange", synchronize);
    return () => window.removeEventListener("hashchange", synchronize);
  }, [filters, remote]);
  const visible = useMemo(() => {
    if (remote !== undefined) return items;
    const filtered = items.filter((item) =>
      filters.every((filter) => {
        const selected = filterValues[filter.key]?.trim() ?? "";
        if (selected === "") return true;
        const value = filter.value(item);
        return filter.type === "text"
          ? (filter
              .literalValues?.(item)
              .some((literal) => literal.includes(selected)) ??
              false) ||
              searchMatches(value, selected)
          : value === selected;
      }),
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
  }, [columns, filterValues, filters, items, sort, remote]);
  const changeSort = (key: string) => {
    if (remote !== undefined) {
      remote.changeSort(key);
      return;
    }
    setSort((current) => ({
      key,
      direction:
        current?.key === key && current.direction === "ascending"
          ? "descending"
          : "ascending",
    }));
  };
  const hasActiveFilters = Object.values(filterValues).some(
    (value) => value.trim() !== "",
  );
  return (
    <div
      class="collection-table"
      aria-busy={remote?.status === "loading" ? "true" : undefined}
    >
      {filters.length > 0 && (
        <div
          class={
            remote === undefined
              ? "table-filters"
              : "table-filters collection-query-filters"
          }
          role="group"
          aria-label={`${caption} filters`}
        >
          <span class="table-filters-label">Filter:</span>
          {filters.map((filter) =>
            filter.type === "text" ? (
              <input
                key={filter.key}
                type="search"
                aria-label={filter.label}
                placeholder={filter.placeholder ?? `${filter.label}…`}
                value={filterValues[filter.key] ?? ""}
                onInput={(event) => {
                  const value = event.currentTarget.value;
                  if (remote !== undefined) {
                    remote.changeFilter(filter.key, value);
                    return;
                  }
                  replaceCollectionFilter(filter.key, value);
                  setFilterValues((current) => ({
                    ...current,
                    [filter.key]: value,
                  }));
                }}
              />
            ) : (
              <select
                key={filter.key}
                aria-label={filter.label}
                value={filterValues[filter.key] ?? ""}
                onChange={(event) => {
                  const value = event.currentTarget.value;
                  if (remote !== undefined) {
                    remote.changeFilter(filter.key, value);
                    return;
                  }
                  replaceCollectionFilter(filter.key, value);
                  setFilterValues((current) => ({
                    ...current,
                    [filter.key]: value,
                  }));
                }}
              >
                <option value="">{filter.label}: any</option>
                {filter.options.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            ),
          )}
          <button
            class="text-button"
            type="button"
            disabled={!hasActiveFilters}
            onClick={() => {
              if (remote !== undefined) {
                remote.resetFilters();
                return;
              }
              for (const filter of filters)
                replaceCollectionFilter(filter.key, "");
              setFilterValues({});
            }}
          >
            Reset
          </button>
          {remote === undefined && (
            <output class="table-filter-summary" aria-live="polite">
              Showing {visible.length} of {items.length}
              {hasMore ? " loaded" : ""}
            </output>
          )}
        </div>
      )}
      {remote?.notice !== undefined && (
        <StateNotice state="warning" title={remote.notice} />
      )}
      {remote?.error !== undefined && (
        <StateNotice state="error" title={remote.error} />
      )}
      {remote !== undefined && (
        <div class="collection-pagination">
          <nav class="inline-actions" aria-label={`${caption} pagination`}>
            <button
              type="button"
              disabled={!remote.hasPrevious}
              onClick={remote.previous}
            >
              Previous
            </button>
            <button
              type="button"
              disabled={!remote.hasNext}
              onClick={remote.next}
            >
              Next
            </button>
          </nav>
          <output class="table-filter-summary" aria-live="polite">
            {remote.status === "loading"
              ? "Loading…"
              : remote.status === "error"
                ? "Unavailable"
                : remote.totalCount === 0
                  ? `No ${hasActiveFilters ? "matching " : ""}${itemNames.plural}`
                  : `Showing ${remote.offset + 1}–${remote.offset + visible.length} of ${remote.totalCount} ${hasActiveFilters ? "matching " : ""}${remote.totalCount === 1 ? itemNames.singular : itemNames.plural}`}
          </output>
        </div>
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
                    <span>{column.label}</span>
                    <span class="sort-indicator" aria-hidden="true">
                      {sort?.key === column.key
                        ? sort.direction === "ascending"
                          ? "↑"
                          : "↓"
                        : "↕"}
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
      {remote?.status === "loading" && visible.length === 0 && (
        <StateNotice
          state="loading"
          title={`Loading ${caption.toLowerCase()}`}
        />
      )}
      {visible.length === 0 &&
        (remote === undefined || remote.status === "current") && (
          <StateNotice
            state="empty"
            title={
              remote !== undefined && hasActiveFilters
                ? "No matches"
                : emptyTitle
            }
          />
        )}
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

export function BinaryToggle({
  attributes,
  checked,
  disabled = false,
  enabledLabel = "Enabled",
  disabledLabel = "Disabled",
  showState = true,
  testID,
  onChange,
}: {
  attributes: FieldControlAttributes;
  checked: boolean;
  disabled?: boolean;
  enabledLabel?: string;
  disabledLabel?: string;
  showState?: boolean;
  testID?: string;
  onChange: (checked: boolean) => void;
}) {
  return (
    <span class="binary-toggle">
      <input
        {...attributes}
        data-testid={testID}
        type="checkbox"
        role="switch"
        checked={checked}
        disabled={disabled}
        onChange={(event) => onChange(event.currentTarget.checked)}
      />
      <span aria-hidden="true" class="binary-toggle-track" />
      {showState && (
        <span class="binary-toggle-state">
          {checked ? enabledLabel : disabledLabel}
        </span>
      )}
    </span>
  );
}

export function SuggestionInput({
  attributes,
  label,
  value,
  options,
  disabled = false,
  placeholder,
  testID,
  onChange,
}: {
  attributes: FieldControlAttributes;
  label: string;
  value: string;
  options: readonly { value: string; detail?: string }[];
  disabled?: boolean;
  placeholder?: string;
  testID: string;
  onChange: (value: string) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  const list = useRef<HTMLUListElement>(null);
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState<string>();
  const visible = options.filter((option) =>
    option.value.toLowerCase().includes(value.toLowerCase()),
  );
  const activeIndex = visible.findIndex((option) => option.value === active);
  const expanded = open && !disabled;
  const listID = `${attributes.id}-options`;
  const close = () => {
    setOpen(false);
    setActive(undefined);
  };
  const choose = (next: string) => {
    onChange(next);
    input.current?.focus();
    close();
  };
  useLayoutEffect(() => {
    if (expanded && activeIndex >= 0)
      list.current?.children[activeIndex]?.scrollIntoView({ block: "nearest" });
  }, [expanded, activeIndex, active]);
  return (
    <div
      class="suggestion-input"
      onFocusOut={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null))
          close();
      }}
    >
      <input
        {...attributes}
        ref={input}
        data-testid={testID}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={expanded}
        aria-controls={expanded ? listID : undefined}
        aria-activedescendant={
          expanded && activeIndex >= 0 ? `${listID}-${activeIndex}` : undefined
        }
        autocomplete="off"
        value={value}
        placeholder={placeholder}
        disabled={disabled}
        onFocus={() => setOpen(true)}
        onInput={(event) => {
          onChange(event.currentTarget.value);
          setActive(undefined);
          setOpen(true);
        }}
        onKeyDown={(event) => {
          if (event.isComposing) return;
          if (event.key === "ArrowDown" || event.key === "ArrowUp") {
            event.preventDefault();
            setOpen(true);
            const index =
              !expanded || activeIndex < 0
                ? event.key === "ArrowDown"
                  ? 0
                  : visible.length - 1
                : (activeIndex +
                    (event.key === "ArrowDown" ? 1 : -1) +
                    visible.length) %
                  visible.length;
            setActive(visible[index]?.value);
          } else if (expanded && event.key === "Enter") {
            event.preventDefault();
            if (activeIndex >= 0) choose(visible[activeIndex]!.value);
            else close();
          } else if (expanded && event.key === "Escape") {
            event.preventDefault();
            event.stopPropagation();
            close();
          } else if (event.key === "Tab") close();
        }}
      />
      <button
        class="suggestion-toggle"
        type="button"
        tabindex={-1}
        aria-label={`Show suggestions for ${label}`}
        aria-expanded={expanded}
        aria-controls={expanded ? listID : undefined}
        disabled={disabled}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => {
          input.current?.focus();
          setActive(undefined);
          setOpen(!expanded);
        }}
      >
        <span aria-hidden="true">▾</span>
      </button>
      {expanded && (
        <div class="suggestion-panel" data-empty={visible.length === 0}>
          <ul
            id={listID}
            ref={list}
            role="listbox"
            aria-label={`${label} suggestions`}
          >
            {visible.map((option, index) => (
              <li
                id={`${listID}-${index}`}
                key={option.value}
                role="option"
                aria-selected={index === activeIndex}
                data-value={option.value}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => choose(option.value)}
              >
                <span>{option.value}</span>
                {option.detail && <small>{option.detail}</small>}
              </li>
            ))}
          </ul>
          {visible.length === 0 && <p>No suggestions. Enter a custom value.</p>}
        </div>
      )}
    </div>
  );
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
            onClick={() => {
              dialog.current?.close();
              onConfirm();
            }}
          >
            {confirmLabel}
          </button>
        </div>
      </form>
    </dialog>
  );
}
