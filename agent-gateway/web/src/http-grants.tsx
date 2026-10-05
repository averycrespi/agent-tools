import { useEffect, useRef, useState } from "preact/hooks";
import type { RefObject } from "preact";
import type { ResolvedLocation } from "./location";
import { useUnsavedChanges } from "./navigation";
import type {
  MutationCoordinator,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import {
  CollectionTable,
  ConfirmationDialog,
  DetailComparison,
  FormField,
  StateNotice,
  StatusLabel,
  TableIdentity,
} from "./primitives";
import { httpDefaultText, readPrincipals, type Principal } from "./principals";
import { decodeHTTPCredential } from "./http-credentials";
import type { SessionClient } from "./session";
import {
  readCollectionPage,
  useCollectionPage,
  type ViewSnapshot,
} from "./view";
import { UserTime } from "./time";
import { parseAuditJSON as parsePolicyJSON } from "./audit-contract";

import {
  decodeGrant,
  decodePreview,
  object,
  exact,
  idPattern,
  type Kind,
  type Destination,
  type Policy,
  type Grant,
  type GrantRow,
} from "./http-policy-response";
const labels: Record<Kind, string> = {
  block_destination: "Block destination",
  allow_tunnel: "Allow tunnel",
  block_requests: "Block requests",
  allow_requests: "Allow requests",
};
async function boundedJSON(response: Response): Promise<unknown> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("HTTP policy data unavailable.");
  const text = await response.text();
  if (text.length > 1024 * 1024)
    throw new Error("HTTP policy data unavailable.");
  return parsePolicyJSON(text);
}
async function read<T>(
  session: SessionClient,
  path: string,
  decode: (value: unknown, response: Response) => T,
): Promise<T | undefined> {
  return session.runProtected(async (context) => {
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
    if (!response.ok) throw new Error("HTTP policy data unavailable.");
    return decode(await boundedJSON(response), response);
  });
}
async function readCredentialChoices(
  session: SessionClient,
): Promise<ReturnType<typeof decodeHTTPCredential>[]> {
  const items: ReturnType<typeof decodeHTTPCredential>[] = [];
  let cursor: string | null = null;
  for (let page = 0; page < 3; page++) {
    const result:
      | {
          items: ReturnType<typeof decodeHTTPCredential>[];
          next: string | null;
        }
      | undefined = await read(
      session,
      `/api/v2/http/credentials?limit=100${cursor === null ? "" : "&cursor=" + encodeURIComponent(cursor)}`,
      (value) => {
        const r = exact(value, [
          "items",
          "next_cursor",
          "total_count",
          "offset",
        ]);
        if (
          !Array.isArray(r.items) ||
          r.items.length > 100 ||
          (r.next_cursor !== null &&
            (typeof r.next_cursor !== "string" || r.next_cursor.length > 512))
        )
          throw new Error("Invalid credential choices.");
        return {
          items: r.items.map(decodeHTTPCredential),
          next: r.next_cursor as string | null,
        };
      },
    );
    if (result === undefined)
      throw new Error("Credential choices unavailable.");
    items.push(...result.items);
    if (
      items.length > 256 ||
      new Set(items.map((i) => i.id)).size !== items.length
    )
      throw new Error("Invalid credential choices.");
    if (result.next === null) return items;
    cursor = result.next;
  }
  throw new Error("Credential choice limit exceeded.");
}
function grantETag(g: Grant): string {
  return `"http-grant-${g.id}-${g.revision}"`;
}
async function grantResponse(response: Response): Promise<Grant> {
  const g = decodeGrant(await boundedJSON(response));
  if (response.headers.get("ETag") !== grantETag(g))
    throw new Error("Invalid HTTP grant revision.");
  return g;
}
function destination(p: Policy): Destination {
  return p.destination ?? p.request!.origin;
}
function policyReviewFields(p: Policy) {
  const d = destination(p);
  const r = p.request;
  return [
    { label: "Type", value: labels[p.type] },
    {
      label: "Destination",
      value: `${r === undefined ? "" : r.origin.scheme + "://"}${d.host}:${d.port}`,
    },
    {
      label: "Methods",
      value:
        r === undefined
          ? "Not applicable"
          : r.methods.any
            ? "Any method"
            : r.methods.values!.join(", "),
    },
    {
      label: "Path",
      value:
        r === undefined
          ? "Not applicable"
          : r.path.kind === "any"
            ? "Any path"
            : r.path.kind === "exact"
              ? r.path.value!
              : r.path.value + " and descendants",
    },
    {
      label: "Local/private access",
      value:
        p.type === "allow_requests" || p.type === "allow_tunnel"
          ? p.allow_private
            ? "Enabled"
            : "Disabled"
          : "Not applicable",
    },
    {
      label: "Credential",
      value:
        p.type !== "allow_requests"
          ? "Not applicable"
          : (p.credential_id ?? "No credential"),
      href:
        p.credential_id === undefined
          ? undefined
          : `#/http/credentials/${p.credential_id}`,
    },
  ];
}
function PolicyFacts({
  policy,
  credentialName,
}: {
  policy: Policy;
  credentialName?: string | undefined;
}) {
  return (
    <>
      {policyReviewFields(policy).map((field) => (
        <div key={field.label}>
          <dt>{field.label}</dt>
          <dd>
            {field.href === undefined ? (
              field.value
            ) : (
              <>
                <a href={field.href}>
                  {field.label === "Credential"
                    ? (credentialName ?? field.value)
                    : field.value}
                </a>
                {field.label === "Credential" &&
                  credentialName !== undefined && (
                    <span class="table-identifier">{policy.credential_id}</span>
                  )}
              </>
            )}
          </dd>
        </div>
      ))}
    </>
  );
}
function GrantComparison({
  current,
  proposed,
  principals,
}: {
  current: Grant;
  proposed: Pick<
    Grant,
    "description" | "principal_id" | "policy" | "expires_at"
  >;
  principals: Principal[];
}) {
  const fields = (grant: typeof proposed) => [
    { label: "Description", value: grant.description || "Unnamed HTTP grant" },
    {
      label: "Agent",
      value: grant.principal_id,
      content: (
        <TableIdentity
          primary={
            principals.find((p) => p.id === grant.principal_id)?.displayName ??
            "Agent unavailable"
          }
          secondary={grant.principal_id}
        />
      ),
    },
    ...policyReviewFields(grant.policy).map((field) => ({
      ...field,
      content:
        field.href === undefined ? (
          field.value
        ) : (
          <a href={field.href}>{field.value}</a>
        ),
    })),
    {
      label: "Expires",
      value: grant.expires_at,
      content: <UserTime value={grant.expires_at} fallback="No expiry" />,
    },
  ];
  const before = fields(current);
  return (
    <DetailComparison
      label="Current versus proposed grant"
      beforeLabel="Current"
      afterLabel="Proposed"
      rows={fields(proposed).map((field, index) => ({
        label: field.label,
        before:
          "content" in before[index]!
            ? before[index]!.content
            : before[index]!.value,
        after: "content" in field ? field.content : field.value,
        changed: before[index]!.value !== field.value,
      }))}
    />
  );
}
interface Props {
  session: SessionClient;
  mutations: MutationCoordinator;
  resolved: ResolvedLocation;
  view: ViewSnapshot;
  onRefresh: () => void;
  notify: (message?: string) => void;
}

function useGrantChoices(props: Props, enabled: boolean) {
  const [principals, setPrincipals] = useState<Principal[]>([]);
  const [credentials, setCredentials] = useState<
    ReturnType<typeof decodeHTTPCredential>[]
  >([]);
  const [loadError, setLoadError] = useState(false);
  const [loading, setLoading] = useState(true);
  useEffect(() => {
    if (!enabled) return;
    let current = true;
    setLoading(true);
    void Promise.all([
      readPrincipals(props.session),
      readCredentialChoices(props.session),
    ])
      .then(([people, creds]) => {
        if (current) {
          setPrincipals(people);
          setCredentials(creds);
          setLoadError(false);
        }
      })
      .catch(() => {
        if (current) {
          setPrincipals([]);
          setCredentials([]);
          setLoadError(true);
        }
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [enabled, props.view.generation]);
  return { principals, credentials, loadError, loading };
}

function usePolicyMutation<T>(props: {
  mutations: MutationCoordinator;
  view: ViewSnapshot;
  onRefresh: () => void;
}) {
  const [controller] = useState(() => props.mutations.create<T>());
  const [state, setState] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [blockedAt, setBlockedAt] = useState<number>();
  const [reviewRequired, setReviewRequired] = useState(false);
  useEffect(() => controller.subscribe(setState), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  const blocked =
    state.state === "submitting" ||
    state.state === "uncertain" ||
    state.availability === "storage_latched" ||
    (blockedAt !== undefined && props.view.generation <= blockedAt);
  const submit = async (spec: MutationSpec<T>, success: (value: T) => void) => {
    try {
      controller.begin(spec);
      const outcome = await controller.submit();
      if (outcome.kind === "acknowledged") {
        controller.abandon();
        success(outcome.value);
      } else if (
        outcome.kind === "uncertain" ||
        (outcome.kind === "rejected" && outcome.requiresRefresh)
      ) {
        setBlockedAt(props.view.generation);
        if (outcome.kind === "rejected") setReviewRequired(true);
        props.onRefresh();
      }
    } catch {
      setBlockedAt(props.view.generation);
      props.onRefresh();
    }
  };
  return {
    blocked,
    submit,
    state,
    reviewRequired,
    review: () => {
      setReviewRequired(false);
      controller.abandon();
    },
  };
}
function MutationNotice({ state }: { state: MutationSnapshot }) {
  if (state.state === "uncertain")
    return (
      <StateNotice state="error" title="Outcome uncertain">
        <p>Inspect current policy. Do not replay this change.</p>
      </StateNotice>
    );
  if (state.state === "rejected")
    return (
      <StateNotice state="error" title="Change not applied">
        <p>{state.problem?.title ?? "Check the proposed policy."}</p>
        {state.requiresRefresh && (
          <p>Inspect refreshed policy before reviewing again.</p>
        )}
      </StateNotice>
    );
  return null;
}

export function HTTPGrants(props: Props) {
  const selected = props.resolved.location.segments[1];
  const [detail, setDetail] = useState<Grant>();
  const [error, setError] = useState(false);
  const [loading, setLoading] = useState(true);
  const [editorVersion, setEditorVersion] = useState(0);
  const [saved, setSaved] = useState(false);
  const title = useRef<HTMLHeadingElement>(null);
  const choices = useGrantChoices(
    props,
    selected !== undefined && selected !== "test-access",
  );
  const closeEditor = () => {
    setEditorVersion((version) => version + 1);
    title.current?.focus();
  };
  useEffect(() => {
    let current = true;
    setLoading(true);
    if (
      selected === undefined ||
      selected === "new" ||
      selected === "test-access"
    )
      return;
    void read(
      props.session,
      `/api/v2/http/grants/${selected}`,
      (value, response) => {
        const grant = decodeGrant(value);
        if (
          grant.id !== selected ||
          response.headers.get("ETag") !== grantETag(grant)
        )
          throw new Error("HTTP grant revision unavailable.");
        return grant;
      },
    )
      .then((value) => {
        if (current && value?.id === selected) {
          setDetail(value);
          setError(false);
        }
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
  if (selected === undefined) return <GrantCollection {...props} />;
  if (selected === "test-access") return <AccessPreview {...props} />;
  if (selected === "new") return <GrantEditor {...props} choices={choices} />;
  if (error && detail?.id !== selected)
    return (
      <StateNotice state="error" title="HTTP grant unavailable">
        <p>Refresh to inspect current policy.</p>
      </StateNotice>
    );
  if (detail?.id !== selected)
    return <StateNotice state="loading" title="Loading HTTP grant" />;
  const credentialAvailable = choices.credentials.find(
    (c) => c.id === detail.policy.credential_id,
  )?.available;
  return (
    <div class="domain-view">
      <nav class="detail-navigation" aria-label="HTTP grant navigation">
        <a href="#/http/grants">Back to HTTP grants</a>
      </nav>
      <header class="detail-context" data-testid="detail-context">
        <div class="detail-context-heading">
          <h1 ref={title} tabindex={-1}>
            {detail.description || "Unnamed HTTP grant"}
          </h1>
          <StatusLabel
            state={detail.state === "active" ? "current" : "neutral"}
          >
            {detail.state === "active" ? "Active" : "Expired"}
          </StatusLabel>
        </div>
        <p class="technical-value">{detail.id}</p>
      </header>
      {error && (
        <StateNotice
          state="stale"
          title={
            saved
              ? "Grant saved; refresh unavailable"
              : "Grant details are stale"
          }
        >
          <p>
            {saved
              ? "Showing last acknowledged values."
              : "Showing last loaded values."}{" "}
            Refresh to inspect current policy before editing.
          </p>
        </StateNotice>
      )}
      <section
        class="detail-section"
        aria-labelledby="http-grant-details-title"
      >
        <div class="panel-heading">
          <h2 id="http-grant-details-title">Grant details</h2>
        </div>
        <h3>Policy and relationships</h3>
        <dl class="detail-facts">
          <div>
            <dt>Agent</dt>
            <dd>
              <TableIdentity
                primary={
                  <a href={`#/agents/${detail.principal_id}`}>
                    {choices.principals.find(
                      (p) => p.id === detail.principal_id,
                    )?.displayName ??
                      (choices.loading ? "Loading agent" : "Agent unavailable")}
                  </a>
                }
                secondary={detail.principal_id}
              />
            </dd>
          </div>
          <PolicyFacts
            policy={detail.policy}
            credentialName={
              choices.credentials.find(
                (c) => c.id === detail.policy.credential_id,
              )?.name
            }
          />
          {detail.policy.credential_id !== undefined && (
            <div>
              <dt>Credential material</dt>
              <dd>
                {choices.loading
                  ? "Loading"
                  : credentialAvailable === undefined
                    ? "Unknown"
                    : credentialAvailable
                      ? "Available"
                      : "Unavailable"}
              </dd>
            </div>
          )}
        </dl>
        <h3>Timing</h3>
        <dl class="detail-facts">
          <div>
            <dt>Expires</dt>
            <dd>
              <UserTime value={detail.expires_at} fallback="No expiry" />
            </dd>
          </div>
          <div>
            <dt>Created</dt>
            <dd>
              <UserTime value={detail.created_at} />
            </dd>
          </div>
        </dl>
      </section>
      <GrantEditor
        {...props}
        key={`${detail.id}:${editorVersion}`}
        grant={detail}
        choices={choices}
        unavailable={error || loading}
        onCancel={closeEditor}
        onSaved={(value) => {
          setDetail(value);
          setSaved(true);
          closeEditor();
          title.current?.focus();
          props.notify("Grant saved");
          props.onRefresh();
        }}
      />
    </div>
  );
}
function GrantCollection(props: Props) {
  const navigate = useUnsavedChanges(false);
  const { items, controls } = useCollectionPage<GrantRow>(
    props.session,
    props.resolved,
    props.view,
    (query, cursor, signal) => {
      const params = new URLSearchParams({
        limit: "50",
        sort: query.sort ?? "description",
        direction: query.direction ?? "ascending",
      });
      for (const key of ["identity", "principal", "target", "type", "state"]) {
        const value = query[`filter_${key}`];
        if (value !== undefined) params.set(key, value);
      }
      if (query.principal_id !== undefined)
        params.set("principal_id", query.principal_id);
      if (cursor !== null) params.set("cursor", cursor);
      return readCollectionPage(
        props.session,
        `/api/v2/http/grants?${params}`,
        (value) => {
          const r = object(value);
          if (
            Object.keys(r).sort().join() !== "grant,principal_display_name" ||
            typeof r.principal_display_name !== "string"
          )
            throw new Error("Invalid grant row.");
          return {
            grant: decodeGrant(r.grant),
            principal_display_name: r.principal_display_name,
          };
        },
        signal,
      );
    },
    navigate,
    { key: "description", direction: "ascending" },
  );
  return (
    <div class="domain-view">
      <div class="collection-toolbar">
        <a class="button-link create-action" href="#/http/grants/new">
          Create grant
        </a>
        <a class="button-link" href="#/http/grants/test-access">
          Test access
        </a>
      </div>
      <section class="panel domain-panel">
        <CollectionTable
          caption="HTTP grants"
          rowHeaderKey="description"
          additionalSorts={[
            { key: "id", label: "Grant ID", sortValue: (r) => r.grant.id },
          ]}
          remote={controls}
          initialSort={{ key: "description", direction: "ascending" }}
          itemNames={{ singular: "grant", plural: "grants" }}
          emptyTitle="No HTTP grants"
          items={items}
          rowKey={(r) => r.grant.id}
          filters={[
            {
              key: "identity",
              label: "Description or ID",
              type: "text",
              value: (r) => r.grant.description ?? "",
              literalValues: (r) => [r.grant.id],
            },
            {
              key: "principal",
              label: "Agent",
              type: "text",
              value: (r) => r.principal_display_name,
              literalValues: (r) => [r.grant.principal_id],
            },
            {
              key: "target",
              label: "Destination host",
              type: "text",
              value: (r) => destination(r.grant.policy).host,
            },
            {
              key: "type",
              label: "Type",
              type: "select",
              value: (r) => r.grant.policy.type,
              options: Object.entries(labels).map(([value, label]) => ({
                value,
                label,
              })),
            },
            {
              key: "state",
              label: "Status",
              type: "select",
              value: (r) => r.grant.state,
              options: [
                { value: "active", label: "Active" },
                { value: "expired", label: "Expired" },
              ],
            },
          ]}
          columns={[
            {
              key: "description",
              label: "Grant",
              role: "identity",
              sortValue: (r) => r.grant.description ?? "",
              render: (r) => (
                <TableIdentity
                  primary={
                    <a href={`#/http/grants/${r.grant.id}`}>
                      {r.grant.description ?? "Unnamed grant"}
                    </a>
                  }
                  secondary={r.grant.id}
                />
              ),
            },
            {
              key: "principal",
              label: "Agent",
              role: "relation",
              sortValue: (r) => r.principal_display_name,
              render: (r) => (
                <a href={`#/agents/${r.grant.principal_id}`}>
                  {r.principal_display_name}
                </a>
              ),
            },
            {
              key: "target",
              label: "Destination",
              role: "relation",
              sortValue: (r) => destination(r.grant.policy).host,
              render: (r) => (
                <>
                  {destination(r.grant.policy).host}:
                  {destination(r.grant.policy).port}
                </>
              ),
            },
            {
              key: "effect",
              label: "Type",
              role: "status",
              sortValue: (r) => r.grant.policy.type,
              render: (r) => labels[r.grant.policy.type],
            },
            {
              key: "state",
              label: "Status",
              role: "status",
              sortValue: (r) => r.grant.state,
              render: (r) => (
                <StatusLabel
                  state={r.grant.state === "active" ? "current" : "neutral"}
                >
                  {r.grant.state === "active" ? "Active" : "Expired"}
                </StatusLabel>
              ),
            },
            {
              key: "expiry",
              label: "Expires",
              role: "time",
              render: (r) => (
                <UserTime value={r.grant.expires_at} fallback="No expiry" />
              ),
            },
          ]}
        />
      </section>
    </div>
  );
}

function GrantEditor(
  props: Props & {
    grant?: Grant;
    choices: ReturnType<typeof useGrantChoices>;
    unavailable?: boolean;
    onCancel?: () => void;
    onSaved?: (grant: Grant) => void;
  },
) {
  const g = props.grant;
  const p = g?.policy;
  const request = p?.request;
  const { principals, credentials, loadError, loading } = props.choices;
  const [principal, setPrincipal] = useState(g?.principal_id ?? "");
  const [description, setDescription] = useState(g?.description ?? "");
  const [kind, setKind] = useState<Kind>(p?.type ?? "allow_requests");
  const [host, setHost] = useState(p === undefined ? "" : destination(p).host);
  const [port, setPort] = useState(
    String(p === undefined ? 443 : destination(p).port),
  );
  const [scheme, setScheme] = useState<"https" | "http">(
    request?.origin.scheme ?? "https",
  );
  const [methods, setMethods] = useState<string[]>(
    request?.methods.values ?? [],
  );
  const [pathKind, setPathKind] = useState<"any" | "exact" | "segment_prefix">(
    request?.path.kind ?? "any",
  );
  const [path, setPath] = useState(request?.path.value ?? "");
  const [privateAccess, setPrivate] = useState(p?.allow_private ?? false);
  const [credential, setCredential] = useState(p?.credential_id ?? "");
  const [expiry, setExpiry] = useState(
    g?.expires_at === null || g?.expires_at === undefined
      ? ""
      : new Date(
          Date.parse(g.expires_at) -
            new Date(g.expires_at).getTimezoneOffset() * 60000,
        )
          .toISOString()
          .slice(0, 16),
  );
  const [dirty, setDirty] = useState(false);
  const [confirm, setConfirm] = useState<"save" | "delete">();
  const [discard, setDiscard] = useState(false);
  const cancelButton = useRef<HTMLButtonElement>(null);
  const deleteButton = useRef<HTMLButtonElement>(null);
  const confirmationFocus = useRef<HTMLButtonElement>(null);
  const descriptionInput = useRef<HTMLInputElement>(null);
  const [error, setError] = useState(false);
  const [expected, setExpected] = useState(
    g === undefined ? null : grantETag(g),
  );
  const button = useRef<HTMLButtonElement>(null);
  const navigate = useUnsavedChanges(dirty);
  const mutation = usePolicyMutation<Grant | null>(props);
  const requests = kind === "allow_requests" || kind === "block_requests";
  const allow = kind === "allow_requests" || kind === "allow_tunnel";
  const compatible = credentials.filter(
    (c) =>
      scheme === "https" &&
      c.boundary.port === Number(port) &&
      (c.boundary.host === host.toLowerCase() ||
        (c.boundary.host.startsWith("*.") &&
          host.toLowerCase().endsWith(c.boundary.host.slice(1)))),
  );
  const policy: Policy = {
    version: 1,
    type: kind,
    ...(requests
      ? {
          request: {
            origin: { scheme, host, port: Number(port) },
            methods:
              methods.length === 0
                ? { any: true as const }
                : { values: methods },
            path:
              pathKind === "any"
                ? { kind: pathKind }
                : { kind: pathKind, value: path },
          },
        }
      : { destination: { host, port: Number(port) } }),
    ...(allow ? { allow_private: privateAccess } : {}),
    ...(kind === "allow_requests" && credential !== ""
      ? { credential_id: credential }
      : {}),
  };
  const stale =
    g !== undefined && (expected !== grantETag(g) || mutation.reviewRequired);
  const submit = () => {
    const deleting = confirm === "delete";
    setConfirm(undefined);
    if (
      mutation.blocked ||
      stale ||
      props.unavailable ||
      (!deleting && (loadError || loading))
    )
      return;
    const body = deleting
      ? null
      : JSON.stringify({
          principal_id: principal,
          description: description || null,
          policy,
          expires_at:
            expiry === ""
              ? null
              : new Date(expiry).toISOString().replace(/\.000Z$/, "Z"),
        });
    void mutation.submit(
      {
        route: `/api/v2/http/grants${g === undefined ? "" : "/" + g.id}`,
        method: deleting ? "DELETE" : g === undefined ? "POST" : "PATCH",
        body,
        precondition: expected,
        requiresPrecondition: g !== undefined,
        idempotency: "none",
        successStatuses: [deleting ? 204 : g === undefined ? 201 : 200],
        decode: async (r) => {
          if (deleting) return null;
          const result = await grantResponse(r);
          if (
            result.principal_id !== principal ||
            (g !== undefined && result.id !== g.id)
          )
            throw new Error("HTTP grant identity unavailable.");
          return result;
        },
      },
      (value) => {
        setDirty(false);
        if (value === null) navigate("#/http/grants", true);
        else if (g === undefined) navigate(`#/http/grants/${value.id}`, true);
        else props.onSaved?.(value);
      },
    );
  };
  return (
    <section
      id="http-grant-editor"
      class="panel domain-panel"
      aria-labelledby="http-grant-editor-title"
    >
      <div class="panel-heading">
        <h2 id="http-grant-editor-title">
          {g === undefined ? "Grant configuration" : "Edit grant"}
        </h2>
      </div>
      {loadError && (
        <StateNotice
          state="error"
          title="Agent or credential choices unavailable"
        />
      )}
      {stale && (
        <StateNotice state="warning" title="Policy changed">
          <p>Inspect the current policy above before applying this draft.</p>
          <button
            type="button"
            disabled={mutation.blocked || props.unavailable}
            onClick={() => {
              setExpected(grantETag(g!));
              mutation.review();
            }}
          >
            Use current revision
          </button>
        </StateNotice>
      )}
      <form
        onInput={() => setDirty(true)}
        onSubmit={(event) => {
          event.preventDefault();
          const valid =
            idPattern.test(principal) &&
            new TextEncoder().encode(description).length <= 256 &&
            host.length <= 253 &&
            /^[1-9][0-9]*$/.test(port) &&
            Number(port) <= 65535 &&
            (pathKind === "any" || path.startsWith("/")) &&
            (expiry === "" || Date.parse(expiry) > Date.now()) &&
            (kind !== "allow_requests" ||
              credential === "" ||
              compatible.some((c) => c.id === credential));
          setError(!valid);
          if (valid) {
            confirmationFocus.current = button.current;
            setConfirm("save");
          }
        }}
      >
        <div role="group" aria-label="Agent and description">
          <FormField
            id="http-grant-principal"
            label="Agent"
            {...(principal ? { hint: principal } : {})}
          >
            {(a) =>
              g !== undefined ? (
                <input
                  {...a}
                  readOnly
                  value={
                    principals.find((v) => v.id === principal)?.displayName ??
                    principal
                  }
                />
              ) : (
                <select
                  {...a}
                  required
                  value={principal}
                  onChange={(e) => setPrincipal(e.currentTarget.value)}
                >
                  <option value="">Select agent</option>
                  {g !== undefined &&
                    !principals.some((v) => v.id === principal) && (
                      <option value={principal}>
                        Agent unavailable · {principal}
                      </option>
                    )}
                  {principals.map((v) => (
                    <option value={v.id} key={v.id}>
                      {v.displayName}
                      {principals.filter((p) => p.displayName === v.displayName)
                        .length > 1
                        ? ` · ${v.id}`
                        : ""}
                    </option>
                  ))}
                </select>
              )
            }
          </FormField>
          <FormField id="http-grant-description" label="Description (optional)">
            {(a) => (
              <input
                {...a}
                ref={descriptionInput}
                maxLength={256}
                value={description}
                onInput={(e) => setDescription(e.currentTarget.value)}
              />
            )}
          </FormField>
        </div>
        <div
          class="form-section"
          role="group"
          aria-label="Destination and matching"
        >
          <FormField id="http-grant-type" label="Grant type">
            {(a) => (
              <select
                {...a}
                value={kind}
                onChange={(e) => setKind(e.currentTarget.value as Kind)}
              >
                {Object.entries(labels).map(([value, label]) => (
                  <option value={value}>{label}</option>
                ))}
              </select>
            )}
          </FormField>
          {kind === "allow_tunnel" && (
            <StateNotice state="warning" title="Opaque tunnel bypass">
              <p>
                Allows encrypted traffic without request method/path checks or
                credential injection. Request blocks do not apply inside this
                tunnel.
              </p>
            </StateNotice>
          )}
          {requests && (
            <FormField id="http-grant-scheme" label="Scheme">
              {(a) => (
                <select
                  {...a}
                  value={scheme}
                  onChange={(e) => {
                    setScheme(e.currentTarget.value as "https" | "http");
                    setCredential("");
                  }}
                >
                  <option value="https">HTTPS</option>
                  <option value="http">HTTP</option>
                </select>
              )}
            </FormField>
          )}
          <FormField
            id="http-grant-host"
            label="Destination host"
            hint="Exact host, or *.example.com for every subdomain excluding the apex."
          >
            {(a) => (
              <input
                {...a}
                required
                maxLength={255}
                value={host}
                onInput={(e) => setHost(e.currentTarget.value)}
              />
            )}
          </FormField>
          <FormField id="http-grant-port" label="Port">
            {(a) => (
              <input
                {...a}
                required
                type="number"
                min={1}
                max={65535}
                value={port}
                onInput={(e) => setPort(e.currentTarget.value)}
              />
            )}
          </FormField>
          {requests && (
            <>
              <fieldset class="collection-field">
                <legend>Methods</legend>
                <p>
                  {methods.length === 0
                    ? "Any method"
                    : "Only the listed methods"}
                </p>
                {methods.map((method, index) => (
                  <div class="form-actions">
                    <input
                      aria-label={`Method ${index + 1}`}
                      required
                      maxLength={32}
                      pattern={"[A-Z0-9!#$%&'*+.^_`\\|~\\-]+"}
                      value={method}
                      onInput={(e) =>
                        setMethods(
                          methods.map((v, i) =>
                            i === index ? e.currentTarget.value : v,
                          ),
                        )
                      }
                    />
                    <button
                      type="button"
                      onClick={() => {
                        setDirty(true);
                        setMethods(methods.filter((_, i) => i !== index));
                      }}
                    >
                      Remove method
                    </button>
                  </div>
                ))}
                <button
                  type="button"
                  disabled={methods.length >= 32}
                  onClick={() => {
                    setDirty(true);
                    setMethods([...methods, "GET"]);
                  }}
                >
                  Add method
                </button>
              </fieldset>
              <FormField id="http-grant-path-kind" label="Path match">
                {(a) => (
                  <select
                    {...a}
                    value={pathKind}
                    onChange={(e) =>
                      setPathKind(e.currentTarget.value as typeof pathKind)
                    }
                  >
                    <option value="any">Any path</option>
                    <option value="exact">Exact path</option>
                    <option value="segment_prefix">Path and descendants</option>
                  </select>
                )}
              </FormField>
              {pathKind !== "any" && (
                <FormField
                  id="http-grant-path"
                  label="Path"
                  hint="Slash, literal @ and unreserved characters only; query, header and body matching are not supported."
                >
                  {(a) => (
                    <input
                      {...a}
                      required
                      maxLength={4096}
                      value={path}
                      onInput={(e) => setPath(e.currentTarget.value)}
                    />
                  )}
                </FormField>
              )}
            </>
          )}
        </div>
        <div
          class="form-section"
          role="group"
          aria-label="Permissions and expiry"
        >
          {kind === "allow_requests" && (
            <FormField
              id="http-grant-credential"
              label="Credential (optional)"
              hint={
                credential ||
                "Only HTTPS credentials covering this entire destination are eligible."
              }
            >
              {(a) => (
                <select
                  {...a}
                  value={credential}
                  onChange={(e) => setCredential(e.currentTarget.value)}
                >
                  <option value="">No credential</option>
                  {credential !== "" &&
                    !compatible.some((c) => c.id === credential) && (
                      <option value={credential}>
                        Unavailable credential · {credential}
                      </option>
                    )}
                  {compatible.map((c) => (
                    <option value={c.id}>
                      {c.name}
                      {credentials.filter((item) => item.name === c.name)
                        .length > 1
                        ? ` · ${c.id}`
                        : ""}
                      {c.available ? "" : " — unavailable"}
                    </option>
                  ))}
                </select>
              )}
            </FormField>
          )}
          {allow && (
            <label class="checkbox-field" for="http-grant-private">
              <input
                id="http-grant-private"
                type="checkbox"
                checked={privateAccess}
                aria-describedby="http-grant-private-hint"
                onChange={(e) => setPrivate(e.currentTarget.checked)}
              />
              <span>
                <strong>Allow local/private destinations</strong>
                <small id="http-grant-private-hint">
                  Permits loopback and private networks within this grant.
                  Metadata, link-local and Gateway endpoints remain forbidden.
                </small>
              </span>
            </label>
          )}
          <FormField id="http-grant-expiry" label="Expires (optional)">
            {(a) => (
              <input
                {...a}
                type="datetime-local"
                value={expiry}
                onInput={(e) => setExpiry(e.currentTarget.value)}
              />
            )}
          </FormField>
        </div>
        {error && (
          <StateNotice state="error" title="Check the proposed policy">
            <p>
              Use a valid agent, destination, future expiry and compatible
              credential.
            </p>
          </StateNotice>
        )}
        <MutationNotice state={mutation.state} />
        <div class="form-actions">
          <button
            ref={button}
            class={g === undefined ? "create-action" : "primary-action"}
            disabled={
              mutation.blocked ||
              stale ||
              loadError ||
              loading ||
              props.unavailable
            }
          >
            {mutation.state.state === "submitting"
              ? "Applying grant…"
              : g === undefined
                ? "Review and create"
                : "Review changes"}
          </button>
          {g !== undefined && (
            <>
              <button
                ref={cancelButton}
                type="button"
                disabled={mutation.blocked}
                onClick={() => (dirty ? setDiscard(true) : props.onCancel?.())}
              >
                Cancel
              </button>
            </>
          )}
        </div>
      </form>
      {g !== undefined && (
        <section class="detail-group" aria-label="Delete grant">
          <h3>Delete grant</h3>
          <button
            ref={deleteButton}
            type="button"
            class="danger-action"
            disabled={mutation.blocked || stale || props.unavailable}
            onClick={() => {
              confirmationFocus.current = deleteButton.current;
              setConfirm("delete");
            }}
          >
            Delete grant
          </button>
        </section>
      )}
      <ConfirmationDialog
        id="http-grant-discard"
        open={discard}
        title="Discard grant changes?"
        consequence="Your unsaved changes will be lost."
        confirmLabel="Discard changes"
        returnFocus={cancelButton as unknown as RefObject<HTMLElement>}
        onCancel={() => setDiscard(false)}
        onConfirm={() => props.onCancel?.()}
      />
      <ConfirmationDialog
        id="http-grant-confirm"
        open={confirm !== undefined}
        title={confirm === "delete" ? "Delete HTTP grant" : "Apply HTTP grant"}
        consequence={
          confirm === "delete" ? (
            "Remove this grant. Other grants and the agent default still apply."
          ) : (
            <>
              {g !== undefined ? (
                <GrantComparison
                  current={g}
                  proposed={{
                    description,
                    principal_id: principal,
                    policy,
                    expires_at:
                      expiry === "" ? null : new Date(expiry).toISOString(),
                  }}
                  principals={principals}
                />
              ) : (
                <section aria-label="Proposed grant">
                  <h3>Proposed grant</h3>
                  <dl class="detail-facts">
                    <div>
                      <dt>Description</dt>
                      <dd>{description || "Unnamed HTTP grant"}</dd>
                    </div>
                    <div>
                      <dt>Agent</dt>
                      <dd>
                        {principals.find((p) => p.id === principal)
                          ?.displayName ?? "Agent"}{" "}
                        · {principal}
                      </dd>
                    </div>
                    <PolicyFacts policy={policy} />
                    <div>
                      <dt>Expires</dt>
                      <dd>
                        <UserTime
                          value={
                            expiry === ""
                              ? null
                              : new Date(expiry).toISOString()
                          }
                          fallback="No expiry"
                        />
                      </dd>
                    </div>
                  </dl>
                </section>
              )}
              {policy.type === "allow_tunnel" && (
                <p>
                  Opaque tunnel bypass: request method/path checks and
                  credential injection do not apply inside this tunnel.
                </p>
              )}
              {policy.allow_private && (
                <p>Permits local/private destinations within this grant.</p>
              )}
              {policy.credential_id !== undefined && (
                <p>
                  The selected credential will be injected into matching HTTPS
                  requests.
                </p>
              )}
            </>
          )
        }
        confirmLabel={confirm === "delete" ? "Delete grant" : "Apply grant"}
        destructive={confirm === "delete"}
        returnFocus={confirmationFocus as unknown as RefObject<HTMLElement>}
        onCancel={() => setConfirm(undefined)}
        onConfirm={submit}
      />
    </section>
  );
}

function AccessPreview(props: Props) {
  const [principals, setPrincipals] = useState<Principal[]>([]);
  const [principal, setPrincipal] = useState("");
  const [connect, setConnect] = useState(false);
  const [url, setURL] = useState("");
  const [method, setMethod] = useState("GET");
  const [customMethod, setCustomMethod] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState("443");
  const [result, setResult] = useState<ReturnType<typeof decodePreview>>();
  const [error, setError] = useState(false);
  const [pending, setPending] = useState(false);
  const version = useRef(0);
  useEffect(() => {
    let active = true;
    void readPrincipals(props.session)
      .then((p) => {
        if (active) setPrincipals(p);
      })
      .catch(() => {
        if (active) setError(true);
      });
    return () => {
      active = false;
      version.current++;
    };
  }, []);
  const changed = () => {
    version.current++;
    setResult(undefined);
  };
  return (
    <section class="panel domain-panel">
      <p>
        Policy only. No request is sent. Network, TLS and secret material are
        unverified; this result is not future admission authority.
      </p>
      <form
        onInput={changed}
        onSubmit={(event) => {
          event.preventDefault();
          const selected = ++version.current;
          setPending(true);
          setError(false);
          setResult(undefined);
          void props.session
            .runProtected(async (context) => {
              const body = JSON.stringify({
                principal_id: principal,
                ...(connect
                  ? { connect: { host, port: Number(port) } }
                  : {
                      url,
                      method: method === "custom" ? customMethod : method,
                    }),
              });
              const response = await fetch("/api/v2/http/access-preview", {
                method: "POST",
                credentials: "same-origin",
                redirect: "error",
                signal: context.signal,
                headers: {
                  "Content-Type": "application/json",
                  "X-CSRF-Token": context.csrfToken,
                },
                body,
              });
              if (await context.sessionLost(response)) return;
              if (!response.ok) throw new Error("Preview unavailable.");
              const raw = decodePreview(
                await boundedJSON(response),
                principal,
                connect,
              );
              if (version.current === selected) setResult(raw);
            })
            .catch(() => {
              if (version.current === selected) setError(true);
            })
            .finally(() => setPending(false));
        }}
      >
        <FormField
          id="preview-principal"
          label="Agent"
          {...(principal ? { hint: principal } : {})}
        >
          {(a) => (
            <select
              {...a}
              required
              value={principal}
              onChange={(e) => setPrincipal(e.currentTarget.value)}
            >
              <option value="">Select agent</option>
              {principals.map((p) => (
                <option value={p.id}>
                  {p.displayName}
                  {principals.filter(
                    (item) => item.displayName === p.displayName,
                  ).length > 1
                    ? ` · ${p.id}`
                    : ""}
                </option>
              ))}
            </select>
          )}
        </FormField>
        <FormField id="preview-kind" label="Access type">
          {(a) => (
            <select
              {...a}
              value={connect ? "connect" : "request"}
              onChange={(e) => setConnect(e.currentTarget.value === "connect")}
            >
              <option value="request">HTTP request</option>
              <option value="connect">CONNECT destination</option>
            </select>
          )}
        </FormField>
        {connect ? (
          <>
            <FormField id="preview-host" label="Host">
              {(a) => (
                <input
                  {...a}
                  required
                  maxLength={253}
                  value={host}
                  onInput={(e) => setHost(e.currentTarget.value)}
                />
              )}
            </FormField>
            <FormField id="preview-port" label="Port">
              {(a) => (
                <input
                  {...a}
                  required
                  type="number"
                  min={1}
                  max={65535}
                  value={port}
                  onInput={(e) => setPort(e.currentTarget.value)}
                />
              )}
            </FormField>
          </>
        ) : (
          <>
            <FormField id="preview-url" label="URL">
              {(a) => (
                <input
                  {...a}
                  required
                  type="url"
                  maxLength={8192}
                  value={url}
                  onInput={(e) => setURL(e.currentTarget.value)}
                />
              )}
            </FormField>
            <FormField id="preview-method" label="Method">
              {(a) => (
                <select
                  {...a}
                  value={method}
                  onChange={(e) => setMethod(e.currentTarget.value)}
                >
                  {[
                    "GET",
                    "HEAD",
                    "POST",
                    "PUT",
                    "PATCH",
                    "DELETE",
                    "OPTIONS",
                    "TRACE",
                  ].map((value) => (
                    <option value={value}>{value}</option>
                  ))}
                  <option value="custom">Custom</option>
                </select>
              )}
            </FormField>
            {method === "custom" && (
              <FormField
                id="preview-custom-method"
                label="Custom method"
                hint="Use an uppercase HTTP method token. For CONNECT, select CONNECT destination above."
              >
                {(a) => (
                  <input
                    {...a}
                    required
                    maxLength={32}
                    pattern={"(?!CONNECT$)[A-Z0-9!#$%&'*+.^_`\\|~\\-]+"}
                    value={customMethod}
                    onInput={(e) => setCustomMethod(e.currentTarget.value)}
                  />
                )}
              </FormField>
            )}
          </>
        )}
        <button class="form-submit-action" disabled={pending}>
          Test access
        </button>
      </form>
      {error && (
        <StateNotice state="error" title="Access preview unavailable">
          <p>Check the coordinates and current agent state.</p>
        </StateNotice>
      )}
      {result !== undefined && <PreviewResult result={result} />}
    </section>
  );
}
function PreviewResult({
  result,
}: {
  result: ReturnType<typeof decodePreview>;
}) {
  const d = object(result.decision);
  const reasons: Record<string, string> = {
    destination_block: "Blocked by a destination grant",
    request_block: "Blocked by a request grant",
    request_allow: "Allowed by request policy",
    principal_default: `Agent default: ${httpDefaultText(result.default)}`,
    tunnel_allow:
      "Opaque tunnel allowed; request policy and injection bypassed",
    intercept_required:
      "Interception required; each decrypted request needs its own evaluation",
    credential_conflict:
      "Blocked: matching grants require different credentials",
    credential_unavailable:
      "Blocked: selected credential has no available material binding",
    address_forbidden: "Blocked: destination address is forbidden",
    private_permission_required:
      "Blocked: a matching local/private allow is required",
  };
  return (
    <section class="detail-group" aria-live="polite">
      <h3>{reasons[String(d.reason)] ?? "Policy result unavailable"}</h3>
      <dl class="detail-facts">
        <div>
          <dt>Policy revision</dt>
          <dd>{String(d.policy_revision)}</dd>
        </div>
        <div>
          <dt>HTTP default</dt>
          <dd>{httpDefaultText(result.default)}</dd>
        </div>
        <div>
          <dt>Default revision</dt>
          <dd>{String(d.default_revision)}</dd>
        </div>
        <div>
          <dt>Transport</dt>
          <dd>{String(d.transport)}</dd>
        </div>
        <div>
          <dt>Local/private permission</dt>
          <dd>
            {d.private_grant === undefined
              ? "Explicit matching allow required"
              : "Matching permission present"}
          </dd>
        </div>
        <div>
          <dt>Agent</dt>
          <dd>
            <a href={`#/agents/${String(object(d.principal).id)}`}>
              {String(object(d.principal).id)}
            </a>{" "}
            · revision {String(object(d.principal).revision)}
          </dd>
        </div>
        {[
          "grant",
          "private_grant",
          "credential",
          "credential_grant",
          "conflict_credential",
          "conflict_grant",
        ].map((key) => {
          if (d[key] === undefined) return null;
          const ref = object(d[key]);
          if (typeof ref.id !== "string" || !idPattern.test(ref.id))
            return null;
          return (
            <div>
              <dt>
                {
                  (
                    {
                      grant: "Deciding grant",
                      private_grant: "Local/private grant",
                      credential: "Credential",
                      credential_grant: "Credential grant",
                      conflict_credential: "Conflicting credential",
                      conflict_grant: "Conflicting grant",
                    } as Record<string, string>
                  )[key]
                }
              </dt>
              <dd>
                <a
                  href={`#/http/${key.includes("credential") && !key.includes("grant") ? "credentials" : "grants"}/${ref.id}`}
                >
                  {ref.id}
                </a>{" "}
                · revision {String(ref.revision)}
              </dd>
            </div>
          );
        })}
      </dl>
    </section>
  );
}
