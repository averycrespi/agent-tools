import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import type {
  MutationController,
  MutationCoordinator,
  MutationOutcome,
  MutationSnapshot,
} from "./mutation";
import { StateNotice, TypedConfirmationDialog } from "./primitives";
import {
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
  const [controller] = useState<MutationController<DeleteResult>>(() =>
    mutations.create<DeleteResult>(),
  );
  const [deletion, setDeletion] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [typedNamespace, setTypedNamespace] = useState("");
  const [blockedReadVersion, setBlockedReadVersion] = useState<number>();
  const trigger = useRef<HTMLButtonElement>(null);

  useEffect(() => controller.subscribe(setDeletion), [controller]);
  useEffect(() => () => controller.close(), [controller]);

  if (server.desiredState === "deleted") return null;

  const waitingForRead =
    blockedReadVersion !== undefined && readVersion <= blockedReadVersion;
  const disabled =
    deletion.state === "submitting" ||
    deletion.availability === "storage_latched" ||
    waitingForRead;

  const settle = async (submission: Promise<MutationOutcome<DeleteResult>>) => {
    setTypedNamespace("");
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      controller.abandon();
      window.location.hash = `#/servers/${server.id}/operations/${outcome.value.operation.id}`;
    } else if (outcome.kind === "rejected" && outcome.requiresRefresh) {
      setBlockedReadVersion(readVersion);
    }
  };
  const review = () => {
    setTypedNamespace("");
    controller.begin({
      route: `/api/v1/servers/${server.id}`,
      method: "DELETE",
      body: "{}",
      precondition: etag,
      requiresPrecondition: true,
      idempotency: "none",
      successStatuses: [200, 202],
      decode: (response) => decodeDelete(response, decodeServerValue),
    });
    controller.confirm();
  };

  return (
    <section
      class="panel domain-panel"
      aria-labelledby="server-destructive-title"
      data-testid="server-destructive-actions"
    >
      <div class="panel-heading">
        <div>
          <h2 id="server-destructive-title">Delete server</h2>
        </div>
      </div>
      <p>
        Permanent deletion tombstones this server identity, withdraws routing,
        and attempts remote credential revocation on a best-effort basis.
      </p>
      <button
        ref={trigger}
        class="danger-action"
        type="button"
        data-testid="delete-server"
        disabled={disabled}
        onClick={review}
      >
        Review permanent deletion
      </button>
      {waitingForRead && (
        <p class="session-message" role="status">
          Waiting for a newer authoritative server snapshot.
        </p>
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
        returnFocus={trigger as unknown as RefObject<HTMLElement>}
        onValue={setTypedNamespace}
        onCancel={() => {
          setTypedNamespace("");
          controller.abandon();
        }}
        onConfirm={() => void settle(controller.submit())}
      />
    </section>
  );
}
