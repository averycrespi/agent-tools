import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
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
  FormField,
  InertJSON,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { ProtectedContext, SessionClient } from "./session";
import { UserTime } from "./time";
import type { ViewSnapshot } from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const jsonNumber = /^-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?$/;
type JSONRecord = Record<string, unknown>;
type Effect = "allow" | "deny";
type GrantState = "active" | "expired";
type ScalarType = "null" | "boolean" | "string" | "number";
interface Grant {
  id: string;
  principalID: string;
  effect: Effect;
  serverID: string;
  upstreamName: string | null;
  constraint: unknown | null;
  expiresAt: string | null;
  state: GrantState;
  createdAt: string;
}
interface Atom {
  pointer: string;
  type: ScalarType;
  value: string;
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
function id(value: unknown): string {
  const result = text(value);
  if (!gatewayID.test(result)) throw new Error("invalid response");
  return result;
}
function decodeGrant(value: unknown): Grant {
  const item = record(value, [
    "id",
    "principal_id",
    "effect",
    "server_id",
    "upstream_name",
    "constraint",
    "expires_at",
    "state",
    "created_at",
  ]);
  if (
    (item.effect !== "allow" && item.effect !== "deny") ||
    (item.state !== "active" && item.state !== "expired") ||
    (item.upstream_name !== null && typeof item.upstream_name !== "string") ||
    (item.expires_at !== null && typeof item.expires_at !== "string") ||
    (item.constraint !== null &&
      (typeof item.constraint !== "object" || Array.isArray(item.constraint)))
  )
    throw new Error("invalid response");
  return {
    id: id(item.id),
    principalID: id(item.principal_id),
    effect: item.effect,
    serverID: id(item.server_id),
    upstreamName: item.upstream_name,
    constraint: item.constraint,
    expiresAt: item.expires_at,
    state: item.state,
    createdAt: text(item.created_at),
  };
}
function headers(context: ProtectedContext): HeadersInit {
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
      headers: headers(context),
    });
    if (await context.sessionLost(response)) return undefined;
    const type = response.headers.get("Content-Type");
    if (type !== "application/json" && type !== "application/problem+json")
      throw new Error("Grant data is unavailable.");
    return { response, value: (await response.json()) as unknown };
  });
}
async function readGrants(
  session: SessionClient,
  query: Readonly<Record<string, string>>,
): Promise<Grant[]> {
  const items: Grant[] = [];
  let cursor: string | null = null;
  let restarted = false;
  for (;;) {
    const params = new URLSearchParams({ limit: "50" });
    if (query.principal_id !== undefined)
      params.set("principal_id", query.principal_id);
    if (query.server_id !== undefined) params.set("server_id", query.server_id);
    if (cursor !== null) params.set("cursor", cursor);
    const result = await requestJSON(session, `/api/v1/grants?${params}`);
    if (result === undefined) return [];
    if (result.response.status === 409 && cursor !== null && !restarted) {
      items.length = 0;
      cursor = null;
      restarted = true;
      continue;
    }
    if (!result.response.ok) throw new Error("Grant data is unavailable.");
    const page = record(result.value, ["items", "next_cursor"]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    items.push(...page.items.map(decodeGrant));
    if (page.next_cursor === null) return items;
    cursor = text(page.next_cursor);
    if (cursor.length === 0 || cursor.length > 4096)
      throw new Error("invalid response");
  }
}
async function readGrant(
  session: SessionClient,
  grantID: string,
): Promise<Grant | undefined> {
  const result = await requestJSON(session, `/api/v1/grants/${grantID}`);
  if (result === undefined) return undefined;
  if (!result.response.ok) throw new Error("Grant data is unavailable.");
  return decodeGrant(result.value);
}
async function decodeMutation(response: Response): Promise<Grant> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid response");
  return decodeGrant((await response.json()) as unknown);
}
function validPointer(pointer: string): boolean {
  return (
    pointer.startsWith("/") &&
    new TextEncoder().encode(pointer).length <= 256 &&
    !/~(?:[^01]|$)/.test(pointer)
  );
}
function constraintText(atoms: readonly Atom[]): string {
  if (atoms.length === 0) return "null";
  const members = atoms.map((atom) => {
    let scalar: string;
    if (atom.type === "null") scalar = "null";
    else if (atom.type === "boolean") {
      if (atom.value !== "true" && atom.value !== "false")
        throw new Error("Boolean values must be true or false.");
      scalar = atom.value;
    } else if (atom.type === "number") {
      if (!jsonNumber.test(atom.value))
        throw new Error("Number values must use valid JSON number syntax.");
      scalar = atom.value;
    } else scalar = JSON.stringify(atom.value);
    return `${JSON.stringify(atom.pointer)}:${scalar}`;
  });
  return `{"equals":{${members.join(",")}}}`;
}
function previewConstraint(atoms: readonly Atom[]): unknown {
  if (atoms.length === 0) return null;
  const equals: Record<string, unknown> = {};
  for (const atom of atoms)
    equals[atom.pointer] =
      atom.type === "null"
        ? null
        : atom.type === "boolean"
          ? atom.value === "true"
          : atom.type === "number"
            ? atom.value
            : atom.value;
  return { equals };
}

function GrantCreate({
  mutations,
  query,
}: {
  mutations: MutationCoordinator;
  query: Readonly<Record<string, string>>;
}) {
  const initialDraft = useRef({
    principalID: query.principal_id ?? "",
    effect: "allow" as Effect,
    serverID: query.server_id ?? "",
    scope: "server" as "server" | "tool",
    upstreamName: "",
    expiresAt: "",
    atoms: [] as Atom[],
  });
  const [principalID, setPrincipalID] = useState(
    initialDraft.current.principalID,
  );
  const [effect, setEffect] = useState<Effect>(initialDraft.current.effect);
  const [serverID, setServerID] = useState(initialDraft.current.serverID);
  const [scope, setScope] = useState<"server" | "tool">(
    initialDraft.current.scope,
  );
  const [upstreamName, setUpstreamName] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [atoms, setAtoms] = useState<Atom[]>([]);
  const [error, setError] = useState<string>();
  const [controller] = useState<MutationController<Grant>>(() =>
    mutations.create<Grant>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const navigate = useUnsavedChanges(
    JSON.stringify({
      principalID,
      effect,
      serverID,
      scope,
      upstreamName,
      expiresAt,
      atoms,
    }) !== JSON.stringify(initialDraft.current),
  );
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  const updateAtom = (index: number, patch: Partial<Atom>) =>
    setAtoms((current) =>
      current.map((atom, position) =>
        position === index ? { ...atom, ...patch } : atom,
      ),
    );
  const submit = async () => {
    setError(undefined);
    try {
      if (!gatewayID.test(principalID) || !gatewayID.test(serverID))
        throw new Error(
          "Principal and server IDs must be complete Gateway IDs.",
        );
      if (scope === "tool" && upstreamName.length === 0)
        throw new Error("Exact-tool scope requires an upstream tool name.");
      if (scope === "server" && atoms.length !== 0)
        throw new Error("Server-wide grants cannot have argument constraints.");
      if (atoms.length > 16)
        throw new Error("At most 16 equality atoms are allowed.");
      if (atoms.some((atom) => !validPointer(atom.pointer)))
        throw new Error("Each atom requires a valid RFC 6901 JSON pointer.");
      if (new Set(atoms.map((atom) => atom.pointer)).size !== atoms.length)
        throw new Error("Constraint pointers must be unique.");
      if (
        expiresAt !== "" &&
        (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(expiresAt) ||
          Number.isNaN(Date.parse(expiresAt)) ||
          Date.parse(expiresAt) <= Date.now())
      )
        throw new Error("Expiry must be a future canonical UTC timestamp.");
      const constraint = constraintText(atoms);
      const body = `{"principal_id":${JSON.stringify(principalID)},"effect":${JSON.stringify(effect)},"server_id":${JSON.stringify(serverID)},"upstream_name":${scope === "server" ? "null" : JSON.stringify(upstreamName)},"constraint":${constraint},"expires_at":${expiresAt === "" ? "null" : JSON.stringify(expiresAt)}}`;
      const spec: MutationSpec<Grant> = {
        route: "/api/v1/grants",
        method: "POST",
        body,
        precondition: null,
        requiresPrecondition: false,
        idempotency: "none",
        successStatuses: [201],
        decode: decodeMutation,
      };
      controller.begin(spec);
      const outcome = await controller.submit();
      if (outcome.kind === "acknowledged")
        navigate(`#/grants/${outcome.value.id}`, true);
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Invalid grant.");
    }
  };
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched";
  return (
    <div class="domain-view" data-testid="grant-create-view">
      <section class="panel domain-panel" aria-labelledby="grant-create-title">
        <div class="panel-heading">
          <div>
            <span class="panel-code">IMMUTABLE POLICY</span>
            <h2 id="grant-create-title">Create grant</h2>
          </div>
        </div>
        <p>
          Grants are immutable ALLOW or DENY records. The API remains
          authoritative; this form validates only the closed syntax and makes no
          overlap or visibility claim.
        </p>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void submit();
          }}
        >
          <FormField id="grant-principal" label="Principal ID" required>
            {(attributes) => (
              <input
                {...attributes}
                data-testid="grant-principal"
                value={principalID}
                onInput={(event) => setPrincipalID(event.currentTarget.value)}
                required
              />
            )}
          </FormField>
          <FormField id="grant-effect" label="Effect">
            {(attributes) => (
              <select
                {...attributes}
                data-testid="grant-effect"
                value={effect}
                onChange={(event) =>
                  setEffect(event.currentTarget.value as Effect)
                }
              >
                <option value="allow">ALLOW</option>
                <option value="deny">DENY</option>
              </select>
            )}
          </FormField>
          <FormField id="grant-server" label="Server ID" required>
            {(attributes) => (
              <input
                {...attributes}
                data-testid="grant-server"
                value={serverID}
                onInput={(event) => setServerID(event.currentTarget.value)}
                required
              />
            )}
          </FormField>
          <FormField id="grant-scope" label="Scope">
            {(attributes) => (
              <select
                {...attributes}
                data-testid="grant-scope"
                value={scope}
                onChange={(event) => {
                  const next = event.currentTarget.value as "server" | "tool";
                  setScope(next);
                  if (next === "server") setAtoms([]);
                }}
              >
                <option value="server">Entire server</option>
                <option value="tool">Exact upstream tool</option>
              </select>
            )}
          </FormField>
          {scope === "tool" && (
            <FormField id="grant-upstream" label="Exact upstream name" required>
              {(attributes) => (
                <input
                  {...attributes}
                  data-testid="grant-upstream"
                  value={upstreamName}
                  onInput={(event) =>
                    setUpstreamName(event.currentTarget.value)
                  }
                  required
                />
              )}
            </FormField>
          )}
          <FormField
            id="grant-expiry"
            label="Expiry"
            hint="Leave blank for permanent; otherwise use a future canonical UTC RFC3339 timestamp."
            optional
          >
            {(attributes) => (
              <input
                {...attributes}
                data-testid="grant-expiry"
                value={expiresAt}
                placeholder="2030-01-01T00:00:00Z"
                onInput={(event) => setExpiresAt(event.currentTarget.value)}
              />
            )}
          </FormField>
          {scope === "tool" && (
            <section class="subpanel" aria-labelledby="constraint-title">
              <h3 id="constraint-title">Exact argument equalities</h3>
              <p>
                Each atom is one RFC 6901 pointer and one JSON scalar. Arrays,
                objects, ranges, coercion, and schedules are not supported.
                Number spelling is preserved.
              </p>
              {atoms.map((atom, index) => (
                <div
                  class="form-grid"
                  data-testid="constraint-atom"
                  key={index}
                >
                  <FormField
                    id={`constraint-pointer-${index}`}
                    label="JSON pointer"
                  >
                    {(attributes) => (
                      <input
                        {...attributes}
                        data-testid="constraint-pointer"
                        value={atom.pointer}
                        onInput={(event) =>
                          updateAtom(index, {
                            pointer: event.currentTarget.value,
                          })
                        }
                      />
                    )}
                  </FormField>
                  <FormField
                    id={`constraint-type-${index}`}
                    label="Scalar type"
                  >
                    {(attributes) => (
                      <select
                        {...attributes}
                        data-testid="constraint-type"
                        value={atom.type}
                        onChange={(event) =>
                          updateAtom(index, {
                            type: event.currentTarget.value as ScalarType,
                          })
                        }
                      >
                        <option value="null">null</option>
                        <option value="boolean">boolean</option>
                        <option value="string">string</option>
                        <option value="number">number</option>
                      </select>
                    )}
                  </FormField>
                  {atom.type !== "null" && (
                    <FormField
                      id={`constraint-value-${index}`}
                      label="Scalar value"
                    >
                      {(attributes) => (
                        <input
                          {...attributes}
                          data-testid="constraint-value"
                          value={atom.value}
                          onInput={(event) =>
                            updateAtom(index, {
                              value: event.currentTarget.value,
                            })
                          }
                        />
                      )}
                    </FormField>
                  )}
                  <button
                    type="button"
                    onClick={() =>
                      setAtoms((current) =>
                        current.filter((_, position) => position !== index),
                      )
                    }
                  >
                    Remove atom
                  </button>
                </div>
              ))}
              <button
                data-testid="add-constraint-atom"
                type="button"
                disabled={atoms.length >= 16}
                onClick={() =>
                  setAtoms((current) => [
                    ...current,
                    { pointer: "/", type: "string", value: "" },
                  ])
                }
              >
                Add equality atom
              </button>
              <InertJSON
                value={previewConstraint(atoms)}
                label="Constraint preview"
              />
            </section>
          )}
          {error !== undefined && (
            <StateNotice state="error" title="Check grant configuration">
              <p>{error}</p>
            </StateNotice>
          )}
          {mutation.problem !== undefined && (
            <StateNotice state="error" title={mutation.problem.title} />
          )}
          {mutation.state === "uncertain" && (
            <StateNotice state="warning" title="Grant outcome is unknown">
              <p>
                Do not replay this non-idempotent creation. Refresh the filtered
                grant list to investigate.
              </p>
            </StateNotice>
          )}
          <button
            data-testid="grant-create-submit"
            type="submit"
            disabled={disabled}
          >
            {mutation.state === "submitting"
              ? "Creating…"
              : "Create immutable grant"}
          </button>
        </form>
      </section>
    </div>
  );
}

type GrantActionResult =
  | { kind: "created"; grant: Grant }
  | { kind: "deleted" };
type GrantAction = "delete" | "create";
type CorrectionPhase = "configure" | "create_second" | "delete_second";

async function principalVisibility(
  session: SessionClient,
  principalID: string,
): Promise<"requestable" | "allowed-only" | "all" | undefined> {
  const result = await requestJSON(
    session,
    `/api/v1/principals/${principalID}`,
  );
  if (result === undefined || !result.response.ok) return undefined;
  const value = record(result.value, [
    "id",
    "display_name",
    "state",
    "visibility",
    "revision",
    "credential_revision",
    "credential",
    "created_at",
    "updated_at",
  ]);
  return value.visibility === "requestable" ||
    value.visibility === "allowed-only" ||
    value.visibility === "all"
    ? value.visibility
    : undefined;
}

function GrantActions({
  session,
  mutations,
  grant,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  grant: Grant;
}) {
  const [controller] = useState<MutationController<GrantActionResult>>(() =>
    mutations.create<GrantActionResult>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [correction, setCorrection] = useState(false);
  const [order, setOrder] = useState<"create_first" | "delete_first">(
    "create_first",
  );
  const [replacementEffect, setReplacementEffect] = useState<Effect>(
    grant.effect === "allow" ? "deny" : "allow",
  );
  const [phase, setPhase] = useState<CorrectionPhase>("configure");
  const [replacementID, setReplacementID] = useState<string>();
  const [action, setAction] = useState<GrantAction>("delete");
  const [confirming, setConfirming] = useState(false);
  const [visibility, setVisibility] = useState<
    "requestable" | "allowed-only" | "all"
  >();
  const [notice, setNotice] = useState<string>();
  const actionButton = useRef<HTMLButtonElement>(null);
  const syntheticDefault = grant.serverID === "00000000000000000000000000";
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (!syntheticDefault) return;
    let current = true;
    void principalVisibility(session, grant.principalID).then((value) => {
      if (current) setVisibility(value);
    });
    return () => {
      current = false;
    };
  }, [grant.principalID, syntheticDefault]);

  const createSpec = (): MutationSpec<GrantActionResult> => {
    const body = JSON.stringify({
      principal_id: grant.principalID,
      effect: replacementEffect,
      server_id: grant.serverID,
      upstream_name: grant.upstreamName,
      constraint: null,
      expires_at: null,
    });
    return {
      route: "/api/v1/grants",
      method: "POST",
      body,
      precondition: null,
      requiresPrecondition: false,
      idempotency: "none",
      successStatuses: [201],
      decode: async (response) => ({
        kind: "created",
        grant: await decodeMutation(response),
      }),
    };
  };
  const deleteSpec = (): MutationSpec<GrantActionResult> => ({
    route: `/api/v1/grants/${grant.id}`,
    method: "DELETE",
    body: null,
    precondition: null,
    requiresPrecondition: false,
    idempotency: "none",
    successStatuses: [204],
    decode: async () => ({ kind: "deleted" }),
  });
  const review = (next: GrantAction) => {
    setNotice(undefined);
    setAction(next);
    controller.begin(next === "create" ? createSpec() : deleteSpec());
    setConfirming(true);
  };
  const refreshPolicy = () =>
    readGrants(session, {
      principal_id: grant.principalID,
      ...(syntheticDefault ? {} : { server_id: grant.serverID }),
    });
  const confirm = async () => {
    setConfirming(false);
    const outcome = await controller.submit();
    if (outcome.kind !== "acknowledged") return;
    await refreshPolicy();
    controller.abandon();
    if (!correction) {
      window.location.hash = `#/grants?principal_id=${grant.principalID}`;
      return;
    }
    if (phase === "configure") {
      if (outcome.value.kind === "created") {
        setReplacementID(outcome.value.grant.id);
        setPhase("delete_second");
        setNotice(
          "Step one was acknowledged and policy was refreshed. The replacement now overlaps until you explicitly confirm deletion as step two.",
        );
      } else {
        setPhase("create_second");
        setNotice(
          "Step one was acknowledged and policy was refreshed. Authorization is absent until you explicitly confirm replacement creation as step two.",
        );
      }
      return;
    }
    const destination =
      outcome.value.kind === "created" ? outcome.value.grant.id : replacementID;
    window.location.hash =
      destination === undefined
        ? `#/grants?principal_id=${grant.principalID}`
        : `#/grants/${destination}`;
  };
  const cancelConfirmation = () => {
    setConfirming(false);
    controller.abandon();
  };
  const resetCorrection = () => {
    setCorrection(false);
    setPhase("configure");
    setReplacementID(undefined);
    setNotice(undefined);
  };
  const defaultWarning =
    visibility === "allowed-only"
      ? "This is the synthetic default ALLOW for an allowed-only principal. Deletion can remove both discovery and call authorization unless another ALLOW applies."
      : visibility === "requestable"
        ? "This is the synthetic default ALLOW for a requestable principal. Deletion removes broad call authorization; requestable discovery still follows current DENY policy."
        : visibility === "all"
          ? "This is the synthetic default ALLOW for an all-visible principal. Current tools may remain discoverable, but visibility never supplies call authorization."
          : "This is a synthetic default ALLOW. Refresh principal visibility before relying on discovery consequences.";
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched";
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="grant-actions-title"
      data-testid="grant-actions"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">POLICY CHANGE</span>
          <h2 id="grant-actions-title">Delete or correct this grant</h2>
        </div>
      </div>
      <p>
        Grants cannot be edited. Correction is exactly two independent operator
        gestures; no composite mutation, force path, automatic second step, or
        automatic replay exists.
      </p>
      {syntheticDefault && (
        <StateNotice
          state="warning"
          title="Synthetic default-grant consequence"
        >
          <p>{defaultWarning}</p>
        </StateNotice>
      )}
      {grant.state === "expired" && (
        <StateNotice
          state="warning"
          title="Expired grant still consumes capacity"
        >
          <p>
            Deletion releases its retained grant slot; expiry does not delete
            evidence.
          </p>
        </StateNotice>
      )}
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title}>
          <p>No later correction step was submitted.</p>
        </StateNotice>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Grant mutation outcome is unknown">
          <p>
            Do not replay and do not submit the later correction step. Refresh
            policy to investigate.
          </p>
        </StateNotice>
      )}
      {notice !== undefined && <StateNotice state="warning" title={notice} />}
      {!correction ? (
        <div class="inline-actions">
          <button
            ref={actionButton}
            data-testid="grant-delete"
            class="danger-action"
            type="button"
            disabled={disabled}
            onClick={() => review("delete")}
          >
            Delete grant
          </button>
          <button
            data-testid="grant-correct"
            type="button"
            disabled={disabled || grant.constraint !== null || syntheticDefault}
            onClick={() => setCorrection(true)}
          >
            Correct grant
          </button>
        </div>
      ) : (
        <div data-testid="grant-correction">
          {phase === "configure" && (
            <>
              <FormField id="correction-order" label="Correction order">
                {(attributes) => (
                  <select
                    {...attributes}
                    data-testid="correction-order"
                    value={order}
                    onChange={(event) =>
                      setOrder(
                        event.currentTarget.value as
                          | "create_first"
                          | "delete_first",
                      )
                    }
                  >
                    <option value="create_first">
                      Create before delete — temporary overlap
                    </option>
                    <option value="delete_first">
                      Delete before create — temporary loss
                    </option>
                  </select>
                )}
              </FormField>
              <FormField id="correction-effect" label="Replacement effect">
                {(attributes) => (
                  <select
                    {...attributes}
                    data-testid="correction-effect"
                    value={replacementEffect}
                    onChange={(event) =>
                      setReplacementEffect(event.currentTarget.value as Effect)
                    }
                  >
                    <option value="allow">ALLOW</option>
                    <option value="deny">DENY</option>
                  </select>
                )}
              </FormField>
              <p>
                The replacement keeps the same principal and scope, is
                unconstrained, and is permanent. Use Create grant for any other
                policy shape.
              </p>
            </>
          )}
          <div class="inline-actions">
            <button
              ref={actionButton}
              data-testid="grant-correction-step"
              type="button"
              disabled={disabled}
              onClick={() =>
                review(
                  phase === "create_second" ||
                    (phase === "configure" && order === "create_first")
                    ? "create"
                    : "delete",
                )
              }
            >
              {phase === "configure" ? "Confirm step one" : "Confirm step two"}
            </button>
            {phase === "configure" && (
              <button type="button" onClick={resetCorrection}>
                Cancel correction
              </button>
            )}
          </div>
        </div>
      )}
      <ConfirmationDialog
        id="grant-action-confirm"
        open={confirming}
        title={
          action === "create" ? "Create replacement grant?" : "Delete grant?"
        }
        consequence={
          <p>
            {action === "create"
              ? "This creates one independent immutable policy record. It does not delete or modify the current grant."
              : "This permanently removes this policy record. No replacement or later correction step is automatic."}
          </p>
        }
        confirmLabel={
          action === "create" ? "Create replacement" : "Delete grant"
        }
        destructive={action === "delete"}
        returnFocus={actionButton}
        onCancel={cancelConfirmation}
        onConfirm={() => void confirm()}
      />
    </section>
  );
}

export function Grants({
  session,
  mutations,
  resolved,
  view,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
}) {
  const segments = resolved.location.segments;
  const create = segments[1] === "new";
  const grantID = segments.length === 2 && !create ? segments[1] : undefined;
  const [items, setItems] = useState<Grant[]>();
  const [detail, setDetail] = useState<Grant>();
  const [error, setError] = useState<string>();
  useEffect(() => {
    let current = true;
    setError(undefined);
    if (create)
      return () => {
        current = false;
      };
    if (grantID !== undefined) {
      setDetail((value) => (value?.id === grantID ? value : undefined));
      void readGrant(session, grantID)
        .then((value) => {
          if (current && value !== undefined) setDetail(value);
        })
        .catch((caught: unknown) => {
          if (current)
            setError(
              caught instanceof Error
                ? caught.message
                : "Grant data is unavailable.",
            );
        });
    } else {
      setItems(undefined);
      void readGrants(session, resolved.location.query)
        .then((value) => {
          if (current) setItems(value);
        })
        .catch((caught: unknown) => {
          if (current)
            setError(
              caught instanceof Error
                ? caught.message
                : "Grant data is unavailable.",
            );
        });
    }
    return () => {
      current = false;
    };
  }, [resolved.canonicalFragment, view.generation]);
  if (create)
    return (
      <GrantCreate mutations={mutations} query={resolved.location.query} />
    );
  if (error !== undefined)
    return (
      <StateNotice state="error" title="Grant data unavailable">
        <p>{error}</p>
      </StateNotice>
    );
  if (grantID !== undefined) {
    if (detail === undefined)
      return <StateNotice state="loading" title="Loading grant" />;
    return (
      <div class="domain-view" data-testid="grant-detail">
        <section class="panel domain-panel" aria-labelledby="grant-title">
          <div class="panel-heading">
            <div>
              <span class="panel-value">
                Immutable {detail.effect.toUpperCase()}
              </span>
              <h2 id="grant-title">Grant {detail.id}</h2>
            </div>
            <StatusLabel
              state={detail.state === "active" ? "current" : "warning"}
            >
              {detail.state}
            </StatusLabel>
          </div>
          <dl class="fact-grid">
            <div>
              <dt>Principal</dt>
              <dd>
                <a href={`#/principals/${detail.principalID}`}>
                  {detail.principalID}
                </a>
              </dd>
            </div>
            <div>
              <dt>Server</dt>
              <dd>
                {detail.serverID === "00000000000000000000000000" ? (
                  "Synthetic default namespace"
                ) : (
                  <a href={`#/servers/${detail.serverID}`}>{detail.serverID}</a>
                )}
              </dd>
            </div>
            <div>
              <dt>Scope</dt>
              <dd>
                {detail.upstreamName === null
                  ? "Entire server"
                  : `Exact tool ${detail.upstreamName}`}
              </dd>
            </div>
            <div>
              <dt>Expiry</dt>
              <dd>
                <UserTime value={detail.expiresAt} fallback="Permanent" />
              </dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>
                <UserTime value={detail.createdAt} />
              </dd>
            </div>
          </dl>
          {detail.constraint !== null && (
            <InertJSON value={detail.constraint} label="Grant constraint" />
          )}
        </section>
        <GrantActions
          key={detail.id}
          session={session}
          mutations={mutations}
          grant={detail}
        />
      </div>
    );
  }
  if (items === undefined)
    return <StateNotice state="loading" title="Loading grants" />;
  const query = new URLSearchParams(resolved.location.query).toString();
  return (
    <div class="domain-view" data-testid="grants-view">
      <div class="collection-toolbar">
        <a
          class="button-link primary-action"
          href={`#/grants/new${query === "" ? "" : `?${query}`}`}
          data-testid="grant-create-link"
        >
          Create grant
        </a>
      </div>
      <section class="panel domain-panel" aria-labelledby="page-title">
        {items.length === 0 ? (
          <StateNotice state="empty" title="No grants match" />
        ) : (
          <CollectionTable
            caption="Grant policy records"
            items={items}
            rowKey={(grant) => grant.id}
            rowTestID="grant-row"
            filters={[
              {
                key: "principal",
                label: "Principal",
                type: "text",
                value: (grant) => grant.principalID,
              },
              {
                key: "target",
                label: "Target",
                type: "text",
                value: (grant) =>
                  `${grant.serverID} ${grant.upstreamName ?? "Entire server"}`,
              },
              {
                key: "effect",
                label: "Effect",
                type: "select",
                value: (grant) => grant.effect,
                options: [
                  { value: "allow", label: "Allow" },
                  { value: "deny", label: "Deny" },
                ],
              },
              {
                key: "state",
                label: "State",
                type: "select",
                value: (grant) => grant.state,
                options: [
                  { value: "active", label: "Active" },
                  { value: "expired", label: "Expired" },
                ],
              },
            ]}
            columns={[
              {
                key: "effect",
                label: "Effect",
                sortValue: (grant) => grant.effect,
                render: (grant) => (
                  <strong>{grant.effect.toUpperCase()}</strong>
                ),
              },
              {
                key: "principal",
                label: "Principal",
                sortValue: (grant) => grant.principalID,
                render: (grant) => (
                  <a href={`#/principals/${grant.principalID}`}>
                    {grant.principalID}
                  </a>
                ),
              },
              {
                key: "target",
                label: "Target",
                sortValue: (grant) => grant.upstreamName ?? grant.serverID,
                render: (grant) =>
                  grant.serverID === "00000000000000000000000000" ? (
                    "Synthetic default namespace"
                  ) : (
                    <a href={`#/servers/${grant.serverID}`}>
                      {grant.upstreamName === null
                        ? "Entire server"
                        : grant.upstreamName}
                    </a>
                  ),
              },
              {
                key: "state",
                label: "State",
                sortValue: (grant) => grant.state,
                render: (grant) => (
                  <StatusLabel
                    state={grant.state === "active" ? "current" : "warning"}
                  >
                    {grant.state}
                  </StatusLabel>
                ),
              },
              {
                key: "action",
                label: "Action",
                render: (grant) => (
                  <a href={`#/grants/${grant.id}`}>Open grant</a>
                ),
              },
            ]}
          />
        )}
      </section>
    </div>
  );
}
