import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import { useUnsavedChanges } from "./navigation";
import type { MutationCoordinator, MutationSnapshot } from "./mutation";
import {
  BinaryToggle,
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
  const [loading, setLoading] = useState(false);
  useEffect(() => {
    let current = true;
    setError(false);
    setLoading(true);
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
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [selected, props.view.generation]);
  if (selected === undefined) return <CredentialCollection {...props} />;
  if (selected === "new") return <CredentialEditor {...props} mode="create" />;
  if (error && detail?.id !== selected)
    return (
      <StateNotice state="error" title="HTTP credential data unavailable">
        <p>Refresh to load the credential.</p>
      </StateNotice>
    );
  if (detail?.id !== selected)
    return <StateNotice state="loading" title="Loading HTTP credential" />;
  return (
    <div class="domain-view">
      {error && (
        <StateNotice
          state="error"
          title="Current HTTP credential data unavailable"
        >
          <p>
            Your safe draft is preserved. Refresh before reviewing the current
            revision.
          </p>
        </StateNotice>
      )}
      <nav class="detail-navigation" aria-label="HTTP credential navigation">
        <a href="#/http/credentials">Back to HTTP credentials</a>
      </nav>
      <header class="detail-context" data-testid="detail-context">
        <div class="detail-context-heading">
          <h1 tabindex={-1}>{detail.name}</h1>
          <StatusLabel state={detail.available ? "current" : "warning"}>
            {detail.available ? "Configured" : "Unavailable"}
          </StatusLabel>
        </div>
        <p class="technical-value">{detail.id}</p>
      </header>
      <section
        class="detail-section"
        aria-labelledby="credential-details-title"
      >
        <div class="panel-heading">
          <h2 id="credential-details-title">Credential details</h2>
        </div>
        <h3>HTTPS scope and recipe</h3>
        <dl class="detail-facts">
          <div>
            <dt>HTTPS boundary</dt>
            <dd>
              {detail.boundary.host}:{detail.boundary.port}
            </dd>
          </div>
          <div>
            <dt>Wildcard hosts</dt>
            <dd>
              {detail.boundary.allow_wildcard
                ? "Allowed within boundary"
                : "Exact host only"}
            </dd>
          </div>
          <div>
            <dt>Header recipe</dt>
            <dd>
              {detail.recipe.header}: {detail.recipe.prefix}
              <span class="secondary-text">[secret]</span>
            </dd>
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
        <h3>Metadata</h3>
        <dl class="detail-facts">
          <div>
            <dt>Revision</dt>
            <dd>{detail.revision}</dd>
          </div>
        </dl>
      </section>
      <CredentialEditor
        {...props}
        key={`${detail.id}-edit`}
        credential={detail}
        mode="edit"
        unavailable={error || loading}
      />
      <CredentialEditor
        {...props}
        key={`${detail.id}-rotate`}
        credential={detail}
        mode="rotate"
        unavailable={error || loading}
      />
      <CredentialEditor
        {...props}
        key={`${detail.id}-delete`}
        credential={detail}
        mode="delete"
        unavailable={error || loading}
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
    (query, cursor, signal) => {
      const params = new URLSearchParams({ limit: "50" });
      for (const key of ["name", "boundary", "recipe", "status"]) {
        const value = query[`filter_${key}`];
        if (value !== undefined) params.set(key, value);
      }
      if (cursor !== null) params.set("cursor", cursor);
      return readCollectionPage(
        props.session,
        `/api/v2/http/credentials?${params}`,
        decodeHTTPCredential,
        signal,
      );
    },
    navigate,
    { key: "created", direction: "descending" },
  );
  return (
    <div class="domain-view">
      <div class="collection-toolbar">
        <a class="button-link create-action" href="#/http/credentials/new">
          Create credential
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
          filters={[
            {
              key: "name",
              label: "Name or ID",
              type: "text",
              value: (c) => c.name,
              literalValues: (c) => [c.id],
            },
            {
              key: "boundary",
              label: "HTTPS boundary",
              type: "text",
              value: (c) => `${c.boundary.host}:${c.boundary.port}`,
            },
            {
              key: "recipe",
              label: "Header recipe",
              type: "text",
              value: (c) => `${c.recipe.header} ${c.recipe.prefix}`,
            },
            {
              key: "status",
              label: "Status",
              type: "select",
              value: (c) => (c.available ? "configured" : "unavailable"),
              options: [
                { value: "configured", label: "Configured" },
                { value: "unavailable", label: "Unavailable" },
              ],
            },
          ]}
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
  unavailable = false,
}: Props & {
  credential?: Credential;
  unavailable?: boolean;
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
  const [inputError, setInputError] = useState<string>();
  const [blockedVersion, setBlockedVersion] = useState<number>();
  const [reviewRequired, setReviewRequired] = useState(false);
  const [expectedETag, setExpectedETag] = useState(() =>
    credential === undefined ? null : etag(credential),
  );
  useEffect(() => {
    if (
      credential === undefined ||
      dirty ||
      reviewRequired ||
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
  }, [credential, dirty, reviewRequired, confirming, mutation.state]);
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
  const recipeReadOnly =
    mode === "edit" && (credential?.referencing_grants.length ?? 0) > 0;
  const blocked =
    unavailable ||
    reviewRequired ||
    mutation.state === "submitting" ||
    mutation.state === "uncertain" ||
    mutation.availability === "storage_latched" ||
    (blockedVersion !== undefined && view.generation <= blockedVersion) ||
    (mode === "delete" && (credential?.referencing_grants.length ?? 0) > 0);
  const title =
    mode === "create"
      ? "Create HTTP credential"
      : mode === "edit"
        ? "Edit credential"
        : mode === "rotate"
          ? "Rotate secret"
          : "Delete credential";
  const clear = () => {
    secret.clear();
    setDirty(false);
  };
  const validationError = (): string | undefined => {
    if (metadata) {
      if (name.trim() === "" || new TextEncoder().encode(name).byteLength > 256)
        return "Name must be nonempty and at most 256 bytes.";
      if (
        host === "" ||
        new TextEncoder().encode(host.startsWith("*.") ? host.slice(2) : host)
          .byteLength > 253
      )
        return "HTTPS destination host must be nonempty and at most 253 bytes, excluding *.";
      if (!/^[1-9][0-9]*$/.test(port) || Number(port) > 65535)
        return "Port must be a whole number from 1 to 65535.";
      if (!/^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,128}$/.test(header))
        return "Header name must contain 1–128 HTTP token characters.";
      if (!/^[\x20-\x7e]{0,128}$/.test(prefix))
        return "Fixed prefix must contain at most 128 printable ASCII characters.";
    }
    if (material) {
      const element = document.getElementById(inputID);
      if (!(element instanceof HTMLInputElement) || element.value === "")
        return "Secret must be nonempty.";
      if (element.value.length + prefix.length > 4096)
        return "Secret and fixed prefix must total at most 4096 characters.";
      if (!/^[\x20-\x7e]+$/.test(element.value))
        return "Secret must contain only printable ASCII characters.";
    }
    return undefined;
  };
  const reportInvalid = (message: string) => {
    secret.clear();
    setInputError(
      material ? `${message} Enter the secret again; it was cleared.` : message,
    );
  };
  const review = () => {
    const error = validationError();
    setInputError(undefined);
    if (error === undefined) setConfirming(true);
    else reportInvalid(error);
  };
  const submit = () => {
    setConfirming(false);
    // The resource can become unavailable after the confirmation was opened.
    if (blocked) {
      reportInvalid(
        "Current credential state cannot authorize this change. Refresh and review again.",
      );
      return;
    }
    const error = validationError();
    if (error !== undefined) {
      reportInvalid(error);
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
      setInputError(
        material
          ? "Credential change could not be prepared. Enter the secret again; it was cleared."
          : "Credential change could not be prepared. Refresh before reviewing again.",
      );
      return;
    }
    const pending = controller.submit();
    // Write-only material is always cleared; safe edit drafts survive rejection.
    if (mode === "edit") secret.clear();
    else clear();
    void pending.then((outcome) => {
      if (outcome.kind === "acknowledged") {
        setDirty(false);
        setReviewRequired(false);
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
        if (mode === "edit" && outcome.kind === "rejected")
          setReviewRequired(true);
        onRefresh();
      }
    });
  };
  return (
    <section
      class="panel domain-panel"
      aria-labelledby={`http-credential-title-${mode}`}
    >
      <div class="panel-heading">
        <h2 id={`http-credential-title-${mode}`}>
          {mode === "create" ? "Credential configuration" : title}
        </h2>
      </div>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          review();
        }}
        onInput={() => setDirty(true)}
      >
        <fieldset
          class="credential-fields"
          disabled={
            mutation.state === "submitting" ||
            mutation.state === "uncertain" ||
            mutation.availability === "storage_latched"
          }
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
              <FormField
                id={`http-host-${mode}`}
                label="HTTPS destination host"
              >
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
              <FormField
                id={`http-wildcard-${mode}`}
                label="Allow the explicit *. subdomain boundary"
              >
                {(attributes) => (
                  <BinaryToggle
                    attributes={attributes}
                    checked={wildcard}
                    onChange={setWildcard}
                  />
                )}
              </FormField>
              {recipeReadOnly && (
                <p>Header recipe is fixed while referenced.</p>
              )}
              <FormField id={`http-header-${mode}`} label="Header name">
                {(attributes) => (
                  <input
                    {...attributes}
                    required
                    readOnly={recipeReadOnly}
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
                    readOnly={recipeReadOnly}
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
          {inputError !== undefined && (
            <StateNotice state="error" title="Check credential fields">
              <p>{inputError}</p>
            </StateNotice>
          )}
          {reviewRequired && (
            <StateNotice
              state="warning"
              title="Review current credential revision"
            >
              <p>
                Compare the current facts above with your retained draft before
                applying it.
              </p>
              <button
                type="button"
                disabled={
                  unavailable ||
                  credential === undefined ||
                  (blockedVersion !== undefined &&
                    view.generation <= blockedVersion)
                }
                onClick={() => {
                  if (credential === undefined) return;
                  setExpectedETag(etag(credential));
                  setReviewRequired(false);
                  controller.abandon();
                }}
              >
                Use reviewed revision; keep draft
              </button>
            </StateNotice>
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
                : `${mode === "create" ? "create-action " : ""}form-submit-action`
            }
            disabled={blocked}
          >
            {mode === "create"
              ? "Review and create"
              : `Review ${mode === "edit" ? "changes" : mode === "rotate" ? "rotation" : "deletion"}`}
          </button>
        </fieldset>
      </form>
      <ConfirmationDialog
        id={`http-credential-confirm-${mode}`}
        open={confirming}
        title={title}
        consequence={
          metadata ? (
            <div class="review-stack">
              <dl class="detail-facts">
                <div>
                  <dt>Name</dt>
                  <dd>{name}</dd>
                </div>
                <div>
                  <dt>HTTPS boundary</dt>
                  <dd>
                    {host}:{port}
                  </dd>
                </div>
                <div>
                  <dt>Wildcard hosts</dt>
                  <dd>
                    {wildcard ? "Allowed within boundary" : "Exact host only"}
                  </dd>
                </div>
                <div>
                  <dt>Header</dt>
                  <dd>{header}</dd>
                </div>
                <div>
                  <dt>Prefix</dt>
                  <dd>{prefix === "" ? "None" : <code>{prefix}</code>}</dd>
                </div>
                <div>
                  <dt>Secret material</dt>
                  <dd>
                    {mode === "create"
                      ? "Write-only; not displayed"
                      : "Unchanged"}
                  </dd>
                </div>
              </dl>
              <p>
                Replace the named header using this recipe. Every referencing
                grant must remain contained.
              </p>
            </div>
          ) : mode === "delete" ? (
            "Permanently retire this unreferenced credential. Stored material cannot be restored from a backup."
          ) : (
            "Once replacement starts, failure may leave this credential unavailable; replacement does not fall back to the old secret."
          )
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
