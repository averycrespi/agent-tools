import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import { validateMatcherConstraint } from "./matcher-validation";
import type { PrincipalDirectory } from "./principals";
import { useUnsavedChanges } from "./navigation";
import {
  type MutationController,
  type MutationCoordinator,
  type MutationSnapshot,
  type MutationSpec,
} from "./mutation";
import {
  BinaryToggle,
  CollectionTable,
  ConfirmationDialog,
  containsControlCharacters,
  FormField,
  InertJSON,
  sentenceCase,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { ProtectedContext, SessionClient } from "./session";
import { UserTime } from "./time";
import type { ViewSnapshot } from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
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
async function requestJSON(
  session: SessionClient,
  route: string,
): Promise<{ response: Response; value: unknown } | undefined> {
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
    return { response, value: (await response.json()) as unknown };
  });
}
interface RequestPage {
  items: RequestSummary[];
  nextCursor: string | null;
  restarted: boolean;
}
async function readRequestPage(
  session: SessionClient,
  requestedCursor: string | null,
): Promise<RequestPage> {
  let cursor = requestedCursor;
  let restarted = false;
  for (;;) {
    const params = new URLSearchParams({ limit: "50" });
    if (cursor !== null) params.set("cursor", cursor);
    const result = await requestJSON(
      session,
      `/api/v1/grant-requests?${params}`,
    );
    if (result === undefined) return { items: [], nextCursor: null, restarted };
    if (result.response.status === 409 && cursor !== null && !restarted) {
      cursor = null;
      restarted = true;
      continue;
    }
    if (!result.response.ok) throw new Error("Request data is unavailable.");
    const page = record(result.value, ["items", "next_cursor"]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    const nextCursor = nullableText(page.next_cursor);
    if (
      nextCursor !== null &&
      (nextCursor.length === 0 || nextCursor.length > 4096)
    )
      throw new Error("invalid response");
    return {
      items: page.items.map(decodeSummary),
      nextCursor,
      restarted,
    };
  }
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
  return decodeRequestDetail(result.value, etag);
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
        <dd>
          {policy.durationSeconds === null
            ? "Permanent"
            : `${policy.durationSeconds} seconds`}
        </dd>
      </div>
      <div>
        <dt>Future tools acknowledged</dt>
        <dd>{policy.futureToolsAcknowledged ? "Yes" : "No"}</dd>
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

function constraintRetained(
  submitted: unknown | null,
  approved: unknown | null,
): boolean {
  if (submitted === null) return true;
  if (approved === null) return false;
  try {
    const left = matcherShape(submitted);
    const right = matcherShape(approved);
    if (left.version === 2 && right.version !== 2) return false;
    return (["equals", "regex"] as const).every((operator) =>
      Object.entries(left[operator]).every(
        ([pointer, value]) =>
          Object.hasOwn(right[operator], pointer) &&
          JSON.stringify(right[operator][pointer]) === JSON.stringify(value),
      ),
    );
  } catch {
    return false;
  }
}

function RequestActions({
  session,
  mutations,
  detail,
  onRefresh,
  onAcknowledged,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  detail: RequestDetail;
  onRefresh: () => void;
  onAcknowledged: (detail: RequestDetail) => void;
}) {
  const submitted = detail.requestedPolicy;
  const [controller] = useState<MutationController<RequestDetail>>(() =>
    mutations.create<RequestDetail>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [mode, setMode] = useState<"approve" | "reject">("approve");
  const defaultDescription = "";
  const [description, setDescription] = useState(defaultDescription);
  const [scope, setScope] = useState<Scope>(submitted.scope);
  const [target, setTarget] = useState(submitted.target);
  const [constraint, setConstraint] = useState(
    submitted.constraint === null ? "" : JSON.stringify(submitted.constraint),
  );
  const [duration, setDuration] = useState(submitted.durationSeconds ?? "");
  const [reason, setReason] = useState("not_approved");
  const initialDraft = useRef({
    description: defaultDescription,
    scope: submitted.scope,
    target: submitted.target,
    constraint:
      submitted.constraint === null ? "" : JSON.stringify(submitted.constraint),
    duration: submitted.durationSeconds ?? "",
    reason: "not_approved",
  });
  const draftFingerprint = JSON.stringify({
    description,
    scope,
    target,
    constraint,
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
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);

  const decodeMutation = async (response: Response) => {
    if (response.headers.get("Content-Type") !== "application/json")
      throw new Error("invalid response");
    const value = (await response.json()) as unknown;
    const etag = response.headers.get("ETag");
    if (etag === null) throw new Error("invalid response");
    return decodeRequestDetail(value, etag);
  };
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
    let parsedConstraint: unknown | null = null;
    if (constraint !== "") {
      try {
        parsedConstraint = JSON.parse(constraint) as unknown;
        matcherShape(parsedConstraint);
      } catch {
        throw new Error("Constraint must use the supported matcher shape.");
      }
    }
    if (scope === "server" && parsedConstraint !== null)
      throw new Error("Server approval cannot include a constraint.");
    if (!constraintRetained(submitted.constraint, parsedConstraint))
      throw new Error(
        "Approval cannot remove or change a submitted constraint atom.",
      );
    if (duration !== "") {
      if (!/^(?:[1-9][0-9]*)$/.test(duration))
        throw new Error("Duration must be canonical seconds.");
      const seconds = Number(duration);
      if (seconds < 60 || seconds > 2592000)
        throw new Error("Duration must be between 60 and 2592000 seconds.");
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
  const review = async (next: "approve" | "reject") => {
    const reviewedDraft = currentDraft.current;
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
        const policy = approvedPolicy();
        if (constraint !== "") {
          setValidating(true);
          const diagnostic = await validateMatcherConstraint(
            session,
            constraint,
          );
          if (
            diagnostic === undefined ||
            currentDraft.current !== reviewedDraft
          )
            return;
          if (diagnostic !== null) throw new Error(diagnostic);
        }
        const constraintToken = constraint === "" ? "null" : constraint;
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
      setMode(next);
      controller.begin(spec);
      setConfirming(true);
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
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged") {
      setBlockedETag(undefined);
      initialDraft.current = {
        description,
        scope,
        target,
        constraint,
        duration,
        reason,
      };
      onAcknowledged(outcome.value);
      controller.abandon();
      onRefresh();
    } else if (outcome.kind === "rejected" && outcome.requiresRefresh) {
      setBlockedETag(detail.etag);
      onRefresh();
    }
  };
  const cancel = () => {
    setConfirming(false);
    controller.abandon();
  };
  const disabled =
    validating ||
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched" ||
    blockedETag === detail.etag;
  if (detail.state !== "pending")
    return (
      <StateNotice state="empty" title="Request adjudication is closed">
        <p>Terminal requests cannot be approved or rejected again.</p>
      </StateNotice>
    );
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="request-actions-title"
      data-testid="request-actions"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">ADJUDICATION</span>
          <h2 id="request-actions-title">Approve a narrowing or reject</h2>
        </div>
      </div>
      <p>
        Approval creates one ordinary ALLOW only. It never resumes, retries, or
        executes a held call. Rejection records one closed reason.
      </p>
      <FormField
        id="approval-description"
        label="Grant description"
        hint="Display metadata; it does not change authorization policy."
        optional
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
                setConstraint("");
              }
            }}
          >
            <option value="server">Server</option>
            <option value="tool">Exact tool</option>
          </select>
        )}
      </FormField>
      <FormField id="approval-target" label="Approved target">
        {(attributes) => (
          <input
            {...attributes}
            data-testid="approval-target"
            value={target}
            disabled={
              submitted.scope === "tool" ||
              (scope === "server" && submitted.scope === "server") ||
              disabled
            }
            onInput={(event) => setTarget(event.currentTarget.value)}
          />
        )}
      </FormField>
      {scope === "tool" && (
        <FormField
          id="approval-constraint"
          label="Approved matcher constraint JSON"
          hint="Submitted equality and regex atoms must remain exact; additional atoms narrow the policy."
          optional
        >
          {(attributes) => (
            <textarea
              {...attributes}
              data-testid="approval-constraint"
              value={constraint}
              disabled={disabled}
              onInput={(event) => {
                setError(undefined);
                setConstraint(event.currentTarget.value);
              }}
            />
          )}
        </FormField>
      )}
      <FormField
        id="approval-duration"
        label="Approved duration seconds"
        hint="Blank means permanent only when the submitted request was permanent."
      >
        {(attributes) => (
          <input
            {...attributes}
            data-testid="approval-duration"
            value={duration}
            disabled={disabled}
            onInput={(event) => setDuration(event.currentTarget.value)}
          />
        )}
      </FormField>
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
      {error !== undefined && (
        <StateNotice state="error" title="Check adjudication">
          <p>{error}</p>
        </StateNotice>
      )}
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
      <div class="inline-actions">
        <button
          ref={actionButton}
          data-testid="request-approve"
          type="button"
          disabled={disabled}
          onClick={() => void review("approve")}
        >
          {validating ? "Validating matcher…" : "Review approval"}
        </button>
        <button
          data-testid="request-reject"
          class="danger-action"
          type="button"
          disabled={disabled}
          onClick={() => void review("reject")}
        >
          Review rejection
        </button>
      </div>
      <ConfirmationDialog
        id="request-adjudication-confirm"
        open={confirming}
        title={
          mode === "approve" ? "Approve narrowed policy?" : "Reject request?"
        }
        consequence={
          <p>
            {mode === "approve"
              ? "Approval atomically closes the request and creates one ALLOW grant; it does not execute a call."
              : `Rejection atomically closes the request with reason ${reason}; it creates no grant.`}
          </p>
        }
        confirmLabel={mode === "approve" ? "Approve request" : "Reject request"}
        destructive={mode === "reject"}
        returnFocus={actionButton}
        onCancel={cancel}
        onConfirm={() => void confirm()}
      />
    </section>
  );
}

export function Requests({
  session,
  principals,
  mutations,
  resolved,
  view,
  onRefresh,
}: {
  session: SessionClient;
  principals: PrincipalDirectory;
  mutations: MutationCoordinator;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
  onRefresh: () => void;
}) {
  const requestID =
    resolved.location.segments.length === 2
      ? resolved.location.segments[1]
      : undefined;
  const [items, setItems] = useState<RequestSummary[]>();
  const [detail, setDetail] = useState<RequestDetail>();
  const [error, setError] = useState<string>();
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const loadPending = useRef(false);
  const [live, setLive] = useState(true);
  const [updatesAvailable, setUpdatesAvailable] = useState(false);
  const liveRef = useRef(true);
  const [principalNames, setPrincipalNames] = useState(principals.snapshot());
  useEffect(() => principals.subscribe(setPrincipalNames), [principals]);
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
    } else if (!liveRef.current) {
      setUpdatesAvailable(true);
    } else {
      void readRequestPage(session, null)
        .then((page) => {
          if (current) {
            setItems(page.items);
            setNextCursor(page.nextCursor);
            setUpdatesAvailable(false);
          }
        })
        .catch((caught: unknown) => {
          if (current)
            setError(
              caught instanceof Error
                ? caught.message
                : "Request data is unavailable.",
            );
        });
    }
    return () => {
      current = false;
    };
  }, [resolved.canonicalFragment, view.generation]);
  if (error !== undefined)
    return (
      <StateNotice state="error" title="Request data unavailable">
        <p>{error}</p>
      </StateNotice>
    );
  if (requestID !== undefined) {
    if (detail === undefined)
      return <StateNotice state="loading" title="Loading request" />;
    const drift =
      detail.submittedEvidence !== null &&
      detail.currentTarget.fingerprint !== null &&
      detail.submittedEvidence.fingerprint !== detail.currentTarget.fingerprint;
    return (
      <div class="domain-view" data-testid="request-detail">
        <nav class="detail-navigation" aria-label="Request navigation">
          <a href="#/requests">Back to requests</a>
        </nav>
        <header class="detail-context" data-testid="detail-context">
          <div class="detail-context-heading">
            <h1 id="request-page-title" tabindex={-1}>
              Request {detail.id}
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
                  {principalNames.get(detail.principalID) ?? detail.principalID}
                </a>
              </dd>
            </div>
            <div>
              <dt>Revision / ETag</dt>
              <dd>{detail.revision}</dd>
            </div>
            <div>
              <dt>Resolved server</dt>
              <dd>
                <a href={`#/servers/${detail.resolvedServerID}?tab=tools`}>
                  Server {detail.resolvedServerID}
                </a>
              </dd>
            </div>
            <div>
              <dt>Resolved upstream tool name</dt>
              <dd>{detail.resolvedUpstreamName ?? "Server scope"}</dd>
            </div>
          </dl>
        </section>
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
            Current comparison does not rewrite immutable submitted evidence or
            the request revision.
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
            <StateNotice state="warning" title="Descriptor fingerprint changed">
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
        <section
          class="panel domain-panel"
          aria-labelledby="approved-policy-title"
        >
          <h2 id="approved-policy-title">Proposed approved policy</h2>
          {detail.approvedPolicy === null ? (
            <StateNotice state="empty" title="No approved policy" />
          ) : (
            <>
              {policyFacts(detail.approvedPolicy)}
              {detail.approvedPolicy.constraint !== null && (
                <InertJSON
                  value={detail.approvedPolicy.constraint}
                  label="Approved constraint"
                />
              )}
            </>
          )}
          <p>
            Approval creates one ordinary ALLOW but never executes or resumes
            the motivating call. An explicit fresh call is required after
            approval.
          </p>
          <Evidence evidence={detail.approvedEvidence} label="Approved" />
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
        <RequestActions
          session={session}
          mutations={mutations}
          detail={detail}
          onRefresh={onRefresh}
          onAcknowledged={setDetail}
        />
      </div>
    );
  }
  if (items === undefined)
    return <StateNotice state="loading" title="Loading requests" />;
  const loadMore = async () => {
    if (loadPending.current || nextCursor === null) return;
    loadPending.current = true;
    setLoadingMore(true);
    try {
      const page = await readRequestPage(session, nextCursor);
      setItems((current) =>
        page.restarted ? page.items : [...(current ?? []), ...page.items],
      );
      setNextCursor(page.nextCursor);
    } catch (caught: unknown) {
      setError(
        caught instanceof Error
          ? caught.message
          : "Request data is unavailable.",
      );
    } finally {
      loadPending.current = false;
      setLoadingMore(false);
    }
  };
  const changeLive = (next: boolean) => {
    liveRef.current = next;
    setLive(next);
    setUpdatesAvailable(false);
    if (next) onRefresh();
  };
  return (
    <div class="domain-view" data-testid="requests-view">
      <section class="panel domain-panel" aria-label="Grant requests">
        <div class="collection-toolbar live-collection-toolbar">
          <label for="request-live-mode">Live mode</label>
          <BinaryToggle
            attributes={{ id: "request-live-mode" }}
            checked={live}
            showState={false}
            onChange={changeLive}
          />
          {updatesAvailable && (
            <StatusLabel state="warning">Updates available</StatusLabel>
          )}
        </div>
        <CollectionTable
          caption="Grant request summaries"
          items={items}
          rowKey={(item) => item.id}
          rowTestID="request-row"
          emptyTitle="No requests match"
          initialSort={{ key: "submitted", direction: "descending" }}
          hasMore={nextCursor !== null}
          loadingMore={loadingMore}
          onLoadMore={() => void loadMore()}
          loadMoreLabel="Load older requests"
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
              value: (item) => principalNames.get(item.principalID) ?? "",
              literalValues: (item) => [item.principalID],
            },
            {
              key: "target",
              label: "Requested target",
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
            {
              key: "state",
              label: "State",
              type: "select",
              value: (item) => item.state,
              options: [
                { value: "pending", label: "Pending" },
                { value: "approved", label: "Approved" },
                { value: "rejected", label: "Rejected" },
                { value: "cancelled", label: "Cancelled" },
              ],
            },
          ]}
          columns={[
            {
              key: "request",
              label: "Request ID",
              render: (item) => <a href={`#/requests/${item.id}`}>{item.id}</a>,
              sortValue: (item) => item.id,
            },
            {
              key: "principal",
              label: "Principal",
              render: (item) => (
                <a href={`#/principals/${item.principalID}`}>
                  {principalNames.get(item.principalID) ?? item.principalID}
                </a>
              ),
              sortValue: (item) =>
                principalNames.get(item.principalID) ?? item.principalID,
            },
            {
              key: "target",
              label: "Requested target",
              render: (item) =>
                `${sentenceCase(item.requestedPolicy.scope)}: ${item.requestedPolicy.target}`,
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
