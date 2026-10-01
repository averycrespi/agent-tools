import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type {
  MutationController,
  MutationCoordinator,
  MutationOutcome,
  MutationSnapshot,
} from "./mutation";
import {
  CollectionTable,
  TableIdentity,
  type OperationalState,
  ConfirmationDialog,
  StateNotice,
  StatusLabel,
} from "./primitives";
import {
  authFlowCanCancel,
  authFlowIsTerminal,
  decodeAuthFlowCreation,
  type AuthFlowCreation,
  type ServerAuthFlowView,
} from "./server-auth-flow-model";
import type { ServerView } from "./server-reads";
import type { PreparedOAuthSink, SensitiveSinkCoordinator } from "./sinks";
import { UserTime } from "./time";

function eligible(server: ServerView): boolean {
  if (server.desiredState === "deleted") return false;
  if (typeof server.transport !== "object" || server.transport === null)
    return false;
  const transport = server.transport as Record<string, unknown>;
  if (transport.kind !== "streamable_http") return false;
  const authentication = transport.authentication;
  return (
    typeof authentication === "object" &&
    authentication !== null &&
    !Array.isArray(authentication) &&
    (authentication as Record<string, unknown>).mode === "oauth"
  );
}

function words(value: string): string {
  const result = value.replaceAll("_", " ");
  if (result.startsWith("oauth")) return `OAuth${result.slice(5)}`;
  return result.charAt(0).toLocaleUpperCase() + result.slice(1);
}

function flowState(flow: ServerAuthFlowView): OperationalState {
  if (!authFlowIsTerminal(flow)) return "loading";
  if (flow.state === "succeeded") return "current";
  if (flow.state === "failed") return "error";
  return flow.state === "interrupted" ? "warning" : "neutral";
}

function FlowRows({
  serverID,
  items,
  hasMore,
  loadingMore,
  onLoadMore,
  stale,
  loading,
}: {
  stale: boolean;
  loading: boolean;
  serverID: string;
  items: readonly ServerAuthFlowView[];
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}) {
  return (
    <CollectionTable
      caption="OAuth activity"
      layout="activity"
      rowHeaderKey="action"
      loadedSubset
      historySummary
      localStale={stale}
      localLoading={loading}
      itemNames={{ singular: "flow", plural: "flows" }}
      emptyTitle="No retained OAuth flows"
      hasMore={hasMore}
      loadingMore={loadingMore}
      onLoadMore={onLoadMore}
      loadMoreLabel="Load more flows"
      items={items}
      rowKey={(flow) => flow.id}
      rowTestID="auth-flow-row"
      filters={[
        {
          key: "status",
          label: "Status",
          type: "select",
          value: (flow) => flow.state,
          options: [
            { value: "preparing", label: "Preparing" },
            { value: "awaiting_callback", label: "Awaiting callback" },
            { value: "exchanging", label: "Exchanging" },
            { value: "succeeded", label: "Succeeded" },
            { value: "failed", label: "Failed" },
            { value: "cancelled", label: "Cancelled" },
            { value: "interrupted", label: "Interrupted" },
            { value: "expired", label: "Expired" },
            { value: "superseded", label: "Superseded" },
          ],
        },
      ]}
      initialSort={{ key: "created", direction: "descending" }}
      columns={[
        {
          key: "created",
          label: "Created",
          role: "time",
          sortValue: (flow) => flow.createdAt,
          render: (flow) => <UserTime value={flow.createdAt} />,
        },
        {
          key: "action",
          label: "Flow",
          role: "identity",
          render: (flow) => (
            <TableIdentity
              primary={
                <a href={`#/mcp/servers/${serverID}/auth-flows/${flow.id}`}>
                  OAuth authorization
                </a>
              }
              secondary={flow.id}
            />
          ),
        },
        {
          key: "status",
          label: "Status",
          role: "status",
          sortValue: (flow) => flow.state,
          render: (flow) => (
            <StatusLabel state={flowState(flow)}>
              {words(flow.state)}
            </StatusLabel>
          ),
        },
        {
          key: "outcome",
          label: "Reason",
          role: "text",
          sortValue: (flow) => flow.reason ?? "",
          render: (flow) => (flow.reason === null ? "—" : words(flow.reason)),
        },
      ]}
    />
  );
}

function StartFlow({
  mutations,
  sinks,
  server,
  etag,
  readVersion,
  exchangeActive,
}: {
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  server: ServerView;
  etag: string;
  readVersion: number;
  exchangeActive: boolean;
}) {
  const [controller] = useState<MutationController<AuthFlowCreation>>(() =>
    mutations.create<AuthFlowCreation>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [notice, setNotice] = useState<string>();
  const [blockedReadVersion, setBlockedReadVersion] = useState<number>();
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);

  const settle = async (
    submission: Promise<MutationOutcome<AuthFlowCreation>>,
    sink: PreparedOAuthSink,
  ) => {
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      if (sink.publish(outcome.value.authorizationURL) === "lost")
        setNotice(
          "The authorization URL could not be displayed. Start a new flow from current state.",
        );
      controller.abandon();
      return;
    }
    if (outcome.kind === "uncertain") {
      sink.lose();
      return;
    }
    sink.cancel();
    if (outcome.kind === "rejected" && outcome.requiresRefresh)
      setBlockedReadVersion(readVersion);
  };

  const start = () => {
    setNotice(undefined);
    const sink = sinks.prepareOAuth("Authorize server OAuth flow");
    if (sink === undefined) {
      setNotice(
        "The one-time authorization URL display is unavailable. No flow was started.",
      );
      return;
    }
    controller.begin({
      route: `/api/v2/mcp/servers/${server.id}/oauth-flows`,
      method: "POST",
      body: "{}",
      precondition: etag,
      requiresPrecondition: true,
      idempotency: "none",
      successStatuses: [201],
      decode: async (response) => {
        const creation = await decodeAuthFlowCreation(response);
        if (
          creation.flow.serverID !== server.id ||
          creation.flow.state !== "awaiting_callback" ||
          creation.flow.targetDesiredRevision !== server.desiredRevision ||
          creation.flow.registrationRevision !== server.oauthClientRevision ||
          creation.flow.finishedAt !== null ||
          creation.flow.reason !== null
        )
          throw new Error("invalid auth flow creation response");
        return creation;
      },
    });
    void settle(controller.submit(), sink);
  };
  const waitingForRead =
    blockedReadVersion !== undefined && readVersion <= blockedReadVersion;
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched" ||
    waitingForRead;
  const replacingAuthority = server.credentialState === "ready";
  const authentication = (server.transport as Record<string, unknown> | null)
    ?.authentication as Record<string, unknown> | undefined;
  const registration = authentication?.registration as
    | Record<string, unknown>
    | undefined;
  const clientSecretMissing =
    registration?.mode === "static" &&
    (registration.token_endpoint_auth_method === "client_secret_basic" ||
      registration.token_endpoint_auth_method === "client_secret_post") &&
    server.oauthClientRevision === "0";

  return (
    <section class="panel domain-panel" aria-labelledby="auth-flow-start-title">
      <div class="panel-heading">
        <div>
          <h2 id="auth-flow-start-title">OAuth authorization</h2>
        </div>
      </div>
      <p>
        Continue authorization in a new browser page. The one-time URL is
        cleared when it is dismissed, opened, or you leave this page.
      </p>
      {eligible(server) && !exchangeActive && (
        <p>
          Starting again invalidates any previous pending authorization link.
        </p>
      )}
      {clientSecretMissing && (
        <p class="bounded-note" id="oauth-client-prerequisite">
          Add the client secret above before authorizing.
        </p>
      )}
      {eligible(server) && !exchangeActive ? (
        <button
          type="button"
          class={replacingAuthority ? undefined : "primary-action"}
          data-testid="start-auth-flow"
          disabled={disabled || clientSecretMissing}
          aria-describedby={
            clientSecretMissing ? "oauth-client-prerequisite" : undefined
          }
          onClick={start}
        >
          {replacingAuthority ? "Reauthorize server" : "Authorize server"}
        </button>
      ) : (
        <StateNotice
          state={exchangeActive ? "loading" : "empty"}
          title={
            exchangeActive
              ? "Authorization in progress"
              : "OAuth is unavailable"
          }
        >
          {!exchangeActive && <p>This server is not configured for OAuth.</p>}
        </StateNotice>
      )}
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title}>
          {mutation.requiresRefresh && (
            <p>
              A fresh server and flow snapshot was requested. Review it before
              starting a new flow.
            </p>
          )}
        </StateNotice>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Flow start outcome unknown">
          <p>
            Inspect refreshed flow history before starting again. This start
            cannot be replayed.
          </p>
        </StateNotice>
      )}
      {waitingForRead && (
        <p class="session-message" role="status">
          Waiting for a newer authoritative server snapshot.
        </p>
      )}
      {notice !== undefined && (
        <p class="session-message" role="status">
          {notice}
        </p>
      )}
    </section>
  );
}

function CancelFlow({
  mutations,
  flow,
  onRefresh,
}: {
  mutations: MutationCoordinator;
  flow: ServerAuthFlowView;
  onRefresh: () => void;
}) {
  const [controller] = useState<MutationController<undefined>>(() =>
    mutations.create<undefined>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);

  const settle = async (submission: Promise<MutationOutcome<undefined>>) => {
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      controller.abandon();
      onRefresh();
    }
  };
  const review = () => {
    controller.begin({
      route: `/api/v2/mcp/servers/${flow.serverID}/oauth-flows/${flow.id}`,
      method: "DELETE",
      body: "{}",
      precondition: null,
      requiresPrecondition: false,
      idempotency: "none",
      successStatuses: [204],
      decode: async () => undefined,
    });
    controller.confirm();
  };

  if (!authFlowCanCancel(flow)) return null;
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="auth-flow-cancel-title"
    >
      <div class="panel-heading">
        <div>
          <h2 id="auth-flow-cancel-title">Cancel this OAuth flow</h2>
        </div>
      </div>
      <button
        ref={trigger}
        type="button"
        data-testid="cancel-auth-flow"
        disabled={
          mutation.state === "submitting" ||
          mutation.availability === "storage_latched"
        }
        onClick={review}
      >
        Review cancellation
      </button>
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title} />
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Cancellation outcome unknown">
          <p>
            Refresh this flow. Do not infer cancellation or replay the request.
          </p>
        </StateNotice>
      )}
      <ConfirmationDialog
        id="auth-flow-cancel-confirm"
        open={mutation.state === "confirming"}
        title="Cancel this OAuth flow?"
        consequence="Cancellation invalidates the current callback state. An authorization page already opened may no longer complete, while an exchange already in progress cannot be cancelled."
        confirmLabel="Cancel OAuth flow"
        returnFocus={trigger as unknown as RefObject<HTMLElement>}
        onCancel={() => controller.abandon()}
        onConfirm={() => void settle(controller.submit())}
      />
    </section>
  );
}

export function ServerAuthFlows({
  mutations,
  sinks,
  server,
  etag,
  readVersion,
  flows,
  flow,
  nextCursor,
  loadingMore,
  restarted,
  onLoadMore,
  onRefresh,
  mode = "full",
  stale = false,
  loading = false,
}: {
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  server: ServerView;
  etag: string;
  readVersion: number;
  flows: readonly ServerAuthFlowView[];
  flow: ServerAuthFlowView | undefined;
  nextCursor: string | null;
  loadingMore: boolean;
  restarted: boolean;
  onLoadMore: () => void;
  onRefresh: () => void;
  mode?: "full" | "history" | "action";
  stale?: boolean;
  loading?: boolean;
}) {
  if (flow !== undefined)
    return (
      <>
        <div data-testid="auth-flow-detail">
          <nav class="detail-navigation" aria-label="OAuth flow navigation">
            <a href={`#/mcp/servers/${server.id}?tab=authentication`}>
              Back to authentication
            </a>
          </nav>
          <header class="detail-context-heading">
            <div>
              <h2 id="auth-flow-detail-title">OAuth flow {flow.id}</h2>
              <span class="table-secondary">OAuth authorization</span>
            </div>
            <StatusLabel state={flowState(flow)}>
              {words(flow.state)}
            </StatusLabel>
          </header>
          <section
            class="detail-section"
            aria-labelledby="auth-flow-facts-title"
          >
            <h3 id="auth-flow-facts-title">Flow details</h3>
            {stale && (
              <StateNotice state="error" title="OAuth flow unavailable">
                These flow details are last-known. Refresh to retry the read.
              </StateNotice>
            )}
            <dl class="detail-list">
              <div>
                <dt>Created</dt>
                <dd>
                  <UserTime value={flow.createdAt} />
                </dd>
              </div>
              <div>
                <dt>Expires</dt>
                <dd>
                  <UserTime value={flow.expiresAt} />
                </dd>
              </div>
              <div>
                <dt>Finished</dt>
                <dd>
                  <UserTime value={flow.finishedAt} fallback="In progress" />
                </dd>
              </div>
              <div>
                <dt>Reason</dt>
                <dd>{flow.reason === null ? "—" : words(flow.reason)}</dd>
              </div>
            </dl>
            {flow.diagnostic !== null && (
              <details>
                <summary>Diagnostic details</summary>
                <dl class="detail-list">
                  <div>
                    <dt>Stage</dt>
                    <dd>{words(flow.diagnostic.stage)}</dd>
                  </div>
                  <div>
                    <dt>Reason</dt>
                    <dd>{words(flow.diagnostic.reason)}</dd>
                  </div>
                  <div>
                    <dt>HTTP status</dt>
                    <dd>{flow.diagnostic.httpStatus ?? "—"}</dd>
                  </div>
                  <div>
                    <dt>Correlation</dt>
                    <dd>{flow.diagnostic.correlationID}</dd>
                  </div>
                </dl>
              </details>
            )}
          </section>
        </div>
        <CancelFlow mutations={mutations} flow={flow} onRefresh={onRefresh} />
      </>
    );
  if (mode === "action")
    return eligible(server) ? (
      <StartFlow
        mutations={mutations}
        sinks={sinks}
        server={server}
        etag={etag}
        readVersion={readVersion}
        exchangeActive={flows.some((item) => item.state === "exchanging")}
      />
    ) : null;
  return (
    <>
      <section
        class="panel domain-panel"
        aria-labelledby="auth-flow-list-title"
        data-testid="auth-flow-list"
      >
        <div class="panel-heading">
          <div>
            <h2 id="auth-flow-list-title">OAuth activity</h2>
          </div>
        </div>
        {restarted && (
          <StateNotice state="stale" title="Flow history changed">
            <p>
              The stale traversal was discarded and restarted from an
              authoritative first page.
            </p>
          </StateNotice>
        )}
        {loading && (
          <StateNotice state="loading" title="Loading OAuth activity" />
        )}
        {stale && (
          <StateNotice state="error" title="OAuth activity unavailable">
            Loaded flows are last-known. Use Load more flows to retry
            continuation, or Refresh to restart.
          </StateNotice>
        )}
        <FlowRows
          stale={stale}
          loading={loading}
          serverID={server.id}
          items={flows}
          hasMore={nextCursor !== null}
          loadingMore={loadingMore}
          onLoadMore={onLoadMore}
        />
      </section>
      {mode === "full" && (
        <StartFlow
          mutations={mutations}
          sinks={sinks}
          server={server}
          etag={etag}
          readVersion={readVersion}
          exchangeActive={flows.some((item) => item.state === "exchanging")}
        />
      )}
    </>
  );
}
