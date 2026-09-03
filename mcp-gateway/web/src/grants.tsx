import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import { readMatcherDescriptors } from "./matcher-catalog";
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
  StateNotice,
  StatusLabel,
} from "./primitives";
import { readPrincipals, type Principal } from "./principals";
import {
  decodeServer,
  type DescriptorView,
  type ServerView,
} from "./server-reads";
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
  description: string | null;
  revision: string;
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
  operator: "equals" | "regex";
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
    "description",
    "revision",
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
    description: item.description === null ? null : text(item.description),
    revision: text(item.revision),
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
async function readServers(session: SessionClient): Promise<ServerView[]> {
  const items: ServerView[] = [];
  let cursor: string | null = null;
  for (;;) {
    const params = new URLSearchParams({ limit: "100" });
    if (cursor !== null) params.set("cursor", cursor);
    const result = await requestJSON(session, `/api/v1/servers?${params}`);
    if (result === undefined) return [];
    if (!result.response.ok) throw new Error("Server data is unavailable.");
    const page = record(result.value, ["items", "next_cursor"]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    items.push(...page.items.map(decodeServer));
    if (page.next_cursor === null) return items;
    cursor = text(page.next_cursor);
  }
}

async function readGrants(session: SessionClient): Promise<Grant[]> {
  const items: Grant[] = [];
  let cursor: string | null = null;
  let restarted = false;
  for (;;) {
    const params = new URLSearchParams({ limit: "50" });
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
  const members = (operator: Atom["operator"]) =>
    atoms
      .filter((atom) => atom.operator === operator)
      .map((atom) => {
        let value: string;
        if (operator === "regex") value = JSON.stringify(atom.value);
        else if (atom.type === "null") value = "null";
        else if (atom.type === "boolean") {
          if (atom.value !== "true" && atom.value !== "false")
            throw new Error("Boolean values must be true or false.");
          value = atom.value;
        } else if (atom.type === "number") {
          if (!jsonNumber.test(atom.value))
            throw new Error("Number values must use valid JSON number syntax.");
          value = atom.value;
        } else value = JSON.stringify(atom.value);
        return `${JSON.stringify(atom.pointer)}:${value}`;
      });
  const equalities = members("equals");
  const expressions = members("regex");
  if (expressions.length === 0) return `{"equals":{${equalities.join(",")}}}`;
  const equalsMember =
    equalities.length === 0 ? "" : `,"equals":{${equalities.join(",")}}`;
  return `{"version":2${equalsMember},"regex":{${expressions.join(",")}}}`;
}
function previewConstraint(atoms: readonly Atom[]): unknown {
  if (atoms.length === 0) return null;
  const equals: Record<string, unknown> = {};
  const regex: Record<string, string> = {};
  for (const atom of atoms) {
    if (atom.operator === "regex") regex[atom.pointer] = atom.value;
    else
      equals[atom.pointer] =
        atom.type === "null"
          ? null
          : atom.type === "boolean"
            ? atom.value === "true"
            : atom.value;
  }
  return Object.keys(regex).length === 0
    ? { equals }
    : {
        version: 2,
        ...(Object.keys(equals).length === 0 ? {} : { equals }),
        regex,
      };
}

function GrantCreate({
  session,
  mutations,
  query,
  principals,
  servers,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  query: Readonly<Record<string, string>>;
  principals: readonly Principal[];
  servers: readonly ServerView[];
}) {
  const initialDraft = useRef({
    description: "",
    principalID: query.principal_id ?? "",
    effect: "allow" as Effect,
    serverID: query.server_id ?? "",
    scope: "server" as "server" | "tool",
    upstreamName: "",
    expiresAt: "",
    atoms: [] as Atom[],
  });
  const [description, setDescription] = useState(
    initialDraft.current.description,
  );
  const [principalID, setPrincipalID] = useState(
    initialDraft.current.principalID,
  );
  const [effect, setEffect] = useState<Effect>(initialDraft.current.effect);
  const [serverID, setServerID] = useState(initialDraft.current.serverID);
  const [scope, setScope] = useState<"server" | "tool">(
    initialDraft.current.scope,
  );
  const [upstreamName, setUpstreamName] = useState("");
  const [descriptors, setDescriptors] = useState<DescriptorView[]>();
  const [catalogError, setCatalogError] = useState(false);
  const [expiresAt, setExpiresAt] = useState("");
  const [atoms, setAtoms] = useState<Atom[]>([]);
  const [error, setError] = useState<string>();
  const [validating, setValidating] = useState(false);
  const submitButton = useRef<HTMLButtonElement>(null);
  const [controller] = useState<MutationController<Grant>>(() =>
    mutations.create<Grant>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const draftFingerprint = JSON.stringify({
    description,
    principalID,
    effect,
    serverID,
    scope,
    upstreamName,
    expiresAt,
    atoms,
  });
  const currentDraft = useRef(draftFingerprint);
  currentDraft.current = draftFingerprint;
  const navigate = useUnsavedChanges(
    draftFingerprint !== JSON.stringify(initialDraft.current),
  );
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    let current = true;
    setCatalogError(false);
    setDescriptors(undefined);
    if (scope !== "tool" || !gatewayID.test(serverID))
      return () => {
        current = false;
      };
    void readMatcherDescriptors(session, serverID)
      .then((items) => {
        if (current && items !== undefined) setDescriptors(items);
      })
      .catch(() => {
        if (current) setCatalogError(true);
      });
    return () => {
      current = false;
    };
  }, [scope, serverID, session]);
  const updateAtom = (index: number, patch: Partial<Atom>) => {
    setError(undefined);
    setAtoms((current) =>
      current.map((atom, position) =>
        position === index ? { ...atom, ...patch } : atom,
      ),
    );
  };
  const review = async () => {
    const reviewedDraft = currentDraft.current;
    setError(undefined);
    try {
      if (
        description.length > 0 &&
        (description.trim() !== description ||
          new TextEncoder().encode(description).length > 256)
      )
        throw new Error(
          "Description must be at most 256 bytes without surrounding whitespace.",
        );
      if (containsControlCharacters(description))
        throw new Error("Description cannot contain control characters.");
      if (!gatewayID.test(principalID) || !gatewayID.test(serverID))
        throw new Error(
          "Principal and server IDs must be complete Gateway IDs.",
        );
      if (scope === "tool" && upstreamName.length === 0)
        throw new Error("Exact-tool scope requires an upstream tool name.");
      if (scope === "server" && atoms.length !== 0)
        throw new Error("Server-wide grants cannot have argument constraints.");
      if (atoms.length > 16)
        throw new Error("At most 16 matcher atoms are allowed.");
      if (atoms.some((atom) => !validPointer(atom.pointer)))
        throw new Error("Each atom requires a valid RFC 6901 JSON pointer.");
      if (
        new Set(atoms.map((atom) => `${atom.operator}:${atom.pointer}`))
          .size !== atoms.length
      )
        throw new Error("Pointers must be unique within each operator.");
      if (
        atoms.some(
          (atom) =>
            atom.operator === "regex" &&
            new TextEncoder().encode(atom.value).length > 1024,
        )
      )
        throw new Error("RE2 patterns must be at most 1024 bytes.");
      if (
        expiresAt !== "" &&
        (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?Z$/.test(expiresAt) ||
          Number.isNaN(Date.parse(expiresAt)) ||
          Date.parse(expiresAt) <= Date.now())
      )
        throw new Error("Expiry must be a future canonical UTC timestamp.");
      const constraint = constraintText(atoms);
      if (constraint !== "null") {
        setValidating(true);
        const diagnostic = await validateMatcherConstraint(session, constraint);
        if (diagnostic === undefined || currentDraft.current !== reviewedDraft)
          return;
        if (diagnostic !== null) throw new Error(diagnostic);
      }
      const body = `{"description":${description === "" ? "null" : JSON.stringify(description)},"principal_id":${JSON.stringify(principalID)},"effect":${JSON.stringify(effect)},"server_id":${JSON.stringify(serverID)},"upstream_name":${scope === "server" ? "null" : JSON.stringify(upstreamName)},"constraint":${constraint},"expires_at":${expiresAt === "" ? "null" : JSON.stringify(expiresAt)}}`;
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
      controller.confirm();
    } catch (caught) {
      setError(caught instanceof Error ? caught.message : "Invalid grant.");
    } finally {
      setValidating(false);
    }
  };
  const disabled =
    validating ||
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
        <p class="bounded-note">
          The effect, target, scope, constraints, and expiry cannot be edited
          after creation. You can update the optional description later.
        </p>
        <form
          onSubmit={(event) => {
            event.preventDefault();
            void review();
          }}
        >
          <FormField
            id="grant-description"
            label="Description"
            hint="Display metadata; it does not change authorization policy."
            optional
          >
            {(attributes) => (
              <input
                {...attributes}
                data-testid="grant-description"
                value={description}
                maxlength={256}
                onInput={(event) => setDescription(event.currentTarget.value)}
              />
            )}
          </FormField>
          <FormField id="grant-principal" label="Principal" required>
            {(attributes) => (
              <select
                {...attributes}
                data-testid="grant-principal"
                value={principalID}
                onChange={(event) => setPrincipalID(event.currentTarget.value)}
                required
              >
                <option value="">Choose a principal</option>
                {principals.map((principal) => (
                  <option value={principal.id} key={principal.id}>
                    {principal.displayName} — {principal.id}
                    {principal.state === "disabled" ? " (Disabled)" : ""}
                  </option>
                ))}
              </select>
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
          <FormField id="grant-server" label="Server or Gateway scope" required>
            {(attributes) => (
              <select
                {...attributes}
                data-testid="grant-server"
                value={serverID}
                onChange={(event) => setServerID(event.currentTarget.value)}
                required
              >
                <option value="">Choose a server</option>
                <option value="00000000000000000000000000">
                  Gateway self-service tools
                </option>
                {servers
                  .filter((server) => server.desiredState !== "deleted")
                  .map((server) => (
                    <option value={server.id} key={server.id}>
                      {server.displayName} — {server.id}
                      {server.desiredState === "disabled" ? " (Disabled)" : ""}
                    </option>
                  ))}
              </select>
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
            <div>
              <FormField
                id="grant-upstream"
                label="Exact upstream tool name"
                hint="Search current durable tools or enter a literal future tool name. Only the exact name is stored."
                required
              >
                {(attributes) => (
                  <input
                    {...attributes}
                    data-testid="grant-upstream"
                    value={upstreamName}
                    list="grant-tool-options"
                    autocomplete="off"
                    onInput={(event) =>
                      setUpstreamName(event.currentTarget.value)
                    }
                    required
                  />
                )}
              </FormField>
              <datalist id="grant-tool-options">
                {(descriptors ?? []).map((descriptor) => (
                  <option
                    value={descriptor.upstreamName}
                    label={descriptor.externalName}
                    key={descriptor.id}
                  />
                ))}
              </datalist>
              <p
                class="bounded-note"
                role="status"
                aria-live="polite"
                data-testid="grant-tool-posture"
              >
                {catalogError
                  ? "Catalog tools are unavailable. Manual entry remains available; verify the literal name before creating authority."
                  : descriptors === undefined
                    ? "Loading current durable tools…"
                    : descriptors.some(
                          (descriptor) =>
                            descriptor.upstreamName === upstreamName,
                        )
                      ? "Current durable descriptor selected. The descriptor assists authoring but is not stored as grant authority."
                      : "No current descriptor matches this literal name. Future, absent, or unavailable tools remain supported."}
              </p>
            </div>
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
              <h3 id="constraint-title">Argument matchers</h3>
              <p>
                Match a JSON pointer against an exact scalar or a full-string
                RE2 pattern. Matching is conjunctive, and number and pattern
                spelling is preserved.
              </p>
              {atoms.map((atom, index) => (
                <div
                  class="form-grid"
                  data-testid="constraint-atom"
                  key={index}
                >
                  <FormField
                    id={`constraint-operator-${index}`}
                    label="Operator"
                  >
                    {(attributes) => (
                      <select
                        {...attributes}
                        data-testid="constraint-operator"
                        value={atom.operator}
                        onChange={(event) =>
                          updateAtom(index, {
                            operator: event.currentTarget
                              .value as Atom["operator"],
                            type:
                              event.currentTarget.value === "regex"
                                ? "string"
                                : atom.type,
                          })
                        }
                      >
                        <option value="equals">Equals</option>
                        <option value="regex">Full-string RE2</option>
                      </select>
                    )}
                  </FormField>
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
                  {atom.operator === "equals" && (
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
                  )}
                  {(atom.operator === "regex" || atom.type !== "null") && (
                    <FormField
                      id={`constraint-value-${index}`}
                      label={
                        atom.operator === "regex"
                          ? "RE2 pattern"
                          : "Scalar value"
                      }
                      hint={
                        atom.operator === "regex"
                          ? "The pattern must match the complete string value."
                          : "The scalar is compared without coercion."
                      }
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
                    {
                      operator: "equals",
                      pointer: "/",
                      type: "string",
                      value: "",
                    },
                  ])
                }
              >
                Add matcher
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
            ref={submitButton}
            class="create-action form-submit-action"
            data-testid="grant-create-submit"
            type="submit"
            disabled={disabled}
          >
            {validating
              ? "Validating matcher…"
              : mutation.state === "submitting"
                ? "Creating…"
                : "Review and create"}
          </button>
        </form>
        <ConfirmationDialog
          id="grant-create-confirm"
          open={mutation.state === "confirming"}
          title="Review grant"
          consequence={
            <div class="review-stack">
              <p>
                This creates one immutable authorization policy. Review every
                value before continuing.
              </p>
              <dl class="fact-grid">
                <div>
                  <dt>Description</dt>
                  <dd>{description}</dd>
                </div>
                <div>
                  <dt>Principal</dt>
                  <dd>
                    {principals.find(
                      (principal) => principal.id === principalID,
                    )?.displayName ?? principalID}
                  </dd>
                </div>
                <div>
                  <dt>Effect</dt>
                  <dd>{effect === "allow" ? "Allow" : "Deny"}</dd>
                </div>
                <div>
                  <dt>Target</dt>
                  <dd>
                    {servers.find((server) => server.id === serverID)
                      ?.displayName ?? serverID}
                    {scope === "tool" ? ` · ${upstreamName}` : " · All tools"}
                  </dd>
                </div>
                <div>
                  <dt>Expiry</dt>
                  <dd>{expiresAt === "" ? "Permanent" : expiresAt}</dd>
                </div>
                <div>
                  <dt>Constraint</dt>
                  <dd>
                    {atoms.length === 0
                      ? "None"
                      : `${atoms.filter((atom) => atom.operator === "equals").length} equality · ${atoms.filter((atom) => atom.operator === "regex").length} regex`}
                  </dd>
                </div>
              </dl>
            </div>
          }
          confirmLabel="Create grant"
          returnFocus={submitButton}
          onCancel={() => controller.abandon()}
          onConfirm={() =>
            void controller.submit().then((outcome) => {
              if (outcome.kind === "acknowledged")
                navigate(`#/grants/${outcome.value.id}`, true);
            })
          }
        />
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

function GrantDescriptionEditor({
  mutations,
  grant,
  onAcknowledged,
}: {
  mutations: MutationCoordinator;
  grant: Grant;
  onAcknowledged: (grant: Grant) => void;
}) {
  const [description, setDescription] = useState(grant.description ?? "");
  const [error, setError] = useState<string>();
  const [controller] = useState<MutationController<Grant>>(() =>
    mutations.create<Grant>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  const submit = async () => {
    setError(undefined);
    if (
      description.length > 0 &&
      (description.trim() !== description ||
        new TextEncoder().encode(description).length > 256 ||
        containsControlCharacters(description))
    ) {
      setError(
        "Description must be at most 256 bytes without surrounding whitespace or control characters.",
      );
      return;
    }
    if (description === (grant.description ?? "")) {
      setError("Change the description before saving.");
      return;
    }
    controller.begin({
      route: `/api/v1/grants/${grant.id}`,
      method: "PATCH",
      body: JSON.stringify({
        description: description === "" ? null : description,
      }),
      precondition: `"grant-${grant.id}-${grant.revision}"`,
      requiresPrecondition: true,
      idempotency: "none",
      successStatuses: [200],
      decode: decodeMutation,
    });
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged") onAcknowledged(outcome.value);
  };
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="grant-description-title"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">DISPLAY METADATA</span>
          <h2 id="grant-description-title">Description</h2>
        </div>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void submit();
        }}
      >
        <FormField
          id="grant-description-edit"
          label="Description"
          hint="Editing this field does not change authorization policy."
          optional
        >
          {(attributes) => (
            <input
              {...attributes}
              value={description}
              maxlength={256}
              onInput={(event) => setDescription(event.currentTarget.value)}
            />
          )}
        </FormField>
        {error !== undefined && (
          <StateNotice state="error" title="Description not saved">
            <p>{error}</p>
          </StateNotice>
        )}
        <button
          class="button-safe form-submit-action"
          type="submit"
          disabled={
            mutation.state === "submitting" ||
            mutation.availability === "storage_latched"
          }
        >
          {mutation.state === "submitting" ? "Saving…" : "Save description"}
        </button>
      </form>
    </section>
  );
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
  const defaultGrant =
    grant.effect === "allow" &&
    grant.serverID === "00000000000000000000000000" &&
    grant.upstreamName === null &&
    grant.constraint === null &&
    grant.expiresAt === null;
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (!defaultGrant) return;
    let current = true;
    void principalVisibility(session, grant.principalID).then((value) => {
      if (current) setVisibility(value);
    });
    return () => {
      current = false;
    };
  }, [grant.principalID, defaultGrant]);

  const createSpec = (): MutationSpec<GrantActionResult> => {
    const body = JSON.stringify({
      description: grant.description,
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
  const refreshPolicy = () => readGrants(session);
  const confirm = async () => {
    setConfirming(false);
    const outcome = await controller.submit();
    if (outcome.kind !== "acknowledged") return;
    await refreshPolicy();
    controller.abandon();
    if (!correction) {
      window.location.hash = "#/grants";
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
      destination === undefined ? "#/grants" : `#/grants/${destination}`;
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
    visibility === undefined
      ? "Deleting this default grant removes the principal's access to Gateway self-service tools. It is not restored automatically."
      : `Deleting this default grant removes the principal's access to Gateway self-service tools. The principal's ${visibility} visibility does not restore authorization.`;
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
          <h2 id="grant-actions-title">Delete or replace this grant</h2>
        </div>
      </div>
      {defaultGrant && (
        <StateNotice state="warning" title="Default Gateway access consequence">
          <p>{defaultWarning}</p>
          <p>Default Gateway access can be deleted but not replaced.</p>
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
          <p>No later replacement step was submitted.</p>
        </StateNotice>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Grant mutation outcome is unknown">
          <p>
            Do not replay and do not submit the later replacement step. Refresh
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
            class="danger-action"
            data-testid="grant-correct"
            type="button"
            disabled={
              disabled ||
              grant.constraint !== null ||
              grant.expiresAt !== null ||
              defaultGrant
            }
            onClick={() => setCorrection(true)}
          >
            Replace grant
          </button>
        </div>
      ) : (
        <div data-testid="grant-correction">
          {phase === "configure" && (
            <>
              <FormField id="correction-order" label="Replacement order">
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
                      Create replacement before delete — temporary overlap
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
                    <option value="allow">Allow</option>
                    <option value="deny">Deny</option>
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
                Cancel replacement
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
              : "This permanently removes this policy record. No replacement or later step is automatic."}
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
  const [principals, setPrincipals] = useState<Principal[]>();
  const [servers, setServers] = useState<ServerView[]>();
  const [error, setError] = useState<string>();
  useEffect(() => {
    let current = true;
    void Promise.all([readPrincipals(session), readServers(session)])
      .then(([nextPrincipals, nextServers]) => {
        if (!current) return;
        setPrincipals(nextPrincipals);
        setServers(nextServers);
      })
      .catch((caught: unknown) => {
        if (current)
          setError(
            caught instanceof Error
              ? caught.message
              : "Grant references are unavailable.",
          );
      });
    return () => {
      current = false;
    };
  }, [view.generation]);
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
      void readGrants(session)
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
  if (create) {
    if (principals === undefined || servers === undefined)
      return <StateNotice state="loading" title="Loading grant options" />;
    return (
      <GrantCreate
        session={session}
        mutations={mutations}
        query={resolved.location.query}
        principals={principals}
        servers={servers}
      />
    );
  }
  if (error !== undefined)
    return (
      <StateNotice state="error" title="Grant data unavailable">
        <p>{error}</p>
      </StateNotice>
    );
  if (grantID !== undefined) {
    if (
      detail?.id !== grantID ||
      principals === undefined ||
      servers === undefined
    )
      return <StateNotice state="loading" title="Loading grant" />;
    const principal = principals.find(
      (candidate) => candidate.id === detail.principalID,
    );
    const server = servers.find(
      (candidate) => candidate.id === detail.serverID,
    );
    return (
      <div class="domain-view" data-testid="grant-detail">
        <nav class="detail-navigation" aria-label="Grant navigation">
          <a href="#/grants">Back to grants</a>
        </nav>
        <header class="detail-context" data-testid="detail-context">
          <div class="detail-context-heading">
            <h1 id="grant-page-title" tabindex={-1}>
              Grant {detail.id}
            </h1>
          </div>
        </header>
        <section class="panel domain-panel" aria-labelledby="grant-title">
          <div class="panel-heading">
            <h2 id="grant-title">Grant details</h2>
            <StatusLabel
              state={detail.state === "active" ? "current" : "warning"}
            >
              {detail.state === "active" ? "Active" : "Expired"}
            </StatusLabel>
          </div>
          <dl class="fact-grid">
            <div>
              <dt>Grant ID</dt>
              <dd>{detail.id}</dd>
            </div>
            <div>
              <dt>Effect</dt>
              <dd>{detail.effect === "allow" ? "Allow" : "Deny"}</dd>
            </div>
            <div>
              <dt>Description</dt>
              <dd>{detail.description ?? "—"}</dd>
            </div>
            <div>
              <dt>Principal</dt>
              <dd>
                <a href={`#/principals/${detail.principalID}`}>
                  {principal?.displayName ?? `Principal ${detail.principalID}`}
                </a>
              </dd>
            </div>
            <div>
              <dt>Server</dt>
              <dd>
                {detail.serverID === "00000000000000000000000000" ? (
                  "Gateway self-service tools"
                ) : (
                  <a href={`#/servers/${detail.serverID}?tab=tools`}>
                    {server?.displayName ?? `Server ${detail.serverID}`}
                  </a>
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
        <GrantDescriptionEditor
          key={`${detail.id}-${detail.revision}`}
          mutations={mutations}
          grant={detail}
          onAcknowledged={setDetail}
        />
        <GrantActions
          key={detail.id}
          session={session}
          mutations={mutations}
          grant={detail}
        />
      </div>
    );
  }
  if (items === undefined || principals === undefined || servers === undefined)
    return <StateNotice state="loading" title="Loading grants" />;
  const principalNames = new Map(
    principals.map((principal) => [principal.id, principal.displayName]),
  );
  const serverNames = new Map(
    servers.map((server) => [server.id, server.displayName]),
  );
  return (
    <div class="domain-view" data-testid="grants-view">
      <div class="collection-toolbar">
        <a
          class="button-link create-action"
          href="#/grants/new"
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
                key: "identity",
                label: "Description or ID",
                type: "text",
                value: (grant) => grant.description ?? "",
                literalValues: (grant) => [grant.id],
              },
              {
                key: "principal",
                label: "Principal",
                type: "text",
                value: (grant) => principalNames.get(grant.principalID) ?? "",
                literalValues: (grant) => [grant.principalID],
              },
              {
                key: "target",
                label: "Target",
                type: "text",
                value: (grant) =>
                  `${serverNames.get(grant.serverID) ?? "Gateway self-service tools"} ${grant.upstreamName ?? "Entire server"}`,
                literalValues: (grant) => [grant.serverID],
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
                key: "id",
                label: "ID",
                sortValue: (grant) => grant.id,
                render: (grant) => (
                  <a href={`#/grants/${grant.id}`}>{grant.id}</a>
                ),
              },
              {
                key: "description",
                label: "Description",
                sortValue: (grant) => grant.description ?? "",
                render: (grant) =>
                  grant.description === null
                    ? "—"
                    : grant.description.length > 64
                      ? `${grant.description.slice(0, 61)}…`
                      : grant.description,
              },
              {
                key: "principal",
                label: "Principal",
                sortValue: (grant) =>
                  principalNames.get(grant.principalID) ?? grant.principalID,
                render: (grant) => (
                  <a href={`#/principals/${grant.principalID}`}>
                    {principalNames.get(grant.principalID) ??
                      `Principal ${grant.principalID}`}
                  </a>
                ),
              },
              {
                key: "target",
                label: "Target",
                sortValue: (grant) =>
                  serverNames.get(grant.serverID) ?? grant.serverID,
                render: (grant) =>
                  grant.serverID === "00000000000000000000000000" ? (
                    "Gateway self-service tools"
                  ) : (
                    <a href={`#/servers/${grant.serverID}?tab=tools`}>
                      {serverNames.get(grant.serverID) ??
                        `Server ${grant.serverID}`}
                      {grant.upstreamName === null
                        ? " — All tools"
                        : ` — ${grant.upstreamName}`}
                    </a>
                  ),
              },
              {
                key: "effect",
                label: "Effect",
                sortValue: (grant) => grant.effect,
                render: (grant) => (
                  <strong>{grant.effect === "allow" ? "Allow" : "Deny"}</strong>
                ),
              },
              {
                key: "state",
                label: "Status",
                sortValue: (grant) => grant.state,
                render: (grant) => (
                  <StatusLabel
                    state={grant.state === "active" ? "current" : "warning"}
                  >
                    {grant.state === "active" ? "Active" : "Expired"}
                  </StatusLabel>
                ),
              },
            ]}
          />
        )}
      </section>
    </div>
  );
}
