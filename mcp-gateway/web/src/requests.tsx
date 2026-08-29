import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import {
  type MutationController,
  type MutationCoordinator,
  type MutationSnapshot,
  type MutationSpec,
} from "./mutation";
import {
  ComparisonTable,
  ConfirmationDialog,
  FormField,
  InertJSON,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { ProtectedContext, SessionClient } from "./session";
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
async function readRequests(
  session: SessionClient,
  query: Readonly<Record<string, string>>,
): Promise<RequestSummary[]> {
  const items: RequestSummary[] = [];
  let cursor: string | null = null;
  let restarted = false;
  const state = query.state ?? "pending";
  for (;;) {
    const params = new URLSearchParams({ limit: "50", state });
    if (query.principal_id !== undefined)
      params.set("principal_id", query.principal_id);
    if (cursor !== null) params.set("cursor", cursor);
    const result = await requestJSON(
      session,
      `/api/v1/grant-requests?${params}`,
    );
    if (result === undefined) return [];
    if (result.response.status === 409 && cursor !== null && !restarted) {
      items.length = 0;
      cursor = null;
      restarted = true;
      continue;
    }
    if (!result.response.ok) throw new Error("Request data is unavailable.");
    const page = record(result.value, ["items", "next_cursor"]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    items.push(...page.items.map(decodeSummary));
    if (page.next_cursor === null) return items;
    cursor = text(page.next_cursor);
    if (cursor.length === 0 || cursor.length > 4096)
      throw new Error("invalid response");
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
        <dd>{policy.scope}</dd>
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
          <dd>{evidence.capturedAt}</dd>
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

function constraintRetained(
  submitted: unknown | null,
  approved: unknown | null,
): boolean {
  if (submitted === null) return true;
  if (approved === null) return false;
  try {
    const left = record(submitted, ["equals"]);
    const right = record(approved, ["equals"]);
    const submittedEquals = scalarObject(left.equals) as JSONRecord;
    const approvedEquals = scalarObject(right.equals) as JSONRecord;
    return Object.entries(submittedEquals).every(
      ([pointer, value]) =>
        Object.hasOwn(approvedEquals, pointer) &&
        JSON.stringify(approvedEquals[pointer]) === JSON.stringify(value),
    );
  } catch {
    return false;
  }
}

function RequestActions({
  mutations,
  detail,
  onRefresh,
  onAcknowledged,
}: {
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
  const [scope, setScope] = useState<Scope>(submitted.scope);
  const [target, setTarget] = useState(submitted.target);
  const [constraint, setConstraint] = useState(
    submitted.constraint === null ? "" : JSON.stringify(submitted.constraint),
  );
  const [duration, setDuration] = useState(submitted.durationSeconds ?? "");
  const [reason, setReason] = useState("not_approved");
  const [error, setError] = useState<string>();
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
      } catch {
        throw new Error("Constraint must be valid JSON.");
      }
      scalarObject(parsedConstraint);
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
  const review = (next: "approve" | "reject") => {
    setError(undefined);
    try {
      const body =
        next === "approve"
          ? (() => {
              const policy = approvedPolicy();
              return JSON.stringify({
                approved_policy: {
                  scope: policy.scope,
                  target: policy.target,
                  constraint: policy.constraint,
                  duration_seconds: policy.durationSeconds,
                  future_tools_acknowledged: policy.futureToolsAcknowledged,
                },
              });
            })()
          : JSON.stringify({ reason });
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
    }
  };
  const confirm = async () => {
    setConfirming(false);
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged") {
      setBlockedETag(undefined);
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
          label="Approved equality constraint JSON (optional)"
          hint="Submitted atoms must remain exact; additional atoms narrow the policy."
        >
          {(attributes) => (
            <textarea
              {...attributes}
              data-testid="approval-constraint"
              value={constraint}
              disabled={disabled}
              onInput={(event) => setConstraint(event.currentTarget.value)}
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
          onClick={() => review("approve")}
        >
          Review approval
        </button>
        <button
          data-testid="request-reject"
          class="danger-action"
          type="button"
          disabled={disabled}
          onClick={() => review("reject")}
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
  const [items, setItems] = useState<RequestSummary[]>();
  const [detail, setDetail] = useState<RequestDetail>();
  const [error, setError] = useState<string>();
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
    } else {
      setItems(undefined);
      void readRequests(session, resolved.location.query)
        .then((value) => {
          if (current) setItems(value);
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
        <section class="panel domain-panel" aria-labelledby="request-title">
          <div class="panel-heading">
            <div>
              <span class="panel-code">REQUEST EVIDENCE</span>
              <h2 id="request-title">Request {detail.id}</h2>
            </div>
            <StatusLabel
              state={detail.state === "pending" ? "warning" : "current"}
            >
              {detail.state}
            </StatusLabel>
          </div>
          <dl class="fact-grid">
            <div>
              <dt>Principal</dt>
              <dd>
                <a href={`#/access/principals/${detail.principalID}`}>
                  {detail.principalID}
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
                <a href={`#/servers/${detail.resolvedServerID}`}>
                  {detail.resolvedServerID}
                </a>
              </dd>
            </div>
            <div>
              <dt>Resolved upstream name</dt>
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
              <dd>{detail.currentTarget.targetState}</dd>
            </div>
            <div>
              <dt>Active descriptor</dt>
              <dd>{detail.currentTarget.activeState ?? "Not applicable"}</dd>
            </div>
            <div>
              <dt>Durable descriptor</dt>
              <dd>{detail.currentTarget.durableState ?? "Not applicable"}</dd>
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
              <a href={`#/access/grants/${detail.approvedGrantID}`}>
                Open historically created grant
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
  const state = resolved.location.query.state ?? "pending";
  const principal = resolved.location.query.principal_id;
  return (
    <div class="domain-view" data-testid="requests-view">
      <section class="panel domain-panel" aria-labelledby="requests-title">
        <div class="panel-heading">
          <div>
            <span class="panel-code">REVIEW QUEUE</span>
            <h2 id="requests-title">Grant requests</h2>
          </div>
          <StatusLabel state="current">{state} filter</StatusLabel>
        </div>
        <p>
          Rows are summary-only. Open one request to retrieve immutable evidence
          and a separate current target comparison.
        </p>
        <nav class="inline-actions" aria-label="Request state filter">
          {(["pending", "approved", "rejected", "cancelled"] as const).map(
            (value) => (
              <a
                aria-current={state === value ? "page" : undefined}
                href={`#/requests?${new URLSearchParams({ ...(principal === undefined ? {} : { principal_id: principal }), state: value })}`}
              >
                {value}
              </a>
            ),
          )}
        </nav>
        {items.length === 0 ? (
          <StateNotice state="empty" title="No requests match" />
        ) : (
          <ComparisonTable caption="Grant request summaries">
            <thead>
              <tr>
                <th scope="col">Request</th>
                <th scope="col">Principal</th>
                <th scope="col">Requested target</th>
                <th scope="col">State</th>
                <th scope="col">Action</th>
              </tr>
            </thead>
            <tbody>
              {items.map((item) => (
                <tr data-testid="request-row" key={item.id}>
                  <th scope="row">
                    <strong>{item.id}</strong>
                    <span>Revision {item.revision}</span>
                  </th>
                  <td>
                    <a href={`#/access/principals/${item.principalID}`}>
                      {item.principalID}
                    </a>
                  </td>
                  <td>
                    {item.requestedPolicy.scope}: {item.requestedPolicy.target}
                  </td>
                  <td>
                    <StatusLabel
                      state={item.state === "pending" ? "warning" : "current"}
                    >
                      {item.state}
                    </StatusLabel>
                  </td>
                  <td>
                    <a href={`#/requests/${item.id}`}>Open request</a>
                  </td>
                </tr>
              ))}
            </tbody>
          </ComparisonTable>
        )}
      </section>
    </div>
  );
}
