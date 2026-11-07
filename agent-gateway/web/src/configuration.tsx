import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import {
  configurationInputBytes,
  decodeImport,
  decodePreview,
  resourceDestinations,
  singleDefinition,
  type Creation,
  type ImportResult,
  type ResourceKind,
} from "./config-contract";
import type { SessionClient } from "./session";
import type { MutationCoordinator, MutationSnapshot } from "./mutation";
import {
  ConfirmationDialog,
  FormField,
  StateNotice,
  sentenceCase,
} from "./primitives";
import { useUnsavedChanges } from "./navigation";
import { downloadConfigurationJSON } from "./sinks";
import { copyConfigurationText } from "./sinks-ui";

export function StableKeyField({
  value,
  onChange,
  disabled = false,
}: {
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  return (
    <FormField
      id="resource-stable-key"
      label="Stable key"
      optional
      hint="Immutable. Leave blank to generate a key."
    >
      {(attributes) => (
        <input
          {...attributes}
          value={value}
          disabled={disabled}
          onInput={(e) => onChange(e.currentTarget.value)}
          pattern={"[a-z][a-z0-9_\\-]{0,63}"}
          maxLength={64}
          placeholder="workshop_reader"
        />
      )}
    </FormField>
  );
}
export function ImportLink({
  kind,
  label,
}: {
  kind: ResourceKind;
  label: string;
}) {
  return (
    <a
      class="button-link secondary"
      href={`#/${resourceDestinations[kind]}/import`}
    >
      Import {label}
    </a>
  );
}
async function boundedText(response: Response): Promise<string> {
  if (
    response.headers.get("Content-Type") !== "application/json" ||
    response.body === null
  )
    throw new Error("Configuration unavailable.");
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    for (;;) {
      const part = await reader.read();
      if (part.done) break;
      length += part.value.length;
      if (length > configurationInputBytes)
        throw new Error("Configuration exceeds the browser's 1 MiB limit.");
      chunks.push(part.value);
    }
    const bytes = new Uint8Array(length);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.length;
    }
    return new TextDecoder("utf-8", { fatal: true }).decode(bytes);
  } finally {
    await reader.cancel();
    reader.releaseLock();
  }
}
export function ConfigActions({
  session,
  kind,
  resourceKey,
}: {
  session: SessionClient;
  kind: ResourceKind;
  resourceKey: string;
}) {
  const [message, setMessage] = useState<string>();
  const [busy, setBusy] = useState(false);
  const generation = useRef(0);
  const locationKey = window.location.hash;
  useLayoutEffect(() => {
    generation.current++;
    setBusy(false);
    setMessage(undefined);
    return () => {
      generation.current++;
    };
  }, [kind, resourceKey, locationKey]);
  const publish = async (copy: boolean) => {
    if (busy) return;
    const owned = generation.current;
    const current = () =>
      generation.current === owned && window.location.hash === locationKey;
    setBusy(true);
    setMessage(undefined);
    try {
      await session.runProtected(async (context) => {
        const response = await fetch(
          `/api/v2/config/resources/${kind}/${resourceKey}`,
          {
            credentials: "same-origin",
            redirect: "error",
            signal: context.signal,
            headers: {
              Accept: "application/json",
              "X-CSRF-Token": context.csrfToken,
            },
          },
        );
        if (await context.sessionLost(response)) return;
        if (!response.ok) throw new Error("Configuration unavailable.");
        const raw = await boundedText(response);
        if (singleDefinition(raw, kind).key !== resourceKey)
          throw new Error("Configuration identity mismatch.");
        if (!current() || !context.isCurrent()) return;
        const ok = copy
          ? (await copyConfigurationText(raw)) === "copied"
          : downloadConfigurationJSON(
              new Blob([raw], { type: "application/json" }),
              `${kind}-${resourceKey}.json`,
            );
        if (current() && context.isCurrent())
          setMessage(
            ok
              ? copy
                ? "Config copied."
                : "Config downloaded."
              : "Config could not be published. Try the action again.",
          );
      });
    } catch {
      if (current())
        setMessage("Config unavailable. Refresh the resource and try again.");
    } finally {
      if (current()) setBusy(false);
    }
  };
  return (
    <section class="detail-section" aria-label="Resource configuration">
      <h2>Configuration</h2>
      <dl class="detail-facts">
        <div>
          <dt>Immutable key</dt>
          <dd class="technical-value">{resourceKey}</dd>
        </div>
      </dl>
      <div class="form-actions">
        <button
          type="button"
          class="secondary"
          disabled={busy}
          onClick={() => void publish(true)}
        >
          Copy config
        </button>
        <button
          type="button"
          class="secondary"
          disabled={busy}
          onClick={() => void publish(false)}
        >
          Download config
        </button>
      </div>
      <p role="status">{message}</p>
    </section>
  );
}
function DefinitionFields({ raw }: { raw: string }) {
  const document = JSON.parse(raw) as { spec: Record<string, unknown> | null };
  if (document.spec === null) return null;
  return (
    <dl class="detail-facts">
      {Object.entries(document.spec)
        .filter(([, value]) => value === null || typeof value !== "object")
        .map(([name, value]) => (
          <div key={name}>
            <dt>
              {name === "url"
                ? "URL"
                : name === "http_default"
                  ? "HTTP default"
                  : sentenceCase(name)}
            </dt>
            <dd>
              {value === null
                ? "None"
                : typeof value === "boolean"
                  ? value
                    ? "Yes"
                    : "No"
                  : String(value)}
            </dd>
          </div>
        ))}
    </dl>
  );
}

const resourceLabels: Record<ResourceKind, string> = {
  agent: "agent",
  mcp_server: "MCP server",
  mcp_grant: "MCP grant",
  http_credential: "HTTP credential",
  http_grant: "HTTP grant",
  git_credential: "Git credential",
  git_repository: "Git repository",
  git_grant: "Git grant",
};

export function ResourceImport({
  session,
  mutations,
  kind,
}: {
  session: SessionClient;
  mutations: MutationCoordinator;
  kind: ResourceKind;
}) {
  const [raw, setRaw] = useState("");
  const [error, setError] = useState<string>();
  const [preview, setPreview] = useState<Creation>();
  const [result, setResult] = useState<ImportResult>();
  const [checking, setChecking] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [controller] = useState(() => mutations.create<ImportResult>());
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const version = useRef(0);
  const button = useRef<HTMLButtonElement>(null);
  const navigate = useUnsavedChanges(raw !== "" && result === undefined);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(
    () => () => {
      version.current++;
      controller.close();
    },
    [controller],
  );
  const locked =
    checking ||
    mutation.state === "submitting" ||
    mutation.state === "uncertain" ||
    result !== undefined;
  const change = (value: string) => {
    version.current++;
    setRaw(value);
    setPreview(undefined);
    setError(undefined);
  };
  const validate = async () => {
    const generation = ++version.current;
    setError(undefined);
    setPreview(undefined);
    let definition: Record<string, unknown>;
    try {
      definition = singleDefinition(raw, kind);
    } catch (e) {
      setError((e as Error).message);
      return;
    }
    setChecking(true);
    try {
      await session.runProtected(async (context) => {
        const response = await fetch("/api/v2/config/preview", {
          method: "POST",
          body: raw,
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        });
        if (await context.sessionLost(response)) return;
        if (!response.ok) {
          if (context.isCurrent() && generation === version.current)
            setError(
              response.status === 409
                ? "An existing or reserved key conflicts, or a dependency is missing. Edit the definition and validate again."
                : "Online validation failed. Check the definition and its dependencies, then validate again.",
            );
          return;
        }
        const value = decodePreview(
          JSON.parse(await boundedText(response)),
          kind,
          definition.key as string,
          definition.reserved === true,
        );
        if (context.isCurrent() && generation === version.current)
          setPreview(value);
      });
    } catch {
      if (generation === version.current)
        setError(
          "Online preview unavailable. Check the connection and validate again.",
        );
    } finally {
      if (generation === version.current) setChecking(false);
    }
  };
  const submit = async () => {
    if (!preview) return;
    setConfirm(false);
    setError(undefined);
    controller.begin({
      route: "/api/v2/config/import",
      method: "POST",
      body: raw,
      precondition: null,
      requiresPrecondition: false,
      idempotency: "none",
      successStatuses: [201],
      decode: async (response) =>
        decodeImport(
          JSON.parse(await boundedText(response)),
          kind,
          preview.key,
          preview.reserved,
        ),
    });
    controller.confirm();
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged") {
      setResult(outcome.value);
    } else if (outcome.kind === "rejected") {
      setPreview(undefined);
      setError(
        outcome.problem.status === 409
          ? "Creation was rejected: a key or dependency changed since preview. Nothing was imported. Validate again."
          : "Creation was rejected. Your input is preserved; check the definition and validate again.",
      );
    }
  };
  const created = result?.creations[0];
  return (
    <section class="panel domain-panel" aria-labelledby="config-import-title">
      <nav class="detail-navigation">
        <a href={`#/${resourceDestinations[kind]}`}>Back to collection</a>
      </nav>
      <h2 id="config-import-title">Import {resourceLabels[kind]}</h2>
      {result && created ? (
        <StateNotice
          state="neutral"
          title={
            created.reserved
              ? "Key reservation committed"
              : "Resource creation committed"
          }
        >
          <dl class="detail-facts">
            <div>
              <dt>Immutable key</dt>
              <dd class="technical-value">{created.key}</dd>
            </div>
          </dl>
          <p>
            {result.activation === "pending"
              ? "Activation pending — creation does not establish upstream readiness."
              : "Configuration committed. Upstream readiness is reported on the resource."}
          </p>
          {preview &&
            preview.requirements.filter(
              (value) => value !== "activate_after_commit",
            ).length > 0 && (
              <p>
                Required next:{" "}
                {preview.requirements
                  .filter((value) => value !== "activate_after_commit")
                  .map(sentenceCase)
                  .join(", ")}
                .
              </p>
            )}
          <div class="form-actions">
            {!created.reserved && (
              <button
                type="button"
                class="primary-action"
                onClick={() =>
                  navigate(
                    `#/${resourceDestinations[kind]}/${created.id}${kind === "mcp_server" ? "?tab=status" : ""}`,
                    true,
                  )
                }
              >
                View created resource
              </button>
            )}
            {!created.reserved && kind === "mcp_server" && (
              <a
                class="button-link"
                href={`#/mcp/servers/${created.id}?tab=authentication`}
              >
                Set up authentication
              </a>
            )}
          </div>
        </StateNotice>
      ) : (
        <>
          <p>
            Create only. Existing or reserved keys cannot be reused. Bundles
            belong in System Configuration.
          </p>
          <FormField
            id="configuration-json"
            label="Configuration JSON"
            hint="One matching v2 definition, at most 1 MiB. Do not paste secret values."
          >
            {(attributes) => (
              <textarea
                {...attributes}
                rows={12}
                value={raw}
                disabled={locked}
                maxLength={configurationInputBytes}
                placeholder="Paste a version 2 resource definition"
                onInput={(e) => change(e.currentTarget.value)}
              />
            )}
          </FormField>
          <FormField id="configuration-file" label="Upload JSON file">
            {(attributes) => (
              <input
                {...attributes}
                type="file"
                accept=".json,application/json"
                disabled={locked}
                onChange={async (e) => {
                  const file = e.currentTarget.files?.[0];
                  e.currentTarget.value = "";
                  if (!file) return;
                  const generation = ++version.current;
                  setPreview(undefined);
                  setError(undefined);
                  if (file.size > configurationInputBytes) {
                    setError("Configuration must be at most 1 MiB.");
                    return;
                  }
                  setChecking(true);
                  try {
                    const text = new TextDecoder("utf-8", {
                      fatal: true,
                    }).decode(await file.arrayBuffer());
                    if (generation === version.current) {
                      change(text);
                      setChecking(false);
                    }
                  } catch {
                    if (generation === version.current) {
                      setChecking(false);
                      setError("The JSON file could not be read as UTF-8.");
                    }
                  }
                }}
              />
            )}
          </FormField>
          {error && (
            <StateNotice state="error" title="Import not completed">
              <p>{error}</p>
            </StateNotice>
          )}
          {checking && (
            <StateNotice state="loading" title="Validating configuration" />
          )}
          {mutation.state === "submitting" && (
            <StateNotice
              state="loading"
              title={preview?.reserved ? "Reserving key" : "Creating resource"}
            />
          )}
          {mutation.state === "uncertain" && (
            <StateNotice state="warning" title="Creation outcome unknown">
              <p>
                Check the collection for the immutable key before starting
                another import. This request will not be replayed.
              </p>
            </StateNotice>
          )}
          <div class="form-actions">
            <button
              type="button"
              class={preview === undefined ? "primary-action" : "secondary"}
              disabled={locked || raw === ""}
              onClick={() => void validate()}
            >
              {checking ? "Validating…" : "Validate and preview"}
            </button>
          </div>
          {preview && (
            <section class="detail-group" aria-label="Import preview">
              <h3>Creation preview</h3>
              <dl class="detail-facts">
                <div>
                  <dt>Immutable key</dt>
                  <dd>{preview.key}</dd>
                </div>
                <div>
                  <dt>Action</dt>
                  <dd>
                    {preview.reserved
                      ? "Reserve deleted key"
                      : "Create resource"}
                  </dd>
                </div>
                <div>
                  <dt>Dependencies</dt>
                  <dd>
                    {preview.dependencies.length
                      ? preview.dependencies
                          .map(
                            (ref) =>
                              `${ref.kind}${ref.key ? ` / ${ref.key}` : ""}`,
                          )
                          .join(", ")
                      : "None"}
                  </dd>
                </div>
                <div>
                  <dt>Setup requirements</dt>
                  <dd>
                    {preview.requirements.length
                      ? preview.requirements.map(sentenceCase).join(", ")
                      : "None"}
                  </dd>
                </div>
              </dl>
              <h3>Intended resource fields</h3>
              <DefinitionFields raw={raw} />
              <details>
                <summary>Exact configuration JSON</summary>
                <pre class="inert-json">{raw}</pre>
              </details>
              <p>
                Preview is advisory. Import revalidates atomically; no
                dependencies or grants are added implicitly.
              </p>
              <div class="form-actions">
                <button
                  type="button"
                  class="primary-action"
                  ref={button}
                  disabled={locked || mutation.availability !== "enabled"}
                  onClick={() => setConfirm(true)}
                >
                  Import
                </button>
              </div>
            </section>
          )}
        </>
      )}
      <ConfirmationDialog
        id="config-import-confirm"
        open={confirm}
        title="Import resource?"
        consequence={`${preview?.reserved ? "Reserve the deleted key" : `Create ${resourceLabels[kind]}`} "${preview?.key ?? ""}". Existing resources are never updated and no credential values are supplied.`}
        confirmLabel="Confirm import"
        returnFocus={button}
        onCancel={() => setConfirm(false)}
        onConfirm={() => void submit()}
      />
    </section>
  );
}
