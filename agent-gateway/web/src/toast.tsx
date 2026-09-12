import { useEffect, useState } from "preact/hooks";

type Listener = (message: string | undefined) => void;

export class ToastCoordinator {
  private message: string | undefined;
  private readonly listeners = new Set<Listener>();
  private timer: ReturnType<typeof setTimeout> | undefined;

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    listener(this.message);
    return () => this.listeners.delete(listener);
  }

  show(message: string): void {
    const normalized = message.trim();
    if (normalized.length === 0 || normalized.length > 160) return;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.message = normalized;
    this.emit();
    this.timer = setTimeout(() => this.clear(), 5000);
  }

  clear(): void {
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
    if (this.message === undefined) return;
    this.message = undefined;
    this.emit();
  }

  private emit(): void {
    for (const listener of this.listeners) listener(this.message);
  }
}

export function ToastHost({ coordinator }: { coordinator: ToastCoordinator }) {
  const [message, setMessage] = useState<string>();
  useEffect(() => coordinator.subscribe(setMessage), [coordinator]);
  if (message === undefined) return null;
  return (
    <aside class="toast" role="status" aria-live="polite" data-testid="toast">
      <span>{message}</span>
      <button
        type="button"
        aria-label="Dismiss notification"
        onClick={() => coordinator.clear()}
      >
        ×
      </button>
    </aside>
  );
}
