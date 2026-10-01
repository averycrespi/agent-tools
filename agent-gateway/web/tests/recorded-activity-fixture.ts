import {
  activityProtocols,
  admissionKinds,
  completionKinds,
  type RecordedActivity,
  type RecordedEvents,
} from "../src/recorded-activity.ts";

export function activityFixture(
  coverage: "complete" | "partial" | "unavailable" = "complete",
): RecordedActivity {
  const start = Date.parse("2026-09-30T12:00:00Z");
  const collection =
    start +
    (coverage === "partial"
      ? 750_000
      : coverage === "unavailable"
        ? 930_000
        : 0);
  return {
    epoch: `${"A".repeat(26)}-1`,
    collection_start: new Date(collection).toISOString(),
    as_of: "2026-09-30T12:15:30Z",
    window_start: new Date(start).toISOString(),
    window_end: "2026-09-30T12:15:00Z",
    bucket_seconds: 60,
    coverage,
    epoch_reason: "process_start",
    buckets: Array.from({ length: 15 }, (_, index) => {
      const from = start + index * 60_000,
        to = from + 60_000;
      const observed = collection < to;
      return {
        start: new Date(from).toISOString(),
        end: new Date(to).toISOString(),
        observed_start: observed
          ? new Date(Math.max(from, collection)).toISOString()
          : null,
        coverage: !observed
          ? "unavailable"
          : collection > from
            ? "partial"
            : "complete",
        counts: observed
          ? (Object.fromEntries(
              activityProtocols.map((protocol) => [
                protocol,
                {
                  admissions: Object.fromEntries(
                    admissionKinds.map((key) => [key, 0]),
                  ),
                  completions: Object.fromEntries(
                    completionKinds.map((key) => [key, 0]),
                  ),
                },
              ]),
            ) as Record<(typeof activityProtocols)[number], RecordedEvents>)
          : null,
      };
    }),
  };
}
