import type { ComponentChildren, RefObject } from "preact";
import { useEffect, useRef } from "preact/hooks";

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

interface FieldControlAttributes {
  id: string;
  "aria-describedby"?: string;
  "aria-invalid"?: true;
}

export function FormField({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  hint?: string;
  error?: string;
  children: (attributes: FieldControlAttributes) => ComponentChildren;
}) {
  const descriptions = [
    hint === undefined ? undefined : `${id}-hint`,
    error === undefined ? undefined : `${id}-error`,
  ]
    .filter((value): value is string => value !== undefined)
    .join(" ");
  const attributes: FieldControlAttributes = { id };
  if (descriptions !== "") attributes["aria-describedby"] = descriptions;
  if (error !== undefined) attributes["aria-invalid"] = true;
  return (
    <div class="form-field">
      <label for={id}>{label}</label>
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
