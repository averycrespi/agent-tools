import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type {
  MutationController,
  MutationCoordinator,
  MutationOutcome,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import {
  CollectionTable,
  ConfirmationDialog,
  StateNotice,
  StatusLabel,
} from "./primitives";
import {
  decodeOperationMutation,
  operationIsTerminal,
  type ExplicitOperationKind,
  type OperationKind,
  type ServerOperationView,
} from "./server-operation-model";
import type { ServerView } from "./server-reads";
import { UserTime } from "./time";

function eligibleKinds(
  server: ServerView,
  activeOperation: ServerOperationView | undefined,
): ExplicitOperationKind[] {
  if (activeOperation !== undefined && !operationIsTerminal(activeOperation)) {
    return activeOperation.kind === "refresh_catalog" &&
      activeOperation.targetDesiredRevision === server.desiredRevision
      ? ["refresh_catalog"]
      : [];
  }
  const result: ExplicitOperationKind[] = [];
  if (
    server.desiredState === "enabled" &&
    (server.runtimeState === "inactive" || server.runtimeState === "active")
  )
    result.push("reload");
  if (
    (server.desiredState === "enabled" &&
      ["retry_wait", "degraded", "authentication_required"].includes(
        server.runtimeState,
      )) ||
    server.credentialState === "cleanup_pending" ||
    ((server.desiredState === "disabled" ||
      server.desiredState === "deleted") &&
      server.runtimeReason === "stop_unconfirmed")
  )
    result.push("retry");
  if (
    server.desiredState === "enabled" &&
    server.runtimeState === "active" &&
    (server.activeState === "current" || server.activeState === "stale")
  )
    result.push("refresh_catalog");
  if (
    server.desiredState !== "deleted" &&
    (server.staticRevision !== "0" ||
      server.oauthClientRevision !== "0" ||
      server.oauthTokensRevision !== "0")
  )
    result.push("disconnect_credentials");
  return result;
}
function label(kind: OperationKind): string {
  switch (kind) {
    case "activate":
      return "Connect server";
    case "reload":
      return "Reload server";
    case "retry":
      return "Retry connection";
    case "refresh_catalog":
      return "Refresh tools";
    case "credential_replace":
      return "Replace credential";
    case "disable":
      return "Disable server";
    case "delete":
      return "Delete server";
    case "disconnect_credentials":
      return "Disconnect credentials";
  }
}
function words(value: string): string {
  const result = value.replaceAll("_", " ");
  return result.charAt(0).toLocaleUpperCase() + result.slice(1);
}
function operationState(
  operation: ServerOperationView,
): "current" | "loading" | "warning" {
  if (!operationIsTerminal(operation)) return "loading";
  return operation.state === "failed" || operation.state === "interrupted"
    ? "warning"
    : "current";
}
function requiresConfirmation(kind: ExplicitOperationKind): boolean {
  return kind === "reload" || kind === "disconnect_credentials";
}
function consequence(kind: ExplicitOperationKind): string {
  return kind === "reload"
    ? "Reload withdraws current routing while the server restarts. Calls already handed downstream may have unknown outcomes."
    : "Disconnect invalidates local credential authority and withdraws affected routing. Remote revocation is best effort and is not guaranteed.";
}
function OperationRows({
  serverID,
  items,
}: {
  serverID: string;
  items: readonly ServerOperationView[];
}) {
  return (
    <CollectionTable
      caption="Server activity"
      items={items}
      rowKey={(operation) => operation.id}
      rowTestID="operation-row"
      filters={[
        {
          key: "action",
          label: "Action",
          type: "select",
          value: (operation) => operation.kind,
          options: [
            { value: "activate", label: "Connect server" },
            { value: "reload", label: "Reload server" },
            { value: "retry", label: "Retry connection" },
            { value: "refresh_catalog", label: "Refresh tools" },
            { value: "credential_replace", label: "Replace credential" },
            { value: "disable", label: "Disable server" },
            { value: "delete", label: "Delete server" },
            {
              value: "disconnect_credentials",
              label: "Disconnect credentials",
            },
          ],
        },
        {
          key: "status",
          label: "Status",
          type: "select",
          value: (operation) => operation.state,
          options: [
            { value: "scheduled", label: "Scheduled" },
            { value: "running", label: "Running" },
            { value: "succeeded", label: "Succeeded" },
            { value: "failed", label: "Failed" },
            { value: "interrupted", label: "Interrupted" },
            { value: "cancelled", label: "Cancelled" },
            { value: "superseded", label: "Superseded" },
          ],
        },
      ]}
      initialSort={{ key: "started", direction: "descending" }}
      columns={[
        {
          key: "action",
          label: "Action",
          sortValue: (operation) => label(operation.kind),
          render: (operation) => (
            <a href={`#/servers/${serverID}/operations/${operation.id}`}>
              {label(operation.kind)}
            </a>
          ),
        },
        {
          key: "status",
          label: "Status",
          sortValue: (operation) => operation.state,
          render: (operation) => (
            <StatusLabel state={operationState(operation)}>
              {words(operation.state)}
            </StatusLabel>
          ),
        },
        {
          key: "started",
          label: "Started",
          sortValue: (operation) => operation.startedAt ?? operation.createdAt,
          render: (operation) => (
            <UserTime value={operation.startedAt ?? operation.createdAt} />
          ),
        },
        {
          key: "outcome",
          label: "Outcome",
          sortValue: (operation) => operation.reason ?? "",
          render: (operation) =>
            operation.reason === null ? "—" : words(operation.reason),
        },
      ]}
    />
  );
}

function OperationStarter({
  mutations,
  server,
  etag,
  readVersion,
  activeOperation,
}: {
  mutations: MutationCoordinator;
  server: ServerView;
  etag: string;
  readVersion: number;
  activeOperation: ServerOperationView | undefined;
}) {
  const [controller] = useState<MutationController<ServerOperationView>>(() =>
    mutations.create<ServerOperationView>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [selected, setSelected] = useState<ExplicitOperationKind>();
  const [blockedReadVersion, setBlockedReadVersion] = useState<number>();
  const [notice, setNotice] = useState<string>();
  const trigger = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);

  const spec = (
    kind: ExplicitOperationKind,
  ): MutationSpec<ServerOperationView> => ({
    route: `/api/v1/servers/${server.id}/operations`,
    method: "POST",
    body: JSON.stringify({ kind }),
    precondition: etag,
    requiresPrecondition: true,
    idempotency: "operation_start",
    successStatuses: [200, 202],
    decode: decodeOperationMutation,
  });
  const settle = async (
    submission: Promise<MutationOutcome<ServerOperationView>>,
  ) => {
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      setBlockedReadVersion(undefined);
      setNotice(`Operation ${outcome.value.id} was accepted.`);
      controller.abandon();
      window.location.hash = `#/servers/${server.id}/operations/${outcome.value.id}`;
    } else if (outcome.kind === "rejected" && outcome.requiresRefresh) {
      setBlockedReadVersion(readVersion);
    }
  };
  const start = (kind: ExplicitOperationKind, button: HTMLButtonElement) => {
    trigger.current = button;
    setSelected(kind);
    setNotice(undefined);
    controller.begin(spec(kind));
    if (requiresConfirmation(kind)) controller.confirm();
    else void settle(controller.submit());
  };
  const waitingForRead =
    blockedReadVersion !== undefined && readVersion <= blockedReadVersion;
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched" ||
    waitingForRead;
  const eligible = eligibleKinds(server, activeOperation);
  return (
    <section class="panel domain-panel" aria-labelledby="operation-start-title">
      <div class="panel-heading">
        <div>
          <h2 id="operation-start-title">Available actions</h2>
        </div>
      </div>
      {eligible.length === 0 ? (
        <StateNotice state="empty" title="No actions are currently available" />
      ) : (
        <div class="action-list">
          {eligible.map((kind) => (
            <button
              key={kind}
              class={
                kind === "disconnect_credentials"
                  ? "danger-action"
                  : "safe-action"
              }
              type="button"
              data-testid={`start-operation-${kind}`}
              disabled={disabled}
              onClick={(event) => start(kind, event.currentTarget)}
            >
              {label(kind)}
            </button>
          ))}
        </div>
      )}
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title}>
          {mutation.requiresRefresh && (
            <p>
              A fresh server and operation snapshot was requested. Review the
              selected operation before starting a new intent.
            </p>
          )}
        </StateNotice>
      )}
      {waitingForRead && (
        <p class="session-message" role="status">
          Waiting for a newer authoritative operation snapshot.
        </p>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Operation start outcome unknown">
          <p>
            Inspect operation history. The exact same start may be replayed
            explicitly only while this in-memory recovery tuple remains live.
          </p>
        </StateNotice>
      )}
      {mutation.canReplay && (
        <button
          type="button"
          data-testid="operation-start-replay"
          onClick={() => void settle(controller.replay())}
        >
          Replay this same operation start
        </button>
      )}
      {notice !== undefined && (
        <p class="session-message" role="status">
          {notice}
        </p>
      )}
      {selected !== undefined && (
        <ConfirmationDialog
          id="operation-start-confirm"
          open={mutation.state === "confirming"}
          title={`${label(selected)}?`}
          consequence={consequence(selected)}
          confirmLabel={label(selected)}
          destructive={selected === "disconnect_credentials"}
          returnFocus={trigger as unknown as RefObject<HTMLElement>}
          onCancel={() => controller.abandon()}
          onConfirm={() => void settle(controller.submit())}
        />
      )}
    </section>
  );
}

export function ServerOperations({
  mutations,
  server,
  etag,
  readVersion,
  operations,
  operation,
  nextCursor,
  loadingMore,
  restarted,
  onLoadMore,
}: {
  mutations: MutationCoordinator;
  server: ServerView;
  etag: string;
  readVersion: number;
  operations: readonly ServerOperationView[];
  operation: ServerOperationView | undefined;
  nextCursor: string | null;
  loadingMore: boolean;
  restarted: boolean;
  onLoadMore: () => void;
}) {
  if (operation !== undefined)
    return (
      <>
        <section
          class="panel domain-panel"
          aria-labelledby="operation-detail-title"
          data-testid="operation-detail"
        >
          <div class="panel-heading">
            <div>
              <h2 id="operation-detail-title">Operation {operation.id}</h2>
              <span class="table-secondary">{label(operation.kind)}</span>
            </div>
            <StatusLabel state={operationState(operation)}>
              {words(operation.state)}
            </StatusLabel>
          </div>
          <p class="detail-navigation">
            <a href={`#/servers/${server.id}?tab=activity`}>
              Back to operations
            </a>
          </p>
          <dl class="detail-list">
            <div>
              <dt>Created</dt>
              <dd>
                <UserTime value={operation.createdAt} />
              </dd>
            </div>
            <div>
              <dt>Started</dt>
              <dd>
                <UserTime value={operation.startedAt} fallback="Not started" />
              </dd>
            </div>
            <div>
              <dt>Finished</dt>
              <dd>
                <UserTime value={operation.finishedAt} fallback="In progress" />
              </dd>
            </div>
            <div>
              <dt>Outcome</dt>
              <dd>
                {operation.reason === null ? "—" : words(operation.reason)}
              </dd>
            </div>
          </dl>
          {!operationIsTerminal(operation) && (
            <p class="bounded-note">
              This nonterminal record polls every two seconds while visible.
              Events only trigger authoritative snapshot reads and never prove
              completion.
            </p>
          )}
        </section>
      </>
    );
  return (
    <>
      <OperationStarter
        mutations={mutations}
        server={server}
        etag={etag}
        readVersion={readVersion}
        activeOperation={operations.find(
          (candidate) => !operationIsTerminal(candidate),
        )}
      />
      <section
        class="panel domain-panel"
        aria-labelledby="operation-list-title"
        data-testid="operation-list"
      >
        <div class="panel-heading">
          <div>
            <h2 id="operation-list-title">Operation history</h2>
          </div>
        </div>
        {operations.length === 0 ? (
          <StateNotice state="empty" title="No retained operations" />
        ) : (
          <OperationRows serverID={server.id} items={operations} />
        )}
        {restarted && (
          <p class="bounded-note">
            A stale cursor restarted this traversal; prior pages were discarded.
          </p>
        )}
        {nextCursor !== null && (
          <button
            type="button"
            data-testid="load-more-operations"
            disabled={loadingMore}
            onClick={onLoadMore}
          >
            Load more operations
          </button>
        )}
      </section>
    </>
  );
}
