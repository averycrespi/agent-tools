export type GitKind = "repositories" | "grants" | "credentials";
export interface GitRef {
  id: string;
  revision: string;
}
export interface GitRule {
  ref: { kind: "exact" | "prefix"; value: string };
  actions: string[];
}
export interface GitPolicy {
  version: number;
  read: boolean;
  refs: GitRule[];
}
export interface GitResource {
  id: string;
  revision: string;
  created_at: string;
  updated_at: string;
  name?: string;
  url?: string;
  aliases?: string[];
  credential_id?: string | null;
  alias_revision?: string;
  principal_id?: string;
  repository_id?: string;
  description?: string | null;
  policy?: GitPolicy;
  expires_at?: string | null;
  state?: string;
  origin?: string;
  recipe?: { header: string; prefix: string };
  available?: boolean;
  referencing_repositories?: { id: string }[];
}
export const gitID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const revision = /^[1-9][0-9]*$/;
function fail(): never {
  throw new Error("Git data is unavailable.");
}
export function object(
  value: unknown,
  required: string[],
  optional: string[] = [],
): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    return fail();
  const r = value as Record<string, unknown>;
  if (
    required.some((k) => !Object.hasOwn(r, k)) ||
    Object.keys(r).some((k) => !required.includes(k) && !optional.includes(k))
  )
    return fail();
  return r;
}
function text(v: unknown, max = 4096): v is string {
  return (
    typeof v === "string" &&
    new TextEncoder().encode(v).length <= max &&
    !/[\p{Cc}\p{Cf}]/u.test(v)
  );
}
function id(v: unknown): boolean {
  return typeof v === "string" && gitID.test(v);
}
function rev(v: unknown): boolean {
  return typeof v === "string" && revision.test(v);
}
function time(v: unknown): boolean {
  return typeof v === "string" && Number.isFinite(Date.parse(v));
}
export function decodeGitResource(kind: GitKind, value: unknown): GitResource {
  const common = ["id", "revision", "created_at", "updated_at"];
  const keys =
    kind === "repositories"
      ? ["name", "url", "aliases", "credential_id", "alias_revision"]
      : kind === "credentials"
        ? ["name", "origin", "recipe", "available", "referencing_repositories"]
        : [
            "principal_id",
            "repository_id",
            "description",
            "policy",
            "expires_at",
            "state",
          ];
  const r = object(value, [...common, ...keys]);
  if (
    !id(r.id) ||
    !rev(r.revision) ||
    !time(r.created_at) ||
    !time(r.updated_at)
  )
    return fail();
  if (kind !== "grants" && (!text(r.name, 256) || r.name.length === 0))
    return fail();
  if (kind === "repositories") {
    if (
      !text(r.url) ||
      !r.url.startsWith("https://") ||
      !rev(r.alias_revision) ||
      !(r.credential_id === null || id(r.credential_id)) ||
      !Array.isArray(r.aliases) ||
      r.aliases.length > 4 ||
      r.aliases.some((v) => !text(v) || !v.startsWith("https://"))
    )
      return fail();
  } else if (kind === "credentials") {
    const recipe = object(r.recipe, ["header", "prefix"]);
    if (
      !text(r.origin) ||
      !r.origin.startsWith("https://") ||
      !text(recipe.header, 128) ||
      !text(recipe.prefix, 128) ||
      typeof r.available !== "boolean" ||
      !Array.isArray(r.referencing_repositories) ||
      r.referencing_repositories.length > 256
    )
      return fail();
    for (const ref of r.referencing_repositories)
      if (!id(object(ref, ["id"]).id)) return fail();
  } else {
    if (
      !id(r.principal_id) ||
      !id(r.repository_id) ||
      !(r.description === null || text(r.description, 1024)) ||
      !(r.expires_at === null || time(r.expires_at)) ||
      !["active", "expired"].includes(String(r.state))
    )
      return fail();
    const p = object(r.policy, ["version", "read", "refs"]);
    if (
      p.version !== 1 ||
      typeof p.read !== "boolean" ||
      !Array.isArray(p.refs) ||
      p.refs.length > 128 ||
      (p.refs.length > 0 && !p.read)
    )
      return fail();
    for (const rule of p.refs) {
      const v = object(rule, ["ref", "actions"]),
        ref = object(v.ref, ["kind", "value"]);
      if (
        !["exact", "prefix"].includes(String(ref.kind)) ||
        !text(ref.value, 1024) ||
        !Array.isArray(v.actions) ||
        v.actions.length === 0 ||
        v.actions.length > 3 ||
        v.actions.some(
          (a) => !["create", "update", "delete"].includes(String(a)),
        )
      )
        return fail();
    }
  }
  return value as GitResource;
}
export function gitETag(kind: GitKind, r: GitResource): string {
  return `"git-${kind === "repositories" ? "repository" : kind === "credentials" ? "credential" : "grant"}-${r.id}-${r.revision}"`;
}
export async function decodeGitResponse(
  kind: GitKind,
  response: Response,
): Promise<GitResource> {
  if (response.headers.get("Content-Type") !== "application/json")
    return fail();
  const body = await response.text();
  if (body.length > 1024 * 1024) return fail();
  const r = decodeGitResource(kind, JSON.parse(body));
  if (response.headers.get("ETag") !== gitETag(kind, r)) return fail();
  return r;
}
export interface GitTraffic {
  admission: {
    id: string;
    admitted_at: string;
    evaluated_at: string;
    principal: GitRef;
    agent_credential: GitRef;
    repository: GitRef;
    alias_revision: string;
    profile_revision: string;
    authorization_revision: string;
    operation: string;
    commands: number;
    allowed: boolean;
    rejection?: string;
    denial?: string;
    policy?: {
      repository_name: string;
      repository_url: string;
      grants: GitRef[];
      grant_count: number;
      creates: number;
      updates: number;
      deletes: number;
    };
    material?: { credential: GitRef; generation: string };
    private_grant?: { id: string; revision: number };
  };
  completion: null | {
    completed_at: string;
    outcome: string;
    status?: number;
    bytes_sent: number;
    bytes_received: number;
    duration_ms: number;
    transfer_complete: boolean;
    reported_result?: string;
    failure?: string;
  };
}
function ref(value: unknown, empty = false): void {
  const r = object(value, ["id", "revision"]);
  if (empty && r.id === "" && r.revision === "") return;
  if (!id(r.id) || !rev(r.revision)) fail();
}
export function decodeGitTrafficPage(value: unknown) {
  const page = object(value, ["items", "next_cursor"]);
  if (
    !Array.isArray(page.items) ||
    page.items.length > 50 ||
    (page.next_cursor !== null &&
      (typeof page.next_cursor !== "string" ||
        page.next_cursor.length > 512 ||
        !/^[A-Za-z0-9_-]+$/.test(page.next_cursor)))
  )
    return fail();
  const items = page.items.map(decodeGitTraffic);
  if (
    new Set(items.map((item) => item.admission.id)).size !== items.length ||
    (items.length === 0 && page.next_cursor !== null)
  )
    return fail();
  return {
    items,
    nextCursor: page.next_cursor as string | null,
    totalCount: undefined,
    offset: 0,
  };
}

export function decodeGitTraffic(value: unknown): GitTraffic {
  const v = object(value, ["admission", "completion"]);
  const a = object(
    v.admission,
    [
      "id",
      "admitted_at",
      "evaluated_at",
      "principal",
      "agent_credential",
      "repository",
      "alias_revision",
      "profile_revision",
      "authorization_revision",
      "operation",
      "commands",
      "allowed",
    ],
    ["policy", "material", "private_grant", "rejection", "denial"],
  );
  if (
    !id(a.id) ||
    !time(a.admitted_at) ||
    !time(a.evaluated_at) ||
    !rev(a.profile_revision) ||
    !rev(a.authorization_revision) ||
    typeof a.allowed !== "boolean" ||
    !Number.isInteger(a.commands) ||
    Number(a.commands) < 0 ||
    Number(a.commands) > 128 ||
    ![
      "read",
      "read_discovery",
      "push",
      "push_discovery",
      "probe",
      "invalid",
    ].includes(String(a.operation))
  )
    return fail();
  if (
    Date.parse(String(a.evaluated_at)) < Date.parse(String(a.admitted_at)) ||
    (a.operation === "push") !== Number(a.commands) > 0 ||
    (a.operation === "invalid") !== (a.rejection !== undefined) ||
    ((a.rejection !== undefined || a.denial !== undefined) && a.allowed) ||
    (a.operation !== "invalid" && !rev(a.alias_revision))
  )
    return fail();
  if (
    a.operation === "invalid" &&
    (a.alias_revision !== "" ||
      a.policy !== undefined ||
      a.material !== undefined ||
      a.private_grant !== undefined)
  )
    return fail();
  ref(a.principal);
  ref(a.agent_credential);
  ref(a.repository, a.operation === "invalid");
  if (
    a.rejection !== undefined &&
    ![
      "unsupported",
      "repository_unavailable",
      "destination_unavailable",
    ].includes(String(a.rejection))
  )
    return fail();
  if (a.denial !== undefined && a.denial !== "credential_unavailable")
    return fail();
  if (a.policy !== undefined) {
    const p = object(a.policy, [
      "repository_name",
      "repository_url",
      "grants",
      "grant_count",
      "creates",
      "updates",
      "deletes",
    ]);
    if (
      !text(p.repository_name, 256) ||
      !text(p.repository_url) ||
      !Array.isArray(p.grants) ||
      p.grants.length > 4
    )
      return fail();
    p.grants.forEach((x) => ref(x));
    for (const k of ["grant_count", "creates", "updates", "deletes"])
      if (!Number.isInteger(p[k]) || Number(p[k]) < 0) return fail();
    if (
      Number(p.grant_count) < p.grants.length ||
      Number(p.grant_count) > 4096 ||
      Number(p.creates) + Number(p.updates) + Number(p.deletes) !==
        a.commands ||
      new Set(p.grants.map((g) => (g as GitRef).id)).size !== p.grants.length
    )
      return fail();
  }
  if (a.material !== undefined) {
    const m = object(a.material, ["credential", "generation"]);
    ref(m.credential);
    if (!rev(m.generation) || !a.allowed) return fail();
  }
  if (a.private_grant !== undefined) {
    const p = object(a.private_grant, ["id", "revision"]);
    if (
      !id(p.id) ||
      !Number.isSafeInteger(p.revision) ||
      Number(p.revision) < 1
    )
      return fail();
  }
  if (v.completion !== null) {
    const c = object(
      v.completion,
      [
        "completed_at",
        "outcome",
        "bytes_sent",
        "bytes_received",
        "duration_ms",
        "transfer_complete",
      ],
      ["status", "reported_result", "failure"],
    );
    if (
      !a.allowed ||
      !time(c.completed_at) ||
      Date.parse(String(c.completed_at)) < Date.parse(String(a.evaluated_at)) ||
      typeof c.transfer_complete !== "boolean" ||
      !["nonmutation", "prestart_failure", "outcome_unknown"].includes(
        String(c.outcome),
      )
    )
      return fail();
    for (const k of ["bytes_sent", "bytes_received", "duration_ms"])
      if (!Number.isSafeInteger(c[k]) || Number(c[k]) < 0) return fail();
    if (
      c.status !== undefined &&
      (!Number.isInteger(c.status) ||
        Number(c.status) < 100 ||
        Number(c.status) > 599)
    )
      return fail();
    if (
      (c.transfer_complete && c.status === undefined) ||
      (c.outcome === "nonmutation" &&
        (!c.transfer_complete || a.operation === "push")) ||
      (c.outcome === "prestart_failure" &&
        (c.status !== undefined ||
          c.bytes_sent !== 0 ||
          c.bytes_received !== 0 ||
          c.transfer_complete)) ||
      (c.failure !== undefined && c.outcome !== "prestart_failure")
    )
      return fail();
    if (
      c.reported_result !== undefined &&
      (!["reported_success", "reported_partial", "reported_failure"].includes(
        String(c.reported_result),
      ) ||
        a.operation !== "push" ||
        c.status !== 200 ||
        !c.transfer_complete)
    )
      return fail();
    if (
      c.failure !== undefined &&
      !["credential_unavailable", "authorization_unavailable"].includes(
        String(c.failure),
      )
    )
      return fail();
  }
  return value as GitTraffic;
}
export const gitLabels: Record<string, string> = {
  read: "Read",
  read_discovery: "Read discovery",
  push: "Push",
  push_discovery: "Push discovery",
  probe: "Push probe",
  invalid: "Unsupported exchange",
  unsupported: "Unsupported Git request",
  repository_unavailable: "Repository unavailable",
  destination_unavailable: "Destination unavailable",
  credential_unavailable: "Credential unavailable",
  authorization_unavailable: "Authorization unavailable",
  reported_success: "Reported success",
  reported_failure: "Reported failure",
  reported_partial: "Reported partial success",
};
export function gitFacts(r: GitTraffic): {
  admission: string;
  transport: string;
  report: string;
} {
  const a = r.admission,
    c = r.completion;
  return {
    admission: a.allowed
      ? "Allowed"
      : (gitLabels[a.rejection ?? a.denial ?? ""] ?? "Blocked"),
    transport: !a.allowed
      ? "Not dispatched"
      : c?.outcome === "prestart_failure"
        ? (gitLabels[c.failure ?? ""] ?? "Not started")
        : c === null
          ? "Unknown"
          : c.transfer_complete
            ? "Complete"
            : "Incomplete",
    report:
      a.operation !== "push"
        ? "Not a push"
        : c?.reported_result
          ? gitLabels[c.reported_result]!
          : "Unknown",
  };
}
