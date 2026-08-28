import { useEffect, useState } from "preact/hooks";
import { decodeStatus, type LimitView, type StatusView } from "./overview";
import { ComparisonTable, StateNotice, StatusLabel } from "./primitives";
import type { SessionClient } from "./session";
import type { ViewCoordinator, ViewSnapshot } from "./view";

type Listener = (status: StatusView | undefined) => void;
type SystemTab = "status" | "admin-credentials" | "backups" | "recovery";

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
  if (viewKey === "#/system?tab=admin-credentials") return "admin-credentials";
  if (viewKey === "#/system?tab=backups") return "backups";
  if (viewKey === "#/system?tab=recovery") return "recovery";
  return "status";
}

export class SystemController {
  private readonly listeners = new Set<Listener>();
  private value: StatusView | undefined;

  constructor(
    session: SessionClient,
    views: ViewCoordinator,
    setStorageLatched: (latched: boolean) => void,
  ) {
    views.registerPanel({
      id: "system-status",
      matches: (viewKey) =>
        viewKey === "#/system" || viewKey === "#/system?tab=recovery",
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
      setStorageLatched(false);
      this.emit();
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
    [
      "admin-credentials",
      "Admin credentials",
      "#/system?tab=admin-credentials",
    ],
    ["backups", "Backups", "#/system?tab=backups"],
    ["recovery", "Stopped recovery", "#/system?tab=recovery"],
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
          <h2 id="system-status-title">Runtime and durable posture</h2>
        </div>
        <StatusLabel state={panelStatus === "error" ? "error" : panelStatus}>
          {panelStatus}
        </StatusLabel>
      </div>
      {panelStatus === "error" && status === undefined ? (
        <StateNotice state="error" title="System status unavailable">
          <p>
            Check Gateway availability, then refresh this authoritative read.
          </p>
        </StateNotice>
      ) : status !== undefined ? (
        <div class="system-stack">
          {status.latched && (
            <StateNotice state="error" title="Storage mutation is closed">
              <p>
                Online mutations remain closed. Reads can remain available, but
                elapsed time and successful reads cannot clear the latch.
              </p>
              <a href="#/system?tab=recovery">Open stopped-recovery guidance</a>
            </StateNotice>
          )}
          <div class="fact-grid">
            <article class="fact-card">
              <span class="panel-code">PROCESS</span>
              <h3>Process {status.processState}</h3>
              <StatusLabel state={status.ready ? "current" : "warning"}>
                Ready {status.ready ? "yes" : "no"}
              </StatusLabel>
              <p>Started {status.startedAt}</p>
            </article>
            <article class="fact-card">
              <span class="panel-code">SQLITE</span>
              <h3>Storage {status.sqliteState}</h3>
              <p>Schema {status.schemaVersion}</p>
              <p>Revision {status.revision}</p>
              <StatusLabel state={status.latched ? "error" : "current"}>
                Mutation admission {status.latched ? "closed" : "open"}
              </StatusLabel>
            </article>
            <article class="fact-card">
              <span class="panel-code">KEYRING</span>
              <h3>Keyring {status.keyring}</h3>
              <p>
                OS-managed capability snapshot; later authority work may still
                require interaction or fail.
              </p>
            </article>
            <article class="fact-card">
              <span class="panel-code">BACKUP</span>
              <h3>Backup {status.backupState}</h3>
              <p>Last completed {status.lastBackupAt ?? "never"}</p>
            </article>
            <article class="fact-card fact-card-wide">
              <span class="panel-code">PROTOCOLS</span>
              <h3>Agent authentication {status.agentAuth}</h3>
              <p>
                modern {status.modernProtocol} · legacy {status.legacyProtocol}
              </p>
            </article>
          </div>
          <ComparisonTable caption="Gateway resource occupancy and hard limits">
            <thead>
              <tr>
                <th scope="col">Resource</th>
                <th scope="col">In use</th>
                <th scope="col">Limit</th>
                <th scope="col">State</th>
              </tr>
            </thead>
            <tbody>
              {status.limits.map((limit) => (
                <tr key={limit.name} data-testid="system-limit-row">
                  <th scope="row">{limit.name}</th>
                  <td>{limit.inUse}</td>
                  <td>{limit.limit}</td>
                  <td>
                    <StatusLabel state={stateForLimit(limit)}>
                      {limit.saturated ? "saturated" : "available"}
                    </StatusLabel>
                  </td>
                </tr>
              ))}
            </tbody>
          </ComparisonTable>
        </div>
      ) : (
        <StateNotice state="loading" title="Loading system status" />
      )}
    </section>
  );
}

function Recovery({ status }: { status: StatusView | undefined }) {
  return (
    <section
      class="panel recovery-panel"
      aria-labelledby="system-recovery-title"
      data-testid="system-recovery"
    >
      <div class="panel-heading">
        <div>
          <span class="panel-code">RECOVERY-STOPPED</span>
          <h2 id="system-recovery-title">Stopped-process recovery boundary</h2>
        </div>
        <span class="classification">CLI ONLY</span>
      </div>
      {status?.latched === true && (
        <StateNotice state="error" title="Current status is latched">
          <p>
            A latched status does not prove whether the triggering write
            committed or rolled back.
          </p>
        </StateNotice>
      )}
      <p>
        Stop every Gateway process that owns the installation before using any
        command below. Each command acquires exclusive stopped-process
        ownership. The browser never invokes these commands.
      </p>
      <div class="recovery-grid">
        <article class="recovery-step">
          <span class="panel-code">NEW INSTALLATION</span>
          <h3>Initialize authority</h3>
          <p>
            Create the installation and publish its first admin bearer once.
          </p>
          <pre tabindex={0}>
            <code>
              mcp-gateway initialize --data-dir &lt;owner-only-data-dir&gt;
              --secret-output &lt;new-owner-only-file&gt;
            </code>
          </pre>
        </article>
        <article class="recovery-step">
          <span class="panel-code">AUTHORITY LOSS</span>
          <h3>Reset administrator authority</h3>
          <p>
            Revoke every prior active admin verifier and publish one
            replacement.
          </p>
          <pre tabindex={0}>
            <code>
              mcp-gateway admin-reset --data-dir &lt;owner-only-data-dir&gt;
              --secret-output &lt;new-owner-only-file&gt;
            </code>
          </pre>
        </article>
        <article class="recovery-step">
          <span class="panel-code">INTEGRITY / LATCH</span>
          <h3>Verify the current generation</h3>
          <p>
            Verify current identity, schema, migration history, durability,
            bounds, integrity, and any recognized recovery marker before latch
            clearing.
          </p>
          <pre tabindex={0}>
            <code>
              mcp-gateway restore --verify-current --data-dir
              &lt;owner-only-data-dir&gt;
            </code>
          </pre>
        </article>
        <article class="recovery-step">
          <span class="panel-code">BACKUP REPLACEMENT</span>
          <h3>Restore one verified backup</h3>
          <p>
            Verify and select the named backup while replacing all restored
            administrator authority with one newly published bearer.
          </p>
          <pre tabindex={0}>
            <code>
              mcp-gateway restore &lt;backup-id&gt; --data-dir
              &lt;owner-only-data-dir&gt; --secret-output
              &lt;new-owner-only-file&gt;
            </code>
          </pre>
        </article>
      </div>
      <StateNotice state="warning" title="Verification is not readiness">
        <p>
          Normal serve startup must verify the selected generation before it can
          become ready. Recovery output proves only the command's acknowledged
          result; it does not infer the fate of another uncertain write.
        </p>
      </StateNotice>
    </section>
  );
}

export function System({
  controller,
  view,
  onRefresh,
}: {
  controller: SystemController;
  view: ViewSnapshot;
  onRefresh: () => void;
}) {
  const [status, setStatus] = useState(controller.snapshot());
  useEffect(() => controller.subscribe(setStatus), [controller]);
  const current = tab(view.viewKey);
  return (
    <div class="system-view" data-testid="system-view">
      <SystemTabs current={current} />
      {(current === "status" || current === "recovery") && (
        <div class="refresh-controls system-refresh">
          <StatusLabel state={view.freshness}>
            Data {view.freshness}
          </StatusLabel>
          <button
            data-testid="manual-refresh"
            type="button"
            onClick={onRefresh}
          >
            Refresh visible data
          </button>
        </div>
      )}
      {current === "status" ? (
        <StatusPanel status={status} view={view} />
      ) : current === "recovery" ? (
        <Recovery status={status} />
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
