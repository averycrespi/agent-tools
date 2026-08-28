import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type {
  MutationController,
  MutationCoordinator,
  MutationOutcome,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import { ConfirmationDialog, StateNotice, StatusLabel } from "./primitives";
import {
  decodeOperationMutation,
  operationIsTerminal,
  type ExplicitOperationKind,
  type ServerOperationView,
} from "./server-operation-model";
import type { ServerView } from "./server-reads";

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
function label(kind: ExplicitOperationKind): string {
  switch (kind) {
    case "reload":
      return "Reload server";
    case "retry":
      return "Retry eligible work";
    case "refresh_catalog":
      return "Refresh catalog";
    case "disconnect_credentials":
      return "Disconnect credentials";
  }
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
    <div class="audit-records">
      {items.map((operation) => (
        <article
          class="audit-record"
          data-testid="operation-row"
          key={operation.id}
        >
          <div class="audit-record-heading">
            <div>
              <span class="panel-code">{operation.kind}</span>
              <h3>
                <a href={`#/servers/${serverID}/operations/${operation.id}`}>
                  Operation {operation.id}
                </a>
              </h3>
            </div>
            <StatusLabel
              state={
                operation.state === "failed" ||
                operation.state === "interrupted"
                  ? "warning"
                  : operationIsTerminal(operation)
                    ? "current"
                    : "warning"
              }
            >
              {operation.state}
            </StatusLabel>
          </div>
          <p>
            Target desired revision {operation.targetDesiredRevision} · created{" "}
            {operation.createdAt}
          </p>
          {operation.reason !== null && <p>Reason {operation.reason}</p>}
        </article>
      ))}
    </div>
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
          <span class="panel-code">EXPLICIT WORK</span>
          <h2 id="operation-start-title">Start an eligible operation</h2>
        </div>
      </div>
      <p>
        Eligibility is a current presentation hint. The API rechecks current
        server, operation, credential, and catalog state before admission.
      </p>
      {eligible.length === 0 ? (
        <StateNotice
          state="empty"
          title="No explicit operation is currently eligible"
        />
      ) : (
        <div class="dialog-actions">
          {eligible.map((kind) => (
            <button
              key={kind}
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
              <span class="panel-code">{operation.kind}</span>
              <h2 id="operation-detail-title">Operation {operation.id}</h2>
            </div>
            <StatusLabel
              state={operationIsTerminal(operation) ? "current" : "warning"}
            >
              {operation.state}
            </StatusLabel>
          </div>
          <p>
            <a href={`#/servers/${server.id}?tab=operations`}>
              Operation history
            </a>{" "}
            · <a href={`#/servers/${server.id}`}>Server record</a>
          </p>
          <div class="fact-grid">
            <article class="fact-card">
              <span class="panel-code">TARGET</span>
              <h3>Desired revision {operation.targetDesiredRevision}</h3>
              <p>
                Static {operation.targetStaticRevision} · OAuth client{" "}
                {operation.targetOAuthClientRevision} · tokens{" "}
                {operation.targetOAuthTokensRevision}
              </p>
            </article>
            <article class="fact-card">
              <span class="panel-code">TIMING</span>
              <h3>{operation.state}</h3>
              <p>Created {operation.createdAt}</p>
              <p>Started {operation.startedAt ?? "not recorded"}</p>
              <p>Finished {operation.finishedAt ?? "not recorded"}</p>
              <p>Reason {operation.reason ?? "none"}</p>
            </article>
          </div>
          {!operationIsTerminal(operation) && (
            <p class="bounded-note">
              This nonterminal record polls every two seconds while visible.
              Events only trigger authoritative snapshot reads and never prove
              completion.
            </p>
          )}
        </section>
        <OperationStarter
          mutations={mutations}
          server={server}
          etag={etag}
          readVersion={readVersion}
          activeOperation={
            operationIsTerminal(operation) ? undefined : operation
          }
        />
      </>
    );
  return (
    <>
      <section
        class="panel domain-panel"
        aria-labelledby="operation-list-title"
        data-testid="operation-list"
      >
        <div class="panel-heading">
          <div>
            <span class="panel-code">OPERATION HISTORY</span>
            <h2 id="operation-list-title">
              Scheduled, running, and terminal work
            </h2>
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
      <OperationStarter
        mutations={mutations}
        server={server}
        etag={etag}
        readVersion={readVersion}
        activeOperation={operations.find(
          (candidate) => !operationIsTerminal(candidate),
        )}
      />
    </>
  );
}
