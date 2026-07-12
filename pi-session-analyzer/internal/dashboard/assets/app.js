import {
  matrixSearch,
  overviewSearch,
  parseState,
  stateSearch,
  withBucket,
} from "./state.js";
import {
  axisTickIndexes,
  bucketLabel,
  collapsedStreamText,
  flatTrendNote,
  formatCost,
  formatInteger,
  rateLabel,
  severityLabel,
  statusClass,
  tokenRowTotal,
  tokenValues,
  unwrapResponse,
} from "./view-model.js";

const $ = (selector) => document.querySelector(selector);
const node = (tag, className = "", text = "") => {
  const value = document.createElement(tag);
  if (className) value.className = className;
  if (text !== "") value.textContent = text;
  return value;
};
const clear = (target) => target.replaceChildren();
const browserTimezone =
  Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
let state = parseState(location.search, browserTimezone);
let overview;
let matrixCursor = "";
let streamCursor = "";
let tokenCursor = "";
let goalOffset = 0;
let todoSnapshotOffset = 0;
let todoItemOffset = 0;
let freshFindingOffset = 0;
let staleFindingOffset = 0;
let activeController;

function announce(message, kind = "") {
  const notice = $("#notice");
  notice.className = `notice ${kind}`.trim();
  notice.textContent = message;
}

async function runAction(action) {
  try {
    await action();
  } catch (error) {
    if (error.name !== "AbortError")
      announce(`${error.message}. Retry or narrow the page.`, "error");
  }
}

async function request(path, signal) {
  const response = await fetch(path, {
    signal,
    headers: { Accept: "application/json" },
  });
  const body = await response
    .json()
    .catch(() => ({ error: "Invalid local response" }));
  if (!response.ok)
    throw new Error(body.error || `Local query failed (${response.status})`);
  const unwrapped = unwrapResponse(body);
  if (unwrapped.truncated) {
    announce(
      "A response reached the safety cap. Narrow the range or load a smaller page.",
      "warning",
    );
    if (unwrapped.value === null)
      throw new Error("The local response exceeded the safety cap");
  }
  return unwrapped.value;
}

function syncControls() {
  $("#range").value = state.range;
  $("#bucket").value = state.bucket;
  $("#timezone").value = state.timezone;
  $("#direction").value = state.direction;
  $("#cwd").value = state.cwd;
  $("#untimed").checked = state.untimed;
  $("#date-from").value = state.dateFrom;
  $("#date-to").value = state.dateTo;
  $("#clear-bucket").hidden = !(state.from && state.to);
}

function navigate(next, replace = false) {
  state = next;
  history[replace ? "replaceState" : "pushState"](null, "", stateSearch(state));
  syncControls();
  loadDashboard();
}

function kpi(label, value, note = "") {
  const box = node("div", "kpi");
  box.append(node("span", "label", label), node("strong", "value", value));
  if (note) box.append(node("p", "muted", note));
  return box;
}

function tag(text, status = "neutral") {
  return node("span", `tag ${statusClass(status)}`, text);
}

function metricList(rows) {
  const list = node("dl", "metric-list");
  for (const [label, value] of rows) {
    const row = node("div", "metric-row");
    row.append(node("dt", "", label), node("dd", "", String(value)));
    list.append(row);
  }
  return list;
}

function localDate(unix) {
  if (unix === null || unix === undefined) return "Untimed";
  try {
    return new Intl.DateTimeFormat(undefined, {
      dateStyle: "medium",
      timeStyle: "short",
      timeZone: state.timezone,
    }).format(new Date(unix * 1000));
  } catch {
    return new Date(unix * 1000).toISOString();
  }
}

function renderOverview(data) {
  const buckets = data.buckets || [];
  overview = data;
  const totals = buckets.reduce(
    (sum, bucket) => ({
      sessions: sum.sessions + bucket.sessions,
      cost: sum.cost + bucket.cost_as_logged,
      calls: sum.calls + bucket.tool_calls,
      output: sum.output + bucket.output_tokens,
      reasoning: sum.reasoning + bucket.reasoning_tokens,
      read: sum.read + bucket.cache_read_tokens,
      write: sum.write + bucket.cache_write_tokens,
      compact: sum.compact + bucket.compactions,
      guards: sum.guards + bucket.broker_guards,
    }),
    {
      sessions: 0,
      cost: 0,
      calls: 0,
      output: 0,
      reasoning: 0,
      read: 0,
      write: 0,
      compact: 0,
      guards: 0,
    },
  );
  const kpis = $("#kpis");
  clear(kpis);
  kpis.append(
    kpi(
      "Sessions started",
      formatInteger(totals.sessions),
      `${data.bucket} buckets · ${data.timezone}`,
    ),
    kpi("Cost as logged by Pi", formatCost(totals.cost)),
    kpi("Tool calls", formatInteger(totals.calls)),
    kpi("Compactions / broker guards", `${totals.compact} / ${totals.guards}`),
    kpi("Output tokens", formatInteger(totals.output)),
    kpi("Reasoning tokens", formatInteger(totals.reasoning)),
    kpi("Cache-read tokens", formatInteger(totals.read)),
    kpi("Cache-write tokens", formatInteger(totals.write)),
    kpi(
      "Untimed sessions",
      formatInteger(data.untimed_sessions),
      "Excluded from temporal charts",
    ),
  );
  renderChart(buckets, data.timezone);
  renderToolOverview(data.tool_outcomes || {});
  renderDetectorOverview(data.detectors || []);
  renderOutcomeOverview(data.signals || {});
  renderDistributions(data.signals || {});
}

function renderChart(buckets, timezone) {
  const host = $("#session-chart");
  clear(host);
  const max = Math.max(1, ...buckets.map((bucket) => bucket.sessions));
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "chart");
  svg.setAttribute("viewBox", `0 0 ${Math.max(600, buckets.length * 12)} 190`);
  svg.setAttribute("role", "group");
  svg.setAttribute("aria-label", "Sessions started by calendar bucket");
  const width = Math.max(600, buckets.length * 12);
  const slot = width / Math.max(1, buckets.length);
  buckets.forEach((bucket, index) => {
    const bar = document.createElementNS(svg.namespaceURI, "rect");
    const height = (bucket.sessions / max) * 150;
    bar.setAttribute("x", String(index * slot + 1));
    bar.setAttribute("y", String(165 - height));
    bar.setAttribute("width", String(Math.max(2, slot - 2)));
    bar.setAttribute("height", String(Math.max(1, height)));
    let barClass = bucket.partial ? "bar partial" : "bar";
    if (
      String(bucket.start_unix) === state.from &&
      String(bucket.end_unix) === state.to
    )
      barClass += " selected";
    bar.setAttribute("class", barClass);
    bar.setAttribute("tabindex", "0");
    bar.setAttribute("role", "button");
    bar.setAttribute("aria-label", bucketLabel(bucket, timezone));
    const select = () => navigate(withBucket(state, bucket));
    bar.addEventListener("click", select);
    bar.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        select();
      }
    });
    svg.append(bar);
  });
  if (buckets.length) {
    const baseline = document.createElementNS(svg.namespaceURI, "line");
    baseline.setAttribute("x1", "0");
    baseline.setAttribute("x2", String(width));
    baseline.setAttribute("y1", "166");
    baseline.setAttribute("y2", "166");
    baseline.setAttribute("class", "axis");
    svg.append(baseline);
    const peak = document.createElementNS(svg.namespaceURI, "text");
    peak.setAttribute("x", "1");
    peak.setAttribute("y", "10");
    peak.setAttribute("class", "tick");
    peak.textContent = `peak ${formatInteger(Math.max(...buckets.map((bucket) => bucket.sessions)))} sessions / bucket`;
    svg.append(peak);
    for (const index of axisTickIndexes(buckets.length)) {
      const label = document.createElementNS(svg.namespaceURI, "text");
      label.setAttribute("y", "184");
      label.setAttribute("class", "tick");
      const anchor =
        index === 0 ? "start" : index === buckets.length - 1 ? "end" : "middle";
      label.setAttribute("text-anchor", anchor);
      const center = index * slot + slot / 2;
      label.setAttribute(
        "x",
        String(anchor === "start" ? 1 : anchor === "end" ? width - 1 : center),
      );
      label.textContent = buckets[index].key;
      svg.append(label);
    }
  }
  host.append(svg);
  $("#chart-summary").textContent = buckets.length
    ? `${buckets.length} ${overview.bucket} buckets. Bars are keyboard selectable.`
    : "No timed sessions in this range.";
  const tbody = $("#bucket-table tbody");
  clear(tbody);
  for (const bucket of buckets) {
    const row = node("tr");
    const select = node(
      "button",
      "matrix-session",
      `${bucket.key}${bucket.partial ? " · partial" : ""}`,
    );
    select.type = "button";
    select.addEventListener("click", () => navigate(withBucket(state, bucket)));
    const first = node("td");
    first.append(select);
    for (const value of [
      first,
      node("td", "", bucket.sessions),
      node("td", "", formatCost(bucket.cost_as_logged)),
      node("td", "", bucket.tool_calls),
      node("td", "", bucket.compactions),
    ])
      row.append(value);
    tbody.append(row);
  }
}

function renderTrends(buckets, stopReasons, signals, unavailable = false) {
  const host = $("#trend-grid");
  clear(host);
  if (unavailable) {
    host.append(
      node(
        "p",
        "muted trend-flat",
        "Per-bucket trend signals are unavailable. Refresh the index view to retry.",
      ),
    );
    return;
  }
  const value = (key) => (bucket) => bucket[key] || 0;
  const metrics = [
    ["Cost as logged", value("cost_as_logged"), formatCost],
    ["Tool-call volume", value("tool_calls"), formatInteger],
    ["Output tokens", value("output_tokens"), formatInteger],
    ["Reasoning tokens", value("reasoning_tokens"), formatInteger],
    ["Cache-read tokens", value("cache_read_tokens"), formatInteger],
    ["Cache-write tokens", value("cache_write_tokens"), formatInteger],
    ["Compactions", value("compactions"), formatInteger],
    ["Broker guards", value("broker_guards"), formatInteger],
    [
      "Fresh error findings",
      (_bucket, index) => signals.fresh_error?.[index] || 0,
      formatInteger,
    ],
    [
      "Fresh warning findings",
      (_bucket, index) => signals.fresh_warn?.[index] || 0,
      formatInteger,
    ],
    [
      "Fresh info findings",
      (_bucket, index) => signals.fresh_info?.[index] || 0,
      formatInteger,
    ],
    [
      "Fresh structural findings",
      (_bucket, index) => signals.fresh_structural?.[index] || 0,
      formatInteger,
    ],
    [
      "Fresh heuristic findings",
      (_bucket, index) => signals.fresh_heuristic?.[index] || 0,
      formatInteger,
    ],
    [
      "Detector coverage gaps",
      (_bucket, index) =>
        (signals.detector_failed?.[index] || 0) +
        (signals.detector_not_run?.[index] || 0),
      formatInteger,
    ],
    [
      "Active goals",
      (_bucket, index) => signals.goal_active?.[index] || 0,
      formatInteger,
    ],
    [
      "Completed goals",
      (_bucket, index) => signals.goal_complete?.[index] || 0,
      formatInteger,
    ],
  ];
  for (const series of stopReasons)
    metrics.push([
      `Stop: ${series.value}`,
      (_bucket, index) => series.counts[index] || 0,
      formatInteger,
    ]);
  const active = [];
  const flat = [];
  for (const metric of metrics) {
    const [, getValue] = metric;
    const total = buckets.reduce(
      (sum, bucket, index) => sum + getValue(bucket, index),
      0,
    );
    (total > 0 ? active : flat).push(metric);
  }
  for (const [label, getValue, format] of active) {
    const panel = node("section", "panel");
    panel.append(node("h3", "", label));
    const max = Math.max(1, ...buckets.map(getValue));
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("class", "mini-chart");
    svg.setAttribute("viewBox", "0 0 300 74");
    svg.setAttribute("role", "img");
    svg.setAttribute(
      "aria-label",
      `${label} trend across ${buckets.length} buckets`,
    );
    buckets.forEach((bucket, index) => {
      const rect = document.createElementNS(svg.namespaceURI, "rect");
      const width = 300 / Math.max(1, buckets.length);
      const height = (getValue(bucket, index) / max) * 68;
      rect.setAttribute("x", String(index * width));
      rect.setAttribute("y", String(72 - height));
      rect.setAttribute("width", String(Math.max(1, width - 1)));
      rect.setAttribute("height", String(Math.max(1, height)));
      svg.append(rect);
    });
    panel.append(svg);
    const details = node("details");
    details.append(
      node(
        "summary",
        "trend-summary",
        `Accessible values · total ${format(buckets.reduce((sum, bucket, index) => sum + getValue(bucket, index), 0))}`,
      ),
    );
    const list = node("dl", "metric-list");
    for (const [index, bucket] of buckets.entries()) {
      const row = node("div", "metric-row");
      row.append(
        node("dt", "", bucket.key),
        node("dd", "", format(getValue(bucket, index))),
      );
      list.append(row);
    }
    details.append(list);
    panel.append(details);
    host.append(panel);
  }
  if (flat.length)
    host.append(
      node(
        "p",
        "muted trend-flat",
        flatTrendNote(flat.map(([label]) => label)),
      ),
    );
  if (!active.length && !flat.length)
    host.append(
      node("p", "muted trend-flat", "No trend series in this range."),
    );
}

function renderToolOverview(report) {
  const host = $("#tool-overview");
  clear(host);
  if (report.analysis_truncated)
    host.append(
      node(
        "p",
        "danger-text",
        `${report.total_calls} calls exceed the analysis bound; rates are unavailable.`,
      ),
    );
  host.append(
    metricList([
      ["All calls", report.total_calls || 0],
      ["Classifiable", report.totals?.classifiable || 0],
      ["Unknown", report.totals?.unknown || 0],
      ["Orphan results", report.data_quality?.orphan_results || 0],
      ["Multiple results", report.data_quality?.multiple_results || 0],
    ]),
  );
  for (const tool of report.tools || []) {
    const details = node("details");
    details.append(
      node(
        "summary",
        "",
        `${tool.tool} · ${rateLabel(tool.error_rate, tool.totals)}`,
      ),
      metricList([
        ["Calls", tool.totals.calls],
        ["Successes", tool.totals.successes],
        ["Unknown", tool.totals.unknown],
      ]),
    );
    host.append(details);
  }
}

function renderDetectorOverview(detectors) {
  const host = $("#detector-overview");
  clear(host);
  if (!detectors.length) {
    host.append(node("p", "muted", "No detector registry metadata returned."));
    return;
  }
  for (const detector of detectors) {
    const row = node("div", "metric-row");
    row.append(
      node("span", "", detector.detector),
      node(
        "span",
        "",
        `${detector.fresh.error}E ${detector.fresh.warn}W ${detector.fresh.info}I · ${detector.fresh.structural} structural / ${detector.fresh.heuristic} heuristic · ${detector.coverage.success} ok / ${detector.coverage.failed} failed / ${detector.coverage.not_run} not run`,
      ),
    );
    host.append(row);
  }
}

function renderOutcomeOverview(signals) {
  const host = $("#outcome-overview");
  clear(host);
  const group = (title, values) => {
    host.append(node("p", "eyebrow", title));
    for (const item of values || []) {
      const row = node("div", "metric-row");
      row.append(node("span", "", item.value), node("span", "", item.count));
      host.append(row);
    }
  };
  group("GOAL OUTCOME", signals.goals);
  group("FINAL STOP REASON", signals.stops);
}

function renderDistributions(signals) {
  const host = $("#distribution-overview");
  clear(host);
  const group = (title, values) => {
    host.append(node("p", "eyebrow", title));
    for (const item of values || []) {
      const row = node("div", "metric-row");
      row.append(node("span", "", item.label), node("span", "", item.count));
      host.append(row);
    }
  };
  group("RECORDS", signals.records);
  group("MESSAGE TURNS", signals.turns);
}

function renderMatrix(page, append = false) {
  const tbody = $("#matrix-table tbody");
  if (!append) clear(tbody);
  for (const row of page.rows || []) {
    const tr = node("tr");
    const button = node("button", "matrix-session", row.id.slice(0, 12));
    button.type = "button";
    button.append(node("small", "", "Open drilldown"));
    button.addEventListener("click", () =>
      navigate({ ...state, session: row.id }),
    );
    const session = node("td");
    session.append(button);
    const start = node("td");
    start.append(
      node("span", "", localDate(row.started_at_unix)),
      node("small", "muted", ` ${row.cwd || "No cwd"}`),
    );
    const splitTokens = `O ${formatInteger(row.output_tokens)} · R ${formatInteger(row.reasoning_tokens)} · CR ${formatInteger(row.cache_read_tokens)} · CW ${formatInteger(row.cache_write_tokens)}`;
    const toolCoverage = row.tool_analysis_truncated
      ? `Unavailable · 0/${row.tool_total_calls}`
      : rateLabel(row.tool_error_rate, row.tool_outcomes);
    const findings = node("td");
    findings.append(
      tag(severityLabel(row.fresh_severity), row.fresh_severity),
      node(
        "div",
        "muted",
        `${row.detector_coverage.success} ok / ${row.detector_coverage.failed} failed / ${row.detector_coverage.not_run} not run`,
      ),
    );
    const outcomes = node("td");
    outcomes.append(
      tag(`Goal ${row.goal_outcome}`, row.goal_outcome),
      tag(`TODO ${row.todo_outcome}`, row.todo_outcome),
      tag(
        row.stop_reason || "No stop",
        row.stop_reason ? "neutral" : "unknown",
      ),
    );
    const schema = node("td");
    schema.append(
      tag(
        `${row.malformed_records} malformed`,
        row.malformed_records ? "error" : "success",
      ),
      tag(
        `${row.unknown_records} unknown`,
        row.unknown_records ? "warn" : "success",
      ),
    );
    for (const cell of [
      session,
      start,
      node("td", "", `${row.records} / ${row.turns}`),
      node("td", "", splitTokens),
      node("td", "", toolCoverage),
      node("td", "", `${row.compactions} / ${row.broker_guards}`),
      findings,
      outcomes,
      schema,
    ])
      tr.append(cell);
    tbody.append(tr);
  }
  matrixCursor = page.next_cursor || "";
  $("#matrix-more").hidden = !matrixCursor;
  if (!(page.rows || []).length && !append) {
    const tr = node("tr");
    const td = node(
      "td",
      "muted",
      state.untimed ? "No untimed sessions." : "No sessions in this range.",
    );
    td.colSpan = 9;
    tr.append(td);
    tbody.append(tr);
  }
}

function renderHeader(header) {
  const host = $("#session-header");
  clear(host);
  if (header.content_truncated)
    host.append(
      kpi(
        "Safety state",
        "TRUNCATED",
        "One or more header labels reached its bound.",
      ),
    );
  host.append(
    kpi("Session", header.id.slice(0, 12), localDate(header.started_at_unix)),
    kpi("Records / turns", `${header.records} / ${header.turns}`),
    kpi("Cost as logged", formatCost(header.cost_as_logged)),
    kpi("Output", formatInteger(header.output_tokens)),
    kpi("Reasoning", formatInteger(header.reasoning_tokens)),
    kpi(
      "Cache read / write",
      `${formatInteger(header.cache_read_tokens)} / ${formatInteger(header.cache_write_tokens)}`,
    ),
    kpi(
      "Compactions / guards",
      `${header.compactions} / ${header.broker_guards}`,
    ),
    kpi(
      "Goal / stop",
      `${header.goal_outcome} / ${header.stop_reason || "absent"}`,
    ),
  );
}

function renderDetailTools(report) {
  const host = $("#detail-tools");
  clear(host);
  renderToolOverviewInto(host, report);
}
function renderToolOverviewInto(host, report) {
  if (
    report.analysis_truncated ||
    report.analysis_content_truncated ||
    report.tools_truncated ||
    report.content_truncated
  )
    host.append(
      node(
        "p",
        "danger-text",
        "Tool analysis is bounded or truncated; coverage metadata remains explicit.",
      ),
    );
  for (const tool of report.tools || []) {
    const row = node("div", "metric-row");
    row.append(
      node("span", "", tool.tool),
      node("span", "", rateLabel(tool.error_rate, tool.totals)),
    );
    host.append(row);
  }
  if (!(report.tools || []).length)
    host.append(node("p", "muted", "No tool calls."));
}

async function navigateEvidence(sourceLine, evidenceID) {
  const page = await request(
    `/api/sessions/${encodeURIComponent(state.session)}/stream?limit=50&anchor_line=${sourceLine}&anchor_id=${encodeURIComponent(evidenceID || "")}`,
    activeController?.signal,
  );
  renderStream(page);
  const match = [...document.querySelectorAll("#detail-stream .evidence")].find(
    (entry) =>
      Number(entry.dataset.sourceLine) === sourceLine &&
      (!evidenceID || entry.dataset.entryId === evidenceID),
  );
  const target = match || document.querySelector("#detail-stream .evidence");
  if (target) {
    target.setAttribute("tabindex", "-1");
    target.focus();
    target.scrollIntoView({ behavior: "smooth", block: "center" });
  }
  if (!match)
    announce(
      `Showing source line ${sourceLine}; the exact evidence ID is not a stream record.`,
      "warning",
    );
}

function appendFreshFindings(host, findings) {
  for (const finding of findings || []) {
    const sourceLine = finding.source_line ?? finding.SourceLine;
    const evidenceID = finding.evidence_id ?? finding.EvidenceID;
    const details = node("details");
    const provenance = node(
      "p",
      "",
      `${finding.summary ?? finding.Summary}\nEvidence ${evidenceID} · source line ${sourceLine}`,
    );
    const jump = node("button", "", "Navigate to evidence");
    jump.type = "button";
    jump.addEventListener("click", () =>
      navigateEvidence(sourceLine, evidenceID),
    );
    provenance.append(node("br"), jump);
    details.append(
      node(
        "summary",
        "",
        `${finding.detector ?? finding.Detector} · ${finding.severity ?? finding.Severity} · line ${sourceLine}`,
      ),
      provenance,
    );
    host.append(details);
  }
}
function appendStaleFindings(host, findings) {
  for (const finding of findings || []) {
    const details = node("details");
    details.append(
      node(
        "summary",
        "",
        `${finding.detector ?? finding.Detector} generation ${finding.generation ?? finding.Generation} · ${finding.run_status ?? finding.RunStatus ?? "retired"}`,
      ),
      node(
        "p",
        "",
        `${finding.summary ?? finding.Summary}\n${finding.run_error ?? finding.RunError ?? ""}`,
      ),
    );
    host.append(details);
  }
}
function renderFindings(data) {
  const host = $("#detail-findings");
  clear(host);
  if (data.content_truncated)
    host.append(
      node("p", "danger-text", "Some finding text reached its safety bound."),
    );
  host.append(node("p", "eyebrow", "DETECTOR COVERAGE"));
  for (const detector of data.detectors || []) {
    const row = node("div", "metric-row");
    row.append(
      node("span", "", detector.detector),
      tag(detector.status, detector.status),
    );
    host.append(row);
  }
  host.append(node("p", "eyebrow", "CURRENT FINDINGS"));
  const freshHost = node("div");
  freshHost.id = "fresh-finding-list";
  appendFreshFindings(freshHost, data.fresh_findings);
  host.append(freshHost);
  host.append(node("p", "eyebrow", "STALE RETAINED EVIDENCE — NOT CURRENT"));
  const staleHost = node("div");
  staleHost.id = "stale-finding-list";
  appendStaleFindings(staleHost, data.stale_evidence);
  host.append(staleHost);
  freshFindingOffset = data.fresh_offset + (data.fresh_findings || []).length;
  staleFindingOffset = data.stale_offset + (data.stale_evidence || []).length;
  $("#findings-more").hidden = !data.fresh_truncated;
  $("#stale-more").hidden = !data.stale_truncated;
}

function renderTokens(page, append = false) {
  const host = $("#detail-tokens");
  if (!append) clear(host);
  const maxTotal = Math.max(
    1,
    ...(page.entries || []).map((entry) =>
      entry.kind === "compaction" ? 0 : tokenRowTotal(entry),
    ),
  );
  for (const entry of page.entries || []) {
    const row = node("div", "token-mark");
    row.append(node("span", "", `L${entry.source_line} ${entry.kind}`));
    if (entry.kind === "compaction")
      row.append(
        node(
          "strong",
          "danger-text",
          `COMPACTION · ${formatInteger(entry.tokens_before)} tokens before`,
        ),
      );
    else if (tokenRowTotal(entry) === 0)
      row.append(node("span", "muted", "No reported usage"));
    else {
      const values = tokenValues(entry);
      const bars = node("div", "token-bars");
      const scale = node("div", "token-scale");
      scale.style.width = `${Math.max(2, (tokenRowTotal(entry) / maxTotal) * 100)}%`;
      for (const [kind, value] of values) {
        if (!value) continue;
        const bar = node("span", kind);
        bar.style.flexGrow = String(value);
        bar.title = `${kind}: ${formatInteger(value)}`;
        scale.append(bar);
      }
      bars.append(scale);
      bars.setAttribute("role", "img");
      bars.setAttribute(
        "aria-label",
        values.map(([kind, value]) => `${kind} ${value}`).join(", "),
      );
      row.append(bars);
    }
    host.append(row);
  }
  tokenCursor = page.next_cursor || "";
  $("#tokens-more").hidden = !tokenCursor;
}

function renderGoal(data, append = false) {
  const host = $("#detail-goal");
  if (!append) {
    clear(host);
    host.append(tag(`Final: ${data.final_state}`, data.final_state));
  }
  for (const item of data.snapshots || []) {
    const row = node("div", "metric-row");
    const stateTag = tag(item.state, item.state);
    if (item.content_truncated)
      stateTag.append(document.createTextNode(" · truncated"));
    row.append(node("span", "", `Line ${item.source_line}`), stateTag);
    host.append(row);
  }
  goalOffset = data.offset + (data.snapshots || []).length;
  $("#goal-more").hidden = !data.truncated;
}
function renderTodo(data, append = false) {
  const host = $("#detail-todo");
  if (!append) {
    clear(host);
    host.append(tag(`Final: ${data.final_state}`, data.final_state));
    if (data.data_quality_truncated || data.final_list_truncated)
      host.append(
        node(
          "p",
          "danger-text",
          "TODO history or final-list analysis is truncated.",
        ),
      );
  }
  for (const snapshot of data.snapshots || []) {
    const row = node("div", "metric-row");
    row.append(
      node("span", "", `Line ${snapshot.source_line}`),
      node(
        "span",
        "",
        snapshot.valid
          ? `${snapshot.counts.todo} todo / ${snapshot.counts.in_progress} active / ${snapshot.counts.done} done / ${snapshot.counts.blocked} blocked`
          : snapshot.error,
      ),
    );
    host.append(row);
  }
  for (const item of data.final_items || []) {
    const details = node("details");
    details.append(
      node(
        "summary",
        "",
        `${item.status} · item ${item.id}${item.content_truncated ? " · truncated" : ""}`,
      ),
      node("p", "", `${item.text}${item.notes ? `\n${item.notes}` : ""}`),
    );
    host.append(details);
  }
  todoSnapshotOffset = data.snapshot_offset + (data.snapshots || []).length;
  todoItemOffset = data.final_item_offset + (data.final_items || []).length;
  $("#todo-more").hidden = !(
    data.snapshots_truncated || data.final_items_truncated
  );
}

function renderStream(page, append = false) {
  const host = $("#detail-stream");
  if (!append) clear(host);
  for (const entry of page.entries || []) {
    const row = node("div", "evidence");
    row.dataset.sourceLine = String(entry.source_line);
    row.dataset.entryId = entry.id;
    row.append(
      node("span", "line", `L${entry.source_line}`),
      tag(entry.kind, entry.is_error ? "error" : "neutral"),
    );
    const details = node("details");
    const content = node("p", "", collapsedStreamText(entry));
    let loaded = false;
    details.append(
      node(
        "summary",
        "",
        [entry.role, entry.name, entry.type, entry.status]
          .filter(Boolean)
          .join(" · ") || entry.id,
      ),
      content,
    );
    details.addEventListener("toggle", async () => {
      if (!details.open || loaded) return;
      try {
        const value = await request(
          `/api/sessions/${encodeURIComponent(state.session)}/detail?kind=${encodeURIComponent(entry.kind)}&id=${encodeURIComponent(entry.id)}`,
          activeController?.signal,
        );
        content.textContent = [value.content, value.details]
          .filter(Boolean)
          .join("\n");
        if (value.content_truncated)
          content.append(
            node(
              "span",
              "danger-text",
              "\nDetail reached its per-entry safety limit.",
            ),
          );
        loaded = true;
      } catch (error) {
        content.textContent = error.message;
      }
    });
    row.append(details);
    host.append(row);
  }
  streamCursor = page.next_cursor || "";
  $("#stream-more").hidden = !streamCursor;
}

async function loadDetail(sessionID, signal) {
  $("#detail").hidden = false;
  const base = `/api/sessions/${encodeURIComponent(sessionID)}`;
  const header = await request(base, signal);
  renderHeader(header);
  const tools = await request(`${base}/tools`, signal);
  renderDetailTools(tools);
  const findings = await request(`${base}/diagnostics`, signal);
  renderFindings(findings);
  const tokens = await request(`${base}/tokens?limit=50`, signal);
  renderTokens(tokens);
  const goal = await request(`${base}/goal?limit=50`, signal);
  renderGoal(goal);
  const todo = await request(
    `${base}/todo?snapshot_limit=50&item_limit=20`,
    signal,
  );
  renderTodo(todo);
  const stream = await request(`${base}/stream?limit=50`, signal);
  renderStream(stream);
  $("#detail-title").focus?.();
}

async function loadMatrix(signal, append = false) {
  let query = matrixSearch(state, overview);
  if (append && matrixCursor)
    query += `&cursor=${encodeURIComponent(matrixCursor)}`;
  const page = await request(`/api/sessions?${query}`, signal);
  renderMatrix(page, append);
  if (
    !append &&
    page.rows?.length === 1 &&
    state.from &&
    state.to &&
    !state.session
  )
    navigate({ ...state, session: page.rows[0].id }, true);
}

async function loadDashboard() {
  activeController?.abort();
  activeController = new AbortController();
  const { signal } = activeController;
  announce("Reading the local scrubbed index…");
  try {
    const query = overviewSearch(state);
    overview = await request(`/api/overview?${query}`, signal);
    renderOverview(overview);
    const signals = await request(
      `/api/overview/signals?${query}`,
      signal,
    ).catch((error) => {
      if (error.name === "AbortError") throw error;
      return null;
    });
    renderTrends(
      overview.buckets || [],
      signals?.stop_reasons || [],
      signals?.bucket_signals || {},
      signals === null,
    );
    await loadMatrix(signal);
    if (state.session) await loadDetail(state.session, signal);
    else $("#detail").hidden = true;
    announce(
      `Index ready · indexed ${overview.indexed_at || "time unavailable"} · ${overview.buckets?.length || 0} ${overview.bucket} buckets · ${overview.timezone}. Run ingest to refresh.`,
    );
  } catch (error) {
    if (error.name !== "AbortError")
      announce(
        `${error.message}. Run ingest if the index is missing or stale, then refresh.`,
        "error",
      );
  }
}

$("#range").addEventListener("change", (event) =>
  navigate({
    ...state,
    range: event.target.value,
    dateFrom: "",
    dateTo: "",
    session: "",
    from: "",
    to: "",
    untimed: false,
  }),
);
$("#bucket").addEventListener("change", (event) =>
  navigate({
    ...state,
    bucket: event.target.value,
    session: "",
    from: "",
    to: "",
    untimed: false,
  }),
);
$("#direction").addEventListener("change", (event) =>
  navigate({ ...state, direction: event.target.value, session: "" }),
);
$("#cwd").addEventListener("change", (event) =>
  navigate({ ...state, cwd: event.target.value, session: "" }),
);
$("#untimed").addEventListener("change", (event) =>
  navigate({
    ...state,
    untimed: event.target.checked,
    session: "",
    from: "",
    to: "",
  }),
);
for (const id of ["date-from", "date-to"])
  $(`#${id}`).addEventListener("change", () => {
    const dateFrom = $("#date-from").value;
    const dateTo = $("#date-to").value;
    if (dateFrom && dateTo)
      navigate({
        ...state,
        dateFrom,
        dateTo,
        session: "",
        from: "",
        to: "",
        untimed: false,
      });
  });
$("#timezone").addEventListener("change", (event) =>
  navigate({
    ...state,
    timezone: event.target.value || "UTC",
    session: "",
    from: "",
    to: "",
  }),
);
$("#refresh").addEventListener("click", loadDashboard);
$("#matrix-more").addEventListener("click", () =>
  loadMatrix(activeController?.signal, true).catch((error) =>
    announce(error.message, "error"),
  ),
);
$("#stream-more").addEventListener("click", () =>
  runAction(async () => {
    const page = await request(
      `/api/sessions/${encodeURIComponent(state.session)}/stream?limit=50&cursor=${encodeURIComponent(streamCursor)}`,
      activeController?.signal,
    );
    renderStream(page, true);
  }),
);
$("#tokens-more").addEventListener("click", () =>
  runAction(async () => {
    const page = await request(
      `/api/sessions/${encodeURIComponent(state.session)}/tokens?limit=50&cursor=${encodeURIComponent(tokenCursor)}`,
      activeController?.signal,
    );
    renderTokens(page, true);
  }),
);
$("#goal-more").addEventListener("click", () =>
  runAction(async () => {
    const page = await request(
      `/api/sessions/${encodeURIComponent(state.session)}/goal?limit=50&offset=${goalOffset}`,
      activeController?.signal,
    );
    renderGoal(page, true);
  }),
);
$("#todo-more").addEventListener("click", () =>
  runAction(async () => {
    const page = await request(
      `/api/sessions/${encodeURIComponent(state.session)}/todo?snapshot_limit=50&item_limit=20&snapshot_offset=${todoSnapshotOffset}&item_offset=${todoItemOffset}`,
      activeController?.signal,
    );
    renderTodo(page, true);
  }),
);
$("#findings-more").addEventListener("click", () =>
  runAction(async () => {
    const data = await request(
      `/api/sessions/${encodeURIComponent(state.session)}/diagnostics?limit=25&fresh_offset=${freshFindingOffset}`,
      activeController?.signal,
    );
    appendFreshFindings($("#fresh-finding-list"), data.fresh_findings);
    freshFindingOffset += (data.fresh_findings || []).length;
    $("#findings-more").hidden = !data.fresh_truncated;
  }),
);
$("#stale-more").addEventListener("click", () =>
  runAction(async () => {
    const data = await request(
      `/api/sessions/${encodeURIComponent(state.session)}/diagnostics?limit=25&stale_offset=${staleFindingOffset}`,
      activeController?.signal,
    );
    appendStaleFindings($("#stale-finding-list"), data.stale_evidence);
    staleFindingOffset += (data.stale_evidence || []).length;
    $("#stale-more").hidden = !data.stale_truncated;
  }),
);
$("#close-detail").addEventListener("click", () =>
  navigate({ ...state, session: "" }),
);
$("#clear-bucket").addEventListener("click", () =>
  navigate({ ...state, from: "", to: "", session: "" }),
);
addEventListener("popstate", () => {
  state = parseState(location.search, browserTimezone);
  syncControls();
  loadDashboard();
});
syncControls();
loadDashboard();
