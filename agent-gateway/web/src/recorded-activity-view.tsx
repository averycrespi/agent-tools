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

export function RecordedActivityView({
  value,
  current,
}: {
  value: RecordedActivity;
  current: boolean;
}) {
  return (
    <div class="activity-band">
      <p
        class={
          value.coverage === "complete" && current
            ? "overview-context"
            : "overview-evidence"
        }
      >
        Last 15 completed minutes ·{" "}
        {value.coverage === "complete"
          ? "Complete coverage"
          : value.coverage === "partial"
            ? "Partial coverage"
            : "Window unavailable"}
        {current ? "" : " · Last known"}
      </p>
      {value.epoch_reason === "clock_reset" && (
        <p class="overview-evidence">
          Collection reset after a clock discontinuity.
        </p>
      )}
      {value.epoch_reason === "counter_overflow" && (
        <p class="overview-evidence">
          Collection reset after counter overflow.
        </p>
      )}
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
                <dl class="activity-totals">
                  <div>
                    <dt>Recorded admissions</dt>
                    <dd>{total.admissions.toLocaleString()}</dd>
                  </div>
                  <div>
                    <dt>Known failure completions</dt>
                    <dd>{total.failures.toLocaleString()}</dd>
                  </div>
                  <div>
                    <dt>Refusals</dt>
                    <dd>{total.refusals.toLocaleString()}</dd>
                  </div>
                  <div>
                    <dt>Unknown completions</dt>
                    <dd>{total.unknown.toLocaleString()}</dd>
                  </div>
                  {protocol === "connect" && (
                    <div>
                      <dt>Interception selections</dt>
                      <dd>{total.interception.toLocaleString()}</dd>
                    </div>
                  )}
                </dl>
              ) : (
                <p>Unobserved window</p>
              )}
              <a
                class="activity-history-link"
                href={
                  protocol === "mcp"
                    ? "#/mcp/invocations"
                    : protocol === "connect"
                      ? "#/http/traffic?filter_type=connect"
                      : "#/http/traffic?filter_type=request"
                }
              >
                {protocol === "mcp"
                  ? "MCP invocation history"
                  : protocol === "connect"
                    ? "CONNECT history"
                    : "HTTP request history"}
              </a>
            </section>
          );
        })}
      </div>
      <details class="detail-group">
        <summary>Minute evidence</summary>
        <dl class="detail-facts">
          <div>
            <dt>Window</dt>
            <dd>
              {formatUserTime(value.window_start)} –{" "}
              {formatUserTime(value.window_end)}
            </dd>
          </div>
          <div>
            <dt>As of</dt>
            <dd>{formatUserTime(value.as_of)}</dd>
          </div>
          <div>
            <dt>Collection started</dt>
            <dd>{formatUserTime(value.collection_start)}</dd>
          </div>
          <div>
            <dt>Epoch</dt>
            <dd>{value.epoch}</dd>
          </div>
          <div>
            <dt>Collection reason</dt>
            <dd>{value.epoch_reason.replaceAll("_", " ")}</dd>
          </div>
        </dl>
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
    </div>
  );
}
