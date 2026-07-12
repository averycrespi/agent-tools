export function unwrapResponse(value) {
  if (value?.truncated === true && Object.hasOwn(value, "value")) return { value: value.value, truncated: true };
  return { value, truncated: false };
}

export function tokenValues(entry) {
  return [
    ["input", entry.input_tokens || 0],
    ["output", entry.output_tokens || 0],
    ["reasoning", entry.reasoning_tokens || 0],
    ["cache-read", entry.cache_read_tokens || 0],
    ["cache-write", entry.cache_write_tokens || 0],
  ];
}

export function collapsedStreamText() {
  return "Open to request bounded detail from the local index.";
}

export function formatInteger(value) {
  return new Intl.NumberFormat("en-US", { notation: Math.abs(value || 0) >= 100000 ? "compact" : "standard", maximumFractionDigits: 1 }).format(value || 0);
}

export function formatCost(value) {
  return `$${Number(value || 0).toFixed(2)}`;
}

export function rateLabel(rate, totals) {
  const coverage = `${formatInteger(totals?.classifiable || 0)}/${formatInteger(totals?.calls || 0)} classifiable`;
  if (rate === null || rate === undefined) return `Unknown · ${coverage}`;
  return `${(rate * 100).toFixed(1)}% errors · ${coverage} · ${totals.confirmed_errors || 0} confirmed / ${totals.inferred_errors || 0} inferred`;
}

export function bucketLabel(bucket, timezone) {
  return `${bucket.key}${bucket.partial ? " (current partial bucket)" : ""}: ${bucket.sessions} sessions, ${formatCost(bucket.cost_as_logged)} logged cost, ${formatInteger(bucket.tool_calls)} tool calls; ${timezone}`;
}

export function severityLabel(value) {
  return value === "none" ? "No fresh finding" : `${value} fresh finding`;
}

export function statusClass(value) {
  if (["error", "failed", "malformed", "truncated"].includes(value)) return "danger";
  if (["warn", "unknown", "active", "blocked"].includes(value)) return "warning";
  if (["complete", "done", "success"].includes(value)) return "good";
  return "neutral";
}
