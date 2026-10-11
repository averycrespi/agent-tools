import type {
  ActivityWindow,
  ProtocolActivity,
} from "../src/protocol-summary.ts";
export function protocolSummaryFixture(
  window: ActivityWindow = "1h",
): ProtocolActivity {
  const until = "2026-10-03T12:00:00.000000000Z";
  const from = {
    "15m": "2026-10-03T11:45:00.000000000Z",
    "1h": "2026-10-03T11:00:00.000000000Z",
    "24h": "2026-10-02T12:00:00.000000000Z",
  }[window];
  const counts = () => ({
    total: 0,
    success: 0,
    other: 0,
    reported_success: 0,
    reported_partial: 0,
    failed: 0,
    denied: 0,
    rejected: 0,
    unknown: 0,
    incomplete: 0,
  });
  return {
    window,
    from,
    until,
    coverage: "retained",
    counts: { http: counts(), git: counts(), mcp: counts() },
  };
}
