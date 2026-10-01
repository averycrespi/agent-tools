import {
  activityCounts,
  activityProtocols,
  activityTotals,
  type ActivityProtocol,
  type RecordedActivity,
} from "./recorded-activity";
import { formatUserTime } from "./time";

const labels: Record<ActivityProtocol, string> = {
  mcp: "MCP calls",
  http_request: "HTTP requests",
  connect: "CONNECT",
  http_unclassified: "HTTP unclassified",
};

function Trend({
  value,
  protocol,
}: {
  value: RecordedActivity;
  protocol: ActivityProtocol;
}) {
  const samples = value.buckets.map((bucket) =>
    bucket.counts ? activityCounts(bucket.counts[protocol]) : undefined,
  );
  const maximum = samples.reduce(
    (max, sample) =>
      sample
        ? [sample.admissions, sample.failures].reduce(
            (m, v) => (v > m ? v : m),
            max,
          )
        : max,
    1n,
  );
  const lines = (key: "admissions" | "failures") => {
    const groups: string[][] = [];
    let points: string[] = [];
    samples.forEach((sample, index) => {
      if (!sample) {
        if (points.length) groups.push(points);
        points = [];
        return;
      }
      const y = 64 - Number((sample[key] * 56n) / maximum);
      points.push(`${8 + index * 16},${y}`);
    });
    if (points.length) groups.push(points);
    return groups.map((group, index) =>
      group.length === 1 ? (
        <circle
          key={index}
          class={`activity-line ${key}`}
          cx={Number(group[0]!.split(",")[0])}
          cy={Number(group[0]!.split(",")[1])}
          r="2"
        />
      ) : (
        <polyline
          key={index}
          class={`activity-line ${key}`}
          points={group.join(" ")}
        />
      ),
    );
  };
  return (
    <svg
      class="activity-trend"
      viewBox="0 0 240 76"
      role="img"
      aria-label={`${labels[protocol]} per-minute recorded admissions and known failure completions; exact values in Minute evidence`}
    >
      <title>{labels[protocol]} recorded activity, oldest minute first</title>
      {lines("admissions")}
      {lines("failures")}
    </svg>
  );
}

export function RecordedActivityView({
  value,
  current,
}: {
  value: RecordedActivity;
  current: boolean;
}) {
  return (
    <div class="activity-band">
      <p class="overview-context">
        Last 15 completed minutes · {formatUserTime(value.window_start)} –{" "}
        {formatUserTime(value.window_end)}. As of {formatUserTime(value.as_of)}.
      </p>
      <p
        class={
          value.coverage === "complete" && current
            ? "overview-context"
            : "overview-evidence"
        }
      >
        {value.coverage === "complete"
          ? "Complete observed window"
          : value.coverage === "partial"
            ? "Partial coverage; counts cover only observed minutes"
            : "Window unavailable; no numeric activity claim"}
        {current ? "." : " · last known; current activity unknown."}{" "}
        {value.epoch_reason === "clock_reset"
          ? "Collection reset after a clock discontinuity."
          : value.epoch_reason === "counter_overflow"
            ? "Collection reset after counter overflow."
            : "Collection began with this process."}
      </p>
      <div class="activity-series">
        {(["mcp", "http_request", "connect"] as const).map((protocol) => {
          const total = activityTotals(value.buckets, protocol);
          return (
            <section
              key={protocol}
              class="activity-protocol"
              aria-label={`${labels[protocol]} recorded activity`}
            >
              <h3>{labels[protocol]}</h3>
              {total ? (
                <>
                  <dl class="activity-totals">
                    <div>
                      <dt>Recorded admissions</dt>
                      <dd>{total.admissions.toLocaleString()}</dd>
                    </div>
                    <div>
                      <dt>Known failure completions</dt>
                      <dd>{total.failures.toLocaleString()}</dd>
                    </div>
                  </dl>
                  <Trend value={value} protocol={protocol} />
                  <p>
                    {total.refusals.toLocaleString()} recorded refusals ·{" "}
                    {total.unknown.toLocaleString()} explicit unknown
                    completions
                    {protocol === "connect"
                      ? ` · ${total.interception.toLocaleString()} interception selections`
                      : ""}
                  </p>
                </>
              ) : (
                <p>Unobserved window</p>
              )}
            </section>
          );
        })}
      </div>
      {value.coverage !== "unavailable" && (
        <p class="activity-legend">
          <span class="activity-admission-key">Admissions</span> ·{" "}
          <span class="activity-failure-key">Known failure completions</span> ·
          gaps are unobserved minutes
        </p>
      )}
      <p>
        {activityTotals(
          value.buckets,
          "http_unclassified",
        )?.admissions.toLocaleString() ?? "Unavailable"}{" "}
        HTTP unclassified recorded admissions; invalid target evidence cannot
        identify request versus CONNECT.
      </p>
      <p class="overview-context">
        Admissions and completions settle separately. Their difference is not
        in-flight work; missing completions or zero failures do not establish
        success. CONNECT is not an inner request; interception selection is not
        completion.
      </p>
      <details>
        <summary>Minute evidence</summary>
        <p>
          Collection epoch {value.epoch} · started{" "}
          {formatUserTime(value.collection_start)}. Current incomplete minute is
          excluded.
        </p>
        {activityProtocols.map((protocol) => (
          <div key={protocol} class="activity-text-evidence">
            <h4>{labels[protocol]}</h4>
            <ol>
              {value.buckets.map((bucket) => {
                const counts = bucket.counts
                  ? activityCounts(bucket.counts[protocol])
                  : undefined;
                return (
                  <li key={bucket.start}>
                    <time dateTime={bucket.start}>
                      {formatUserTime(bucket.start)}
                    </time>{" "}
                    –{" "}
                    <time dateTime={bucket.end}>
                      {formatUserTime(bucket.end)}
                    </time>
                    : {bucket.coverage}
                    {bucket.observed_start && bucket.coverage === "partial"
                      ? ` from ${formatUserTime(bucket.observed_start)}`
                      : ""}
                    {counts
                      ? `; ${counts.admissions} admissions, ${counts.failures} known failure completions, ${counts.unknown} explicit unknown completions, ${counts.succeeded} recorded succeeded completions, ${counts.refusals} refusals, ${counts.interception} interception selections`
                      : "; unobserved"}
                    {bucket.counts &&
                      `; failure classes: ${bucket.counts[protocol].completions.prestart_failure} prestart, ${bucket.counts[protocol].completions.downstream_failure} downstream, ${bucket.counts[protocol].completions.upstream_failure} upstream`}
                  </li>
                );
              })}
            </ol>
          </div>
        ))}
      </details>
      <div class="activity-history-links">
        <a href="#/mcp/invocations">MCP invocation history</a>
        <a href="#/http/traffic">HTTP and CONNECT history</a>
      </div>
    </div>
  );
}
