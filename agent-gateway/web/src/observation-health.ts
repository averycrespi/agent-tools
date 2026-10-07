export interface Measurement {
  state: "available" | "absent" | "unavailable";
  bytes: number | null;
}
export interface HistoryHealth {
  delivery: {
    accepted: number;
    acknowledged: number;
    discarded: number;
    queue_records: number;
    queue_bytes: number;
    completion_records: number;
    queue_record_limit: number;
    queue_byte_limit: number;
  };
  database_measurement: Measurement;
  wal_measurement: Measurement;
  free_space_measurement: Measurement;
  pressure_reason: string;
  accounting_available: boolean;
}
export interface TrafficRecovery {
  health: string;
  lastAcknowledged: string;
  incident: {
    firstFailure: string;
    cause: string;
    stage: string;
    settlement: string;
    recovery: string;
    recoveryCause: string;
    recoveryStage: string;
    sqliteCode: number;
    affected: number;
    discarded: number;
  } | null;
}
export interface DiagnosticHealth {
  state: string;
  epoch: string;
  accepted: number;
  written: number;
  dropped: number;
  invalid: number;
  write_failures: number;
  queue_records: number;
  queue_bytes: number;
  queue_limit: number;
  writing: boolean;
  last_successful_write: string | null;
  overflow: boolean;
}
export interface Observations {
  epoch: string;
  started_at: string;
  coverage: string;
  overflow: boolean;
  protocols: {
    protocol: string;
    requests: number;
    executions: number;
    results: number[];
    git_reports: number[];
    latency: number[][];
  }[];
}
function object(value: unknown, keys: string[]): Record<string, unknown> {
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).sort().join() !== keys.sort().join()
  )
    throw new Error("invalid observation health");
  return value as Record<string, unknown>;
}
function count(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0)
    throw new Error("invalid observation count");
  return value;
}
function bool(value: unknown): boolean {
  if (typeof value !== "boolean") throw new Error("invalid observation flag");
  return value;
}
function text(value: unknown): string {
  if (typeof value !== "string" || value.length > 128)
    throw new Error("invalid observation text");
  return value;
}
function closed(value: unknown, values: string[]): string {
  const v = text(value);
  if (!values.includes(v)) throw new Error("invalid observation state");
  return v;
}
function measurement(value: unknown): Measurement {
  const v = object(value, ["state", "bytes"]);
  const state = closed(v.state, [
    "available",
    "absent",
    "unavailable",
  ]) as Measurement["state"];
  if ((state === "available") !== (v.bytes !== null))
    throw new Error("invalid measurement availability");
  return { state, bytes: v.bytes === null ? null : count(v.bytes) };
}
export const historyHealthKeys = [
  "delivery",
  "database_measurement",
  "wal_measurement",
  "free_space_measurement",
  "pressure_reason",
  "accounting_available",
];
export function decodeHistoryHealth(
  v: Record<string, unknown>,
): HistoryHealth | undefined {
  if (!historyHealthKeys.some((k) => k in v)) return undefined;
  const d = object(v.delivery, [
    "accepted",
    "acknowledged",
    "discarded",
    "queue_records",
    "queue_bytes",
    "completion_records",
    "queue_record_limit",
    "queue_byte_limit",
  ]);
  return {
    delivery: {
      accepted: count(d.accepted),
      acknowledged: count(d.acknowledged),
      discarded: count(d.discarded),
      queue_records: count(d.queue_records),
      queue_bytes: count(d.queue_bytes),
      completion_records: count(d.completion_records),
      queue_record_limit: count(d.queue_record_limit),
      queue_byte_limit: count(d.queue_byte_limit),
    },
    database_measurement: measurement(v.database_measurement),
    wal_measurement: measurement(v.wal_measurement),
    free_space_measurement: measurement(v.free_space_measurement),
    pressure_reason: closed(v.pressure_reason, [
      "none",
      "queue_capacity",
      "checkpoint_reader",
      "checkpoint_unavailable",
      "budget_reservation",
      "measurement_unavailable",
      "low_space",
      "history_unavailable",
    ]),
    accounting_available: bool(v.accounting_available),
  };
}
export const trafficRecoveryKeys = ["health", "last_acknowledged", "incident"];
const trafficCauses = [
  "unknown",
  "permission",
  "ownership",
  "missing",
  "integrity",
  "full",
  "io",
  "locked",
  "deadline",
  "capacity",
];
const trafficStages = [
  "opening",
  "validation",
  "reservation",
  "begin",
  "statement",
  "commit",
  "rollback",
  "acknowledgment",
  "read",
];
const recoveryStates = [
  "healthy",
  "degraded",
  "recovering",
  "operator_action_required",
  "recovered",
  "disabled",
  "opening",
  "unavailable",
];
function timestamp(value: unknown, empty = false): string {
  const v = text(value);
  if (empty && v === "") return v;
  if (
    !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?Z$/.test(v) ||
    !Number.isFinite(Date.parse(v))
  )
    throw new Error("invalid incident timestamp");
  return v;
}
export function decodeTrafficRecovery(
  v: Record<string, unknown>,
): TrafficRecovery | undefined {
  if (!trafficRecoveryKeys.some((k) => k in v)) return undefined;
  const health = closed(v.health, recoveryStates);
  const lastAcknowledged = timestamp(v.last_acknowledged, true);
  if (v.incident === null) return { health, lastAcknowledged, incident: null };
  const i = object(v.incident, [
    "first_failure",
    "cause",
    "stage",
    "settlement",
    "recovery",
    "recovery_cause",
    "recovery_stage",
    "sqlite_code",
    "affected",
    "discarded",
  ]);
  const sqliteCode = count(i.sqlite_code);
  if (sqliteCode > 65535) throw new Error("invalid SQLite code");
  return {
    health,
    lastAcknowledged,
    incident: {
      firstFailure: timestamp(i.first_failure),
      cause: closed(i.cause, trafficCauses),
      stage: closed(i.stage, trafficStages),
      recoveryCause: closed(i.recovery_cause, trafficCauses),
      recoveryStage: closed(i.recovery_stage, trafficStages),
      settlement: closed(i.settlement, [
        "not_started",
        "rolled_back",
        "committed",
        "uncertain",
      ]),
      recovery: closed(i.recovery, [
        "degraded",
        "recovering",
        "operator_action_required",
        "recovered",
      ]),
      sqliteCode,
      affected: count(i.affected),
      discarded: count(i.discarded),
    },
  };
}
export function trafficRecoveryAction(r: TrafficRecovery): string {
  const i = r.incident;
  if (!i) return "";
  if (i.recovery === "recovered")
    return "Recording resumed; discarded history was not reconstructed.";
  if (i.recovery === "degraded")
    return "Validation passed; awaiting a new acknowledged observation.";
  if (i.settlement === "uncertain" && i.recovery === "recovering")
    return "Wait for owned settlement and safe validation. Do not replay missing history.";
  if (i.recovery === "recovering")
    return "Waiting for bounded storage revalidation. Serving remains independent.";
  if (["permission", "ownership"].includes(i.recoveryCause))
    return "Inspect history ownership and permissions; do not change them while serving.";
  if (i.recoveryCause === "integrity")
    return "Preserve history files; use a separately qualified stopped recovery plan.";
  if (["full", "capacity"].includes(i.recoveryCause))
    return "Inspect disk capacity and the traffic budget. Do not delete live database or WAL files.";
  if (i.recoveryCause === "missing")
    return "Inspect the selected history generation; do not create a replacement.";
  return "Inspect filesystem availability. Missing history never authorizes replay.";
}
export function decodeDiagnosticHealth(
  value: unknown,
): DiagnosticHealth | undefined {
  if (value === undefined) return undefined;
  const v = object(value, [
    "state",
    "epoch",
    "accepted",
    "written",
    "dropped",
    "invalid",
    "write_failures",
    "queue_records",
    "queue_bytes",
    "queue_limit",
    "writing",
    "last_successful_write",
    "overflow",
  ]);
  return {
    state: closed(v.state, [
      "ready",
      "writing",
      "pressure",
      "failed",
      "unavailable",
    ]),
    epoch: text(v.epoch),
    accepted: count(v.accepted),
    written: count(v.written),
    dropped: count(v.dropped),
    invalid: count(v.invalid),
    write_failures: count(v.write_failures),
    queue_records: count(v.queue_records),
    queue_bytes: count(v.queue_bytes),
    queue_limit: count(v.queue_limit),
    writing: bool(v.writing),
    last_successful_write:
      v.last_successful_write === null ? null : text(v.last_successful_write),
    overflow: bool(v.overflow),
  };
}
export function decodeObservations(value: unknown): Observations | undefined {
  if (value === undefined) return undefined;
  const v = object(value, [
    "epoch",
    "started_at",
    "coverage",
    "overflow",
    "protocols",
  ]);
  const vector = (v: unknown, n: number): number[] => {
    if (!Array.isArray(v) || v.length !== n)
      throw new Error("invalid observation vector");
    return v.map(count);
  };
  if (!Array.isArray(v.protocols) || v.protocols.length !== 4)
    throw new Error("invalid observation protocols");
  return {
    epoch: text(v.epoch),
    started_at: text(v.started_at),
    coverage: closed(v.coverage, ["owner_boundaries", "unavailable"]),
    overflow: bool(v.overflow),
    protocols: v.protocols.map((p, i) => {
      const r = object(p, [
        "protocol",
        "requests",
        "executions",
        "results",
        "git_reports",
        "latency",
      ]);
      if (!Array.isArray(r.latency) || r.latency.length !== 3)
        throw new Error("invalid observation stages");
      return {
        protocol: closed(r.protocol, [["mcp", "http", "connect", "git"][i]!]),
        requests: count(r.requests),
        executions: count(r.executions),
        results: vector(r.results, 5),
        git_reports: vector(r.git_reports, 3),
        latency: r.latency.map((v) => vector(v, 6)),
      };
    }),
  };
}
export function measurementText(value: Measurement | undefined): string {
  return value?.state === "available"
    ? `${value.bytes!.toLocaleString()} bytes`
    : value?.state === "absent"
      ? "Absent"
      : "Unavailable";
}
