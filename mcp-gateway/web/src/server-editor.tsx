import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type {
  MutationController,
  MutationCoordinator,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import { ConfirmationDialog, FormField, StateNotice } from "./primitives";
import type { ServerView } from "./server-reads";

type JSONRecord = Record<string, unknown>;
type TransportKind = "stdio" | "streamable_http";
type AuthMode = "none" | "bearer" | "oauth";
type RegistrationMode = "static" | "dynamic";

interface Draft {
  namespace: string;
  displayName: string;
  enabled: boolean;
  transportKind: TransportKind;
  executable: string;
  argumentsJSON: string;
  workingDirectory: string;
  environmentJSON: string;
  secretEnvironmentJSON: string;
  url: string;
  protocolMode: "modern" | "legacy" | "auto";
  authMode: AuthMode;
  registrationMode: RegistrationMode;
  issuer: string;
  clientID: string;
  tokenEndpointAuthMethod:
    | "none"
    | "client_secret_basic"
    | "client_secret_post";
  trustedOriginsJSON: string;
  requestOfflineAccess: boolean;
}
interface MutationResult {
  server: ServerView;
  operationID: string | null;
  etag: string;
}

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
const namespacePattern = /^[a-z][a-z0-9_-]{0,31}$/;
const secretSlotPattern = /^[a-z][a-z0-9_]{0,63}$/;
const absolutePathPattern = /^\/(?:[^\0]*)$/;

function blankDraft(): Draft {
  return {
    namespace: "",
    displayName: "",
    enabled: false,
    transportKind: "stdio",
    executable: "",
    argumentsJSON: "[]",
    workingDirectory: "",
    environmentJSON: "{}",
    secretEnvironmentJSON: "{}",
    url: "",
    protocolMode: "modern",
    authMode: "none",
    registrationMode: "dynamic",
    issuer: "",
    clientID: "",
    tokenEndpointAuthMethod: "none",
    trustedOriginsJSON: "[]",
    requestOfflineAccess: false,
  };
}
function jsonRecord(value: unknown): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("Expected a JSON object.");
  return value as JSONRecord;
}
function stringMap(source: string, label: string): Record<string, string> {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source) as unknown;
  } catch {
    throw new Error(`${label} must be one JSON object.`);
  }
  const object = jsonRecord(parsed);
  if (Object.values(object).some((value) => typeof value !== "string"))
    throw new Error(`${label} values must all be strings.`);
  return object as Record<string, string>;
}
function stringArray(source: string, label: string): string[] {
  let parsed: unknown;
  try {
    parsed = JSON.parse(source) as unknown;
  } catch {
    throw new Error(`${label} must be one JSON array.`);
  }
  if (
    !Array.isArray(parsed) ||
    parsed.some((value) => typeof value !== "string")
  )
    throw new Error(`${label} must contain only strings.`);
  return parsed;
}
function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (typeof value === "object" && value !== null) {
    const item = value as JSONRecord;
    return `{${Object.keys(item)
      .sort()
      .map((key) => `${JSON.stringify(key)}:${canonical(item[key])}`)
      .join(",")}}`;
  }
  return JSON.stringify(value);
}
function transportFromDraft(draft: Draft): unknown {
  if (draft.transportKind === "stdio") {
    const args = stringArray(draft.argumentsJSON, "Arguments");
    const environment = stringMap(draft.environmentJSON, "Environment");
    const secretEnvironment = stringMap(
      draft.secretEnvironmentJSON,
      "Secret environment",
    );
    if (
      draft.executable.length === 0 ||
      draft.workingDirectory.length === 0 ||
      !absolutePathPattern.test(draft.executable) ||
      !absolutePathPattern.test(draft.workingDirectory)
    )
      throw new Error(
        "Executable and working directory must be absolute paths.",
      );
    if (args.length > 64) throw new Error("At most 64 arguments are allowed.");
    if (Object.keys(environment).length > 32)
      throw new Error("At most 32 ordinary environment entries are allowed.");
    if (Object.keys(secretEnvironment).length > 16)
      throw new Error("At most 16 secret environment slots are allowed.");
    for (const [name, slot] of Object.entries(secretEnvironment)) {
      if (!secretSlotPattern.test(slot))
        throw new Error(`Secret environment ${name} must name a keyring slot.`);
      if (Object.hasOwn(environment, name))
        throw new Error(
          `${name} cannot be both ordinary and secret environment.`,
        );
    }
    return {
      kind: "stdio",
      executable: draft.executable,
      arguments: args,
      working_directory: draft.workingDirectory,
      environment,
      secret_environment: secretEnvironment,
    };
  }
  let parsedURL: URL;
  try {
    parsedURL = new URL(draft.url);
  } catch {
    throw new Error("HTTP URL must be absolute.");
  }
  if (
    parsedURL.username !== "" ||
    parsedURL.password !== "" ||
    parsedURL.search !== "" ||
    parsedURL.hash !== ""
  )
    throw new Error("HTTP URL cannot contain credentials, query, or fragment.");
  let authentication: unknown;
  if (draft.authMode === "none" || draft.authMode === "bearer") {
    authentication = { mode: draft.authMode };
  } else {
    const trustedOrigins = stringArray(
      draft.trustedOriginsJSON,
      "Trusted origins",
    );
    if (draft.registrationMode === "static" && draft.clientID.length === 0)
      throw new Error("Static OAuth registration requires a client ID.");
    authentication = {
      mode: "oauth",
      registration:
        draft.registrationMode === "static"
          ? {
              mode: "static",
              issuer: draft.issuer === "" ? null : draft.issuer,
              client_id: draft.clientID,
              token_endpoint_auth_method: draft.tokenEndpointAuthMethod,
            }
          : {
              mode: "dynamic",
              issuer: draft.issuer === "" ? null : draft.issuer,
            },
      trusted_origins: trustedOrigins,
      request_offline_access: draft.requestOfflineAccess,
    };
  }
  return {
    kind: "streamable_http",
    url: draft.url,
    protocol_mode: draft.protocolMode,
    authentication,
  };
}
function draftFromServer(server: ServerView): Draft {
  const draft = blankDraft();
  draft.namespace = server.namespace;
  draft.displayName = server.displayName;
  draft.enabled = server.desiredState === "enabled";
  const transport = server.transport as JSONRecord;
  if (transport.kind === "stdio") {
    draft.transportKind = "stdio";
    draft.executable = transport.executable as string;
    draft.argumentsJSON = JSON.stringify(transport.arguments);
    draft.workingDirectory = transport.working_directory as string;
    draft.environmentJSON = JSON.stringify(transport.environment);
    draft.secretEnvironmentJSON = JSON.stringify(transport.secret_environment);
    return draft;
  }
  draft.transportKind = "streamable_http";
  draft.url = transport.url as string;
  draft.protocolMode = transport.protocol_mode as Draft["protocolMode"];
  const authentication = transport.authentication as JSONRecord;
  draft.authMode = authentication.mode as AuthMode;
  if (draft.authMode !== "oauth") return draft;
  const registration = authentication.registration as JSONRecord;
  draft.registrationMode = registration.mode as RegistrationMode;
  draft.issuer = (registration.issuer as string | null) ?? "";
  draft.clientID = (registration.client_id as string | undefined) ?? "";
  draft.tokenEndpointAuthMethod =
    (registration.token_endpoint_auth_method as
      | Draft["tokenEndpointAuthMethod"]
      | undefined) ?? "none";
  draft.trustedOriginsJSON = JSON.stringify(authentication.trusted_origins);
  draft.requestOfflineAccess = authentication.request_offline_access as boolean;
  return draft;
}
function operationID(value: unknown): string | null {
  if (value === null) return null;
  const operation = jsonRecord(value);
  const expected = [
    "id",
    "server_id",
    "kind",
    "target_desired_revision",
    "target_credential_revisions",
    "state",
    "reason",
    "created_at",
    "started_at",
    "finished_at",
  ];
  if (Object.keys(operation).sort().join(",") !== expected.sort().join(","))
    throw new Error("invalid mutation response");
  const revisions = jsonRecord(operation.target_credential_revisions);
  if (
    Object.keys(revisions).sort().join(",") !==
      "oauth_client,oauth_tokens,static_credential" ||
    !gatewayID.test(String(operation.id)) ||
    !gatewayID.test(String(operation.server_id)) ||
    ![
      "activate",
      "reload",
      "retry",
      "refresh_catalog",
      "credential_replace",
      "disable",
      "delete",
      "disconnect_credentials",
    ].includes(String(operation.kind)) ||
    ![
      "scheduled",
      "running",
      "succeeded",
      "failed",
      "cancelled",
      "superseded",
      "interrupted",
    ].includes(String(operation.state)) ||
    typeof operation.target_desired_revision !== "string" ||
    Object.values(revisions).some((revision) => typeof revision !== "string") ||
    typeof operation.created_at !== "string" ||
    (operation.started_at !== null &&
      typeof operation.started_at !== "string") ||
    (operation.finished_at !== null &&
      typeof operation.finished_at !== "string") ||
    (operation.reason !== null && typeof operation.reason !== "string")
  )
    throw new Error("invalid mutation response");
  return operation.id as string;
}
async function decodeMutation(
  response: Response,
  decodeServer: (value: unknown) => ServerView,
): Promise<MutationResult> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid mutation response");
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 1024 * 1024)
    throw new Error("invalid mutation response");
  const root = jsonRecord(JSON.parse(body) as unknown);
  if (Object.keys(root).sort().join(",") !== "operation,server")
    throw new Error("invalid mutation response");
  const server = decodeServer(root.server);
  const etag = response.headers.get("ETag");
  if (etag !== `"server-${server.id}-${server.desiredRevision}"`)
    throw new Error("invalid mutation response");
  return { server, operationID: operationID(root.operation), etag };
}

function EditorForm({
  draft,
  setDraft,
  disabled,
  namespaceLocked,
}: {
  draft: Draft;
  setDraft: (draft: Draft) => void;
  disabled: boolean;
  namespaceLocked: boolean;
}) {
  const update = <K extends keyof Draft>(key: K, value: Draft[K]) =>
    setDraft({ ...draft, [key]: value });
  return (
    <>
      <FormField
        id="server-namespace"
        label="Namespace"
        hint="Immutable lowercase routing identity."
      >
        {(attributes) => (
          <input
            {...attributes}
            value={draft.namespace}
            disabled={disabled || namespaceLocked}
            onInput={(event) => update("namespace", event.currentTarget.value)}
          />
        )}
      </FormField>
      <FormField id="server-display-name" label="Display name">
        {(attributes) => (
          <input
            {...attributes}
            value={draft.displayName}
            disabled={disabled}
            onInput={(event) =>
              update("displayName", event.currentTarget.value)
            }
          />
        )}
      </FormField>
      <FormField
        id="server-enabled"
        label="Desired state"
        hint="Changing this state schedules server work."
      >
        {(attributes) => (
          <select
            {...attributes}
            value={draft.enabled ? "enabled" : "disabled"}
            disabled={disabled}
            onChange={(event) =>
              update("enabled", event.currentTarget.value === "enabled")
            }
          >
            <option value="disabled">Disabled</option>
            <option value="enabled">Enabled</option>
          </select>
        )}
      </FormField>
      <FormField id="server-transport-kind" label="Transport">
        {(attributes) => (
          <select
            {...attributes}
            value={draft.transportKind}
            disabled={disabled}
            onChange={(event) =>
              update(
                "transportKind",
                event.currentTarget.value as TransportKind,
              )
            }
          >
            <option value="stdio">Local stdio</option>
            <option value="streamable_http">Streamable HTTP</option>
          </select>
        )}
      </FormField>
      {draft.transportKind === "stdio" ? (
        <>
          <p class="bounded-note">
            The Gateway launches this process as its own operating-system user.
            This is process execution, not an OS sandbox or containment
            boundary.
          </p>
          <FormField
            id="server-executable"
            label="Executable"
            hint="Absolute path; no shell interpolation."
          >
            {(attributes) => (
              <input
                {...attributes}
                value={draft.executable}
                disabled={disabled}
                onInput={(event) =>
                  update("executable", event.currentTarget.value)
                }
              />
            )}
          </FormField>
          <FormField
            id="server-arguments"
            label="Arguments"
            hint='JSON string array, for example ["--stdio"].'
          >
            {(attributes) => (
              <textarea
                {...attributes}
                value={draft.argumentsJSON}
                disabled={disabled}
                onInput={(event) =>
                  update("argumentsJSON", event.currentTarget.value)
                }
              />
            )}
          </FormField>
          <FormField
            id="server-working-directory"
            label="Working directory"
            hint="Absolute path."
          >
            {(attributes) => (
              <input
                {...attributes}
                value={draft.workingDirectory}
                disabled={disabled}
                onInput={(event) =>
                  update("workingDirectory", event.currentTarget.value)
                }
              />
            )}
          </FormField>
          <FormField
            id="server-environment"
            label="Ordinary environment"
            hint="JSON string map. Do not enter credentials or secret values here."
          >
            {(attributes) => (
              <textarea
                {...attributes}
                value={draft.environmentJSON}
                disabled={disabled}
                onInput={(event) =>
                  update("environmentJSON", event.currentTarget.value)
                }
              />
            )}
          </FormField>
          <FormField
            id="server-secret-environment"
            label="Secret environment slots"
            hint="JSON map from environment names to keyring slot names only. Secret values are never accepted by this form."
          >
            {(attributes) => (
              <textarea
                {...attributes}
                value={draft.secretEnvironmentJSON}
                disabled={disabled}
                onInput={(event) =>
                  update("secretEnvironmentJSON", event.currentTarget.value)
                }
              />
            )}
          </FormField>
        </>
      ) : (
        <>
          <FormField
            id="server-url"
            label="HTTP endpoint"
            hint="Credentials, query strings, and fragments are rejected."
          >
            {(attributes) => (
              <input
                {...attributes}
                value={draft.url}
                disabled={disabled}
                onInput={(event) => update("url", event.currentTarget.value)}
              />
            )}
          </FormField>
          <FormField id="server-protocol-mode" label="Protocol mode">
            {(attributes) => (
              <select
                {...attributes}
                value={draft.protocolMode}
                disabled={disabled}
                onChange={(event) =>
                  update(
                    "protocolMode",
                    event.currentTarget.value as Draft["protocolMode"],
                  )
                }
              >
                <option value="modern">Modern</option>
                <option value="legacy">Legacy</option>
                <option value="auto">Automatic negotiation</option>
              </select>
            )}
          </FormField>
          <FormField
            id="server-auth-mode"
            label="Authentication"
            hint="Bearer and OAuth secret material is installed only through separate write-only workflows."
          >
            {(attributes) => (
              <select
                {...attributes}
                value={draft.authMode}
                disabled={disabled}
                onChange={(event) =>
                  update("authMode", event.currentTarget.value as AuthMode)
                }
              >
                <option value="none">None</option>
                <option value="bearer">Managed bearer slot</option>
                <option value="oauth">OAuth</option>
              </select>
            )}
          </FormField>
          {draft.authMode === "oauth" && (
            <>
              <FormField
                id="server-registration-mode"
                label="OAuth registration"
              >
                {(attributes) => (
                  <select
                    {...attributes}
                    value={draft.registrationMode}
                    disabled={disabled}
                    onChange={(event) =>
                      update(
                        "registrationMode",
                        event.currentTarget.value as RegistrationMode,
                      )
                    }
                  >
                    <option value="dynamic">Dynamic</option>
                    <option value="static">Static</option>
                  </select>
                )}
              </FormField>
              <FormField
                id="server-issuer"
                label="Issuer"
                hint="Optional canonical HTTPS issuer."
              >
                {(attributes) => (
                  <input
                    {...attributes}
                    value={draft.issuer}
                    disabled={disabled}
                    onInput={(event) =>
                      update("issuer", event.currentTarget.value)
                    }
                  />
                )}
              </FormField>
              {draft.registrationMode === "static" && (
                <>
                  <FormField id="server-client-id" label="Client ID">
                    {(attributes) => (
                      <input
                        {...attributes}
                        value={draft.clientID}
                        disabled={disabled}
                        onInput={(event) =>
                          update("clientID", event.currentTarget.value)
                        }
                      />
                    )}
                  </FormField>
                  <FormField
                    id="server-token-auth"
                    label="Token endpoint authentication"
                  >
                    {(attributes) => (
                      <select
                        {...attributes}
                        value={draft.tokenEndpointAuthMethod}
                        disabled={disabled}
                        onChange={(event) =>
                          update(
                            "tokenEndpointAuthMethod",
                            event.currentTarget
                              .value as Draft["tokenEndpointAuthMethod"],
                          )
                        }
                      >
                        <option value="none">None</option>
                        <option value="client_secret_basic">
                          Client secret basic
                        </option>
                        <option value="client_secret_post">
                          Client secret post
                        </option>
                      </select>
                    )}
                  </FormField>
                </>
              )}
              <FormField
                id="server-trusted-origins"
                label="Trusted origins"
                hint="JSON array of canonical HTTPS origins."
              >
                {(attributes) => (
                  <textarea
                    {...attributes}
                    value={draft.trustedOriginsJSON}
                    disabled={disabled}
                    onInput={(event) =>
                      update("trustedOriginsJSON", event.currentTarget.value)
                    }
                  />
                )}
              </FormField>
              <FormField id="server-offline-access" label="Offline access">
                {(attributes) => (
                  <select
                    {...attributes}
                    value={draft.requestOfflineAccess ? "yes" : "no"}
                    disabled={disabled}
                    onChange={(event) =>
                      update(
                        "requestOfflineAccess",
                        event.currentTarget.value === "yes",
                      )
                    }
                  >
                    <option value="no">Do not request</option>
                    <option value="yes">Request offline access</option>
                  </select>
                )}
              </FormField>
            </>
          )}
        </>
      )}
    </>
  );
}

export function ServerEditor({
  mutations,
  server,
  etag,
  onRefresh,
  decodeServerValue,
}: {
  mutations: MutationCoordinator;
  server?: ServerView;
  etag?: string;
  onRefresh: () => void;
  decodeServerValue: (value: unknown) => ServerView;
}) {
  const create = server === undefined;
  const [draft, setDraft] = useState<Draft>(() =>
    create ? blankDraft() : draftFromServer(server),
  );
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [blockedETag, setBlockedETag] = useState<string>();
  const [controller] = useState<MutationController<MutationResult>>(() =>
    mutations.create<MutationResult>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const submitButton = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (server !== undefined) setDraft(draftFromServer(server));
  }, [server?.id]);

  const settle = async (
    submission: Promise<import("./mutation").MutationOutcome<MutationResult>>,
  ) => {
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      setBlockedETag(undefined);
      setNotice(
        outcome.value.operationID === null
          ? "Desired server record saved."
          : `Desired state saved; operation ${outcome.value.operationID} was scheduled.`,
      );
      controller.abandon();
      if (create) window.location.hash = `#/servers/${outcome.value.server.id}`;
      else onRefresh();
    } else if (
      outcome.kind === "rejected" &&
      outcome.requiresRefresh &&
      !create
    ) {
      setBlockedETag(etag);
    }
  };
  const prepare = ():
    | { spec: MutationSpec<MutationResult>; behavioral: boolean }
    | undefined => {
    setError(undefined);
    setNotice(undefined);
    try {
      if (
        !namespacePattern.test(draft.namespace) ||
        draft.namespace === "mcp_gateway"
      )
        throw new Error(
          "Namespace must be a permitted lowercase routing identity.",
        );
      if (
        draft.displayName.length === 0 ||
        new TextEncoder().encode(draft.displayName).byteLength > 256
      )
        throw new Error("Display name must contain 1–256 UTF-8 bytes.");
      const transport = transportFromDraft(draft);
      if (create) {
        return {
          behavioral: true,
          spec: {
            route: "/api/v1/servers",
            method: "POST",
            body: JSON.stringify({
              namespace: draft.namespace,
              display_name: draft.displayName,
              enabled: draft.enabled,
              transport,
            }),
            precondition: null,
            requiresPrecondition: false,
            idempotency: "server_create",
            successStatuses: [200, 201],
            decode: (response) => decodeMutation(response, decodeServerValue),
          },
        };
      }
      if (etag === undefined)
        throw new Error("Refresh the current server revision before editing.");
      const behavioral =
        draft.enabled !== (server.desiredState === "enabled") ||
        canonical(transport) !== canonical(server.transport);
      const body = behavioral
        ? { display_name: draft.displayName, enabled: draft.enabled, transport }
        : { display_name: draft.displayName };
      return {
        behavioral,
        spec: {
          route: `/api/v1/servers/${server.id}`,
          method: "PATCH",
          body: JSON.stringify(body),
          precondition: etag,
          requiresPrecondition: true,
          idempotency: "none",
          successStatuses: [200],
          decode: (response) => decodeMutation(response, decodeServerValue),
        },
      };
    } catch (caught) {
      setError(
        caught instanceof Error
          ? caught.message
          : "Invalid server configuration.",
      );
      return undefined;
    }
  };
  const start = () => {
    const prepared = prepare();
    if (prepared === undefined) return;
    controller.begin(prepared.spec);
    if (prepared.behavioral) controller.confirm();
    else void settle(controller.submit());
  };
  const waitingForFreshETag =
    !create && blockedETag !== undefined && blockedETag === etag;
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched" ||
    waitingForFreshETag;
  const form = (
    <>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          start();
        }}
        data-testid="server-editor"
      >
        <EditorForm
          draft={draft}
          setDraft={setDraft}
          disabled={disabled}
          namespaceLocked={!create}
        />
        {error !== undefined && (
          <StateNotice state="error" title="Check server configuration">
            <p>{error}</p>
          </StateNotice>
        )}
        {mutation.problem !== undefined && (
          <StateNotice state="error" title={mutation.problem.title}>
            {mutation.requiresRefresh && (
              <p>
                A current server reload was requested. Your safe nonsecret draft
                is preserved; review it after the refreshed ETag arrives.
              </p>
            )}
          </StateNotice>
        )}
        {waitingForFreshETag && (
          <p class="session-message" role="status">
            Waiting for a fresh server ETag before another submission.
          </p>
        )}
        {mutation.state === "uncertain" && (
          <StateNotice state="warning" title="Mutation outcome unknown">
            <p>
              {mutation.canReplay
                ? "The same create may be replayed explicitly with its retained in-memory idempotency key."
                : "Inspect the authoritative server record before making another change."}
            </p>
          </StateNotice>
        )}
        {notice !== undefined && (
          <p class="session-message" role="status">
            {notice}
          </p>
        )}
        {mutation.availability === "storage_latched" && (
          <StateNotice
            state="unavailable"
            title="Mutations closed by storage latch"
          />
        )}
        <button
          ref={submitButton}
          class="primary-action"
          type="submit"
          disabled={disabled}
          data-testid="server-editor-submit"
        >
          {create ? "Review server creation" : "Save desired state"}
        </button>
        {mutation.canReplay && (
          <button
            type="button"
            data-testid="server-create-replay"
            onClick={() => void settle(controller.replay())}
          >
            Replay this same create
          </button>
        )}
      </form>
      <ConfirmationDialog
        id="server-change-confirm"
        open={mutation.state === "confirming"}
        title={
          create ? "Create server identity?" : "Apply behavioral server change?"
        }
        consequence={
          create
            ? "This creates a durable routing identity and may schedule process work when enabled."
            : "Changing desired state or transport interrupts current server routing and schedules reconciliation."
        }
        confirmLabel={create ? "Create server" : "Apply behavioral change"}
        returnFocus={submitButton as unknown as RefObject<HTMLElement>}
        onCancel={() => controller.abandon()}
        onConfirm={() => void settle(controller.submit())}
      />
    </>
  );
  if (create)
    return (
      <section class="panel domain-panel" aria-labelledby="server-editor-title">
        <div class="panel-heading">
          <div>
            <span class="panel-code">NEW SERVER</span>
            <h2 id="server-editor-title">
              Create sanitized server configuration
            </h2>
          </div>
        </div>
        {form}
      </section>
    );
  return (
    <details class="panel domain-panel">
      <summary>Edit desired server state</summary>
      <p>
        Display-only changes save directly. State or transport changes require
        consequence confirmation and a fresh server ETag.
      </p>
      {form}
    </details>
  );
}
