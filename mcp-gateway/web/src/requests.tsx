import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import {
  matcherSchemaSuggestions,
  readMatcherDescriptor,
  readMatcherDescriptors,
  type MatcherDescriptorSummary,
  type MatcherSchemaSuggestions,
} from "./matcher-catalog";
import type { DescriptorView } from "./server-reads";
import {
  MatcherAtomEditor,
  MatcherRecognition,
  matcherConstraintText,
} from "./matcher-editor";
import type { MatcherAtom } from "./matcher-editor";
import { validateMatcherConstraint } from "./matcher-validation";
import { useUnsavedChanges } from "./navigation";
import {
  type MutationController,
  type MutationCoordinator,
  type MutationSnapshot,
  type MutationSpec,
} from "./mutation";
import {
  CollectionTable,
  ConfirmationDialog,
  containsControlCharacters,
  FormField,
  InertJSON,
  sentenceCase,
  StateNotice,
  StatusLabel,
  SuggestionInput,
} from "./primitives";
import type { ProtectedContext, SessionClient } from "./session";
import { UserTime } from "./time";
import {
  readCollectionPage,
  useCollectionPage,
  type ViewSnapshot,
} from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const durationUnits = { days: 86400, hours: 3600, minutes: 60, seconds: 1 };
type DurationUnit = keyof typeof durationUnits;
type JSONRecord = Record<string, unknown>;
export type RequestState = "pending" | "approved" | "rejected" | "cancelled";
type Scope = "tool" | "server";
interface Policy {
  scope: Scope;
  target: string;
  constraint: unknown | null;
  durationSeconds: string | null;
  futureToolsAcknowledged: boolean;
}
export interface RequestSummary {
  id: string;
  principalID: string;
  state: RequestState;
  revision: string;
  requestedPolicy: Policy;
  approvedPolicy: Policy | null;
  approvedGrantID: string | null;
  rejectionReason: string | null;
  createdAt: string;
  updatedAt: string;
  closedAt: string | null;
}
interface DescriptorEvidence {
  serverID: string;
  toolID: string;
  namespace: string;
  upstreamName: string;
  externalName: string;
  catalogRevision: string;
  fingerprint: string;
  durableState: "current" | "retired";
  descriptor: unknown;
  capturedAt: string;
}
interface TargetComparison {
  scope: Scope;
  targetState: "extant" | "deleted";
  activeState: "current" | "stale" | "absent" | "unavailable" | null;
  durableState: "current" | "retired" | "absent" | null;
  catalogRevision: string | null;
  fingerprint: string | null;
  descriptor: unknown | null;
}
export interface RequestDetail extends RequestSummary {
  submittedConstraintSource: string | null;
  resolvedServerID: string;
  resolvedUpstreamName: string | null;
  submittedEvidence: DescriptorEvidence | null;
  approvedEvidence: DescriptorEvidence | null;
  currentTarget: TargetComparison;
  etag: string;
}
function record(value: unknown, keys: readonly string[]): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  const result = value as JSONRecord;
  if (Object.keys(result).sort().join(",") !== [...keys].sort().join(","))
    throw new Error("invalid response");
  return result;
}
function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("invalid response");
  return value;
}
function nullableText(value: unknown): string | null {
  if (value !== null && typeof value !== "string")
    throw new Error("invalid response");
  return value;
}
function id(value: unknown): string {
  const result = text(value);
  if (!gatewayID.test(result)) throw new Error("invalid response");
  return result;
}
function scalarObject(value: unknown): unknown {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  return value;
}
function closed<T extends string>(value: unknown, allowed: readonly T[]): T {
  const result = text(value);
  if (!allowed.includes(result as T)) throw new Error("invalid response");
  return result as T;
}
function nullableClosed<T extends string>(
  value: unknown,
  allowed: readonly T[],
): T | null {
  return value === null ? null : closed(value, allowed);
}
function decodePolicy(value: unknown): Policy {
  const item = record(value, [
    "scope",
    "target",
    "constraint",
    "duration_seconds",
    "future_tools_acknowledged",
  ]);
  if (
    item.constraint !== null &&
    (typeof item.constraint !== "object" || Array.isArray(item.constraint))
  )
    throw new Error("invalid response");
  if (typeof item.future_tools_acknowledged !== "boolean")
    throw new Error("invalid response");
  return {
    scope: closed(item.scope, ["tool", "server"]),
    target: text(item.target),
    constraint: item.constraint,
    durationSeconds: nullableText(item.duration_seconds),
    futureToolsAcknowledged: item.future_tools_acknowledged,
  };
}
const summaryKeys = [
  "id",
  "principal_id",
  "state",
  "revision",
  "requested_policy",
  "approved_policy",
  "approved_grant_id",
  "rejection_reason",
  "created_at",
  "updated_at",
  "closed_at",
] as const;
function decodeSummary(value: unknown): RequestSummary {
  const item = record(value, summaryKeys);
  const approvedGrantID = nullableText(item.approved_grant_id);
  if (approvedGrantID !== null && !gatewayID.test(approvedGrantID))
    throw new Error("invalid response");
  return {
    id: id(item.id),
    principalID: id(item.principal_id),
    state: closed(item.state, ["pending", "approved", "rejected", "cancelled"]),
    revision: text(item.revision),
    requestedPolicy: decodePolicy(item.requested_policy),
    approvedPolicy:
      item.approved_policy === null ? null : decodePolicy(item.approved_policy),
    approvedGrantID,
    rejectionReason: nullableText(item.rejection_reason),
    createdAt: text(item.created_at),
    updatedAt: text(item.updated_at),
    closedAt: nullableText(item.closed_at),
  };
}
function decodeEvidence(value: unknown): DescriptorEvidence {
  const item = record(value, [
    "server_id",
    "tool_id",
    "namespace",
    "upstream_name",
    "external_name",
    "catalog_revision",
    "fingerprint",
    "durable_state",
    "descriptor",
    "captured_at",
  ]);
  return {
    serverID: id(item.server_id),
    toolID: id(item.tool_id),
    namespace: text(item.namespace),
    upstreamName: text(item.upstream_name),
    externalName: text(item.external_name),
    catalogRevision: text(item.catalog_revision),
    fingerprint: text(item.fingerprint),
    durableState: closed(item.durable_state, ["current", "retired"]),
    descriptor: scalarObject(item.descriptor),
    capturedAt: text(item.captured_at),
  };
}
function decodeTarget(value: unknown): TargetComparison {
  const item = record(value, [
    "scope",
    "target_state",
    "active_state",
    "durable_state",
    "catalog_revision",
    "fingerprint",
    "descriptor",
  ]);
  return {
    scope: closed(item.scope, ["tool", "server"]),
    targetState: closed(item.target_state, ["extant", "deleted"]),
    activeState: nullableClosed(item.active_state, [
      "current",
      "stale",
      "absent",
      "unavailable",
    ]),
    durableState: nullableClosed(item.durable_state, [
      "current",
      "retired",
      "absent",
    ]),
    catalogRevision: nullableText(item.catalog_revision),
    fingerprint: nullableText(item.fingerprint),
    descriptor: item.descriptor === null ? null : scalarObject(item.descriptor),
  };
}
export function decodeRequestDetail(
  value: unknown,
  etag: string,
  submittedConstraintSource?: string | null,
): RequestDetail {
  const keys = [
    ...summaryKeys,
    "resolved_server_id",
    "resolved_upstream_name",
    "submitted_evidence",
    "approved_evidence",
    "current_target",
  ];
  const item = record(value, keys);
  const summary = decodeSummary(
    Object.fromEntries(summaryKeys.map((key) => [key, item[key]])),
  );
  const resolvedUpstreamName = nullableText(item.resolved_upstream_name);
  return {
    ...summary,
    submittedConstraintSource:
      submittedConstraintSource === undefined
        ? summary.requestedPolicy.constraint === null
          ? null
          : JSON.stringify(summary.requestedPolicy.constraint)
        : submittedConstraintSource,
    resolvedServerID: id(item.resolved_server_id),
    resolvedUpstreamName,
    submittedEvidence:
      item.submitted_evidence === null
        ? null
        : decodeEvidence(item.submitted_evidence),
    approvedEvidence:
      item.approved_evidence === null
        ? null
        : decodeEvidence(item.approved_evidence),
    currentTarget: decodeTarget(item.current_target),
    etag,
  };
}
function requestHeaders(context: ProtectedContext): HeadersInit {
  return { Accept: "application/json", "X-CSRF-Token": context.csrfToken };
}
function jsonStringEnd(source: string, start: number): number {
  if (source[start] !== '"') throw new Error("invalid response");
  for (let index = start + 1; index < source.length; index++) {
    if (source[index] === "\\") index += 1;
    else if (source[index] === '"') return index + 1;
  }
  throw new Error("invalid response");
}
function jsonValueEnd(source: string, start: number): number {
  if (source[start] === '"') return jsonStringEnd(source, start);
  if (source[start] === "{" || source[start] === "[") {
    const close = source[start] === "{" ? "}" : "]";
    let depth = 1;
    for (let index = start + 1; index < source.length; index++) {
      if (source[index] === '"') index = jsonStringEnd(source, index) - 1;
      else if (source[index] === source[start]) depth += 1;
      else if (source[index] === close && --depth === 0) return index + 1;
    }
    throw new Error("invalid response");
  }
  let index = start;
  while (index < source.length && !/[\s,}\]]/.test(source[index] ?? ""))
    index += 1;
  return index;
}
function jsonMemberSource(source: string, key: string): string | undefined {
  let index = 0;
  while (/\s/.test(source[index] ?? "")) index += 1;
  if (source[index++] !== "{") throw new Error("invalid response");
  for (;;) {
    while (/\s/.test(source[index] ?? "")) index += 1;
    if (source[index] === "}") return undefined;
    const keyEnd = jsonStringEnd(source, index);
    const member = JSON.parse(source.slice(index, keyEnd)) as unknown;
    index = keyEnd;
    while (/\s/.test(source[index] ?? "")) index += 1;
    if (source[index++] !== ":") throw new Error("invalid response");
    while (/\s/.test(source[index] ?? "")) index += 1;
    const valueStart = index;
    index = jsonValueEnd(source, valueStart);
    if (member === key) return source.slice(valueStart, index);
    while (/\s/.test(source[index] ?? "")) index += 1;
    if (source[index] === ",") index += 1;
    else if (source[index] === "}") return undefined;
    else throw new Error("invalid response");
  }
}
function requestedConstraintSource(source: string): string | null {
  const policy = jsonMemberSource(source, "requested_policy");
  if (policy === undefined) throw new Error("invalid response");
  const constraint = jsonMemberSource(policy, "constraint");
  if (constraint === undefined) throw new Error("invalid response");
  return constraint === "null" ? null : constraint;
}
async function requestJSON(
  session: SessionClient,
  route: string,
): Promise<{ response: Response; value: unknown; source: string } | undefined> {
  return session.runProtected(async (context) => {
    const response = await fetch(route, {
      credentials: "same-origin",
      redirect: "error",
      signal: context.signal,
      headers: requestHeaders(context),
    });
    if (await context.sessionLost(response)) return undefined;
    const type = response.headers.get("Content-Type");
    if (type !== "application/json" && type !== "application/problem+json")
      throw new Error("Request data is unavailable.");
    const source = await response.text();
    return { response, value: JSON.parse(source) as unknown, source };
  });
}
interface RequestRow extends RequestSummary {
  principalName: string;
  serverName: string;
  serverID: string;
  upstreamName: string | null;
}
function decodeRequestRow(value: unknown): RequestRow {
  const item = record(value, [
    "request",
    "principal_display_name",
    "server_display_name",
    "resolved_server_id",
    "resolved_upstream_name",
  ]);
  return {
    ...decodeSummary(item.request),
    principalName: text(item.principal_display_name),
    serverName: text(item.server_display_name),
    serverID: id(item.resolved_server_id),
    upstreamName: nullableText(item.resolved_upstream_name),
  };
}
function readQueue(
  session: SessionClient,
  query: Readonly<Record<string, string>>,
  cursor: string | null,
  signal: AbortSignal,
) {
  const params = new URLSearchParams({
    limit: "50",
    representation: "table",
    sort: query.sort ?? "submitted",
    direction:
      query.direction ??
      (query.sort === undefined && query.queue === "all"
        ? "descending"
        : "ascending"),
  });
  for (const key of ["request", "principal", "target", "scope", "state"]) {
    const value = query[`filter_${key}`];
    if (value !== undefined) params.set(key, value);
  }
  if (query.queue !== "all") params.set("state", "pending");
  if (cursor !== null) params.set("cursor", cursor);
  return readCollectionPage(
    session,
    `/api/v1/grant-requests?${params}`,
    decodeRequestRow,
    signal,
  );
}
async function readRequest(
  session: SessionClient,
  requestID: string,
): Promise<RequestDetail | undefined> {
  const result = await requestJSON(
    session,
    `/api/v1/grant-requests/${requestID}`,
  );
  if (result === undefined) return undefined;
  if (!result.response.ok) throw new Error("Request data is unavailable.");
  const etag = result.response.headers.get("ETag");
  if (
    etag === null ||
    etag !==
      `"grant-request-${requestID}-${(result.value as JSONRecord).revision}"`
  )
    throw new Error("The current request revision is unavailable.");
  return decodeRequestDetail(
    result.value,
    etag,
    requestedConstraintSource(result.source),
  );
}
function readableDuration(seconds: string | null): string {
  if (seconds === null) return "Permanent";
  for (const [unit, size] of Object.entries(durationUnits)) {
    if (Number(seconds) % size === 0) {
      const amount = Number(seconds) / size;
      return `${amount} ${amount === 1 ? unit.slice(0, -1) : unit}`;
    }
  }
  return `${seconds} seconds`;
}

function Conditions({
  source,
  locked = false,
}: {
  source: string | null;
  locked?: boolean;
}) {
  if (source === null || source === "") return <p>Unrestricted arguments</p>;
  try {
    const shape = matcherShape(JSON.parse(source) as unknown);
    return (
      <div>
        <ul class="request-conditions">
          {(["equals", "regex"] as const).flatMap((operator) =>
            Object.entries(shape[operator]).map(([pointer, value]) => (
              <li>
                {locked && <strong>Locked · </strong>}
                <code>{pointer}</code>{" "}
                {operator === "equals" ? "equals" : "matches"}{" "}
                <code>{JSON.stringify(value)}</code>
              </li>
            )),
          )}
        </ul>
        <details>
          <summary>Exact condition source</summary>
          <textarea
            aria-label="Exact condition source"
            data-testid={locked ? "approval-submitted-constraint" : undefined}
            class="inert-json"
            readOnly
            value={source}
          />
        </details>
      </div>
    );
  } catch {
    return <StateNotice state="error" title="Conditions unavailable" />;
  }
}

function PolicyWarnings({ policy }: { policy: Policy }) {
  return (
    <div class="review-stack">
      {policy.durationSeconds === null && (
        <StateNotice
          state="warning"
          title="Permanent access has no automatic expiry"
        />
      )}
      {policy.constraint === null && (
        <StateNotice
          state="warning"
          title="Unconstrained access matches every argument object"
        />
      )}
      {policy.scope === "server" && (
        <StateNotice
          state="warning"
          title="Server-wide access includes future tools"
        />
      )}
    </div>
  );
}

function descriptorComparison(detail: RequestDetail): string {
  const policy = detail.approvedPolicy ?? detail.requestedPolicy;
  if (policy.scope === "server")
    return "Not applicable to server-wide authority";
  const evidence =
    detail.approvedPolicy !== null && detail.requestedPolicy.scope === "server"
      ? detail.approvedEvidence
      : detail.submittedEvidence;
  if (
    evidence === null ||
    detail.currentTarget.fingerprint === null ||
    detail.currentTarget.descriptor === null
  )
    return "Descriptor comparison unavailable — missing evidence";
  return evidence.fingerprint === detail.currentTarget.fingerprint
    ? "Descriptor unchanged"
    : "Descriptor changed — inspect submitted and current evidence";
}

function policyFacts(policy: Policy) {
  return (
    <dl class="fact-grid">
      <div>
        <dt>Scope</dt>
        <dd>{sentenceCase(policy.scope)}</dd>
      </div>
      <div>
        <dt>Target</dt>
        <dd>{policy.target}</dd>
      </div>
      <div>
        <dt>Duration</dt>
        <dd>{readableDuration(policy.durationSeconds)}</dd>
      </div>
      <div>
        <dt>Future tools acknowledged</dt>
        <dd>{policy.futureToolsAcknowledged ? "Yes" : "No"}</dd>
      </div>
    </dl>
  );
}

function policyComparisonFacts(requested: Policy, approved: Policy) {
  return (
    <dl class="fact-grid">
      <div>
        <dt>Scope</dt>
        <dd>
          {sentenceCase(requested.scope)} →{" "}
          {requested.scope === approved.scope
            ? "Unchanged"
            : sentenceCase(approved.scope)}
        </dd>
      </div>
      <div>
        <dt>Tools</dt>
        <dd>
          {requested.target} →{" "}
          {requested.scope === approved.scope &&
          requested.target === approved.target
            ? "Unchanged"
            : approved.target}
        </dd>
      </div>
      <div>
        <dt>Duration</dt>
        <dd>
          {readableDuration(requested.durationSeconds)} →{" "}
          {requested.durationSeconds === approved.durationSeconds
            ? "Unchanged"
            : readableDuration(approved.durationSeconds)}
        </dd>
      </div>
      <div>
        <dt>Future tools acknowledged</dt>
        <dd>
          {requested.futureToolsAcknowledged ? "Yes" : "No"} →{" "}
          {requested.futureToolsAcknowledged ===
          approved.futureToolsAcknowledged
            ? "Unchanged"
            : approved.futureToolsAcknowledged
              ? "Yes"
              : "No"}
        </dd>
      </div>
    </dl>
  );
}
function Evidence({
  evidence,
  label,
}: {
  evidence: DescriptorEvidence | null;
  label: string;
}) {
  if (evidence === null)
    return (
      <StateNotice state="empty" title={`${label}: no descriptor evidence`} />
    );
  return (
    <section class="subpanel">
      <h3>{label}: immutable descriptor evidence</h3>
      <p>
        {evidence.durableState === "retired"
          ? "Retired historical evidence; it is not proof of a callable tool."
          : "Current when captured; current target comparison remains authoritative for present state."}
      </p>
      <dl class="fact-grid">
        <div>
          <dt>Namespace / tool</dt>
          <dd>
            {evidence.namespace} / {evidence.upstreamName}
          </dd>
        </div>
        <div>
          <dt>Catalog revision</dt>
          <dd>{evidence.catalogRevision}</dd>
        </div>
        <div>
          <dt>Fingerprint</dt>
          <dd class="technical-value">{evidence.fingerprint}</dd>
        </div>
        <div>
          <dt>Captured</dt>
          <dd>
            <UserTime value={evidence.capturedAt} />
          </dd>
        </div>
      </dl>
      <a href={`#/servers/${evidence.serverID}/descriptors/${evidence.toolID}`}>
        Open retained descriptor
      </a>
      <InertJSON
        value={evidence.descriptor}
        label={`${label} normalized descriptor`}
      />
    </section>
  );
}

interface MatcherShape {
  version: 1 | 2;
  equals: JSONRecord;
  regex: JSONRecord;
}

function matcherShape(value: unknown): MatcherShape {
  const constraint = scalarObject(value) as JSONRecord;
  const keys = Object.keys(constraint);
  const version =
    keys.length === 1 && keys[0] === "equals"
      ? 1
      : constraint.version === 2 &&
          keys.every((key) => ["version", "equals", "regex"].includes(key)) &&
          (constraint.equals !== undefined || constraint.regex !== undefined)
        ? 2
        : undefined;
  if (version === undefined) throw new Error("invalid constraint");
  const equals =
    constraint.equals === undefined
      ? {}
      : (scalarObject(constraint.equals) as JSONRecord);
  const regex =
    constraint.regex === undefined
      ? {}
      : (scalarObject(constraint.regex) as JSONRecord);
  const atoms = Object.keys(equals).length + Object.keys(regex).length;
  if (
    atoms < 1 ||
    atoms > 16 ||
    Object.values(equals).some(
      (item) => typeof item === "object" && item !== null,
    ) ||
    Object.values(regex).some((item) => typeof item !== "string")
  )
    throw new Error("invalid constraint");
  return { version, equals, regex };
}

function objectMembers(source: string | undefined): string {
  if (source === undefined) return "";
  const trimmed = source.trim();
  if (!trimmed.startsWith("{") || !trimmed.endsWith("}"))
    throw new Error("invalid constraint");
  return trimmed.slice(1, -1).trim();
}
function mergeConstraintSource(
  submittedSource: string | null,
  additionalSource: string,
): string {
  if (additionalSource === "") {
    if (submittedSource === null) return "";
    const submitted = matcherShape(JSON.parse(submittedSource) as unknown);
    return submitted.version === 2
      ? submittedSource
      : `{"version":2,"equals":${jsonMemberSource(submittedSource, "equals")}}`;
  }
  let additionalValue: unknown;
  try {
    additionalValue = JSON.parse(additionalSource) as unknown;
  } catch {
    throw new Error("Additional matcher atoms must be valid JSON.");
  }
  const additional = matcherShape(additionalValue);
  if (submittedSource === null) return additionalSource;
  if (additional.version !== 2)
    throw new Error("Additional matcher atoms must use version 2.");
  const submitted = matcherShape(JSON.parse(submittedSource) as unknown);
  for (const operator of ["equals", "regex"] as const) {
    if (
      Object.keys(additional[operator]).some((pointer) =>
        Object.hasOwn(submitted[operator], pointer),
      )
    )
      throw new Error(
        "Additional matcher atoms cannot replace a submitted operator and pointer.",
      );
  }
  const maps = (["equals", "regex"] as const)
    .map((operator) => {
      const members = [
        objectMembers(jsonMemberSource(submittedSource, operator)),
        objectMembers(jsonMemberSource(additionalSource, operator)),
      ].filter((value) => value !== "");
      return members.length === 0
        ? ""
        : `${JSON.stringify(operator)}:{${members.join(",")}}`;
    })
    .filter((value) => value !== "");
  const merged = `{"version":2,${maps.join(",")}}`;
  matcherShape(JSON.parse(merged) as unknown);
  return merged;
}

function RequestActions({
  session,
  mutations,
  detail,
  onRefresh,
  onAcknowledged,
  onUncertain,
  principalName,
  serverName,
  readAvailable,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  detail: RequestDetail;
  onRefresh: () => void;
  onAcknowledged: (detail: RequestDetail) => void;
  onUncertain: () => void;
  principalName: string;
  serverName: string;
  readAvailable: boolean;
}) {
  const submitted = detail.requestedPolicy;
  const [controller] = useState<MutationController<RequestDetail>>(() =>
    mutations.create<RequestDetail>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [mode, setMode] = useState<"approve" | "reject">("approve");
  const [narrowing, setNarrowing] = useState(false);
  const [unchanged, setUnchanged] = useState(false);
  const [rejecting, setRejecting] = useState(false);
  const rejectionBeforeOpen = useRef("not_approved");
  const reviewedDraftRef = useRef<string>();
  const unchangedButton = useRef<HTMLButtonElement>(null);
  const defaultDescription = "";
  const [description, setDescription] = useState(defaultDescription);
  const [scope, setScope] = useState<Scope>(submitted.scope);
  const [target, setTarget] = useState(submitted.target);
  const [approvalDescriptors, setApprovalDescriptors] =
    useState<MatcherDescriptorSummary[]>();
  const [selectedApprovalDescriptor, setSelectedApprovalDescriptor] =
    useState<DescriptorView>();
  const [approvalCatalogError, setApprovalCatalogError] = useState(false);
  const [approvalDescriptorError, setApprovalDescriptorError] = useState(false);
  const [additionalAtoms, setAdditionalAtoms] = useState<MatcherAtom[]>([]);
  const initialDurationUnit =
    (Object.keys(durationUnits) as DurationUnit[]).find(
      (unit) =>
        submitted.durationSeconds !== null &&
        Number(submitted.durationSeconds) % durationUnits[unit] === 0,
    ) ?? "minutes";
  const [durationUnit, setDurationUnit] =
    useState<DurationUnit>(initialDurationUnit);
  const [durationAmount, setDurationAmount] = useState(
    submitted.durationSeconds === null
      ? ""
      : String(
          Number(submitted.durationSeconds) /
            durationUnits[initialDurationUnit],
        ),
  );
  const duration =
    durationAmount === ""
      ? ""
      : String(Number(durationAmount) * durationUnits[durationUnit]);
  const [reason, setReason] = useState("not_approved");
  const initialDraft = useRef({
    description: defaultDescription,
    scope: submitted.scope,
    target: submitted.target,
    additionalAtoms: [] as MatcherAtom[],
    duration: submitted.durationSeconds ?? "",
    reason: "not_approved",
  });
  const draftFingerprint = JSON.stringify({
    description,
    scope,
    target,
    additionalAtoms,
    duration,
    reason,
  });
  const currentDraft = useRef(draftFingerprint);
  currentDraft.current = draftFingerprint;
  useUnsavedChanges(draftFingerprint !== JSON.stringify(initialDraft.current));
  const [error, setError] = useState<string>();
  const [validating, setValidating] = useState(false);
  const [blockedETag, setBlockedETag] = useState<string>();
  const [confirming, setConfirming] = useState(false);
  const actionButton = useRef<HTMLButtonElement>(null);
  const rejectButton = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  const narrowsServerToTool = submitted.scope === "server" && scope === "tool";
  useEffect(() => {
    const request = new AbortController();
    setApprovalCatalogError(false);
    setApprovalDescriptors(undefined);
    if (!narrowsServerToTool) return () => request.abort();
    void readMatcherDescriptors(
      session,
      detail.resolvedServerID,
      request.signal,
    )
      .then((items) => {
        if (!request.signal.aborted && items !== undefined)
          setApprovalDescriptors(items);
      })
      .catch(() => {
        if (!request.signal.aborted) setApprovalCatalogError(true);
      });
    return () => request.abort();
  }, [detail.resolvedServerID, narrowsServerToTool, session]);

  const decodeMutation = async (response: Response) => {
    if (response.headers.get("Content-Type") !== "application/json")
      throw new Error("invalid response");
    const source = await response.text();
    const value = JSON.parse(source) as unknown;
    const etag = response.headers.get("ETag");
    if (etag === null) throw new Error("invalid response");
    return decodeRequestDetail(value, etag, requestedConstraintSource(source));
  };
  const selectedApprovalDescriptorSummary = approvalDescriptors?.find(
    (descriptor) => descriptor.externalName === target,
  );
  useEffect(() => {
    const request = new AbortController();
    setApprovalDescriptorError(false);
    setSelectedApprovalDescriptor(undefined);
    if (!narrowsServerToTool || selectedApprovalDescriptorSummary === undefined)
      return () => request.abort();
    void readMatcherDescriptor(
      session,
      selectedApprovalDescriptorSummary,
      request.signal,
    )
      .then((item) => {
        if (!request.signal.aborted && item !== undefined)
          setSelectedApprovalDescriptor(item);
      })
      .catch(() => {
        if (!request.signal.aborted) setApprovalDescriptorError(true);
      });
    return () => request.abort();
  }, [
    detail.resolvedServerID,
    narrowsServerToTool,
    selectedApprovalDescriptorSummary,
    session,
  ]);
  const approvalSuggestions: MatcherSchemaSuggestions | undefined =
    narrowsServerToTool
      ? selectedApprovalDescriptor === undefined ||
        selectedApprovalDescriptor.serverID !== detail.resolvedServerID ||
        selectedApprovalDescriptor.externalName !== target ||
        selectedApprovalDescriptor.id !== selectedApprovalDescriptorSummary?.id
        ? undefined
        : matcherSchemaSuggestions(selectedApprovalDescriptor.descriptor)
      : detail.currentTarget.descriptor === null
        ? undefined
        : matcherSchemaSuggestions(detail.currentTarget.descriptor);
  const approvalToolStatus =
    target === ""
      ? "Choose a tool"
      : approvalCatalogError
        ? "Unavailable"
        : approvalDescriptors === undefined
          ? "Loading…"
          : selectedApprovalDescriptorSummary === undefined
            ? "Unknown"
            : "Known";
  const approvalToolNotice = approvalCatalogError
    ? "Catalog unavailable. Manual entry is still available."
    : selectedApprovalDescriptorSummary === undefined
      ? null
      : approvalDescriptorError
        ? "Schema unavailable. Manual constraints are still available."
        : approvalSuggestions === undefined
          ? "Loading schema…"
          : null;
  const additionalConstraintSource = () =>
    additionalAtoms.length === 0 ? "" : matcherConstraintText(additionalAtoms);
  const approvedPolicy = (): Policy => {
    if (
      submitted.scope === "tool" &&
      (scope !== "tool" || target !== submitted.target)
    )
      throw new Error("A tool request cannot change scope or target.");
    if (
      submitted.scope === "server" &&
      scope === "server" &&
      target !== submitted.target
    )
      throw new Error("A server approval cannot broaden to another target.");
    if (target.length === 0) throw new Error("Approval target is required.");
    if (
      submitted.scope === "server" &&
      scope === "tool" &&
      !target.startsWith(`${submitted.target}.`)
    )
      throw new Error("Choose a tool on the requested server.");
    let constraintSource: string;
    try {
      constraintSource = mergeConstraintSource(
        detail.submittedConstraintSource,
        additionalConstraintSource(),
      );
    } catch (caught) {
      if (caught instanceof Error && caught.message.startsWith("Additional "))
        throw caught;
      throw new Error("Constraint must use the supported matcher shape.");
    }
    const parsedConstraint =
      constraintSource === ""
        ? null
        : (JSON.parse(constraintSource) as unknown);
    if (scope === "server" && parsedConstraint !== null)
      throw new Error("Server approval cannot include a constraint.");
    const durationInput = document.getElementById(
      "approval-duration",
    ) as HTMLInputElement;
    if (durationInput !== null && !durationInput.validity.valid)
      throw new Error(
        "Enter a positive whole-number duration and choose its unit.",
      );
    if (duration !== "") {
      if (
        !/^[1-9][0-9]*$/.test(durationAmount) ||
        !/^[1-9][0-9]*$/.test(duration)
      )
        throw new Error(
          "Enter a positive whole-number duration and choose its unit.",
        );
      const seconds = Number(duration);
      if (seconds < 60 || seconds > 2592000)
        throw new Error("Duration must be between 1 minute and 30 days.");
      if (
        submitted.durationSeconds !== null &&
        BigInt(duration) > BigInt(submitted.durationSeconds)
      )
        throw new Error("Approval cannot extend the submitted duration.");
    } else if (submitted.durationSeconds !== null) {
      throw new Error("A temporary request cannot become permanent.");
    }
    return {
      scope,
      target,
      constraint: parsedConstraint,
      durationSeconds: duration === "" ? null : duration,
      futureToolsAcknowledged: scope === "server",
    };
  };
  let constraintDraftError: string | undefined;
  const reviewedConstraint = (() => {
    try {
      return mergeConstraintSource(
        detail.submittedConstraintSource,
        additionalConstraintSource(),
      );
    } catch (caught) {
      constraintDraftError =
        caught instanceof Error ? caught.message : "Invalid conditions";
      return "";
    }
  })();
  const reviewBody = `{"description":${description === "" ? "null" : JSON.stringify(description)},"approved_policy":{"scope":${JSON.stringify(scope)},"target":${JSON.stringify(target)},"constraint":${reviewedConstraint === "" ? "null" : reviewedConstraint},"duration_seconds":${duration === "" ? "null" : JSON.stringify(duration)},"future_tools_acknowledged":${String(scope === "server")}}}`;
  const review = async (next: "approve" | "reject", asRequested = false) => {
    const reviewedDraft = currentDraft.current;
    setMode(next);
    setUnchanged(asRequested);
    setError(undefined);
    try {
      let body: string;
      if (next === "approve") {
        if (
          description.length > 0 &&
          (description.trim() !== description ||
            new TextEncoder().encode(description).length > 256)
        )
          throw new Error(
            "Grant description must be at most 256 bytes without surrounding whitespace.",
          );
        if (containsControlCharacters(description))
          throw new Error(
            "Grant description cannot contain control characters.",
          );
        const policy = asRequested ? submitted : approvedPolicy();
        const constraintSource = mergeConstraintSource(
          detail.submittedConstraintSource,
          asRequested ? "" : additionalConstraintSource(),
        );
        if (constraintSource !== "") {
          setValidating(true);
          const diagnostic = await validateMatcherConstraint(
            session,
            constraintSource,
          );
          if (
            diagnostic === undefined ||
            currentDraft.current !== reviewedDraft
          )
            return;
          if (diagnostic !== null) throw new Error(diagnostic);
        }
        const constraintToken =
          constraintSource === "" ? "null" : constraintSource;
        body = `{"description":${description === "" ? "null" : JSON.stringify(description)},"approved_policy":{"scope":${JSON.stringify(policy.scope)},"target":${JSON.stringify(policy.target)},"constraint":${constraintToken},"duration_seconds":${policy.durationSeconds === null ? "null" : JSON.stringify(policy.durationSeconds)},"future_tools_acknowledged":${String(policy.futureToolsAcknowledged)}}}`;
      } else body = JSON.stringify({ reason });
      const spec: MutationSpec<RequestDetail> = {
        route: `/api/v1/grant-requests/${detail.id}/${next}`,
        method: "POST",
        body,
        precondition: detail.etag,
        requiresPrecondition: true,
        idempotency: "none",
        successStatuses: [200],
        decode: decodeMutation,
      };
      reviewedDraftRef.current = reviewedDraft;
      controller.begin(spec);
      setRejecting(false);
      if (next === "reject") await confirm();
      else setConfirming(true);
    } catch (caught) {
      setError(
        caught instanceof Error ? caught.message : "Invalid adjudication.",
      );
    } finally {
      setValidating(false);
    }
  };
  const confirm = async () => {
    setConfirming(false);
    if (!readAvailable || reviewedDraftRef.current !== currentDraft.current) {
      controller.abandon();
      setError("The draft changed. Review the current draft again.");
      return;
    }
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged") {
      setBlockedETag(undefined);
      initialDraft.current = {
        description,
        scope,
        target,
        additionalAtoms,
        duration,
        reason,
      };
      onAcknowledged(outcome.value);
      controller.abandon();
      onRefresh();
    } else if (outcome.kind === "uncertain") {
      onUncertain();
    } else if (outcome.kind === "rejected" && outcome.requiresRefresh) {
      setBlockedETag(detail.etag);
      onRefresh();
    }
  };
  const cancel = () => {
    setConfirming(false);
    if (mode === "reject") setReason(rejectionBeforeOpen.current);
    controller.abandon();
  };
  const disabled =
    !readAvailable ||
    validating ||
    mutation.state === "submitting" ||
    mutation.state === "uncertain" ||
    mutation.availability === "storage_latched" ||
    blockedETag === detail.etag;
  if (detail.state !== "pending")
    return (
      <StateNotice state="empty" title="Request adjudication is closed">
        <p>Terminal requests cannot be approved or rejected again.</p>
      </StateNotice>
    );
  const confirmationPolicy = unchanged
    ? submitted
    : {
        scope,
        target,
        constraint:
          reviewedConstraint === ""
            ? null
            : (JSON.parse(reviewedConstraint) as unknown),
        durationSeconds: duration === "" ? null : duration,
        futureToolsAcknowledged: scope === "server",
      };
  const confirmationNarrowsToTool =
    submitted.scope === "server" && confirmationPolicy.scope === "tool";
  const confirmationDescriptor =
    selectedApprovalDescriptor?.serverID === detail.resolvedServerID &&
    selectedApprovalDescriptor.externalName === confirmationPolicy.target &&
    selectedApprovalDescriptor.id === selectedApprovalDescriptorSummary?.id
      ? selectedApprovalDescriptor
      : undefined;
  const confirmationSource = unchanged
    ? mergeConstraintSource(detail.submittedConstraintSource, "")
    : reviewedConstraint;
  const isNarrowed =
    !unchanged &&
    (scope !== submitted.scope ||
      target !== submitted.target ||
      additionalAtoms.length > 0 ||
      duration !== (submitted.durationSeconds ?? ""));
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="request-actions-title"
      data-testid="request-actions"
    >
      <div class="panel-heading">
        <div>
          <h2 id="request-actions-title">Choose a decision</h2>
        </div>
      </div>
      <p>
        Approval grants the selected authority; it does not execute or retry a
        call. Rejection records your reason without granting access.
      </p>
      <FormField
        id="approval-description"
        label="Grant description"
        optional
        hint="Display metadata only; does not change authority."
      >
        {(attributes) => (
          <input
            {...attributes}
            data-testid="approval-description"
            value={description}
            maxlength={256}
            disabled={disabled}
            onInput={(event) => setDescription(event.currentTarget.value)}
          />
        )}
      </FormField>
      <div class="form-actions">
        <button
          ref={unchangedButton}
          class="primary-action"
          type="button"
          disabled={disabled}
          onClick={() => void review("approve", true)}
        >
          Approve as requested
        </button>
        <button
          type="button"
          class="secondary"
          disabled={disabled}
          onClick={() => setNarrowing(true)}
        >
          Narrow access
        </button>
        <button
          ref={rejectButton}
          data-testid="request-reject"
          type="button"
          class="danger-action"
          disabled={disabled}
          onClick={() => {
            rejectionBeforeOpen.current = reason;
            setMode("reject");
            setRejecting(true);
          }}
        >
          Reject request
        </button>
      </div>
      {error !== undefined && (
        <StateNotice state="error" title="Check decision">
          <p>{error}</p>
        </StateNotice>
      )}
      {narrowing && (
        <section class="form-section" aria-labelledby="request-approval-title">
          <h3 id="request-approval-title">Narrow access</h3>
          <h3>Tools</h3>
          <FormField id="approval-scope" label="Approved scope">
            {(attributes) => (
              <select
                {...attributes}
                data-testid="approval-scope"
                value={scope}
                disabled={submitted.scope === "tool" || disabled}
                onChange={(event) => {
                  const next = event.currentTarget.value as Scope;
                  setScope(next);
                  if (next === "server") {
                    setTarget(submitted.target);
                    setAdditionalAtoms([]);
                  }
                }}
              >
                <option value="server">Server</option>
                <option value="tool">Exact tool</option>
              </select>
            )}
          </FormField>
          <FormField id="approval-target" label="Approved target">
            {(attributes) =>
              narrowsServerToTool ? (
                <div class="matcher-tool-input">
                  <SuggestionInput
                    attributes={attributes}
                    label="Approved target"
                    testID="approval-target"
                    value={target}
                    options={(approvalDescriptors ?? [])
                      .filter(
                        (descriptor) =>
                          descriptor.serverID === detail.resolvedServerID,
                      )
                      .map((descriptor) => ({
                        value: descriptor.externalName,
                      }))}
                    disabled={disabled}
                    onChange={setTarget}
                  />
                  <MatcherRecognition
                    status={approvalToolStatus}
                    testID="approval-tool-recognition"
                  />
                </div>
              ) : (
                <input
                  {...attributes}
                  data-testid="approval-target"
                  value={target}
                  disabled
                />
              )
            }
          </FormField>
          {narrowsServerToTool && approvalToolNotice && (
            <p
              class="bounded-note"
              role="status"
              data-testid="approval-tool-posture"
            >
              {approvalToolNotice}
            </p>
          )}
          <h3>Conditions</h3>
          {scope === "tool" && (
            <>
              <Conditions source={detail.submittedConstraintSource} locked />
              <div aria-labelledby="approval-additional-matchers-title">
                <h3 id="approval-additional-matchers-title">
                  Additional constraints — All must match
                  <span class="optional-label"> (optional)</span>
                </h3>
                <p class="field-hint">
                  Add rules without changing the submitted policy.
                </p>
                {approvalSuggestions?.unsupported && (
                  <p
                    class="bounded-note"
                    data-testid="approval-matcher-schema-posture"
                  >
                    Some schema fields cannot be suggested. Custom pointers are
                    still available.
                  </p>
                )}
                <MatcherAtomEditor
                  idPrefix="approval-additional"
                  testPrefix="approval-additional"
                  atoms={additionalAtoms}
                  suggestions={approvalSuggestions}
                  schemaState={
                    narrowsServerToTool &&
                    !approvalCatalogError &&
                    (approvalDescriptors === undefined ||
                      (selectedApprovalDescriptorSummary !== undefined &&
                        !approvalDescriptorError &&
                        approvalSuggestions === undefined))
                      ? "loading"
                      : "unavailable"
                  }
                  disabled={disabled}
                  onChange={(next) => {
                    setError(undefined);
                    setAdditionalAtoms(next);
                  }}
                />
              </div>
            </>
          )}
          <h3>Duration</h3>
          <FormField
            id="approval-duration"
            label="Approved duration"
            hint={
              submitted.durationSeconds === null
                ? "Enter a whole number and choose its unit, from 1 minute to 30 days. Leave blank for permanent access."
                : "Enter a whole number and choose its unit, from 1 minute to 30 days. The duration cannot exceed the request; temporary access cannot become permanent."
            }
            optional={submitted.durationSeconds === null}
          >
            {(attributes) => (
              <div class="duration-controls">
                <input
                  {...attributes}
                  type="number"
                  min="1"
                  step="1"
                  data-testid="approval-duration"
                  value={durationAmount}
                  disabled={disabled}
                  onInput={(event) =>
                    setDurationAmount(event.currentTarget.value)
                  }
                />
                <select
                  aria-label="Approved duration unit"
                  data-testid="approval-duration-unit"
                  value={durationUnit}
                  disabled={disabled}
                  onChange={(event) =>
                    setDurationUnit(event.currentTarget.value as DurationUnit)
                  }
                >
                  <option value="minutes">Minutes</option>
                  <option value="hours">Hours</option>
                  <option value="days">Days</option>
                  <option value="seconds">Seconds</option>
                </select>
              </div>
            )}
          </FormField>
          <section class="subpanel" aria-label="Requested versus will approve">
            <h3>Requested versus Will approve</h3>
            <p>Draft only; invalid changes cannot be approved.</p>
            <p>
              Tools: {submitted.target} →{" "}
              {scope === submitted.scope && target === submitted.target
                ? "Unchanged"
                : target}
            </p>
            <p>
              Duration: {readableDuration(submitted.durationSeconds)} →{" "}
              {duration === (submitted.durationSeconds ?? "")
                ? "Unchanged"
                : readableDuration(duration === "" ? null : duration)}
            </p>
            <h4>Requested conditions</h4>
            <Conditions source={detail.submittedConstraintSource} />
            <h4>Will approve conditions</h4>
            {constraintDraftError === undefined ? (
              <Conditions source={reviewedConstraint} />
            ) : (
              <StateNotice
                state="error"
                title="Invalid draft conditions — correct them before review"
              />
            )}
            <p>
              Every condition must match (AND). Submitted conditions remain
              locked.
            </p>
          </section>
          <div class="form-actions">
            <button
              ref={actionButton}
              data-testid="request-approve"
              type="button"
              disabled={disabled}
              onClick={() => void review("approve")}
            >
              {validating ? "Validating matcher…" : "Review approval"}
            </button>
          </div>
        </section>
      )}
      <ConfirmationDialog
        id="request-rejection"
        open={rejecting}
        title="Reject request"
        confirmLabel="Reject request"
        destructive
        returnFocus={rejectButton}
        onCancel={() => {
          setReason(rejectionBeforeOpen.current);
          setRejecting(false);
        }}
        onConfirm={() => void review("reject")}
        consequence={
          <div>
            <p>
              Rejection closes this request and creates no grant. It does not
              revoke existing access or create a DENY.
            </p>
            <FormField id="rejection-reason" label="Rejection reason">
              {(attributes) => (
                <select
                  {...attributes}
                  data-testid="rejection-reason"
                  value={reason}
                  disabled={disabled}
                  onChange={(event) => setReason(event.currentTarget.value)}
                >
                  <option value="not_approved">Not approved</option>
                  <option value="existing_access">Existing access</option>
                  <option value="scope_too_broad">Scope too broad</option>
                  <option value="policy_conflict">Policy conflict</option>
                </select>
              )}
            </FormField>
          </div>
        }
      />
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title}>
          {mutation.requiresRefresh && (
            <p>
              The current request was reloaded. Review its terminal state and
              revision; nothing was replayed.
            </p>
          )}
        </StateNotice>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Adjudication outcome is unknown">
          <p>
            Do not retry or replay. Refresh the request and grant history to
            investigate possible atomic commit.
          </p>
        </StateNotice>
      )}
      <ConfirmationDialog
        id="request-adjudication-confirm"
        open={confirming}
        title={
          mode === "approve"
            ? isNarrowed
              ? "Approve narrowed access?"
              : "Approve as requested?"
            : "Reject request?"
        }
        consequence={
          mode === "approve" ? (
            <div class="review-stack">
              <p>
                Approval atomically closes the request and creates one ALLOW
                grant; it does not execute a call. Every matcher atom is
                required (AND), and any matching DENY takes precedence.
              </p>
              <PolicyWarnings policy={confirmationPolicy} />
              <Conditions source={confirmationSource} />
              <p>
                {confirmationNarrowsToTool
                  ? "Narrowed to one tool — no like-for-like submitted descriptor to compare"
                  : descriptorComparison(detail)}
              </p>
              <p>Temporary access starts at approval, not submission.</p>
              <dl class="fact-grid">
                <div>
                  <dt>Description</dt>
                  <dd>{description === "" ? "None" : description}</dd>
                </div>
                <div>
                  <dt>Principal</dt>
                  <dd>{principalName}</dd>
                </div>
                <div>
                  <dt>Server</dt>
                  <dd>{serverName}</dd>
                </div>
                <div>
                  <dt>Literal tool name</dt>
                  <dd>
                    {confirmationPolicy.scope === "tool"
                      ? confirmationPolicy.target
                      : "All tools"}
                  </dd>
                </div>
                <div>
                  <dt>Catalog posture</dt>
                  <dd>
                    {confirmationPolicy.scope === "server"
                      ? "Not applicable to server-wide authority"
                      : confirmationNarrowsToTool
                        ? approvalCatalogError
                          ? "Unavailable — literal manual name"
                          : confirmationDescriptor === undefined ||
                              approvalDescriptorError
                            ? "No verified current descriptor — literal manual name"
                            : `Current durable descriptor · catalog revision ${confirmationDescriptor.catalogRevision}`
                        : `${detail.currentTarget.activeState ?? "unavailable"} / ${detail.currentTarget.durableState ?? "absent"}`}
                  </dd>
                </div>
                <div>
                  <dt>Approved duration</dt>
                  <dd>
                    {readableDuration(confirmationPolicy.durationSeconds)}
                  </dd>
                </div>
                <div>
                  <dt>Constraint</dt>
                  <dd>
                    {confirmationSource === ""
                      ? "Unrestricted"
                      : "All listed conditions must match"}
                  </dd>
                </div>
              </dl>
              <details>
                <summary>Exact identifiers and serialized policy</summary>
                <p>Principal ID: {detail.principalID}</p>
                <p>Server ID: {detail.resolvedServerID}</p>
                <strong>Read-only serialized policy</strong>
                <textarea
                  class="inert-json matcher-policy-review"
                  aria-label="Read-only serialized policy"
                  data-testid="approval-review-policy"
                  readOnly
                  rows={8}
                  value={
                    unchanged
                      ? `{"description":${JSON.stringify(description === "" ? null : description)},"approved_policy":{"scope":${JSON.stringify(submitted.scope)},"target":${JSON.stringify(submitted.target)},"constraint":${confirmationSource === "" ? "null" : confirmationSource},"duration_seconds":${JSON.stringify(submitted.durationSeconds)},"future_tools_acknowledged":${String(submitted.futureToolsAcknowledged)}}}`
                      : reviewBody
                  }
                />
              </details>
            </div>
          ) : (
            <p>
              Rejection atomically closes the request with reason{" "}
              {sentenceCase(reason)}; it creates no grant, does not revoke
              existing access, and does not create a DENY.
            </p>
          )
        }
        confirmLabel={
          mode === "approve"
            ? isNarrowed
              ? "Approve narrowed access"
              : "Approve as requested"
            : "Reject request"
        }
        destructive={mode === "reject"}
        returnFocus={
          mode === "approve"
            ? unchanged
              ? unchangedButton
              : actionButton
            : rejectButton
        }
        onCancel={cancel}
        onConfirm={() => void confirm()}
      />
    </section>
  );
}

export function Requests({
  session,
  mutations,
  resolved,
  view,
  onRefresh,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
  onRefresh: () => void;
}) {
  const requestID =
    resolved.location.segments.length === 2
      ? resolved.location.segments[1]
      : undefined;
  const [detail, setDetail] = useState<RequestDetail>();
  const [error, setError] = useState<string>();
  const [uncertain, setUncertain] = useState<string>();
  const queue = useRef<ResolvedLocation>({
    location: { destination: "requests", segments: ["requests"], query: {} },
    canonicalFragment: "#/requests",
    invalid: false,
  });
  if (requestID === undefined) queue.current = resolved;
  const navigate = useUnsavedChanges(false);
  const { items, controls } = useCollectionPage(
    session,
    queue.current,
    view,
    (query, cursor, signal) => readQueue(session, query, cursor, signal),
    navigate,
    {
      key: "submitted",
      direction:
        queue.current.location.query.queue === "all"
          ? "descending"
          : "ascending",
    },
  );
  const [identity, setIdentity] = useState<RequestRow>();
  const [nextRequest, setNextRequest] = useState<string>();
  useEffect(() => {
    let current = true;
    setError(undefined);
    if (requestID !== undefined) {
      setDetail((value) => (value?.id === requestID ? value : undefined));
      void readRequest(session, requestID)
        .then((value) => {
          if (current && value !== undefined) setDetail(value);
        })
        .catch((caught: unknown) => {
          if (current)
            setError(
              caught instanceof Error
                ? caught.message
                : "Request data is unavailable.",
            );
        });
      const controller = new AbortController();
      void readQueue(
        session,
        { queue: "all", filter_request: requestID },
        null,
        controller.signal,
      )
        .then((page) => {
          if (current)
            setIdentity(page?.items.find((item) => item.id === requestID));
        })
        .catch(() => {
          if (current) setIdentity(undefined);
        });
      void readQueue(session, {}, null, controller.signal)
        .then((page) => {
          if (current)
            setNextRequest(
              page?.items.find((item) => item.id !== requestID)?.id,
            );
        })
        .catch(() => {
          if (current) setNextRequest(undefined);
        });
    }
    return () => {
      current = false;
    };
  }, [resolved.canonicalFragment, view.generation]);
  if (error !== undefined && (detail === undefined || detail.id !== requestID))
    return (
      <StateNotice state="error" title="Request data unavailable">
        <p>{error}</p>
      </StateNotice>
    );
  if (requestID !== undefined) {
    if (detail === undefined || detail.id !== requestID)
      return <StateNotice state="loading" title="Loading request" />;
    const principalName =
      identity?.id === detail.id
        ? identity.principalName
        : `Principal ${detail.principalID}`;
    const serverName =
      identity?.id === detail.id
        ? identity.serverName
        : `Server ${detail.resolvedServerID}`;
    const drift =
      detail.submittedEvidence !== null &&
      detail.currentTarget.fingerprint !== null &&
      detail.submittedEvidence.fingerprint !== detail.currentTarget.fingerprint;
    return (
      <div
        class="domain-view"
        data-testid="request-detail"
        data-request-id={detail.id}
      >
        <nav class="detail-navigation" aria-label="Request navigation">
          <a href={queue.current.canonicalFragment}>
            Back to{" "}
            {queue.current.location.query.queue === "all" ? "all" : "pending"}{" "}
            requests
          </a>
          {detail.state !== "pending" && nextRequest !== undefined && (
            <a href={`#/requests/${nextRequest}`}>Review next</a>
          )}
          {detail.state !== "pending" && nextRequest === undefined && (
            <span>
              No next pending request available. Return to the queue to refresh.
            </span>
          )}
        </nav>
        <header class="detail-context" data-testid="detail-context">
          <div class="detail-context-heading">
            <h1 id="request-page-title" tabindex={-1}>
              {detail.state === "pending"
                ? "Review request"
                : "Request decision"}
            </h1>
          </div>
        </header>
        <section class="panel domain-panel" aria-labelledby="request-title">
          <div class="panel-heading">
            <h2 id="request-title">Request details</h2>
            <StatusLabel
              state={detail.state === "pending" ? "warning" : "current"}
            >
              {sentenceCase(detail.state)}
            </StatusLabel>
          </div>
          <dl class="fact-grid">
            <div>
              <dt>Principal</dt>
              <dd>
                <a href={`#/principals/${detail.principalID}`}>
                  {principalName}
                </a>
              </dd>
            </div>
            <div>
              <dt>Submitted</dt>
              <dd>
                <UserTime value={detail.createdAt} />
              </dd>
            </div>
            <div>
              <dt>Requested target</dt>
              <dd>
                <a href={`#/servers/${detail.resolvedServerID}?tab=tools`}>
                  {serverName}
                </a>
              </dd>
            </div>
            <div>
              <dt>Tools</dt>
              <dd>{detail.resolvedUpstreamName ?? "All tools"}</dd>
            </div>
          </dl>
          <p>
            {sentenceCase(detail.requestedPolicy.scope)} scope ·{" "}
            {readableDuration(detail.requestedPolicy.durationSeconds)}
            {detail.requestedPolicy.durationSeconds !== null &&
              " starting at approval"}
          </p>
          <h3>Requested conditions</h3>
          <Conditions source={detail.submittedConstraintSource} />
          <PolicyWarnings policy={detail.requestedPolicy} />
          <p>{descriptorComparison(detail)}</p>
          <p>
            Target: {detail.currentTarget.targetState}; active descriptor:{" "}
            {detail.currentTarget.activeState ?? "not applicable"}; retained
            descriptor: {detail.currentTarget.durableState ?? "not applicable"}.
          </p>
          {error !== undefined && (
            <StateNotice state="error" title="Request refresh failed">
              <p>
                {error} Displayed evidence may be stale; refresh before
                deciding.
              </p>
            </StateNotice>
          )}
          {uncertain === detail.id && (
            <StateNotice
              state="warning"
              title="Adjudication outcome is unknown"
            >
              <p>
                Do not resubmit. Inspect refreshed request and grant history; no
                success or rollback was confirmed.
              </p>
            </StateNotice>
          )}
        </section>
        {detail.state === "pending" && uncertain !== detail.id && (
          <RequestActions
            key={`${detail.id}:${detail.etag}`}
            session={session}
            mutations={mutations}
            detail={detail}
            onRefresh={onRefresh}
            onAcknowledged={setDetail}
            onUncertain={() => setUncertain(detail.id)}
            principalName={principalName}
            serverName={serverName}
            readAvailable={error === undefined}
          />
        )}
        <details class="panel domain-panel">
          <summary>Technical identifiers and immutable evidence</summary>
          <dl class="fact-grid">
            <div>
              <dt>Request ID</dt>
              <dd>{detail.id}</dd>
            </div>
            <div>
              <dt>Principal ID</dt>
              <dd>{detail.principalID}</dd>
            </div>
            <div>
              <dt>Server ID</dt>
              <dd>{detail.resolvedServerID}</dd>
            </div>
            <div>
              <dt>Revision / ETag</dt>
              <dd>{detail.etag}</dd>
            </div>
          </dl>
          <p>
            Tool descriptions and schemas are untrusted inert evidence, not
            instructions or proof of callable authority.
          </p>
          <section
            class="panel domain-panel"
            aria-labelledby="submitted-policy-title"
          >
            <h2 id="submitted-policy-title">
              Submitted policy and evidence — immutable
            </h2>
            {policyFacts(detail.requestedPolicy)}
            {detail.requestedPolicy.constraint !== null && (
              <InertJSON
                value={detail.requestedPolicy.constraint}
                label="Submitted constraint"
              />
            )}
            <Evidence evidence={detail.submittedEvidence} label="Submitted" />
          </section>
          <section
            class="panel domain-panel"
            aria-labelledby="current-target-title"
          >
            <h2 id="current-target-title">
              Current target comparison — read-time
            </h2>
            <p>
              Current comparison does not rewrite immutable submitted evidence
              or the request revision.
            </p>
            <dl class="fact-grid">
              <div>
                <dt>Target</dt>
                <dd>{sentenceCase(detail.currentTarget.targetState)}</dd>
              </div>
              <div>
                <dt>Active descriptor</dt>
                <dd>
                  {detail.currentTarget.activeState === null
                    ? "Not applicable"
                    : sentenceCase(detail.currentTarget.activeState)}
                </dd>
              </div>
              <div>
                <dt>Durable descriptor</dt>
                <dd>
                  {detail.currentTarget.durableState === null
                    ? "Not applicable"
                    : sentenceCase(detail.currentTarget.durableState)}
                </dd>
              </div>
              <div>
                <dt>Current fingerprint</dt>
                <dd>{detail.currentTarget.fingerprint ?? "Absent"}</dd>
              </div>
            </dl>
            {drift && (
              <StateNotice
                state="warning"
                title="Descriptor fingerprint changed"
              >
                <p>
                  The current descriptor differs from submitted evidence. Active
                  current does not mean it matches the submission.
                </p>
              </StateNotice>
            )}
            {detail.currentTarget.durableState === "retired" && (
              <StateNotice
                state="warning"
                title="Current comparison is retired historical evidence"
              >
                <p>Retained evidence is not callable authority.</p>
              </StateNotice>
            )}
            {detail.currentTarget.durableState === "absent" && (
              <StateNotice
                state="unavailable"
                title="Current descriptor is absent"
              />
            )}
            {detail.currentTarget.descriptor !== null && (
              <InertJSON
                value={detail.currentTarget.descriptor}
                label="Current normalized descriptor"
              />
            )}
          </section>
        </details>
        {detail.state !== "pending" && (
          <section
            class="panel domain-panel"
            aria-labelledby="approved-policy-title"
          >
            <h2 id="approved-policy-title">
              {sentenceCase(detail.state)} decision
            </h2>
            <StateNotice state="empty" title="Request adjudication is closed">
              <p>Terminal requests cannot be approved or rejected again.</p>
            </StateNotice>
            {detail.approvedPolicy === null ? (
              <p>
                {detail.state === "cancelled"
                  ? "The requesting principal cancelled this request."
                  : "This request was rejected."}{" "}
                This decision created no grant and did not revoke existing
                access or create a DENY.
              </p>
            ) : (
              <>
                <section
                  class="subpanel"
                  aria-label="Requested versus approved"
                >
                  <h3>Requested versus Approved</h3>
                  {policyComparisonFacts(
                    detail.requestedPolicy,
                    detail.approvedPolicy,
                  )}
                  <h4>Requested conditions</h4>
                  <Conditions source={detail.submittedConstraintSource} />
                  <h4>Approved conditions</h4>
                  <Conditions
                    source={
                      detail.approvedPolicy.constraint === null
                        ? null
                        : JSON.stringify(detail.approvedPolicy.constraint)
                    }
                  />
                </section>
                <details>
                  <summary>Approved serialized policy</summary>
                  <InertJSON
                    value={detail.approvedPolicy}
                    label="Approved policy"
                  />
                </details>
              </>
            )}
            {detail.state === "approved" && (
              <p>
                Approval created one ordinary ALLOW but never executed or
                resumed a call. An explicit fresh call is required after
                approval.
              </p>
            )}
            {detail.approvedEvidence !== null && (
              <details>
                <summary>Approved descriptor evidence</summary>
                <Evidence evidence={detail.approvedEvidence} label="Approved" />
              </details>
            )}
            {detail.approvedGrantID !== null && (
              <p>
                <a href={`#/grants/${detail.approvedGrantID}`}>
                  Grant {detail.approvedGrantID}
                </a>
                . This historical link does not prove the grant still exists, is
                active, or currently authorizes calls.
              </p>
            )}
            {detail.rejectionReason !== null && (
              <p>Closed rejection reason: {detail.rejectionReason}</p>
            )}
          </section>
        )}
      </div>
    );
  }
  const allRequests = resolved.location.query.queue === "all";
  return (
    <div class="domain-view" data-testid="requests-view">
      <section class="panel domain-panel" aria-label="Grant requests">
        <nav class="subnav" aria-label="Request queues">
          <a href="#/requests" aria-current={!allRequests ? "page" : undefined}>
            Pending
          </a>
          <a
            href="#/requests?queue=all"
            aria-current={allRequests ? "page" : undefined}
          >
            All requests
          </a>
        </nav>
        <h2>{allRequests ? "All requests" : "Pending requests"}</h2>
        <CollectionTable
          caption="Grant request summaries"
          items={items}
          rowKey={(item) => item.id}
          rowTestID="request-row"
          emptyTitle="No requests match"
          remote={controls}
          itemNames={{ singular: "request", plural: "requests" }}
          filters={[
            {
              key: "request",
              label: "Request ID",
              type: "text",
              value: () => "",
              literalValues: (item) => [item.id],
            },
            {
              key: "principal",
              label: "Principal",
              type: "text",
              value: (item) => item.principalName,
              literalValues: (item) => [item.principalID],
            },
            {
              key: "target",
              label: "Target",
              type: "text",
              value: (item) => item.requestedPolicy.target,
            },
            {
              key: "scope",
              label: "Scope",
              type: "select",
              value: (item) => item.requestedPolicy.scope,
              options: [
                { value: "tool", label: "Tool" },
                { value: "server", label: "Server" },
              ],
            },
            ...(allRequests
              ? [
                  {
                    key: "state",
                    label: "State",
                    type: "select" as const,
                    value: (item: RequestRow) => item.state,
                    options: [
                      { value: "pending", label: "Pending" },
                      { value: "approved", label: "Approved" },
                      { value: "rejected", label: "Rejected" },
                      { value: "cancelled", label: "Cancelled" },
                    ],
                  },
                ]
              : []),
          ]}
          columns={[
            {
              key: "request",
              label: "Decision",
              render: (item) => (
                <div>
                  <a href={`#/requests/${item.id}`}>
                    {item.state === "pending" ? "Review" : "View decision"}
                  </a>
                  <details>
                    <summary>Request ID</summary>
                    <span class="technical-value">{item.id}</span>
                  </details>
                </div>
              ),
              sortValue: (item) => item.id,
            },
            {
              key: "principal",
              label: "Principal",
              render: (item) => (
                <a href={`#/principals/${item.principalID}`}>
                  {item.principalName}
                </a>
              ),
              sortValue: (item) => item.principalName,
            },
            {
              key: "target",
              label: "Target",
              render: (item) => (
                <a
                  href={`#/servers/${item.serverID}?tab=tools`}
                  title={item.requestedPolicy.target}
                >
                  {item.serverName} · {item.upstreamName ?? "All tools"}
                </a>
              ),
              sortValue: (item) => item.requestedPolicy.target,
            },
            {
              key: "state",
              label: "State",
              render: (item) => (
                <StatusLabel
                  state={item.state === "pending" ? "warning" : "current"}
                >
                  {sentenceCase(item.state)}
                </StatusLabel>
              ),
              sortValue: (item) => item.state,
            },
            {
              key: "access",
              label: "Access requested",
              render: (item) => {
                const constraint = item.requestedPolicy.constraint;
                const count =
                  constraint === null
                    ? 0
                    : (() => {
                        const shape = matcherShape(constraint);
                        return (
                          Object.keys(shape.equals).length +
                          Object.keys(shape.regex).length
                        );
                      })();
                return `${readableDuration(item.requestedPolicy.durationSeconds)} · ${count === 0 ? "Unrestricted" : `${count} condition${count === 1 ? "" : "s"}`}`;
              },
            },
            {
              key: "submitted",
              label: "Submitted",
              render: (item) => <UserTime value={item.createdAt} />,
              sortValue: (item) => item.createdAt,
            },
          ]}
        />
      </section>
    </div>
  );
}
