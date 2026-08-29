import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type {
  MutationController,
  MutationCoordinator,
  MutationOutcome,
  MutationSnapshot,
} from "./mutation";
import {
  ConfirmationDialog,
  StateNotice,
  TypedConfirmationDialog,
} from "./primitives";
import {
  decodeOperationMutation,
  decodeOperation,
  type ServerOperationView,
} from "./server-operation-model";
import type { ServerView } from "./server-reads";

type JSONRecord = Record<string, unknown>;
interface DeleteResult {
  server: ServerView;
  operation: ServerOperationView;
}

async function decodeDelete(
  response: Response,
  decodeServer: (value: unknown) => ServerView,
): Promise<DeleteResult> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid server deletion response");
  const body = new Uint8Array(await response.arrayBuffer());
  if (body.byteLength > 1024 * 1024)
    throw new Error("invalid server deletion response");
  const value = JSON.parse(
    new TextDecoder("utf-8", { fatal: true }).decode(body),
  ) as unknown;
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid server deletion response");
  const root = value as JSONRecord;
  if (Object.keys(root).sort().join(",") !== "operation,server")
    throw new Error("invalid server deletion response");
  const server = decodeServer(root.server);
  const operation = decodeOperation(root.operation);
  const etag = response.headers.get("ETag");
  if (
    server.desiredState !== "deleted" ||
    server.transport !== null ||
    operation.serverID !== server.id ||
    operation.kind !== "delete" ||
    etag !== `"server-${server.id}-${server.desiredRevision}"`
  )
    throw new Error("invalid server deletion response");
  return { server, operation };
}

function hasCredentials(server: ServerView): boolean {
  return (
    server.staticRevision !== "0" ||
    server.oauthClientRevision !== "0" ||
    server.oauthTokensRevision !== "0"
  );
}

export function ServerDestructiveActions({
  mutations,
  server,
  etag,
  readVersion,
  decodeServerValue,
}: {
  mutations: MutationCoordinator;
  server: ServerView;
  etag: string;
  readVersion: number;
  decodeServerValue: (value: unknown) => ServerView;
}) {
  const [disconnectController] = useState<
    MutationController<ServerOperationView>
  >(() => mutations.create<ServerOperationView>());
  const [deleteController] = useState<MutationController<DeleteResult>>(() =>
    mutations.create<DeleteResult>(),
  );
  const [disconnect, setDisconnect] = useState<MutationSnapshot>(() =>
    disconnectController.snapshot(),
  );
  const [deletion, setDeletion] = useState<MutationSnapshot>(() =>
    deleteController.snapshot(),
  );
  const [typedNamespace, setTypedNamespace] = useState("");
  const [blockedReadVersion, setBlockedReadVersion] = useState<number>();
  const disconnectTrigger = useRef<HTMLButtonElement>(null);
  const deleteTrigger = useRef<HTMLButtonElement>(null);

  useEffect(
    () => disconnectController.subscribe(setDisconnect),
    [disconnectController],
  );
  useEffect(() => deleteController.subscribe(setDeletion), [deleteController]);
  useEffect(
    () => () => {
      disconnectController.close();
      deleteController.close();
    },
    [deleteController, disconnectController],
  );

  if (server.desiredState === "deleted") return null;

  const waitingForRead =
    blockedReadVersion !== undefined && readVersion <= blockedReadVersion;
  const disabled =
    disconnect.state === "submitting" ||
    deletion.state === "submitting" ||
    disconnect.availability === "storage_latched" ||
    deletion.availability === "storage_latched" ||
    waitingForRead;

  const settleDisconnect = async (
    submission: Promise<MutationOutcome<ServerOperationView>>,
  ) => {
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      disconnectController.abandon();
      window.location.hash = `#/servers/${server.id}/operations/${outcome.value.id}`;
    } else if (outcome.kind === "rejected" && outcome.requiresRefresh) {
      setBlockedReadVersion(readVersion);
    }
  };
  const reviewDisconnect = () => {
    disconnectController.begin({
      route: `/api/v1/servers/${server.id}/operations`,
      method: "POST",
      body: JSON.stringify({ kind: "disconnect_credentials" }),
      precondition: etag,
      requiresPrecondition: true,
      idempotency: "operation_start",
      successStatuses: [200, 202],
      decode: async (response) => {
        const operation = await decodeOperationMutation(response);
        if (
          operation.serverID !== server.id ||
          operation.kind !== "disconnect_credentials"
        )
          throw new Error("invalid disconnect response");
        return operation;
      },
    });
    disconnectController.confirm();
  };
  const settleDelete = async (
    submission: Promise<MutationOutcome<DeleteResult>>,
  ) => {
    setTypedNamespace("");
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      deleteController.abandon();
      window.location.hash = `#/servers/${server.id}/operations/${outcome.value.operation.id}`;
    } else if (outcome.kind === "rejected" && outcome.requiresRefresh) {
      setBlockedReadVersion(readVersion);
    }
  };
  const reviewDelete = () => {
    setTypedNamespace("");
    deleteController.begin({
      route: `/api/v1/servers/${server.id}`,
      method: "DELETE",
      body: "{}",
      precondition: etag,
      requiresPrecondition: true,
      idempotency: "none",
      successStatuses: [200, 202],
      decode: (response) => decodeDelete(response, decodeServerValue),
    });
    deleteController.confirm();
  };

  return (
    <section
      class="panel domain-panel"
      aria-labelledby="server-destructive-title"
      data-testid="server-destructive-actions"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">HIGH-IMPACT ACTIONS</span>
          <h2 id="server-destructive-title">Disconnect or delete</h2>
        </div>
      </div>
      <p>
        Disconnect invalidates local credential authority and withdraws affected
        routing before one best-effort remote revocation pass. Remote revocation
        is not guaranteed, and cleanup may remain pending.
      </p>
      <p>
        Cleanup retry is local-only: it does not replay remote revocation,
        restore credential authority, or force cleanup.
      </p>
      <div class="dialog-actions">
        {hasCredentials(server) && (
          <button
            ref={disconnectTrigger}
            type="button"
            data-testid="disconnect-server-credentials"
            disabled={disabled}
            onClick={reviewDisconnect}
          >
            Review credential disconnect
          </button>
        )}
        <button
          ref={deleteTrigger}
          class="danger-action"
          type="button"
          data-testid="delete-server"
          disabled={disabled}
          onClick={reviewDelete}
        >
          Review permanent deletion
        </button>
      </div>
      {waitingForRead && (
        <p class="session-message" role="status">
          Waiting for a newer authoritative server snapshot.
        </p>
      )}
      {disconnect.problem !== undefined && (
        <StateNotice state="error" title={disconnect.problem.title} />
      )}
      {disconnect.state === "uncertain" && (
        <StateNotice state="warning" title="Disconnect outcome unknown">
          <p>
            Inspect server and operation history. Do not replay or infer remote
            revocation, cleanup, or authority restoration.
          </p>
        </StateNotice>
      )}
      {deletion.problem !== undefined && (
        <StateNotice state="error" title={deletion.problem.title} />
      )}
      {deletion.state === "uncertain" && (
        <StateNotice state="warning" title="Deletion outcome unknown">
          <p>
            Refresh the permanent server identity and operation history. Do not
            replay, force deletion, or infer remote cleanup.
          </p>
        </StateNotice>
      )}
      <ConfirmationDialog
        id="server-disconnect-confirm"
        open={disconnect.state === "confirming"}
        title="Disconnect server credentials?"
        consequence="Local credential authority is invalidated and affected routing is withdrawn. Remote revocation is one best-effort pass and is not guaranteed; cleanup may remain pending."
        confirmLabel="Disconnect credentials"
        destructive
        returnFocus={disconnectTrigger as unknown as RefObject<HTMLElement>}
        onCancel={() => disconnectController.abandon()}
        onConfirm={() => void settleDisconnect(disconnectController.submit())}
      />
      <TypedConfirmationDialog
        id="server-delete-confirm"
        open={deletion.state === "confirming"}
        title="Permanently delete this server?"
        consequence={
          <p>
            This tombstones the permanent identity and immutable namespace,
            withdraws routing, invalidates local authority, and attempts remote
            revocation only on a best-effort basis. Cleanup may remain pending.
            There is no force or authority-restoration path.
          </p>
        }
        expected={server.namespace}
        value={typedNamespace}
        confirmLabel="Permanently delete server"
        returnFocus={deleteTrigger as unknown as RefObject<HTMLElement>}
        onValue={setTypedNamespace}
        onCancel={() => {
          setTypedNamespace("");
          deleteController.abandon();
        }}
        onConfirm={() => void settleDelete(deleteController.submit())}
      />
    </section>
  );
}
