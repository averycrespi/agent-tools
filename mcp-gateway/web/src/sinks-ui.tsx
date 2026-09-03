import { useEffect, useRef, useState } from "preact/hooks";
import { FormField, StatusLabel } from "./primitives.tsx";
import {
  copyToClipboard,
  openOAuthWindow,
  SensitiveSinkCoordinator,
  type OAuthPresenter,
  type OneTimePresenter,
  type WriteOnlyValue,
} from "./sinks.ts";

function clearSelection(): void {
  const selection = window.getSelection();
  if (selection !== null) selection.removeAllRanges();
}

export function CopyableValue({
  value,
  label,
  testID,
}: {
  value: string;
  label: string;
  testID?: string;
}) {
  const [status, setStatus] = useState<"idle" | "copied" | "failed">("idle");
  const generation = useRef(0);
  useEffect(() => {
    generation.current += 1;
    setStatus("idle");
  }, [value]);

  const copy = async () => {
    const current = ++generation.current;
    const result = await copyToClipboard(value, (candidate) =>
      navigator.clipboard.writeText(candidate),
    );
    if (generation.current === current) setStatus(result);
  };

  return (
    <div class="copyable-value">
      <code data-testid={testID}>{value}</code>
      <button
        class="text-button"
        type="button"
        aria-label={`Copy ${label}`}
        onClick={() => void copy()}
      >
        Copy
      </button>
      <span class="copyable-value-status" role="status" aria-live="polite">
        {status === "copied" && `${sentenceLabel(label)} copied.`}
        {status === "failed" && `Could not copy ${label}.`}
      </span>
    </div>
  );
}

function sentenceLabel(value: string): string {
  return `${value.charAt(0).toUpperCase()}${value.slice(1)}`;
}

export function WriteOnlyField({
  value,
  id,
  label,
  hint,
  multiline = false,
  onInput,
}: {
  value: WriteOnlyValue;
  id: string;
  label: string;
  hint: string;
  multiline?: boolean;
  onInput?: (value: string) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  const textarea = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    const node = multiline ? textarea.current : input.current;
    if (node === null) return;
    value.attach(node);
    return () => value.detach(node);
  }, [multiline, value]);
  return (
    <FormField id={id} label={label} hint={hint} required>
      {(attributes) =>
        multiline ? (
          <textarea
            {...attributes}
            ref={textarea}
            rows={5}
            autocomplete="off"
            autocapitalize="none"
            spellcheck={false}
            onInput={(event) => onInput?.(event.currentTarget.value)}
          />
        ) : (
          <input
            {...attributes}
            ref={input}
            type="password"
            autocomplete="off"
            autocapitalize="none"
            spellcheck={false}
            onInput={(event) => onInput?.(event.currentTarget.value)}
          />
        )
      }
    </FormField>
  );
}

function OneTimeDisplay({
  coordinator,
}: {
  coordinator: SensitiveSinkCoordinator;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [generation, setGeneration] = useState(0);
  const [label, setLabel] = useState("");
  const [secret, setSecret] = useState("");
  const [phase, setPhase] = useState<"awaiting" | "display" | "lost">(
    "awaiting",
  );
  const [copyStatus, setCopyStatus] = useState<"idle" | "copied" | "failed">(
    "idle",
  );

  useEffect(() => {
    const presenter: OneTimePresenter = {
      prepare: (nextLabel, nextGeneration) => {
        const node = dialog.current;
        if (node === null || node.open) return false;
        setGeneration(nextGeneration);
        setLabel(nextLabel);
        setSecret("");
        setCopyStatus("idle");
        setPhase("awaiting");
        try {
          node.showModal();
          return true;
        } catch {
          return false;
        }
      },
      publish: (value, currentGeneration) => {
        if (
          dialog.current?.open !== true ||
          !coordinator.isCurrent(currentGeneration)
        ) {
          return false;
        }
        setSecret(value);
        setCopyStatus("idle");
        setPhase("display");
        return true;
      },
      lose: (currentGeneration) => {
        if (!coordinator.isCurrent(currentGeneration)) return;
        setSecret("");
        setCopyStatus("idle");
        setPhase("lost");
        clearSelection();
      },
      clear: () => {
        setSecret("");
        setLabel("");
        setCopyStatus("idle");
        setPhase("awaiting");
        clearSelection();
        if (dialog.current?.open) dialog.current.close();
      },
    };
    return coordinator.registerOneTimePresenter(presenter);
  }, [coordinator]);

  const copy = async () => {
    if (phase !== "display" || secret === "") return;
    const result = await copyToClipboard(secret, (value) =>
      navigator.clipboard.writeText(value),
    );
    if (coordinator.isCurrent(generation)) setCopyStatus(result);
  };

  return (
    <dialog
      ref={dialog}
      class="sensitive-dialog"
      aria-labelledby="one-time-display-title"
      aria-describedby="one-time-display-warning"
      onCancel={(event) => {
        event.preventDefault();
        coordinator.dismiss(generation);
      }}
    >
      <span class="panel-code">ONE-TIME VALUE</span>
      <h2 id="one-time-display-title">{label || "One-time bearer"}</h2>
      <p id="one-time-display-warning" class="sensitive-warning">
        This bearer cannot be recovered or shown again. Copying leaves it in the
        operating-system clipboard until you overwrite it; this application
        cannot revoke clipboard contents.
      </p>
      {phase === "awaiting" && (
        <StatusLabel state="loading">Waiting for the response</StatusLabel>
      )}
      {phase === "lost" && (
        <StatusLabel state="unavailable">
          No bearer can be displayed. Inspect current state before another
          explicit action.
        </StatusLabel>
      )}
      {phase === "display" && (
        <div class="one-time-value" data-testid="one-time-value">
          <code>{secret}</code>
        </div>
      )}
      <div
        class="sink-status"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {copyStatus === "copied" && "Copied to the operating-system clipboard."}
        {copyStatus === "failed" &&
          "Clipboard copy failed. The bearer was not echoed."}
      </div>
      <div class="dialog-actions">
        <button type="button" onClick={() => coordinator.dismiss(generation)}>
          Dismiss and clear
        </button>
        {phase === "display" && (
          <button
            class="primary-action"
            type="button"
            data-testid="copy-one-time-value"
            onClick={() => void copy()}
          >
            Copy bearer
          </button>
        )}
      </div>
    </dialog>
  );
}

function OAuthDisplay({
  coordinator,
}: {
  coordinator: SensitiveSinkCoordinator;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const [generation, setGeneration] = useState(0);
  const [label, setLabel] = useState("");
  const [url, setURL] = useState("");
  const [phase, setPhase] = useState<"awaiting" | "display" | "lost">(
    "awaiting",
  );
  const [openFailed, setOpenFailed] = useState(false);
  const [copyStatus, setCopyStatus] = useState<"idle" | "copied" | "failed">(
    "idle",
  );

  useEffect(() => {
    const presenter: OAuthPresenter = {
      prepare: (nextLabel, nextGeneration) => {
        const node = dialog.current;
        if (node === null || node.open) return false;
        setGeneration(nextGeneration);
        setLabel(nextLabel);
        setURL("");
        setOpenFailed(false);
        setCopyStatus("idle");
        setPhase("awaiting");
        try {
          node.showModal();
          return true;
        } catch {
          return false;
        }
      },
      publish: (value, currentGeneration) => {
        if (
          dialog.current?.open !== true ||
          !coordinator.isCurrent(currentGeneration)
        ) {
          return false;
        }
        setURL(value);
        setOpenFailed(false);
        setCopyStatus("idle");
        setPhase("display");
        return true;
      },
      lose: (currentGeneration) => {
        if (!coordinator.isCurrent(currentGeneration)) return;
        setURL("");
        setOpenFailed(false);
        setCopyStatus("idle");
        setPhase("lost");
        clearSelection();
      },
      clear: () => {
        setURL("");
        setLabel("");
        setOpenFailed(false);
        setCopyStatus("idle");
        setPhase("awaiting");
        clearSelection();
        if (dialog.current?.open) dialog.current.close();
      },
    };
    return coordinator.registerOAuthPresenter(presenter);
  }, [coordinator]);

  const open = () => {
    if (phase !== "display" || url === "") return;
    const result = openOAuthWindow(url, () =>
      window.open(url, "_blank", "noopener,noreferrer"),
    );
    if (result === "blocked") {
      setOpenFailed(true);
      setCopyStatus("idle");
      return;
    }
    setURL("");
    clearSelection();
    coordinator.dismiss(generation);
  };

  const copy = async () => {
    if (phase !== "display" || url === "") return;
    const result = await copyToClipboard(url, (value) =>
      navigator.clipboard.writeText(value),
    );
    if (coordinator.isCurrent(generation)) setCopyStatus(result);
  };

  return (
    <dialog
      ref={dialog}
      class="sensitive-dialog"
      aria-labelledby="oauth-display-title"
      aria-describedby="oauth-display-warning"
      onCancel={(event) => {
        event.preventDefault();
        coordinator.dismiss(generation);
      }}
    >
      <span class="panel-code">ONE-TIME URL</span>
      <h2 id="oauth-display-title">{label || "Authorization URL"}</h2>
      <p id="oauth-display-warning" class="sensitive-warning">
        Open this URL only when you are ready to continue authorization. It is
        kept only in this dialog and is cleared on dismissal or navigation.
      </p>
      {phase === "awaiting" && (
        <StatusLabel state="loading">Waiting for the response</StatusLabel>
      )}
      {phase === "lost" && (
        <StatusLabel state="unavailable">
          No authorization URL can be displayed. Start a new flow from current
          state.
        </StatusLabel>
      )}
      {phase === "display" && (
        <div class="one-time-url" data-testid="one-time-oauth-url">
          <code>{url}</code>
        </div>
      )}
      <div
        class="sink-status"
        role="status"
        aria-live="polite"
        aria-atomic="true"
      >
        {openFailed &&
          "The browser blocked the new page. The URL remains available in this dialog. Copying leaves it in the operating-system clipboard until you overwrite it."}
        {copyStatus === "copied" &&
          " Copied to the operating-system clipboard."}
        {copyStatus === "failed" && " Clipboard copy failed."}
      </div>
      <div class="dialog-actions">
        <button type="button" onClick={() => coordinator.dismiss(generation)}>
          Dismiss and clear
        </button>
        {phase === "display" && openFailed && (
          <button
            type="button"
            data-testid="copy-oauth-url"
            onClick={() => void copy()}
          >
            Copy URL
          </button>
        )}
        {phase === "display" && (
          <button
            class="primary-action"
            type="button"
            data-testid="open-oauth-url"
            onClick={open}
          >
            Open authorization page
          </button>
        )}
      </div>
    </dialog>
  );
}

export function SensitiveSinkHost({
  coordinator,
}: {
  coordinator: SensitiveSinkCoordinator;
}) {
  return (
    <>
      <OneTimeDisplay coordinator={coordinator} />
      <OAuthDisplay coordinator={coordinator} />
    </>
  );
}
