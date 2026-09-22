// Public response grammar only: no policy evaluation, resolution or authority.
export type Kind =
  | "block_destination"
  | "allow_tunnel"
  | "block_requests"
  | "allow_requests";
export interface Destination {
  host: string;
  port: number;
}
export interface Policy {
  version: 1;
  type: Kind;
  destination?: Destination;
  request?: {
    origin: Destination & { scheme: "http" | "https" };
    methods: { any?: true; values?: string[] };
    path: { kind: "any" | "exact" | "segment_prefix"; value?: string };
  };
  allow_private?: boolean;
  credential_id?: string;
}
export interface Grant {
  id: string;
  principal_id: string;
  description: string | null;
  revision: string;
  policy: Policy;
  expires_at: string | null;
  state: "active" | "expired";
  created_at: string;
  updated_at: string;
}
export interface GrantRow {
  grant: Grant;
  principal_display_name: string;
}
export const idPattern = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
export function object(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value))
    throw new Error("HTTP policy data unavailable.");
  return value as Record<string, unknown>;
}
export function exact(value: unknown, keys: string[]): Record<string, unknown> {
  const result = object(value);
  if (Object.keys(result).sort().join() !== keys.sort().join())
    throw new Error("HTTP policy data unavailable.");
  return result;
}
function revision(value: unknown): boolean {
  return (
    typeof value === "string" &&
    /^[1-9][0-9]{0,18}$/.test(value) &&
    (value.length < 19 || value <= "9223372036854775807")
  );
}
// Retain nanoseconds for ordering; Date alone silently truncates them.
export function canonicalTime(value: unknown): string | undefined {
  if (typeof value !== "string") return;
  const match = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.(\d{9})Z$/.exec(value);
  if (match === null) return;
  const seconds = value.slice(0, 19);
  const time = Date.parse(seconds + "Z");
  if (
    !Number.isFinite(time) ||
    new Date(time).toISOString().slice(0, 19) !== seconds
  )
    return;
  return seconds + (match[1] ?? "").padEnd(9, "0");
}
// Bounded RFC 3492 decoding lets the native URL parser validate the Unicode
// spelling too: browsers can otherwise fast-path malformed ASCII A-labels.
function canonicalALabel(label: string): boolean {
  const encoded = label.slice(4),
    delimiter = encoded.lastIndexOf("-");
  const points =
    delimiter < 0
      ? []
      : Array.from(encoded.slice(0, delimiter), (c) => c.charCodeAt(0));
  let position = delimiter < 0 ? 0 : delimiter + 1,
    n = 128,
    i = 0,
    bias = 72;
  const max = 0x7fffffff;
  while (position < encoded.length) {
    const old = i;
    let weight = 1;
    for (let k = 36; ; k += 36) {
      if (position >= encoded.length) return false;
      const code = encoded.charCodeAt(position++);
      const digit =
        code >= 97 && code <= 122
          ? code - 97
          : code >= 48 && code <= 57
            ? code - 22
            : 36;
      if (digit >= 36 || digit > Math.floor((max - i) / weight)) return false;
      i += digit * weight;
      const threshold = k <= bias ? 1 : k >= bias + 26 ? 26 : k - bias;
      if (digit < threshold) break;
      if (weight > Math.floor(max / (36 - threshold))) return false;
      weight *= 36 - threshold;
    }
    const count = points.length + 1;
    let delta = Math.floor((i - old) / (old === 0 ? 700 : 2));
    delta += Math.floor(delta / count);
    let k = 0;
    while (delta > 455) {
      delta = Math.floor(delta / 35);
      k += 36;
    }
    bias = k + Math.floor((36 * delta) / (delta + 38));
    n += Math.floor(i / count);
    if (n > 0x10ffff || (n >= 0xd800 && n <= 0xdfff)) return false;
    i %= count;
    points.splice(i, 0, n);
    i++;
  }
  if (!points.some((point) => point >= 128)) return false;
  const decoded = String.fromCodePoint(...points),
    bytes = new TextEncoder().encode(decoded);
  if (
    bytes[0] === 45 ||
    bytes.at(-1) === 45 ||
    (bytes.length > 4 && bytes[2] === 45 && bytes[3] === 45)
  )
    return false;
  try {
    return new URL(`http://${decoded}/`).hostname === label;
  } catch {
    return false;
  }
}
export function canonicalHost(value: unknown): boolean {
  if (typeof value !== "string") return false;
  const wildcard = value.startsWith("*.");
  const host = wildcard ? value.slice(2) : value;
  if (host.length === 0 || host.length > 253) return false;
  try {
    if (host.includes(":")) {
      const normalized = new URL(`http://[${host}]/`).hostname;
      return (
        !wildcard &&
        normalized === `[${host}]` &&
        !/^\[::ffff:[0-9a-f]+:[0-9a-f]+\]$/.test(normalized)
      );
    }
    if (new URL(`http://${host}/`).hostname !== host) return false;
    if (/^\d+\.\d+\.\d+\.\d+$/.test(host)) return !wildcard;
    return (
      (!wildcard || host.includes(".")) &&
      host
        .split(".")
        .every(
          (label) =>
            label.length <= 63 &&
            (label.slice(2, 4) !== "--" ||
              (label.startsWith("xn--") && canonicalALabel(label))) &&
            /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label),
        ) &&
      !/^(?:\d+|0x[0-9a-f]*)$/.test(host.split(".").at(-1)!)
    );
  } catch {
    return false;
  }
}
export function validatePolicy(value: unknown): void {
  const p = object(value);
  const kind = String(p.type);
  const requests = kind === "allow_requests" || kind === "block_requests";
  const allow = kind === "allow_requests" || kind === "allow_tunnel";
  exact(p, [
    "version",
    "type",
    requests ? "request" : "destination",
    ...(allow ? ["allow_private"] : []),
    ...(Object.hasOwn(p, "credential_id") ? ["credential_id"] : []),
  ]);
  if (
    p.version !== 1 ||
    ![
      "block_destination",
      "allow_tunnel",
      "block_requests",
      "allow_requests",
    ].includes(kind) ||
    (allow && typeof p.allow_private !== "boolean") ||
    (Object.hasOwn(p, "credential_id") &&
      (kind !== "allow_requests" ||
        typeof p.credential_id !== "string" ||
        !idPattern.test(p.credential_id))) ||
    new TextEncoder().encode(JSON.stringify(p)).length > 16384
  )
    throw new Error("Invalid HTTP policy.");
  const request = requests
    ? exact(p.request, ["origin", "methods", "path"])
    : undefined;
  const d = exact(
    request?.origin ?? p.destination,
    requests ? ["scheme", "host", "port"] : ["host", "port"],
  );
  if (
    !canonicalHost(d.host) ||
    typeof d.port !== "number" ||
    !Number.isInteger(d.port) ||
    d.port < 1 ||
    d.port > 65535 ||
    (requests && !["http", "https"].includes(String(d.scheme))) ||
    (p.credential_id !== undefined && d.scheme !== "https")
  )
    throw new Error("Invalid destination.");
  if (request !== undefined) {
    const methods = object(request.methods);
    if (methods.any === true) exact(methods, ["any"]);
    else {
      exact(methods, ["values"]);
      if (
        !Array.isArray(methods.values) ||
        methods.values.length === 0 ||
        methods.values.length > 32 ||
        methods.values.some(
          (v, i, values) =>
            typeof v !== "string" ||
            v.length > 32 ||
            v === "CONNECT" ||
            !/^[!#$%&'*+.^_`|~0-9A-Z-]+$/.test(v) ||
            (i > 0 && values[i - 1] >= v),
        )
      )
        throw new Error("Invalid methods.");
    }
    const path = object(request.path);
    if (path.kind === "any") exact(path, ["kind"]);
    else {
      exact(path, ["kind", "value"]);
      if (
        !["exact", "segment_prefix"].includes(String(path.kind)) ||
        typeof path.value !== "string" ||
        path.value.length > 4096 ||
        !path.value.startsWith("/") ||
        !/^[A-Za-z0-9/._~-]+$/.test(path.value) ||
        path.value.includes("//") ||
        path.value
          .split("/")
          .some((segment) => segment === "." || segment === "..") ||
        (path.kind === "segment_prefix" &&
          path.value !== "/" &&
          path.value.endsWith("/"))
      )
        throw new Error("Invalid path.");
    }
  }
}
export function decodeGrant(value: unknown): Grant {
  const g = exact(value, [
    "id",
    "principal_id",
    "description",
    "revision",
    "policy",
    "expires_at",
    "state",
    "created_at",
    "updated_at",
  ]);
  if (
    typeof g.id !== "string" ||
    !idPattern.test(g.id) ||
    typeof g.principal_id !== "string" ||
    !idPattern.test(g.principal_id) ||
    !revision(g.revision) ||
    !["active", "expired"].includes(String(g.state))
  )
    throw new Error("HTTP policy data unavailable.");
  if (
    g.description !== null &&
    (typeof g.description !== "string" ||
      new TextEncoder().encode(g.description).length < 1 ||
      new TextEncoder().encode(g.description).length > 256 ||
      /[\u0000-\u001f\u007f-\u009f\uD800-\uDFFF]/u.test(g.description) ||
      /^\p{White_Space}|\p{White_Space}$/u.test(g.description))
  )
    throw new Error("Invalid description.");
  validatePolicy(g.policy);
  const created = canonicalTime(g.created_at),
    updated = canonicalTime(g.updated_at);
  if (created === undefined || updated === undefined || updated < created)
    throw new Error("Invalid grant time.");
  // Expiry can pass while a response travels; never compare with the client clock.
  if (g.expires_at === null) {
    if (g.state === "expired") throw new Error("Invalid expiry.");
  } else {
    const expires = canonicalTime(g.expires_at);
    if (expires === undefined || expires <= created)
      throw new Error("Invalid expiry.");
  }
  return value as Grant;
}
export function decodePreview(
  value: unknown,
  principalID: string,
  connect: boolean,
): Record<string, unknown> {
  const r = exact(value, [
    "decision",
    "default",
    "policy_only",
    "network_verified",
    "tls_verified",
    "material_verified",
    "admission_authority",
  ]);
  if (
    r.policy_only !== true ||
    r.network_verified !== false ||
    r.tls_verified !== false ||
    r.material_verified !== false ||
    r.admission_authority !== false ||
    !["allow", "block"].includes(String(r.default))
  )
    throw new Error("Invalid preview.");
  validateHTTPDecision(r.decision, principalID, connect, r.default);
  return r;
}

export function validateHTTPDecision(
  value: unknown,
  principalID: string,
  connect: boolean,
  defaultPolicy: unknown,
): Record<string, unknown> {
  const d = object(value);
  const refs = [
    "grant",
    "private_grant",
    "credential",
    "credential_grant",
    "conflict_credential",
    "conflict_grant",
  ];
  exact(d, [
    "version",
    "principal",
    "policy_revision",
    "default_revision",
    "transport",
    "allowed",
    "reason",
    ...refs.filter((k) => Object.hasOwn(d, k)),
  ]);
  if (d.version !== 1 || typeof d.allowed !== "boolean")
    throw new Error("Invalid decision.");
  for (const key of ["policy_revision", "default_revision"])
    if (!Number.isSafeInteger(d[key]) || Number(d[key]) < 1)
      throw new Error("Invalid revision.");
  for (const key of ["principal", ...refs.filter((k) => Object.hasOwn(d, k))]) {
    const ref = exact(d[key], ["id", "revision"]);
    if (
      typeof ref.id !== "string" ||
      !idPattern.test(ref.id) ||
      !Number.isSafeInteger(ref.revision) ||
      Number(ref.revision) < 1
    )
      throw new Error("Invalid decision reference.");
  }
  if (
    object(d.principal).id !== principalID ||
    !connect !== (d.transport === "request")
  )
    throw new Error("Invalid preview identity.");
  const has = (key: string) => d[key] !== undefined;
  if (
    has("credential") !== has("credential_grant") ||
    has("conflict_credential") !== has("conflict_grant") ||
    (has("credential") && (d.transport !== "request" || !has("grant")))
  )
    throw new Error("Invalid credential evidence.");
  if (
    has("conflict_credential") &&
    (!has("credential") ||
      object(d.credential).id === object(d.conflict_credential).id ||
      String(object(d.credential_grant).id) >=
        String(object(d.conflict_grant).id))
  )
    throw new Error("Invalid conflict evidence.");
  const noExtras =
    !has("private_grant") && !has("credential") && !has("conflict_credential");
  let valid = false;
  switch (d.reason) {
    case "destination_block":
      valid =
        !d.allowed &&
        has("grant") &&
        noExtras &&
        ["none", "request"].includes(String(d.transport));
      break;
    case "request_block":
      valid =
        !d.allowed && has("grant") && noExtras && d.transport === "request";
      break;
    case "intercept_required":
      valid =
        !d.allowed && !has("grant") && noExtras && d.transport === "intercept";
      break;
    case "tunnel_allow":
      valid =
        d.allowed &&
        has("grant") &&
        !has("credential") &&
        d.transport === "tunnel";
      break;
    case "request_allow":
      valid =
        d.allowed &&
        has("grant") &&
        !has("conflict_credential") &&
        d.transport === "request";
      break;
    case "principal_default":
      valid =
        d.allowed === (defaultPolicy === "allow") &&
        !has("grant") &&
        noExtras &&
        d.transport === "request";
      break;
    case "credential_conflict":
      valid =
        !d.allowed &&
        has("grant") &&
        has("conflict_credential") &&
        d.transport === "request";
      break;
    case "credential_unavailable":
      valid =
        !d.allowed &&
        has("grant") &&
        has("credential") &&
        !has("conflict_credential") &&
        d.transport === "request";
      break;
    case "address_forbidden":
    case "private_permission_required":
      // The server can classify literal IPs without DNS or network verification.
      valid =
        !d.allowed &&
        !has("conflict_credential") &&
        (d.reason !== "private_permission_required" || !has("private_grant")) &&
        (d.transport === "tunnel"
          ? has("grant") && !has("credential")
          : d.transport === "request" &&
            (has("grant") || (defaultPolicy === "allow" && noExtras)));
      break;
  }
  if (!valid) throw new Error("Invalid HTTP decision.");
  return d;
}
