import type { OverviewSnapshot, ServerView } from "./overview";
import type { ViewSnapshot } from "./view";

export interface AttentionItem {
  id: string;
  label: string;
  explanation: string;
  href: string;
}

// These terminal faults require intervention. Connectivity, capacity and wire
// failures are retryable at the runtime owner, not operator work items.
export function serverAttention(server: ServerView): string | undefined {
  if (server.desired !== "enabled") return undefined;
  if (server.runtime === "authentication_required")
    return "Authentication required";
  if (server.runtime === "degraded") {
    if (server.reason === "configuration_invalid")
      return "Configuration invalid";
    if (server.reason === "protocol_unsupported") return "Protocol unsupported";
    if (server.reason === "stop_unconfirmed") return "Process stop unconfirmed";
  }
  if (["activating", "retry_wait", "stopping"].includes(server.runtime))
    return undefined;
  if (
    [
      "absent",
      "locked",
      "interaction_required",
      "unavailable",
      "unsupported",
      "reauthentication_required",
      "cleanup_pending",
    ].includes(server.credential)
  )
    return `Credentials ${server.credential.replaceAll("_", " ")}`;
  return undefined;
}

export function attentionItems(snapshot: OverviewSnapshot, view: ViewSnapshot) {
  const current = (source: string) =>
    view.panels[`overview-${source}`]?.status === "current";
  const items: AttentionItem[] = [];
  const status = current("status") ? snapshot.status : undefined;
  if (status?.latched)
    items.push({
      id: "control-storage",
      label: "Control storage recovery required",
      explanation:
        "Changes are blocked. Review the stopped-process recovery procedure in System status.",
      href: "#/system",
    });
  if (status?.traffic?.recovery?.health === "operator_action_required")
    items.push({
      id: "traffic-recording",
      label: "Traffic recording needs attention",
      explanation:
        "Recording requires operator action. Inspect traffic storage recovery in System status.",
      href: "#/system",
    });
  if (current("servers")) {
    const seen = new Set<string>();
    for (const server of [...(snapshot.servers?.items ?? [])].sort((a, b) =>
      a.id < b.id ? -1 : a.id > b.id ? 1 : 0,
    )) {
      const reason = serverAttention(server);
      if (!reason || seen.has(server.id)) continue;
      seen.add(server.id);
      items.push({
        id: server.id,
        label: server.name || `MCP server ${server.id}`,
        explanation: reason,
        href: `#/mcp/servers/${server.id}`,
      });
    }
  }
  if (current("requests") && snapshot.requests && snapshot.requests.total > 0)
    items.push({
      id: "pending-requests",
      label: `${snapshot.requests.total.toLocaleString()} pending MCP ${snapshot.requests.total === 1 ? "request" : "requests"}`,
      explanation: "Review pending access requests.",
      href: "#/mcp/access-requests",
    });
  const complete = !!(
    status?.traffic?.recovery &&
    current("servers") &&
    snapshot.servers?.complete &&
    current("requests") &&
    snapshot.requests
  );
  const sources = ["status", "servers", "requests"].map(
    (id) => view.panels[`overview-${id}`],
  );
  const notice = complete
    ? undefined
    : sources.some((source) => source?.status === "error")
      ? "Attention data unavailable; list may be incomplete."
      : sources.some((source) => source?.status === "stale")
        ? "Attention data stale; current list is unknown."
        : sources.some((source) => !source || source.status === "loading")
          ? "Loading attention data…"
          : "Attention data incomplete; more items may need attention.";
  return { items, complete, notice };
}
