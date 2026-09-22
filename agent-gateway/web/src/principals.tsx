import { useEffect, useRef, useState } from "preact/hooks";
import { parseFragment, type ResolvedLocation } from "./location";
import { useUnsavedChanges } from "./navigation";
import {
  type MutationController,
  type MutationCoordinator,
  type MutationOutcome,
  type MutationSnapshot,
  type MutationSpec,
} from "./mutation";
import {
  BinaryToggle,
  CollectionTable,
  TableIdentity,
  ConfirmationDialog,
  FormField,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { ProtectedContext, SessionClient } from "./session";
import type { PreparedOneTimeSink, SensitiveSinkCoordinator } from "./sinks";
import { UserTime } from "./time";
import {
  readCollectionPage,
  useCollectionPage,
  type ViewCoordinator,
  type ViewSnapshot,
} from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const decimal = /^(0|[1-9][0-9]*)$/;
type PrincipalState = "active" | "disabled";
type PrincipalVisibility = "requestable" | "allowed-only" | "all";
type JSONRecord = Record<string, unknown>;

interface AgentCredential {
  id: string;
  fingerprint: string;
  revision: string;
  createdAt: string;
}

export interface Principal {
  id: string;
  displayName: string;
  state: PrincipalState;
  visibility: PrincipalVisibility;
  httpDefault: "allow" | "block";
  revision: string;
  credentialRevision: string;
  credential: AgentCredential | null;
  hasCredential: boolean;
  createdAt: string;
  updatedAt: string;
}
interface PrincipalDetail {
  principal: Principal;
  etag: string;
}
interface PrincipalCreation {
  principal: Principal;
  defaultGrantID: string;
}
interface CredentialCreation {
  principal: Principal;
  bearer: string;
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
function identifier(value: unknown): string {
  const valueText = text(value);
  if (!gatewayID.test(valueText)) throw new Error("invalid response");
  return valueText;
}
function revision(value: unknown): string {
  const valueText = text(value);
  if (!decimal.test(valueText)) throw new Error("invalid response");
  return valueText;
}
function closed<T extends string>(value: unknown, values: readonly T[]): T {
  const valueText = text(value);
  if (!values.includes(valueText as T)) throw new Error("invalid response");
  return valueText as T;
}
function decodePrincipal(value: unknown): Principal {
  const item = record(value, [
    "id",
    "display_name",
    "state",
    "visibility",
    "http_default",
    "revision",
    "credential_revision",
    "credential",
    "created_at",
    "updated_at",
  ]);
  const credential =
    item.credential === null
      ? null
      : record(item.credential, [
          "id",
          "fingerprint",
          "revision",
          "created_at",
        ]);
  return {
    id: identifier(item.id),
    displayName: text(item.display_name),
    state: closed(item.state, ["active", "disabled"]),
    visibility: closed(item.visibility, ["requestable", "allowed-only", "all"]),
    httpDefault: closed(item.http_default, ["allow", "block"]),
    revision: revision(item.revision),
    credentialRevision: revision(item.credential_revision),
    credential:
      credential === null
        ? null
        : {
            id: identifier(credential.id),
            fingerprint: text(credential.fingerprint),
            revision: revision(credential.revision),
            createdAt: text(credential.created_at),
          },
    hasCredential: credential !== null,
    createdAt: text(item.created_at),
    updatedAt: text(item.updated_at),
  };
}
function decodeCreation(value: unknown): PrincipalCreation {
  const creation = record(value, ["principal", "default_grant"]);
  const grant = record(creation.default_grant, [
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
  const principal = decodePrincipal(creation.principal);
  if (
    grant.description !== "Default Gateway access" ||
    revision(grant.revision) !== "1" ||
    identifier(grant.principal_id) !== principal.id ||
    grant.effect !== "allow" ||
    grant.server_id !== "00000000000000000000000000" ||
    grant.upstream_name !== null ||
    grant.constraint !== null ||
    grant.expires_at !== null ||
    grant.state !== "active"
  )
    throw new Error("invalid response");
  return { principal, defaultGrantID: identifier(grant.id) };
}
function requestHeaders(context: ProtectedContext): HeadersInit {
  return { Accept: "application/json", "X-CSRF-Token": context.csrfToken };
}
async function readJSON(
  session: SessionClient,
  route: string,
): Promise<{ value: unknown; response: Response } | undefined> {
  return session.runProtected(async (context) => {
    const response = await fetch(route, {
      credentials: "same-origin",
      redirect: "error",
      signal: context.signal,
      headers: requestHeaders(context),
    });
    if (await context.sessionLost(response)) return undefined;
    if (
      response.headers.get("Content-Type") !== "application/json" &&
      response.headers.get("Content-Type") !== "application/problem+json"
    )
      throw new Error("Principal data is unavailable.");
    return { value: (await response.json()) as unknown, response };
  });
}
export async function readPrincipals(
  session: SessionClient,
): Promise<Principal[]> {
  const items: Principal[] = [];
  let cursor: string | null = null;
  let restarted = false;
  for (;;) {
    const route = `/api/v2/principals?limit=50${cursor === null ? "" : `&cursor=${encodeURIComponent(cursor)}`}`;
    const result = await readJSON(session, route);
    if (result === undefined) return [];
    if (result.response.status === 409 && cursor !== null && !restarted) {
      items.length = 0;
      cursor = null;
      restarted = true;
      continue;
    }
    if (!result.response.ok) throw new Error("Principal data is unavailable.");
    const page = record(result.value, [
      "items",
      "next_cursor",
      "total_count",
      "offset",
    ]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    items.push(...page.items.map(decodePrincipal));
    if (page.next_cursor === null) return items;
    cursor = text(page.next_cursor);
    if (cursor.length === 0 || cursor.length > 4096)
      throw new Error("invalid response");
  }
}

export class PrincipalDirectory {
  private readonly listeners = new Set<
    (principals: ReadonlyMap<string, string>) => void
  >();
  private principals: ReadonlyMap<string, string> = new Map();

  constructor(session: SessionClient, views: ViewCoordinator) {
    views.registerPanel({
      id: "principal-directory",
      matches: (key) =>
        key === "#/overview" ||
        ["invocations", "http-traffic"].includes(
          parseFragment(key)?.destination ?? "",
        ),
      invalidations: ["authorization"],
      read: () => readPrincipals(session),
      publish: (principals) => {
        this.principals = new Map(
          principals.map((principal) => [principal.id, principal.displayName]),
        );
        this.emit();
      },
    });
    session.registerProtectedState(() => {
      this.principals = new Map();
      this.emit();
    });
  }

  snapshot(): ReadonlyMap<string, string> {
    return this.principals;
  }

  subscribe(
    listener: (principals: ReadonlyMap<string, string>) => void,
  ): () => void {
    this.listeners.add(listener);
    listener(this.principals);
    return () => this.listeners.delete(listener);
  }

  private emit(): void {
    for (const listener of this.listeners) listener(this.principals);
  }
}

async function readPrincipal(
  session: SessionClient,
  id: string,
): Promise<PrincipalDetail | undefined> {
  const result = await readJSON(session, `/api/v2/principals/${id}`);
  if (result === undefined) return undefined;
  if (!result.response.ok) throw new Error("Principal data is unavailable.");
  const principal = decodePrincipal(result.value);
  const etag = result.response.headers.get("ETag");
  if (principal.id !== id || etag !== `"principal-${id}-${principal.revision}"`)
    throw new Error("The current principal revision is unavailable.");
  return { principal, etag };
}
async function decodeMutationPrincipal(response: Response): Promise<Principal> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid response");
  const principal = decodePrincipal((await response.json()) as unknown);
  if (
    response.headers.get("ETag") !==
    `"principal-${principal.id}-${principal.revision}"`
  )
    throw new Error("invalid principal revision");
  return principal;
}
async function decodeCredentialCreation(
  response: Response,
): Promise<CredentialCreation> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid response");
  const value = record((await response.json()) as unknown, [
    "principal",
    "bearer",
  ]);
  const bearer = text(value.bearer);
  if (!/^mgw_agent_[A-Za-z0-9_-]{43}$/.test(bearer))
    throw new Error("invalid response");
  return { principal: decodePrincipal(value.principal), bearer };
}
async function decodeMutationCreation(
  response: Response,
): Promise<PrincipalCreation> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid response");
  return decodeCreation((await response.json()) as unknown);
}

function visibilityText(value: PrincipalVisibility): string {
  return value === "requestable"
    ? "Requestable"
    : value === "allowed-only"
      ? "Allowed tools only"
      : "All tools";
}

function PrincipalEditor({
  mutations,
  detail,
  onRefresh,
  notify,
  readUnavailable = false,
}: {
  readUnavailable?: boolean;
  mutations: MutationCoordinator;
  detail?: PrincipalDetail;
  onRefresh: () => void;
  notify: (message: string) => void;
}) {
  const create = detail === undefined;
  const principal = detail?.principal;
  const initialDraft = useRef({
    displayName: principal?.displayName ?? "",
    state: principal?.state ?? ("active" as PrincipalState),
    visibility: principal?.visibility ?? ("requestable" as PrincipalVisibility),
    httpDefault: principal?.httpDefault ?? ("block" as const),
  });
  const [displayName, setDisplayName] = useState(
    initialDraft.current.displayName,
  );
  const [state, setState] = useState<PrincipalState>(
    initialDraft.current.state,
  );
  const [visibility, setVisibility] = useState<PrincipalVisibility>(
    initialDraft.current.visibility,
  );
  const [httpDefault, setHTTPDefault] = useState<"allow" | "block">(
    initialDraft.current.httpDefault,
  );
  const [expected, setExpected] = useState(detail?.etag);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [controller] = useState<
    MutationController<Principal | PrincipalCreation>
  >(() => mutations.create<Principal | PrincipalCreation>());
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const submitButton = useRef<HTMLButtonElement>(null);
  const dirty =
    JSON.stringify({ displayName, state, visibility, httpDefault }) !==
    JSON.stringify(initialDraft.current);
  const stale = !create && (readUnavailable || expected !== detail?.etag);
  const navigate = useUnsavedChanges(dirty);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (
      principal === undefined ||
      dirty ||
      mutation.state === "uncertain" ||
      mutation.requiresRefresh
    )
      return;
    initialDraft.current = {
      displayName: principal.displayName,
      state: principal.state,
      visibility: principal.visibility,
      httpDefault: principal.httpDefault,
    };
    setDisplayName(principal.displayName);
    setState(principal.state);
    setVisibility(principal.visibility);
    setHTTPDefault(principal.httpDefault);
    setExpected(detail?.etag);
  }, [principal?.id, detail?.etag]);

  const settle = async (
    promise: Promise<MutationOutcome<Principal | PrincipalCreation>>,
  ) => {
    const outcome = await promise;
    if (outcome.kind === "acknowledged") {
      controller.abandon();
      const saved =
        "principal" in outcome.value ? outcome.value.principal : outcome.value;
      initialDraft.current = {
        displayName: saved.displayName,
        state: saved.state,
        visibility: saved.visibility,
        httpDefault: saved.httpDefault,
      };
      setDisplayName(saved.displayName);
      setState(saved.state);
      setVisibility(saved.visibility);
      setHTTPDefault(saved.httpDefault);
      setExpected(`"principal-${saved.id}-${saved.revision}"`);
      if (create) {
        notify(
          "Principal created; MCP discovery visibility saved. Ordinary grant added for six fixed MCP self-service tools, not downstream tools or future protocols.",
        );
        navigate(`#/principals/${saved.id}`, true);
      } else {
        setNotice("Principal settings saved.");
        onRefresh();
      }
    }
  };
  const prepare = ():
    | { spec: MutationSpec<Principal | PrincipalCreation>; authority: boolean }
    | undefined => {
    setError(undefined);
    setNotice(undefined);
    if (
      displayName.length === 0 ||
      new TextEncoder().encode(displayName).length > 256
    ) {
      setError("Display name must contain 1–256 UTF-8 bytes.");
      return undefined;
    }
    if (create)
      return {
        authority: false,
        spec: {
          route: "/api/v2/principals",
          method: "POST",
          body: JSON.stringify({ display_name: displayName, visibility }),
          precondition: null,
          requiresPrecondition: false,
          idempotency: "none",
          successStatuses: [201],
          decode: decodeMutationCreation,
        },
      };
    if (
      detail === undefined ||
      stale ||
      mutation.state === "uncertain" ||
      mutation.requiresRefresh
    )
      return undefined;
    const current = detail.principal;
    const baseline = initialDraft.current;
    const patch: Record<string, string> = {};
    if (displayName !== baseline.displayName) patch.display_name = displayName;
    if (state !== baseline.state) patch.state = state;
    if (visibility !== baseline.visibility) patch.visibility = visibility;
    if (httpDefault !== baseline.httpDefault) patch.http_default = httpDefault;
    if (Object.keys(patch).length === 0) {
      setError("Change at least one principal field.");
      return undefined;
    }
    return {
      authority: patch.state !== undefined || patch.http_default !== undefined,
      spec: {
        route: `/api/v2/principals/${current.id}`,
        method: "PATCH",
        body: JSON.stringify(patch),
        precondition: expected ?? null,
        requiresPrecondition: true,
        idempotency: "none",
        successStatuses: [200],
        decode: async (response) => {
          const saved = await decodeMutationPrincipal(response);
          if (saved.id !== current.id)
            throw new Error("invalid principal identity");
          return saved;
        },
      },
    };
  };
  const start = () => {
    const prepared = prepare();
    if (prepared === undefined) return;
    controller.begin(prepared.spec);
    if (create || prepared.authority) controller.confirm();
    else void settle(controller.submit());
  };
  const disabled =
    mutation.state === "submitting" ||
    mutation.state === "confirming" ||
    mutation.availability === "storage_latched";

  return (
    <section
      class="panel domain-panel"
      aria-labelledby="principal-editor-title"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">
            {create ? "PRINCIPAL NEW" : "PRINCIPAL EDIT"}
          </span>
          <h2 id="principal-editor-title">
            {create ? "Create principal" : "Edit principal"}
          </h2>
        </div>
      </div>
      {create && (
        <>
          <p>
            Creating a principal also adds the ordinary Default Gateway access
            grant for Gateway's six fixed MCP self-service tools, not downstream
            tools or future protocols.
          </p>
          <p class="bounded-note">
            Gateway generates the principal ID as a permanent identity. The
            display name and MCP discovery visibility can be changed later.
          </p>
        </>
      )}
      <form
        id="principal-editor-form"
        data-testid="principal-editor"
        onSubmit={(event) => {
          event.preventDefault();
          start();
        }}
      >
        <FormField id="principal-display-name" label="Display name" required>
          {(attributes) => (
            <input
              {...attributes}
              data-testid="principal-display-name"
              value={displayName}
              disabled={disabled}
              onInput={(event) => setDisplayName(event.currentTarget.value)}
              required
            />
          )}
        </FormField>
        {!create && (
          <FormField
            id="principal-state"
            label="Principal enabled"
            hint="Disabling immediately removes current credential authority."
          >
            {(attributes) => (
              <BinaryToggle
                attributes={attributes}
                checked={state === "active"}
                disabled={disabled}
                testID="principal-state"
                onChange={(checked) =>
                  setState(checked ? "active" : "disabled")
                }
              />
            )}
          </FormField>
        )}
        <FormField
          id="principal-visibility"
          label="MCP discovery visibility"
          hint="Discovery visibility grants no access; MCP grants remain authoritative."
        >
          {(attributes) => (
            <select
              {...attributes}
              data-testid="principal-visibility"
              value={visibility}
              disabled={disabled}
              onChange={(event) =>
                setVisibility(event.currentTarget.value as PrincipalVisibility)
              }
            >
              <option value="requestable">Requestable</option>
              <option value="allowed-only">Allowed tools only</option>
              <option value="all">All tools</option>
            </select>
          )}
        </FormField>
        {!create && (
          <FormField
            id="principal-http-default"
            label="HTTP default"
            hint="Default allow supplies no credential, tunnel permission or local/private access."
          >
            {(attributes) => (
              <select
                {...attributes}
                value={httpDefault}
                disabled={disabled}
                onChange={(event) =>
                  setHTTPDefault(event.currentTarget.value as "allow" | "block")
                }
              >
                <option value="block">Block</option>
                <option value="allow">Allow requests</option>
              </select>
            )}
          </FormField>
        )}
      </form>
      {(stale || mutation.requiresRefresh || mutation.state === "uncertain") &&
        detail !== undefined && (
          <StateNotice
            state="warning"
            title="Review current principal settings"
          >
            <p>
              Your draft is preserved. Current values:{" "}
              {detail.principal.displayName}; {detail.principal.state};{" "}
              {visibilityText(detail.principal.visibility)}; HTTP default{" "}
              {detail.principal.httpDefault}.
            </p>
            <div class="form-actions">
              <button type="button" onClick={onRefresh}>
                Refresh current settings
              </button>
              {mutation.state !== "uncertain" && (
                <button
                  type="button"
                  disabled={readUnavailable}
                  onClick={() => {
                    const current = detail.principal;
                    const baseline = initialDraft.current;
                    if (displayName === baseline.displayName)
                      setDisplayName(current.displayName);
                    if (state === baseline.state) setState(current.state);
                    if (visibility === baseline.visibility)
                      setVisibility(current.visibility);
                    if (httpDefault === baseline.httpDefault)
                      setHTTPDefault(current.httpDefault);
                    initialDraft.current = {
                      displayName: current.displayName,
                      state: current.state,
                      visibility: current.visibility,
                      httpDefault: current.httpDefault,
                    };
                    setExpected(detail.etag);
                    controller.abandon();
                  }}
                >
                  Use reviewed revision; keep draft
                </button>
              )}
              {mutation.state === "uncertain" && (
                <button
                  type="button"
                  disabled={readUnavailable}
                  onClick={() => {
                    const current = detail.principal;
                    initialDraft.current = {
                      displayName: current.displayName,
                      state: current.state,
                      visibility: current.visibility,
                      httpDefault: current.httpDefault,
                    };
                    setDisplayName(current.displayName);
                    setState(current.state);
                    setVisibility(current.visibility);
                    setHTTPDefault(current.httpDefault);
                    setExpected(detail.etag);
                    controller.abandon();
                    setNotice(undefined);
                  }}
                >
                  Discard uncertain draft; use current settings
                </button>
              )}
            </div>
          </StateNotice>
        )}
      {error !== undefined && (
        <StateNotice state="error" title="Check principal configuration">
          <p>{error}</p>
        </StateNotice>
      )}
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title}>
          {mutation.requiresRefresh && (
            <p>
              Review current settings and the preserved draft before accepting a
              refreshed revision.
            </p>
          )}
        </StateNotice>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Principal outcome is unknown">
          <p>
            Do not replay this non-idempotent change. Refresh the principal and
            authorization state to investigate.
          </p>
        </StateNotice>
      )}
      {notice !== undefined && <StateNotice state="empty" title={notice} />}
      <button
        ref={submitButton}
        class={`${create ? "create-action" : "safe-action"} form-submit-action`}
        data-testid="principal-editor-submit"
        form="principal-editor-form"
        type="submit"
        disabled={
          disabled ||
          stale ||
          mutation.requiresRefresh ||
          mutation.state === "uncertain"
        }
      >
        {mutation.state === "submitting"
          ? "Submitting…"
          : create
            ? "Review and create"
            : "Save principal"}
      </button>
      <ConfirmationDialog
        id="principal-change-confirm"
        open={mutation.state === "confirming"}
        title={create ? "Review principal" : "Review principal changes"}
        consequence={
          create ? (
            <div class="review-stack">
              <p>
                Review this permanent identity and its initial MCP self-service
                access before creating it. Discovery visibility grants no
                access; MCP grants remain authoritative.
              </p>
              <dl class="fact-grid">
                <div>
                  <dt>Display name</dt>
                  <dd>{displayName}</dd>
                </div>
                <div>
                  <dt>MCP discovery visibility</dt>
                  <dd>{visibilityText(visibility)}</dd>
                </div>
                <div>
                  <dt>Initial MCP self-service access</dt>
                  <dd>
                    Default Gateway access — an ordinary grant for Gateway's six
                    fixed MCP self-service tools, not downstream tools or future
                    protocols.
                  </dd>
                </div>
              </dl>
            </div>
          ) : (
            <div class="review-stack">
              <dl class="fact-grid">
                {displayName !== initialDraft.current.displayName && (
                  <div>
                    <dt>Display name</dt>
                    <dd>{displayName}</dd>
                  </div>
                )}
                {state !== initialDraft.current.state && (
                  <div>
                    <dt>Principal enabled</dt>
                    <dd>{state === "active" ? "Enabled" : "Disabled"}</dd>
                  </div>
                )}
                {visibility !== initialDraft.current.visibility && (
                  <div>
                    <dt>MCP discovery visibility</dt>
                    <dd>{visibilityText(visibility)}</dd>
                  </div>
                )}
                {httpDefault !== initialDraft.current.httpDefault && (
                  <div>
                    <dt>HTTP default</dt>
                    <dd>{httpDefault}</dd>
                  </div>
                )}
              </dl>
              {state !== initialDraft.current.state && (
                <p>
                  {state === "disabled"
                    ? "Disabling revokes current agent authority and interrupts credential-bound sessions and streams."
                    : "Re-enabling does not restore revoked credentials or removed MCP self-service access."}
                </p>
              )}
              {httpDefault !== initialDraft.current.httpDefault && (
                <p>
                  Explicit blocks still win. Default allow supplies no
                  credential, tunnel permission or local/private access.
                </p>
              )}
            </div>
          )
        }
        confirmLabel={create ? "Create principal" : "Save principal changes"}
        destructive={!create && state === "disabled"}
        returnFocus={submitButton}
        onCancel={() => controller.abandon()}
        onConfirm={() => void settle(controller.submit())}
      />
    </section>
  );
}

function PrincipalCredentialActions({
  mutations,
  sinks,
  detail,
  onRefresh,
}: {
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  detail: PrincipalDetail;
  onRefresh: () => void;
}) {
  const principal = detail.principal;
  const [controller] = useState<
    MutationController<CredentialCreation | Principal>
  >(() => mutations.create<CredentialCreation | Principal>());
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [action, setAction] = useState<"issue" | "rotate" | "revoke">("issue");
  const [prepared, setPrepared] = useState<PreparedOneTimeSink>();
  const [blockedETag, setBlockedETag] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const issueButton = useRef<HTMLButtonElement>(null);
  const revokeButton = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => () => prepared?.cancel(), [prepared]);

  const beginIssue = () => {
    setNotice(undefined);
    const spec: MutationSpec<CredentialCreation | Principal> = {
      route: `/api/v2/principals/${principal.id}/credential`,
      method: "POST",
      body: "{}",
      precondition: detail.etag,
      requiresPrecondition: true,
      idempotency: "none",
      successStatuses: [201],
      decode: decodeCredentialCreation,
    };
    setAction(principal.hasCredential ? "rotate" : "issue");
    setPrepared(undefined);
    controller.begin(spec);
    controller.confirm();
  };
  const beginRevoke = () => {
    setNotice(undefined);
    const spec: MutationSpec<CredentialCreation | Principal> = {
      route: `/api/v2/principals/${principal.id}/credential`,
      method: "DELETE",
      body: "{}",
      precondition: detail.etag,
      requiresPrecondition: true,
      idempotency: "none",
      successStatuses: [200],
      decode: decodeMutationPrincipal,
    };
    setAction("revoke");
    setPrepared(undefined);
    controller.begin(spec);
    controller.confirm();
  };
  const cancel = () => {
    prepared?.cancel();
    setPrepared(undefined);
    controller.abandon();
  };
  const confirm = async () => {
    let activeSink = prepared;
    if (action !== "revoke" && activeSink === undefined) {
      activeSink = sinks.prepareOneTime(
        `${principal.hasCredential ? "Replacement" : "New"} agent bearer for ${principal.displayName}`,
      );
      if (activeSink === undefined) {
        setNotice(
          "The protected one-time display could not be prepared. No credential was changed.",
        );
        controller.abandon();
        return;
      }
    }
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged") {
      setBlockedETag(undefined);
      if (action !== "revoke") {
        if (!("bearer" in outcome.value)) throw new Error("invalid response");
        const publication = activeSink?.publish(outcome.value.bearer) ?? "lost";
        setNotice(
          publication === "published"
            ? "The one-time agent bearer is ready. It cannot be revealed again."
            : action === "rotate"
              ? "The replacement may now be current and the prior bearer may already be invalid. Review principal metadata, then explicitly rotate or revoke the lost current credential. Do not replay the operation."
              : "A current credential may now occupy the slot, but its bearer was lost and cannot be recovered. Review principal metadata, then explicitly rotate or revoke it. Do not replay issue.",
        );
      } else {
        setNotice(
          "Agent credential revoked. Prior authority no longer authenticates.",
        );
      }
      setPrepared(undefined);
      controller.abandon();
      onRefresh();
      return;
    }
    if (outcome.kind === "rejected" && outcome.requiresRefresh) {
      setBlockedETag(detail.etag);
      onRefresh();
    }
    if (action !== "revoke") {
      if (outcome.kind === "uncertain") activeSink?.lose();
      else activeSink?.cancel();
    }
    setPrepared(undefined);
  };
  const disabled =
    principal.state !== "active" ||
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched" ||
    blockedETag === detail.etag;
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="principal-credential-title"
      data-testid="principal-credential-actions"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">AGENT AUTHORITY</span>
          <h2 id="principal-credential-title">Agent credential</h2>
        </div>
        <StatusLabel state={principal.hasCredential ? "current" : "empty"}>
          {principal.hasCredential ? "Issued" : "Not issued"}
        </StatusLabel>
      </div>
      <p>
        Only one agent credential may be active at a time. Rotating it replaces
        the current credential.
      </p>
      {principal.credential !== null && (
        <dl class="fact-grid">
          <div>
            <dt>Credential ID</dt>
            <dd class="technical-value">{principal.credential.id}</dd>
          </div>
          <div>
            <dt>Fingerprint</dt>
            <dd class="technical-value">{principal.credential.fingerprint}</dd>
          </div>
          <div>
            <dt>Issued</dt>
            <dd>
              <UserTime value={principal.credential.createdAt} />
            </dd>
          </div>
          <div>
            <dt>Credential revision</dt>
            <dd>{principal.credential.revision}</dd>
          </div>
        </dl>
      )}
      {principal.state !== "active" && (
        <StateNotice state="unavailable" title="Principal is disabled">
          <p>Re-enable the principal before issuing agent authority.</p>
        </StateNotice>
      )}
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title}>
          {mutation.requiresRefresh && (
            <p>
              The current principal revision was reloaded. Review current
              authority before trying a new explicit action.
            </p>
          )}
        </StateNotice>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Credential outcome is unknown">
          <p>
            {action === "rotate"
              ? "Do not replay. The replacement may be current and the prior bearer may already be invalid. Refresh the principal, then explicitly rotate or revoke the observed current credential."
              : action === "issue"
                ? "Do not replay issue. A current credential may occupy the slot while its bearer is permanently lost. Refresh the principal, then explicitly rotate or revoke the observed credential."
                : "Do not replay revoke. Authority may already be revoked. Refresh the principal before another explicit action."}
          </p>
        </StateNotice>
      )}
      {notice !== undefined && <StateNotice state="empty" title={notice} />}
      <div class="inline-actions">
        <button
          ref={issueButton}
          class={principal.hasCredential ? "danger-action" : "create-action"}
          data-testid="principal-credential-issue"
          type="button"
          disabled={disabled}
          onClick={beginIssue}
        >
          {principal.hasCredential ? "Rotate credential" : "Issue credential"}
        </button>
        {principal.hasCredential && (
          <button
            ref={revokeButton}
            class="danger-action"
            data-testid="principal-credential-revoke"
            type="button"
            disabled={disabled}
            onClick={beginRevoke}
          >
            Revoke credential
          </button>
        )}
      </div>
      <ConfirmationDialog
        id="principal-credential-confirm"
        open={mutation.state === "confirming"}
        title={
          action === "rotate"
            ? "Rotate agent credential?"
            : action === "issue"
              ? "Issue agent credential?"
              : "Revoke agent credential?"
        }
        consequence={
          action === "rotate" ? (
            <p>
              Rotating immediately replaces current agent authority. The new
              bearer is displayed once.
            </p>
          ) : action === "issue" ? (
            <p>
              The new bearer is displayed once and becomes this principal's
              current authority.
            </p>
          ) : (
            <p>
              The current bearer, authenticated sessions, and streams stop
              authorizing this principal.
            </p>
          )
        }
        confirmLabel={
          action === "rotate"
            ? "Rotate credential"
            : action === "issue"
              ? "Issue credential"
              : "Revoke credential"
        }
        destructive={action === "revoke"}
        returnFocus={action !== "revoke" ? issueButton : revokeButton}
        onCancel={cancel}
        onConfirm={() => void confirm()}
      />
    </section>
  );
}

export function Principals({
  session,
  mutations,
  sinks,
  resolved,
  view,
  onRefresh,
  notify,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
  onRefresh: () => void;
  notify: (message: string) => void;
}) {
  const segments = resolved.location.segments;
  const newPrincipal = segments[1] === "new";
  const principalID =
    segments.length === 2 && !newPrincipal ? segments[1] : undefined;
  const [detail, setDetail] = useState<PrincipalDetail>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    let current = true;
    setError(undefined);
    if (newPrincipal || principalID === undefined)
      return () => {
        current = false;
      };
    if (principalID !== undefined) {
      setDetail((current) =>
        current?.principal.id === principalID ? current : undefined,
      );
      void readPrincipal(session, principalID)
        .then((value) => {
          if (current && value !== undefined) setDetail(value);
        })
        .catch((caught: unknown) => {
          if (current)
            setError(
              caught instanceof Error
                ? caught.message
                : "Principal data is unavailable.",
            );
        });
    }
    return () => {
      current = false;
    };
  }, [resolved.canonicalFragment, view.generation]);

  if (newPrincipal)
    return (
      <div class="domain-view" data-testid="principal-create-view">
        <PrincipalEditor
          mutations={mutations}
          onRefresh={onRefresh}
          notify={notify}
        />
      </div>
    );
  if (
    principalID !== undefined &&
    error !== undefined &&
    detail?.principal.id !== principalID
  )
    return (
      <StateNotice state="error" title="Principal data unavailable">
        <p>{error}</p>
      </StateNotice>
    );
  if (principalID !== undefined) {
    if (detail?.principal.id !== principalID)
      return <StateNotice state="loading" title="Loading principal" />;
    const principal = detail.principal;
    return (
      <div class="domain-view" data-testid="principal-detail">
        {error !== undefined && (
          <StateNotice state="error" title="Current principal data unavailable">
            <p>{error} Your draft is preserved; refresh before saving.</p>
          </StateNotice>
        )}
        <nav class="detail-navigation" aria-label="Principal navigation">
          <a href="#/principals">Back to principals</a>
        </nav>
        <header class="detail-context" data-testid="detail-context">
          <div class="detail-context-heading">
            <div>
              <span class="panel-code">PRINCIPAL</span>
              <h1 id="principal-page-title" tabindex={-1}>
                {principal.displayName}
              </h1>
            </div>
          </div>
        </header>
        <section class="panel domain-panel" aria-labelledby="principal-title">
          <div class="panel-heading">
            <div>
              <span class="panel-code">PERMANENT IDENTITY</span>
              <h2 id="principal-title">Principal details</h2>
            </div>
            <StatusLabel
              state={principal.state === "active" ? "current" : "neutral"}
            >
              {principal.state === "active" ? "Active" : "Disabled"}
            </StatusLabel>
          </div>
          <dl class="fact-grid">
            <div>
              <dt>Principal ID</dt>
              <dd class="technical-value">{principal.id}</dd>
            </div>
            <div>
              <dt>MCP discovery visibility</dt>
              <dd>{visibilityText(principal.visibility)}</dd>
            </div>
            <div>
              <dt>Principal revision</dt>
              <dd>{principal.revision}</dd>
            </div>
            <div>
              <dt>Updated</dt>
              <dd>
                <UserTime value={principal.updatedAt} />
              </dd>
            </div>
          </dl>
        </section>
        <PrincipalCredentialActions
          mutations={mutations}
          sinks={sinks}
          detail={detail}
          onRefresh={onRefresh}
        />
        <PrincipalEditor
          key={principal.id}
          mutations={mutations}
          detail={detail}
          readUnavailable={error !== undefined}
          onRefresh={onRefresh}
          notify={notify}
        />
      </div>
    );
  }
  return (
    <PrincipalCollection session={session} resolved={resolved} view={view} />
  );
}

function PrincipalCollection({
  session,
  resolved,
  view,
}: {
  session: SessionClient;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
}) {
  const navigate = useUnsavedChanges(false);
  const { items, controls } = useCollectionPage<Principal>(
    session,
    resolved,
    view,
    (query, cursor, signal) => {
      const params = new URLSearchParams({
        limit: "50",
        sort: query.sort ?? "name",
        direction: query.direction ?? "ascending",
      });
      for (const key of ["name", "state", "visibility"]) {
        const value = query[`filter_${key}`];
        if (value !== undefined) params.set(key, value);
      }
      if (cursor !== null) params.set("cursor", cursor);
      return readCollectionPage(
        session,
        `/api/v2/principals?${params}`,
        decodePrincipal,
        signal,
      );
    },
    navigate,
    { key: "name", direction: "ascending" },
  );
  return (
    <div class="domain-view" data-testid="principals-view">
      <div class="collection-toolbar">
        <a
          class="button-link create-action"
          href="#/principals/new"
          data-testid="principal-create-link"
        >
          Create principal
        </a>
      </div>
      <section class="panel domain-panel" aria-labelledby="page-title">
        <CollectionTable
          caption="Principal identities"
          rowHeaderKey="name"
          additionalSorts={[
            {
              key: "id",
              label: "Principal ID",
              sortValue: (principal) => principal.id,
            },
          ]}
          remote={controls}
          itemNames={{ singular: "principal", plural: "principals" }}
          emptyTitle="No principals"
          items={items}
          rowKey={(principal) => principal.id}
          initialSort={{ key: "name", direction: "ascending" }}
          rowTestID="principal-row"
          filters={[
            {
              key: "name",
              label: "Name or ID",
              type: "text",
              value: (principal) => principal.displayName,
              literalValues: (principal) => [principal.id],
            },
            {
              key: "state",
              label: "Status",
              type: "select",
              value: (principal) => principal.state,
              options: [
                { value: "active", label: "Active" },
                { value: "disabled", label: "Disabled" },
              ],
            },
            {
              key: "visibility",
              label: "MCP discovery visibility",
              type: "select",
              value: (principal) => principal.visibility,
              options: [
                { value: "requestable", label: "Requestable" },
                { value: "allowed-only", label: "Allowed only" },
                { value: "all", label: "All" },
              ],
            },
          ]}
          columns={[
            {
              key: "name",
              label: "Principal",
              role: "identity",
              sortValue: (principal) => principal.displayName,
              render: (principal) => (
                <TableIdentity
                  primary={
                    <a href={`#/principals/${principal.id}`}>
                      {principal.displayName}
                    </a>
                  }
                  secondary={principal.id}
                />
              ),
            },
            {
              key: "state",
              label: "Status",
              role: "status",
              sortValue: (principal) => principal.state,
              render: (principal) => (
                <StatusLabel
                  state={principal.state === "active" ? "current" : "neutral"}
                >
                  {principal.state === "active" ? "Active" : "Disabled"}
                </StatusLabel>
              ),
            },
            {
              key: "visibility",
              label: "MCP discovery visibility",
              role: "status",
              sortValue: (principal) => principal.visibility,
              render: (principal) => visibilityText(principal.visibility),
            },
          ]}
        />
      </section>
    </div>
  );
}
