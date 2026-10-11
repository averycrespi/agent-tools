export const activityWindows = ["15m", "1h", "24h"] as const;
export type ActivityWindow = (typeof activityWindows)[number];
export type Protocol = "http" | "git" | "mcp";
export const outcomeKeys = [
  "success",
  "other",
  "reported_success",
  "failed",
  "denied",
  "rejected",
  "unknown",
  "incomplete",
  "reported_partial",
] as const;
export type OutcomeCounts = Record<(typeof outcomeKeys)[number], number> & {
  total: number;
};
export interface ProtocolActivity {
  window: ActivityWindow;
  from: string;
  until: string;
  coverage: "retained" | "partial" | "unavailable";
  counts: Record<Protocol, OutcomeCounts> | null;
}
function object(
  value: unknown,
  keys: readonly string[],
): Record<string, unknown> {
  if (
    !value ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).sort().join(",") !== [...keys].sort().join(",")
  )
    throw new Error("Invalid protocol summary");
  return value as Record<string, unknown>;
}
export function validActivityInstant(value: string): boolean {
  return (
    /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{9}Z$/.test(value) &&
    Number.isFinite(Date.parse(value)) &&
    new Date(value).toISOString() === value.slice(0, 23) + "Z"
  );
}
function instantNanos(value: string): bigint {
  return BigInt(Date.parse(value)) * 1_000_000n + BigInt(value.slice(23, 29));
}
export function validActivityRange(
  query: Readonly<Record<string, string>>,
): boolean {
  const from = query.filter_from,
    until = query.filter_until;
  return (
    (from === undefined && until === undefined) ||
    (from !== undefined &&
      until !== undefined &&
      validActivityInstant(from) &&
      validActivityInstant(until) &&
      from < until &&
      instantNanos(until) - instantNanos(from) <= 86_400_000_000_000n)
  );
}
export function decodeProtocolActivity(value: unknown): ProtocolActivity {
  const r = object(value, ["window", "from", "until", "coverage", "counts"]);
  if (
    !activityWindows.includes(r.window as ActivityWindow) ||
    typeof r.from !== "string" ||
    typeof r.until !== "string" ||
    !validActivityRange({ filter_from: r.from, filter_until: r.until }) ||
    !["retained", "partial", "unavailable"].includes(r.coverage as string)
  )
    throw new Error("Invalid protocol summary");
  const duration = { "15m": 900n, "1h": 3600n, "24h": 86400n }[
    r.window as ActivityWindow
  ];
  if (
    instantNanos(r.until) - instantNanos(r.from) !==
    duration * 1_000_000_000n
  )
    throw new Error("Inconsistent activity window");
  if (r.coverage === "unavailable") {
    if (r.counts !== null) throw new Error("Invalid unavailable summary");
  } else {
    const protocols = object(r.counts, ["http", "git", "mcp"]);
    for (const protocol of ["http", "git", "mcp"]) {
      const counts = object(protocols[protocol], ["total", ...outcomeKeys]);
      for (const count of Object.values(counts))
        if (
          typeof count !== "number" ||
          !Number.isSafeInteger(count) ||
          count < 0
        )
          throw new Error("Invalid activity count");
      if (
        outcomeKeys.reduce(
          (sum, key) => sum + BigInt(counts[key] as number),
          0n,
        ) !== BigInt(counts.total as number)
      )
        throw new Error("Inconsistent activity total");
    }
  }
  return value as ProtocolActivity;
}
export function activityHref(
  protocol: Protocol,
  summary: ProtocolActivity,
): string {
  const path = protocol === "mcp" ? "mcp/invocations" : `${protocol}/traffic`;
  const query = new URLSearchParams({
    filter_from: summary.from,
    filter_until: summary.until,
  });
  if (protocol === "http") query.set("filter_type", "request");
  return `#/${path}?${query}`;
}
// The collection owner supplies a complete unfiltered total; never count page rows.
export function decodeInventoryTotal(value: unknown): number {
  const r = object(value, ["items", "next_cursor", "total_count", "offset"]);
  if (
    !Array.isArray(r.items) ||
    r.items.length > 1 ||
    r.offset !== 0 ||
    typeof r.total_count !== "number" ||
    !Number.isSafeInteger(r.total_count) ||
    r.total_count < r.items.length ||
    r.items.length !== Math.min(r.total_count, 1) ||
    (r.total_count > 1
      ? typeof r.next_cursor !== "string" || !r.next_cursor
      : r.next_cursor !== null)
  )
    throw new Error("Invalid inventory count");
  return r.total_count;
}
