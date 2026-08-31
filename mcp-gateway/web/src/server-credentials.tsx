import type { RefObject } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import { useUnsavedChanges } from "./navigation";
import type {
  MutationController,
  MutationCoordinator,
  MutationOutcome,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import { ConfirmationDialog, StateNotice, StatusLabel } from "./primitives";
import {
  decodeOperation,
  type ServerOperationView,
} from "./server-operation-model";
import type { ServerView } from "./server-reads";
import { WriteOnlyField } from "./sinks-ui";
import type { SensitiveSinkCoordinator, WriteOnlyValue } from "./sinks";

type JSONRecord = Record<string, unknown>;
type ReplacementKind = "static_credential" | "oauth_client";
interface ReplacementResult {
  serverID: string;
  kind: ReplacementKind;
  credentialRevision: string;
  operation: ServerOperationView;
}

function replacementShape(server: ServerView):
  | {
      kind: ReplacementKind;
      slots: string[];
      expectedRevision: string;
    }
  | undefined {
  if (server.desiredState === "deleted" || server.transport === null)
    return undefined;
  const transport = server.transport as JSONRecord;
  if (transport.kind === "stdio") {
    const environment = transport.secret_environment as Record<string, string>;
    const slots = [...new Set(Object.values(environment))].sort();
    return slots.length === 0
      ? undefined
      : {
          kind: "static_credential",
          slots,
          expectedRevision: server.staticRevision,
        };
  }
  const authentication = (transport.authentication ?? {}) as JSONRecord;
  if (authentication.mode === "bearer")
    return {
      kind: "static_credential",
      slots: ["bearer"],
      expectedRevision: server.staticRevision,
    };
  if (authentication.mode !== "oauth") return undefined;
  const registration = (authentication.registration ?? {}) as JSONRecord;
  return registration.mode === "static" &&
    (registration.token_endpoint_auth_method === "client_secret_basic" ||
      registration.token_endpoint_auth_method === "client_secret_post")
    ? {
        kind: "oauth_client",
        slots: ["client_secret"],
        expectedRevision: server.oauthClientRevision,
      }
    : undefined;
}
function exactRecord(value: unknown, keys: readonly string[]): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid credential replacement response");
  const item = value as JSONRecord;
  if (Object.keys(item).sort().join(",") !== [...keys].sort().join(","))
    throw new Error("invalid credential replacement response");
  return item;
}
async function decodeReplacement(
  response: Response,
): Promise<ReplacementResult> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("invalid credential replacement response");
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 1024 * 1024)
    throw new Error("invalid credential replacement response");
  const root = exactRecord(JSON.parse(body) as unknown, [
    "server_id",
    "kind",
    "credential_revision",
    "operation",
  ]);
  const operation = decodeOperation(root.operation);
  if (
    typeof root.server_id !== "string" ||
    operation.serverID !== root.server_id ||
    (root.kind !== "static_credential" && root.kind !== "oauth_client") ||
    typeof root.credential_revision !== "string" ||
    !/^(?:0|[1-9][0-9]*)$/.test(root.credential_revision) ||
    operation.kind !== "credential_replace"
  )
    throw new Error("invalid credential replacement response");
  return {
    serverID: root.server_id,
    kind: root.kind,
    credentialRevision: root.credential_revision,
    operation,
  };
}

function ReplacementForm({
  mutations,
  sinks,
  server,
  etag,
  readVersion,
  shape,
  onRefresh,
}: {
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  server: ServerView;
  etag: string;
  readVersion: number;
  shape: NonNullable<ReturnType<typeof replacementShape>>;
  onRefresh: () => void;
}) {
  const [values] = useState<Array<{ slot: string; value: WriteOnlyValue }>>(
    () => shape.slots.map((slot) => ({ slot, value: sinks.createWriteOnly() })),
  );
  const [controller] = useState<MutationController<ReplacementResult>>(() =>
    mutations.create<ReplacementResult>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [confirming, setConfirming] = useState(false);
  const [error, setError] = useState<string>();
  const [notice, setNotice] = useState<string>();
  const [blockedReadVersion, setBlockedReadVersion] = useState<number>();
  const [credentialDirty, setCredentialDirty] = useState(false);
  const submitButton = useRef<HTMLButtonElement>(null);
  const navigate = useUnsavedChanges(credentialDirty);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(
    () => () => {
      controller.close();
      for (const entry of values) entry.value.close();
    },
    [controller, values],
  );
  const updateCredentialDirty = () => {
    setCredentialDirty(
      values.some(({ slot }) => {
        const input = document.getElementById(`credential-slot-${slot}`);
        return (
          (input instanceof HTMLInputElement ||
            input instanceof HTMLTextAreaElement) &&
          input.value !== ""
        );
      }),
    );
  };
  const clear = () => {
    for (const entry of values) entry.value.clear();
    setCredentialDirty(false);
  };
  const settle = async (
    submission: Promise<MutationOutcome<ReplacementResult>>,
  ) => {
    const outcome = await submission;
    if (outcome.kind === "acknowledged") {
      setBlockedReadVersion(undefined);
      setNotice(
        `Credential revision ${outcome.value.credentialRevision} was published; operation ${outcome.value.operation.id} was scheduled.`,
      );
      controller.abandon();
      navigate(
        `#/servers/${server.id}/operations/${outcome.value.operation.id}`,
        true,
      );
    } else if (
      (outcome.kind === "rejected" && outcome.requiresRefresh) ||
      outcome.kind === "uncertain"
    ) {
      setBlockedReadVersion(readVersion);
      onRefresh();
    }
  };
  const review = () => {
    setError(undefined);
    setNotice(undefined);
    const entries = values.map(({ slot }) => {
      const input = document.getElementById(`credential-slot-${slot}`);
      const secret =
        input instanceof HTMLInputElement ||
        input instanceof HTMLTextAreaElement
          ? input.value
          : "";
      return [slot, secret] as const;
    });
    if (entries.some(([, value]) => value.length === 0)) {
      clear();
      setError("Every write-only credential field must be nonempty.");
      return;
    }
    setConfirming(true);
  };
  const confirm = () => {
    setConfirming(false);
    const secrets = Object.fromEntries(
      values.map(({ slot }) => {
        const input = document.getElementById(`credential-slot-${slot}`);
        const secret =
          input instanceof HTMLInputElement ||
          input instanceof HTMLTextAreaElement
            ? input.value
            : "";
        return [slot, secret] as const;
      }),
    );
    const body =
      shape.kind === "static_credential"
        ? JSON.stringify({
            kind: shape.kind,
            expected_revision: shape.expectedRevision,
            values: secrets,
          })
        : JSON.stringify({
            kind: shape.kind,
            expected_revision: shape.expectedRevision,
            client_secret: secrets.client_secret,
          });
    const spec: MutationSpec<ReplacementResult> = {
      route: `/api/v1/servers/${server.id}/credential-replacements`,
      method: "POST",
      body,
      precondition: etag,
      requiresPrecondition: true,
      idempotency: "none",
      successStatuses: [202],
      uncertainProblemCodes: ["keyring_unavailable"],
      decode: async (response) => {
        const result = await decodeReplacement(response);
        if (result.serverID !== server.id || result.kind !== shape.kind)
          throw new Error("invalid credential replacement response");
        return result;
      },
    };
    controller.begin(spec);
    const submission = controller.submit();
    clear();
    void settle(submission);
  };
  const waitingForRead =
    blockedReadVersion !== undefined && readVersion <= blockedReadVersion;
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched" ||
    waitingForRead;
  return (
    <>
      <form data-testid="credential-replacement-form">
        {values.map(({ slot, value }) => (
          <WriteOnlyField
            key={slot}
            value={value}
            id={`credential-slot-${slot}`}
            label={
              shape.kind === "oauth_client"
                ? "OAuth client secret"
                : `Secret slot ${slot}`
            }
            hint="Write-only. This value is cleared immediately after submission and is never returned by the Gateway."
            onInput={updateCredentialDirty}
          />
        ))}
        {error !== undefined && (
          <StateNotice state="error" title="Credential input required">
            <p>{error}</p>
          </StateNotice>
        )}
        {mutation.problem !== undefined && (
          <StateNotice state="error" title={mutation.problem.title}>
            {mutation.requiresRefresh && (
              <p>
                A fresh server snapshot was requested. Re-enter credentials only
                after reviewing current revisions and operation history.
              </p>
            )}
          </StateNotice>
        )}
        {mutation.state === "uncertain" && (
          <StateNotice state="warning" title="Replacement outcome unknown">
            <p>
              The credential or operation may have been published. No replay is
              available. Inspect the refreshed server and operation history
              before making a new decision.
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
        <button
          ref={submitButton}
          class="danger-action"
          type="button"
          data-testid="credential-replacement-submit"
          disabled={disabled}
          onClick={review}
        >
          Review credential replacement
        </button>
      </form>
      <ConfirmationDialog
        id="credential-replacement-confirm"
        open={confirming}
        title="Replace current server authority?"
        consequence="Replacement withdraws current routing and may interrupt calls. Calls already handed downstream can have unknown outcomes. A native keyring operation may require OS interaction, fail, or outlive cancellation."
        confirmLabel="Replace credential"
        destructive
        returnFocus={submitButton as unknown as RefObject<HTMLElement>}
        onCancel={() => {
          setConfirming(false);
          clear();
        }}
        onConfirm={confirm}
      />
    </>
  );
}

function ReplacementFormHost(
  props: Omit<Parameters<typeof ReplacementForm>[0], "shape"> & {
    shape: NonNullable<ReturnType<typeof replacementShape>>;
  },
) {
  const shapeKey = `${props.shape.kind}:${props.shape.slots.join(",")}`;
  const [activeKey, setActiveKey] = useState(shapeKey);
  useEffect(() => {
    if (activeKey !== shapeKey) setActiveKey(shapeKey);
  }, [activeKey, shapeKey]);
  if (activeKey !== shapeKey) return null;
  return <ReplacementForm {...props} />;
}

function authenticationPresentation(server: ServerView): {
  heading: string;
  description: string;
  status: string;
  warning: boolean;
} {
  if (server.transport === null)
    return {
      heading: "Authentication",
      description: "No active transport is configured.",
      status: "Unavailable",
      warning: true,
    };
  const transport = server.transport as JSONRecord;
  const authentication = (transport.authentication ?? {}) as JSONRecord;
  let heading = "No authentication";
  let description = "This server does not require credentials.";
  if (transport.kind === "stdio") {
    const environment = (transport.secret_environment ?? {}) as JSONRecord;
    if (Object.keys(environment).length > 0) {
      heading = "Local secrets";
      description = "Replace the write-only values used by this local server.";
    }
  } else if (authentication.mode === "bearer") {
    heading = "Bearer token";
    description = "Replace the write-only bearer token used for this server.";
  } else if (authentication.mode === "oauth") {
    heading = "OAuth";
    description =
      "Authorize this server or replace its configured client secret.";
  }
  const warning = [
    "absent",
    "reauthentication_required",
    "locked",
    "interaction_required",
    "unavailable",
    "unsupported",
  ].includes(server.credentialState);
  return {
    heading,
    description,
    status:
      heading === "No authentication"
        ? "Not required"
        : warning
          ? "Action required"
          : "Configured",
    warning,
  };
}

export function ServerCredentials({
  mutations,
  sinks,
  server,
  etag,
  readVersion,
  onRefresh,
}: {
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  server: ServerView;
  etag: string;
  readVersion: number;
  onRefresh: () => void;
}) {
  const shape = replacementShape(server);
  const presentation = authenticationPresentation(server);
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="server-credentials-title"
      data-testid="server-credentials"
    >
      <div class="panel-heading">
        <div>
          <h2 id="server-credentials-title">{presentation.heading}</h2>
        </div>
        <StatusLabel state={presentation.warning ? "warning" : "current"}>
          {presentation.status}
        </StatusLabel>
      </div>
      <p>{presentation.description}</p>
      <p>
        <a href={`#/servers/${server.id}?tab=activity`}>Inspect activity</a>
      </p>
      {shape !== undefined && (
        <ReplacementFormHost
          mutations={mutations}
          sinks={sinks}
          server={server}
          etag={etag}
          readVersion={readVersion}
          shape={shape}
          onRefresh={onRefresh}
        />
      )}
    </section>
  );
}
