export const activityProtocols = [
  "mcp",
  "http_request",
  "connect",
  "http_unclassified",
] as const;
export const admissionKinds = [
  "allow",
  "deny",
  "block",
  "invalid_params",
  "unknown_tool",
  "invalid_arguments",
  "authorization_unavailable",
  "invalid_request",
  "interception_selected",
] as const;
export const completionKinds = [
  "succeeded",
  "prestart_failure",
  "downstream_failure",
  "upstream_failure",
  "outcome_unknown",
] as const;
export type ActivityProtocol = (typeof activityProtocols)[number];
type Coverage = "complete" | "partial" | "unavailable";
export interface RecordedEvents {
  admissions: Record<(typeof admissionKinds)[number], number>;
  completions: Record<(typeof completionKinds)[number], number>;
}
export interface ActivityBucket {
  start: string;
  end: string;
  observed_start: string | null;
  coverage: Coverage;
  counts: Record<ActivityProtocol, RecordedEvents> | null;
}
export interface RecordedActivity {
  epoch: string;
  collection_start: string;
  as_of: string;
  window_start: string;
  window_end: string;
  bucket_seconds: 60;
  coverage: Coverage;
  epoch_reason: "process_start" | "clock_reset" | "counter_overflow";
  buckets: ActivityBucket[];
}
function record(
  value: unknown,
  keys: readonly string[],
): Record<string, unknown> {
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).sort().join(",") !== [...keys].sort().join(",")
  )
    throw new Error("invalid recorded activity");
  return value as Record<string, unknown>;
}
function instant(value: unknown): bigint {
  if (
    typeof value !== "string" ||
    !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(value) ||
    !Number.isFinite(Date.parse(value))
  )
    throw new Error("invalid activity time");
  const parsed = Date.parse(value);
  if (new Date(parsed).toISOString().slice(0, 19) !== value.slice(0, 19))
    throw new Error("invalid activity calendar time");
  // Coverage is nanosecond evidence; Date's millisecond rounding can turn a
  // partial first minute into an apparently complete one.
  const fraction = value[19] === "." ? value.slice(20, -1) : "";
  return (
    BigInt(Date.parse(`${value.slice(0, 19)}Z`)) * 1_000_000n +
    BigInt(fraction.padEnd(9, "0"))
  );
}
function coverage(value: unknown): Coverage {
  if (value !== "complete" && value !== "partial" && value !== "unavailable")
    throw new Error("invalid activity coverage");
  return value;
}
function counts(value: unknown, keys: readonly string[]) {
  const row = record(value, keys);
  for (const count of Object.values(row))
    if (typeof count !== "number" || !Number.isSafeInteger(count) || count < 0)
      throw new Error("invalid activity count");
}
export function decodeRecordedActivity(value: unknown): RecordedActivity {
  const root = record(value, [
    "epoch",
    "collection_start",
    "as_of",
    "window_start",
    "window_end",
    "bucket_seconds",
    "coverage",
    "epoch_reason",
    "buckets",
  ]);
  if (
    typeof root.epoch !== "string" ||
    !/^[A-Z2-7]{26}-[1-9]\d{0,19}$/.test(root.epoch) ||
    !["process_start", "clock_reset", "counter_overflow"].includes(
      String(root.epoch_reason),
    ) ||
    root.bucket_seconds !== 60
  )
    throw new Error("invalid activity epoch");
  const asOf = instant(root.as_of),
    start = instant(root.window_start),
    end = instant(root.window_end),
    collection = instant(root.collection_start);
  const state = coverage(root.coverage);
  const minute = 60_000_000_000n;
  const currentMinute = asOf - (((asOf % minute) + minute) % minute);
  if (
    end !== currentMinute ||
    start !== end - 15n * minute ||
    collection > end + minute ||
    !Array.isArray(root.buckets) ||
    root.buckets.length !== 15
  )
    throw new Error("invalid activity bounds");
  let observed = 0,
    complete = 0;
  root.buckets.forEach((value, index) => {
    const bucket = record(value, [
      "start",
      "end",
      "observed_start",
      "coverage",
      "counts",
    ]);
    const from = instant(bucket.start),
      to = instant(bucket.end),
      covered = coverage(bucket.coverage);
    if (from !== start + BigInt(index) * minute || to !== from + minute)
      throw new Error("invalid activity bucket");
    if (covered === "unavailable") {
      if (
        bucket.observed_start !== null ||
        bucket.counts !== null ||
        (state !== "unavailable" && collection < to)
      )
        throw new Error("unobserved activity must be nonnumeric");
      return;
    }
    observed++;
    if (covered === "complete") complete++;
    const observedStart = instant(bucket.observed_start);
    if (
      observedStart !== (from > collection ? from : collection) ||
      observedStart >= to ||
      (covered === "complete") !== (observedStart === from)
    )
      throw new Error("invalid observed activity bounds");
    const protocols = record(bucket.counts, activityProtocols);
    for (const protocol of activityProtocols) {
      const events = record(protocols[protocol], ["admissions", "completions"]);
      counts(events.admissions, admissionKinds);
      counts(events.completions, completionKinds);
    }
  });
  if (
    (state === "unavailable") !== (observed === 0) ||
    (state === "complete") !== (complete === 15)
  )
    throw new Error("inconsistent activity coverage");
  return value as RecordedActivity;
}

export function activityCounts(events: RecordedEvents) {
  const a = events.admissions,
    c = events.completions;
  return {
    admissions: admissionKinds.reduce((sum, key) => sum + BigInt(a[key]), 0n),
    failures:
      BigInt(c.prestart_failure) +
      BigInt(c.downstream_failure) +
      BigInt(c.upstream_failure),
    refusals:
      BigInt(a.deny) +
      BigInt(a.block) +
      BigInt(a.invalid_params) +
      BigInt(a.unknown_tool) +
      BigInt(a.invalid_arguments) +
      BigInt(a.authorization_unavailable) +
      BigInt(a.invalid_request),
    unknown: BigInt(c.outcome_unknown),
    interception: BigInt(a.interception_selected),
    succeeded: BigInt(c.succeeded),
  };
}
export function activityTotals(
  buckets: readonly ActivityBucket[],
  protocol: ActivityProtocol,
) {
  const observed = buckets.filter((bucket) => bucket.counts !== null);
  if (observed.length === 0) return undefined;
  const totals = {
    admissions: 0n,
    failures: 0n,
    refusals: 0n,
    unknown: 0n,
    interception: 0n,
    succeeded: 0n,
  };
  for (const bucket of observed) {
    const values = activityCounts(bucket.counts![protocol]);
    for (const key of Object.keys(totals) as (keyof typeof totals)[])
      totals[key] += values[key];
  }
  return totals;
}
