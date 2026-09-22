import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import { useUnsavedChanges } from "./navigation";
import type { MutationCoordinator, MutationSnapshot } from "./mutation";
import {
  CollectionTable,
  ConfirmationDialog,
  FormField,
  StateNotice,
  StatusLabel,
  TableIdentity,
} from "./primitives";
import type { SessionClient } from "./session";
import type { SensitiveSinkCoordinator } from "./sinks";
import { WriteOnlyField } from "./sinks-ui";
import {
  readCollectionPage,
  useCollectionPage,
  type ViewSnapshot,
} from "./view";

interface Credential {
  id: string;
  name: string;
  boundary: { host: string; port: number; allow_wildcard: boolean };
  recipe: { header: string; prefix: string };
  revision: string;
  available: boolean;
  referencing_grants: { id: string }[];
  created_at: string;
  updated_at: string;
}
const idPattern = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
function exact(value: unknown, keys: string[]): Record<string, unknown> {
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    Object.keys(value).sort().join() !== keys.sort().join()
  )
    throw new Error("HTTP credential data is unavailable.");
  return value as Record<string, unknown>;
}
export function decodeHTTPCredential(value: unknown): Credential {
  const r = exact(value, [
    "id",
    "name",
    "boundary",
    "recipe",
    "revision",
    "available",
    "referencing_grants",
    "created_at",
    "updated_at",
  ]);
  const b = exact(r.boundary, ["host", "port", "allow_wildcard"]);
  const recipe = exact(r.recipe, ["header", "prefix"]);
  if (
    typeof r.id !== "string" ||
    !idPattern.test(r.id) ||
    typeof r.name !== "string" ||
    r.name.length === 0 ||
    new TextEncoder().encode(r.name).length > 256 ||
    typeof r.revision !== "string" ||
    !/^[1-9][0-9]*$/.test(r.revision) ||
    typeof r.available !== "boolean" ||
    typeof b.host !== "string" ||
    b.host.length === 0 ||
    b.host.length > 255 ||
    typeof b.port !== "number" ||
    !Number.isInteger(b.port) ||
    b.port < 1 ||
    b.port > 65535 ||
    typeof b.allow_wildcard !== "boolean" ||
    typeof recipe.header !== "string" ||
    !/^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$/.test(recipe.header) ||
    typeof recipe.prefix !== "string" ||
    !/^[\x20-\x7e]{0,128}$/.test(recipe.prefix) ||
    !Array.isArray(r.referencing_grants) ||
    r.referencing_grants.length > 4096 ||
    typeof r.created_at !== "string" ||
    !Number.isFinite(Date.parse(r.created_at)) ||
    typeof r.updated_at !== "string" ||
    !Number.isFinite(Date.parse(r.updated_at))
  )
    throw new Error("HTTP credential data is unavailable.");
  for (const ref of r.referencing_grants) {
    const item = exact(ref, ["id"]);
    if (typeof item.id !== "string" || !idPattern.test(item.id))
      throw new Error("Invalid credential reference.");
  }
  return value as Credential;
}
async function decodeResponse(response: Response): Promise<Credential> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("HTTP credential data is unavailable.");
  const body = await response.text();
  if (body.length > 1024 * 1024)
    throw new Error("HTTP credential data is unavailable.");
  const credential = decodeHTTPCredential(JSON.parse(body));
  if (response.headers.get("ETag") !== etag(credential))
    throw new Error("HTTP credential revision is unavailable.");
  return credential;
}
function etag(c: Credential): string {
  return `"http-credential-${c.id}-${c.revision}"`;
}

interface Props {
  session: SessionClient;
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
  onRefresh: () => void;
}
export function HTTPCredentials(props: Props) {
  const selected = props.resolved.location.segments[1];
  const [detail, setDetail] = useState<Credential>();
  const [error, setError] = useState(false);
  useEffect(() => {
    let current = true;
    setError(false);
    if (selected === undefined || selected === "new") return;
    setDetail((prior) => (prior?.id === selected ? prior : undefined));
    void props.session
      .runProtected(async (context) => {
        const response = await fetch(`/api/v2/http/credentials/${selected}`, {
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
          headers: {
            Accept: "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        });
        if (await context.sessionLost(response)) return;
        if (!response.ok)
          throw new Error("HTTP credential data is unavailable.");
        const value = await decodeResponse(response);
        if (value.id !== selected)
          throw new Error("Invalid credential identity.");
        if (current) setDetail(value);
      })
      .catch(() => {
        if (current) setError(true);
      });
    return () => {
      current = false;
    };
  }, [selected, props.view.generation]);
  if (selected === undefined) return <CredentialCollection {...props} />;
  if (selected === "new") return <CredentialEditor {...props} mode="create" />;
  if (error)
    return (
      <StateNotice state="error" title="HTTP credential data unavailable">
        <p>
          Refresh to inspect current authority. Do not replay an uncertain
          mutation.
        </p>
      </StateNotice>
    );
  if (detail?.id !== selected)
    return <StateNotice state="loading" title="Loading HTTP credential" />;
  return (
    <div class="domain-view">
      <nav class="detail-navigation" aria-label="HTTP credential navigation">
        <a href="#/http/credentials">Back to HTTP credentials</a>
      </nav>
      <header class="detail-context">
        <h1 tabindex={-1}>{detail.name}</h1>
      </header>
      <section class="panel domain-panel">
        <div class="panel-heading">
          <h2>Credential details</h2>
          <StatusLabel state={detail.available ? "current" : "warning"}>
            {detail.available ? "Configured" : "Unavailable"}
          </StatusLabel>
        </div>
        <dl class="fact-grid">
          <div>
            <dt>Credential ID</dt>
            <dd class="technical-value">{detail.id}</dd>
          </div>
          <div>
            <dt>HTTPS boundary</dt>
            <dd>
              {detail.boundary.host}:{detail.boundary.port}
            </dd>
          </div>
          <div>
            <dt>Header recipe</dt>
            <dd>
              {detail.recipe.header}: {detail.recipe.prefix}
              <span class="secondary-text">[secret]</span>
            </dd>
          </div>
          <div>
            <dt>Revision</dt>
            <dd>{detail.revision}</dd>
          </div>
        </dl>
        <h3>Referencing grants</h3>
        {detail.referencing_grants.length === 0 ? (
          <p>No referencing grants</p>
        ) : (
          <ul>
            {detail.referencing_grants.map((ref) => (
              <li key={ref.id} class="technical-value">
                <a href={`#/http/grants/${ref.id}`}>{ref.id}</a>
              </li>
            ))}
          </ul>
        )}
      </section>
      <CredentialEditor
        {...props}
        key={`${detail.id}-edit`}
        credential={detail}
        mode="edit"
      />
      <CredentialEditor
        {...props}
        key={`${detail.id}-rotate`}
        credential={detail}
        mode="rotate"
      />
      <CredentialEditor
        {...props}
        key={`${detail.id}-delete`}
        credential={detail}
        mode="delete"
      />
    </div>
  );
}
function CredentialCollection(props: Props) {
  const navigate = useUnsavedChanges(false);
  const { items, controls } = useCollectionPage<Credential>(
    props.session,
    props.resolved,
    props.view,
    (_query, cursor, signal) =>
      readCollectionPage(
        props.session,
        `/api/v2/http/credentials?limit=50${cursor === null ? "" : `&cursor=${encodeURIComponent(cursor)}`}`,
        decodeHTTPCredential,
        signal,
      ),
    navigate,
    { key: "created", direction: "descending" },
  );
  return (
    <div class="domain-view">
      <div class="collection-toolbar">
        <a class="button-link create-action" href="#/http/credentials/new">
          Create HTTP credential
        </a>
      </div>
      <section class="panel domain-panel">
        <CollectionTable
          caption="HTTP credentials"
          rowHeaderKey="name"
          remote={controls}
          itemNames={{ singular: "credential", plural: "credentials" }}
          emptyTitle="No HTTP credentials"
          items={items}
          rowKey={(c) => c.id}
          initialSort={{ key: "created", direction: "descending" }}
          filters={[]}
          columns={[
            {
              key: "name",
              label: "Credential",
              role: "identity",
              render: (c) => (
                <TableIdentity
                  primary={<a href={`#/http/credentials/${c.id}`}>{c.name}</a>}
                  secondary={c.id}
                />
              ),
            },
            {
              key: "boundary",
              label: "HTTPS boundary",
              role: "relation",
              render: (c) => (
                <>
                  {c.boundary.host}:{c.boundary.port}
                </>
              ),
            },
            {
              key: "recipe",
              label: "Header recipe",
              role: "identity",
              render: (c) => (
                <>
                  {c.recipe.header}: {c.recipe.prefix}[secret]
                </>
              ),
            },
            {
              key: "status",
              label: "Status",
              role: "status",
              render: (c) => (
                <StatusLabel state={c.available ? "current" : "warning"}>
                  {c.available ? "Configured" : "Unavailable"}
                </StatusLabel>
              ),
            },
          ]}
        />
      </section>
    </div>
  );
}

function CredentialEditor({
  mutations,
  sinks,
  credential,
  mode,
  onRefresh,
  view,
}: Props & {
  credential?: Credential;
  mode: "create" | "edit" | "rotate" | "delete";
}) {
  const [name, setName] = useState(credential?.name ?? "");
  const [host, setHost] = useState(credential?.boundary.host ?? "");
  const [port, setPort] = useState(String(credential?.boundary.port ?? 443));
  const [wildcard, setWildcard] = useState(
    credential?.boundary.allow_wildcard ?? false,
  );
  const [header, setHeader] = useState(
    credential?.recipe.header ?? "Authorization",
  );
  const [prefix, setPrefix] = useState(credential?.recipe.prefix ?? "Bearer ");
  const [secret] = useState(() => sinks.createWriteOnly());
  const [controller] = useState(() => mutations.create<Credential | null>());
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [confirming, setConfirming] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [inputError, setInputError] = useState(false);
  const [blockedVersion, setBlockedVersion] = useState<number>();
  const [expectedETag, setExpectedETag] = useState(() =>
    credential === undefined ? null : etag(credential),
  );
  useEffect(() => {
    if (
      credential === undefined ||
      dirty ||
      confirming ||
      mutation.state === "submitting"
    )
      return;
    setExpectedETag(etag(credential));
    setName(credential.name);
    setHost(credential.boundary.host);
    setPort(String(credential.boundary.port));
    setWildcard(credential.boundary.allow_wildcard);
    setHeader(credential.recipe.header);
    setPrefix(credential.recipe.prefix);
  }, [credential, dirty, confirming, mutation.state]);
  const button = useRef<HTMLButtonElement>(null);
  const navigate = useUnsavedChanges(dirty);
  const inputID = `http-credential-secret-${mode}`;
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(
    () => () => {
      controller.close();
      secret.close();
    },
    [controller, secret],
  );
  const metadata = mode === "create" || mode === "edit";
  const material = mode === "create" || mode === "rotate";
  const blocked =
    mutation.state === "submitting" ||
    mutation.state === "uncertain" ||
    mutation.availability === "storage_latched" ||
    (blockedVersion !== undefined && view.generation <= blockedVersion) ||
    (mode === "delete" && (credential?.referencing_grants.length ?? 0) > 0);
  const title =
    mode === "create"
      ? "Create HTTP credential"
      : mode === "edit"
        ? "Edit boundary and recipe"
        : mode === "rotate"
          ? "Rotate secret"
          : "Delete credential";
  const clear = () => {
    secret.clear();
    setDirty(false);
  };
  const validInput = () => {
    const element = document.getElementById(inputID);
    return (
      (!metadata ||
        (name.trim() !== "" &&
          new TextEncoder().encode(name).byteLength <= 256 &&
          host !== "" &&
          new TextEncoder().encode(host.startsWith("*.") ? host.slice(2) : host)
            .byteLength <= 253 &&
          /^[1-9][0-9]*$/.test(port) &&
          Number(port) <= 65535 &&
          /^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$/.test(header) &&
          /^[\x20-\x7e]{0,128}$/.test(prefix))) &&
      (!material ||
        (element instanceof HTMLInputElement &&
          element.value !== "" &&
          element.value.length + prefix.length <= 4096 &&
          /^[\x20-\x7e]+$/.test(element.value)))
    );
  };
  const review = () => {
    const valid = validInput();
    setInputError(!valid);
    if (valid) setConfirming(true);
    else secret.clear();
  };
  const submit = () => {
    setConfirming(false);
    if (!validInput()) {
      clear();
      setInputError(true);
      return;
    }
    const element = document.getElementById(inputID);
    const value = element instanceof HTMLInputElement ? element.value : "";
    const definition = {
      name,
      boundary: { host, port: Number(port), allow_wildcard: wildcard },
      recipe: { header, prefix },
    };
    const body =
      mode === "create"
        ? { ...definition, secret: value }
        : mode === "edit"
          ? definition
          : mode === "rotate"
            ? { secret: value }
            : {};
    const route = `/api/v2/http/credentials${credential === undefined ? "" : `/${credential.id}`}${mode === "rotate" ? "/rotate" : ""}`;
    try {
      controller.begin({
        route,
        method:
          mode === "delete" ? "DELETE" : mode === "edit" ? "PATCH" : "POST",
        body: JSON.stringify(body),
        precondition: expectedETag,
        requiresPrecondition: credential !== undefined,
        idempotency: "none",
        successStatuses: [
          mode === "create" ? 201 : mode === "delete" ? 204 : 200,
        ],
        ...(material ? { uncertainProblemCodes: ["keyring_unavailable"] } : {}),
        decode: async (response) =>
          mode === "delete" ? null : decodeResponse(response),
      });
    } catch {
      clear();
      setInputError(true);
      return;
    }
    const pending = controller.submit();
    clear();
    void pending.then((outcome) => {
      if (outcome.kind === "acknowledged") {
        controller.abandon();
        if (mode === "delete") navigate("#/http/credentials", true);
        else if (mode === "create" && outcome.value !== null)
          navigate(`#/http/credentials/${outcome.value.id}`, true);
        else onRefresh();
      } else if (
        outcome.kind === "uncertain" ||
        (outcome.kind === "rejected" && outcome.requiresRefresh)
      ) {
        setBlockedVersion(view.generation);
        onRefresh();
      }
    });
  };
  return (
    <section class="panel domain-panel">
      <h2>{title}</h2>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          review();
        }}
        onInput={() => setDirty(true)}
      >
        {metadata && (
          <>
            <FormField id={`http-name-${mode}`} label="Name">
              {(attributes) => (
                <input
                  {...attributes}
                  required
                  value={name}
                  onInput={(e) => setName(e.currentTarget.value)}
                />
              )}
            </FormField>
            <FormField id={`http-host-${mode}`} label="HTTPS destination host">
              {(attributes) => (
                <input
                  {...attributes}
                  required
                  value={host}
                  onInput={(e) => setHost(e.currentTarget.value)}
                />
              )}
            </FormField>
            <FormField id={`http-port-${mode}`} label="Port">
              {(attributes) => (
                <input
                  {...attributes}
                  required
                  type="number"
                  min="1"
                  max="65535"
                  value={port}
                  onInput={(e) => setPort(e.currentTarget.value)}
                />
              )}
            </FormField>
            <label>
              <input
                type="checkbox"
                checked={wildcard}
                onChange={(e) => setWildcard(e.currentTarget.checked)}
              />
              Allow the explicit *. subdomain boundary
            </label>
            <FormField id={`http-header-${mode}`} label="Header name">
              {(attributes) => (
                <input
                  {...attributes}
                  required
                  value={header}
                  onInput={(e) => setHeader(e.currentTarget.value)}
                />
              )}
            </FormField>
            <FormField
              id={`http-prefix-${mode}`}
              label="Fixed prefix (optional)"
            >
              {(attributes) => (
                <input
                  {...attributes}
                  value={prefix}
                  onInput={(e) => setPrefix(e.currentTarget.value)}
                />
              )}
            </FormField>
          </>
        )}
        {material && (
          <WriteOnlyField
            id={inputID}
            value={secret}
            label="Secret"
            hint="Write-only. Cleared after submission; stored values cannot be revealed."
          />
        )}
        {mode === "delete" &&
          (credential?.referencing_grants.length ?? 0) > 0 && (
            <p>Remove referencing grants before deleting this credential.</p>
          )}
        {inputError && (
          <StateNotice state="error" title="Check credential fields" />
        )}
        {mutation.problem !== undefined && (
          <StateNotice state="error" title={mutation.problem.title} />
        )}
        {mutation.state === "uncertain" && (
          <StateNotice
            state="warning"
            title="Credential change outcome unknown"
          >
            <p>
              Inspect current metadata before making a new decision. No replay
              is available.
            </p>
          </StateNotice>
        )}
        <button
          ref={button}
          type="submit"
          class={
            mode === "delete"
              ? "danger-action form-submit-action"
              : "form-submit-action"
          }
          disabled={blocked}
        >
          Review {mode === "edit" ? "changes" : mode}
        </button>
      </form>
      <ConfirmationDialog
        id={`http-credential-confirm-${mode}`}
        open={confirming}
        title={title}
        consequence={
          metadata
            ? `HTTPS ${host}:${port}; replace ${header} with ${prefix}[secret]. Every referencing grant must remain contained.`
            : mode === "delete"
              ? "Permanently retire this unreferenced credential. Stored material cannot be restored from a backup."
              : "Replace the current secret. Future admissions use the new generation; a failed or uncertain rotation leaves authority unavailable."
        }
        confirmLabel={title}
        destructive={mode !== "create"}
        returnFocus={button as unknown as RefObject<HTMLElement>}
        onCancel={() => {
          setConfirming(false);
          secret.clear();
        }}
        onConfirm={submit}
      />
    </section>
  );
}
