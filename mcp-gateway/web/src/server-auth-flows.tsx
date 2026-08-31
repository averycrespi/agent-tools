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

function FlowRows({
  serverID,
  items,
}: {
  serverID: string;
  items: readonly ServerAuthFlowView[];
}) {
  return (
    <CollectionTable
      caption="OAuth activity"
      items={items}
      rowKey={(flow) => flow.id}
      rowTestID="auth-flow-row"
      filterLabel="Filter OAuth activity"
      filterValue={(flow) => `${words(flow.state)} ${flow.reason ?? ""}`}
      columns={[
        {
          key: "action",
          label: "Action",
          render: (flow) => (
            <a href={`#/servers/${serverID}/auth-flows/${flow.id}`}>
              OAuth authorization
            </a>
          ),
        },
        {
          key: "status",
          label: "Status",
          sortValue: (flow) => flow.state,
          render: (flow) => (
            <StatusLabel
              state={
                flow.state === "failed" || flow.state === "interrupted"
                  ? "warning"
                  : authFlowIsTerminal(flow)
                    ? "current"
                    : "loading"
              }
            >
              {words(flow.state)}
            </StatusLabel>
          ),
        },
        {
          key: "started",
          label: "Started",
          sortValue: (flow) => flow.createdAt,
          render: (flow) => (
            <time dateTime={flow.createdAt}>{flow.createdAt}</time>
          ),
        },
        {
          key: "outcome",
          label: "Outcome",
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
      if (sink.publish(outcome.value.authorizationURL) === "lost") {
        setNotice(
          "The authorization URL could not be displayed. Start a new flow from current state.",
        );
      } else {
        setNotice(`Flow ${outcome.value.flow.id} is awaiting authorization.`);
      }
      controller.abandon();
      return;
    }
    if (outcome.kind === "uncertain") {
      sink.lose();
      setNotice(
        "The flow start outcome is unknown. Inspect current flow history and start a new flow only from refreshed state.",
      );
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
      route: `/api/v1/servers/${server.id}/auth-flows`,
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

  return (
    <section class="panel domain-panel" aria-labelledby="auth-flow-start-title">
      <div class="panel-heading">
        <div>
          <span class="panel-code">ONE-TIME AUTHORIZATION</span>
          <h2 id="auth-flow-start-title">Authorize with OAuth</h2>
        </div>
      </div>
      <p>
        The authorization URL appears once in an inert dialog. Open it only with
        the explicit button; dismissal, navigation, or session loss clears it.
      </p>
      <p class="bounded-note">
        Callback and external authorization authority remain server-owned. A
        lost URL cannot be recovered or replayed; start a new flow from current
        state.
      </p>
      {eligible(server) && !exchangeActive ? (
        <button
          type="button"
          class="primary-action"
          data-testid="start-auth-flow"
          disabled={disabled}
          onClick={start}
        >
          Start OAuth flow
        </button>
      ) : (
        <StateNotice state="empty" title="Foreground OAuth is not eligible">
          <p>
            {exchangeActive
              ? "An OAuth exchange is already active. Refresh its authoritative state before starting another flow."
              : "This server is not a current Streamable HTTP OAuth configuration."}
          </p>
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
            Inspect authoritative flow history. This start cannot be replayed.
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
      route: `/api/v1/servers/${flow.serverID}/auth-flows/${flow.id}`,
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
          <span class="panel-code">ACTIVE FLOW</span>
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
}) {
  if (flow !== undefined)
    return (
      <>
        <section
          class="panel domain-panel"
          aria-labelledby="auth-flow-detail-title"
          data-testid="auth-flow-detail"
        >
          <div class="panel-heading">
            <div>
              <h2 id="auth-flow-detail-title">OAuth authorization</h2>
              <span class="table-secondary">Correlation {flow.id}</span>
            </div>
            <StatusLabel
              state={
                authFlowIsTerminal(flow)
                  ? flow.state === "failed" || flow.state === "interrupted"
                    ? "warning"
                    : "current"
                  : "loading"
              }
            >
              {words(flow.state)}
            </StatusLabel>
          </div>
          <p>
            <a href={`#/servers/${server.id}?tab=activity`}>OAuth activity</a> ·{" "}
            <a href={`#/servers/${server.id}`}>Overview</a>
          </p>
          <dl class="detail-list">
            <div>
              <dt>Created</dt>
              <dd>{flow.createdAt}</dd>
            </div>
            <div>
              <dt>Expires</dt>
              <dd>{flow.expiresAt}</dd>
            </div>
            <div>
              <dt>Finished</dt>
              <dd>{flow.finishedAt ?? "In progress"}</dd>
            </div>
            <div>
              <dt>Outcome</dt>
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
          {!authFlowIsTerminal(flow) && (
            <p class="bounded-note">
              This nonterminal flow polls every two seconds while visible.
              Events trigger authoritative reads and never prove progress or
              completion.
            </p>
          )}
        </section>
        <CancelFlow mutations={mutations} flow={flow} onRefresh={onRefresh} />
        <StartFlow
          mutations={mutations}
          sinks={sinks}
          server={server}
          etag={etag}
          readVersion={readVersion}
          exchangeActive={flow.state === "exchanging"}
        />
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
        {flows.length === 0 ? (
          <StateNotice state="empty" title="No retained OAuth flows" />
        ) : (
          <FlowRows serverID={server.id} items={flows} />
        )}
        {nextCursor !== null && (
          <button type="button" disabled={loadingMore} onClick={onLoadMore}>
            {loadingMore ? "Loading…" : "Load more flows"}
          </button>
        )}
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
