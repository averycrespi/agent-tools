export type SessionLifecycle =
  | "bootstrapping"
  | "signed_out"
  | "authenticated"
  | "reauthenticating";

export interface SessionSnapshot {
  lifecycle: SessionLifecycle;
  epoch: number;
  message?: string;
}

export interface SessionBootstrap {
  csrfToken: string;
  idleExpiresAt: string;
  absoluteExpiresAt: string;
}

export interface ServerConfigurationContext {
  field: string;
  rule: string;
}

export interface Problem {
  status: number;
  code: string;
  title: string;
  context?: ServerConfigurationContext;
}

export interface ProtectedContext {
  epoch: number;
  csrfToken: string;
  signal: AbortSignal;
  isCurrent: () => boolean;
  sessionLost: (response: Response) => Promise<boolean>;
}

type Listener = (snapshot: SessionSnapshot) => void;
type ClearProtectedState = () => void;

const sessionSuccessType = "application/json";
const problemType = "application/problem+json";

function hasExactKeys(value: object, expected: readonly string[]): boolean {
  return Object.keys(value).sort().join(",") === [...expected].sort().join(",");
}

function isTimestamp(value: unknown): value is string {
  return (
    typeof value === "string" &&
    value.length <= 64 &&
    /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(value) &&
    !Number.isNaN(Date.parse(value))
  );
}

export function parseSessionBootstrap(
  value: unknown,
): SessionBootstrap | undefined {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    !hasExactKeys(value, [
      "absolute_expires_at",
      "csrf_token",
      "idle_expires_at",
    ]) ||
    !("csrf_token" in value) ||
    typeof value.csrf_token !== "string" ||
    !/^[A-Za-z0-9_-]{43}$/.test(value.csrf_token) ||
    !("idle_expires_at" in value) ||
    !isTimestamp(value.idle_expires_at) ||
    !("absolute_expires_at" in value) ||
    !isTimestamp(value.absolute_expires_at) ||
    Date.parse(value.idle_expires_at) > Date.parse(value.absolute_expires_at)
  ) {
    return undefined;
  }
  return {
    csrfToken: value.csrf_token,
    idleExpiresAt: value.idle_expires_at,
    absoluteExpiresAt: value.absolute_expires_at,
  };
}

const serverConfigurationFields = new Set([
  "configuration",
  "namespace",
  "display_name",
  "enabled",
  "transport",
  "transport.kind",
  "transport.executable",
  "transport.arguments",
  "transport.working_directory",
  "transport.environment",
  "transport.secret_environment",
  "transport.url",
  "transport.protocol_mode",
  "transport.authentication",
  "transport.authentication.mode",
  "transport.authentication.trusted_origins",
  "transport.authentication.request_offline_access",
  "transport.authentication.registration",
  "transport.authentication.registration.mode",
  "transport.authentication.registration.issuer",
  "transport.authentication.registration.client_id",
  "transport.authentication.registration.token_endpoint_auth_method",
]);
const serverConfigurationRules = new Set([
  "invalid",
  "required",
  "maximum",
  "unique",
  "disjoint",
  "canonical_absolute_path",
  "canonical_url",
  "transport_policy",
]);

function parseServerConfigurationContext(
  value: unknown,
): ServerConfigurationContext | undefined {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    !hasExactKeys(value, ["field", "rule"]) ||
    !("field" in value) ||
    typeof value.field !== "string" ||
    !serverConfigurationFields.has(value.field) ||
    !("rule" in value) ||
    typeof value.rule !== "string" ||
    !serverConfigurationRules.has(value.rule)
  )
    return undefined;
  return { field: value.field, rule: value.rule };
}

export function parseProblem(value: unknown): Problem | undefined {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    !("status" in value) ||
    !Number.isInteger(value.status) ||
    typeof value.status !== "number" ||
    value.status < 400 ||
    value.status > 599 ||
    !("code" in value) ||
    typeof value.code !== "string" ||
    !/^[a-z][a-z0-9_]{0,63}$/.test(value.code) ||
    !("title" in value) ||
    typeof value.title !== "string" ||
    value.title.length === 0 ||
    value.title.length > 256
  ) {
    return undefined;
  }
  if (value.code === "invalid_server_configuration") {
    if (hasExactKeys(value, ["code", "status", "title"]))
      return { status: value.status, code: value.code, title: value.title };
    if (!hasExactKeys(value, ["code", "context", "status", "title"]))
      return undefined;
    const context =
      "context" in value
        ? parseServerConfigurationContext(value.context)
        : undefined;
    if (context === undefined) return undefined;
    return {
      status: value.status,
      code: value.code,
      title: value.title,
      context,
    };
  }
  if (!hasExactKeys(value, ["code", "status", "title"])) return undefined;
  return { status: value.status, code: value.code, title: value.title };
}

async function responseValue(response: Response): Promise<unknown> {
  try {
    return (await response.json()) as unknown;
  } catch {
    return undefined;
  }
}

function responseType(response: Response): string {
  return response.headers.get("Content-Type") ?? "";
}

export class SessionClient {
  private readonly request: typeof fetch;
  private lifecycle: SessionLifecycle = "bootstrapping";
  private epoch = 0;
  private session: SessionBootstrap | undefined;
  private message: string | undefined;
  private started = false;
  private recovery: Promise<void> | undefined;
  private readonly listeners = new Set<Listener>();
  private readonly clearProtectedState = new Set<ClearProtectedState>();
  private readonly controllers = new Set<AbortController>();
  private readonly timers = new Set<ReturnType<typeof setTimeout>>();

  constructor(request: typeof fetch = fetch) {
    this.request = request.bind(globalThis);
  }

  snapshot(): SessionSnapshot {
    return this.message === undefined
      ? { lifecycle: this.lifecycle, epoch: this.epoch }
      : { lifecycle: this.lifecycle, epoch: this.epoch, message: this.message };
  }

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    listener(this.snapshot());
    return () => this.listeners.delete(listener);
  }

  registerProtectedState(clear: ClearProtectedState): () => void {
    this.clearProtectedState.add(clear);
    return () => this.clearProtectedState.delete(clear);
  }

  start(): void {
    if (this.started) return;
    this.started = true;
    void this.bootstrap(false);
  }

  async exchange(candidate: string): Promise<boolean> {
    if (this.lifecycle !== "signed_out" || candidate.length === 0) return false;
    this.message = undefined;
    this.emit();
    let bearer = candidate;
    try {
      const response = await this.request("/api/v1/admin-sessions", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${bearer}`,
          "Content-Type": "application/json",
        },
        body: "{}",
        credentials: "same-origin",
        redirect: "error",
      });
      bearer = "";
      if (
        response.status === 201 &&
        responseType(response) === sessionSuccessType
      ) {
        const session = parseSessionBootstrap(await responseValue(response));
        if (session !== undefined) {
          this.authenticate(session);
          return true;
        }
      } else {
        await this.readProblem(response);
      }
      this.message = "Sign-in failed. Check the credential and try again.";
      this.emit();
      return false;
    } catch {
      bearer = "";
      this.message = "Sign-in could not be completed.";
      this.emit();
      return false;
    } finally {
      bearer = "";
      candidate = "";
    }
  }

  async logout(): Promise<void> {
    if (this.lifecycle !== "authenticated" || this.session === undefined)
      return;
    const csrfToken = this.session.csrfToken;
    this.advanceEpoch("signed_out");
    try {
      const response = await this.request("/api/v1/admin-sessions/current", {
        method: "DELETE",
        headers: {
          "Content-Type": "application/json",
          "X-CSRF-Token": csrfToken,
        },
        body: "{}",
        credentials: "same-origin",
        redirect: "error",
      });
      if (response.status !== 204) {
        await this.readProblem(response);
        this.message =
          "The local session was cleared, but logout was not confirmed.";
        this.emit();
      }
    } catch {
      this.message =
        "The local session was cleared, but logout was not confirmed.";
      this.emit();
    }
  }

  async runProtected<T>(
    operation: (context: ProtectedContext) => Promise<T>,
  ): Promise<T | undefined> {
    if (this.lifecycle !== "authenticated" || this.session === undefined)
      return undefined;
    const capturedEpoch = this.epoch;
    const controller = new AbortController();
    this.controllers.add(controller);
    const current = () =>
      this.lifecycle === "authenticated" && this.epoch === capturedEpoch;
    try {
      const value = await operation({
        epoch: capturedEpoch,
        csrfToken: this.session.csrfToken,
        signal: controller.signal,
        isCurrent: current,
        sessionLost: async (response) => {
          if (
            response.status !== 401 &&
            !(
              response.status === 403 &&
              (await this.problemCode(response)) === "csrf_failed"
            )
          ) {
            return false;
          }
          await this.recoverAfterSessionLoss(capturedEpoch);
          return true;
        },
      });
      return current() ? value : undefined;
    } catch (error) {
      if (controller.signal.aborted || !current()) return undefined;
      throw error;
    } finally {
      this.controllers.delete(controller);
    }
  }

  scheduleProtected(
    callback: () => void,
    delayMilliseconds: number,
  ): () => void {
    if (this.lifecycle !== "authenticated") return () => {};
    const capturedEpoch = this.epoch;
    const timer = setTimeout(() => {
      this.timers.delete(timer);
      if (this.lifecycle === "authenticated" && this.epoch === capturedEpoch)
        callback();
    }, delayMilliseconds);
    this.timers.add(timer);
    return () => {
      clearTimeout(timer);
      this.timers.delete(timer);
    };
  }

  recoverAfterSessionLoss(lostEpoch: number = this.epoch): Promise<void> {
    if (this.recovery !== undefined) return this.recovery;
    if (lostEpoch !== this.epoch) return Promise.resolve();
    this.advanceEpoch("reauthenticating");
    const recovery = this.bootstrap(true).finally(() => {
      if (this.recovery === recovery) this.recovery = undefined;
    });
    this.recovery = recovery;
    return recovery;
  }

  private async bootstrap(recovery: boolean): Promise<void> {
    try {
      const response = await this.request("/api/v1/admin-sessions/current", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: "{}",
        credentials: "same-origin",
        redirect: "error",
      });
      if (
        response.status === 200 &&
        responseType(response) === sessionSuccessType
      ) {
        const session = parseSessionBootstrap(await responseValue(response));
        if (session !== undefined) {
          this.authenticate(session);
          return;
        }
      } else {
        const problem = await this.readProblem(response);
        if (
          response.status === 401 &&
          problem?.code === "authentication_required"
        ) {
          this.setSignedOut(undefined);
          return;
        }
      }
      this.setSignedOut(
        recovery
          ? "The session could not be recovered. Sign in again."
          : "The session response was invalid. Sign in again.",
      );
    } catch {
      this.setSignedOut("The session could not be restored. Sign in again.");
    }
  }

  private authenticate(session: SessionBootstrap): void {
    this.advanceEpoch("authenticated");
    this.session = session;
    this.message = undefined;
    this.emit();
  }

  private setSignedOut(message: string | undefined): void {
    this.session = undefined;
    this.lifecycle = "signed_out";
    this.message = message;
    this.emit();
  }

  private advanceEpoch(lifecycle: SessionLifecycle): void {
    this.epoch += 1;
    this.lifecycle = lifecycle;
    this.session = undefined;
    this.message = undefined;
    for (const controller of this.controllers) controller.abort();
    this.controllers.clear();
    for (const timer of this.timers) clearTimeout(timer);
    this.timers.clear();
    for (const clear of this.clearProtectedState) clear();
    this.emit();
  }

  private async readProblem(response: Response): Promise<Problem | undefined> {
    if (responseType(response) !== problemType) return undefined;
    const problem = parseProblem(await responseValue(response));
    return problem?.status === response.status ? problem : undefined;
  }

  private async problemCode(response: Response): Promise<string | undefined> {
    return (await this.readProblem(response))?.code;
  }

  private emit(): void {
    const snapshot = this.snapshot();
    for (const listener of this.listeners) listener(snapshot);
  }
}
