import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type { ResolvedLocation } from "./location";
import type { MutationCoordinator, MutationSnapshot } from "./mutation";
import { useUnsavedChanges } from "./navigation";
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
import { UserTime } from "./time";
import {
  readCollectionPage,
  useCollectionPage,
  type ViewSnapshot,
} from "./view";
import {
  decodeGitResource,
  decodeGitResponse,
  decodeGitTraffic,
  decodeGitTrafficPage,
  gitETag,
  gitFacts,
  gitID,
  gitLabels,
  type GitKind,
  type GitResource,
  type GitRule,
  type GitTraffic,
} from "./git-contract";

interface Props {
  session: SessionClient;
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
  onRefresh: () => void;
}
const singular = (kind: GitKind) =>
  kind === "repositories"
    ? "repository"
    : kind === "credentials"
      ? "credential"
      : "grant";
const label = (r: GitResource) =>
  r.name ?? r.description ?? "Unnamed Git grant";
function useGitDetail<T>(
  props: Props,
  path: string | undefined,
  decode: (response: Response) => Promise<T>,
) {
  const [value, setValue] = useState<T>();
  const [error, setError] = useState(false);
  useEffect(() => {
    let current = true;
    setError(false);
    if (path === undefined) return;
    void props.session
      .runProtected(async (context) => {
        const response = await fetch(path, {
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
          headers: {
            Accept: "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        });
        if (await context.sessionLost(response)) return;
        if (!response.ok) throw new Error("Read unavailable");
        const result = await decode(response);
        if (current) setValue(result);
      })
      .catch(() => {
        if (current) setError(true);
      });
    return () => {
      current = false;
    };
  }, [path, props.view.generation]);
  return { value, error };
}
export function GitConfiguration(props: Props & { kind: GitKind }) {
  const { kind } = props,
    selected = props.resolved.location.segments[1];
  const { value, error } = useGitDetail(
    props,
    selected && selected !== "new"
      ? `/api/v2/git/${kind}/${selected}`
      : undefined,
    async (response) => {
      const r = await decodeGitResponse(kind, response);
      if (r.id !== selected) throw new Error("Identity unavailable");
      return r;
    },
  );
  if (selected === undefined) return <GitCollection {...props} />;
  if (selected === "new") return <GitEditor {...props} mode="create" />;
  if (!value)
    return (
      <StateNotice
        state={error ? "error" : "loading"}
        title={error ? "Git resource unavailable" : "Loading Git resource"}
      />
    );
  return (
    <div class="domain-view">
      <nav class="detail-navigation" aria-label="Git resource navigation">
        <a href={`#/git/${kind}`}>Back to Git {kind}</a>
      </nav>
      <header class="detail-context">
        <h1 tabindex={-1}>{label(value)}</h1>
      </header>
      {error && (
        <StateNotice state="error" title="Git resource refresh unavailable">
          Saved facts may be stale. Refresh before making changes.
        </StateNotice>
      )}
      <section class="panel domain-panel">
        <div class="panel-heading">
          <h2>{`Git ${singular(kind)} details`}</h2>
          {kind === "credentials" ? (
            <StatusLabel state={value.available ? "current" : "warning"}>
              {value.available ? "Configured" : "Unavailable"}
            </StatusLabel>
          ) : kind === "grants" ? (
            <StatusLabel
              state={value.state === "active" ? "current" : "neutral"}
            >
              {value.state === "active" ? "Active" : "Expired"}
            </StatusLabel>
          ) : null}
        </div>
        <dl class="fact-grid">
          <div>
            <dt>ID</dt>
            <dd class="technical-value">{value.id}</dd>
          </div>
          <div>
            <dt>Revision</dt>
            <dd>{value.revision}</dd>
          </div>
          {kind === "repositories" && (
            <>
              <div>
                <dt>Canonical destination (immutable)</dt>
                <dd>{value.url}</dd>
              </div>
              <div>
                <dt>Explicit aliases</dt>
                <dd>
                  {value.aliases?.length
                    ? value.aliases.map((a) => <p key={a}>{a}</p>)
                    : "None"}
                </dd>
              </div>
              <div>
                <dt>Git credential</dt>
                <dd>
                  {value.credential_id ? (
                    <a href={`#/git/credentials/${value.credential_id}`}>
                      {value.credential_id}
                    </a>
                  ) : (
                    "None"
                  )}
                </dd>
              </div>
            </>
          )}
          {kind === "grants" && (
            <>
              <div>
                <dt>Agent</dt>
                <dd>
                  <a href={`#/agents/${value.principal_id}`}>
                    {value.principal_id}
                  </a>
                </dd>
              </div>
              <div>
                <dt>Repository</dt>
                <dd>
                  <a href={`#/git/repositories/${value.repository_id}`}>
                    {value.repository_id}
                  </a>
                </dd>
              </div>
              <div>
                <dt>Read</dt>
                <dd>{value.policy?.read ? "Allowed" : "Blocked"}</dd>
              </div>
              <div>
                <dt>Expires</dt>
                <dd>
                  <UserTime value={value.expires_at} fallback="No expiry" />
                </dd>
              </div>
            </>
          )}
          {kind === "credentials" && (
            <>
              <div>
                <dt>HTTPS origin</dt>
                <dd>{value.origin}</dd>
              </div>
              <div>
                <dt>Header recipe</dt>
                <dd>
                  {value.recipe?.header}: {value.recipe?.prefix}[secret]
                </dd>
              </div>
              <div>
                <dt>Referencing repositories</dt>
                <dd>
                  {value.referencing_repositories?.length
                    ? value.referencing_repositories.map((r) => (
                        <p key={r.id}>
                          <a href={`#/git/repositories/${r.id}`}>{r.id}</a>
                        </p>
                      ))
                    : "None"}
                </dd>
              </div>
            </>
          )}
        </dl>
        {kind === "grants" && (
          <ul>
            {value.policy?.refs.map((r) => (
              <li key={`${r.ref.kind}:${r.ref.value}`}>
                <span class="technical-value">{r.ref.value}</span> ({r.ref.kind}
                ): {r.actions.join(", ")}
              </li>
            ))}
          </ul>
        )}
      </section>
      <GitEditor {...props} resource={value} mode="edit" unavailable={error} />
      {kind === "credentials" && (
        <GitEditor
          {...props}
          resource={value}
          mode="rotate"
          unavailable={error}
        />
      )}
      <GitEditor
        {...props}
        resource={value}
        mode="delete"
        unavailable={error}
      />
    </div>
  );
}
function GitCollection(props: Props & { kind: GitKind }) {
  const navigate = useUnsavedChanges(false),
    { kind } = props;
  const { items, controls } = useCollectionPage<GitResource>(
    props.session,
    props.resolved,
    props.view,
    (_q, cursor, signal) =>
      readCollectionPage(
        props.session,
        `/api/v2/git/${kind}?limit=50${cursor === null ? "" : `&cursor=${encodeURIComponent(cursor)}`}`,
        (v) => decodeGitResource(kind, v),
        signal,
      ),
    navigate,
    { key: "id", direction: "ascending" },
  );
  return (
    <div class="domain-view">
      <div class="collection-toolbar">
        <a class="button-link create-action" href={`#/git/${kind}/new`}>
          Create {singular(kind)}
        </a>
      </div>
      <section class="panel domain-panel">
        <CollectionTable
          caption={`Git ${kind}`}
          rowHeaderKey="name"
          remote={controls}
          itemNames={{ singular: singular(kind), plural: kind }}
          emptyTitle={`No Git ${kind}`}
          items={items}
          rowKey={(r) => r.id}
          filters={[]}
          initialSort={{ key: "id", direction: "ascending" }}
          columns={[
            {
              key: "name",
              label:
                kind === "repositories"
                  ? "Repository"
                  : kind === "credentials"
                    ? "Credential"
                    : "Grant",
              role: "identity",
              render: (r) => (
                <TableIdentity
                  primary={<a href={`#/git/${kind}/${r.id}`}>{label(r)}</a>}
                  secondary={r.id}
                />
              ),
            },
            {
              key: "scope",
              label: kind === "grants" ? "Repository" : "Destination",
              role: "relation",
              render: (r) =>
                kind === "grants" ? (
                  <a href={`#/git/repositories/${r.repository_id}`}>
                    {r.repository_id}
                  </a>
                ) : (
                  (r.url ?? r.origin)
                ),
            },
            ...(kind === "grants"
              ? [
                  {
                    key: "agent",
                    label: "Agent",
                    role: "relation" as const,
                    render: (r: GitResource) => (
                      <a href={`#/agents/${r.principal_id}`}>
                        {r.principal_id}
                      </a>
                    ),
                  },
                ]
              : []),
            {
              key: "status",
              label: kind === "repositories" ? "Git credential" : "Status",
              role: "status",
              render: (r) =>
                kind === "repositories" ? (
                  r.credential_id ? (
                    <a href={`#/git/credentials/${r.credential_id}`}>
                      {r.credential_id}
                    </a>
                  ) : (
                    "None"
                  )
                ) : (
                  <StatusLabel
                    state={
                      kind === "credentials"
                        ? r.available
                          ? "current"
                          : "warning"
                        : r.state === "active"
                          ? "current"
                          : "neutral"
                    }
                  >
                    {kind === "credentials"
                      ? r.available
                        ? "Configured"
                        : "Unavailable"
                      : r.state === "active"
                        ? "Active"
                        : "Expired"}
                  </StatusLabel>
                ),
            },
          ]}
        />
      </section>
    </div>
  );
}
function GitEditor({
  kind,
  resource,
  mode,
  unavailable = false,
  ...props
}: Props & {
  kind: GitKind;
  resource?: GitResource;
  mode: "create" | "edit" | "rotate" | "delete";
  unavailable?: boolean;
}) {
  const [draft, setDraft] = useState(() => ({
    name: resource?.name ?? "",
    url: resource?.url ?? "",
    aliases: resource?.aliases ?? [],
    credential: resource?.credential_id ?? "",
    principal: resource?.principal_id ?? "",
    repository: resource?.repository_id ?? "",
    description: resource?.description ?? "",
    read: resource?.policy?.read ?? true,
    refs: resource?.policy?.refs ?? ([] as GitRule[]),
    expires: resource?.expires_at ?? "",
    origin: resource?.origin ?? "",
    header: resource?.recipe?.header ?? "Authorization",
    prefix: resource?.recipe?.prefix ?? "Bearer ",
  }));
  const [expected, setExpected] = useState(
    resource ? gitETag(kind, resource) : null,
  );
  const [dirty, setDirty] = useState(false),
    [confirming, setConfirming] = useState(false),
    [inputError, setInputError] = useState<string>();
  const [secret] = useState(() => props.sinks.createWriteOnly()),
    [controller] = useState(() => props.mutations.create<GitResource | null>());
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const button = useRef<HTMLButtonElement>(null),
    navigate = useUnsavedChanges(dirty),
    secretID = `git-secret-${mode}`;
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(
    () => () => {
      controller.close();
      secret.close();
    },
    [controller, secret],
  );
  const changedRevision =
    resource !== undefined && expected !== gitETag(kind, resource);
  const blocked =
    unavailable ||
    changedRevision ||
    mutation.state === "submitting" ||
    mutation.state === "uncertain" ||
    mutation.availability === "storage_latched";
  const metadata = mode === "create" || mode === "edit",
    material =
      kind === "credentials" && (mode === "create" || mode === "rotate");
  const title = `${mode === "create" ? "Create" : mode === "edit" ? "Edit" : mode === "rotate" ? "Rotate" : "Delete"} Git ${singular(kind)}`;
  const change = <K extends keyof typeof draft>(
    key: K,
    value: (typeof draft)[K],
  ) => {
    setDraft((d) => ({ ...d, [key]: value }));
    setDirty(true);
  };
  const field = (
    key:
      | "name"
      | "url"
      | "credential"
      | "principal"
      | "repository"
      | "description"
      | "origin"
      | "header"
      | "prefix",
    labelText: string,
    required = true,
    readonly = false,
  ) => (
    <FormField id={`git-${key}-${mode}`} label={labelText}>
      {(attributes) => (
        <input
          {...attributes}
          required={required}
          readOnly={readonly}
          value={draft[key]}
          onInput={(e) => change(key, e.currentTarget.value)}
        />
      )}
    </FormField>
  );
  const definition = () =>
    kind === "repositories"
      ? {
          name: draft.name,
          url: draft.url,
          aliases: draft.aliases,
          credential_id: draft.credential || null,
        }
      : kind === "credentials"
        ? {
            name: draft.name,
            origin: draft.origin,
            recipe: { header: draft.header, prefix: draft.prefix },
          }
        : {
            principal_id: draft.principal,
            repository_id: draft.repository,
            description: draft.description || null,
            policy: { version: 1, read: draft.read, refs: draft.refs },
            expires_at: draft.expires || null,
          };
  const validate = () => {
    if (metadata) {
      if (
        kind !== "grants" &&
        (draft.name.trim() === "" ||
          new TextEncoder().encode(draft.name).length > 256)
      )
        return "Enter a name of at most 256 bytes.";
      if (
        kind === "repositories" &&
        (!draft.url.startsWith("https://") ||
          draft.aliases.some((a) => !a.startsWith("https://")) ||
          (draft.credential !== "" && !gitID.test(draft.credential)))
      )
        return "Use HTTPS destinations and a valid optional Git credential ID.";
      if (
        kind === "grants" &&
        (!gitID.test(draft.principal) || !gitID.test(draft.repository))
      )
        return "Enter valid agent and repository IDs.";
      if (kind === "grants" && draft.refs.length > 0 && !draft.read)
        return "Push permissions require read access.";
      if (
        kind === "grants" &&
        draft.refs.some(
          (r) => !r.ref.value.startsWith("refs/") || r.actions.length === 0,
        )
      )
        return "Each ref rule needs a refs/ selector and at least one operation.";
    }
    if (material) {
      const el = document.getElementById(secretID);
      if (
        !(el instanceof HTMLInputElement) ||
        !el.value ||
        !/^[\x20-\x7e]+$/.test(el.value) ||
        el.value.length + draft.prefix.length > 4096
      )
        return "Enter a nonempty printable secret; secret and prefix must fit 4096 characters.";
    }
    return undefined;
  };
  const review = () => {
    const error = validate();
    setInputError(error);
    if (error) {
      secret.clear();
    } else setConfirming(true);
  };
  const submit = () => {
    setConfirming(false);
    const error = validate();
    if (error) {
      setInputError(error);
      secret.clear();
      return;
    }
    const element = document.getElementById(secretID);
    const value = element instanceof HTMLInputElement ? element.value : "";
    const body =
      mode === "rotate"
        ? { secret: value }
        : mode === "delete"
          ? {}
          : material
            ? { ...definition(), secret: value }
            : definition();
    try {
      controller.begin({
        route: `/api/v2/git/${kind}${resource ? `/${resource.id}` : ""}${mode === "rotate" ? "/rotate" : ""}`,
        method:
          mode === "delete" ? "DELETE" : mode === "edit" ? "PATCH" : "POST",
        // Policy deletes are bodyless; credential deletion requires {}.
        body:
          mode === "delete" && kind !== "credentials"
            ? null
            : JSON.stringify(body),
        precondition: expected,
        requiresPrecondition: resource !== undefined,
        idempotency: "none",
        successStatuses: [
          mode === "create" ? 201 : mode === "delete" ? 204 : 200,
        ],
        ...(material ? { uncertainProblemCodes: ["keyring_unavailable"] } : {}),
        decode: async (response) =>
          mode === "delete" ? null : decodeGitResponse(kind, response),
      });
      const pending = controller.submit();
      secret.clear();
      void pending.then((outcome) => {
        if (outcome.kind === "acknowledged") {
          setDirty(false);
          controller.abandon();
          if (mode === "delete") navigate(`#/git/${kind}`, true);
          else if (mode === "create" && outcome.value)
            navigate(`#/git/${kind}/${outcome.value.id}`, true);
          else {
            if (outcome.value) setExpected(gitETag(kind, outcome.value));
            props.onRefresh();
          }
        } else if (
          outcome.kind === "uncertain" ||
          (outcome.kind === "rejected" && outcome.requiresRefresh)
        )
          props.onRefresh();
      });
    } catch {
      secret.clear();
      setInputError(
        "Change could not be prepared. Refresh and review current metadata before making a new decision.",
      );
    }
  };
  return (
    <section class="panel domain-panel">
      <div class="panel-heading">
        <h2>
          {mode === "create"
            ? `${kind === "repositories" ? "Repository" : kind === "grants" ? "Grant" : "Credential"} configuration`
            : title}
        </h2>
      </div>
      <form
        onSubmit={(e) => {
          e.preventDefault();
          review();
        }}
        onInput={() => setDirty(true)}
      >
        <fieldset
          class="git-mutation-fields"
          disabled={mutation.state === "submitting"}
        >
          {metadata && kind !== "grants" && field("name", "Name")}
          {metadata && kind === "repositories" && (
            <>
              {field(
                "url",
                "Canonical HTTPS destination",
                true,
                resource !== undefined,
              )}
              <p>
                Aliases recognize this same immutable repository; they cannot
                retarget its authority.
              </p>
              {draft.aliases.map((alias, i) => (
                <div class="form-row" key={i}>
                  <FormField id={`git-alias-${i}`} label={`Alias ${i + 1}`}>
                    {(attributes) => (
                      <input
                        {...attributes}
                        required
                        value={alias}
                        onInput={(e) =>
                          change(
                            "aliases",
                            draft.aliases.map((a, j) =>
                              i === j ? e.currentTarget.value : a,
                            ),
                          )
                        }
                      />
                    )}
                  </FormField>
                  <button
                    type="button"
                    onClick={() =>
                      change(
                        "aliases",
                        draft.aliases.filter((_, j) => i !== j),
                      )
                    }
                  >
                    Remove alias {i + 1}
                  </button>
                </div>
              ))}
              <button
                type="button"
                disabled={draft.aliases.length >= 4}
                onClick={() => change("aliases", [...draft.aliases, ""])}
              >
                Add alias
              </button>
              {field("credential", "Git credential ID (optional)", false)}
            </>
          )}
          {metadata && kind === "credentials" && (
            <>
              {field("origin", "HTTPS origin")}
              {field("header", "Header name")}
              {field("prefix", "Fixed prefix (optional)", false)}
            </>
          )}
          {metadata && kind === "grants" && (
            <>
              {field("principal", "Agent ID", true, resource !== undefined)}
              {field(
                "repository",
                "Repository ID",
                true,
                resource !== undefined,
              )}
              {field("description", "Description (optional)", false)}
              <FormField id={`git-read-${mode}`} label="Read repository">
                {(attributes) => (
                  <BinaryToggle
                    attributes={attributes}
                    checked={draft.read}
                    onChange={(v) => {
                      change("read", v);
                      if (!v) change("refs", []);
                    }}
                  />
                )}
              </FormField>
              <h3>Push ref permissions</h3>
              <p>
                Adding a push rule includes read access. No write-only grants.
              </p>
              {draft.refs.map((rule, i) => (
                <fieldset key={i}>
                  <legend>Ref rule {i + 1}</legend>
                  <FormField id={`git-rule-kind-${i}`} label="Match">
                    {(attributes) => (
                      <select
                        {...attributes}
                        value={rule.ref.kind}
                        onChange={(e) =>
                          change(
                            "refs",
                            draft.refs.map((r, j) =>
                              i === j
                                ? {
                                    ...r,
                                    ref: {
                                      ...r.ref,
                                      kind: e.currentTarget.value as
                                        | "exact"
                                        | "prefix",
                                    },
                                  }
                                : r,
                            ),
                          )
                        }
                      >
                        <option value="exact">Exact ref</option>
                        <option value="prefix">
                          Namespace prefix ending in /
                        </option>
                      </select>
                    )}
                  </FormField>
                  <FormField id={`git-rule-ref-${i}`} label="Ref selector">
                    {(attributes) => (
                      <input
                        {...attributes}
                        required
                        value={rule.ref.value}
                        onInput={(e) =>
                          change(
                            "refs",
                            draft.refs.map((r, j) =>
                              i === j
                                ? {
                                    ...r,
                                    ref: {
                                      ...r.ref,
                                      value: e.currentTarget.value,
                                    },
                                  }
                                : r,
                            ),
                          )
                        }
                      />
                    )}
                  </FormField>
                  {["create", "update", "delete"].map((action) => (
                    <FormField
                      id={`git-rule-${i}-${action}`}
                      label={action[0]!.toUpperCase() + action.slice(1)}
                    >
                      {(attributes) => (
                        <BinaryToggle
                          attributes={attributes}
                          checked={rule.actions.includes(action)}
                          onChange={(v) =>
                            change(
                              "refs",
                              draft.refs.map((r, j) =>
                                i === j
                                  ? {
                                      ...r,
                                      actions: v
                                        ? [...r.actions, action]
                                        : r.actions.filter((a) => a !== action),
                                    }
                                  : r,
                              ),
                            )
                          }
                        />
                      )}
                    </FormField>
                  ))}
                  <button
                    type="button"
                    onClick={() =>
                      change(
                        "refs",
                        draft.refs.filter((_, j) => i !== j),
                      )
                    }
                  >
                    Remove ref rule {i + 1}
                  </button>
                </fieldset>
              ))}
              <button
                type="button"
                disabled={draft.refs.length >= 128}
                onClick={() => {
                  change("read", true);
                  change("refs", [
                    ...draft.refs,
                    { ref: { kind: "exact", value: "" }, actions: ["update"] },
                  ]);
                }}
              >
                Add push rule
              </button>
              <FormField id={`git-expires-${mode}`} label="Expiry (optional)">
                {(attributes) => (
                  <input
                    {...attributes}
                    type="datetime-local"
                    value={draft.expires ? localTime(draft.expires) : ""}
                    onInput={(e) =>
                      change(
                        "expires",
                        e.currentTarget.value
                          ? new Date(e.currentTarget.value).toISOString()
                          : "",
                      )
                    }
                  />
                )}
              </FormField>
            </>
          )}
          {material && (
            <WriteOnlyField
              id={secretID}
              value={secret}
              label="Secret"
              hint="Write-only. Cleared on submission or cancellation."
            />
          )}
          {inputError && (
            <StateNotice state="error" title="Check Git configuration">
              <p>
                {inputError}
                {material ? " Enter the secret again; it was cleared." : ""}
              </p>
            </StateNotice>
          )}
          {changedRevision && (
            <StateNotice state="warning" title="Revision changed">
              <p>Review the current facts above against your retained draft.</p>
              <button
                type="button"
                disabled={unavailable || mutation.state === "uncertain"}
                onClick={() => {
                  setExpected(gitETag(kind, resource!));
                  controller.abandon();
                }}
              >
                Use reviewed revision
              </button>
            </StateNotice>
          )}
          {mutation.problem && (
            <StateNotice state="error" title={mutation.problem.title} />
          )}
          {mutation.state === "uncertain" && (
            <StateNotice state="warning" title="Change outcome unknown">
              Inspect current metadata before deciding on another operation. No
              automatic retry.
            </StateNotice>
          )}
          <button
            ref={button}
            type="submit"
            class={`${mode === "delete" ? "danger-action" : mode === "create" ? "create-action" : ""} form-submit-action`}
            disabled={blocked}
          >
            {mode === "create"
              ? "Review and create"
              : mode === "edit"
                ? "Review changes"
                : mode === "rotate"
                  ? "Review rotation"
                  : "Review deletion"}
          </button>
        </fieldset>
      </form>
      <ConfirmationDialog
        id={`git-confirm-${kind}-${mode}`}
        open={confirming}
        title={title}
        consequence={
          mode === "delete" ? (
            "Retire this resource. Referenced resources cannot be deleted; historical traffic is unchanged."
          ) : mode === "rotate" ? (
            "Replace protected material. A failed cutover can leave the credential unavailable; there is no fallback to the old secret."
          ) : (
            <div class="confirmation-details">
              <dl class="fact-grid">
                {kind !== "grants" && (
                  <div>
                    <dt>Name</dt>
                    <dd>{draft.name}</dd>
                  </div>
                )}
                {kind === "repositories" && (
                  <>
                    <div>
                      <dt>Immutable destination</dt>
                      <dd>{draft.url}</dd>
                    </div>
                    <div>
                      <dt>Aliases</dt>
                      <dd>
                        {draft.aliases.length
                          ? draft.aliases.join(", ")
                          : "None"}
                      </dd>
                    </div>
                    <div>
                      <dt>Git credential</dt>
                      <dd>{draft.credential || "None"}</dd>
                    </div>
                  </>
                )}
                {kind === "credentials" && (
                  <>
                    <div>
                      <dt>HTTPS origin</dt>
                      <dd>{draft.origin}</dd>
                    </div>
                    <div>
                      <dt>Header recipe</dt>
                      <dd>
                        {draft.header}: {draft.prefix}[secret]
                      </dd>
                    </div>
                  </>
                )}
                {kind === "grants" && (
                  <>
                    <div>
                      <dt>Agent</dt>
                      <dd>{draft.principal}</dd>
                    </div>
                    <div>
                      <dt>Repository</dt>
                      <dd>{draft.repository}</dd>
                    </div>
                    <div>
                      <dt>Read repository</dt>
                      <dd>{draft.read ? "Allowed" : "Blocked"}</dd>
                    </div>
                    <div>
                      <dt>Expiry</dt>
                      <dd>
                        <UserTime
                          value={draft.expires || null}
                          fallback="No expiry"
                        />
                      </dd>
                    </div>
                  </>
                )}
              </dl>
              {kind === "grants" && (
                <ul>
                  {draft.refs.map((rule, i) => (
                    <li key={i}>
                      {rule.ref.kind}:{" "}
                      <span class="technical-value">{rule.ref.value}</span> —{" "}
                      {rule.actions.join(", ")}
                    </li>
                  ))}
                </ul>
              )}
            </div>
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
function localTime(value: string): string {
  const d = new Date(value);
  if (!Number.isFinite(d.getTime())) return "";
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

export function GitTrafficView(props: Props) {
  const selected = props.resolved.location.segments[1];
  const { value, error } = useGitDetail(
    props,
    selected ? `/api/v2/git/traffic/${selected}` : undefined,
    async (response) => {
      if (response.headers.get("Content-Type") !== "application/json")
        throw new Error("Unavailable");
      const text = await response.text();
      if (text.length > 16384) throw new Error("Unavailable");
      const item = decodeGitTraffic(JSON.parse(text));
      if (item.admission.id !== selected) throw new Error("Unavailable");
      return item;
    },
  );
  if (!selected) return <GitTrafficCollection {...props} />;
  if (!value)
    return (
      <StateNotice
        state={error ? "error" : "loading"}
        title={error ? "Git traffic unavailable" : "Loading Git traffic"}
      />
    );
  const a = value.admission,
    c = value.completion,
    f = gitFacts(value);
  return (
    <div class="domain-view">
      <nav class="detail-navigation" aria-label="Git traffic navigation">
        <a href="#/git/traffic">Back to Git traffic</a>
      </nav>
      <header class="detail-context">
        <h1 tabindex={-1}>Git exchange: {a.id}</h1>
      </header>
      {error && (
        <StateNotice state="warning" title="Git traffic refresh unavailable" />
      )}
      <section class="panel domain-panel">
        <h2>Recorded exchange</h2>
        <dl class="fact-grid">
          <div>
            <dt>Admitted</dt>
            <dd>
              <UserTime value={a.admitted_at} />
            </dd>
          </div>
          <div>
            <dt>Repository at admission</dt>
            <dd>
              {a.policy?.repository_name ?? (a.repository.id || "Unavailable")}
            </dd>
          </div>
          {a.policy && (
            <div>
              <dt>Canonical destination at admission</dt>
              <dd>{a.policy.repository_url}</dd>
            </div>
          )}
          <div>
            <dt>Exchange</dt>
            <dd>{gitLabels[a.operation]}</dd>
          </div>
          <div>
            <dt>Admission</dt>
            <dd>{f.admission}</dd>
          </div>
          <div>
            <dt>Transport</dt>
            <dd>{f.transport}</dd>
          </div>
          <div>
            <dt>Upstream report</dt>
            <dd>{f.report}</dd>
          </div>
          <div>
            <dt>Command count</dt>
            <dd>{a.commands}</dd>
          </div>
          {a.policy && (
            <div>
              <dt>Create / update / delete commands</dt>
              <dd>
                {a.policy.creates} / {a.policy.updates} / {a.policy.deletes}
              </dd>
            </div>
          )}
          {c && (
            <>
              <div>
                <dt>HTTP status</dt>
                <dd>{c.status ?? "None"}</dd>
              </div>
              <div>
                <dt>Bytes sent / received</dt>
                <dd>
                  {c.bytes_sent} / {c.bytes_received}
                </dd>
              </div>
            </>
          )}
        </dl>
        {a.operation === "push" ? (
          <p>
            Upstream reports are not independently verified repository effects.
            Reconcile an uncertain push with the remote before deciding on
            another operation.
          </p>
        ) : (
          <p>
            Transport completion does not prove a valid local checkout.
            Discovery and probes are not completed pushes.
          </p>
        )}
        <details>
          <summary>Admission-time policy references</summary>
          <dl class="fact-grid">
            <div>
              <dt>Repository</dt>
              <dd>
                {a.repository.id || "Unavailable"} · revision{" "}
                {a.repository.revision || "Unavailable"}
              </dd>
            </div>
            <div>
              <dt>Agent</dt>
              <dd>
                {a.principal.id} · revision {a.principal.revision}
              </dd>
            </div>
            <div>
              <dt>Authorization revision</dt>
              <dd>{a.authorization_revision}</dd>
            </div>
            <div>
              <dt>Alias revision</dt>
              <dd>{a.alias_revision || "Unavailable"}</dd>
            </div>
            <div>
              <dt>Routing profile revision</dt>
              <dd>{a.profile_revision}</dd>
            </div>
            <div>
              <dt>Agent credential</dt>
              <dd>
                {a.agent_credential.id} · revision {a.agent_credential.revision}
              </dd>
            </div>
            {a.material && (
              <div>
                <dt>Git credential at admission</dt>
                <dd>
                  {a.material.credential.id} · revision{" "}
                  {a.material.credential.revision} · generation{" "}
                  {a.material.generation}
                </dd>
              </div>
            )}
            {a.private_grant && (
              <div>
                <dt>Private destination permission</dt>
                <dd>
                  {a.private_grant.id} · revision {a.private_grant.revision}
                </dd>
              </div>
            )}
          </dl>
          {a.policy && (
            <>
              <p>
                {a.policy.grants.length} of {a.policy.grant_count} applicable
                grant references retained
              </p>
              <ul>
                {a.policy.grants.map((g) => (
                  <li key={g.id}>
                    {g.id} · revision {g.revision}
                  </li>
                ))}
              </ul>
            </>
          )}
          <p>
            Historical references are not current authority. No observed ref
            names, OIDs or upstream messages are retained.
          </p>
        </details>
      </section>
    </div>
  );
}
function GitTrafficCollection(props: Props) {
  const navigate = useUnsavedChanges(false);
  const { items, controls } = useCollectionPage<GitTraffic>(
    props.session,
    props.resolved,
    props.view,
    (_q, cursor, signal) =>
      readCollectionPage(
        props.session,
        `/api/v2/git/traffic?limit=50${cursor === null ? "" : `&cursor=${encodeURIComponent(cursor)}`}`,
        undefined,
        signal,
        decodeGitTrafficPage,
      ),
    navigate,
    { key: "admitted", direction: "descending" },
  );
  return (
    <section class="panel domain-panel">
      <CollectionTable
        caption="Git traffic"
        rowHeaderKey="exchange"
        layout="activity"
        remote={controls}
        itemNames={{ singular: "exchange", plural: "exchanges" }}
        emptyTitle="No Git traffic"
        items={items}
        rowKey={(r) => r.admission.id}
        filters={[]}
        initialSort={{ key: "admitted", direction: "descending" }}
        columns={[
          {
            key: "admitted",
            label: "Admitted",
            role: "time",
            render: (r) => <UserTime value={r.admission.admitted_at} />,
          },
          {
            key: "exchange",
            label: "Exchange",
            role: "identity",
            render: (r) => (
              <TableIdentity
                primary={
                  <a href={`#/git/traffic/${r.admission.id}`}>
                    {gitLabels[r.admission.operation]}
                  </a>
                }
                secondary={r.admission.id}
              />
            ),
          },
          {
            key: "repository",
            label: "Repository at admission",
            role: "relation",
            render: (r) =>
              r.admission.policy?.repository_name ??
              (r.admission.repository.id || "Unavailable"),
          },
          {
            key: "admission",
            label: "Admission",
            role: "status",
            render: (r) => (
              <StatusLabel state={r.admission.allowed ? "current" : "neutral"}>
                {gitFacts(r).admission}
              </StatusLabel>
            ),
          },
          {
            key: "transport",
            label: "Transport",
            role: "status",
            render: (r) => gitFacts(r).transport,
          },
          {
            key: "report",
            label: "Upstream report",
            role: "status",
            render: (r) => gitFacts(r).report,
          },
        ]}
      />
    </section>
  );
}
