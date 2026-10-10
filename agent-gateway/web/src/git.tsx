import type { RefObject } from "preact";
import { oppositeGitAlias, validGitAliases } from "./git-alias";
import { gitOriginCovered } from "./git-routing-contract";
import type { GitTrafficController } from "./git-traffic-history";
import { gitTrafficOptions, validGitTrafficQuery } from "./git-traffic-query";
import {
  useGitChoices,
  choiceLabel,
  compatibleGitCredential,
  type GitChoices,
} from "./git-choices";
import { useEffect, useRef, useState } from "preact/hooks";
import { serializeLocation, type ResolvedLocation } from "./location";
import {
  GitCoverage,
  useGitCoverage,
  type GitCoverageSource,
} from "./git-coverage";
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
  sentenceCase,
  useDebouncedInput,
} from "./primitives";
import type { SessionClient } from "./session";
import type { SensitiveSinkCoordinator } from "./sinks";
import { WriteOnlyField } from "./sinks-ui";
import { UserTime, HistoryWindow } from "./time";
import {
  readCollectionPage,
  useCollectionPage,
  type ViewSnapshot,
} from "./view";
import {
  decodeGitResource,
  decodeGitResponse,
  decodeGitTraffic,
  gitETag,
  gitFacts,
  gitOutcome,
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
  const choices = useGitChoices(props.session, props.view.generation, kind);
  const coverage = useGitCoverage(
    props.session,
    props.view.generation,
    kind === "repositories" &&
      props.view.viewKey === props.resolved.canonicalFragment,
  );
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
  if (selected === undefined)
    return <GitCollection {...props} choices={choices} coverage={coverage} />;
  if (selected === "new")
    return (
      <GitEditor
        {...props}
        choices={choices}
        coverage={coverage}
        mode="create"
      />
    );
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
        <a
          href={serializeLocation({
            ...props.resolved.location,
            segments: [props.resolved.location.destination],
          })}
        >
          Back to Git {kind}
        </a>
      </nav>
      <header class="detail-context" data-testid="detail-context">
        <div class="detail-context-heading">
          <h1 tabindex={-1}>{label(value)}</h1>
          {kind === "credentials" ? (
            <StatusLabel
              state={error ? "stale" : value.available ? "current" : "warning"}
            >
              {value.available ? "Configured" : "Unavailable"}
            </StatusLabel>
          ) : kind === "grants" ? (
            <StatusLabel
              state={
                error
                  ? "stale"
                  : value.state === "active"
                    ? "current"
                    : "neutral"
              }
            >
              {value.state === "active" ? "Active" : "Expired"}
            </StatusLabel>
          ) : null}
        </div>
        <p class="technical-value">{value.id}</p>
      </header>
      {error && (
        <StateNotice state="error" title="Git resource refresh unavailable">
          Saved facts may be stale. Refresh before making changes.
        </StateNotice>
      )}
      <section class="detail-section">
        <h2>{`${singular(kind)[0]!.toUpperCase()}${singular(kind).slice(1)} details`}</h2>
        <dl class="detail-facts">
          {kind === "repositories" && (
            <>
              <div>
                <dt>Canonical destination</dt>
                <dd>{value.url}</dd>
              </div>
              <div>
                <dt>Origin enabled</dt>
                <dd>
                  {coverage.loading || coverage.error || !coverage.profile
                    ? "Unavailable"
                    : gitOriginCovered(value.url!, coverage.profile) ===
                        undefined
                      ? "Unavailable"
                      : coverage.profile.active &&
                          gitOriginCovered(value.url!, coverage.profile)
                        ? "Yes"
                        : "No"}
                </dd>
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
                    <GitRelationship
                      id={value.credential_id}
                      kind="credentials"
                      choices={choices}
                    />
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
                  <GitRelationship
                    id={value.principal_id!}
                    kind="agents"
                    choices={choices}
                  />
                </dd>
              </div>
              <div>
                <dt>Repository</dt>
                <dd>
                  <GitRelationship
                    id={value.repository_id!}
                    kind="repositories"
                    choices={choices}
                  />
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
                          <GitRelationship
                            id={r.id}
                            kind="repositories"
                            choices={choices}
                          />
                        </p>
                      ))
                    : "None"}
                </dd>
              </div>
            </>
          )}
        </dl>
        {kind === "grants" &&
          (value.policy?.refs.length ? (
            <ul>
              {value.policy?.refs.map((r) => (
                <li key={`${r.ref.kind}:${r.ref.value}`}>
                  <span class="technical-value">{r.ref.value}</span> (
                  {r.ref.kind}
                  ): {r.actions.join(", ")}
                </li>
              ))}
            </ul>
          ) : (
            <p>No push permissions</p>
          ))}
        <h3>Metadata</h3>
        <dl class="detail-facts">
          <div>
            <dt>Revision</dt>
            <dd>{value.revision}</dd>
          </div>
        </dl>
      </section>
      <GitEditor
        {...props}
        choices={choices}
        resource={value}
        mode="edit"
        unavailable={error}
      />
      {kind === "credentials" && (
        <GitEditor
          {...props}
          choices={choices}
          resource={value}
          mode="rotate"
          unavailable={error}
        />
      )}
      <GitEditor
        {...props}
        choices={choices}
        resource={value}
        mode="delete"
        unavailable={error}
      />
    </div>
  );
}
function GitRelationship({
  id,
  kind,
  choices,
}: {
  id: string;
  kind: "agents" | "repositories" | "credentials";
  choices: GitChoices;
}) {
  const name = choices[kind].find((item) => item.id === id)?.name;
  return (
    <TableIdentity
      primary={
        <a href={kind === "agents" ? `#/agents/${id}` : `#/git/${kind}/${id}`}>
          {name ||
            (choices.loading ? "Loading relationship" : "Resource unavailable")}
          {name && choices.error ? " · Last known" : ""}
        </a>
      }
      secondary={id}
    />
  );
}

function GitCollection(
  props: Props & {
    kind: GitKind;
    choices: GitChoices;
    coverage: GitCoverageSource;
  },
) {
  const navigate = useUnsavedChanges(false),
    { kind } = props;
  const { items, controls } = useCollectionPage<GitResource>(
    props.session,
    props.resolved,
    props.view,
    (query, cursor, signal) => {
      const params = new URLSearchParams({
        limit: "50",
        sort: query.sort ?? (kind === "grants" ? "description" : "name"),
        direction: query.direction ?? "ascending",
      });
      for (const key of kind === "repositories"
        ? ["name", "destination", "credential"]
        : kind === "credentials"
          ? ["name", "origin", "status"]
          : ["identity", "repository", "principal", "state"]) {
        const value = query[`filter_${key}`];
        if (value !== undefined) params.set(key, value);
      }
      if (cursor !== null) params.set("cursor", cursor);
      return readCollectionPage(
        props.session,
        `/api/v2/git/${kind}?${params}`,
        (v) => decodeGitResource(kind, v),
        signal,
      );
    },
    navigate,
    { key: kind === "grants" ? "description" : "name", direction: "ascending" },
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
          rowHeaderKey={kind === "grants" ? "description" : "name"}
          remote={controls}
          itemNames={{ singular: singular(kind), plural: kind }}
          emptyTitle={`No Git ${kind}`}
          items={items}
          rowKey={(r) => r.id}
          filters={[
            {
              key: kind === "grants" ? "identity" : "name",
              label: kind === "grants" ? "Description or ID" : "Name or ID",
              type: "text",
              value: (r) => r.name ?? r.description ?? "",
              literalValues: (r) => [r.id],
            },
            ...(kind === "grants"
              ? [
                  {
                    key: "repository",
                    label: "Repository",
                    type: "text" as const,
                    value: (r: GitResource) =>
                      props.choices.repositories.find(
                        (v) => v.id === r.repository_id,
                      )?.name ?? "",
                    literalValues: (r: GitResource) => [r.repository_id!],
                  },
                  {
                    key: "principal",
                    label: "Agent",
                    type: "text" as const,
                    value: (r: GitResource) =>
                      props.choices.agents.find((v) => v.id === r.principal_id)
                        ?.name ?? "",
                    literalValues: (r: GitResource) => [r.principal_id!],
                  },
                  {
                    key: "state",
                    label: "Status",
                    type: "select" as const,
                    value: (r: GitResource) => r.state!,
                    options: [
                      { value: "active", label: "Active" },
                      { value: "expired", label: "Expired" },
                    ],
                  },
                ]
              : kind === "repositories"
                ? [
                    {
                      key: "destination",
                      label: "Destination",
                      type: "text" as const,
                      value: (r: GitResource) => r.url!,
                      placeholder: "Literal destination substring",
                    },
                    {
                      key: "credential",
                      label: "Git credential",
                      type: "text" as const,
                      value: (r: GitResource) =>
                        props.choices.credentials.find(
                          (v) => v.id === r.credential_id,
                        )?.name ?? "",
                      literalValues: (r: GitResource) => [
                        r.credential_id ?? "",
                      ],
                    },
                  ]
                : [
                    {
                      key: "origin",
                      label: "HTTPS origin",
                      type: "text" as const,
                      value: (r: GitResource) => r.origin!,
                      placeholder: "Destination",
                    },
                    {
                      key: "status",
                      label: "Status",
                      type: "select" as const,
                      value: (r: GitResource) =>
                        r.available ? "configured" : "unavailable",
                      options: [
                        { value: "configured", label: "Configured" },
                        { value: "unavailable", label: "Unavailable" },
                      ],
                    },
                  ]),
          ]}
          additionalSorts={[{ key: "id", label: "ID", sortValue: (r) => r.id }]}
          initialSort={{
            key: kind === "grants" ? "description" : "name",
            direction: "ascending",
          }}
          columns={[
            {
              key: kind === "grants" ? "description" : "name",
              sortValue: (r) => label(r),
              label:
                kind === "repositories"
                  ? "Repository"
                  : kind === "credentials"
                    ? "Credential"
                    : "Grant",
              role: "identity",
              render: (r) => (
                <TableIdentity
                  primary={
                    <a
                      href={serializeLocation({
                        ...props.resolved.location,
                        segments: [props.resolved.location.destination, r.id],
                      })}
                    >
                      {label(r)}
                    </a>
                  }
                  secondary={r.id}
                />
              ),
            },
            {
              key:
                kind === "grants"
                  ? "repository"
                  : kind === "credentials"
                    ? "origin"
                    : "destination",
              sortValue: (r) =>
                r.url ??
                r.origin ??
                props.choices.repositories.find((v) => v.id === r.repository_id)
                  ?.name ??
                r.repository_id!,
              label: kind === "grants" ? "Repository" : "Destination",
              role: "relation",
              render: (r) =>
                kind === "grants" ? (
                  <GitRelationship
                    id={r.repository_id!}
                    kind="repositories"
                    choices={props.choices}
                  />
                ) : (
                  <>
                    {r.url ?? r.origin}
                    {kind === "repositories" && (
                      <GitCoverage
                        source={props.coverage}
                        destination={r.url!}
                      />
                    )}
                  </>
                ),
            },
            ...(kind === "grants"
              ? [
                  {
                    key: "principal",
                    sortValue: (r: GitResource) =>
                      props.choices.agents.find((v) => v.id === r.principal_id)
                        ?.name ?? r.principal_id!,
                    label: "Agent",
                    role: "relation" as const,
                    render: (r: GitResource) => (
                      <GitRelationship
                        id={r.principal_id!}
                        kind="agents"
                        choices={props.choices}
                      />
                    ),
                  },
                ]
              : []),
            {
              key:
                kind === "repositories"
                  ? "credential"
                  : kind === "grants"
                    ? "state"
                    : "status",
              sortValue: (r) =>
                kind === "repositories"
                  ? (props.choices.credentials.find(
                      (v) => v.id === r.credential_id,
                    )?.name ??
                    r.credential_id ??
                    "")
                  : kind === "grants"
                    ? r.state!
                    : r.available
                      ? "configured"
                      : "unavailable",
              label: kind === "repositories" ? "Git credential" : "Status",
              role: kind === "repositories" ? "relation" : "status",
              render: (r) =>
                kind === "repositories" ? (
                  r.credential_id ? (
                    <GitRelationship
                      id={r.credential_id}
                      kind="credentials"
                      choices={props.choices}
                    />
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
  choices,
  resource,
  mode,
  unavailable = false,
  coverage,
  ...props
}: Props & {
  coverage?: GitCoverageSource;
  kind: GitKind;
  choices: GitChoices;
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
  const aliasesEdited = useRef(false);
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
  const title =
    mode === "rotate"
      ? "Rotate secret"
      : `${mode === "create" ? "Create Git" : mode === "edit" ? "Edit" : "Delete"} ${singular(kind)}`;
  const change = <K extends keyof typeof draft>(
    key: K,
    value: (typeof draft)[K],
  ) => {
    if (key === "aliases") aliasesEdited.current = true;
    setDraft((d) => {
      if (
        key === "url" &&
        kind === "repositories" &&
        mode === "create" &&
        !aliasesEdited.current
      ) {
        const alias = oppositeGitAlias(String(value));
        return { ...d, url: String(value), aliases: alias ? [alias] : [] };
      }
      return { ...d, [key]: value };
    });
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
      if (kind === "repositories" && !validGitAliases(draft.url, draft.aliases))
        return "Aliases must be distinct from each other and the canonical destination.";
      if (
        kind === "grants" &&
        (!gitID.test(draft.principal) || !gitID.test(draft.repository))
      )
        return "Enter valid agent and repository IDs.";
      if (
        (kind === "repositories" || (kind === "grants" && mode === "create")) &&
        (choices.loading || choices.error)
      )
        return "Refresh resource choices before reviewing.";
      if (
        kind === "repositories" &&
        draft.credential !== "" &&
        !choices.credentials.some(
          (c) =>
            c.id === draft.credential && compatibleGitCredential(c, draft.url),
        )
      )
        return "Select an available credential for this HTTPS origin, or choose None.";
      if (
        kind === "grants" &&
        mode === "create" &&
        (!choices.agents.some((a) => a.id === draft.principal) ||
          !choices.repositories.some((r) => r.id === draft.repository))
      )
        return "Select an existing agent and repository.";
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
              <section class="git-list-editor" aria-label="Repository aliases">
                <h3>Aliases</h3>
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
              </section>
              <FormField
                id={`git-credential-${mode}`}
                label="Git credential"
                {...(draft.credential ? { hint: draft.credential } : {})}
              >
                {(attributes) => (
                  <select
                    {...attributes}
                    value={draft.credential}
                    onChange={(e) =>
                      change("credential", e.currentTarget.value)
                    }
                  >
                    <option value="">None — public repository</option>
                    {draft.credential &&
                      !choices.credentials.some(
                        (c) =>
                          c.id === draft.credential &&
                          compatibleGitCredential(c, draft.url),
                      ) && (
                        <option value={draft.credential} disabled>
                          {choices.credentials.find(
                            (c) => c.id === draft.credential,
                          )?.name || draft.credential}{" "}
                          · Unavailable or incompatible
                        </option>
                      )}
                    {choices.credentials
                      .filter((c) => compatibleGitCredential(c, draft.url))
                      .map((c) => (
                        <option key={c.id} value={c.id}>
                          {choiceLabel(c, choices.credentials)}
                        </option>
                      ))}
                  </select>
                )}
              </FormField>
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
              {(["principal", "repository"] as const).map((key) => {
                const items =
                  key === "principal" ? choices.agents : choices.repositories;
                const title = key === "principal" ? "Agent" : "Repository";
                return (
                  <FormField
                    id={`git-${key}-${mode}`}
                    label={title}
                    {...(draft[key] ? { hint: draft[key] } : {})}
                  >
                    {(attributes) =>
                      resource !== undefined ? (
                        <input
                          {...attributes}
                          readOnly
                          value={
                            items.find((item) => item.id === draft[key])
                              ?.name ?? draft[key]
                          }
                        />
                      ) : (
                        <select
                          {...attributes}
                          required
                          disabled={choices.loading || choices.error}
                          value={draft[key]}
                          onChange={(e) => change(key, e.currentTarget.value)}
                        >
                          <option value="">
                            {choices.loading
                              ? `Loading ${title.toLowerCase()}s`
                              : choices.error
                                ? `${title} choices unavailable`
                                : items.length
                                  ? `Select ${title.toLowerCase()}`
                                  : `No ${title.toLowerCase()}s configured`}
                          </option>
                          {draft[key] &&
                            !items.some((item) => item.id === draft[key]) && (
                              <option value={draft[key]}>
                                {draft[key]} · Unavailable
                              </option>
                            )}
                          {items.map((item) => (
                            <option key={item.id} value={item.id}>
                              {choiceLabel(item, items)}
                            </option>
                          ))}
                        </select>
                      )
                    }
                  </FormField>
                );
              })}
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
              <section class="git-list-editor" aria-label="Push permissions">
                <h3>Push permissions</h3>
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
                    <div class="push-permissions-row">
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
                                            : r.actions.filter(
                                                (a) => a !== action,
                                              ),
                                        }
                                      : r,
                                  ),
                                )
                              }
                            />
                          )}
                        </FormField>
                      ))}
                    </div>
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
                      {
                        ref: { kind: "exact", value: "" },
                        actions: ["update"],
                      },
                    ]);
                  }}
                >
                  Add push rule
                </button>
              </section>
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
          {metadata &&
            (kind === "repositories" || kind === "grants") &&
            choices.error && (
              <StateNotice state="error" title="Resource choices unavailable">
                Refresh to load choices. Your draft is retained.
              </StateNotice>
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
          <div class="form-actions">
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
          </div>
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
              <dl class="detail-facts">
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
                      <dd>
                        {draft.url}
                        {coverage && (
                          <GitCoverage
                            source={coverage}
                            destination={draft.url}
                            contextual
                          />
                        )}
                      </dd>
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
                      <dd>
                        {draft.credential ? (
                          <GitRelationship
                            id={draft.credential}
                            kind="credentials"
                            choices={choices}
                          />
                        ) : (
                          "None — public repository"
                        )}
                      </dd>
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
                      <dd>
                        <GitRelationship
                          id={draft.principal}
                          kind="agents"
                          choices={choices}
                        />
                      </dd>
                    </div>
                    <div>
                      <dt>Repository</dt>
                      <dd>
                        <GitRelationship
                          id={draft.repository}
                          kind="repositories"
                          choices={choices}
                        />
                      </dd>
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

export function GitTrafficView(
  props: Props & { controller: GitTrafficController },
) {
  const selected = props.resolved.location.segments[1];
  const repositories = useGitChoices(
    props.session,
    props.view.generation,
    "traffic",
  );
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
  if (!selected)
    return <GitTrafficCollection {...props} repositories={repositories} />;
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
        <a
          href={serializeLocation({
            ...props.resolved.location,
            segments: ["git-traffic"],
          })}
        >
          Back to Git traffic
        </a>
      </nav>
      <header class="detail-context" data-testid="detail-context">
        <div class="detail-context-heading">
          <h1 tabindex={-1}>
            {gitLabels[a.operation]}
            {a.policy?.repository_name ? ` — ${a.policy.repository_name}` : ""}
          </h1>
          <StatusLabel
            state={
              error
                ? "stale"
                : !a.allowed
                  ? "neutral"
                  : ["HTTP success", "Reported success"].includes(
                        gitOutcome(value),
                      )
                    ? "current"
                    : "warning"
            }
          >
            {gitOutcome(value)}
          </StatusLabel>
        </div>
        <p class="technical-value">{a.id}</p>
      </header>
      {error && (
        <StateNotice state="warning" title="Git traffic refresh unavailable" />
      )}
      <section class="detail-section">
        <h2>Recorded exchange</h2>
        <dl class="detail-facts">
          <div>
            <dt>Admitted</dt>
            <dd>
              <UserTime value={a.admitted_at} />
            </dd>
          </div>
          <div>
            <dt>Repository</dt>
            <dd>
              <RecordedRepository item={value} choices={repositories} />
            </dd>
          </div>
          {a.policy && (
            <div>
              <dt>Canonical destination at admission</dt>
              <dd>{a.policy.repository_url}</dd>
            </div>
          )}
          <div>
            <dt>Operation</dt>
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
        {a.operation === "push" && (
          <section aria-label="Targeted refs">
            <h2>Targeted refs</h2>
            {!a.ref_evidence ? (
              <p>Ref evidence unavailable (legacy record).</p>
            ) : (
              <>
                <p>
                  {a.ref_evidence.refs.length} of {a.commands} requested refs
                  retained
                  {a.ref_evidence.state === "truncated"
                    ? "; truncated — omitted refs and their outcomes are unavailable."
                    : "."}
                </p>
                <div
                  class="table-region table-resource"
                  role="region"
                  aria-label="Targeted ref evidence"
                  tabindex={0}
                >
                  <table>
                    <caption>Requested operations and upstream claims</caption>
                    <thead>
                      <tr>
                        <th scope="col">Ref</th>
                        <th scope="col">Requested action</th>
                        <th scope="col">Upstream outcome</th>
                      </tr>
                    </thead>
                    <tbody>
                      {a.ref_evidence.refs.map((ref, i) => (
                        <tr key={ref.name}>
                          <th scope="row" data-label="Ref">
                            <span class="technical-value">{ref.name}</span>
                          </th>
                          <td data-label="Requested action">
                            {sentenceCase(ref.action)}
                          </td>
                          <td data-label="Upstream outcome">
                            {!a.allowed
                              ? "Not dispatched"
                              : c?.ref_outcomes?.[i] === "ok"
                                ? "Reported success"
                                : c?.ref_outcomes?.[i] === "ng"
                                  ? "Reported failure"
                                  : "Unknown"}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              </>
            )}
          </section>
        )}
        <details>
          <summary>Admission-time policy references</summary>
          <dl class="detail-facts">
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
        </details>
      </section>
    </div>
  );
}
function RecordedRepository({
  item,
  choices,
}: {
  item: GitTraffic;
  choices: GitChoices;
}) {
  const id = item.admission.repository.id;
  const name = item.admission.policy?.repository_name || id || "Unavailable";
  const available =
    !choices.loading &&
    !choices.error &&
    choices.repositories.some((repository) => repository.id === id);
  return (
    <TableIdentity
      primary={
        available ? <a href={`#/git/repositories/${id}`}>{name}</a> : name
      }
      secondary={id || undefined}
    />
  );
}

function GitTrafficCollection(
  props: Props & { controller: GitTrafficController; repositories: GitChoices },
) {
  const navigate = useUnsavedChanges(false);
  const [current, setCurrent] = useState(props.controller.snapshot());
  useEffect(() => props.controller.subscribe(setCurrent), [props.controller]);
  const query = props.resolved.location.query;
  const [draft, setDraft] = useState({ ...query });
  const [invalid, setInvalid] = useState(false);
  const apply = (next: Record<string, string>) => {
    const clean = Object.fromEntries(
      Object.entries(next).filter(([, value]) => value !== ""),
    );
    if (!validGitTrafficQuery(clean)) {
      setInvalid(true);
      return;
    }
    setInvalid(false);
    if (JSON.stringify(clean) !== JSON.stringify(query))
      navigate(
        serializeLocation({
          destination: "git-traffic",
          segments: ["git-traffic"],
          query: clean,
        }),
      );
  };
  useDebouncedInput(draft, apply);
  const items = current.key === props.view.viewKey ? current.items : [];
  const busy = props.view.panels["git-traffic"]?.refreshing === true;
  const failed =
    current.error || props.view.panels["git-traffic"]?.status === "error";
  return (
    <section class="panel domain-panel">
      <div class="collection-toolbar live-collection-toolbar">
        <label for="git-traffic-live-mode">Live mode</label>
        <BinaryToggle
          attributes={{ id: "git-traffic-live-mode" }}
          checked={current.live}
          showState={false}
          onChange={(live) => props.controller.setLive(live)}
        />
      </div>
      <HistoryWindow query={query} />
      <div
        class="table-filters collection-query-filters"
        role="group"
        aria-label="Git traffic filters"
      >
        {Object.entries(gitTrafficOptions).map(([key, values]) => (
          <>
            {key === "admission" && (
              <input
                type="search"
                aria-label="Repo name or ID"
                placeholder="Repo name or ID"
                value={draft.filter_repository ?? ""}
                onInput={(event) =>
                  setDraft({
                    ...draft,
                    filter_repository: event.currentTarget.value,
                  })
                }
              />
            )}
            <select
              aria-label={
                key === "report" ? "Upstream report" : sentenceCase(key)
              }
              value={draft[`filter_${key}`] ?? ""}
              onChange={(event) => {
                const next = {
                  ...draft,
                  [`filter_${key}`]: event.currentTarget.value,
                };
                setDraft(next);
                apply(next);
              }}
            >
              <option value="">
                {key === "report" ? "Upstream report" : sentenceCase(key)}: any
              </option>
              {values.map((value) => (
                <option value={value}>
                  {gitLabels[value] ?? sentenceCase(value)}
                </option>
              ))}
            </select>
          </>
        ))}
        <button
          type="button"
          disabled={
            !Object.values(draft).some(Boolean) &&
            !Object.values(query).some(Boolean)
          }
          onClick={() => {
            setDraft({});
            apply({});
          }}
        >
          Reset
        </button>
      </div>
      <p class="table-filter-hint">
        Search recorded repository names with typo tolerance or literal partial
        IDs.
      </p>
      {invalid && (
        <StateNotice state="error" title="Invalid repository search" />
      )}
      {current.paused && (
        <div class="inline-actions">
          {current.live && (
            <StatusLabel state="warning">
              Live paused while viewing older results
            </StatusLabel>
          )}
          <button type="button" onClick={() => props.controller.resume()}>
            {current.live ? "Resume live" : "Return to newest"}
          </button>
        </div>
      )}
      {current.notice && <StateNotice state="warning" title={current.notice} />}
      {current.olderError && (
        <StateNotice state="error" title="Older exchanges unavailable">
          Loaded exchanges are unchanged. Try Load older exchanges again.
        </StateNotice>
      )}
      {failed && (
        <StateNotice state="error" title="Git traffic unavailable">
          Previously loaded exchanges may be stale.
        </StateNotice>
      )}
      {!current.loaded && !failed && (
        <StateNotice state="loading" title="Loading Git traffic" />
      )}
      <CollectionTable
        caption="Git traffic"
        rowHeaderKey="exchange"
        layout="activity"
        localStale={failed}
        localLoading={!current.loaded && !failed}
        historySummary
        historyMatching={Object.keys(query).length > 0}
        summaryExtra={
          current.loaded && !failed ? (
            <span>
              {current.next === null
                ? "No older exchanges"
                : items.length >= 500
                  ? "Load limit reached. Narrow the filters or return to newest."
                  : ""}
            </span>
          ) : undefined
        }
        hasMore={current.next !== null && items.length < 500 && !failed}
        loadingMore={busy || current.loadingOlder}
        loadMoreLabel="Load older exchanges"
        onLoadMore={() => void props.controller.older()}
        itemNames={{ singular: "exchange", plural: "exchanges" }}
        emptyTitle="No Git traffic"
        items={items}
        rowKey={(r) => r.admission.id}
        filters={[]}
        columns={[
          {
            key: "admitted",
            label: "Admitted",
            role: "time",
            render: (r) => <UserTime value={r.admission.admitted_at} />,
          },
          {
            key: "exchange",
            label: "Operation",
            role: "identity",
            render: (r) => (
              <TableIdentity
                primary={
                  <a
                    href={serializeLocation({
                      ...props.resolved.location,
                      segments: ["git-traffic", r.admission.id],
                    })}
                  >
                    {gitLabels[r.admission.operation]}
                  </a>
                }
                secondary={r.admission.id}
              />
            ),
          },
          {
            key: "repository",
            label: "Repository",
            role: "relation",
            render: (r) => (
              <RecordedRepository item={r} choices={props.repositories} />
            ),
          },
          {
            key: "outcome",
            label: "Outcome",
            role: "status",
            render: (r) => (
              <StatusLabel
                state={
                  ["HTTP success", "Reported success"].includes(gitOutcome(r))
                    ? "current"
                    : "warning"
                }
              >
                {gitOutcome(r)}
              </StatusLabel>
            ),
          },
          {
            key: "admission",
            label: "Admission",
            role: "status",
            render: (r) => (
              <StatusLabel state="neutral">{gitFacts(r).admission}</StatusLabel>
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
