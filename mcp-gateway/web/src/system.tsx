import { useEffect, useRef, useState } from "preact/hooks";
import { useUnsavedChanges } from "./navigation";
import { decodeStatus, type LimitView, type StatusView } from "./overview";
import type {
  MutationController,
  MutationCoordinator,
  MutationSnapshot,
  MutationSpec,
} from "./mutation";
import {
  CollectionTable,
  ComparisonTable,
  ConfirmationDialog,
  FormField,
  sentenceCase,
  StateNotice,
  StatusLabel,
} from "./primitives";
import type { SessionClient } from "./session";
import type { PreparedOneTimeSink, SensitiveSinkCoordinator } from "./sinks";
import { UserTime } from "./time";
import type { ViewCoordinator, ViewSnapshot } from "./view";

type Listener = (status: StatusView | undefined) => void;
type CredentialListener = (credentials: AdminCredential[] | undefined) => void;
type BackupListener = (backups: Backup[] | undefined) => void;
type SystemTab = "status" | "resource-limits" | "admin-credentials" | "backups";
type CredentialStatus = "active" | "revoked" | "expired";
type AdminCredential = {
  id: string;
  fingerprint: string;
  createdAt: string;
  expiresAt: string | null;
  nonExpiring: boolean;
  status: CredentialStatus;
  revision: string;
};
type CreatedAdminCredential = AdminCredential & { bearer: string };
type Backup = {
  id: string;
  createdAt: string;
  installationID: string;
  schemaVersion: string;
  sourceRevision: string;
  sizeBytes: number;
  sha256: string;
};
type JSONRecord = Record<string, unknown>;
const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
function record(value: unknown, keys: readonly string[]): JSONRecord {
  if (value === null || typeof value !== "object" || Array.isArray(value))
    throw new Error("invalid response");
  const item = value as JSONRecord;
  const actual = Object.keys(item).sort();
  const expected = [...keys].sort();
  if (
    actual.length !== expected.length ||
    actual.some((key, index) => key !== expected[index])
  )
    throw new Error("invalid response");
  return item;
}
function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("invalid response");
  return value;
}
function decodeCredential(value: unknown): AdminCredential {
  const item = record(value, [
    "id",
    "fingerprint",
    "created_at",
    "expires_at",
    "non_expiring",
    "status",
    "revision",
  ]);
  const id = text(item.id);
  const fingerprint = text(item.fingerprint);
  const status = text(item.status);
  if (
    !gatewayID.test(id) ||
    !/^[0-9a-f]{16}$/.test(fingerprint) ||
    (status !== "active" && status !== "revoked" && status !== "expired") ||
    typeof item.non_expiring !== "boolean" ||
    (item.expires_at !== null && typeof item.expires_at !== "string")
  )
    throw new Error("invalid response");
  return {
    id,
    fingerprint,
    createdAt: text(item.created_at),
    expiresAt: item.expires_at as string | null,
    nonExpiring: item.non_expiring,
    status,
    revision: text(item.revision),
  };
}
function decodeCreatedCredential(value: unknown): CreatedAdminCredential {
  const item = record(value, [
    "id",
    "fingerprint",
    "created_at",
    "expires_at",
    "non_expiring",
    "status",
    "revision",
    "bearer",
  ]);
  const credential = decodeCredential(
    Object.fromEntries(
      Object.entries(item).filter(([key]) => key !== "bearer"),
    ),
  );
  const bearer = text(item.bearer);
  if (!/^mgw_admin_[A-Za-z0-9_-]{43}$/.test(bearer))
    throw new Error("invalid response");
  return { ...credential, bearer };
}

function decodeBackup(value: unknown): Backup {
  const item = record(value, [
    "id",
    "created_at",
    "installation_id",
    "schema_version",
    "source_revision",
    "size_bytes",
    "sha256",
  ]);
  const id = text(item.id);
  const sha256 = text(item.sha256);
  if (
    !gatewayID.test(id) ||
    !/^[0-9a-f]{64}$/.test(sha256) ||
    typeof item.size_bytes !== "number" ||
    !Number.isSafeInteger(item.size_bytes) ||
    item.size_bytes < 0
  )
    throw new Error("invalid response");
  return {
    id,
    createdAt: text(item.created_at),
    installationID: text(item.installation_id),
    schemaVersion: text(item.schema_version),
    sourceRevision: text(item.source_revision),
    sizeBytes: item.size_bytes,
    sha256,
  };
}

async function readCredentials(
  csrfToken: string,
  signal: AbortSignal,
): Promise<AdminCredential[]> {
  const credentials: AdminCredential[] = [];
  let cursor: string | null = null;
  let restarted = false;
  for (;;) {
    const params = new URLSearchParams({ limit: "100" });
    if (cursor !== null) params.set("cursor", cursor);
    const response = await fetch(`/api/v1/admin-credentials?${params}`, {
      method: "GET",
      headers: { "X-CSRF-Token": csrfToken },
      credentials: "same-origin",
      redirect: "error",
      signal,
    });
    if (response.status === 409 && cursor !== null && !restarted) {
      credentials.length = 0;
      cursor = null;
      restarted = true;
      continue;
    }
    if (
      response.status !== 200 ||
      response.headers.get("Content-Type") !== "application/json"
    )
      throw new Error("admin credential read failed");
    const page = record((await response.json()) as unknown, [
      "items",
      "next_cursor",
    ]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    credentials.push(...page.items.map(decodeCredential));
    if (page.next_cursor === null) return credentials;
    cursor = text(page.next_cursor);
  }
}

async function readBackups(
  csrfToken: string,
  signal: AbortSignal,
): Promise<Backup[]> {
  const backups: Backup[] = [];
  let cursor: string | null = null;
  let restarted = false;
  for (;;) {
    const params = new URLSearchParams({ limit: "100" });
    if (cursor !== null) params.set("cursor", cursor);
    const response = await fetch(`/api/v1/backups?${params}`, {
      method: "GET",
      headers: { "X-CSRF-Token": csrfToken },
      credentials: "same-origin",
      redirect: "error",
      signal,
    });
    if (response.status === 409 && cursor !== null && !restarted) {
      backups.length = 0;
      cursor = null;
      restarted = true;
      continue;
    }
    if (
      response.status !== 200 ||
      response.headers.get("Content-Type") !== "application/json"
    )
      throw new Error("backup read failed");
    const page = record((await response.json()) as unknown, [
      "items",
      "next_cursor",
    ]);
    if (!Array.isArray(page.items)) throw new Error("invalid response");
    backups.push(...page.items.map(decodeBackup));
    if (page.next_cursor === null) return backups;
    cursor = text(page.next_cursor);
  }
}

async function getStatus(
  csrfToken: string,
  signal: AbortSignal,
): Promise<Response> {
  return fetch("/api/v1/system-status", {
    method: "GET",
    headers: { "X-CSRF-Token": csrfToken },
    credentials: "same-origin",
    redirect: "error",
    signal,
  });
}

async function responseJSON(response: Response): Promise<unknown> {
  if (
    response.status !== 200 ||
    response.headers.get("Content-Type") !== "application/json"
  )
    throw new Error("system status read failed");
  const body = await response.text();
  if (new TextEncoder().encode(body).byteLength > 1024 * 1024)
    throw new Error("system status read failed");
  return JSON.parse(body) as unknown;
}

function tab(viewKey: string): SystemTab {
  if (viewKey === "#/system/backups/new") return "backups";
  if (viewKey === "#/system/admin-credentials/new") return "admin-credentials";
  if (viewKey === "#/system?tab=resource-limits") return "resource-limits";
  if (viewKey === "#/system?tab=admin-credentials") return "admin-credentials";
  if (viewKey === "#/system?tab=backups") return "backups";
  return "status";
}

export class SystemController {
  private readonly listeners = new Set<Listener>();
  private readonly credentialListeners = new Set<CredentialListener>();
  private readonly backupListeners = new Set<BackupListener>();
  private value: StatusView | undefined;
  private credentials: AdminCredential[] | undefined;
  private backups: Backup[] | undefined;

  constructor(
    session: SessionClient,
    views: ViewCoordinator,
    setStorageLatched: (latched: boolean) => void,
  ) {
    views.registerPanel({
      id: "mutation-latch",
      matches: () => true,
      invalidations: ["system_status"],
      read: async (context) =>
        decodeStatus(
          await responseJSON(
            await getStatus(context.csrfToken, context.signal),
          ),
        ),
      publish: (status) => setStorageLatched(status.latched),
    });
    views.registerPanel({
      id: "backups",
      matches: (viewKey) =>
        viewKey === "#/system?tab=backups" ||
        viewKey === "#/system/backups/new",
      invalidations: ["backups"],
      read: async (context) => readBackups(context.csrfToken, context.signal),
      publish: (backups) => {
        this.backups = backups;
        this.emitBackups();
      },
    });
    views.registerPanel({
      id: "admin-credentials",
      matches: (viewKey) =>
        viewKey === "#/system?tab=admin-credentials" ||
        viewKey === "#/system/admin-credentials/new",
      invalidations: ["admin_credentials"],
      read: async (context) =>
        readCredentials(context.csrfToken, context.signal),
      publish: (credentials) => {
        this.credentials = credentials;
        this.emitCredentials();
      },
    });
    views.registerPanel({
      id: "system-status",
      matches: (viewKey) =>
        viewKey === "#/system" || viewKey === "#/system?tab=resource-limits",
      invalidations: ["system_status"],
      read: async (context) =>
        decodeStatus(
          await responseJSON(
            await getStatus(context.csrfToken, context.signal),
          ),
        ),
      publish: (status) => {
        this.value = status;
        setStorageLatched(status.latched);
        this.emit();
      },
    });
    session.registerProtectedState(() => {
      this.value = undefined;
      this.credentials = undefined;
      this.backups = undefined;
      setStorageLatched(false);
      this.emit();
      this.emitCredentials();
      this.emitBackups();
    });
  }

  snapshot(): StatusView | undefined {
    return this.value;
  }

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    listener(this.value);
    return () => this.listeners.delete(listener);
  }

  credentialSnapshot(): AdminCredential[] | undefined {
    return this.credentials;
  }

  subscribeCredentials(listener: CredentialListener): () => void {
    this.credentialListeners.add(listener);
    listener(this.credentials);
    return () => this.credentialListeners.delete(listener);
  }

  backupSnapshot(): Backup[] | undefined {
    return this.backups;
  }

  subscribeBackups(listener: BackupListener): () => void {
    this.backupListeners.add(listener);
    listener(this.backups);
    return () => this.backupListeners.delete(listener);
  }

  private emitBackups(): void {
    for (const listener of this.backupListeners) listener(this.backups);
  }

  private emitCredentials(): void {
    for (const listener of this.credentialListeners) listener(this.credentials);
  }

  private emit(): void {
    for (const listener of this.listeners) listener(this.value);
  }
}

function stateForLimit(limit: LimitView): "current" | "warning" {
  return limit.saturated ? "warning" : "current";
}

function SystemTabs({ current }: { current: SystemTab }) {
  const items: ReadonlyArray<[SystemTab, string, string]> = [
    ["status", "Status", "#/system"],
    ["resource-limits", "Resource limits", "#/system?tab=resource-limits"],
    [
      "admin-credentials",
      "Admin credentials",
      "#/system?tab=admin-credentials",
    ],
    ["backups", "Backups", "#/system?tab=backups"],
  ];
  return (
    <nav class="subnav" aria-label="System sections">
      {items.map(([name, label, href]) => (
        <a
          key={name}
          href={href}
          aria-current={current === name ? "page" : undefined}
        >
          {label}
        </a>
      ))}
    </nav>
  );
}

function StatusPanel({
  status,
  view,
}: {
  status: StatusView | undefined;
  view: ViewSnapshot;
}) {
  const panel = view.panels["system-status"];
  const panelStatus = panel?.status ?? "loading";
  return (
    <section
      class="panel"
      aria-labelledby="system-status-title"
      data-testid="system-status-panel"
      data-panel-status={panelStatus}
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">SYSTEM-01</span>
          <h2 id="system-status-title">Gateway status</h2>
        </div>
        <StatusLabel state={panelStatus === "error" ? "error" : panelStatus}>
          {sentenceCase(panelStatus)}
        </StatusLabel>
      </div>
      {panelStatus === "error" && status === undefined ? (
        <StateNotice state="error" title="System status unavailable">
          <p>
            Check Gateway availability, then refresh this authoritative read.
          </p>
        </StateNotice>
      ) : panelStatus === "loading" && panel?.hasValue !== true ? (
        <StateNotice state="loading" title="Loading system status" />
      ) : status !== undefined ? (
        <div class="system-stack">
          <section aria-labelledby="overall-health-title">
            <h3 id="overall-health-title">Overall health</h3>
            <StatusLabel
              state={
                status.ready && !status.latched && status.keyring === "ready"
                  ? "current"
                  : "warning"
              }
            >
              {status.ready && !status.latched && status.keyring === "ready"
                ? "Healthy"
                : "Degraded"}
            </StatusLabel>
          </section>
          <section aria-labelledby="system-issues-title">
            <h3 id="system-issues-title">Issues requiring attention</h3>
            {status.latched && (
              <StateNotice
                state="error"
                title="Storage mutations are unavailable"
              >
                <p>
                  Reads can remain available, but only the documented
                  stopped-process recovery procedure can clear this condition.
                </p>
              </StateNotice>
            )}
            {status.keyring !== "ready" && (
              <StateNotice
                state="warning"
                title="Credential storage is unavailable"
              >
                <p>Credential operations may require interaction or fail.</p>
              </StateNotice>
            )}
            {!status.latched && status.keyring === "ready" && (
              <p>No current issues require operator action.</p>
            )}
          </section>
          <div class="fact-grid">
            <article class="fact-card">
              <span class="panel-code">PROCESS</span>
              <h3>Process {sentenceCase(status.processState)}</h3>
              <StatusLabel state={status.ready ? "current" : "warning"}>
                {status.ready ? "Ready" : "Not ready"}
              </StatusLabel>
              <p>
                Started <UserTime value={status.startedAt} />
              </p>
            </article>
            <article class="fact-card">
              <span class="panel-code">SQLITE</span>
              <h3>Storage {sentenceCase(status.sqliteState)}</h3>
              <p>Schema {status.schemaVersion}</p>
              <p>Revision {status.revision}</p>
              <StatusLabel state={status.latched ? "error" : "current"}>
                Mutation admission {status.latched ? "closed" : "open"}
              </StatusLabel>
            </article>
            <article class="fact-card">
              <span class="panel-code">KEYRING</span>
              <h3>Keyring {sentenceCase(status.keyring)}</h3>
              <p>
                OS-managed capability snapshot; later authority work may still
                require interaction or fail.
              </p>
            </article>
            <article class="fact-card">
              <span class="panel-code">BACKUP</span>
              <h3>Backup {sentenceCase(status.backupState)}</h3>
              <p>
                Last completed{" "}
                <UserTime value={status.lastBackupAt} fallback="never" />
              </p>
            </article>
            <article class="fact-card fact-card-wide">
              <span class="panel-code">PROTOCOLS</span>
              <h3>Agent authentication {sentenceCase(status.agentAuth)}</h3>
              <p>
                modern {status.modernProtocol} · legacy {status.legacyProtocol}
              </p>
            </article>
          </div>
          <section aria-labelledby="resource-summary-title">
            <h3 id="resource-summary-title">Resource summary</h3>
            <p>
              {status.limits.filter((limit) => limit.saturated).length}{" "}
              saturated resources across {status.limits.length} enforced limits.
            </p>
            <a href="#/system?tab=resource-limits">View resource limits</a>
          </section>
        </div>
      ) : (
        <StateNotice state="loading" title="Loading system status" />
      )}
    </section>
  );
}

function ResourceLimits({ status }: { status: StatusView | undefined }) {
  if (status === undefined)
    return <StateNotice state="loading" title="Loading resource limits" />;
  const limits = [...status.limits].sort((left, right) => {
    if (left.saturated !== right.saturated) return left.saturated ? -1 : 1;
    return left.name.localeCompare(right.name);
  });
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="system-limits-title"
      data-testid="system-limits-view"
    >
      <div class="panel-heading">
        <div>
          <h2 id="system-limits-title">Resource limits</h2>
        </div>
        <StatusLabel
          state={
            limits.some((limit) => limit.saturated) ? "warning" : "current"
          }
        >
          {limits.some((limit) => limit.saturated)
            ? "Attention needed"
            : "Available"}
        </StatusLabel>
      </div>
      <p>Current occupancy against enforced Gateway limits.</p>
      <ComparisonTable caption="Gateway resource occupancy and hard limits">
        <thead>
          <tr>
            <th scope="col">Resource</th>
            <th scope="col">In use</th>
            <th scope="col">Limit</th>
            <th scope="col">Status</th>
          </tr>
        </thead>
        <tbody>
          {limits.map((limit) => (
            <tr key={limit.name} data-testid="system-limit-row">
              <th scope="row">{limit.name}</th>
              <td>{limit.inUse}</td>
              <td>{limit.limit}</td>
              <td>
                <StatusLabel state={stateForLimit(limit)}>
                  {limit.saturated ? "Saturated" : "Available"}
                </StatusLabel>
              </td>
            </tr>
          ))}
        </tbody>
      </ComparisonTable>
    </section>
  );
}

function Backups({
  session,
  backups,
  view,
  mutations,
  onRefresh,
  createMode,
}: {
  session: SessionClient;
  backups: Backup[] | undefined;
  view: ViewSnapshot;
  mutations: MutationCoordinator;
  onRefresh: () => void;
  createMode: boolean;
}) {
  const [controller] = useState<MutationController<Backup | undefined>>(() =>
    mutations.create<Backup | undefined>(),
  );
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [detail, setDetail] = useState<Backup>();
  const [deleting, setDeleting] = useState<Backup>();
  const [notice, setNotice] = useState<string>();
  const createButton = useRef<HTMLButtonElement>(null);
  const deleteButton = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => {
    if (detail !== undefined && backups !== undefined)
      setDetail(backups.find((backup) => backup.id === detail.id));
  }, [backups]);
  const panel = view.panels.backups;
  const panelStatus = panel?.status ?? "loading";
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched";
  const decodeCreate = async (response: Response) => {
    if (response.headers.get("Content-Type") !== "application/json")
      throw new Error("invalid response");
    return decodeBackup((await response.json()) as unknown);
  };
  const decodeDelete = async (response: Response) => {
    if (response.status !== 204) throw new Error("invalid response");
    return undefined;
  };
  const settle = async (
    outcome: Awaited<ReturnType<typeof controller.submit>>,
  ) => {
    if (outcome.kind === "acknowledged") {
      setNotice(
        outcome.value === undefined
          ? "Backup deleted. Restore remains a stopped-process CLI operation."
          : `Backup ${outcome.value.id} is durably published.`,
      );
      controller.abandon();
      if (outcome.value !== undefined)
        window.location.hash = "#/system?tab=backups";
      onRefresh();
    }
  };
  const create = () => {
    setNotice(undefined);
    const spec: MutationSpec<Backup | undefined> = {
      route: "/api/v1/backups",
      method: "POST",
      body: "{}",
      precondition: null,
      requiresPrecondition: false,
      idempotency: "backup_create",
      successStatuses: [200, 201],
      decode: decodeCreate,
    };
    controller.begin(spec);
    controller.confirm();
  };
  const confirmCreate = () => void controller.submit().then(settle);
  const inspect = async (backupID: string) => {
    const value = await session.runProtected(async (context) => {
      const response = await fetch(`/api/v1/backups/${backupID}`, {
        method: "GET",
        headers: { "X-CSRF-Token": context.csrfToken },
        credentials: "same-origin",
        redirect: "error",
        signal: context.signal,
      });
      if (await context.sessionLost(response)) return undefined;
      if (
        response.status !== 200 ||
        response.headers.get("Content-Type") !== "application/json"
      )
        throw new Error("Backup detail is unavailable.");
      return decodeBackup((await response.json()) as unknown);
    });
    setDetail(value);
  };
  const beginDelete = (backup: Backup) => {
    setNotice(undefined);
    setDeleting(backup);
    const spec: MutationSpec<Backup | undefined> = {
      route: `/api/v1/backups/${backup.id}`,
      method: "DELETE",
      body: "{}",
      precondition: null,
      requiresPrecondition: false,
      idempotency: "none",
      successStatuses: [204],
      decode: decodeDelete,
    };
    controller.begin(spec);
    controller.confirm();
  };
  const cancelDelete = () => {
    setDeleting(undefined);
    controller.abandon();
  };
  const confirmDelete = async () => {
    setDeleting(undefined);
    await settle(await controller.submit());
  };
  if (createMode)
    return (
      <section
        class="panel domain-panel"
        aria-labelledby="backup-create-title"
        data-testid="backup-create-view"
      >
        <div class="panel-heading">
          <div>
            <h2 id="backup-create-title">Create backup</h2>
          </div>
        </div>
        <p>
          A backup is an immutable owner-only recovery artifact. Restore remains
          a stopped-process operation documented in the operator guide.
        </p>
        {mutation.problem !== undefined && (
          <StateNotice state="error" title={mutation.problem.title} />
        )}
        {mutation.state === "uncertain" && (
          <StateNotice state="warning" title="Backup outcome is unknown">
            <p>
              Nothing is replayed automatically. Return to backup inventory and
              inspect current records before acting again.
            </p>
            {mutation.canReplay && (
              <button
                data-testid="backup-replay"
                type="button"
                onClick={() => void controller.replay().then(settle)}
              >
                Replay this same backup create
              </button>
            )}
          </StateNotice>
        )}
        <div class="inline-actions">
          <a class="button-link" href="#/system?tab=backups">
            Cancel
          </a>
          <button
            ref={createButton}
            class="create-action"
            data-testid="backup-review-create"
            type="button"
            disabled={disabled}
            onClick={create}
          >
            Review and create
          </button>
        </div>
        <ConfirmationDialog
          id="backup-create-confirm"
          open={mutation.state === "confirming"}
          title="Review backup"
          consequence="Create one immutable owner-only backup artifact. Restore remains a separate stopped-process operation."
          confirmLabel="Create backup"
          returnFocus={createButton}
          onCancel={() => controller.abandon()}
          onConfirm={confirmCreate}
        />
      </section>
    );
  return (
    <section
      class="panel domain-panel"
      aria-labelledby="backups-title"
      data-testid="backups-view"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">DURABLE RECOVERY</span>
          <h2 id="backups-title">Backups</h2>
        </div>
        <StatusLabel state={panelStatus === "error" ? "error" : panelStatus}>
          {sentenceCase(panelStatus)}
        </StatusLabel>
      </div>
      <p>
        Create publishes one owner-only artifact. The browser cannot restore,
        reset, verify, or clear a storage latch.
      </p>
      <div class="collection-toolbar">
        <a
          class="button-link create-action"
          data-testid="backup-create"
          href="#/system/backups/new"
        >
          Create backup
        </a>
      </div>
      {mutation.availability === "storage_latched" && (
        <StateNotice state="error" title="Storage mutation is closed">
          <p>
            Reads remain available. Use stopped verification and recovery
            guidance; navigation cannot clear the latch.
          </p>
        </StateNotice>
      )}
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title} />
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Backup outcome is unknown">
          <p>
            Nothing is replayed automatically. Refresh records before choosing
            an explicit same-intent replay.
          </p>
          {mutation.canReplay && (
            <button
              data-testid="backup-replay"
              type="button"
              onClick={() => void controller.replay().then(settle)}
            >
              Replay this same backup create
            </button>
          )}
        </StateNotice>
      )}
      {notice !== undefined && <StateNotice state="empty" title={notice} />}
      {panelStatus === "error" && backups === undefined ? (
        <StateNotice state="error" title="Backups unavailable" />
      ) : (panelStatus === "loading" && panel?.hasValue !== true) ||
        backups === undefined ? (
        <StateNotice state="loading" title="Loading backups" />
      ) : backups.length === 0 ? (
        <StateNotice state="empty" title="No backups" />
      ) : (
        <ComparisonTable caption="Published backup artifacts">
          <thead>
            <tr>
              <th scope="col">Backup</th>
              <th scope="col">Source</th>
              <th scope="col">Size</th>
              <th scope="col">Actions</th>
            </tr>
          </thead>
          <tbody>
            {[...backups]
              .sort((left, right) =>
                right.createdAt.localeCompare(left.createdAt),
              )
              .map((backup) => (
                <tr key={backup.id} data-testid="backup-row">
                  <th scope="row">
                    <code>{backup.id}</code> ·{" "}
                    <UserTime value={backup.createdAt} />
                  </th>
                  <td>
                    Schema {backup.schemaVersion} · revision{" "}
                    {backup.sourceRevision}
                  </td>
                  <td>{backup.sizeBytes} bytes</td>
                  <td class="inline-actions">
                    <button
                      data-testid="backup-inspect"
                      type="button"
                      onClick={() => void inspect(backup.id)}
                    >
                      Inspect
                    </button>
                    <button
                      ref={deleteButton}
                      class="danger-action"
                      data-testid="backup-delete"
                      type="button"
                      disabled={disabled}
                      onClick={() => beginDelete(backup)}
                    >
                      Delete
                    </button>
                  </td>
                </tr>
              ))}
          </tbody>
        </ComparisonTable>
      )}
      {detail !== undefined && (
        <section class="subpanel" data-testid="backup-detail">
          <h3>Backup {detail.id}</h3>
          <dl class="fact-grid">
            <div>
              <dt>Installation</dt>
              <dd>{detail.installationID}</dd>
            </div>
            <div>
              <dt>Schema</dt>
              <dd>{detail.schemaVersion}</dd>
            </div>
            <div>
              <dt>Source revision</dt>
              <dd>{detail.sourceRevision}</dd>
            </div>
            <div>
              <dt>SHA-256</dt>
              <dd>
                <code>{detail.sha256}</code>
              </dd>
            </div>
          </dl>
        </section>
      )}
      <ConfirmationDialog
        id="backup-delete-confirm"
        open={deleting !== undefined && mutation.state === "confirming"}
        title="Delete backup artifact?"
        consequence={
          <p>
            Backup {deleting?.id} is permanently removed. This does not restore,
            reset, or change the running database.
          </p>
        }
        confirmLabel="Delete backup"
        destructive
        returnFocus={deleteButton}
        onCancel={cancelDelete}
        onConfirm={() => void confirmDelete()}
      />
    </section>
  );
}

function AdminCredentials({
  session,
  credentials,
  view,
  mutations,
  sinks,
  onRefresh,
  createMode,
}: {
  session: SessionClient;
  credentials: AdminCredential[] | undefined;
  view: ViewSnapshot;
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  onRefresh: () => void;
  createMode: boolean;
}) {
  const [controller] = useState<
    MutationController<CreatedAdminCredential | undefined>
  >(() => mutations.create<CreatedAdminCredential | undefined>());
  const [mutation, setMutation] = useState<MutationSnapshot>(() =>
    controller.snapshot(),
  );
  const [expiry, setExpiry] = useState("");
  const [prepared, setPrepared] = useState<PreparedOneTimeSink>();
  const [intent, setIntent] = useState<"create" | "revoke">("create");
  const [revoke, setRevoke] = useState<AdminCredential>();
  const [detail, setDetail] = useState<AdminCredential>();
  const [notice, setNotice] = useState<string>();
  const [expiryError, setExpiryError] = useState<string>();
  useUnsavedChanges(expiry !== "");
  const createButton = useRef<HTMLButtonElement>(null);
  const revokeButton = useRef<HTMLButtonElement>(null);
  useEffect(() => controller.subscribe(setMutation), [controller]);
  useEffect(() => () => controller.close(), [controller]);
  useEffect(() => () => prepared?.cancel(), [prepared]);
  useEffect(() => {
    if (detail !== undefined && credentials !== undefined)
      setDetail(credentials.find((credential) => credential.id === detail.id));
  }, [credentials]);
  const panel = view.panels["admin-credentials"];
  const panelStatus = panel?.status ?? "loading";
  const activeNonExpiring =
    credentials?.filter(
      (credential) => credential.status === "active" && credential.nonExpiring,
    ).length ?? 0;
  const disabled =
    mutation.state === "submitting" ||
    mutation.availability === "storage_latched";

  const decodeCreate = async (response: Response) => {
    if (response.headers.get("Content-Type") !== "application/json")
      throw new Error("invalid response");
    return decodeCreatedCredential((await response.json()) as unknown);
  };
  const decodeRevoke = async (response: Response) => {
    if (response.status !== 204) throw new Error("invalid response");
    return undefined;
  };
  const beginCreate = () => {
    setNotice(undefined);
    setExpiryError(undefined);
    let expiresAt: string | null = null;
    if (expiry !== "") {
      const timestamp = Date.parse(expiry);
      const delta = timestamp - Date.now();
      if (
        !Number.isFinite(timestamp) ||
        delta < 5 * 60_000 ||
        delta > 365 * 24 * 60 * 60_000
      ) {
        setExpiryError(
          "Expiry must be an RFC 3339 time from 5 minutes through 365 days in the future.",
        );
        return;
      }
      expiresAt = new Date(timestamp).toISOString();
    }
    setIntent("create");
    setPrepared(undefined);
    const spec: MutationSpec<CreatedAdminCredential | undefined> = {
      route: "/api/v1/admin-credentials",
      method: "POST",
      body: JSON.stringify({ expires_at: expiresAt }),
      precondition: null,
      requiresPrecondition: false,
      idempotency: "none",
      successStatuses: [201],
      decode: decodeCreate,
    };
    controller.begin(spec);
    controller.confirm();
  };
  const confirmCreate = async () => {
    const sink = sinks.prepareOneTime("New administrator bearer");
    if (sink === undefined) {
      setNotice(
        "The protected one-time display could not be prepared. No credential was created.",
      );
      controller.abandon();
      return;
    }
    const outcome = await controller.submit();
    if (outcome.kind === "acknowledged" && outcome.value !== undefined) {
      const publication = sink.publish(outcome.value.bearer);
      setNotice(
        publication === "published"
          ? "The one-time administrator bearer is ready. It cannot be revealed again."
          : "The created credential may be active, but its bearer was lost and cannot be recovered. Review credential metadata and explicitly revoke it if unusable before creating a deliberate replacement. Nothing was replayed.",
      );
      setPrepared(undefined);
      setExpiry("");
      controller.abandon();
      onRefresh();
      return;
    }
    if (outcome.kind === "uncertain") sink.lose();
    else sink.cancel();
    setPrepared(undefined);
  };
  const inspectCredential = async (credentialID: string) => {
    setNotice(undefined);
    const value = await session.runProtected(async (context) => {
      const response = await fetch(
        `/api/v1/admin-credentials/${credentialID}`,
        {
          method: "GET",
          headers: { "X-CSRF-Token": context.csrfToken },
          credentials: "same-origin",
          redirect: "error",
          signal: context.signal,
        },
      );
      if (await context.sessionLost(response)) return undefined;
      if (
        response.status !== 200 ||
        response.headers.get("Content-Type") !== "application/json"
      )
        throw new Error("Administrator credential detail is unavailable.");
      return decodeCredential((await response.json()) as unknown);
    });
    setDetail(value);
  };
  const beginRevoke = (credential: AdminCredential) => {
    setNotice(undefined);
    setIntent("revoke");
    setRevoke(credential);
    const spec: MutationSpec<CreatedAdminCredential | undefined> = {
      route: `/api/v1/admin-credentials/${credential.id}`,
      method: "DELETE",
      body: "{}",
      precondition: null,
      requiresPrecondition: false,
      idempotency: "none",
      successStatuses: [204],
      decode: decodeRevoke,
    };
    controller.begin(spec);
    controller.confirm();
  };
  const cancelRevoke = () => {
    setRevoke(undefined);
    controller.abandon();
  };
  const confirmRevoke = async () => {
    const outcome = await controller.submit();
    setRevoke(undefined);
    if (outcome.kind === "acknowledged") {
      setNotice(
        "Administrator credential revoked. Every child browser session authenticated by it is closed.",
      );
      controller.abandon();
      onRefresh();
    }
  };

  if (createMode)
    return (
      <section
        class="panel domain-panel"
        aria-labelledby="admin-credential-create-title"
        data-testid="admin-credential-create-view"
      >
        <div class="panel-heading">
          <div>
            <h2 id="admin-credential-create-title">Create admin credential</h2>
          </div>
        </div>
        <p>
          Gateway creates a permanent credential identity and displays its
          bearer once. The expiry cannot be changed after creation.
        </p>
        <FormField
          id="admin-credential-expiry"
          label="Expiry (RFC 3339)"
          hint="Blank creates non-expiring authority. Expiry must be 5 minutes through 365 days ahead."
          optional
          {...(expiryError === undefined ? {} : { error: expiryError })}
        >
          {(attributes) => (
            <input
              {...attributes}
              data-testid="admin-credential-expiry"
              value={expiry}
              disabled={disabled}
              placeholder="2030-01-01T00:00:00Z"
              onInput={(event) => {
                setExpiry(event.currentTarget.value);
                setExpiryError(undefined);
              }}
            />
          )}
        </FormField>
        {mutation.problem !== undefined && (
          <StateNotice state="error" title={mutation.problem.title} />
        )}
        {notice !== undefined && <StateNotice state="empty" title={notice} />}
        {mutation.state === "uncertain" && (
          <StateNotice state="warning" title="Credential outcome is unknown">
            <p>
              Do not replay. The credential may be active while its bearer is
              permanently lost. Refresh metadata, then explicitly revoke an
              unusable credential before creating a deliberate replacement.
            </p>
          </StateNotice>
        )}
        <div class="inline-actions">
          <a class="button-link" href="#/system?tab=admin-credentials">
            Cancel
          </a>
          <button
            ref={createButton}
            class="create-action"
            data-testid="admin-credential-create"
            type="button"
            disabled={disabled}
            onClick={beginCreate}
          >
            Review and create
          </button>
        </div>
        <ConfirmationDialog
          id="admin-credential-create-confirm"
          open={mutation.state === "confirming" && intent === "create"}
          title="Review admin credential"
          consequence={
            <div class="review-stack">
              <p>
                Create one administrator authority whose bearer is displayed
                once.
              </p>
              <dl>
                <dt>Expiry</dt>
                <dd>{expiry === "" ? "Non-expiring" : expiry}</dd>
              </dl>
            </div>
          }
          confirmLabel="Create admin credential"
          returnFocus={createButton}
          onCancel={() => controller.abandon()}
          onConfirm={() => void confirmCreate()}
        />
      </section>
    );

  return (
    <section
      class="panel domain-panel"
      aria-labelledby="admin-credentials-title"
      data-testid="admin-credentials-view"
      data-panel-status={panelStatus}
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">ADMIN AUTHORITY</span>
          <h2 id="admin-credentials-title">Admin credentials</h2>
        </div>
        <StatusLabel state={panelStatus === "error" ? "error" : panelStatus}>
          {sentenceCase(panelStatus)}
        </StatusLabel>
      </div>
      <p>
        Bearers appear once in the protected display. Persist only fingerprints
        and metadata; a bearer cannot be recovered or shown again.
      </p>
      <div class="collection-toolbar">
        <a
          class="button-link create-action"
          data-testid="admin-credential-create"
          href="#/system/admin-credentials/new"
        >
          Create admin credential
        </a>
      </div>
      {mutation.problem !== undefined && (
        <StateNotice state="error" title={mutation.problem.title}>
          <p>No force or silent overwrite path is available.</p>
        </StateNotice>
      )}
      {mutation.state === "uncertain" && (
        <StateNotice state="warning" title="Credential outcome is unknown">
          <p>
            {intent === "create"
              ? "Do not replay. The credential may be active while its bearer is permanently lost. Refresh metadata, then explicitly revoke an unusable credential before creating a deliberate replacement."
              : "Do not replay revoke. The credential may already be revoked and child sessions may already be closed. Refresh metadata before another explicit action."}
          </p>
        </StateNotice>
      )}
      {notice !== undefined && <StateNotice state="empty" title={notice} />}
      {panelStatus === "error" && credentials === undefined ? (
        <StateNotice
          state="error"
          title="Administrator credentials unavailable"
        />
      ) : (panelStatus === "loading" && panel?.hasValue !== true) ||
        credentials === undefined ? (
        <StateNotice
          state="loading"
          title="Loading administrator credentials"
        />
      ) : credentials.length === 0 ? (
        <StateNotice state="empty" title="No administrator credentials" />
      ) : (
        <CollectionTable
          caption="Admin credentials"
          items={[...credentials].sort((left, right) =>
            right.createdAt.localeCompare(left.createdAt),
          )}
          rowKey={(credential) => credential.id}
          rowTestID="admin-credential-row"
          filters={[
            {
              key: "identity",
              label: "Fingerprint or ID",
              type: "text",
              value: (credential) =>
                `${credential.fingerprint} ${credential.id}`,
            },
            {
              key: "status",
              label: "Status",
              type: "select",
              value: (credential) => credential.status,
              options: [
                { value: "active", label: "Active" },
                { value: "revoked", label: "Revoked" },
                { value: "expired", label: "Expired" },
              ],
            },
          ]}
          columns={[
            {
              key: "fingerprint",
              label: "Fingerprint",
              render: (credential) => (
                <button
                  class="text-button"
                  data-testid="admin-credential-inspect"
                  type="button"
                  onClick={() => void inspectCredential(credential.id)}
                >
                  {credential.fingerprint}
                </button>
              ),
              sortValue: (credential) => credential.fingerprint,
            },
            {
              key: "id",
              label: "ID",
              render: (credential) => <code>{credential.id}</code>,
              sortValue: (credential) => credential.id,
            },
            {
              key: "status",
              label: "Status",
              render: (credential) => (
                <StatusLabel
                  state={
                    credential.status === "active" ? "current" : "unavailable"
                  }
                >
                  {sentenceCase(credential.status)}
                </StatusLabel>
              ),
              sortValue: (credential) => credential.status,
            },
            {
              key: "created",
              label: "Created",
              render: (credential) => <UserTime value={credential.createdAt} />,
              sortValue: (credential) => credential.createdAt,
            },
            {
              key: "expires",
              label: "Expires",
              render: (credential) => (
                <UserTime
                  value={credential.expiresAt}
                  fallback="Non-expiring"
                />
              ),
              sortValue: (credential) => credential.expiresAt ?? "",
            },
            {
              key: "actions",
              label: "Actions",
              render: (credential) => {
                const protectedLast =
                  credential.status === "active" &&
                  credential.nonExpiring &&
                  activeNonExpiring <= 1;
                return credential.status === "active" ? (
                  <button
                    class="danger-action"
                    data-testid="admin-credential-revoke"
                    type="button"
                    disabled={disabled || protectedLast}
                    title={
                      protectedLast
                        ? "The last active non-expiring administrator authority cannot be revoked."
                        : undefined
                    }
                    onClick={(event) => {
                      revokeButton.current = event.currentTarget;
                      beginRevoke(credential);
                    }}
                  >
                    Revoke
                  </button>
                ) : (
                  "Terminal"
                );
              },
            },
          ]}
        />
      )}
      {detail !== undefined && (
        <section class="subpanel" data-testid="admin-credential-detail">
          <h3>Credential {detail.fingerprint}</h3>
          <dl class="fact-grid">
            <div>
              <dt>Permanent ID</dt>
              <dd>
                <code>{detail.id}</code>
              </dd>
            </div>
            <div>
              <dt>Status</dt>
              <dd>{sentenceCase(detail.status)}</dd>
            </div>
            <div>
              <dt>Created</dt>
              <dd>
                <UserTime value={detail.createdAt} />
              </dd>
            </div>
            <div>
              <dt>Expiry</dt>
              <dd>
                <UserTime value={detail.expiresAt} fallback="Non-expiring" />
              </dd>
            </div>
            <div>
              <dt>Revision</dt>
              <dd>{detail.revision}</dd>
            </div>
          </dl>
        </section>
      )}
      <StateNotice state="warning" title="Revocation closes child sessions">
        <p>
          Revoking authority closes every browser session authenticated by that
          credential, including this session when it is a child. The Gateway
          refuses removal of the last active non-expiring authority.
        </p>
      </StateNotice>
      <ConfirmationDialog
        id="admin-credential-revoke-confirm"
        open={revoke !== undefined && mutation.state === "confirming"}
        title="Revoke administrator credential?"
        consequence={
          <p>
            Credential {revoke?.fingerprint} stops authenticating and all of its
            child browser sessions close. This cannot be forced or undone.
          </p>
        }
        confirmLabel="Revoke administrator credential"
        destructive
        returnFocus={revokeButton}
        onCancel={cancelRevoke}
        onConfirm={() => void confirmRevoke()}
      />
    </section>
  );
}

export function System({
  controller,
  session,
  mutations,
  sinks,
  view,
  onRefresh,
}: {
  controller: SystemController;
  session: SessionClient;
  mutations: MutationCoordinator;
  sinks: SensitiveSinkCoordinator;
  view: ViewSnapshot;
  onRefresh: () => void;
}) {
  const [status, setStatus] = useState(controller.snapshot());
  const [credentials, setCredentials] = useState(
    controller.credentialSnapshot(),
  );
  const [backups, setBackups] = useState(controller.backupSnapshot());
  useEffect(() => controller.subscribe(setStatus), [controller]);
  useEffect(
    () => controller.subscribeCredentials(setCredentials),
    [controller],
  );
  useEffect(() => controller.subscribeBackups(setBackups), [controller]);
  const current = tab(view.viewKey);
  return (
    <div class="system-view" data-testid="system-view">
      <SystemTabs current={current} />
      {current === "status" ? (
        <StatusPanel status={status} view={view} />
      ) : current === "resource-limits" ? (
        <ResourceLimits status={status} />
      ) : current === "admin-credentials" ? (
        <AdminCredentials
          session={session}
          credentials={credentials}
          view={view}
          mutations={mutations}
          sinks={sinks}
          onRefresh={onRefresh}
          createMode={view.viewKey === "#/system/admin-credentials/new"}
        />
      ) : current === "backups" ? (
        <Backups
          session={session}
          backups={backups}
          view={view}
          mutations={mutations}
          onRefresh={onRefresh}
          createMode={view.viewKey === "#/system/backups/new"}
        />
      ) : (
        <section class="panel" aria-labelledby="system-later-title">
          <div class="panel-heading">
            <div>
              <span class="panel-code">SYSTEM-LATER</span>
              <h2 id="system-later-title">Workflow not yet available</h2>
            </div>
            <StatusLabel state="unavailable">Unavailable</StatusLabel>
          </div>
          <p>This System workflow is reserved for a later delivery slice.</p>
        </section>
      )}
    </div>
  );
}
