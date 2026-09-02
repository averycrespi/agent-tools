export const MAX_FRAGMENT_BYTES = 2048;

export type Destination =
  | "overview"
  | "servers"
  | "catalog"
  | "principals"
  | "grants"
  | "requests"
  | "invocations"
  | "system"
  | "sign-in";

export interface ApplicationLocation {
  destination: Destination;
  segments: readonly string[];
  query: Readonly<Record<string, string>>;
}

export interface ResolvedLocation {
  location: ApplicationLocation;
  canonicalFragment: string;
  invalid: boolean;
}

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const serverTabs = new Set([
  "status",
  "tools",
  "activity",
  "authentication",
  "settings",
]);
const systemTabs = new Set([
  "status",
  "resource-limits",
  "admin-credentials",
  "backups",
]);
const requestStates = new Set(["pending", "approved", "rejected", "cancelled"]);
const admissionClasses = new Set([
  "invalid_params",
  "unknown_tool",
  "invalid_arguments",
  "authorization_unavailable",
  "evaluated",
]);
const decisions = new Set(["allow", "deny", "block"]);
const outcomes = new Set([
  "invalid_params",
  "unknown_tool",
  "invalid_arguments",
  "authorization_unavailable",
  "deny",
  "block",
  "prestart_failure",
  "succeeded",
  "downstream_failure",
  "outcome_unknown",
]);

function isGatewayID(value: string): boolean {
  return gatewayID.test(value);
}

function parseQuery(raw: string): Record<string, string> | undefined {
  if (raw === "") return {};
  const result: Record<string, string> = {};
  for (const member of raw.split("&")) {
    if (member === "" || member.indexOf("=") <= 0) return undefined;
    const separator = member.indexOf("=");
    if (separator !== member.lastIndexOf("=")) return undefined;
    let key: string;
    let value: string;
    try {
      key = decodeURIComponent(member.slice(0, separator));
      value = decodeURIComponent(member.slice(separator + 1));
    } catch {
      return undefined;
    }
    if (value === "" || Object.hasOwn(result, key)) return undefined;
    result[key] = value;
  }
  return result;
}

function isCollectionFilter(key: string, value: string): boolean {
  return (
    /^filter_[a-z][a-z0-9_-]*$/.test(key) &&
    new TextEncoder().encode(value).byteLength <= 256 &&
    !/[\p{Cc}\p{Cf}]/u.test(value)
  );
}

function exactQuery(
  query: Record<string, string>,
  validators: Readonly<Record<string, (value: string) => boolean>>,
): boolean {
  for (const [key, value] of Object.entries(query)) {
    const validate = validators[key];
    if (validate === undefined) {
      if (!isCollectionFilter(key, value)) return false;
      continue;
    }
    if (!validate(value)) return false;
  }
  return true;
}

function location(
  destination: Destination,
  segments: readonly string[],
  query: Record<string, string>,
): ApplicationLocation {
  return { destination, segments, query };
}

export function parseFragment(raw: string): ApplicationLocation | undefined {
  if (
    raw.length < 3 ||
    raw.length > MAX_FRAGMENT_BYTES ||
    !raw.startsWith("#/")
  ) {
    return undefined;
  }
  for (let index = 0; index < raw.length; index += 1) {
    const character = raw.charCodeAt(index);
    if (character < 0x20 || character > 0x7e) return undefined;
  }
  const question = raw.indexOf("?");
  if (question !== -1 && question !== raw.lastIndexOf("?")) return undefined;
  const path = raw.slice(2, question === -1 ? undefined : question);
  if (path.includes("%")) return undefined;
  const rawQuery = question === -1 ? "" : raw.slice(question + 1);
  if (
    (question !== -1 && rawQuery === "") ||
    path === "" ||
    path.endsWith("/") ||
    path.split("/").some((part) => part === "")
  ) {
    return undefined;
  }
  let segments = path.split("/");
  const query = parseQuery(rawQuery);
  if (query === undefined) return undefined;
  if (
    segments[0] === "access" &&
    (segments[1] === "principals" || segments[1] === "grants")
  )
    segments = segments.slice(1);
  const noQuery = Object.keys(query).length === 0;
  const [first, second, third, fourth] = segments;

  if (segments.length === 1) {
    if (first === "overview" || first === "sign-in") {
      if (noQuery) return location(first, segments, query);
    }
    if (first === "catalog" && exactQuery(query, {}))
      return location(first, segments, query);
    if (first === "servers" && exactQuery(query, {}))
      return location("servers", segments, query);
  }
  if (first === "servers") {
    if (segments.length === 2 && second === "new" && noQuery) {
      return location("servers", segments, query);
    }
    if (segments.length === 2 && second !== undefined && isGatewayID(second)) {
      if (exactQuery(query, { tab: (value) => serverTabs.has(value) })) {
        return location("servers", segments, query);
      }
    }
    if (
      segments.length === 4 &&
      second !== undefined &&
      isGatewayID(second) &&
      fourth !== undefined &&
      isGatewayID(fourth) &&
      noQuery &&
      (third === "operations" ||
        third === "auth-flows" ||
        third === "descriptors")
    ) {
      return location("servers", segments, query);
    }
  }
  if (first === "principals") {
    if (segments.length === 1 && exactQuery(query, {}))
      return location("principals", segments, query);
    if (segments.length === 2 && second === "new" && noQuery)
      return location("principals", segments, query);
    if (
      segments.length === 2 &&
      second !== undefined &&
      isGatewayID(second) &&
      noQuery
    )
      return location("principals", segments, query);
  }
  if (first === "grants") {
    if (
      segments.length === 1 &&
      exactQuery(query, { principal_id: isGatewayID, server_id: isGatewayID })
    )
      return location("grants", segments, query);
    if (
      segments.length === 2 &&
      second === "new" &&
      exactQuery(query, { principal_id: isGatewayID, server_id: isGatewayID })
    )
      return location("grants", segments, query);
    if (
      segments.length === 2 &&
      second !== undefined &&
      isGatewayID(second) &&
      noQuery
    )
      return location("grants", segments, query);
  }
  if (first === "requests") {
    if (
      segments.length === 1 &&
      exactQuery(query, {
        principal_id: isGatewayID,
        state: (value) => requestStates.has(value),
      })
    ) {
      return location("requests", segments, query);
    }
    if (
      segments.length === 2 &&
      second !== undefined &&
      isGatewayID(second) &&
      noQuery
    ) {
      return location("requests", segments, query);
    }
  }
  if (first === "invocations") {
    if (
      segments.length === 1 &&
      exactQuery(query, {
        principal_id: isGatewayID,
        server_id: isGatewayID,
        admission_class: (value) => admissionClasses.has(value),
        decision: (value) => decisions.has(value),
        outcome: (value) => outcomes.has(value),
      })
    ) {
      return location("invocations", segments, query);
    }
    if (
      segments.length === 2 &&
      second !== undefined &&
      isGatewayID(second) &&
      noQuery
    ) {
      return location("invocations", segments, query);
    }
  }
  if (first === "system") {
    if (
      segments.length === 1 &&
      exactQuery(query, { tab: (value) => systemTabs.has(value) })
    )
      return location("system", segments, query);
    if (
      segments.length === 3 &&
      (second === "backups" || second === "admin-credentials") &&
      segments[2] === "new" &&
      noQuery
    )
      return location("system", segments, query);
  }
  return undefined;
}

const queryOrder: Readonly<Record<string, readonly string[]>> = {
  grants: ["principal_id", "server_id"],
  "grants/new": ["principal_id", "server_id"],
  requests: ["principal_id", "state"],
  invocations: [
    "principal_id",
    "server_id",
    "admission_class",
    "decision",
    "outcome",
  ],
};

export function serializeLocation(value: ApplicationLocation): string {
  const path = value.segments.join("/");
  const query = { ...value.query };
  if (path.startsWith("servers/") && query.tab === "overview") delete query.tab;
  if (path === "system" && query.tab === "status") delete query.tab;
  const fixedKeys =
    queryOrder[path] ?? (Object.hasOwn(query, "tab") ? ["tab"] : []);
  const keys = [
    ...fixedKeys,
    ...Object.keys(query)
      .filter((key) => key.startsWith("filter_") && !fixedKeys.includes(key))
      .sort(),
  ];
  const members = keys
    .filter((key) => Object.hasOwn(query, key))
    .map(
      (key) => `${encodeURIComponent(key)}=${encodeURIComponent(query[key]!)}`,
    );
  return `#/${path}${members.length === 0 ? "" : `?${members.join("&")}`}`;
}

export function resolveFragment(
  raw: string,
  authenticated: boolean,
): ResolvedLocation {
  const parsed = parseFragment(raw);
  if (parsed !== undefined) {
    return {
      location: parsed,
      canonicalFragment: serializeLocation(parsed),
      invalid: false,
    };
  }
  const canonicalFragment = authenticated ? "#/overview" : "#/sign-in";
  const fallback = parseFragment(canonicalFragment);
  if (fallback === undefined) throw new Error("fixed location is invalid");
  return { location: fallback, canonicalFragment, invalid: true };
}

export function replaceForLifecycle(authenticated: boolean): void {
  const canonicalFragment = authenticated ? "#/overview" : "#/sign-in";
  window.history.replaceState(null, "", canonicalFragment);
}

export function synchronizeFragment(authenticated: boolean): ResolvedLocation {
  const resolved = resolveFragment(window.location.hash, authenticated);
  if (window.location.hash !== resolved.canonicalFragment) {
    window.history.replaceState(null, "", resolved.canonicalFragment);
  }
  return resolved;
}
