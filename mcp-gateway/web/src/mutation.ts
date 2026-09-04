import {
  parseProblem,
  SessionClient,
  type Problem,
  type ProtectedContext,
} from "./session.ts";

export type MutationState =
  | "editing"
  | "confirming"
  | "submitting"
  | "acknowledged"
  | "rejected"
  | "uncertain";
export type MutationAvailability = "enabled" | "storage_latched";
export type IdempotencyRoute =
  | "none"
  | "backup_create"
  | "server_create"
  | "operation_start";
export type MutationMethod = "POST" | "PATCH" | "DELETE";

export interface MutationSpec<T> {
  route: string;
  method: MutationMethod;
  body: string | null;
  precondition: string | null;
  requiresPrecondition: boolean;
  idempotency: IdempotencyRoute;
  successStatuses: readonly number[];
  uncertainProblemCodes?: readonly string[];
  decode: (response: Response) => Promise<T>;
}

export interface MutationSnapshot {
  state: MutationState;
  availability: MutationAvailability;
  canReplay: boolean;
  requiresRefresh: boolean;
  problem?: Problem;
}

export type MutationOutcome<T> =
  | { kind: "acknowledged"; value: T }
  | { kind: "rejected"; problem: Problem; requiresRefresh: boolean }
  | { kind: "uncertain"; canReplay: boolean }
  | { kind: "discarded" };

interface RecoveryTuple<T> {
  key: string;
  spec: MutationSpec<T>;
  fingerprint: string;
}

interface InternalResult<T> {
  outcome: MutationOutcome<T>;
  storageLatched: boolean;
}

interface MutationCoordinatorOptions {
  request?: typeof fetch;
  refreshCurrent?: () => void;
  key?: () => string;
}

type MutationListener = (snapshot: MutationSnapshot) => void;
type AvailabilityListener = (availability: MutationAvailability) => void;

const gatewayID = "[0-7][0-9A-HJKMNP-TV-Z]{25}";
const idempotencyRoutes: Readonly<
  Record<Exclude<IdempotencyRoute, "none">, RegExp>
> = {
  backup_create: /^\/api\/v1\/backups$/,
  server_create: /^\/api\/v1\/servers$/,
  operation_start: new RegExp(`^/api/v1/servers/${gatewayID}/operations$`),
};
const preconditionRoutes = [
  new RegExp(`^PATCH /api/v1/servers/${gatewayID}$`),
  new RegExp(`^DELETE /api/v1/servers/${gatewayID}$`),
  new RegExp(`^POST /api/v1/servers/${gatewayID}/operations$`),
  new RegExp(`^POST /api/v1/servers/${gatewayID}/credential-replacements$`),
  new RegExp(`^POST /api/v1/servers/${gatewayID}/auth-flows$`),
  new RegExp(`^PATCH /api/v1/principals/${gatewayID}$`),
  new RegExp(`^PATCH /api/v1/grants/${gatewayID}$`),
  new RegExp(`^(?:POST|DELETE) /api/v1/principals/${gatewayID}/credential$`),
  new RegExp(`^POST /api/v1/grant-requests/${gatewayID}/(?:approve|reject)$`),
];
const strongETag = /^"[\x21\x23-\x7e]{1,255}"$/;
const mutationRoute = /^\/api\/v1\/[A-Za-z0-9_/-]{1,512}$/;
const credentialReplacementRoute = new RegExp(
  `^/api/v1/servers/${gatewayID}/credential-replacements$`,
);
const matcherApprovalRoute = new RegExp(
  `^/api/v1/grant-requests/${gatewayID}/approve$`,
);
const maximumBodyBytes = 1024 * 1024;

function randomKey(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return `mgw-web-${Array.from(bytes, (value) =>
    value.toString(16).padStart(2, "0"),
  ).join("")}`;
}

function isCanonicalJSON(value: string): boolean {
  try {
    return JSON.stringify(JSON.parse(value) as unknown) === value;
  } catch {
    return false;
  }
}

function isTokenPreservingMatcherMutationJSON(
  spec: MutationSpec<unknown>,
): boolean {
  if (
    spec.method !== "POST" ||
    spec.body === null ||
    (spec.route !== "/api/v1/grants" && !matcherApprovalRoute.test(spec.route))
  )
    return false;
  try {
    JSON.parse(spec.body);
    return true;
  } catch {
    return false;
  }
}

function validateSpec<T>(spec: MutationSpec<T>): void {
  const routeIdempotency = (
    Object.entries(idempotencyRoutes) as Array<
      [Exclude<IdempotencyRoute, "none">, RegExp]
    >
  ).find(([, route]) => route.test(spec.route))?.[0];
  const requiresPrecondition = preconditionRoutes.some((route) =>
    route.test(`${spec.method} ${spec.route}`),
  );
  if (
    !mutationRoute.test(spec.route) ||
    (spec.body !== null &&
      (new TextEncoder().encode(spec.body).byteLength > maximumBodyBytes ||
        (!isCanonicalJSON(spec.body) &&
          !isTokenPreservingMatcherMutationJSON(spec)))) ||
    spec.requiresPrecondition !== requiresPrecondition ||
    requiresPrecondition !== (spec.precondition !== null) ||
    (spec.precondition !== null && !strongETag.test(spec.precondition)) ||
    spec.successStatuses.length === 0 ||
    spec.successStatuses.some(
      (status) => !Number.isInteger(status) || status < 200 || status > 299,
    ) ||
    routeIdempotency !==
      (spec.idempotency === "none" ? undefined : spec.idempotency) ||
    (spec.idempotency !== "none" &&
      (spec.method !== "POST" || spec.body === null)) ||
    (spec.idempotency === "operation_start" && spec.precondition === null) ||
    ((spec.idempotency === "backup_create" ||
      spec.idempotency === "server_create") &&
      spec.precondition !== null) ||
    ((spec.uncertainProblemCodes?.length ?? 0) !== 0 &&
      (!credentialReplacementRoute.test(spec.route) ||
        spec.uncertainProblemCodes?.length !== 1 ||
        spec.uncertainProblemCodes[0] !== "keyring_unavailable"))
  ) {
    throw new Error("invalid mutation specification");
  }
}

function fingerprint<T>(spec: MutationSpec<T>): string {
  return JSON.stringify([
    spec.route,
    spec.method,
    spec.body,
    spec.precondition,
    spec.idempotency,
    [...spec.successStatuses],
    [...(spec.uncertainProblemCodes ?? [])],
  ]);
}

async function responseProblem(
  response: Response,
): Promise<Problem | undefined> {
  if (response.headers.get("Content-Type") !== "application/problem+json")
    return undefined;
  let value: unknown;
  try {
    value = (await response.json()) as unknown;
  } catch {
    return undefined;
  }
  const problem = parseProblem(value);
  return problem?.status === response.status ? problem : undefined;
}

export class MutationCoordinator {
  private readonly session: SessionClient;
  private readonly request: typeof fetch;
  private readonly refreshCurrent: () => void;
  private readonly createKey: () => string;
  private readonly controllers = new Set<MutationController<unknown>>();
  private readonly listeners = new Set<AvailabilityListener>();
  private readonly unregisterProtectedState: () => void;
  private availability: MutationAvailability = "enabled";

  constructor(
    session: SessionClient,
    options: MutationCoordinatorOptions = {},
  ) {
    this.session = session;
    this.request = (options.request ?? fetch).bind(globalThis);
    this.refreshCurrent = options.refreshCurrent ?? (() => {});
    this.createKey = options.key ?? randomKey;
    this.unregisterProtectedState = this.session.registerProtectedState(() => {
      for (const controller of this.controllers) controller.resetForEpoch();
    });
  }

  snapshot(): MutationAvailability {
    return this.availability;
  }

  subscribe(listener: AvailabilityListener): () => void {
    this.listeners.add(listener);
    listener(this.availability);
    return () => this.listeners.delete(listener);
  }

  create<T>(): MutationController<T> {
    const controller = new MutationController<T>(this);
    this.controllers.add(controller as MutationController<unknown>);
    return controller;
  }

  setStorageLatched(latched: boolean): void {
    const next = latched ? "storage_latched" : "enabled";
    if (this.availability === next) return;
    this.availability = next;
    for (const listener of this.listeners) listener(next);
    for (const controller of this.controllers) controller.availabilityChanged();
  }

  close(): void {
    this.unregisterProtectedState();
    for (const controller of [...this.controllers]) controller.close();
    this.controllers.clear();
    this.listeners.clear();
  }

  available(): boolean {
    return this.availability === "enabled";
  }

  release(controller: MutationController<unknown>): void {
    this.controllers.delete(controller);
  }

  key(): string {
    const value = this.createKey();
    if (!/^[\x21-\x7e]{1,128}$/.test(value))
      throw new Error("invalid idempotency key");
    return value;
  }

  refresh(): void {
    this.refreshCurrent();
  }

  async execute<T>(
    spec: MutationSpec<T>,
    key: string | undefined,
  ): Promise<InternalResult<T>> {
    const result = await this.session.runProtected(async (context) =>
      this.requestMutation(context, spec, key),
    );
    return (
      result ?? {
        outcome: { kind: "discarded" },
        storageLatched: false,
      }
    );
  }

  private async requestMutation<T>(
    context: ProtectedContext,
    spec: MutationSpec<T>,
    key: string | undefined,
  ): Promise<InternalResult<T>> {
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      "X-CSRF-Token": context.csrfToken,
    };
    if (spec.precondition !== null) headers["If-Match"] = spec.precondition;
    if (key !== undefined) headers["Idempotency-Key"] = key;
    let response: Response;
    try {
      response = await this.request(spec.route, {
        method: spec.method,
        headers,
        body: spec.body,
        credentials: "same-origin",
        redirect: "error",
        signal: context.signal,
      });
    } catch {
      return {
        outcome: { kind: "uncertain", canReplay: key !== undefined },
        storageLatched: false,
      };
    }
    if (await context.sessionLost(response)) {
      return { outcome: { kind: "discarded" }, storageLatched: false };
    }
    if (spec.successStatuses.includes(response.status)) {
      try {
        return {
          outcome: { kind: "acknowledged", value: await spec.decode(response) },
          storageLatched: false,
        };
      } catch {
        return {
          outcome: { kind: "uncertain", canReplay: key !== undefined },
          storageLatched: false,
        };
      }
    }
    const problem = await responseProblem(response);
    if (problem === undefined) {
      return {
        outcome: { kind: "uncertain", canReplay: key !== undefined },
        storageLatched: false,
      };
    }
    if (spec.uncertainProblemCodes?.includes(problem.code) === true) {
      return {
        outcome: { kind: "uncertain", canReplay: key !== undefined },
        storageLatched: false,
      };
    }
    if (problem.code === "storage_unavailable") {
      return {
        outcome: { kind: "uncertain", canReplay: key !== undefined },
        storageLatched: true,
      };
    }
    const requiresRefresh =
      response.status === 409 ||
      response.status === 412 ||
      response.status === 428;
    return {
      outcome: { kind: "rejected", problem, requiresRefresh },
      storageLatched: false,
    };
  }
}

export class MutationController<T> {
  private readonly coordinator: MutationCoordinator;
  private readonly listeners = new Set<MutationListener>();
  private state: MutationState = "editing";
  private spec: MutationSpec<T> | undefined;
  private recovery: RecoveryTuple<T> | undefined;
  private problem: Problem | undefined;
  private requiresRefresh = false;
  private version = 0;
  private inFlight: Promise<MutationOutcome<T>> | undefined;
  private closed = false;

  constructor(coordinator: MutationCoordinator) {
    this.coordinator = coordinator;
  }

  snapshot(): MutationSnapshot {
    const snapshot: MutationSnapshot = {
      state: this.state,
      availability: this.coordinator.snapshot(),
      canReplay:
        this.state === "uncertain" &&
        this.recovery !== undefined &&
        this.coordinator.available(),
      requiresRefresh: this.requiresRefresh,
    };
    if (this.problem !== undefined) snapshot.problem = this.problem;
    return snapshot;
  }

  subscribe(listener: MutationListener): () => void {
    this.listeners.add(listener);
    listener(this.snapshot());
    return () => this.listeners.delete(listener);
  }

  begin(spec: MutationSpec<T>): void {
    if (this.closed) throw new Error("mutation is closed");
    if (this.state === "submitting") throw new Error("mutation is submitting");
    const owned: MutationSpec<T> = {
      ...spec,
      successStatuses: [...spec.successStatuses],
      ...(spec.uncertainProblemCodes === undefined
        ? {}
        : { uncertainProblemCodes: [...spec.uncertainProblemCodes] }),
    };
    validateSpec(owned);
    this.version += 1;
    this.spec = owned;
    this.recovery =
      owned.idempotency === "none"
        ? undefined
        : {
            key: this.coordinator.key(),
            spec: owned,
            fingerprint: fingerprint(owned),
          };
    this.problem = undefined;
    this.requiresRefresh = false;
    this.state = "editing";
    this.emit();
  }

  confirm(): void {
    if (this.state !== "editing") throw new Error("mutation is not editable");
    this.state = "confirming";
    this.emit();
  }

  submit(): Promise<MutationOutcome<T>> {
    if (this.inFlight !== undefined) return this.inFlight;
    if (
      this.spec === undefined ||
      (this.state !== "editing" && this.state !== "confirming")
    ) {
      return Promise.resolve({ kind: "discarded" });
    }
    return this.start(false);
  }

  replay(): Promise<MutationOutcome<T>> {
    if (this.inFlight !== undefined) return this.inFlight;
    if (
      this.state !== "uncertain" ||
      this.recovery === undefined ||
      !this.coordinator.available() ||
      fingerprint(this.recovery.spec) !== this.recovery.fingerprint
    ) {
      return Promise.resolve({ kind: "discarded" });
    }
    this.spec = this.recovery.spec;
    return this.start(true);
  }

  abandon(): void {
    if (this.state === "submitting") throw new Error("mutation is submitting");
    this.version += 1;
    this.spec = undefined;
    this.recovery = undefined;
    this.problem = undefined;
    this.requiresRefresh = false;
    this.state = "editing";
    this.emit();
  }

  resetForEpoch(): void {
    if (this.closed) return;
    this.version += 1;
    this.spec = undefined;
    this.recovery = undefined;
    this.problem = undefined;
    this.requiresRefresh = false;
    this.state = "editing";
    this.inFlight = undefined;
    this.emit();
  }

  availabilityChanged(): void {
    this.emit();
  }

  close(): void {
    if (this.closed) return;
    this.resetForEpoch();
    this.closed = true;
    this.listeners.clear();
    this.coordinator.release(this as MutationController<unknown>);
  }

  private start(replay: boolean): Promise<MutationOutcome<T>> {
    if (!this.coordinator.available() || this.spec === undefined)
      return Promise.resolve({ kind: "discarded" });
    const version = this.version;
    const spec = this.spec;
    const key = this.recovery?.key;
    this.state = "submitting";
    this.problem = undefined;
    this.requiresRefresh = false;
    this.emit();
    const submission = this.coordinator
      .execute(spec, key)
      .then((result) => {
        if (version !== this.version) return { kind: "discarded" } as const;
        if (result.storageLatched) this.coordinator.setStorageLatched(true);
        const outcome = result.outcome;
        if (outcome.kind === "acknowledged") {
          this.state = "acknowledged";
          this.spec = undefined;
          this.recovery = undefined;
        } else if (outcome.kind === "rejected") {
          this.state = "rejected";
          this.spec = undefined;
          this.problem = outcome.problem;
          this.requiresRefresh = outcome.requiresRefresh;
          this.recovery = undefined;
          if (outcome.requiresRefresh) this.coordinator.refresh();
        } else if (outcome.kind === "uncertain") {
          this.state = "uncertain";
          if (!outcome.canReplay) {
            this.spec = undefined;
            this.recovery = undefined;
          }
        } else {
          this.state = "editing";
          this.recovery = undefined;
        }
        if (replay && outcome.kind === "uncertain") {
          this.state = "uncertain";
        }
        this.emit();
        return outcome;
      })
      .finally(() => {
        if (this.inFlight === submission) this.inFlight = undefined;
      });
    this.inFlight = submission;
    return submission;
  }

  private emit(): void {
    const snapshot = this.snapshot();
    for (const listener of this.listeners) listener(snapshot);
  }
}
