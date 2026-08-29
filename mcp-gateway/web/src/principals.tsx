import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import {
  type MutationController,
  type MutationCoordinator,
  type MutationOutcome,
  type MutationSnapshot,
  type MutationSpec,
} from "./mutation";
import {
  ComparisonTable,
  ConfirmationDialog,
  FormField,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { ProtectedContext, SessionClient } from "./session";
import type { ViewSnapshot } from "./view";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const decimal = /^(0|[1-9][0-9]*)$/;
type PrincipalState = "active" | "disabled";
type PrincipalVisibility = "requestable" | "allowed-only" | "all";
type JSONRecord = Record<string, unknown>;

interface Principal {
  id: string;
  displayName: string;
  state: PrincipalState;
  visibility: PrincipalVisibility;
  revision: string;
  credentialRevision: string;
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
    "revision",
    "credential_revision",
    "credential",
    "created_at",
    "updated_at",
  ]);
  if (
    item.credential !== null &&
    (typeof item.credential !== "object" || Array.isArray(item.credential))
  )
    throw new Error("invalid response");
  return {
    id: identifier(item.id),
    displayName: text(item.display_name),
    state: closed(item.state, ["active", "disabled"]),
    visibility: closed(item.visibility, ["requestable", "allowed-only", "all"]),
    revision: revision(item.revision),
    credentialRevision: revision(item.credential_revision),
    hasCredential: item.credential !== null,
    createdAt: text(item.created_at),
    updatedAt: text(item.updated_at),
  };
}
function decodeCreation(value: unknown): PrincipalCreation {
  const creation = record(value, ["principal", "default_grant"]);
  const grant = record(creation.default_grant, [
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
  const principal = decodePrincipal(creation.principal);
  if (
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
async function readPrincipals(session: SessionClient): Promise<Principal[]> {
  const items: Principal[] = [];
  let cursor: string | null = null;
  let restarted = false;
  for (;;) {
    const route = `/api/v1/principals?limit=50${cursor === null ? "" : `&cursor=${encodeURIComponent(cursor)}`}`;
    const result = await readJSON(session, route);
    if (result === undefined) return [];
    if (result.response.status === 409 && cursor !== null && !restarted) {
      items.length = 0;
      cursor = null;
      restarted = true;
      continue;
    }
    if (!result.response.ok) throw new Error("Principal data is unavailable.");
    const page = record(result.value, ["items", "next_cursor"]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    items.push(...page.items.map(decodePrincipal));
    if (page.next_cursor === null) return items;
    cursor = text(page.next_cursor);
    if (cursor.length === 0 || cursor.length > 4096)
      throw new Error("invalid response");
  }
}
async function readPrincipal(
  session: SessionClient,
  id: string,
): Promise<PrincipalDetail | undefined> {
  const result = await readJSON(session, `/api/v1/principals/${id}`);
  if (result === undefined) return undefined;
  if (!result.response.ok) throw new Error("Principal data is unavailable.");
  const etag = result.response.headers.get("ETag");
  if (etag === null || !/^"[\x21\x23-\x7e]{1,255}"$/.test(etag))
    throw new Error("The current principal revision is unavailable.");
  return { principal: decodePrincipal(result.value), etag };
}
async function decodeMutationPrincipal(response: Response): Promise<Principal> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid response");
  return decodePrincipal((await response.json()) as unknown);
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
}: {
  mutations: MutationCoordinator;
  detail?: PrincipalDetail;
  onRefresh: () => void;
}) {
  const create = detail === undefined;
  const principal = detail?.principal;
  const [displayName, setDisplayName] = useState(principal?.displayName ?? "");
  const [state, setState] = useState<PrincipalState>(
    principal?.state ?? "active",
  );
  const [visibility, setVisibility] = useState<PrincipalVisibility>(
    principal?.visibility ?? "requestable",
  );
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [controller] = useState<
    MutationController<Principal | PrincipalCreation>
  >(() => mutations.create<Principal | PrincipalCreation>());
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const submitButton = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (principal === undefined) return;
    setDisplayName(principal.displayName);
    setState(principal.state);
    setVisibility(principal.visibility);
  }, [principal?.id]);

  const settle = async (
    promise: Promise<MutationOutcome<Principal | PrincipalCreation>>,
  ) => {
    const outcome = await promise;
    if (outcome.kind === "acknowledged") {
      controller.abandon();
      const saved =
        "principal" in outcome.value ? outcome.value.principal : outcome.value;
      if (create) {
        window.location.hash = `#/access/principals/${saved.id}`;
      } else {
        setNotice("Principal record saved.");
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
          route: "/api/v1/principals",
          method: "POST",
          body: JSON.stringify({ display_name: displayName, visibility }),
          precondition: null,
          requiresPrecondition: false,
          idempotency: "none",
          successStatuses: [201],
          decode: decodeMutationCreation,
        },
      };
    if (detail === undefined) return undefined;
    const current = detail.principal;
    const patch: Record<string, string> = {};
    if (displayName !== current.displayName) patch.display_name = displayName;
    if (state !== current.state) patch.state = state;
    if (visibility !== current.visibility) patch.visibility = visibility;
    if (Object.keys(patch).length === 0) {
      setError("Change at least one principal field.");
      return undefined;
    }
    return {
      authority: state !== current.state,
      spec: {
        route: `/api/v1/principals/${current.id}`,
        method: "PATCH",
        body: JSON.stringify(patch),
        precondition: detail.etag,
        requiresPrecondition: true,
        idempotency: "none",
        successStatuses: [200],
        decode: decodeMutationPrincipal,
      },
    };
  };
  const start = () => {
    const prepared = prepare();
    if (prepared === undefined) return;
    controller.begin(prepared.spec);
    if (prepared.authority) controller.confirm();
    else void settle(controller.submit());
  };
  const disabled =
    mutation.state === "submitting" ||
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
            {create ? "Create principal" : "Edit principal record"}
          </h2>
        </div>
      </div>
      <p>
        Principal identity is permanent. Discovery visibility is not call
        authorization. Creation atomically adds a permanent synthetic default
        ALLOW grant for the principal namespace.
      </p>
      {!create && (
        <p>
          Re-enabling restores neither a revoked credential nor a deleted
          default grant.
        </p>
      )}
      <form
        data-testid="principal-editor"
        onSubmit={(event) => {
          event.preventDefault();
          start();
        }}
      >
        <FormField id="principal-display-name" label="Display name">
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
          <FormField id="principal-state" label="Principal state">
            {(attributes) => (
              <select
                {...attributes}
                data-testid="principal-state"
                value={state}
                disabled={disabled}
                onChange={(event) =>
                  setState(event.currentTarget.value as PrincipalState)
                }
              >
                <option value="active">Active</option>
                <option value="disabled">Disabled</option>
              </select>
            )}
          </FormField>
        )}
        <FormField
          id="principal-visibility"
          label="Discovery visibility"
          hint="Visibility controls discovery only; grants remain authoritative."
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
        {error !== undefined && (
          <StateNotice state="error" title="Check principal configuration">
            <p>{error}</p>
          </StateNotice>
        )}
        {mutation.problem !== undefined && (
          <StateNotice state="error" title={mutation.problem.title}>
            {mutation.requiresRefresh && (
              <p>
                The current principal was reloaded. Review the preserved safe
                draft before submitting again.
              </p>
            )}
          </StateNotice>
        )}
        {mutation.state === "uncertain" && (
          <StateNotice state="warning" title="Principal outcome is unknown">
            <p>
              Do not replay this non-idempotent change. Refresh the principal
              and authorization state to investigate.
            </p>
          </StateNotice>
        )}
        {notice !== undefined && <StateNotice state="empty" title={notice} />}
        <button
          ref={submitButton}
          data-testid="principal-editor-submit"
          type="submit"
          disabled={disabled}
        >
          {mutation.state === "submitting"
            ? "Submitting…"
            : create
              ? "Create principal"
              : "Save principal"}
        </button>
      </form>
      <ConfirmationDialog
        id="principal-change-confirm"
        open={mutation.state === "confirming"}
        title={
          state === "disabled" ? "Disable principal?" : "Re-enable principal?"
        }
        consequence={
          state === "disabled" ? (
            <p>
              Disabling revokes current agent authority and interrupts
              credential-bound sessions and streams.
            </p>
          ) : (
            <p>
              Re-enabling restores neither a credential nor a deleted default
              grant.
            </p>
          )
        }
        confirmLabel={
          state === "disabled" ? "Disable principal" : "Re-enable principal"
        }
        returnFocus={submitButton}
        onCancel={() => controller.abandon()}
        onConfirm={() => void settle(controller.submit())}
      />
    </section>
  );
}

export function Principals({
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
  const segments = resolved.location.segments;
  const newPrincipal = segments[2] === "new";
  const principalID =
    segments.length === 3 && !newPrincipal ? segments[2] : undefined;
  const [items, setItems] = useState<Principal[]>();
  const [detail, setDetail] = useState<PrincipalDetail>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    let current = true;
    setError(undefined);
    if (newPrincipal)
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
    } else {
      setItems(undefined);
      void readPrincipals(session)
        .then((value) => {
          if (current) setItems(value);
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
        <PrincipalEditor mutations={mutations} onRefresh={onRefresh} />
      </div>
    );
  if (error !== undefined)
    return (
      <StateNotice state="error" title="Principal data unavailable">
        <p>{error}</p>
      </StateNotice>
    );
  if (principalID !== undefined) {
    if (detail === undefined)
      return <StateNotice state="loading" title="Loading principal" />;
    const principal = detail.principal;
    return (
      <div class="domain-view" data-testid="principal-detail">
        <section class="panel domain-panel" aria-labelledby="principal-title">
          <div class="panel-heading">
            <div>
              <span class="panel-code">PERMANENT IDENTITY</span>
              <h2 id="principal-title">{principal.displayName}</h2>
            </div>
            <StatusLabel
              state={principal.state === "active" ? "current" : "warning"}
            >
              {principal.state}
            </StatusLabel>
          </div>
          <dl class="fact-grid">
            <div>
              <dt>Principal ID</dt>
              <dd class="technical-value">{principal.id}</dd>
            </div>
            <div>
              <dt>Discovery visibility</dt>
              <dd>{visibilityText(principal.visibility)}</dd>
            </div>
            <div>
              <dt>Principal revision</dt>
              <dd>{principal.revision}</dd>
            </div>
            <div>
              <dt>Credential revision</dt>
              <dd>{principal.credentialRevision}</dd>
            </div>
            <div>
              <dt>Credential authority</dt>
              <dd>{principal.hasCredential ? "Present" : "Absent"}</dd>
            </div>
            <div>
              <dt>Updated</dt>
              <dd>{principal.updatedAt}</dd>
            </div>
          </dl>
          <p>
            Visibility is not call authorization. Grants and current credential
            authority are evaluated separately.
          </p>
          <div class="inline-actions">
            <a href={`#/access/grants?principal_id=${principal.id}`}>
              View grants
            </a>
            <a href={`#/requests?principal_id=${principal.id}`}>
              View requests
            </a>
          </div>
        </section>
        <PrincipalEditor
          mutations={mutations}
          detail={detail}
          onRefresh={onRefresh}
        />
      </div>
    );
  }
  if (items === undefined)
    return <StateNotice state="loading" title="Loading principals" />;
  return (
    <div class="domain-view" data-testid="principals-view">
      <section class="panel domain-panel" aria-labelledby="principals-title">
        <div class="panel-heading">
          <div>
            <span class="panel-code">ACCESS</span>
            <h2 id="principals-title">Permanent agent principals</h2>
          </div>
          <a class="button-link" href="#/access/principals/new">
            Create principal
          </a>
        </div>
        <p>
          Compare permanent identity, discovery visibility, state, and
          revisions. Visibility does not grant call authority.
        </p>
        {items.length === 0 ? (
          <StateNotice state="empty" title="No principals" />
        ) : (
          <ComparisonTable caption="Principal identities">
            <thead>
              <tr>
                <th scope="col">Principal</th>
                <th scope="col">State</th>
                <th scope="col">Visibility</th>
                <th scope="col">Revisions</th>
                <th scope="col">Action</th>
              </tr>
            </thead>
            <tbody>
              {items.map((principal) => (
                <tr data-testid="principal-row" key={principal.id}>
                  <th scope="row">
                    <strong>{principal.displayName}</strong>
                    <span class="technical-value">{principal.id}</span>
                  </th>
                  <td>
                    <StatusLabel
                      state={
                        principal.state === "active" ? "current" : "warning"
                      }
                    >
                      {principal.state}
                    </StatusLabel>
                  </td>
                  <td>{visibilityText(principal.visibility)}</td>
                  <td>
                    Principal {principal.revision} · credential{" "}
                    {principal.credentialRevision}
                  </td>
                  <td>
                    <a href={`#/access/principals/${principal.id}`}>
                      Open principal
                    </a>
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
