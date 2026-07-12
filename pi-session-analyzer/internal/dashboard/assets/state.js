const ranges = new Set(["7d", "30d", "90d", "all"]);
const buckets = new Set(["auto", "day", "week", "month"]);

export function defaultState(timezone = "UTC") {
  return { range: "30d", bucket: "auto", timezone, cwd: "", untimed: false, session: "", from: "", to: "", dateFrom: "", dateTo: "", direction: "desc" };
}

export function parseState(search, timezone = "UTC") {
  const state = defaultState(timezone);
  const params = new URLSearchParams(search);
  if (ranges.has(params.get("range"))) state.range = params.get("range");
  if (buckets.has(params.get("bucket"))) state.bucket = params.get("bucket");
  if (params.get("timezone")) state.timezone = params.get("timezone");
  state.cwd = params.get("cwd") || "";
  state.untimed = params.get("untimed") === "true";
  state.session = params.get("session") || "";
  if (params.get("direction") === "asc") state.direction = "asc";
  if (/^-?\d+$/.test(params.get("from") || "") && /^-?\d+$/.test(params.get("to") || "")) {
    state.from = params.get("from");
    state.to = params.get("to");
  }
  if (/^\d{4}-\d{2}-\d{2}$/.test(params.get("date_from") || "") && /^\d{4}-\d{2}-\d{2}$/.test(params.get("date_to") || "")) {
    state.dateFrom = params.get("date_from");
    state.dateTo = params.get("date_to");
  }
  return state;
}

export function stateSearch(state) {
  const params = new URLSearchParams();
  params.set("range", state.range);
  params.set("bucket", state.bucket);
  params.set("timezone", state.timezone);
  if (state.cwd) params.set("cwd", state.cwd);
  if (state.untimed) params.set("untimed", "true");
  if (state.session) params.set("session", state.session);
  if (state.direction === "asc") params.set("direction", "asc");
  if (state.from && state.to) {
    params.set("from", state.from);
    params.set("to", state.to);
  }
  if (state.dateFrom && state.dateTo) {
    params.set("date_from", state.dateFrom);
    params.set("date_to", state.dateTo);
  }
  return `?${params.toString()}`;
}

export function overviewSearch(state) {
  const params = new URLSearchParams({ timezone: state.timezone, range: state.range, bucket: state.bucket });
  if (state.dateFrom && state.dateTo) {
    params.set("from", state.dateFrom);
    params.set("to", state.dateTo);
  }
  return params.toString();
}

export function matrixSearch(state, overview) {
  const params = new URLSearchParams({ limit: "10", direction: state.direction });
  if (state.untimed) {
    params.set("untimed", "true");
  } else {
    const buckets = overview?.buckets || [];
    const from = state.from || String(buckets[0]?.start_unix ?? "");
    const to = state.to || String(buckets.at(-1)?.end_unix ?? "");
    if (from) params.set("from", from);
    if (to) params.set("to", to);
  }
  if (state.cwd) params.set("cwd", state.cwd);
  return params.toString();
}

export function withBucket(state, bucket) {
  return { ...state, untimed: false, session: "", from: String(bucket.start_unix), to: String(bucket.end_unix) };
}
