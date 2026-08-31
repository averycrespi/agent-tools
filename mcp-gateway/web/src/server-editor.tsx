import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { useUnsavedChanges } from "./navigation";
import type {
  MutationController,
  MutationCoordinator,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import { ConfirmationDialog, FormField, StateNotice } from "./primitives";
import { decodeOperation } from "./server-operation-model";
import type { ServerView } from "./server-reads";

type JSONRecord = Record<string, unknown>;
type TransportKind = "" | "stdio" | "streamable_http";
type AuthMode = "" | "none" | "bearer" | "oauth";
type RegistrationMode = "static" | "dynamic";

interface StringItem {
  id: string;
  value: string;
}
interface PairItem {
  id: string;
  name: string;
  value: string;
}
interface Draft {
  namespace: string;
  displayName: string;
  enabled: boolean;
  transportKind: TransportKind;
  executable: string;
  arguments: StringItem[];
  workingDirectory: string;
  environment: PairItem[];
  secretEnvironment: PairItem[];
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
  trustedOrigins: StringItem[];
  requestOfflineAccess: boolean;
}
interface MutationResult {
  server: ServerView;
  operationID: string | null;
  etag: string;
}

const namespacePattern = /^[a-z][a-z0-9_-]{0,31}$/;
const secretSlotPattern = /^[a-z][a-z0-9_]{0,63}$/;
const absolutePathPattern = /^\/(?:[^\0]*)$/;
let nextItemID = 0;

function itemID(prefix: string): string {
  nextItemID += 1;
  return `${prefix}-${nextItemID}`;
}
function stringItems(prefix: string, values: readonly string[]): StringItem[] {
  return values.map((value) => ({ id: itemID(prefix), value }));
}
function pairItems(
  prefix: string,
  values: Readonly<Record<string, string>>,
): PairItem[] {
  return Object.entries(values).map(([name, value]) => ({
    id: itemID(prefix),
    name,
    value,
  }));
}
function pairRecord(items: readonly PairItem[], label: string) {
  const result = Object.create(null) as Record<string, string>;
  for (const item of items) {
    if (item.name === "") throw new Error(`${label} names cannot be empty.`);
    if (Object.hasOwn(result, item.name))
      throw new Error(`${label} names must be unique.`);
    result[item.name] = item.value;
  }
  return result;
}
function blankDraft(): Draft {
  return {
    namespace: "",
    displayName: "",
    enabled: false,
    transportKind: "",
    executable: "",
    arguments: [],
    workingDirectory: "",
    environment: [],
    secretEnvironment: [],
    url: "",
    protocolMode: "auto",
    authMode: "",
    registrationMode: "dynamic",
    issuer: "",
    clientID: "",
    tokenEndpointAuthMethod: "none",
    trustedOrigins: [],
    requestOfflineAccess: false,
  };
}
function jsonRecord(value: unknown): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("Expected a JSON object.");
  return value as JSONRecord;
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
  if (draft.transportKind === "")
    throw new Error("Choose how Gateway connects to this server.");
  if (draft.transportKind === "stdio") {
    const args = draft.arguments.map((item) => item.value);
    const environment = pairRecord(draft.environment, "Environment variable");
    const secretEnvironment = pairRecord(
      draft.secretEnvironment,
      "Secret environment binding",
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
  if (draft.authMode === "")
    throw new Error("Choose how Gateway authenticates to this server.");
  const normalizedURL = draft.url.trim();
  let parsedURL: URL;
  try {
    parsedURL = new URL(normalizedURL);
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
    const trustedOrigins = draft.trustedOrigins.map((item) =>
      item.value.trim(),
    );
    if (trustedOrigins.some((origin) => origin === ""))
      throw new Error("OAuth network origins cannot be empty.");
    if (new Set(trustedOrigins).size !== trustedOrigins.length)
      throw new Error("OAuth network origins must be unique.");
    const issuer = draft.issuer.trim();
    const clientID = draft.clientID.trim();
    if (draft.registrationMode === "static" && clientID.length === 0)
      throw new Error("Static OAuth registration requires a client ID.");
    authentication = {
      mode: "oauth",
      registration:
        draft.registrationMode === "static"
          ? {
              mode: "static",
              issuer: issuer === "" ? null : issuer,
              client_id: clientID,
              token_endpoint_auth_method: draft.tokenEndpointAuthMethod,
            }
          : {
              mode: "dynamic",
              issuer: issuer === "" ? null : issuer,
            },
      trusted_origins: trustedOrigins,
      request_offline_access: draft.requestOfflineAccess,
    };
  }
  return {
    kind: "streamable_http",
    url: normalizedURL,
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
    draft.arguments = stringItems(
      "argument",
      transport.arguments as readonly string[],
    );
    draft.workingDirectory = transport.working_directory as string;
    draft.environment = pairItems(
      "environment",
      transport.environment as Record<string, string>,
    );
    draft.secretEnvironment = pairItems(
      "secret-environment",
      transport.secret_environment as Record<string, string>,
    );
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
  draft.trustedOrigins = stringItems(
    "oauth-origin",
    authentication.trusted_origins as readonly string[],
  );
  draft.requestOfflineAccess = authentication.request_offline_access as boolean;
  return draft;
}
function operationID(value: unknown): string | null {
  return value === null ? null : decodeOperation(value).id;
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

function StringListEditor({
  id,
  label,
  hint,
  itemLabel,
  addLabel,
  items,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  itemLabel: string;
  addLabel: string;
  items: StringItem[];
  disabled: boolean;
  onChange: (items: StringItem[]) => void;
}) {
  return (
    <fieldset class="collection-field" aria-describedby={`${id}-hint`}>
      <legend>
        {label}
        <span class="optional-label"> (optional)</span>
      </legend>
      <p class="field-hint" id={`${id}-hint`}>
        {hint}
      </p>
      {items.map((item, index) => (
        <div class="collection-row" key={item.id}>
          <label class="visually-hidden" for={`${id}-${item.id}`}>
            {itemLabel} {index + 1}
          </label>
          <input
            id={`${id}-${item.id}`}
            data-testid={id}
            value={item.value}
            disabled={disabled}
            onInput={(event) =>
              onChange(
                items.map((current) =>
                  current.id === item.id
                    ? { ...current, value: event.currentTarget.value }
                    : current,
                ),
              )
            }
          />
          <button
            type="button"
            disabled={disabled}
            aria-label={`Remove ${itemLabel.toLowerCase()} ${index + 1}`}
            onClick={() =>
              onChange(items.filter((current) => current.id !== item.id))
            }
          >
            Remove
          </button>
        </div>
      ))}
      <button
        type="button"
        class="quiet-action"
        data-testid={`${id}-add`}
        disabled={disabled}
        onClick={() => onChange([...items, { id: itemID(id), value: "" }])}
      >
        {addLabel}
      </button>
    </fieldset>
  );
}

function PairListEditor({
  id,
  label,
  hint,
  nameLabel,
  valueLabel,
  valueRequired = true,
  items,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  nameLabel: string;
  valueLabel: string;
  valueRequired?: boolean;
  items: PairItem[];
  disabled: boolean;
  onChange: (items: PairItem[]) => void;
}) {
  return (
    <fieldset class="collection-field" aria-describedby={`${id}-hint`}>
      <legend>
        {label}
        <span class="optional-label"> (optional)</span>
      </legend>
      <p class="field-hint" id={`${id}-hint`}>
        {hint}
      </p>
      {items.map((item, index) => (
        <div class="collection-row collection-pair" key={item.id}>
          <label class="visually-hidden" for={`${id}-${item.id}-name`}>
            {nameLabel} {index + 1}
          </label>
          <input
            id={`${id}-${item.id}-name`}
            data-testid={`${id}-name`}
            value={item.name}
            required
            placeholder={nameLabel}
            disabled={disabled}
            onInput={(event) =>
              onChange(
                items.map((current) =>
                  current.id === item.id
                    ? { ...current, name: event.currentTarget.value }
                    : current,
                ),
              )
            }
          />
          <label class="visually-hidden" for={`${id}-${item.id}-value`}>
            {valueLabel} {index + 1}
          </label>
          <input
            id={`${id}-${item.id}-value`}
            data-testid={`${id}-value`}
            value={item.value}
            required={valueRequired}
            placeholder={valueLabel}
            disabled={disabled}
            onInput={(event) =>
              onChange(
                items.map((current) =>
                  current.id === item.id
                    ? { ...current, value: event.currentTarget.value }
                    : current,
                ),
              )
            }
          />
          <button
            type="button"
            disabled={disabled}
            aria-label={`Remove ${label.toLowerCase()} row ${index + 1}`}
            onClick={() =>
              onChange(items.filter((current) => current.id !== item.id))
            }
          >
            Remove
          </button>
        </div>
      ))}
      <button
        type="button"
        class="quiet-action"
        data-testid={`${id}-add`}
        disabled={disabled}
        onClick={() =>
          onChange([...items, { id: itemID(id), name: "", value: "" }])
        }
      >
        Add row
      </button>
    </fieldset>
  );
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
      <p class="form-convention">
        All fields are required unless marked optional.
      </p>
      <FormField
        id="server-namespace"
        label="Namespace"
        hint="Permanent lowercase routing identity."
        required
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
      <FormField id="server-display-name" label="Display name" required>
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
        label="Initial state"
        hint="Enabled servers schedule connection work after creation."
        required
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
      <fieldset class="choice-field">
        <legend>Connection method</legend>
        <label>
          <input
            id="server-transport-stdio"
            name="server-transport-kind"
            type="radio"
            value="stdio"
            checked={draft.transportKind === "stdio"}
            disabled={disabled}
            required
            onChange={() => update("transportKind", "stdio")}
          />
          <span>
            <strong>Local process (stdio)</strong>
            <small>Gateway launches a local executable.</small>
          </span>
        </label>
        <label>
          <input
            id="server-transport-http"
            name="server-transport-kind"
            type="radio"
            value="streamable_http"
            checked={draft.transportKind === "streamable_http"}
            disabled={disabled}
            required
            onChange={() => update("transportKind", "streamable_http")}
          />
          <span>
            <strong>HTTP endpoint</strong>
            <small>Gateway connects to a Streamable HTTP server.</small>
          </span>
        </label>
      </fieldset>
      {draft.transportKind === "stdio" && (
        <>
          <p class="bounded-note">
            Gateway runs this executable directly as its operating-system user.
            It is not sandboxed.
          </p>
          <FormField
            id="server-executable"
            label="Executable"
            hint="Absolute path; no shell interpolation."
            required
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
            id="server-working-directory"
            label="Working directory"
            hint="Absolute path."
            required
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
          <details class="form-disclosure">
            <summary>Optional process settings</summary>
            <StringListEditor
              id="server-argument"
              label="Arguments"
              hint="Passed literally and in this order; no shell interpolation."
              itemLabel="Argument"
              addLabel="Add argument"
              items={draft.arguments}
              disabled={disabled}
              onChange={(items) => update("arguments", items)}
            />
            <PairListEditor
              id="server-environment"
              label="Environment variables"
              hint="Ordinary nonsecret values only."
              nameLabel="Variable name"
              valueLabel="Value"
              valueRequired={false}
              items={draft.environment}
              disabled={disabled}
              onChange={(items) => update("environment", items)}
            />
            <PairListEditor
              id="server-secret-environment"
              label="Secret environment bindings"
              hint="Map environment variables to keyring slot names. Secret values are added later under Credentials."
              nameLabel="Environment variable"
              valueLabel="Credential slot"
              items={draft.secretEnvironment}
              disabled={disabled}
              onChange={(items) => update("secretEnvironment", items)}
            />
          </details>
        </>
      )}
      {draft.transportKind === "streamable_http" && (
        <>
          <FormField
            id="server-url"
            label="HTTP endpoint"
            hint="Credentials, query strings, and fragments are rejected."
            required
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
          <details class="form-disclosure">
            <summary>Compatibility settings</summary>
            <FormField
              id="server-protocol-mode"
              label="Protocol preference"
              required
            >
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
                  <option value="auto">Automatic (recommended)</option>
                  <option value="modern">Current only — 2026-07-28</option>
                  <option value="legacy">Legacy only — 2025-11-25</option>
                </select>
              )}
            </FormField>
            <p class="field-hint">
              Automatic uses legacy only when the server explicitly reports that
              the current protocol is unsupported.
            </p>
          </details>
          <fieldset class="choice-field">
            <legend>Authentication</legend>
            <p class="field-hint">
              Credentials are added separately and stored in the
              operating-system keyring; this configuration contains no secret
              values.
            </p>
            {(
              [
                ["none", "No authentication"],
                ["bearer", "Bearer token"],
                ["oauth", "OAuth"],
              ] as const
            ).map(([mode, label]) => (
              <label key={mode}>
                <input
                  id={`server-auth-${mode}`}
                  name="server-auth-mode"
                  type="radio"
                  value={mode}
                  checked={draft.authMode === mode}
                  disabled={disabled}
                  required
                  onChange={() => update("authMode", mode)}
                />
                <span>{label}</span>
              </label>
            ))}
          </fieldset>
          {draft.authMode === "bearer" && (
            <p class="bounded-note">
              After creating the server, add its bearer token under Credentials.
              Gateway stores it in the keyring and sends it in the Authorization
              header.
            </p>
          )}
          {draft.authMode === "oauth" && (
            <>
              <p class="bounded-note">
                After creating the server, complete an OAuth authorization flow.
                Gateway stores resulting token authority in the keyring.
              </p>
              <FormField
                id="server-registration-mode"
                label="OAuth client registration"
                required
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
                    <option value="dynamic">
                      Register Gateway automatically
                    </option>
                    <option value="static">Use an existing OAuth client</option>
                  </select>
                )}
              </FormField>
              <p class="field-hint">
                Automatic registration uses the provider's advertised dynamic
                registration endpoint when authorization begins.
              </p>
              <FormField
                id="server-issuer"
                label="Authorization server issuer"
                hint="Leave blank only when metadata identifies one issuer on the same origin as the HTTP endpoint; otherwise enter the exact HTTPS issuer."
                optional
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
                  <FormField id="server-client-id" label="Client ID" required>
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
                    hint="Basic and request-body methods require a separately installed client secret."
                    required
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
                        <option value="none">
                          Public client — no client secret
                        </option>
                        <option value="client_secret_basic">
                          Client secret in HTTP Basic
                        </option>
                        <option value="client_secret_post">
                          Client secret in request body
                        </option>
                      </select>
                    )}
                  </FormField>
                </>
              )}
              <details class="form-disclosure">
                <summary>Advanced OAuth settings</summary>
                <StringListEditor
                  id="server-oauth-origin"
                  label="Additional OAuth origins allowed on restricted networks"
                  hint="HTTPS network exceptions for private or loopback OAuth endpoints. The MCP server origin is already included; TLS and browser-origin policy are unchanged."
                  itemLabel="OAuth origin"
                  addLabel="Add OAuth origin"
                  items={draft.trustedOrigins}
                  disabled={disabled}
                  onChange={(items) => update("trustedOrigins", items)}
                />
                <label class="checkbox-field" for="server-offline-access">
                  <input
                    id="server-offline-access"
                    type="checkbox"
                    checked={draft.requestOfflineAccess}
                    disabled={disabled}
                    onChange={(event) =>
                      update(
                        "requestOfflineAccess",
                        event.currentTarget.checked,
                      )
                    }
                  />
                  <span>
                    <strong>Request offline access when supported</strong>
                    <small>
                      Requests the offline_access scope only when advertised.
                      The provider may still omit a refresh token.
                    </small>
                  </span>
                </label>
              </details>
            </>
          )}
        </>
      )}
    </>
  );
}

function CreationReview({ draft }: { draft: Draft }) {
  const connection =
    draft.transportKind === "stdio"
      ? `Local process — ${draft.executable}`
      : `HTTP — ${draft.url.trim()}`;
  const authentication =
    draft.transportKind === "stdio"
      ? "Not applicable"
      : draft.authMode === "none"
        ? "No authentication"
        : draft.authMode === "bearer"
          ? "Bearer token"
          : "OAuth";
  const values = (items: readonly StringItem[]) =>
    items.length === 0 ? "None" : items.map((item) => item.value).join(", ");
  const pairs = (items: readonly PairItem[], separator: string) =>
    items.length === 0
      ? "None"
      : items.map((item) => `${item.name}${separator}${item.value}`).join(", ");
  return (
    <>
      <dl class="review-list" data-testid="server-creation-review">
        <div>
          <dt>Namespace</dt>
          <dd>{draft.namespace}</dd>
        </div>
        <div>
          <dt>Display name</dt>
          <dd>{draft.displayName}</dd>
        </div>
        <div>
          <dt>Initial state</dt>
          <dd>{draft.enabled ? "Enabled" : "Disabled"}</dd>
        </div>
        <div>
          <dt>Connection</dt>
          <dd>{connection}</dd>
        </div>
        {draft.transportKind === "stdio" ? (
          <>
            <div>
              <dt>Working directory</dt>
              <dd>{draft.workingDirectory}</dd>
            </div>
            <div>
              <dt>Arguments</dt>
              <dd>{values(draft.arguments)}</dd>
            </div>
            <div>
              <dt>Environment</dt>
              <dd>{pairs(draft.environment, "=")}</dd>
            </div>
            <div>
              <dt>Secret bindings</dt>
              <dd>{pairs(draft.secretEnvironment, " → ")}</dd>
            </div>
          </>
        ) : (
          <>
            <div>
              <dt>Protocol</dt>
              <dd>{draft.protocolMode}</dd>
            </div>
            <div>
              <dt>Authentication</dt>
              <dd>{authentication}</dd>
            </div>
            {draft.authMode === "oauth" && (
              <>
                <div>
                  <dt>OAuth registration</dt>
                  <dd>
                    {draft.registrationMode === "dynamic"
                      ? "Register Gateway automatically"
                      : `Existing client ${draft.clientID.trim()} (${draft.tokenEndpointAuthMethod})`}
                  </dd>
                </div>
                <div>
                  <dt>OAuth issuer</dt>
                  <dd>
                    {draft.issuer.trim() === ""
                      ? "Discover from same-origin metadata"
                      : draft.issuer.trim()}
                  </dd>
                </div>
                <div>
                  <dt>OAuth network origins</dt>
                  <dd>
                    {values(
                      draft.trustedOrigins.map((item) => ({
                        ...item,
                        value: item.value.trim(),
                      })),
                    )}
                  </dd>
                </div>
                <div>
                  <dt>Offline access</dt>
                  <dd>
                    {draft.requestOfflineAccess
                      ? "Request when advertised"
                      : "Do not request"}
                  </dd>
                </div>
              </>
            )}
          </>
        )}
      </dl>
      <p>
        The namespace is permanent. Creating an enabled server also schedules
        connection work.
      </p>
    </>
  );
}

export function ServerEditor({
  mutations,
  server,
  etag,
  onRefresh,
  notify,
  decodeServerValue,
}: {
  mutations: MutationCoordinator;
  server?: ServerView;
  etag?: string;
  onRefresh: () => void;
  notify: (message: string) => void;
  decodeServerValue: (value: unknown) => ServerView;
}) {
  const create = server === undefined;
  const initialDraft = useRef<Draft>(
    create ? blankDraft() : draftFromServer(server),
  );
  const [draft, setDraft] = useState<Draft>(initialDraft.current);
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
  const navigate = useUnsavedChanges(
    canonical(draft) !== canonical(initialDraft.current),
  );
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (server === undefined) return;
    const next = draftFromServer(server);
    initialDraft.current = next;
    setDraft(next);
  }, [server?.id]);

  const settle = async (
    submission: Promise<import("./mutation").MutationOutcome<MutationResult>>,
  ) => {
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      setBlockedETag(undefined);
      setNotice(undefined);
      notify(
        create
          ? "Server created."
          : outcome.value.operationID === null
            ? "Server settings saved."
            : "Server settings saved; applying changes.",
      );
      controller.abandon();
      initialDraft.current = draft;
      setDraft({ ...draft });
      if (create) navigate(`#/servers/${outcome.value.server.id}`, true);
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
          {create ? "Review and create" : "Save desired state"}
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
        title={create ? "Review server" : "Apply behavioral server change?"}
        consequence={
          create ? (
            <CreationReview draft={draft} />
          ) : (
            "Changing desired state or transport interrupts current server routing and schedules reconciliation."
          )
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
            <h2 id="server-editor-title">Create server</h2>
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
