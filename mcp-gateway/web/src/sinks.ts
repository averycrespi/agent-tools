import { SessionClient } from "./session.ts";

export type SinkPublication = "published" | "lost";
export type ClipboardPublication = "copied" | "failed";
export type OAuthOpenResult = "opened" | "blocked";

export async function copyToClipboard(
  value: string,
  write: (candidate: string) => Promise<void>,
): Promise<ClipboardPublication> {
  let candidate = value;
  try {
    await write(candidate);
    candidate = "";
    return "copied";
  } catch {
    candidate = "";
    return "failed";
  } finally {
    candidate = "";
  }
}

export function openOAuthWindow(
  url: string,
  open: (target: string, name: string, features: string) => WindowProxy | null,
): OAuthOpenResult {
  const opened = open(url, "_blank", "noopener,noreferrer");
  if (opened === null) return "blocked";
  try {
    opened.opener = null;
  } catch {
    // The noopener feature is authoritative when a browser denies access.
  }
  return "opened";
}

export interface PreparedOneTimeSink {
  publish(value: string): SinkPublication;
  lose(): void;
  cancel(): void;
}

export interface PreparedOAuthSink {
  publish(url: string): SinkPublication;
  lose(): void;
  cancel(): void;
}

export interface OneTimePresenter {
  prepare(label: string, generation: number): boolean;
  publish(value: string, generation: number): boolean;
  lose(generation: number): void;
  clear(): void;
}

export interface OAuthPresenter {
  prepare(label: string, generation: number): boolean;
  publish(url: string, generation: number): boolean;
  lose(generation: number): void;
  clear(): void;
}

const bearer = /^mgw_(?:admin|agent)_[A-Za-z0-9_-]{43}$/;
const loopback =
  /^127\.(?:0|[1-9][0-9]{0,2})\.(?:0|[1-9][0-9]{0,2})\.(?:0|[1-9][0-9]{0,2})$/;
const maximumOAuthURLBytes = 8192;

function validOAuthURL(value: string): boolean {
  if (
    value.length === 0 ||
    new TextEncoder().encode(value).byteLength > maximumOAuthURLBytes
  ) {
    return false;
  }
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    return false;
  }
  if (
    parsed.href !== value ||
    parsed.username !== "" ||
    parsed.password !== "" ||
    parsed.hash !== ""
  ) {
    return false;
  }
  if (parsed.protocol === "https:") return true;
  if (parsed.protocol !== "http:" || !loopback.test(parsed.hostname))
    return false;
  return parsed.hostname.split(".").every((part) => Number(part) <= 255);
}

export class WriteOnlyValue {
  private input: HTMLInputElement | HTMLTextAreaElement | undefined;
  private readonly release: (value: WriteOnlyValue) => void;

  constructor(release: (value: WriteOnlyValue) => void) {
    this.release = release;
  }

  attach(input: HTMLInputElement | HTMLTextAreaElement): void {
    this.input = input;
    input.value = "";
  }

  detach(input: HTMLInputElement | HTMLTextAreaElement): void {
    if (this.input === input) {
      input.value = "";
      this.input = undefined;
    }
  }

  read(): string {
    return this.input?.value ?? "";
  }

  clear(): void {
    if (this.input !== undefined) this.input.value = "";
  }

  close(): void {
    this.clear();
    this.input = undefined;
    this.release(this);
  }
}

export class SensitiveSinkCoordinator {
  private readonly session: SessionClient;
  private readonly unregisterProtectedState: () => void;
  private oneTimePresenter: OneTimePresenter | undefined;
  private oauthPresenter: OAuthPresenter | undefined;
  private readonly writeOnlyValues = new Set<WriteOnlyValue>();
  private generation = 0;
  private active:
    | { kind: "one_time" | "oauth"; generation: number; epoch: number }
    | undefined;

  constructor(session: SessionClient) {
    this.session = session;
    this.unregisterProtectedState = session.registerProtectedState(() =>
      this.clear(),
    );
  }

  registerOneTimePresenter(presenter: OneTimePresenter): () => void {
    this.oneTimePresenter?.clear();
    this.oneTimePresenter = presenter;
    return () => {
      if (this.oneTimePresenter !== presenter) return;
      presenter.clear();
      this.oneTimePresenter = undefined;
      if (this.active?.kind === "one_time") this.invalidate();
    };
  }

  registerOAuthPresenter(presenter: OAuthPresenter): () => void {
    this.oauthPresenter?.clear();
    this.oauthPresenter = presenter;
    return () => {
      if (this.oauthPresenter !== presenter) return;
      presenter.clear();
      this.oauthPresenter = undefined;
      if (this.active?.kind === "oauth") this.invalidate();
    };
  }

  createWriteOnly(): WriteOnlyValue {
    const value = new WriteOnlyValue((candidate) =>
      this.writeOnlyValues.delete(candidate),
    );
    this.writeOnlyValues.add(value);
    return value;
  }

  prepareOneTime(label: string): PreparedOneTimeSink | undefined {
    const snapshot = this.session.snapshot();
    const presenter = this.oneTimePresenter;
    if (
      snapshot.lifecycle !== "authenticated" ||
      presenter === undefined ||
      label.length === 0 ||
      label.length > 160
    ) {
      return undefined;
    }
    this.clear();
    const generation = ++this.generation;
    this.active = { kind: "one_time", generation, epoch: snapshot.epoch };
    if (!presenter.prepare(label, generation)) {
      presenter.clear();
      this.invalidate();
      return undefined;
    }
    return this.preparedOneTime(generation, snapshot.epoch);
  }

  prepareOAuth(label: string): PreparedOAuthSink | undefined {
    const snapshot = this.session.snapshot();
    const presenter = this.oauthPresenter;
    if (
      snapshot.lifecycle !== "authenticated" ||
      presenter === undefined ||
      label.length === 0 ||
      label.length > 160
    ) {
      return undefined;
    }
    this.clear();
    const generation = ++this.generation;
    this.active = { kind: "oauth", generation, epoch: snapshot.epoch };
    if (!presenter.prepare(label, generation)) {
      presenter.clear();
      this.invalidate();
      return undefined;
    }
    return this.preparedOAuth(generation, snapshot.epoch);
  }

  isCurrent(generation: number): boolean {
    const snapshot = this.session.snapshot();
    return (
      this.active?.generation === generation &&
      this.active.epoch === snapshot.epoch &&
      snapshot.lifecycle === "authenticated"
    );
  }

  dismiss(generation: number): void {
    if (!this.isCurrent(generation)) return;
    this.clear();
  }

  clearForNavigation(): void {
    this.clear();
  }

  close(): void {
    this.unregisterProtectedState();
    this.clear();
    for (const value of [...this.writeOnlyValues]) value.close();
  }

  private preparedOneTime(
    generation: number,
    epoch: number,
  ): PreparedOneTimeSink {
    let settled = false;
    return Object.freeze({
      publish: (value: string): SinkPublication => {
        if (settled) return "lost";
        settled = true;
        if (!this.matches("one_time", generation, epoch)) return "lost";
        if (
          !bearer.test(value) ||
          !this.oneTimePresenter?.publish(value, generation)
        ) {
          this.oneTimePresenter?.lose(generation);
          return "lost";
        }
        return "published";
      },
      lose: () => {
        if (settled) return;
        settled = true;
        if (this.matches("one_time", generation, epoch))
          this.oneTimePresenter?.lose(generation);
      },
      cancel: () => {
        if (settled) return;
        settled = true;
        if (this.matches("one_time", generation, epoch)) this.clear();
      },
    });
  }

  private preparedOAuth(generation: number, epoch: number): PreparedOAuthSink {
    let settled = false;
    return Object.freeze({
      publish: (url: string): SinkPublication => {
        if (settled) return "lost";
        settled = true;
        if (!this.matches("oauth", generation, epoch)) return "lost";
        if (
          !validOAuthURL(url) ||
          !this.oauthPresenter?.publish(url, generation)
        ) {
          this.oauthPresenter?.lose(generation);
          return "lost";
        }
        return "published";
      },
      lose: () => {
        if (settled) return;
        settled = true;
        if (this.matches("oauth", generation, epoch))
          this.oauthPresenter?.lose(generation);
      },
      cancel: () => {
        if (settled) return;
        settled = true;
        if (this.matches("oauth", generation, epoch)) this.clear();
      },
    });
  }

  private matches(
    kind: "one_time" | "oauth",
    generation: number,
    epoch: number,
  ): boolean {
    return (
      this.active?.kind === kind &&
      this.active.generation === generation &&
      this.active.epoch === epoch &&
      this.isCurrent(generation)
    );
  }

  private clear(): void {
    this.oneTimePresenter?.clear();
    this.oauthPresenter?.clear();
    for (const value of this.writeOnlyValues) value.clear();
    this.invalidate();
  }

  private invalidate(): void {
    this.generation += 1;
    this.active = undefined;
  }
}
