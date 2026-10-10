import {
  canonicalHost,
  canonicalTime,
  exact,
  idPattern,
  object,
  validateHTTPDecision,
  validatePolicy,
} from "./http-policy-response.ts";

import { validActivityRange } from "./protocol-summary.ts";
export const trafficOptions = {
  type: ["request", "connect", "invalid"],
  decision: ["allow", "block", "intercept", "invalid"],
  outcome: [
    "not_dispatched",
    "interception_selected",
    "outcome_unknown",
    "succeeded",
    "prestart_failure",
    "upstream_failure",
  ],
} as const;
export function validHTTPTrafficQuery(
  query: Readonly<Record<string, string>>,
): boolean {
  return (
    validActivityRange(query) &&
    Object.entries(query).every(([key, value]) => {
      if (key === "filter_from" || key === "filter_until") return true;
      if (key === "filter_principal_id" || key === "filter_connect_id")
        return idPattern.test(value);
      if (key === "filter_destination" || key === "filter_principal")
        return (
          new TextEncoder().encode(value).byteLength <= 256 &&
          !/[\p{Cc}\p{Cf}]/u.test(value)
        );
      const options =
        trafficOptions[key.slice(7) as keyof typeof trafficOptions];
      return (
        key.startsWith("filter_") &&
        options !== undefined &&
        (options as readonly string[]).includes(value)
      );
    })
  );
}
function id(value: unknown): string {
  if (typeof value !== "string" || !idPattern.test(value))
    throw new Error("Invalid HTTP traffic identity.");
  return value;
}
function time(value: unknown): string {
  if (typeof value !== "string" || canonicalTime(value) === undefined)
    throw new Error("Invalid HTTP traffic time.");
  return value;
}
function integer(value: unknown, minimum = 0): number {
  if (!Number.isSafeInteger(value) || Number(value) < minimum)
    throw new Error("Invalid HTTP traffic number.");
  return Number(value);
}
function closed(value: unknown, values: readonly string[]): string {
  if (typeof value !== "string" || !values.includes(value))
    throw new Error("Invalid HTTP traffic state.");
  return value;
}
function reference(value: unknown): { id: string; revision: number } {
  const r = exact(value, ["id", "revision"]);
  return { id: id(r.id), revision: integer(r.revision, 1) };
}
export interface TrafficTarget {
  host: string;
  port: number;
  scheme?: string;
  method?: string;
}
function target(value: unknown): TrafficTarget | null {
  if (value === null) return null;
  const t = object(value),
    request = Object.hasOwn(t, "scheme");
  exact(t, ["host", "port", ...(request ? ["scheme", "method"] : [])]);
  if (
    !canonicalHost(t.host) ||
    String(t.host).includes("*") ||
    integer(t.port, 1) > 65535
  )
    throw new Error("Invalid HTTP traffic destination.");
  if (
    request &&
    ((t.scheme !== "http" && t.scheme !== "https") ||
      typeof t.method !== "string" ||
      !/^[A-Z0-9!#$%&'*+.^_`|~-]{1,32}$/.test(t.method) ||
      t.method === "CONNECT")
  )
    throw new Error("Invalid HTTP request evidence.");
  return t as unknown as TrafficTarget;
}
export interface Rejection {
  stage: string;
  reason: string;
}
export interface ConnectContext {
  id: string;
  host: string;
  port: number;
}
const rejectionReasons: Record<string, Record<string, string>> = {
  headers: {
    invalid_headers: "Invalid headers",
    trailers_unsupported: "Trailers are not supported",
    upgrade_unsupported: "Protocol upgrades are not supported",
    inner_proxy_authorization: "Proxy credentials inside CONNECT",
  },
  request_form: {
    connect_body: "CONNECT must not carry a body",
    nested_connect: "Nested CONNECT is not supported",
    origin_form_required: "Origin-form request required inside CONNECT",
    absolute_http_required: "Absolute-form HTTP request required",
  },
  target: {
    invalid_request_target: "Request target failed validation",
    invalid_connect_target: "CONNECT target failed validation",
    invalid_target_syntax: "Invalid request target syntax",
    target_too_long: "Request target exceeds byte limit",
    forbidden_path: "Forbidden path construct",
    authority_mismatch: "Request authority mismatch",
  },
};
export function rejectionLabel(value: Rejection | undefined): string {
  return value === undefined
    ? "Rejection details unavailable"
    : (rejectionReasons[value.stage]?.[value.reason] ??
        "Rejection details unavailable");
}
function rejection(value: unknown): Rejection {
  const r = exact(value, ["stage", "reason"]);
  if (
    typeof r.stage !== "string" ||
    typeof r.reason !== "string" ||
    !Object.hasOwn(rejectionReasons, r.stage) ||
    !Object.hasOwn(rejectionReasons[r.stage]!, r.reason)
  )
    throw new Error("Invalid rejection evidence.");
  return r as unknown as Rejection;
}
function connect(
  value: unknown,
  ownID: string,
  t: TrafficTarget | null,
): ConnectContext {
  const c = exact(value, ["id", "host", "port"]);
  const inherited = target({ host: c.host, port: c.port })!;
  if (
    id(c.id) === ownID ||
    (t !== null &&
      (t.scheme !== "https" ||
        t.host !== inherited.host ||
        t.port !== inherited.port))
  )
    throw new Error("Invalid CONNECT context.");
  return c as unknown as ConnectContext;
}
function optionalKeys(value: unknown, keys: string[]): string[] {
  const v = object(value);
  return keys.filter((key) => Object.hasOwn(v, key));
}
export interface Termination {
  stage: string;
  condition: string;
  context?: string;
}
const terminationStages = [
  "exchange",
  "upstream_read",
  "downstream_write",
  "downstream_flush",
  "deadline",
  "response_headers",
  "complete",
];
function termination(value: unknown, outcome: unknown): Termination {
  const t = exact(value, [
    "stage",
    "condition",
    ...optionalKeys(value, ["context"]),
  ]);
  closed(t.stage, terminationStages);
  closed(t.condition, ["clean", "cancelled", "timeout", "failure"]);
  if (t.context !== undefined) closed(t.context, ["cancelled", "timeout"]);
  if (
    t.condition === "clean"
      ? outcome !== "succeeded" ||
        !["complete", "response_headers"].includes(String(t.stage)) ||
        t.context !== undefined
      : outcome !== "outcome_unknown" || t.stage === "complete"
  )
    throw new Error("Inconsistent HTTP termination evidence.");
  return t as unknown as Termination;
}
export function transferLabel(
  recorded: boolean | undefined,
  value?: Termination,
): string {
  if (recorded === false) return "Missing terminal evidence";
  if (value === undefined) return "Termination details unavailable";
  return value.condition === "clean"
    ? value.stage === "response_headers"
      ? "Headers prepared (HEAD)"
      : "Clean HTTP transfer"
    : "Incomplete HTTP transfer";
}
export interface TrafficSummary {
  id: string;
  admitted_at: string;
  principal_id: string;
  target: TrafficTarget | null;
  type: string;
  decision: string;
  outcome: string;
  rejection?: Rejection;
  connect?: ConnectContext;
  response_source?: string;
  completion_recorded?: boolean;
  termination?: Termination;
}
export interface TrafficPage {
  items: TrafficSummary[];
  nextCursor: string | null;
}
export function decodeTrafficPage(value: unknown): TrafficPage {
  const p = exact(value, ["items", "next_cursor"]);
  if (
    !Array.isArray(p.items) ||
    p.items.length > 100 ||
    (p.next_cursor !== null &&
      (typeof p.next_cursor !== "string" ||
        p.next_cursor.length > 512 ||
        !/^[A-Za-z0-9_-]+$/.test(p.next_cursor)))
  )
    throw new Error("Invalid HTTP traffic page.");
  const seen = new Set<string>();
  const items = p.items.map((value) => {
    const r = exact(value, [
      "id",
      "admitted_at",
      "principal_id",
      "target",
      "type",
      "decision",
      "outcome",
      ...optionalKeys(value, [
        "rejection",
        "connect",
        "response_source",
        "completion_recorded",
        "termination",
      ]),
    ]);
    const item: TrafficSummary = {
      id: id(r.id),
      admitted_at: time(r.admitted_at),
      principal_id: id(r.principal_id),
      target: target(r.target),
      type: closed(r.type, trafficOptions.type),
      decision: closed(r.decision, trafficOptions.decision),
      outcome: closed(r.outcome, trafficOptions.outcome),
    };
    if (
      item.type === "invalid"
        ? item.target !== null ||
          item.decision !== "invalid" ||
          item.outcome !== "not_dispatched"
        : item.target === null ||
          item.decision === "invalid" ||
          (item.type === "request") !== (item.target.scheme !== undefined) ||
          !validSummaryOutcome(item.decision, item.outcome) ||
          (item.type === "request" && item.decision === "intercept")
    )
      throw new Error("Inconsistent HTTP summary.");
    if (r.rejection !== undefined) {
      item.rejection = rejection(r.rejection);
      if (item.type !== "invalid") throw new Error("Invalid rejection class.");
    }
    if (r.connect !== undefined)
      item.connect = connect(r.connect, item.id, item.target);
    if (r.response_source !== undefined)
      item.response_source = closed(r.response_source, ["gateway", "upstream"]);
    if (item.rejection !== undefined && item.response_source !== "gateway")
      throw new Error("Invalid rejection source.");
    if (r.completion_recorded !== undefined) {
      if (
        typeof r.completion_recorded !== "boolean" ||
        (r.completion_recorded && item.decision !== "allow")
      )
        throw new Error("Invalid HTTP completion presence.");
      item.completion_recorded = r.completion_recorded;
    }
    if (r.termination !== undefined) {
      if (
        item.type !== "request" ||
        item.decision !== "allow" ||
        item.completion_recorded !== true
      )
        throw new Error("Invalid HTTP termination presence.");
      item.termination = termination(r.termination, item.outcome);
      const observed = item.termination;
      if (
        (observed.stage === "exchange"
          ? item.response_source !== "gateway"
          : item.response_source !== "upstream") ||
        (observed.stage === "response_headers" &&
          item.target?.method !== "HEAD") ||
        (observed.stage === "complete" && item.target?.method === "HEAD")
      )
        throw new Error("Invalid HTTP termination boundary.");
    }
    if (seen.has(item.id)) throw new Error("Duplicate HTTP traffic record.");
    seen.add(item.id);
    return item;
  });
  return { items, nextCursor: p.next_cursor as string | null };
}
function validSummaryOutcome(decision: string, outcome: string): boolean {
  if (decision === "intercept")
    return outcome === "interception_selected" || outcome === "not_dispatched";
  if (decision === "block") return outcome === "not_dispatched";
  return outcome !== "not_dispatched" && outcome !== "interception_selected";
}

// Older servers used not_dispatched for interception. The recorded decision,
// never the allowed bit alone, supplies the honest presentation in either era.
export function trafficOutcome(row: TrafficSummary): string {
  return row.decision === "intercept" ? "interception_selected" : row.outcome;
}
export function trafficDecisionLabel(decision: string, type: string): string {
  if (decision === "intercept") return "Interception selected";
  if (type === "connect" && decision === "allow")
    return "Opaque tunnel allowed";
  if (type === "connect" && decision === "block") return "CONNECT denied";
  return decision[0]!.toUpperCase() + decision.slice(1);
}
export const interceptionExplanation =
  "Inner requests are authorized separately. Selection does not prove CONNECT acceptance, TLS establishment, upstream dispatch, request completion or connection closure.";

export interface TrafficItem {
  admission: Record<string, unknown>;
  completion: Record<string, unknown> | null;
}
export function decodeTrafficItem(value: unknown): TrafficItem {
  const item = exact(value, ["admission", "completion"]);
  const a = exact(item.admission, [
    "id",
    "admitted_at",
    "principal",
    "agent_credential",
    "credential_fingerprint",
    "class",
    "default",
    "target",
    "evaluated_at",
    "decision",
    "grants",
    "material",
    ...optionalKeys(item.admission, ["rejection", "connect"]),
  ]);
  id(a.id);
  time(a.admitted_at);
  time(a.evaluated_at);
  const principal = reference(a.principal);
  reference(a.agent_credential);
  if (
    String(a.evaluated_at) < String(a.admitted_at) ||
    typeof a.credential_fingerprint !== "string" ||
    !/^[a-f0-9]{16}$/.test(a.credential_fingerprint) ||
    !Array.isArray(a.grants) ||
    a.grants.length > 4
  )
    throw new Error("Invalid HTTP evidence.");
  const t = target(a.target);
  if (a.rejection !== undefined) {
    rejection(a.rejection);
    if (a.class !== "invalid_request")
      throw new Error("Invalid rejection class.");
  }
  if (a.connect !== undefined) connect(a.connect, String(a.id), t);
  if (a.class === "invalid_request") {
    if (
      a.default !== "" ||
      t !== null ||
      a.decision !== null ||
      a.grants.length !== 0 ||
      a.material !== null ||
      item.completion !== null
    )
      throw new Error("Invalid unparsed evidence.");
  } else {
    if (
      a.class !== "evaluated" ||
      t === null ||
      !["allow", "block"].includes(String(a.default))
    )
      throw new Error("Invalid HTTP admission.");
    const d = validateHTTPDecision(
      a.decision,
      principal.id,
      t.scheme === undefined,
      a.default,
    );
    if (reference(d.principal).revision !== principal.revision)
      throw new Error("Invalid principal revision.");
    const expected = new Map<string, number>();
    for (const name of [
      "grant",
      "private_grant",
      "credential_grant",
      "conflict_grant",
    ])
      if (d[name] !== undefined) {
        const ref = reference(d[name]);
        if (expected.has(ref.id) && expected.get(ref.id) !== ref.revision)
          throw new Error("Conflicting grant revisions.");
        expected.set(ref.id, ref.revision);
      }
    if (expected.size !== a.grants.length)
      throw new Error("Invalid grant evidence.");
    let previous = "";
    for (const value of a.grants) {
      const g = exact(value, ["reference", "policy"]),
        ref = reference(g.reference);
      if (ref.id <= previous || expected.get(ref.id) !== ref.revision)
        throw new Error("Invalid grant reference.");
      previous = ref.id;
      validatePolicy(g.policy);
    }
    if (a.material !== null) {
      const m = exact(a.material, ["credential", "generation"]),
        ref = reference(m.credential),
        decided = reference(d.credential);
      if (
        !d.allowed ||
        ref.id !== decided.id ||
        ref.revision !== decided.revision ||
        typeof m.generation !== "string" ||
        !/^[1-9][0-9]{0,18}$/.test(m.generation)
      )
        throw new Error("Invalid material evidence.");
    }
    if (d.allowed && d.credential !== undefined && a.material === null)
      throw new Error("Missing material evidence.");
    if (item.completion !== null) {
      const c = object(item.completion);
      exact(c, [
        "completed_at",
        "outcome",
        "bytes_sent",
        "bytes_received",
        "duration_ms",
        ...optionalKeys(c, [
          "status",
          "response_source",
          "gateway_status",
          "termination",
        ]),
      ]);
      if (!d.allowed || time(c.completed_at) < String(a.evaluated_at))
        throw new Error("Invalid terminal evidence.");
      closed(c.outcome, [
        "outcome_unknown",
        "succeeded",
        "prestart_failure",
        "upstream_failure",
      ]);
      if (c.termination !== undefined) {
        const observed = termination(c.termination, c.outcome);
        if (
          d.transport !== "request" ||
          (observed.stage === "exchange"
            ? c.response_source !== "gateway"
            : c.response_source !== "upstream") ||
          (observed.stage === "response_headers" && t.method !== "HEAD") ||
          (observed.stage === "complete" && t.method === "HEAD")
        )
          throw new Error("Invalid HTTP termination boundary.");
      }
      integer(c.bytes_sent);
      integer(c.bytes_received);
      integer(c.duration_ms);
      if (
        c.status !== undefined &&
        (integer(c.status, 100) > 599 || d.transport !== "request")
      )
        throw new Error("Invalid HTTP status.");
      if (c.response_source !== undefined)
        closed(c.response_source, ["gateway", "upstream"]);
      if (
        c.gateway_status !== undefined &&
        (c.response_source !== "gateway" ||
          integer(c.gateway_status, 400) > 599)
      )
        throw new Error("Invalid Gateway response.");
      if (
        c.response_source === "gateway" &&
        (c.gateway_status === undefined || c.status !== undefined)
      )
        throw new Error("Invalid response source.");
      if (c.response_source === "upstream" && c.status === undefined)
        throw new Error("Missing upstream status.");
      if (
        c.outcome === "prestart_failure" &&
        (c.status !== undefined || c.bytes_sent !== 0 || c.bytes_received !== 0)
      )
        throw new Error("Invalid prestart evidence.");
    }
  }
  return {
    admission: a,
    completion: item.completion === null ? null : object(item.completion),
  };
}
export function destinationLabel(t: TrafficTarget | null): string {
  if (t === null) return "Not parsed";
  const host = t.host.includes(":") ? `[${t.host}]` : t.host;
  return t.scheme === undefined
    ? `CONNECT ${host}:${t.port}`
    : `${t.method} ${t.scheme}://${host}:${t.port}`;
}
