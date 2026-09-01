export const MAX_FRAGMENT_BYTES = 2048;

export type Destination =
  | "overview"
  | "servers"
  | "catalog"
  | "access"
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
  "tools",
  "activity",
  "authentication",
  "settings",
  "diagnostics",
]);
const systemTabs = new Set([
  "status",
  "admin-credentials",
  "backups",
  "recovery",
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
    const key = member.slice(0, separator);
    const value = member.slice(separator + 1);
    if (value === "" || Object.hasOwn(result, key)) return undefined;
    result[key] = value;
  }
  return result;
}

function exactQuery(
  query: Record<string, string>,
  validators: Readonly<Record<string, (value: string) => boolean>>,
): boolean {
  for (const [key, value] of Object.entries(query)) {
    const validate = validators[key];
    if (validate === undefined || !validate(value)) return false;
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
    !raw.startsWith("#/") ||
    raw.includes("%")
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
  const rawQuery = question === -1 ? "" : raw.slice(question + 1);
  if (
    (question !== -1 && rawQuery === "") ||
    path === "" ||
    path.endsWith("/") ||
    path.split("/").some((part) => part === "")
  ) {
    return undefined;
  }
  const segments = path.split("/");
  const query = parseQuery(rawQuery);
  if (query === undefined) return undefined;
  const noQuery = Object.keys(query).length === 0;
  const [first, second, third, fourth] = segments;

  if (segments.length === 1 && noQuery) {
    if (first === "overview" || first === "catalog" || first === "sign-in") {
      return location(first, segments, query);
    }
    if (first === "servers") return location("servers", segments, query);
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
  if (first === "access" && second === "principals") {
    if (segments.length === 2 && noQuery)
      return location("access", segments, query);
    if (segments.length === 3 && third === "new" && noQuery) {
      return location("access", segments, query);
    }
    if (
      segments.length === 3 &&
      third !== undefined &&
      isGatewayID(third) &&
      noQuery
    ) {
      return location("access", segments, query);
    }
  }
  if (first === "access" && second === "grants") {
    if (
      segments.length === 2 &&
      exactQuery(query, { principal_id: isGatewayID, server_id: isGatewayID })
    ) {
      return location("access", segments, query);
    }
    if (
      segments.length === 3 &&
      third === "new" &&
      exactQuery(query, { principal_id: isGatewayID, server_id: isGatewayID })
    ) {
      return location("access", segments, query);
    }
    if (
      segments.length === 3 &&
      third !== undefined &&
      isGatewayID(third) &&
      noQuery
    ) {
      return location("access", segments, query);
    }
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
  if (
    first === "system" &&
    segments.length === 1 &&
    exactQuery(query, { tab: (value) => systemTabs.has(value) })
  ) {
    return location("system", segments, query);
  }
  return undefined;
}

const queryOrder: Readonly<Record<string, readonly string[]>> = {
  "access/grants": ["principal_id", "server_id"],
  "access/grants/new": ["principal_id", "server_id"],
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
  const keys = queryOrder[path] ?? (Object.hasOwn(query, "tab") ? ["tab"] : []);
  const members = keys
    .filter((key) => Object.hasOwn(query, key))
    .map((key) => `${key}=${query[key]}`);
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
