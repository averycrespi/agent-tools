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
  $("#order").value = `${state.sort}-${state.direction}`;
  $("#untimed").checked = state.untimed;
  $("#date-from").value = state.dateFrom;
  $("#date-to").value = state.dateTo;
  $("#clear-bucket").hidden = !(state.from && state.to);
  renderCwdOptions();
}

let cwdOptions = [];
let cwdOptionsNote = "";

function renderCwdOptions() {
  const selected = state.cwds || [];
  $("#cwd-summary").textContent = selected.length
    ? `${selected.length} selected`
    : "All projects";
  const host = $("#cwd-options");
  clear(host);
  const known = new Set(cwdOptions.map((option) => option.cwd));
  const entries = [
    ...selected
      .filter((cwd) => !known.has(cwd))
      .map((cwd) => ({ cwd, sessions: null })),
    ...cwdOptions,
  ];
  if (cwdOptionsNote) host.append(node("p", "muted", cwdOptionsNote));
  if (!entries.length) {
    host.append(node("p", "muted", "No project directories indexed."));
    return;
  }
  for (const entry of entries) {
    const label = node("label", "dropdown-item");
    const checkbox = node("input");
    checkbox.type = "checkbox";
    checkbox.value = entry.cwd;
    checkbox.checked = selected.includes(entry.cwd);
    checkbox.addEventListener("change", () => {
      const next = checkbox.checked
        ? [...selected, entry.cwd]
        : selected.filter((cwd) => cwd !== entry.cwd);
      navigate({ ...state, cwds: next.slice(0, 16), session: "" });
    });
    label.append(
      checkbox,
      node(
        "span",
        "",
        entry.sessions === null
          ? entry.cwd
          : `${entry.cwd} (${formatInteger(entry.sessions)})`,
      ),
    );
    host.append(label);
  }
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

function helpWidget(text, label) {
  const details = node("details", "help");
  const summary = node("summary", "", "?");
  summary.setAttribute("aria-label", label);
  details.append(summary, node("p", "help-pop", text));
  return details;
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

function tableFrom(headers, rows) {
  const scroll = node("div", "table-scroll");
  const table = node("table", "panel-table");
  const head = node("thead");
  const headRow = node("tr");
  for (const header of headers) headRow.append(node("th", "", header));
  head.append(headRow);
  const body = node("tbody");
  for (const cells of rows) {
    const tr = node("tr");
    for (const cell of cells) tr.append(node("td", "", String(cell)));
    body.append(tr);
  }
  table.append(head, body);
  scroll.append(table);
  return scroll;
}

function barList(host, title, items, getLabel, getCount) {
  if (title) host.append(node("p", "eyebrow", title));
  if (!items.length) {
    host.append(node("p", "muted", "No data in range."));
    return;
  }
  const list = node("div", "bar-list");
  const max = Math.max(1, ...items.map(getCount));
  for (const item of items) {
    const row = node("div", "bar-row");
    const track = node("div", "bar-track");
    const fill = node("div", "bar-fill");
    fill.style.width = `${(getCount(item) / max) * 100}%`;
    track.append(fill);
    row.append(
      node("span", "bar-label", getLabel(item)),
      track,
      node("span", "bar-count", formatInteger(getCount(item))),
    );
    list.append(row);
  }
  host.append(list);
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
    kpi("Cost", formatCost(totals.cost), "As logged by Pi"),
    kpi("Tool calls", formatInteger(totals.calls)),
    kpi("Compactions", formatInteger(totals.compact)),
    kpi("Broker guards", formatInteger(totals.guards)),
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
  renderSkillOverview(data.skills || {});
}

function renderSkillOverview(skills) {
  const host = $("#skill-overview");
  clear(host);
  const rows = skills.rows || [];
  host.append(
    metricList([
      ["Distinct skills", formatInteger(skills.distinct_skills || 0)],
      ["Invocations", formatInteger(skills.invocations || 0)],
      [
        "Sessions using ≥1 skill",
        formatInteger(skills.sessions_with_skills || 0),
      ],
    ]),
  );
  if (skills.truncated)
    host.append(
      node(
        "p",
        "danger-text",
        `Only the ${rows.length} most-invoked skills are listed.`,
      ),
    );
  if (skills.content_truncated)
    host.append(
      node("p", "danger-text", "Some skill names reached their bound."),
    );
  if (!rows.length) {
    host.append(node("p", "muted", "No skill reads in range."));
    return;
  }
  barList(
    host,
    "INVOCATIONS BY SKILL",
    rows,
    (row) => row.skill,
    (row) => row.invocations,
  );
  const details = node("details");
  details.append(
    node(
      "summary",
      "trend-summary",
      "Accessible skill table with sessions, recency, and paths",
    ),
    tableFrom(
      ["Skill", "Invocations", "Sessions", "Last used", "SKILL.md path"],
      rows.map((row) => [
        row.skill,
        formatInteger(row.invocations),
        formatInteger(row.sessions),
        localDate(row.last_used_unix),
        row.target,
      ]),
    ),
  );
  host.append(details);
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
    const select = () => {
      navigate(withBucket(state, bucket));
      $("#matrix").scrollIntoView({ behavior: "smooth" });
    };
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
    select.addEventListener("click", () => {
      navigate(withBucket(state, bucket));
      $("#matrix").scrollIntoView({ behavior: "smooth" });
    });
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
    [
      "Cost as logged",
      value("cost_as_logged"),
      formatCost,
      "Cost recorded by Pi for sessions started in each bucket, summed from per-message usage.",
    ],
    [
      "Tool-call volume",
      value("tool_calls"),
      formatInteger,
      "Tool calls issued by sessions started in each bucket.",
    ],
    [
      "Skill invocations",
      value("skill_invocations"),
      formatInteger,
      "Read tool calls on SKILL.md instruction files by sessions started in each bucket.",
    ],
    [
      "Output tokens",
      value("output_tokens"),
      formatInteger,
      "Visible completion tokens generated by each bucket's sessions.",
    ],
    [
      "Reasoning tokens",
      value("reasoning_tokens"),
      formatInteger,
      "Model reasoning tokens, reported separately from visible output.",
    ],
    [
      "Cache-read tokens",
      value("cache_read_tokens"),
      formatInteger,
      "Prompt tokens served from the provider's cache instead of being reprocessed.",
    ],
    [
      "Cache-write tokens",
      value("cache_write_tokens"),
      formatInteger,
      "Prompt tokens written into the provider's cache. Providers that do not bill cache creation separately (for example OpenAI) always report zero here.",
    ],
    [
      "Compactions",
      value("compactions"),
      formatInteger,
      "Context compaction events recorded in each bucket's sessions.",
    ],
    [
      "Broker guards",
      value("broker_guards"),
      formatInteger,
      "Tool calls the MCP broker blocked or altered.",
    ],
    [
      "Fresh error findings",
      (_bucket, index) => signals.fresh_error?.[index] || 0,
      formatInteger,
      "Current detector findings with error severity across each bucket's sessions.",
    ],
    [
      "Fresh warning findings",
      (_bucket, index) => signals.fresh_warn?.[index] || 0,
      formatInteger,
      "Current detector findings with warning severity across each bucket's sessions.",
    ],
    [
      "Fresh info findings",
      (_bucket, index) => signals.fresh_info?.[index] || 0,
      formatInteger,
      "Current detector findings with info severity across each bucket's sessions.",
    ],
    [
      "Fresh structural findings",
      (_bucket, index) => signals.fresh_structural?.[index] || 0,
      formatInteger,
      "Current findings backed by exact record evidence rather than pattern matching.",
    ],
    [
      "Fresh heuristic findings",
      (_bucket, index) => signals.fresh_heuristic?.[index] || 0,
      formatInteger,
      "Current findings from pattern-based detectors; treat these as leads, not proof.",
    ],
    [
      "Detector coverage gaps",
      (_bucket, index) =>
        (signals.detector_failed?.[index] || 0) +
        (signals.detector_not_run?.[index] || 0),
      formatInteger,
      "Detector runs that failed or have not run across each bucket's sessions.",
    ],
    [
      "Active goals",
      (_bucket, index) => signals.goal_active?.[index] || 0,
      formatInteger,
      "Sessions whose final goal state is still active — the goal was never marked complete.",
    ],
    [
      "Completed goals",
      (_bucket, index) => signals.goal_complete?.[index] || 0,
      formatInteger,
      "Sessions whose final goal state is complete.",
    ],
  ];
  for (const series of stopReasons)
    metrics.push([
      `Stop: ${series.value}`,
      (_bucket, index) => series.counts[index] || 0,
      formatInteger,
      `Sessions in each bucket whose final assistant message stopped with reason "${series.value}".`,
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
  for (const [label, getValue, format, description] of active) {
    const panel = node("section", "panel");
    const head = node("div", "panel-head");
    head.append(
      node("h3", "", label),
      helpWidget(description, `Explain the ${label} trend`),
    );
    panel.append(head);
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

function toolOutcomeTable(tools) {
  return tableFrom(
    ["Tool", "Error rate", "Confirmed / inferred", "Classifiable / calls"],
    tools.map((tool) => [
      tool.tool,
      tool.error_rate === null || tool.error_rate === undefined
        ? "Unknown"
        : `${(tool.error_rate * 100).toFixed(1)}%`,
      `${tool.totals.confirmed_errors} / ${tool.totals.inferred_errors}`,
      `${formatInteger(tool.totals.classifiable)} / ${formatInteger(tool.totals.calls)}`,
    ]),
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
        `${formatInteger(report.total_calls)} calls exceed the analysis bound; rates are unavailable.`,
      ),
    );
  host.append(
    metricList([
      ["All calls", formatInteger(report.total_calls || 0)],
      ["Classifiable", formatInteger(report.totals?.classifiable || 0)],
      ["Unknown", formatInteger(report.totals?.unknown || 0)],
      [
        "Orphan results",
        formatInteger(report.data_quality?.orphan_results || 0),
      ],
      [
        "Multiple results",
        formatInteger(report.data_quality?.multiple_results || 0),
      ],
    ]),
  );
  if ((report.tools || []).length) host.append(toolOutcomeTable(report.tools));
}

function renderDetectorOverview(detectors) {
  const host = $("#detector-overview");
  clear(host);
  if (!detectors.length) {
    host.append(node("p", "muted", "No detector registry metadata returned."));
    return;
  }
  host.append(
    tableFrom(
      [
        "Detector",
        "Fresh E / W / I",
        "Structural / heuristic",
        "OK / failed / not run",
      ],
      detectors.map((detector) => [
        detector.detector,
        `${detector.fresh.error} / ${detector.fresh.warn} / ${detector.fresh.info}`,
        `${detector.fresh.structural} / ${detector.fresh.heuristic}`,
        `${detector.coverage.success} / ${detector.coverage.failed} / ${detector.coverage.not_run}`,
      ]),
    ),
  );
}

function renderOutcomeOverview(signals) {
  const host = $("#outcome-overview");
  clear(host);
  barList(
    host,
    "GOAL OUTCOME",
    signals.goals || [],
    (item) => item.value,
    (item) => item.count,
  );
  barList(
    host,
    "FINAL STOP REASON",
    signals.stops || [],
    (item) => item.value,
    (item) => item.count,
  );
}

function renderDistributions(signals) {
  const host = $("#distribution-overview");
  clear(host);
  barList(
    host,
    "RECORDS PER SESSION",
    signals.records || [],
    (item) => item.label,
    (item) => item.count,
  );
  barList(
    host,
    "MESSAGE TURNS PER SESSION",
    signals.turns || [],
    (item) => item.label,
    (item) => item.count,
  );
}

function renderMatrix(page, append = false) {
  const tbody = $("#matrix-table tbody");
  if (!append) clear(tbody);
  for (const row of page.rows || []) {
    const tr = node("tr");
    const link = node("a", "matrix-session", row.id.slice(0, 12));
    link.href = stateSearch({ ...state, session: row.id });
    link.target = "_blank";
    link.rel = "noopener";
    link.append(node("small", "", "Open drilldown in a new tab"));
    const session = node("td");
    session.append(link);
    const start = node("td");
    start.append(
      node("span", "", localDate(row.started_at_unix)),
      node("small", "muted", ` ${row.cwd || "No cwd"}`),
    );
    const splitTokens = `O ${formatInteger(row.output_tokens)} / R ${formatInteger(row.reasoning_tokens)} / CR ${formatInteger(row.cache_read_tokens)} / CW ${formatInteger(row.cache_write_tokens)}`;
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
      node("td", "", formatCost(row.cost_as_logged)),
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
    td.colSpan = 10;
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
    kpi("Cost", formatCost(header.cost_as_logged), "As logged by Pi"),
    kpi("Output", formatInteger(header.output_tokens)),
    kpi("Reasoning", formatInteger(header.reasoning_tokens)),
    kpi("Cache read", formatInteger(header.cache_read_tokens)),
    kpi("Cache write", formatInteger(header.cache_write_tokens)),
    kpi("Compactions", formatInteger(header.compactions)),
    kpi("Broker guards", formatInteger(header.broker_guards)),
    kpi("Goal", header.goal_outcome),
    kpi("Stop reason", header.stop_reason || "absent"),
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
  if ((report.tools || []).length) host.append(toolOutcomeTable(report.tools));
  else host.append(node("p", "muted", "No tool calls."));
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
  const [header, tools, findings, tokens, goal, todo, stream] =
    await Promise.all([
      request(base, signal),
      request(`${base}/tools`, signal),
      request(`${base}/diagnostics`, signal),
      request(`${base}/tokens?limit=50`, signal),
      request(`${base}/goal?limit=50`, signal),
      request(`${base}/todo?snapshot_limit=50&item_limit=20`, signal),
      request(`${base}/stream?limit=50`, signal),
    ]);
  renderHeader(header);
  renderDetailTools(tools);
  renderFindings(findings);
  renderTokens(tokens);
  renderGoal(goal);
  renderTodo(todo);
  renderStream(stream);
  $("#detail-title").focus?.();
}

async function loadMatrix(signal, append = false) {
  let query = matrixSearch(state, overview);
  if (append && matrixCursor)
    query += `&cursor=${encodeURIComponent(matrixCursor)}`;
  const page = await request(`/api/sessions?${query}`, signal);
  renderMatrix(page, append);
  const note = $("#matrix-note");
  if (state.from && state.to) {
    note.textContent = `Narrowed to the selected chart bucket starting ${localDate(Number(state.from))}.`;
    note.hidden = false;
  } else {
    note.hidden = true;
  }
}

async function loadDashboard() {
  activeController?.abort();
  activeController = new AbortController();
  const { signal } = activeController;
  const drilldown = Boolean(state.session);
  document.body.classList.toggle("drilldown", drilldown);
  $("#overview").hidden = drilldown;
  $("#matrix").hidden = drilldown;
  $("#detail").hidden = !drilldown;
  announce("Reading the local scrubbed index…");
  try {
    if (drilldown) {
      await loadDetail(state.session, signal);
      announce(
        `Session ${state.session.slice(0, 12)} loaded from the local scrubbed index. Times shown in ${state.timezone}.`,
      );
      return;
    }
    const query = overviewSearch(state);
    overview = await request(`/api/overview?${query}`, signal);
    renderOverview(overview);
    request("/api/cwds", signal)
      .then((options) => {
        cwdOptions = options?.values || [];
        cwdOptionsNote = options?.truncated
          ? "Only the 100 busiest project directories are listed."
          : "";
        renderCwdOptions();
      })
      .catch(() => {});
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
$("#order").addEventListener("change", (event) => {
  const [sort, direction] = event.target.value.split("-");
  navigate({ ...state, sort, direction, session: "" });
});
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
addEventListener(
  "toggle",
  (event) => {
    const details = event.target;
    if (!(details instanceof HTMLElement)) return;
    if (!details.classList.contains("help") || !details.open) return;
    const pop = details.querySelector(".help-pop");
    if (!pop) return;
    pop.classList.remove("flip");
    if (pop.getBoundingClientRect().right > innerWidth - 8)
      pop.classList.add("flip");
  },
  true,
);
addEventListener("popstate", () => {
  state = parseState(location.search, browserTimezone);
  syncControls();
  loadDashboard();
});
syncControls();
loadDashboard();
