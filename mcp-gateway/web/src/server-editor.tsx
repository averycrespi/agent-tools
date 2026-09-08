import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { useUnsavedChanges } from "./navigation";
import type {
  MutationController,
  MutationCoordinator,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import {
  BinaryToggle,
  ConfirmationDialog,
  FormField,
  StateNotice,
} from "./primitives";
import { decodeOperation } from "./server-operation-model";
import type { ServerView } from "./server-reads";
import type { ServerConfigurationContext } from "./session";

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
  callbackURI: string;
  authServerMetadataURL: string;
  explicitScopes: boolean;
  scopes: StringItem[];
}
interface MutationResult {
  server: ServerView;
  operationID: string | null;
  etag: string;
}
type DraftValidationField =
  | "url"
  | "issuer"
  | "clientID"
  | "origin"
  | "callbackURI"
  | "authServerMetadataURL"
  | "scopes";
class DraftValidationError extends Error {
  constructor(
    readonly field: DraftValidationField,
    message: string,
    readonly itemID?: string,
  ) {
    super(message);
  }
}

const namespacePattern = /^[a-z][a-z0-9_-]{0,31}$/;
const secretSlotPattern = /^[a-z][a-z0-9_]{0,63}$/;
const absolutePathPattern = /^\/(?:[^\0]*)$/;
let nextItemID = 0;
const protocolLabels = {
  auto: "Automatic (recommended)",
  modern: "Current only — 2026-07-28",
  legacy: "Legacy only — 2025-11-25",
};
const tokenAuthLabels = {
  none: "Public client — no client secret",
  client_secret_basic: "Client secret in HTTP Basic",
  client_secret_post: "Client secret in request body",
};

function isCanonicalAbsolutePath(value: string): boolean {
  if (!absolutePathPattern.test(value)) return false;
  if (value === "/") return true;
  return (
    value.endsWith("/") === false &&
    value
      .slice(1)
      .split("/")
      .every((segment) => segment !== "" && segment !== "." && segment !== "..")
  );
}

function serverConfigurationContextMessage(
  context: ServerConfigurationContext,
): string {
  const labels: Record<string, string> = {
    configuration: "Server configuration",
    namespace: "Namespace",
    display_name: "Display name",
    enabled: "Initial state",
    transport: "Connection",
    "transport.kind": "Connection type",
    "transport.executable": "Executable",
    "transport.arguments": "Arguments",
    "transport.working_directory": "Working directory",
    "transport.environment": "Environment",
    "transport.secret_environment": "Secret environment bindings",
    "transport.url": "HTTP endpoint",
    "transport.protocol_mode": "Protocol mode",
    "transport.authentication": "Authentication",
    "transport.authentication.mode": "Authentication mode",
    "transport.authentication.trusted_origins": "Trusted origins",
    "transport.authentication.request_offline_access": "Offline access policy",
    "transport.authentication.callback_uri": "Callback URI",
    "transport.authentication.auth_server_metadata_url":
      "Authorization metadata URL",
    "transport.authentication.scopes": "Initial scopes",
    "transport.authentication.registration": "OAuth registration",
    "transport.authentication.registration.mode": "OAuth registration mode",
    "transport.authentication.registration.issuer": "OAuth issuer",
    "transport.authentication.registration.client_id": "OAuth client ID",
    "transport.authentication.registration.token_endpoint_auth_method":
      "Token endpoint authentication method",
  };
  const label = labels[context.field] ?? "Server configuration";
  switch (context.rule) {
    case "required":
      return `${label} is required.`;
    case "maximum":
      return `${label} exceeds its maximum size or item count.`;
    case "unique":
      return `${label} must not contain duplicates.`;
    case "disjoint":
      return `${label} conflicts with another configuration source.`;
    case "canonical_absolute_path":
      return `${label} must be an absolute canonical path without a trailing slash, empty segment, ".", or ".." segment.`;
    case "canonical_url":
      if (context.field === "transport.authentication.callback_uri")
        return "Callback URI must be an exact HTTP loopback URL with an explicit port and path, without credentials, query, fragment, or encoded path.";
      if (context.field === "transport.authentication.auth_server_metadata_url")
        return "Authorization metadata URL must be an exact canonical HTTPS URL with a path and without credentials or fragment. A bounded query is allowed.";
      return `${label} must be a canonical URL without credentials, a query, fragment, uppercase hostname, or default port.`;
    case "transport_policy":
      return `${label} does not satisfy the selected transport security policy.`;
    default:
      return `${label} is invalid.`;
  }
}

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
    callbackURI: "",
    authServerMetadataURL: "",
    explicitScopes: false,
    scopes: [],
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
    if (!isCanonicalAbsolutePath(draft.executable))
      throw new Error(
        'Executable must be an absolute canonical path without a trailing slash, empty segment, ".", or ".." segment.',
      );
    if (!isCanonicalAbsolutePath(draft.workingDirectory))
      throw new Error(
        'Working directory must be an absolute canonical path without a trailing slash, empty segment, ".", or ".." segment.',
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
  if (normalizedURL === "")
    throw new DraftValidationError("url", "Enter an HTTP endpoint.");
  let parsedURL: URL;
  try {
    parsedURL = new URL(normalizedURL);
  } catch {
    throw new DraftValidationError("url", "HTTP URL must be absolute.");
  }
  if (parsedURL.protocol !== "http:" && parsedURL.protocol !== "https:")
    throw new DraftValidationError(
      "url",
      "HTTP endpoint must use http or https.",
    );
  if (
    parsedURL.username !== "" ||
    parsedURL.password !== "" ||
    parsedURL.search !== "" ||
    parsedURL.hash !== ""
  )
    throw new DraftValidationError(
      "url",
      "HTTP URL cannot contain credentials, query, or fragment.",
    );
  let authentication: unknown;
  if (draft.authMode === "none" || draft.authMode === "bearer") {
    authentication = { mode: draft.authMode };
  } else {
    const trustedOrigins: string[] = [];
    const seenOrigins = new Set<string>();
    for (const item of draft.trustedOrigins) {
      const origin = item.value.trim();
      if (origin === "")
        throw new DraftValidationError(
          "origin",
          "Enter an OAuth network origin.",
          item.id,
        );
      let parsedOrigin: URL;
      try {
        parsedOrigin = new URL(origin);
      } catch {
        throw new DraftValidationError(
          "origin",
          "OAuth network origin must be an absolute HTTP or HTTPS origin.",
          item.id,
        );
      }
      if (
        (parsedOrigin.protocol !== "https:" &&
          parsedOrigin.protocol !== "http:") ||
        parsedOrigin.username !== "" ||
        parsedOrigin.password !== "" ||
        parsedOrigin.origin !== origin
      )
        throw new DraftValidationError(
          "origin",
          "OAuth network origin must contain only an HTTP or HTTPS scheme, host, and optional port.",
          item.id,
        );
      if (seenOrigins.has(origin))
        throw new DraftValidationError(
          "origin",
          "OAuth network origins must be unique.",
          item.id,
        );
      seenOrigins.add(origin);
      trustedOrigins.push(origin);
    }
    const issuer = draft.issuer.trim();
    if (issuer !== "") {
      let parsedIssuer: URL;
      try {
        parsedIssuer = new URL(issuer);
      } catch {
        throw new DraftValidationError(
          "issuer",
          "OAuth issuer must be an absolute HTTPS URL.",
        );
      }
      if (
        parsedIssuer.protocol !== "https:" ||
        parsedIssuer.username !== "" ||
        parsedIssuer.password !== "" ||
        parsedIssuer.search !== "" ||
        parsedIssuer.hash !== ""
      )
        throw new DraftValidationError(
          "issuer",
          "OAuth issuer must be an HTTPS URL without credentials, query, or fragment.",
        );
    }
    const clientID = draft.clientID.trim();
    if (draft.registrationMode === "static" && clientID.length === 0)
      throw new DraftValidationError("clientID", "Enter the OAuth client ID.");
    const callbackURI = draft.callbackURI;
    if (callbackURI !== "") {
      const match =
        /^http:\/\/(localhost|127(?:\.[0-9]{1,3}){3}|\[::1\]):([1-9][0-9]{0,4})(\/[^?#%\\\s]*)$/.exec(
          callbackURI,
        );
      if (
        match === null ||
        Number(match[2]) > 65535 ||
        !isCanonicalAbsolutePath(match[3] ?? "") ||
        new URL(callbackURI).pathname !== match[3] ||
        (match[1]?.startsWith("127.") &&
          match[1]
            .split(".")
            .some(
              (part) => Number(part) > 255 || String(Number(part)) !== part,
            )) ||
        new TextEncoder().encode(callbackURI).byteLength > 8192
      )
        throw new DraftValidationError(
          "callbackURI",
          "Use an exact HTTP localhost or numeric-loopback URI with an explicit port and canonical path, such as http://localhost:3118/callback. No credentials, query, fragment, or encoded path.",
        );
    }
    const metadataURL = draft.authServerMetadataURL;
    if (metadataURL !== "") {
      let parsed: URL;
      try {
        parsed = new URL(metadataURL);
      } catch {
        throw new DraftValidationError(
          "authServerMetadataURL",
          "Enter an absolute HTTPS metadata URL.",
        );
      }
      if (
        parsed.protocol !== "https:" ||
        parsed.username !== "" ||
        parsed.password !== "" ||
        metadataURL.includes("#") ||
        parsed.href !== metadataURL ||
        new TextEncoder().encode(metadataURL).byteLength > 8192 ||
        !isCanonicalAbsolutePath(parsed.pathname)
      )
        throw new DraftValidationError(
          "authServerMetadataURL",
          "Use the exact canonical HTTPS metadata URL with a path, without credentials or fragment. A bounded query is allowed.",
        );
    }
    const scopes = draft.scopes.map((item) => item.value);
    if (
      draft.explicitScopes &&
      (scopes.length > 64 ||
        scopes.some(
          (scope) => !/^[\x21\x23-\x5b\x5d-\x7e]{1,256}$/.test(scope),
        ) ||
        [...new Set(scopes)].join(" ").length > 8192)
    )
      throw new DraftValidationError(
        "scopes",
        "Enter one scope per row, without spaces, quotes, or backslashes. Remove unused rows. Use at most 64 ASCII scopes (256 bytes each, 8192 bytes total).",
      );
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
      ...(callbackURI === "" ? {} : { callback_uri: callbackURI }),
      ...(metadataURL === "" ? {} : { auth_server_metadata_url: metadataURL }),
      ...(draft.explicitScopes ? { scopes: [...new Set(scopes)].sort() } : {}),
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
  draft.callbackURI = (authentication.callback_uri as string | undefined) ?? "";
  draft.authServerMetadataURL =
    (authentication.auth_server_metadata_url as string | undefined) ?? "";
  draft.explicitScopes = authentication.scopes !== undefined;
  draft.scopes = stringItems(
    "scope",
    (authentication.scopes as string[] | undefined) ?? [],
  );
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
  errors = {},
  error,
  onChange,
}: {
  id: string;
  label: string;
  hint: string;
  itemLabel: string;
  addLabel: string;
  items: StringItem[];
  disabled: boolean;
  errors?: Readonly<Record<string, string>>;
  error?: string;
  onChange: (items: StringItem[]) => void;
}) {
  return (
    <fieldset
      class="collection-field"
      id={id}
      aria-describedby={
        error === undefined ? `${id}-hint` : `${id}-hint ${id}-error`
      }
      aria-invalid={error === undefined ? undefined : true}
    >
      <legend>
        {label}
        <span class="optional-label"> (optional)</span>
      </legend>
      <p class="field-hint" id={`${id}-hint`}>
        {hint}
      </p>
      {error !== undefined && (
        <span class="field-error" id={`${id}-error`} role="alert">
          {error}
        </span>
      )}
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
            aria-invalid={
              errors[item.id] === undefined && error === undefined
                ? undefined
                : true
            }
            aria-describedby={[
              `${id}-hint`,
              ...(error === undefined ? [] : [`${id}-error`]),
              ...(errors[item.id] === undefined
                ? []
                : [`${id}-${item.id}-error`]),
            ].join(" ")}
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
          {errors[item.id] !== undefined && (
            <span
              class="field-error collection-error"
              id={`${id}-${item.id}-error`}
              role="alert"
            >
              {errors[item.id]}
            </span>
          )}
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
  addLabel,
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
  addLabel: string;
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
        {addLabel}
      </button>
    </fieldset>
  );
}

function EditorForm({
  draft,
  setDraft,
  disabled,
  namespaceLocked,
  urlError,
  issuerError,
  clientIDError,
  originErrors,
  compatibilityErrors,
  configurationError,
  clearFieldError,
}: {
  draft: Draft;
  setDraft: (draft: Draft) => void;
  disabled: boolean;
  namespaceLocked: boolean;
  urlError: string | undefined;
  issuerError: string | undefined;
  clientIDError: string | undefined;
  originErrors: Readonly<Record<string, string>>;
  compatibilityErrors: Readonly<Record<string, string>>;
  configurationError: ServerConfigurationContext | undefined;
  clearFieldError: (field: DraftValidationField) => void;
}) {
  const [advancedOpen, setAdvancedOpen] = useState(
    () =>
      draft.issuer !== "" ||
      draft.callbackURI !== "" ||
      draft.authServerMetadataURL !== "" ||
      draft.explicitScopes ||
      draft.trustedOrigins.length > 0 ||
      draft.requestOfflineAccess,
  );
  useEffect(() => {
    const field = configurationError?.field;
    if (
      issuerError !== undefined ||
      Object.keys(compatibilityErrors).length > 0 ||
      Object.keys(originErrors).length > 0 ||
      (field !== undefined &&
        [
          "transport.authentication.registration.issuer",
          "transport.authentication.callback_uri",
          "transport.authentication.auth_server_metadata_url",
          "transport.authentication.scopes",
          "transport.authentication.trusted_origins",
          "transport.authentication.request_offline_access",
        ].includes(field))
    )
      setAdvancedOpen(true);
  }, [issuerError, compatibilityErrors, originErrors, configurationError]);
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
        hint="Namespace cannot be changed after creation. It prefixes this server's tool names. Use 1–32 lowercase letters, digits, underscores, or hyphens, starting with a letter; mcp_gateway is reserved."
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
      <FormField
        id="server-display-name"
        label="Display name"
        hint="A recognizable name for this server in Gateway. You can change it later."
        required
      >
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
        label="Server enabled"
        hint="When enabled, Gateway will try to connect when you save this server. Leave disabled to finish configuring credentials first."
      >
        {(attributes) => (
          <BinaryToggle
            attributes={attributes}
            checked={draft.enabled}
            disabled={disabled}
            testID="server-enabled"
            onChange={(enabled) => update("enabled", enabled)}
          />
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
            hint="Enter the full path to the executable on the Gateway host. Shell expressions such as ~ and $HOME are not expanded."
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
            hint="Enter the full path to the directory the process should run in, on the Gateway host."
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
          <StringListEditor
            id="server-argument"
            label="Arguments"
            hint="Add one command-line argument per row, in order. Values are passed as written, without shell expansion or shell quoting."
            itemLabel="Argument"
            addLabel="Add argument"
            items={draft.arguments}
            disabled={disabled}
            onChange={(items) => update("arguments", items)}
          />
          <PairListEditor
            id="server-environment"
            label="Environment variables"
            addLabel="Add variable"
            hint="Add non-secret environment variables for the process. Use Secret environment bindings for passwords and tokens."
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
            addLabel="Add binding"
            hint={
              namespaceLocked
                ? "Map each environment variable to a credential slot name, not a secret value. Manage the matching secret under Authentication."
                : "Map each environment variable to a credential slot name, not a secret value. After creating the server, add the matching secret under Authentication."
            }
            nameLabel="Environment variable"
            valueLabel="Credential slot"
            items={draft.secretEnvironment}
            disabled={disabled}
            onChange={(items) => update("secretEnvironment", items)}
          />
        </>
      )}
      {draft.transportKind === "streamable_http" && (
        <>
          <FormField
            id="server-url"
            label="HTTP endpoint"
            hint="Enter the server's full MCP endpoint URL, including its path. Do not include credentials, a query string, or a fragment."
            {...(urlError === undefined ? {} : { error: urlError })}
            required
          >
            {(attributes) => (
              <input
                {...attributes}
                value={draft.url}
                disabled={disabled}
                onInput={(event) => {
                  clearFieldError("url");
                  update("url", event.currentTarget.value);
                }}
              />
            )}
          </FormField>
          <FormField
            id="server-protocol-mode"
            label="Protocol preference"
            hint="Keep Automatic unless the provider requires a specific version. It tries the current protocol and uses legacy only if the server explicitly reports that the current version is unsupported."
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
                <option value="auto">{protocolLabels.auto}</option>
                <option value="modern">{protocolLabels.modern}</option>
                <option value="legacy">{protocolLabels.legacy}</option>
              </select>
            )}
          </FormField>
          <fieldset class="choice-field">
            <legend>Authentication</legend>
            <p class="field-hint">
              Choose the authentication method required by the server. Tokens
              and client secrets are managed separately and stored in the
              operating-system keyring, not in this form.
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
              {namespaceLocked
                ? "Manage this server's bearer token under Authentication."
                : "After creating the server, add its bearer token under Authentication."}{" "}
              Gateway stores it in the keyring and sends it in the Authorization
              header.
            </p>
          )}
          {draft.authMode === "oauth" && (
            <>
              <p class="bounded-note">
                {namespaceLocked
                  ? "Start or manage OAuth authorization under Authentication."
                  : "After creating the server, open Authentication to authorize Gateway with the provider."}{" "}
                Gateway stores access and refresh tokens in the keyring.
              </p>
              <FormField
                id="server-registration-mode"
                label="OAuth client registration"
                hint={
                  draft.registrationMode === "dynamic"
                    ? "Gateway registers a client when you start authorization. The provider must support automatic client registration."
                    : "Use an OAuth application you have already registered with the provider. Enter its client details below."
                }
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
              {draft.registrationMode === "static" && (
                <>
                  <FormField
                    id="server-client-id"
                    label="Client ID"
                    hint="Enter the client ID assigned when you registered your OAuth application with the provider. This is not the client secret."
                    {...(clientIDError === undefined
                      ? {}
                      : { error: clientIDError })}
                    required
                  >
                    {(attributes) => (
                      <input
                        {...attributes}
                        value={draft.clientID}
                        disabled={disabled}
                        onInput={(event) => {
                          clearFieldError("clientID");
                          update("clientID", event.currentTarget.value);
                        }}
                      />
                    )}
                  </FormField>
                  <FormField
                    id="server-token-auth"
                    label="Token endpoint authentication"
                    hint="Choose the method configured for your OAuth application. For either client-secret method, add the secret separately under Authentication."
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
                        <option value="none">{tokenAuthLabels.none}</option>
                        <option value="client_secret_basic">
                          {tokenAuthLabels.client_secret_basic}
                        </option>
                        <option value="client_secret_post">
                          {tokenAuthLabels.client_secret_post}
                        </option>
                      </select>
                    )}
                  </FormField>
                </>
              )}
              <details
                class="form-disclosure"
                open={advancedOpen}
                onToggle={(event) => setAdvancedOpen(event.currentTarget.open)}
              >
                <summary>
                  {advancedOpen
                    ? "Hide advanced OAuth settings"
                    : "Show advanced OAuth settings"}
                </summary>
                <FormField
                  id="server-issuer"
                  label="OAuth issuer URL"
                  hint="Enter the exact HTTPS issuer URL from your provider's OAuth documentation, not its authorization or token endpoint. You can leave this blank if the MCP server advertises exactly one issuer with the same scheme, hostname, and port as its HTTP endpoint."
                  {...(issuerError === undefined ? {} : { error: issuerError })}
                  optional
                >
                  {(attributes) => (
                    <input
                      {...attributes}
                      value={draft.issuer}
                      disabled={disabled}
                      onInput={(event) => {
                        clearFieldError("issuer");
                        update("issuer", event.currentTarget.value);
                      }}
                    />
                  )}
                </FormField>
                <FormField
                  id="server-callback-uri"
                  label="Callback URI"
                  optional
                  hint="Leave blank to use Gateway's default callback. To use a different callback, enter the exact HTTP loopback redirect URL registered with your provider, including port and path. That port must be free; Gateway listens there only during authorization."
                  {...(compatibilityErrors.callbackURI === undefined
                    ? {}
                    : { error: compatibilityErrors.callbackURI })}
                >
                  {(attributes) => (
                    <input
                      {...attributes}
                      value={draft.callbackURI}
                      disabled={disabled}
                      placeholder="http://localhost:3118/callback"
                      onInput={(event) => {
                        clearFieldError("callbackURI");
                        update("callbackURI", event.currentTarget.value);
                      }}
                    />
                  )}
                </FormField>
                <FormField
                  id="server-auth-metadata-url"
                  label="Authorization metadata URL"
                  optional
                  hint="Leave blank for standard OAuth discovery. Set this only if your provider specifies a different HTTPS metadata URL. It does not change the issuer; if this URL fails, Gateway will not try another location."
                  {...(compatibilityErrors.authServerMetadataURL === undefined
                    ? {}
                    : { error: compatibilityErrors.authServerMetadataURL })}
                >
                  {(attributes) => (
                    <input
                      {...attributes}
                      value={draft.authServerMetadataURL}
                      disabled={disabled}
                      onInput={(event) => {
                        clearFieldError("authServerMetadataURL");
                        update(
                          "authServerMetadataURL",
                          event.currentTarget.value,
                        );
                      }}
                    />
                  )}
                </FormField>
                <label class="checkbox-field" for="server-explicit-scopes">
                  <input
                    id="server-explicit-scopes"
                    type="checkbox"
                    checked={draft.explicitScopes}
                    disabled={disabled}
                    onChange={(event) =>
                      update("explicitScopes", event.currentTarget.checked)
                    }
                  />
                  <span>
                    <strong>Configure initial scopes explicitly</strong>
                    <small>
                      Leave off to use the MCP server's advertised defaults.
                      Turn on to choose scopes below; an empty list requests no
                      initial scopes. Offline access is controlled separately.
                    </small>
                  </span>
                </label>
                {draft.explicitScopes && (
                  <StringListEditor
                    id="server-initial-scopes"
                    label="Initial scopes"
                    hint="Add scope names from your provider's documentation, one per row. These replace the server's defaults; duplicates are removed. Adding permissions later requires authorizing the server again."
                    itemLabel="Scope"
                    addLabel="Add scope"
                    items={draft.scopes}
                    disabled={disabled}
                    {...(compatibilityErrors.scopes === undefined
                      ? {}
                      : { error: compatibilityErrors.scopes })}
                    onChange={(items) => {
                      clearFieldError("scopes");
                      update("scopes", items);
                    }}
                  />
                )}
                <StringListEditor
                  id="server-oauth-origin"
                  label="Additional OAuth origins allowed on restricted networks"
                  hint="Allow OAuth connections to additional private-network or loopback hosts. Enter each HTTPS origin (scheme, hostname, and optional port), without a path. The MCP server's origin is already allowed. This does not relax TLS checks or browser access rules."
                  itemLabel="OAuth origin"
                  addLabel="Add OAuth origin"
                  items={draft.trustedOrigins}
                  disabled={disabled}
                  errors={originErrors}
                  onChange={(items) => {
                    clearFieldError("origin");
                    update("trustedOrigins", items);
                  }}
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
                      Ask for offline_access so Gateway can refresh access
                      without another sign-in. Gateway requests it only if the
                      provider advertises support; a refresh token is not
                      guaranteed.
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
              <dd>{protocolLabels[draft.protocolMode]}</dd>
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
                      : `Existing client ${draft.clientID.trim()} (${tokenAuthLabels[draft.tokenEndpointAuthMethod]})`}
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
                  <dt>Callback URI</dt>
                  <dd>
                    {draft.callbackURI === ""
                      ? "Gateway main callback"
                      : draft.callbackURI}
                  </dd>
                </div>
                <div>
                  <dt>Authorization metadata URL</dt>
                  <dd>
                    {draft.authServerMetadataURL === ""
                      ? "Standard discovery"
                      : draft.authServerMetadataURL}
                  </dd>
                </div>
                <div>
                  <dt>Initial scopes</dt>
                  <dd>
                    {!draft.explicitScopes
                      ? "Server-advertised defaults"
                      : draft.scopes.length === 0
                        ? "No initial scopes (explicit)"
                        : [...new Set(draft.scopes.map((item) => item.value))]
                            .sort()
                            .join(" ")}
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
        The namespace is permanent. If enabled, Gateway will try to connect
        after the server is created.
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
  const [urlError, setURLError] = useState<string>();
  const [issuerError, setIssuerError] = useState<string>();
  const [clientIDError, setClientIDError] = useState<string>();
  const [originErrors, setOriginErrors] = useState<Record<string, string>>({});
  const [compatibilityErrors, setCompatibilityErrors] = useState<
    Record<string, string>
  >({});
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
      else {
        onRefresh();
        navigate(
          outcome.value.operationID === null
            ? `#/servers/${outcome.value.server.id}`
            : `#/servers/${outcome.value.server.id}/operations/${outcome.value.operationID}`,
          true,
        );
      }
    } else if (
      outcome.kind === "rejected" &&
      outcome.requiresRefresh &&
      !create
    ) {
      setBlockedETag(etag);
    }
  };
  const clearFieldError = (field: DraftValidationField) => {
    if (field === "url") setURLError(undefined);
    else if (field === "issuer") setIssuerError(undefined);
    else if (field === "clientID") setClientIDError(undefined);
    else if (field === "origin") setOriginErrors({});
    else setCompatibilityErrors({});
  };
  const prepare = ():
    | { spec: MutationSpec<MutationResult>; behavioral: boolean }
    | undefined => {
    setError(undefined);
    setURLError(undefined);
    setIssuerError(undefined);
    setClientIDError(undefined);
    setOriginErrors({});
    setCompatibilityErrors({});
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
      const message =
        caught instanceof Error
          ? caught.message
          : "Invalid server configuration.";
      if (caught instanceof DraftValidationError) {
        if (caught.field === "url") setURLError(message);
        else if (caught.field === "issuer") setIssuerError(message);
        else if (caught.field === "clientID") setClientIDError(message);
        else if (caught.itemID !== undefined)
          setOriginErrors({ [caught.itemID]: message });
        else setCompatibilityErrors({ [caught.field]: message });
      } else setError(message);
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
          urlError={urlError}
          issuerError={issuerError}
          clientIDError={clientIDError}
          originErrors={originErrors}
          compatibilityErrors={compatibilityErrors}
          configurationError={mutation.problem?.context}
          clearFieldError={clearFieldError}
        />
        {error !== undefined && (
          <StateNotice state="error" title="Check server configuration">
            <p>{error}</p>
          </StateNotice>
        )}
        {mutation.problem !== undefined && (
          <StateNotice state="error" title={mutation.problem.title}>
            {mutation.problem.context !== undefined && (
              <p>
                {serverConfigurationContextMessage(mutation.problem.context)}
              </p>
            )}
            {mutation.requiresRefresh && !create && (
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
          class={`${create ? "create-action" : "safe-action"} form-submit-action`}
          type="submit"
          disabled={disabled}
          data-testid="server-editor-submit"
        >
          {create ? "Review and create" : "Save settings"}
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
        destructive={!create}
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
    <section class="panel domain-panel" aria-labelledby="server-editor-title">
      <div class="panel-heading">
        <h2 id="server-editor-title">Configuration</h2>
      </div>
      {form}
    </section>
  );
}
