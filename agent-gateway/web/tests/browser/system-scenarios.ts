import { captureScreenshot, hasCaptureOwner } from "../frontend/capture.ts";
import { captureStateFeedback } from "./state-feedback.ts";
import { activityFixture } from "../recorded-activity-fixture.ts";
import {
  captureOverviewLayout,
  prepareOverviewBaseline,
} from "./overview-layout.ts";
import {
  captureDetailLayout,
  prepareDetailBaseline,
  captureTableState,
} from "./detail-layout.ts";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { capture as captureFrontend } from "../frontend/capture.ts";
import AxeBuilder from "@axe-core/playwright";
import { assertTableConventions } from "./table-conventions.ts";
import { expect, type BrowserContext, type Page } from "@playwright/test";
import {
  assertSecretAbsent,
  browserStorage,
  eventually,
  fail,
  waitForLifecycle,
} from "./shared.ts";
import {
  invocationFixture,
  invocationIDs,
  overviewInvocationFixture,
  overviewLimitNames,
  overviewRequestFixture,
  overviewServer,
  overviewStatusFixture,
} from "./fixtures.ts";

export async function runOverviewInvocationSystemCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.waitForFunction(() =>
    ["overview-status", "overview-servers", "overview-requests"].every(
      (id) =>
        document
          .querySelector(`[data-testid="${id}"]`)
          ?.getAttribute("data-panel-status") === "current",
    ),
  );
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Gateway readiness",
    "Active MCP catalog tools",
    "Configured MCP servers",
    "Pending MCP decisions",
  ])
    if (!body.includes(phrase))
      fail(`Overview workflow canary omitted ${phrase}`);
  if (body.includes("redacted_arguments"))
    fail("Overview workflow canary exposed invocation capture");

  await page.locator('#primary-navigation a[href="#/mcp/invocations"]').click();
  await page.locator('[data-testid="invocations-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="invocations-view"]')
        ?.textContent?.includes("No retained invocations") === true,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes("Live updates on") ||
    body.includes("Live updates paused") ||
    !(await page.getByRole("switch", { name: "Live mode" }).isChecked()) ||
    body.includes("retains at most 4,096 recent rows") ||
    body.includes("Bounded invocation evidence") ||
    body.includes("redacted_arguments")
  )
    fail("Invocation workflow canary exposed redundant copy or capture");

  await page.evaluate(() => {
    window.location.hash = "#/system";
  });
  await page.locator('[data-testid="system-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (
    (await page.locator('[data-testid="system-limit-row"]').count()) !== 0 ||
    !body.includes("Gateway status") ||
    (await page.getByRole("link", { name: "Resource limits" }).count()) !== 1
  )
    fail("System status did not keep detailed limits in their destination");

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=resource-limits";
  });
  await page.locator('[data-testid="system-limits-view"]').waitFor();
  if ((await page.locator('[data-testid="system-limit-row"]').count()) !== 31)
    fail("Resource limits workflow omitted closed limits");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "overview_invocation_system_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: 4 })}\n`,
  );
}

export async function runSystemAdministrationCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  let domainMutations = 0;
  page.on("request", (request) => {
    if (
      request.method() !== "GET" &&
      /\/api\/v2\/(?:admin-credentials|backups)/.test(request.url())
    )
      domainMutations += 1;
  });
  await page.evaluate(() => {
    window.location.hash = "#/system";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  const destinations: Array<[string, string]> = [
    ["#/system", "system-status-panel"],
    ["#/system?tab=resource-limits", "system-limits-view"],
    ["#/system?tab=admin-credentials", "admin-credentials-view"],
    ["#/system?tab=backups", "backups-view"],
  ];
  let rendered = "";
  for (const [hash, testID] of destinations) {
    await page.evaluate((target) => {
      window.location.hash = target;
    }, hash);
    await page.locator(`[data-testid="${testID}"]`).waitFor();
    rendered += ` ${(await page.locator("body").textContent()) ?? ""}`;
  }
  for (const phrase of [
    "Gateway status",
    "Resource limits",
    "Admin credentials",
    "Backups",
  ])
    if (!rendered.includes(phrase))
      fail(`System administration canary omitted ${phrase}`);
  if (rendered.includes("Workflow not yet available"))
    fail("System administration canary retained a System placeholder");
  if (domainMutations !== 0)
    fail("System administration canary submitted a mutation");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "system_administration_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: destinations.length, mutations: domainMutations })}\n`,
  );
}

export async function runCapabilityAudit(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  let eventStreams = 0;
  let mutations = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/v2/events"))
      eventStreams += 1;
  });
  await page.route("**/api/v2/system-status", async (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(overviewStatusFixture()),
    }),
  );
  await page.route("**/api/v2/admin-credentials?*", async (route) => {
    if (route.request().method() !== "GET") mutations += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [], next_cursor: null }),
    });
  });
  await page.route("**/api/v2/backups?*", async (route) => {
    if (route.request().method() !== "GET") mutations += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [], next_cursor: null }),
    });
  });
  await page.route(
    "**/api/v2/events",
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": force reconnect\n\n",
      }),
    { times: 1 },
  );
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.waitForFunction(
    () =>
      (
        document.querySelector(
          '[data-testid="admin-credential-create"]',
        ) as HTMLButtonElement | null
      )?.disabled === true,
  );
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=backups";
  });
  await page.locator('[data-testid="backups-view"]').waitFor();
  await page.locator('[data-testid="backup-create"]').click();
  await page.waitForFunction(
    () =>
      (
        document.querySelector(
          '[data-testid="backup-review-create"]',
        ) as HTMLButtonElement | null
      )?.disabled === true,
  );
  if (mutations !== 0)
    fail("cross-destination storage latch submitted a mutation");
  const reconnectDeadline = Date.now() + 5000;
  while (eventStreams < 2 && Date.now() < reconnectDeadline)
    await new Promise((resolve) => setTimeout(resolve, 25));
  if (eventStreams < 2) fail("event stream did not reconnect");
  const body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("stopped-process operation") ||
    !body.includes("Storage mutation is closed")
  )
    fail("cross-destination latch guidance is incomplete");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "capability_audit_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), event_streams: eventStreams, mutations, destinations: 2 })}\n`,
  );
}

export async function runBackups(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const ids = ["01ARZ3NDEKTSV4RRFFQ69G5FB0", "01ARZ3NDEKTSV4RRFFQ69G5FB1"];
  const backup = (index: number) => ({
    ...(index === 1 ? { history: "omitted" } : {}),
    id: ids[index],
    created_at: `2026-08-2${8 + index}T12:00:00Z`,
    installation_id: "11111111-2222-3333-4444-555555555555",
    schema_version: "10",
    source_revision: String(index + 7),
    size_bytes: 4096 + index,
    sha256: String(index + 1).repeat(64),
  });
  let items = [backup(0)];
  let creates = 0;
  let deletes = 0;
  let details = 0;
  let backupReadFails = false;
  let recoveryKey: string | undefined;
  let exports = 0;
  await page.route("**/api/v2/history/export", async (route) => {
    exports += 1;
    if (route.request().method() !== "GET")
      fail("history export mutated state");
    if (exports === 2) {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "history_unavailable",
          title: "Optional history is unavailable.",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        format: 1,
        installation_id: ids[0],
        generation: ids[1],
        captured_at: "2026-08-29T12:00:00Z",
        high_water: "0",
        pruning: "0",
        retained: 0,
        after_sequence: "0",
        next_sequence: "0",
        truncated: false,
        complete_traffic_audit: false,
        absence:
          "Absent records do not establish nonexecution; missing completion remains unknown. Each response is a new snapshot of rolling best-effort history.",
        records: [],
      }),
    });
  });
  await page.route("**/api/v2/backups**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const id =
      url.pathname === "/api/v2/backups"
        ? undefined
        : url.pathname.split("/").pop();
    if (request.method() === "GET" && id === undefined) {
      if (url.searchParams.get("limit") !== "100")
        fail("backup list changed shape");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          backupReadFails ? {} : { items, next_cursor: null },
        ),
      });
      return;
    }
    if (request.method() === "GET" && id !== undefined) {
      details += 1;
      const item = items.find((candidate) => candidate.id === id);
      if (item === undefined) fail("unknown backup detail");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(item),
      });
      return;
    }
    if (request.method() === "POST" && id === undefined) {
      creates += 1;
      const headers = await request.allHeaders();
      const key = headers["idempotency-key"];
      if (request.postData() !== "{}" || key === undefined)
        fail("backup create changed shape");
      if (creates === 1) {
        recoveryKey = key;
        await route.abort("failed");
        return;
      }
      if (creates === 2) {
        if (key !== recoveryKey) fail("backup replay changed idempotency key");
        const created = backup(1);
        items = [...items, created];
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(created),
        });
        return;
      }
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    if (request.method() === "DELETE" && id !== undefined) {
      deletes += 1;
      const headers = await request.allHeaders();
      if (
        request.postData() !== "{}" ||
        headers["idempotency-key"] !== undefined
      )
        fail("backup delete changed shape");
      items = items.filter((item) => item.id !== id);
      if (deletes === 2) {
        await route.abort("failed");
        return;
      }
      await route.fulfill({ status: 204, body: "" });
      return;
    }
    fail("unexpected backup request");
  });

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=backups";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="backups-view"]').waitFor();
  if (
    (
      (await page.locator('[data-testid="backups-view"]').textContent()) ?? ""
    ).includes(
      "The browser cannot restore, reset, verify, or clear a storage latch.",
    )
  )
    fail("backup inventory retained redundant recovery guidance");
  await assertTableConventions(
    page,
    "Published backup artifacts",
    ["Backup", "Source", "Size", "Created", "Actions"],
    "Backup",
  );
  const inventory = page.locator('[data-testid="backups-view"]');
  await expect(
    inventory.locator(".panel-heading .status-label.current"),
  ).toHaveCount(0);
  await captureStateFeedback(page, "backups-current");
  const rows = inventory.locator('[data-testid="backup-row"]');
  const assertSimplifiedInventory = async () => {
    await expect(
      inventory.getByRole("button", { name: "Inspect" }),
    ).toHaveCount(0);
    await expect(
      inventory.locator('[data-testid="backup-detail"]'),
    ).toHaveCount(0);
    await expect(rows.locator("a, summary, [role=link]")).toHaveCount(0);
    await expect(rows.getByRole("button")).toHaveText(
      items.map(() => "Delete"),
    );
    expect(details).toBe(0);
  };
  await assertSimplifiedInventory();
  await expect(rows.first().getByRole("rowheader")).toHaveText(
    `Legacy backup${ids[0]}`,
  );
  await expect(rows.first().locator('[data-label="Source"]')).toHaveText(
    "Schema 10Revision 7",
  );
  await expect(rows.first().locator('[data-label="Size"]')).toHaveText(
    "4,096 bytes",
  );
  await expect(rows.first().locator("time")).toHaveAttribute(
    "datetime",
    backup(0).created_at,
  );
  expect(exports).toBe(0);
  const exportPanel = page.getByTestId("history-export");
  await exportPanel.locator("summary").click();
  await exportPanel.getByRole("button", { name: "Read export" }).click();
  await expect(exportPanel.getByLabel("History export JSON")).toContainText(
    '"complete_traffic_audit": false',
  );
  await captureFrontend(page, "backup-history-export");
  await exportPanel.getByRole("button", { name: "Read export" }).click();
  await expect(
    exportPanel.getByText("History export unavailable", { exact: true }),
  ).toBeVisible();
  await expect(exportPanel.getByLabel("History export JSON")).toHaveCount(0);
  await captureFrontend(page, "backup-history-unavailable");
  expect(exports).toBe(2);
  expect(creates).toBe(0);
  await exportPanel.locator("summary").click();
  await page.locator('[data-testid="backup-create"]').click();
  await captureFrontend(page, "backup-create");
  await page
    .locator('[data-testid="backup-create-view"]')
    .getByRole("link", { name: "Cancel", exact: true })
    .click();
  await expect(inventory).toBeVisible();
  expect(creates).toBe(0);
  await page.locator('[data-testid="backup-create"]').click();
  await page.locator('[data-testid="backup-review-create"]').click();
  await page.locator('[data-testid="backup-create-confirm-cancel"]').click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(
    page.locator('[data-testid="backup-review-create"]'),
  ).toBeFocused();
  await page.locator('[data-testid="backup-review-create"]').click();
  await captureFrontend(page, "backup-create-confirmation");
  if (Number(creates) !== 0) fail("backup submitted before final review");
  await page.locator('[data-testid="backup-create-confirm-submit"]').click();
  await page.getByText("Backup outcome is unknown", { exact: true }).waitFor();
  await captureFrontend(page, "backup-create-uncertain");
  if (creates !== 1) fail("uncertain backup create replayed automatically");
  await page.locator('[data-testid="backup-replay"]').click();
  await page.getByText(/is durably published/).waitFor();
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText("Security backup · history omitted");
  await expect(rows.first()).toContainText(ids[1]!);
  await inventory.getByRole("button", { name: "Size", exact: true }).click();
  await expect(rows.first()).toContainText(ids[0]!);
  await page.setViewportSize({ width: 390, height: 1000 });
  await inventory
    .locator(".table-sort-controls select")
    .selectOption("created");
  await expect(rows.first()).toContainText(ids[0]!);
  await inventory
    .getByRole("button", { name: "Published backup artifacts sort direction" })
    .click();
  await expect(rows.first()).toContainText(ids[1]!);
  await assertSimplifiedInventory();
  await page.locator('[data-testid="backup-delete"]').first().click();
  await expect(page.getByRole("dialog")).toContainText(ids[1]!);
  await captureTableState(page, "backup-delete-confirmation");
  await page.locator('[data-testid="backup-delete-confirm-cancel"]').click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(page.getByTestId("backup-delete").first()).toBeFocused();
  expect(deletes).toBe(0);
  await expect(rows).toHaveCount(2);
  backupReadFails = true;
  await page.getByTestId("manual-refresh").click();
  await expect(
    page.getByText("Last-known backups", { exact: true }),
  ).toBeVisible();
  await expect(rows).toHaveCount(2);
  await expect(
    page.getByText("2 backups (last-known)", { exact: true }),
  ).toBeVisible();
  await captureTableState(page, "backups-stale");
  backupReadFails = false;
  await page.getByTestId("manual-refresh").click();
  await expect(
    page.getByText("Last-known backups", { exact: true }),
  ).toHaveCount(0);
  await page.locator('[data-testid="backup-delete"]').first().click();
  await page.locator('[data-testid="backup-delete-confirm-submit"]').click();
  await page.getByText(/Backup deleted/).waitFor();
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText(ids[0]!);
  await page.locator('[data-testid="backup-delete"]').click();
  await page.locator('[data-testid="backup-delete-confirm-submit"]').click();
  await expect(
    inventory.getByText("Refresh backups before taking another action."),
  ).toBeVisible();
  await expect(page.getByTestId("backup-replay")).toHaveCount(0);
  await captureStateFeedback(page, "backup-delete-unknown");
  expect(deletes).toBe(2);
  await page.getByTestId("manual-refresh").click();
  await expect(
    inventory.getByText("No backups", { exact: true }),
  ).toBeVisible();
  await assertSimplifiedInventory();
  await page.locator('[data-testid="backup-create"]').click();
  await page.locator('[data-testid="backup-review-create"]').click();
  await page.locator('[data-testid="backup-create-confirm-submit"]').click();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-mutation-availability") === "storage_latched",
  );
  await captureFrontend(page, "backup-storage-latched");
  if (await page.locator('[data-testid="backup-review-create"]').isEnabled())
    fail("storage latch left backup mutation enabled");
  const body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of ["immutable owner-only", "stopped-process operation"])
    if (!body.includes(phrase)) fail(`backup boundary omitted ${phrase}`);
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "backups_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), creates, deletes, details })}\n`,
  );
}

export async function runAdminCredentials(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const ids = [
    "01ARZ3NDEKTSV4RRFFQ69G5FA0",
    "01ARZ3NDEKTSV4RRFFQ69G5FA1",
    "01ARZ3NDEKTSV4RRFFQ69G5FA2",
    "01ARZ3NDEKTSV4RRFFQ69G5FA3",
  ];
  const issuedBearer = `mgw_admin_${"I".repeat(43)}`;
  const lostBearer = `mgw_admin_${"L".repeat(43)}`;
  const credential = (
    index: number,
    status: "active" | "revoked" = "active",
    nonExpiring = true,
  ) => ({
    id: ids[index],
    fingerprint: `${String(index + 1).padStart(16, "0")}`,
    created_at: "2026-08-28T12:00:00Z",
    expires_at: nonExpiring ? null : "2026-09-28T12:00:00Z",
    non_expiring: nonExpiring,
    status,
    revision: String(index + 1),
  });
  let items = [credential(0), credential(1), credential(2, "active", false)];
  let creates = 0;
  let revokes = 0;
  let adminReadFails = false;
  let expectedExpiry: string | null = null;
  let releaseLost: (() => void) | undefined;
  let markLostStarted: (() => void) | undefined;
  const lostStarted = new Promise<void>((resolve) => {
    markLostStarted = resolve;
  });

  await page.route("**/api/v2/admin-credentials**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const id =
      path === "/api/v2/admin-credentials" ? undefined : path.split("/").pop();
    if (request.method() === "GET" && id === undefined) {
      const query = new URL(request.url()).searchParams;
      if (query.get("limit") !== "100")
        fail("admin credential list changed shape");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          adminReadFails ? {} : { items, next_cursor: null },
        ),
      });
      return;
    }
    if (request.method() === "POST" && id === undefined) {
      creates += 1;
      const body = JSON.parse(request.postData() ?? "null") as Record<
        string,
        unknown
      >;
      if (Object.keys(body).join(",") !== "expires_at")
        fail("admin credential create changed shape");
      if (body.expires_at !== (creates === 1 ? expectedExpiry : null))
        fail(
          "admin credential expiry did not serialize local time as UTC or preserve non-expiring state",
        );
      if (creates === 2) {
        markLostStarted?.();
        await new Promise<void>((resolve) => {
          releaseLost = resolve;
        });
      }
      if (creates === 3) {
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          body: "{",
        });
        return;
      }
      const created = credential(3);
      items = [...items, created];
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          ...created,
          bearer: creates === 1 ? issuedBearer : lostBearer,
        }),
      });
      return;
    }
    if (request.method() === "DELETE" && id !== undefined) {
      revokes += 1;
      if (request.postData() !== "{}")
        fail("admin credential revoke changed shape");
      if (revokes === 3) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: "{",
        });
        return;
      }
      if (id === ids[0]) {
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 409,
            code: "conflict",
            title: "The last active non-expiring authority cannot be revoked.",
          }),
        });
        return;
      }
      items = items.map((item) =>
        item.id === id
          ? { ...item, status: "revoked" as const, revision: "9" }
          : item,
      );
      await route.fulfill({ status: 204, body: "" });
      return;
    }
    fail("unexpected admin credential request");
  });

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  await expect(
    page
      .getByTestId("admin-credentials-view")
      .locator(".panel-heading .status-label.current"),
  ).toHaveCount(0);
  await captureStateFeedback(page, "admin-credentials-current");
  const inventoryCopy =
    (await page
      .locator('[data-testid="admin-credentials-view"]')
      .textContent()) ?? "";
  if (
    inventoryCopy.includes("Bearers appear once in the protected display.") ||
    inventoryCopy.includes("Revocation closes child sessions")
  )
    fail("admin credential inventory retained workflow-only guidance");
  if (
    (await page
      .getByRole("heading", {
        level: 2,
        name: "Admin credentials",
        exact: true,
      })
      .count()) !== 1
  )
    fail("Admin credentials title was inconsistent");
  const adminHeaders = await page
    .locator('[data-testid="admin-credentials-view"] thead th')
    .allInnerTexts();
  if (
    adminHeaders.map((value) => value.replace(/\s?[↑↓↕]$/, "")).join("|") !==
    "Credential|Status|Created|Expires|Actions"
  )
    fail(`Admin credential columns drifted: ${adminHeaders.join("|")}`);
  await assertTableConventions(
    page,
    "Admin credentials",
    ["Credential", "Status", "Created", "Expires", "Actions"],
    "Credential",
  );
  if (
    (await page.locator('[data-testid="admin-credential-inspect"]').count()) !==
      0 ||
    (await page.locator('[data-testid="admin-credential-detail"]').count()) !==
      0 ||
    (await page
      .locator('[data-testid="admin-credential-row"] th code')
      .count()) !== items.length
  )
    fail("admin credential fingerprints remained interactive or expanded");
  const adminStatus = page.getByRole("combobox", {
    name: "Status",
    exact: true,
  });
  await adminStatus.selectOption("expired");
  await expect(page.getByTestId("admin-credential-row")).toHaveCount(0);
  await expect(adminStatus).toBeVisible();
  await expect(page).toHaveURL(/#\/system\?tab=admin-credentials$/);
  await captureTableState(page, "admin-local-filter-empty");
  await page.getByRole("button", { name: "Reset", exact: true }).click();
  await expect(page.getByTestId("admin-credential-row")).toHaveCount(3);
  adminReadFails = true;
  await page.getByTestId("manual-refresh").click();
  await expect(
    page.getByText("Last-known administrator credentials", { exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("admin-credential-row")).toHaveCount(3);
  await expect(
    page.getByText("Showing 3 of 3 (last-known)", { exact: true }),
  ).toBeVisible();
  await captureTableState(page, "admin-stale");
  adminReadFails = false;
  await page.getByTestId("manual-refresh").click();
  await expect(
    page.getByText("Last-known administrator credentials", { exact: true }),
  ).toHaveCount(0);
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.locator('[data-testid="admin-credential-create-view"]').waitFor();
  if (
    (await page.getByTestId("admin-credential-expiry").getAttribute("type")) !==
    "datetime-local"
  )
    fail("admin credential expiry did not use a date/time control");
  await captureFrontend(page, "admin-credential-create");
  const expiryInput = page.getByTestId("admin-credential-expiry");
  await expiryInput.fill("2030-01-01T12:34:56");
  await expiryInput.press("Backspace");
  if (
    !(await expiryInput.evaluate(
      (input: HTMLInputElement) =>
        input.value === "" && input.validity.badInput,
    ))
  )
    fail("admin expiry fixture did not produce native incomplete input");
  await page.getByTestId("admin-credential-create").click();
  await page
    .getByText(
      "Choose a complete expiry date and time, or clear it for a non-expiring credential.",
      { exact: true },
    )
    .waitFor();
  if (
    creates !== 0 ||
    (await page
      .getByTestId("admin-credential-create-confirm-submit")
      .isVisible()) ||
    (await expiryInput.getAttribute("aria-invalid")) !== "true" ||
    !(await expiryInput.getAttribute("aria-describedby"))?.includes(
      "admin-credential-expiry-error",
    )
  )
    fail(
      "incomplete admin expiry was treated as blank or lacked associated error",
    );
  await expiryInput.fill("");
  await page.getByTestId("admin-credential-create").click();
  await page
    .locator("dialog[open]")
    .getByText("No expiry", { exact: true })
    .waitFor();
  await page.getByTestId("admin-credential-create-confirm-cancel").click();
  await page
    .locator('[data-testid="admin-credential-expiry"]')
    .fill("2000-01-01T12:34:56");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .getByText(
      "Choose an expiry from 5 minutes through 365 days in the future.",
      { exact: true },
    )
    .waitFor();
  await captureFrontend(page, "admin-credential-validation");
  if (creates !== 0) fail("invalid admin expiry reached the API");
  const localExpiry = await page.evaluate(() => {
    const future = new Date(Date.now() + 60 * 60_000);
    return new Date(future.getTime() - future.getTimezoneOffset() * 60_000)
      .toISOString()
      .slice(0, 19);
  });
  expectedExpiry = await page.evaluate(
    (value) => new Date(value).toISOString(),
    localExpiry,
  );
  await page
    .locator('[data-testid="admin-credential-expiry"]')
    .fill(localExpiry);
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .getByRole("heading", { name: "Review admin credential", exact: true })
    .waitFor();
  if (creates !== 0) fail("admin credential submitted before final review");
  await captureDetailLayout(page, "administrator-expiry-review");
  await page
    .locator('[data-testid="admin-credential-create-confirm-submit"]')
    .click();
  await page.locator('[data-testid="one-time-value"]').waitFor();
  if (
    (await page.locator('[data-testid="one-time-value"]').textContent()) !==
    issuedBearer
  )
    fail("admin bearer did not reach the prepared sink");
  await captureFrontend(page, "admin-one-time-bearer");
  await page.locator('[data-testid="copy-one-time-value"]').click();
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-revoke"]').first().click();
  await page
    .locator('[data-testid="admin-credential-revoke-confirm-submit"]')
    .click();
  await page
    .getByText("The last active non-expiring authority cannot be revoked.", {
      exact: true,
    })
    .waitFor();
  const expiringRow = page
    .locator('[data-testid="admin-credential-row"]')
    .filter({ hasText: ids[2]! });
  await expiringRow.locator('[data-testid="admin-credential-revoke"]').click();
  await page
    .locator('[data-testid="admin-credential-revoke-confirm-submit"]')
    .click();
  await page.getByText(/Administrator credential revoked/).waitFor();
  const body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("cannot be forced"))
    fail("admin credential consequence omitted cannot be forced");

  await page.locator('[data-testid="admin-credential-revoke"]').last().click();
  await page
    .locator('[data-testid="admin-credential-revoke-confirm-submit"]')
    .click();
  await page
    .getByText(
      "Do not replay revoke. The credential may already be revoked and child sessions may already be closed. Refresh metadata before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.evaluate(() => {
    window.location.hash = "#/overview";
  });
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();

  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.locator('[data-testid="admin-credential-create-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .locator('[data-testid="admin-credential-create-confirm-submit"]')
    .click();
  await lostStarted;
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  releaseLost?.();
  await page
    .getByText(
      "The created credential may be active, but its bearer was lost and cannot be recovered. Review credential metadata and explicitly revoke it if unusable before creating a deliberate replacement. Nothing was replayed.",
      { exact: true },
    )
    .waitFor();
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .locator('[data-testid="admin-credential-create-confirm-submit"]')
    .click();
  await page
    .getByText(
      "No bearer can be displayed. Inspect current state before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await page
    .getByText(
      "Do not replay. The credential may be active while its bearer is permanently lost. Refresh metadata, then explicitly revoke an unusable credential before creating a deliberate replacement.",
      { exact: true },
    )
    .waitFor();
  items = items.map((item) =>
    item.id === ids[0]
      ? item
      : { ...item, status: "revoked" as const, revision: "9" },
  );
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.getByTestId("admin-credentials-view").waitFor();
  await page.getByTestId("manual-refresh").click();
  const protectedRow = page
    .getByTestId("admin-credential-row")
    .filter({ hasText: ids[0]! });
  const protectedRevoke = protectedRow.getByTestId("admin-credential-revoke");
  await expect(protectedRevoke).toBeDisabled();
  const protectionText = protectedRow.getByText(
    "The last active non-expiring administrator credential cannot be revoked.",
    { exact: true },
  );
  await expect(protectionText).toBeVisible();
  const guidanceWidth = (await protectionText.boundingBox())!.width;
  expect(guidanceWidth).toBeGreaterThanOrEqual(200);
  expect(guidanceWidth).toBeLessThanOrEqual(256);
  await expect(protectedRevoke).toHaveAccessibleDescription(
    "The last active non-expiring administrator credential cannot be revoked.",
  );
  await captureTableState(page, "admin-protected-revoke");
  const logoutResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v2/admin-sessions/current" &&
      response.request().method() === "DELETE",
  );
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  const logout = await logoutResponse;
  if (logout.status() !== 204) {
    const problem = (await logout.json()) as { code?: string };
    fail(
      `admin credential scenario logout failed: HTTP ${logout.status()}, code ${problem.code ?? "absent"}, cookie sent ${Boolean((await logout.request().allHeaders()).cookie)}`,
    );
  }
  await expect
    .poll(
      async () => (await context.cookies(baseURL)).map((cookie) => cookie.name),
      { timeout: 3000 },
    )
    .not.toContain("agent_gateway_session");
  await waitForLifecycle(page, "signed_out");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, issuedBearer, lostBearer],
    false,
  );
  process.stdout.write(
    `${JSON.stringify({ event: "admin_credentials_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), creates, revokes })}\n`,
  );
}

export async function runOverview(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  // Install before authentication creates the grouped polling timers.
  await page.clock.install();
  let invocationReads = 0;
  let activityReads = 0;
  let activityMode: "complete" | "partial" | "reset" | "error" = "complete";
  let catalogMode: "current" | "error" = "current";
  let customHeaders: unknown = { "X-MCP-Toolsets": "repos,issues" };
  const screenshots = await mkdtemp(join(tmpdir(), "overview-headers-"));
  const compareBaseline = prepareOverviewBaseline(page);
  const httpServer = () => ({
    ...overviewServer("01ARZ3NDEKTSV4RRFFQ69G5FA0", "Quiet server", "active"),
    transport: {
      kind: "streamable_http",
      url: "https://example.invalid/mcp",
      protocol_mode: "modern",
      authentication: { mode: "bearer" },
      ...(customHeaders === undefined ? {} : { headers: customHeaders }),
    },
  });
  let serverMode:
    | "complete"
    | "stale"
    | "partial"
    | "quiet"
    | "saturated"
    | "empty"
    | "error" = "complete";
  let statusMode: "abnormal" | "quiet" | "error" = "abnormal";
  let reportedStatus = false;
  let proxyDisabled = false;
  let requestMode: "populated" | "quiet" | "error" = "populated";
  let queueFault: "total" | "offset" | "cursor" | "label" | undefined;
  let staleRestarted = false;
  let heldStatus = true;
  let releaseHeldStatus: (() => void) | undefined;
  let heldStatusPromise = new Promise<void>((resolve) => {
    releaseHeldStatus = resolve;
  });
  let heldStatusReads = 0;

  await page.route("**/api/v2/system-status", async (route) => {
    if (
      route.request().method() !== "GET" ||
      new URL(route.request().url()).search !== ""
    )
      fail("Overview status request changed shape");
    const late = heldStatus;
    if (late) {
      heldStatusReads += 1;
      await heldStatusPromise;
    }
    if (statusMode === "error") {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    const status = {
      ...overviewStatusFixture(),
      ...(reportedStatus
        ? {
            endpoints: {
              authority: "127.0.0.1:8210",
              api: statusMode === "quiet" ? "ready" : "read_only",
              mcp: statusMode === "quiet" ? "ready" : "unavailable",
            },
            http_proxy: {
              enabled: !proxyDisabled,
              ready: statusMode === "quiet",
              ca_ready: statusMode === "quiet",
              authority: "127.0.0.1:8212",
              connections: {
                in_use: statusMode === "quiet" ? 0 : 230,
                limit: 256,
                saturated: false,
              },
              work: {
                in_use: statusMode === "quiet" ? 0 : 128,
                limit: 128,
                saturated: statusMode !== "quiet",
              },
              active_streams: 0,
              active_tunnels: 0,
            },
            traffic: {
              state: statusMode === "quiet" ? "ready" : "faulted",
              ready: statusMode === "quiet",
              faulted: statusMode !== "quiet",
              pressure: statusMode !== "quiet",
              budget_bytes: 4294967296,
              database_bytes: 1048576,
              wal_bytes: 65536,
              quota_refusals: 0,
              pruned_records: 0,
              generation: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
              rolling_history: true,
              unknown_completion_possible: true,
            },
          }
        : {}),
    };
    status.limits.mcp_work = { in_use: 64, limit: 64, saturated: true };
    status.limits.downstream_dispatch = {
      in_use: 858993460,
      limit: 1073741824,
      saturated: false,
    };
    if (statusMode === "quiet") {
      status.process.state = "ready";
      status.process.ready = true;
      status.sqlite.state = "ready";
      status.sqlite.latched = false;
      status.keyring.capability = "ready";
      for (const limit of Object.values(status.limits)) {
        limit.in_use = 0;
        limit.saturated = false;
      }
      status.limits.servers = { in_use: 79, limit: 100, saturated: false };
    }
    if (late) status.process.state = "draining";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(status),
    });
  });
  await page.route("**/api/v2/mcp/servers?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "cursor")
    )
      fail("Overview server request changed shape");
    const cursor = query.get("cursor");
    if (serverMode === "error") {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    if (
      serverMode === "quiet" ||
      serverMode === "empty" ||
      serverMode === "saturated"
    ) {
      const server = httpServer();
      if (serverMode === "saturated") server.runtime.dispatch.saturated = true;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: serverMode === "empty" ? [] : [server],
          next_cursor: null,
        }),
      });
      return;
    }
    if (serverMode === "complete") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            overviewServer(
              "01ARZ3NDEKTSV4RRFFQ69G5FA0",
              `literal-<script>${"S".repeat(180)}`,
              "active",
            ),
            overviewServer(
              "01ARZ3NDEKTSV4RRFFQ69G5FA1",
              "Needs operator attention",
              "degraded",
            ),
            ...Array.from({ length: 7 }, (_, index) => {
              const server = overviewServer(
                `01ARZ3NDEKTSV4RRFFQ69G5FB${index}`,
                index === 0
                  ? `literal-<script>${"S".repeat(180)}`
                  : `Affected server ${index + 2}`,
                "active",
              );
              if (index === 0)
                server.credential_state = "reauthentication_required";
              else server.catalog.active_state = "unavailable";
              return server;
            }),
            {
              ...overviewServer(
                "01ARZ3NDEKTSV4RRFFQ69G5FAB",
                "Deleted server history",
                "deleted",
              ),
              desired_state: "deleted",
              transport: null,
              runtime: {
                ...overviewServer(
                  "01ARZ3NDEKTSV4RRFFQ69G5FAB",
                  "Deleted server history",
                  "deleted",
                ).runtime,
                state: "deleted",
              },
              catalog: {
                ...overviewServer(
                  "01ARZ3NDEKTSV4RRFFQ69G5FAB",
                  "Deleted server history",
                  "deleted",
                ).catalog,
                durable_state: "retired",
                active_state: "absent",
                active_revision: null,
                active_tool_count: 0,
              },
              deleted_at: "2026-08-28T01:00:00Z",
            },
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    if (serverMode === "stale" && cursor === "stale") {
      staleRestarted = true;
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "stale_cursor",
          title: "The cursor is stale.",
        }),
      });
      return;
    }
    if (serverMode === "stale" && !staleRestarted) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            overviewServer(
              "01ARZ3NDEKTSV4RRFFQ69G5FA2",
              "Discarded stale server",
              "active",
            ),
          ],
          next_cursor: "stale",
        }),
      });
      return;
    }
    if (serverMode === "partial" && cursor === "broken") {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          overviewServer(
            "01ARZ3NDEKTSV4RRFFQ69G5FA1",
            "Needs operator attention",
            "degraded",
          ),
        ],
        next_cursor: serverMode === "partial" ? "broken" : null,
      }),
    });
  });
  await page.route("**/api/v2/mcp/grant-requests?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (query.get("limit") === "1") {
      await route.fallback();
      return;
    }
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "5" ||
      query.get("state") !== "pending" ||
      query.get("sort") !== "submitted" ||
      query.get("direction") !== "ascending" ||
      [...query.keys()].some(
        (key) => !["limit", "state", "sort", "direction"].includes(key),
      )
    )
      fail("Overview request queue read changed shape");
    if (requestMode === "error") {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items:
          requestMode === "quiet"
            ? []
            : Array.from({ length: 5 }, (_, index) => ({
                ...overviewRequestFixture(),
                id: `01ARZ3NDEKTSV4RRFFQ69G5FC${index}`,
                created_at: `2026-08-${[28, 26, 29, 25, 27][index]}T00:00:00Z`,
                requested_policy: {
                  ...overviewRequestFixture().requested_policy,
                  ...(index === 0 ? { read_only: true } : {}),
                  target:
                    index === 0
                      ? `requested-${"T".repeat(180)}`
                      : `target-${index}`,
                },
              })).map((request) => ({
                request,
                principal_display_name:
                  queueFault === "label" ? null : "Overview agent",
                server_display_name: "Needs operator attention",
                resolved_server_id: "01ARZ3NDEKTSV4RRFFQ69G5FA1",
                resolved_upstream_name: null,
              })),
        total_count:
          queueFault === "total" ? -1 : requestMode === "quiet" ? 0 : 6,
        offset: queueFault === "offset" ? 1 : 0,
        next_cursor:
          queueFault === "cursor"
            ? null
            : requestMode === "quiet"
              ? null
              : "more-pending",
      }),
    });
  });
  await page.route("**/api/v2/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        total_count: 1,
        offset: 0,
        items: [
          {
            id: overviewRequestFixture().principal_id,
            display_name: "Overview agent",
            http_default: "block",
            state: "active",
            visibility: "all",
            revision: "1",
            credential_revision: "0",
            credential: null,
            created_at: "2026-08-28T00:00:00Z",
            updated_at: "2026-08-28T00:00:00Z",
          },
        ],
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v2/recorded-activity", async (route) => {
    expect(route.request().method()).toBe("GET");
    expect(new URL(route.request().url()).search).toBe("");
    activityReads++;
    if (activityMode === "error") {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    const value = activityFixture(
      activityMode === "reset" ? "unavailable" : activityMode,
    );
    if (activityMode === "reset") {
      value.epoch = `${"B".repeat(26)}-2`;
      value.epoch_reason = "clock_reset";
      value.collection_start = "2026-09-30T12:16:00Z";
    }
    value.buckets.forEach((bucket, index) => {
      if (!bucket.counts) return;
      bucket.counts.mcp.admissions.allow = index * 2;
      bucket.counts.mcp.completions.downstream_failure = index % 3;
      bucket.counts.http_request.admissions.allow = index;
      bucket.counts.http_request.completions.outcome_unknown = index % 2;
      bucket.counts.connect.admissions.interception_selected = index % 4;
    });
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify(value),
    });
  });
  await page.route("**/api/v2/mcp/catalog?*", async (route) => {
    expect(new URL(route.request().url()).search).toBe("?limit=1");
    if (catalogMode === "error") {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    await route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        catalog: {
          active_state: "empty",
          active_generation: "snapshot-1",
          changed_at: null,
          issue_count: 0,
        },
        items: [],
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v2/mcp/invocations?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "5" ||
      [...query.keys()].some((key) => key !== "limit")
    )
      fail("Overview invocation read changed shape");
    invocationReads += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [overviewInvocationFixture()],
        next_cursor: "older",
      }),
    });
  });

  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="overview-grid"]').waitFor();
  let overviewPhase = "initial";
  const overview = page.locator('[data-testid="overview-grid"]');
  const source = (id: string) => page.locator(`[data-testid="overview-${id}"]`);
  const sourceText = async (id: string) =>
    (await source(id).textContent()) ?? "";
  const assertCurrent = async (id: string) => {
    await page
      .waitForFunction(
        (id) =>
          document
            .querySelector(`[data-testid="overview-${id}"]`)
            ?.getAttribute("data-panel-status") === "current",
        id,
      )
      .catch(async () =>
        fail(
          `Overview ${id} did not become current after ${overviewPhase} (status=${await source(id).getAttribute("data-panel-status")})`,
        ),
      );
  };
  const capture = async (state: string) => {
    await captureFrontend(page, `overview-${state}`, true);
    overviewPhase = state;
    if ((await overview.locator("nav").count()) !== 0)
      fail(`Overview ${state} retained redundant destination navigation`);
    if (process.env.AGENT_GATEWAY_OVERVIEW_ARTIFACT_DIR) {
      await captureOverviewLayout(page, state);
      return;
    }
    for (const theme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: theme });
      for (const width of [1440, 390, 320]) {
        await page.setViewportSize({ width, height: 1000 });
        const clipped = await overview.evaluate((root) => {
          const width = document.documentElement.clientWidth;
          const headerBoxes = [
            ...document.querySelectorAll(
              ".masthead button, .masthead select, .freshness-control > .status-label",
            ),
          ]
            .map((element) => element.getBoundingClientRect())
            .filter((box) => box.width > 0);
          return (
            headerBoxes.some(
              (box, index) =>
                box.left < 0 ||
                box.right > width ||
                headerBoxes
                  .slice(index + 1)
                  .some(
                    (other) =>
                      box.left < other.right &&
                      box.right > other.left &&
                      box.top < other.bottom &&
                      box.bottom > other.top,
                  ),
            ) ||
            document.documentElement.scrollWidth > width ||
            [...root.querySelectorAll("a")].some((link) => {
              const box = link.getBoundingClientRect();
              return (
                box.left < 0 ||
                box.right > width ||
                link.scrollWidth > link.clientWidth + 1
              );
            })
          );
        });
        if (clipped)
          fail(`Overview ${state}/${theme}/${width} clipped content or links`);
        const screenshot = await page.screenshot({
          fullPage: true,
          path: join(screenshots, `${state}-${theme}-${width}.png`),
        });
        if (screenshot.length === 0) fail(`Overview ${state} screenshot empty`);
      }
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.emulateMedia({ colorScheme: "light" });
  };
  await assertCurrent("servers");
  await assertCurrent("requests");
  await assertCurrent("activity");
  await assertCurrent("catalog");
  if (
    (await source("status").getAttribute("data-panel-status")) !== "loading" ||
    !(await sourceText("status")).includes("Loading current data")
  )
    fail("System loading blocked independent reads or claimed success");
  await capture("loading");
  heldStatus = false;
  releaseHeldStatus?.();
  await assertCurrent("status");
  const body = (await overview.textContent()) ?? "";
  for (const text of [
    "not ready",
    "storage mutation is closed",
    "Keyring unavailable",
    "Capacity saturated; additional work may be rejected.",
    "80% capacity pressure; headroom for additional work is limited.",
    "64 / 64",
    "858993460 / 1073741824",
    "Needs operator attention",
    "Overview agent",
    "5 shown; more need attention",
    "5 shown; more pending",
    "9 Configured MCP servers",
    "8 Active MCP catalog tools",
    "6 pending",
  ]) {
    if (!body.includes(text)) fail(`Overview omitted ${text}`);
  }
  for (const text of [
    "POSTURE-01",
    "SERVERS-01",
    "REQUESTS-01",
    "AUDIT-01",
    "Recent invocations",
    "Missing terminal evidence",
    "Deleted server history",
    "stopped-process recovery",
    "Affected server 6",
  ])
    if (body.includes(text)) fail(`Overview retained ${text}`);
  if (
    (await overview
      .locator(".panel-code, .fact-card, .compact-record-fields, button")
      .count()) !== 0
  )
    fail(
      "Overview retained ornamental inventory, diagnostics or inline mutations",
    );
  const serverLinks = await source("servers")
    .locator("li a")
    .evaluateAll((links) => links.map((link) => link.getAttribute("href")));
  if (
    serverLinks.join("|") !==
    ["FA1", "FB0", "FB1", "FB2", "FB3"]
      .map((suffix) => `#/mcp/servers/01ARZ3NDEKTSV4RRFFQ69G5${suffix}`)
      .join("|")
  )
    fail("Server preview changed eligibility, order or five-item bound");
  const reasons = await source("servers").locator("li p").allTextContents();
  if (
    reasons.join("|") !==
    "Runtime degraded|Credentials reauthentication required|Active catalog unavailable|Active catalog unavailable|Active catalog unavailable"
  )
    fail("Server reasons were not specific observed states");
  const requestLinks = await source("requests")
    .locator("li a")
    .evaluateAll((links) => links.map((link) => link.getAttribute("href")));
  if (
    requestLinks.join("|") !==
    Array.from(
      { length: 5 },
      (_, index) => `#/mcp/access-requests/01ARZ3NDEKTSV4RRFFQ69G5FC${index}`,
    ).join("|")
  )
    fail(
      "Request preview re-sorted queue timestamps or lost bounds/review links",
    );
  if (
    (await source("requests").locator("time").count()) !== 5 ||
    !(await sourceText("requests")).includes("Waiting about")
  )
    fail("Request waiting time missing");
  if ((await page.locator("script").count()) !== 1)
    fail("Overview rendered active content");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) !== "storage_latched"
  )
    fail("Overview did not close mutation admission for latched storage");
  for (const href of [
    "#/system",
    "#/mcp/servers",
    "#/mcp/tools",
    "#/mcp/access-requests",
    "#/mcp/invocations",
  ])
    if (
      (await page.locator(`#primary-navigation a[href="${href}"]`).count()) !==
      1
    )
      fail(`Primary navigation omitted destination ${href}`);
  for (const [sourceID, name, href] of [
    ["status", "Inspect System", "#/system"],
    ["material", "Inspect storage status", "#/system"],
    ["material", "Inspect keyring status", "#/system"],
    ["capacity", "Resource limits", "#/system?tab=resource-limits"],
  ] as const)
    if (
      (await source(sourceID)
        .getByRole("link", { name, exact: true })
        .getAttribute("href")) !== href
    )
      fail(`Overview omitted contextual condition link ${name}`);
  await source("servers").locator("li a").first().focus();
  await page.keyboard.press("Tab");
  if (
    !(await source("servers")
      .locator("li a")
      .nth(1)
      .evaluate((link) => link === document.activeElement))
  )
    fail("Server links not keyboard reachable in queue order");
  await expect(overview.locator(":scope > .overview-panel")).toHaveCount(7);
  await expect(overview.locator(":scope > .overview-panel h2")).toHaveText([
    "Gateway readiness",
    "Storage and material",
    "Work capacity",
    "MCP attention",
    "Pending MCP decisions",
    "MCP inventory",
    "Recorded activity",
  ]);
  await expect(source("status")).toContainText("Not reported");
  reportedStatus = true;
  await page.getByTestId("manual-refresh").click();
  await expect(source("capacity")).toContainText("HTTP connections230 / 256");
  await expect(source("capacity")).toContainText("HTTP work128 / 128");
  await expect(
    source("capacity").locator(".overview-conditions li strong"),
  ).toHaveText(["MCP work", "HTTP work", "Downstream dispatch"]);
  await expect(source("capacity")).toContainText(
    "1 additional pool under pressure",
  );
  await expect(source("material")).toContainText("Traffic storeFaulted");
  await assertCardAlignment();
  await capture("reported-pools");
  proxyDisabled = true;
  await page.getByTestId("manual-refresh").click();
  await expect(
    source("status").getByText("Disabled", { exact: true }),
  ).toHaveAttribute("data-state", "neutral");
  proxyDisabled = false;
  await page.getByTestId("manual-refresh").click();
  await expect(
    source("status").getByText("Not ready", { exact: true }),
  ).toBeVisible();

  const minuteEvidence = source("activity").getByText("Minute evidence", {
    exact: true,
  });
  await minuteEvidence.focus();
  await page.keyboard.press("Enter");
  await expect(source("activity").locator("details[open] ol li")).toHaveCount(
    60,
  );
  await expect(minuteEvidence).toBeFocused();
  await captureOverviewLayout(page, "activity-minute-evidence");
  await minuteEvidence.focus();
  await page.keyboard.press("Enter");
  await expect(
    source("activity").getByRole("link", {
      name: "MCP invocation history",
      exact: true,
    }),
  ).toHaveAttribute("href", "#/mcp/invocations");
  await expect(
    source("activity").getByRole("link", {
      name: "HTTP request history",
      exact: true,
    }),
  ).toHaveAttribute("href", "#/http/traffic?filter_type=request");
  if (!hasCaptureOwner(page)) {
    const accessibility = await new AxeBuilder({ page })
      .include('[data-testid="overview-grid"]')
      .analyze();
    if (accessibility.violations.length > 0)
      fail(
        `Overview accessibility violations: ${accessibility.violations.map((item) => item.id).join(",")}`,
      );
  }
  await capture("attention");
  await compareBaseline("attention");
  for (const fault of ["total", "offset", "cursor", "label"] as const) {
    queueFault = fault;
    await page.getByTestId("manual-refresh").click();
    await expect(source("requests")).toHaveAttribute(
      "data-panel-status",
      "error",
    );
    await expect(source("requests")).toContainText(
      "last known; current queue unknown",
    );
    await assertCurrent("servers");
  }
  queueFault = undefined;
  await page.getByTestId("manual-refresh").click();
  await assertCurrent("requests");
  await page.waitForTimeout(5100);
  if (invocationReads !== 0)
    fail("Overview performed invocation reads or polling");

  serverMode = "stale";
  staleRestarted = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("1 servers flagged"),
  );
  if (
    !staleRestarted ||
    ((await page.locator("body").textContent()) ?? "").includes(
      "Discarded stale server",
    )
  )
    fail("stale server traversal was not restarted cleanly");

  serverMode = "partial";
  activityMode = "partial";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("loaded; incomplete"),
  );
  if (
    !(await sourceText("servers")).includes(
      "additional affected servers may exist",
    ) ||
    !(await sourceText("servers")).includes("loaded; incomplete")
  )
    fail("Partial traversal implied an exhaustive count");
  await assertCurrent("activity");
  await expect(source("activity")).toContainText("Partial coverage");
  await capture("partial");
  serverMode = "saturated";
  await page.getByTestId("manual-refresh").click();
  await expect(source("servers")).toContainText("Capacity saturated");
  await expect(
    source("servers").getByTestId("overview-server-row"),
  ).toHaveCount(1);
  await capture("capacity-only-attention");
  serverMode = "partial";
  activityMode = "reset";
  await page.getByTestId("manual-refresh").click();
  await expect(source("activity")).toContainText(
    "Collection reset after a clock discontinuity",
  );
  await expect(source("activity").locator(".activity-totals")).toHaveCount(0);
  await capture("activity-reset");
  activityMode = "complete";

  statusMode = "quiet";
  serverMode = "quiet";
  requestMode = "quiet";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("0 servers flagged"),
  );
  await assertCurrent("status");
  await assertCurrent("requests");
  const quiet = (await overview.textContent()) ?? "";
  for (const text of [
    "ProcessReady",
    "Administration APIReady",
    "MCP ingressReady",
    "HTTP proxyReady",
    "Traffic storeReady",
    "HTTP connections0 / 256",
    "HTTP work0 / 128",
    "SQLiteReady · Not latched",
    "Keyring startupReady",
    "0 servers flagged",
    "0 pending",
    "1 Configured MCP servers",
    "1 Active MCP catalog tools",
  ])
    if (!quiet.includes(text)) fail(`Quiet Overview omitted ${text}`);
  if (
    quiet.includes("capacity pressure") ||
    quiet.includes("saturated") ||
    quiet.includes("healthy")
  )
    fail("Ready Overview claimed unsupported aggregate health or pressure");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) === "storage_latched"
  )
    fail("Fresh unlatched read did not reopen admission");
  async function assertCardAlignment() {
    await page.setViewportSize({ width: 1440, height: 1000 });
    const cardGeometry = await Promise.all(
      ["status", "material", "capacity"].map(async (id) => {
        const card = source(id);
        const box = await card.boundingBox();
        const heading = await card
          .getByRole("heading", { level: 2 })
          .boundingBox();
        const footer = await card
          .locator(".overview-stack > a")
          .last()
          .boundingBox();
        return {
          top: box!.y,
          bottom: box!.y + box!.height,
          heading: heading!.y,
          footer: footer!.y,
        };
      }),
    );
    for (const key of ["top", "bottom", "heading", "footer"] as const)
      expect(
        Math.max(...cardGeometry.map((c) => c[key])) -
          Math.min(...cardGeometry.map((c) => c[key])),
      ).toBeLessThanOrEqual(1);
  }
  await assertCardAlignment();
  await expect(
    source("material").getByText("Idle", { exact: true }),
  ).toHaveAttribute("data-state", "neutral");
  for (const removed of [
    "downstream reachability",
    "client trust",
    "recovery assurance",
    "callability count",
  ])
    await expect(overview).not.toContainText(removed);
  await capture("quiet");
  await compareBaseline("quiet");

  for (const headers of [null, [], "invalid", { "X-MCP-Toolsets": 7 }]) {
    customHeaders = headers;
    await page.locator('[data-testid="manual-refresh"]').click();
    await page.waitForFunction(
      () =>
        document
          .querySelector('[data-testid="overview-servers"]')
          ?.getAttribute("data-panel-status") === "error",
    );
    if ((await sourceText("servers")).includes("in the current read"))
      fail("Malformed headers retained current server reassurance");
    await assertCurrent("status");
    await assertCurrent("requests");
  }
  await capture("invalid-headers");
  for (const headers of [undefined, {}, { "X-MCP-Toolsets": "repos,issues" }]) {
    customHeaders = headers;
    await page.locator('[data-testid="manual-refresh"]').click();
    await assertCurrent("servers");
    if (!(await sourceText("inventory")).includes("1 Configured MCP servers"))
      fail("Valid HTTP headers prevented complete server counts");
  }

  serverMode = "empty";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("No servers configured"),
  );
  if (
    !(await sourceText("inventory")).includes("0 Configured MCP servers") ||
    !(await sourceText("inventory")).includes("0 Active MCP catalog tools")
  )
    fail("Empty inventory counts misleading");
  await capture("empty");

  for (const failed of ["status", "servers", "requests"] as const) {
    statusMode = failed === "status" ? "error" : "quiet";
    serverMode = failed === "servers" ? "error" : "quiet";
    requestMode = failed === "requests" ? "error" : "quiet";
    await page.locator('[data-testid="manual-refresh"]').click();
    await page.waitForFunction(
      (id) =>
        document
          .querySelector(`[data-testid="overview-${id}"]`)
          ?.getAttribute("data-panel-status") === "error",
      failed,
    );
    if (
      !(await sourceText(failed)).includes(
        "Refresh failed. Showing the last read; current state is unknown.",
      )
    )
      fail(`Isolated ${failed} error omitted evidence boundary`);
    if ((await sourceText(failed)).includes("in the current read"))
      fail(`Isolated ${failed} error retained current reassurance`);
    for (const other of ["status", "servers", "requests"].filter(
      (id) => id !== failed,
    ))
      await assertCurrent(other);
    await capture(`error-${failed}`);
  }
  statusMode = "quiet";
  serverMode = "quiet";
  requestMode = "quiet";
  activityMode = "error";
  catalogMode = "error";
  await page.getByTestId("manual-refresh").click();
  await expect(source("activity")).toHaveAttribute(
    "data-panel-status",
    "error",
  );
  await expect(source("catalog")).toHaveAttribute("data-panel-status", "error");
  await expect(source("activity")).toContainText("Last known");
  await expect(source("catalog")).toContainText(
    "last known; current state unknown",
  );
  for (const id of ["status", "servers", "requests"]) await assertCurrent(id);
  await capture("activity-catalog-error");
  activityMode = "complete";
  catalogMode = "current";
  await page.locator('[data-testid="manual-refresh"]').click();
  for (const id of ["status", "servers", "requests", "activity", "catalog"])
    await assertCurrent(id);
  await page.route("**/api/v2/events", (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: ": ended stream\n\n",
    }),
  );
  await page.reload();
  await page.waitForFunction(() =>
    ["status", "servers", "requests"].every((id) =>
      document
        .querySelector(`[data-testid="overview-${id}"]`)
        ?.textContent?.includes(
          "Data stale. Showing the last read; current state is unknown.",
        ),
    ),
  );
  for (const id of ["status", "servers", "requests"]) {
    if (
      !(await sourceText(id)).includes(
        "Data stale. Showing the last read; current state is unknown.",
      ) ||
      (await sourceText(id)).includes("in the current read")
    )
      fail(`Stale ${id} claimed current reassurance`);
  }
  await capture("stale");
  await page.unroute("**/api/v2/events");
  await page.reload();
  for (const id of ["status", "servers", "requests"]) await assertCurrent(id);
  if (invocationReads !== 0)
    fail("Overview invocation reads resumed on visibility or refresh");

  heldStatusPromise = new Promise<void>((resolve) => {
    releaseHeldStatus = resolve;
  });
  const previousHeldReads = heldStatusReads;
  heldStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await eventually(
    () => heldStatusReads > previousHeldReads,
    "held overview refresh did not start",
  );
  if (
    (await page
      .locator('[data-testid="overview-status"]')
      .getAttribute("data-panel-status")) !== "current" ||
    ((await page.locator("body").textContent()) ?? "").includes("Data stale")
  )
    fail("overview refresh flashed stale feedback");
  heldStatus = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  (releaseHeldStatus as (() => void) | undefined)?.();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="overview-status"]')
        ?.getAttribute("data-panel-status") === "current",
  );
  if (
    (
      (await page.locator('[data-testid="overview-status"]').textContent()) ??
      ""
    ).includes("draining")
  )
    fail("late Overview read replaced the current generation");

  if (
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    )
  )
    fail("overview long content overflowed the document");
  requestMode = "error";
  await page.reload();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="overview-requests"]')
        ?.getAttribute("data-panel-status") === "error",
  );
  await assertCurrent("status");
  await assertCurrent("servers");
  if (
    !(await sourceText("requests")).includes("Read unavailable") ||
    (await sourceText("requests")).includes("No pending")
  )
    fail("Unavailable initial queue appeared empty-success");
  await capture("unavailable");
  if (invocationReads !== 0)
    fail("Overview invocation reads returned after reload");
  const beforePoll = activityReads;
  await page.clock.fastForward(31_000);
  await expect.poll(() => activityReads).toBeGreaterThan(beforePoll);
  await assertCurrent("activity");
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  const hiddenReads = activityReads;
  await page.clock.fastForward(61_000);
  expect(activityReads, "hidden Overview pauses grouped activity reads").toBe(
    hiddenReads,
  );
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.clock.fastForward(31_000);
  await expect.poll(() => activityReads).toBeGreaterThan(hiddenReads);
  await assertCurrent("activity");
  await page.evaluate(() => {
    window.location.hash = "#/audit-log";
  });
  await expect(
    page.getByRole("heading", { level: 1, name: "Audit Log", exact: true }),
  ).toBeVisible();
  const awayReads = activityReads;
  await page.clock.fastForward(61_000);
  expect(activityReads, "navigation stops activity reads").toBe(awayReads);
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "overview_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), invocation_reads: invocationReads, activity_reads: activityReads, screenshots })}\n`,
  );
}

export async function runInvocations(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const compareBaseline = prepareDetailBaseline(page);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );

  const historyScreenshots: string[] = [];
  const captureCanary = `INVOCATION_CAPTURE_<script>${"C".repeat(64)}`;
  let argumentCapture: unknown = {
    note: captureCanary,
    token: "[REDACTED]",
  };
  let listReads = 0;
  let continuationReads = 0;
  let itemReads = 0;
  let vocabularyItemReads = 0;
  let staleMode = false;
  let staleRestarted = false;
  let itemMissing = false;
  let failureDiagnostics: unknown = undefined;
  await page.route("**/api/v2/mcp/invocations**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const headers = await request.allHeaders();
    if (
      request.method() !== "GET" ||
      request.postData() !== null ||
      headers["x-csrf-token"] === undefined
    )
      fail("invocation view issued an unauthenticated or non-read request");
    if (url.pathname !== "/api/v2/mcp/invocations") {
      if (
        url.pathname === `/api/v2/mcp/invocations/${invocationIDs.admission}`
      ) {
        expect(url.search).toBe("");
        vocabularyItemReads += 1;
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            ...invocationFixture(
              invocationIDs.admission,
              "admission",
              "invalid_params",
            ),
            redacted_arguments: null,
          }),
        });
        return;
      }
      if (
        url.pathname !== `/api/v2/mcp/invocations/${invocationIDs.missing}` ||
        url.search !== ""
      )
        fail("invocation item request changed shape");
      itemReads += 1;
      if (itemMissing) {
        await route.fulfill({
          status: 404,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 404,
            code: "not_found",
            title: "The resource was not found.",
          }),
        });
      } else {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            ...invocationFixture(
              invocationIDs.missing,
              failureDiagnostics === undefined
                ? "missing_terminal"
                : "terminal",
              failureDiagnostics === undefined
                ? "outcome_unknown"
                : "downstream_failure",
              failureDiagnostics === undefined ? "gateway" : "downstream",
            ),
            redacted_arguments: argumentCapture,
            ...(failureDiagnostics === undefined
              ? {}
              : {
                  diagnostics: failureDiagnostics,
                  outcome: {
                    class: "downstream_failure",
                    basis: "terminal",
                    completed_at: "2026-08-28T12:00:02Z",
                  },
                }),
          }),
        });
      }
      return;
    }
    const query = url.searchParams;
    const allowed = new Set([
      "limit",
      "cursor",
      "tool",
      "principal",
      "decision",
      "outcome",
      "search_locale",
    ]);
    if (
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => !allowed.has(key)) ||
      [...query.keys()].some((key) => query.getAll(key).length !== 1)
    )
      fail("invocation list request changed shape");
    if (query.get("outcome") === "invalid_params") {
      listReads += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            invocationFixture(
              invocationIDs.admission,
              "admission",
              "invalid_params",
            ),
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    if (query.get("decision") === "block") {
      listReads += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            invocationFixture(
              invocationIDs.policy,
              "policy",
              "block",
              "downstream",
            ),
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    if (query.get("tool") === "namespace.allowed") {
      if (query.has("cursor") || !query.has("search_locale"))
        fail("Invocation filter reused cursor or omitted search locale");
      listReads += 1;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            invocationFixture(
              invocationIDs.terminal,
              "terminal",
              "succeeded",
              "downstream",
            ),
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    const cursor = query.get("cursor");
    if (cursor !== null) continuationReads += 1;
    else listReads += 1;
    if (staleMode) {
      if (cursor === "stale-floor") {
        staleRestarted = true;
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 409,
            code: "stale_cursor",
            title: "The cursor snapshot is no longer available.",
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          staleRestarted
            ? {
                items: [
                  invocationFixture(
                    invocationIDs.missing,
                    "missing_terminal",
                    "outcome_unknown",
                    "gateway",
                  ),
                ],
                next_cursor: null,
              }
            : {
                items: [
                  invocationFixture(
                    invocationIDs.stale,
                    "terminal",
                    "succeeded",
                    "downstream",
                  ),
                ],
                next_cursor: "stale-floor",
              },
        ),
      });
      return;
    }
    if (cursor === "page-2") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            invocationFixture(
              invocationIDs.terminal,
              "terminal",
              "succeeded",
              "downstream",
            ),
            invocationFixture(
              invocationIDs.explicitUnknown,
              "terminal",
              "outcome_unknown",
              "downstream",
            ),
            invocationFixture(
              invocationIDs.missing,
              "missing_terminal",
              "outcome_unknown",
              "gateway",
            ),
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          invocationFixture(
            invocationIDs.admission,
            "admission",
            "invalid_params",
          ),
          invocationFixture(
            invocationIDs.policy,
            "policy",
            "deny",
            "downstream",
          ),
        ],
        next_cursor: "page-2",
      }),
    });
  });

  await page.route("**/api/v2/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        total_count: 1,
        offset: 0,
        items: [
          {
            id: invocationIDs.principal,
            display_name: "Build agent",
            http_default: "block",
            state: "active",
            visibility: "all",
            revision: "1",
            credential_revision: "1",
            credential: null,
            created_at: "2026-08-28T12:00:00Z",
            updated_at: "2026-08-28T12:00:00Z",
          },
        ],
        next_cursor: null,
      }),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/mcp/invocations";
  });
  await page.locator('[data-testid="invocations-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="invocation-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  expect(await page.locator("thead th").allTextContents()).toEqual([
    "Admitted",
    "Invocation",
    "Agent",
    "Authorization",
    "Outcome",
  ]);
  for (const phrase of [
    "Invocation evidence",
    "retains at most 4,096 recent rows",
    "Filtered pages are independently coherent",
    "gateway:get_identity",
    "Tool searches recorded names",
    "Names ignore accents and tolerate one typo",
  ])
    if (body.includes(phrase)) fail(`invocation list retained ${phrase}`);
  await assertTableConventions(
    page,
    "Invocation history",
    ["Admitted", "Invocation", "Agent", "Authorization", "Outcome"],
    "Invocation",
  );
  const liveSwitch = page.getByRole("switch", { name: "Live mode" });
  if ((await liveSwitch.count()) !== 1 || !(await liveSwitch.isChecked()))
    fail("invocation live mode was not enabled by default");
  if (
    (await liveSwitch.evaluate((element) => {
      const style = getComputedStyle(element);
      return style.position !== "absolute" || style.opacity !== "0";
    })) ||
    !body.includes("Build agent")
  )
    fail("invocation live control or principal label was not normalized");
  const outcomeOptions = await page
    .getByLabel("Outcome", { exact: true })
    .locator("option")
    .allTextContents();
  for (const outcome of [
    "Invalid parameters",
    "Invalid arguments",
    "Authorization unavailable",
    "Prestart failure",
  ])
    if (!outcomeOptions.includes(outcome))
      fail(`invocation outcome filter omitted ${outcome}`);
  if ((await page.locator('[data-testid="invocation-row"] time').count()) !== 2)
    fail("invocation list did not render admitted timestamps in user time");
  if (
    body.includes(captureCanary) ||
    body.includes("redacted_arguments") ||
    body.includes("Recorded principal") ||
    body.includes("Recorded credential")
  )
    fail("invocation collection exposed item capture or internal identities");

  const toolCell = (id: string) =>
    page
      .locator('[data-testid="invocation-row"]')
      .filter({ hasText: id })
      .locator('[data-label="Invocation"] .table-primary');
  await expect(toolCell(invocationIDs.policy).getByRole("link")).toHaveText(
    "namespace.allowed",
  );
  await expect(
    toolCell(invocationIDs.policy).getByRole("link"),
  ).toHaveAttribute(
    "href",
    `#/mcp/servers/${invocationIDs.server}/descriptors/${invocationIDs.tool}`,
  );
  await expect(toolCell(invocationIDs.admission)).toHaveText("Not resolved");
  await expect(toolCell(invocationIDs.admission).getByRole("link")).toHaveCount(
    0,
  );

  const invocationIdentity = page
    .getByTestId("invocation-row")
    .filter({ hasText: invocationIDs.policy })
    .locator(".table-identifier");
  await expect(invocationIdentity).toHaveText(invocationIDs.policy);
  await expect(
    invocationIdentity.getByRole("link", {
      name: `Invocation ${invocationIDs.policy}`,
      exact: true,
    }),
  ).toHaveAttribute("href", `#/mcp/invocations/${invocationIDs.policy}`);

  const authorizationLabel = (id: string) =>
    page
      .getByTestId("invocation-row")
      .filter({ hasText: id })
      .locator('[data-label="Authorization"] .status-label');
  await expect(authorizationLabel(invocationIDs.admission)).toHaveText(
    "Not evaluated",
  );
  await expect(authorizationLabel(invocationIDs.admission)).toHaveAttribute(
    "data-state",
    "neutral",
  );
  await expect(authorizationLabel(invocationIDs.policy)).toHaveText("Deny");
  await expect(authorizationLabel(invocationIDs.policy)).toHaveAttribute(
    "data-state",
    "neutral",
  );

  const invalidOutcome = page.getByLabel("Outcome", { exact: true });
  await expect(
    invalidOutcome.locator('option[value="invalid_params"]'),
  ).toHaveText("Invalid parameters");
  await expect(
    page
      .getByTestId("invocation-row")
      .filter({ hasText: invocationIDs.admission })
      .locator('[data-label="Outcome"] .status-label'),
  ).toHaveText("Invalid parameters");
  const invalidQuery = page.waitForRequest((request) => {
    const url = new URL(request.url());
    return (
      url.pathname === "/api/v2/mcp/invocations" &&
      url.searchParams.get("outcome") === "invalid_params"
    );
  });
  await invalidOutcome.selectOption("invalid_params");
  await invalidQuery;
  await expect(page).toHaveURL(/filter_outcome=invalid_params/);
  await expect(page.getByTestId("invocation-row")).toHaveCount(1);
  await page
    .getByRole("link", {
      name: `Invocation ${invocationIDs.admission}`,
      exact: true,
    })
    .click();
  await expect(
    page
      .getByTestId("invocation-detail")
      .getByText("Invalid parameters", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Back to invocations", exact: true })
    .click();
  await expect(invalidOutcome).toHaveValue("invalid_params");
  expect(vocabularyItemReads).toBe(1);
  await invalidOutcome.selectOption("");
  await expect(page.getByTestId("invocation-row")).toHaveCount(2);

  const beforeWait = listReads;
  await page.waitForTimeout(5100);
  if (listReads !== beforeWait)
    fail("invocation live mode retained polling instead of event updates");
  await liveSwitch.uncheck();
  const pausedBody = (await page.locator("body").textContent()) ?? "";
  if (
    (await liveSwitch.isChecked()) ||
    pausedBody.includes("Live updates on") ||
    pausedBody.includes("Live updates paused")
  )
    fail("invocation live mode did not expose state through the switch alone");
  const beforeContinuation = continuationReads;
  const loadOlder = page.getByRole("button", {
    name: "Load older invocations",
  });
  await loadOlder.evaluate((element) => {
    const button = element as HTMLButtonElement;
    button.click();
    button.click();
  });
  await page
    .waitForFunction(
      () =>
        document.querySelectorAll('[data-testid="invocation-row"]').length ===
        5,
    )
    .catch(() => fail("invocation continuation did not render five rows"));
  if (continuationReads !== beforeContinuation + 1)
    fail("invocation continuation was duplicated or discarded while paused");
  await liveSwitch.check();
  body = (await page.locator("body").textContent()) ?? "";
  if (body.includes("missing_terminal") || body.includes("basis"))
    fail("invocation collection exposed internal outcome semantics");

  await expect(toolCell(invocationIDs.missing)).toHaveText(
    "mcp_gateway.get_identity",
  );
  await expect(toolCell(invocationIDs.missing).getByRole("link")).toHaveCount(
    0,
  );
  await expect(authorizationLabel(invocationIDs.terminal)).toHaveText("Allow");
  await expect(authorizationLabel(invocationIDs.terminal)).toHaveAttribute(
    "data-state",
    "current",
  );
  expect(
    await authorizationLabel(invocationIDs.terminal).evaluate(
      (label) => getComputedStyle(label).color,
    ),
  ).not.toBe(
    await authorizationLabel(invocationIDs.policy).evaluate(
      (label) => getComputedStyle(label).color,
    ),
  );
  const linkScreenshots = await mkdtemp(
    join(tmpdir(), "gateway-history-links-"),
  );
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `tools-${width}.png`);
    await captureScreenshot(page, { path, fullPage: true });
    historyScreenshots.push(path);
  }
  await page.setViewportSize({ width: 1440, height: 900 });

  const authorizationFilter = page.getByLabel("Authorization", { exact: true });
  await authorizationFilter.selectOption("block");
  await expect(authorizationLabel(invocationIDs.policy)).toHaveText("Block");
  await expect(authorizationLabel(invocationIDs.policy)).toHaveAttribute(
    "data-state",
    "neutral",
  );
  await expect(
    page
      .getByTestId("invocation-row")
      .locator('[data-label="Outcome"] .status-label'),
  ).toHaveAttribute("data-state", "neutral");
  const blockScreenshot = join(linkScreenshots, "authorization-block.png");
  await captureScreenshot(page, { path: blockScreenshot, fullPage: true });
  historyScreenshots.push(blockScreenshot);
  await authorizationFilter.selectOption("");
  await expect(page.getByTestId("invocation-row")).toHaveCount(2);

  const toolFilter = page.getByLabel("Tool", { exact: true });
  await toolFilter.fill("namespace.allowed");
  await page
    .waitForFunction(
      () =>
        document.querySelectorAll('[data-testid="invocation-row"]').length ===
        1,
    )
    .catch(() => fail("invocation tool filter did not narrow rows"));
  if (
    !(await page.evaluate(() => window.location.hash)).includes(
      "filter_tool=namespace.allowed",
    )
  )
    fail("invocation tool filter was not persisted in the URL");
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await expect(toolFilter).toHaveValue("");
  const storage = await browserStorage(page);
  if (JSON.stringify(storage).includes("namespace.allowed"))
    fail("invocation filter entered browser storage");

  staleMode = true;
  staleRestarted = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByRole("button", { name: "Load older invocations" }).waitFor();
  await page.getByRole("button", { name: "Load older invocations" }).click();
  await page.waitForFunction(
    (selector) => document.querySelector(selector) !== null,
    `a[href="#/mcp/invocations/${invocationIDs.missing}"]`,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!staleRestarted || body.includes(invocationIDs.stale))
    fail("stale invocation traversal was merged instead of restarted");

  await page
    .locator(`a[href="#/mcp/invocations/${invocationIDs.missing}"]`)
    .click();
  await page.locator('[data-testid="invocation-detail"]').waitFor();
  const detailAuthorization = page
    .getByTestId("invocation-detail")
    .locator(".detail-facts > div")
    .filter({
      has: page.locator("dt").filter({ hasText: /^Authorization decision$/ }),
    })
    .locator(".status-label");
  await expect(detailAuthorization).toHaveText("Allow");
  await expect(detailAuthorization).toHaveAttribute("data-state", "current");
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    invocationIDs.missing,
    "Gateway-owned local target",
    "not proof of downstream handoff",
    "does not automatically replay",
    "explicit caller retry can duplicate an effect",
    "Build agent",
    "Authorization decision",
  ])
    if (!body.includes(phrase)) fail(`invocation detail omitted ${phrase}`);
  if (
    (await page
      .locator('[data-testid="invocation-detail"] .panel-value')
      .count()) !== 0 ||
    (await page
      .locator(
        '[data-testid="invocation-detail"] [data-testid="detail-context"] h1',
      )
      .count()) !== 1 ||
    (await page
      .locator('[data-testid="invocation-detail"]')
      .getByRole("heading", {
        level: 2,
        name: "Invocation details",
        exact: true,
      })
      .count()) !== 1 ||
    (await page
      .locator('[data-testid="invocation-detail"] section.panel h1')
      .count()) !== 0 ||
    (await page
      .locator('[data-testid="invocation-detail"] .detail-facts')
      .count()) !== 3 ||
    (
      await page
        .locator('[data-testid="invocation-detail"] dt')
        .allTextContents()
    ).filter((label) => label === "Invocation ID").length !== 1 ||
    (
      await page
        .locator('[data-testid="invocation-detail"] dt')
        .allTextContents()
    ).includes("ID") ||
    (await page
      .locator(
        `[data-testid="invocation-detail"] a[href="#/agents/${invocationIDs.principal}"]`,
      )
      .count()) !== 1 ||
    (await page
      .locator(
        `[data-testid="invocation-detail"] a[href="#/mcp/grants/${invocationIDs.grant}"]`,
      )
      .count()) !== 1
  )
    fail("invocation detail did not use linked resource facts");
  if (
    !body.includes(captureCanary) ||
    !body.includes("Captured arguments") ||
    body.includes("Other secrets may remain visible") ||
    body.includes("Fixed-redacted arguments") ||
    (await page.locator("script").count()) !== 1 ||
    (await page.evaluate(
      () =>
        (window as unknown as { __invocation_capture__?: boolean })
          .__invocation_capture__ === true,
    ))
  )
    fail("invocation capture was not explained inert item-only content");

  await expect(page.getByTestId("failure-diagnostics")).toHaveCount(0);
  await captureDetailLayout(page, "invocation-missing-terminal");
  await compareBaseline("invocation-missing-terminal");
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `legacy-detail-${width}.png`);
    await captureScreenshot(page, { path, fullPage: true });
    historyScreenshots.push(path);
  }
  failureDiagnostics = {
    gateway_observed: { source: "tool", reason: "reported_error" },
    server_reported: {
      version: 1,
      category: "rate_limit",
      phase: "response_status",
      http_status: 429,
      retry_after_seconds: 12,
    },
  };
  await page.getByTestId("manual-refresh").click();
  await expect(page.getByTestId("failure-diagnostics")).toContainText(
    "Server reported (unverified)",
  );
  for (const text of [
    "Gateway observed",
    "Rate limit",
    "HTTP status: 429",
    "Retry guidance: 12 seconds",
    "The tool may have executed. Retrying could duplicate effects.",
  ])
    await expect(page.getByTestId("failure-diagnostics")).toContainText(text);
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `diagnostics-detail-${width}.png`);
    await captureScreenshot(page, { path, fullPage: true });
    historyScreenshots.push(path);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  }
  failureDiagnostics = {
    gateway_observed: { source: "tool", reason: "reported_error" },
    server_reported: {
      version: 2,
      category: "response_contract",
      phase: "response_validation",
      validation: {
        schema: "models_result",
        version: 1,
        violations: [
          {
            code: "invalid_date",
            path: "$.models.[].release_date",
            rule: "date",
          },
          { code: "missing", path: "$.models.[].name", rule: "required" },
          {
            code: "type",
            path: "$.models.[].description",
            rule: "type",
            expected: "string",
            observed: "number",
          },
        ],
        truncated: true,
      },
    },
  };
  argumentCapture = {};
  await page.getByTestId("manual-refresh").click();
  for (const text of [
    "$.models.[].release_date",
    "Response validation failed",
    "Invalid release date; expected YYYY-MM-DD or RFC 3339 timestamp",
    "Required field is missing",
    "Wrong value type",
    "models_result",
    "$.models.[].name",
    "Expected string; observed number",
    "Additional validation violations omitted",
    "Server reported (unverified)",
  ]) {
    await expect(page.getByTestId("failure-diagnostics")).toContainText(text);
  }
  const diagnosticsPanel = page.getByTestId("failure-diagnostics");
  const technical = diagnosticsPanel.locator("details");
  const disclosure = technical.locator("summary");
  await expect(technical).not.toHaveAttribute("open", "");
  await expect(
    technical.getByText("models_result", { exact: true }),
  ).not.toBeVisible();
  await expect(page.getByTestId("invocation-argument-capture")).toHaveText(
    "Captured arguments · Empty",
  );
  await expect(
    page.getByTestId("invocation-argument-capture").locator("pre"),
  ).toHaveCount(0);
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `validation-detail-${width}.png`);
    await captureScreenshot(page, { path, fullPage: true });
    historyScreenshots.push(path);
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= window.innerWidth,
      ),
    ).toBe(true);
  }
  await disclosure.focus();
  await page.keyboard.press("Enter");
  await expect(technical).toHaveAttribute("open", "");
  for (const text of [
    "models_result",
    "Rule:",
    "Code:",
    "Response contract",
    "Diagnostic version 2",
    "Array positions and dynamic keys are masked.",
  ])
    await expect(technical).toContainText(text);
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `validation-expanded-${width}.png`);
    await diagnosticsPanel.screenshot({ path });
    historyScreenshots.push(path);
  }
  await disclosure.click();
  await expect(technical).not.toHaveAttribute("open", "");
  failureDiagnostics = {
    gateway_observed: { source: "protocol", reason: "rpc_error" },
  };
  await page.getByTestId("manual-refresh").click();
  await expect(page.getByTestId("failure-diagnostics")).toContainText(
    "Rpc error",
  );
  await expect(page.getByTestId("failure-diagnostics")).not.toContainText(
    "Server reported",
  );
  failureDiagnostics = {
    gateway_observed: { source: "transport", reason: "prestart" },
  };
  await page.getByTestId("manual-refresh").click();
  await expect(diagnosticsPanel).toContainText("Prestart");
  await expect(diagnosticsPanel).not.toContainText("may have executed");
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `prestart-${width}.png`);
    await diagnosticsPanel.screenshot({ path });
    historyScreenshots.push(path);
  }
  failureDiagnostics = undefined;
  argumentCapture = "[TRUNCATED]";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page
    .getByText(
      "The redacted capture exceeded 8 KiB; argument content was not retained.",
      { exact: true },
    )
    .waitFor();
  if (
    ((await page.locator("body").textContent()) ?? "").includes(captureCanary)
  )
    fail("truncated invocation retained prior argument content");
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `capture-truncated-${width}.png`);
    await page.getByTestId("invocation-argument-capture").screenshot({ path });
    historyScreenshots.push(path);
  }

  await expect(page.getByTestId("failure-diagnostics")).toHaveCount(0);
  argumentCapture = null;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page
    .getByText("No argument capture was retained.", { exact: true })
    .waitFor();
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    const path = join(linkScreenshots, `capture-absent-${width}.png`);
    await page.getByTestId("invocation-argument-capture").screenshot({ path });
    historyScreenshots.push(path);
  }

  itemMissing = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.locator('[data-testid="invocation-missing"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes(captureCanary) ||
    !body.includes(
      "missing or evicted item does not prove it never existed or never executed",
    )
  )
    fail("evicted invocation guidance was unsafe");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "invocations_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), list_reads: listReads, continuation_reads: continuationReads, item_reads: itemReads, vocabulary_item_reads: vocabularyItemReads, history_screenshots: historyScreenshots })}\n`,
  );
}

export async function runSystemStatus(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  let statusReads = 0;
  let eventStreams = 0;
  let currentStatus = {
    ...overviewStatusFixture(),
    endpoints: {
      authority: "127.0.0.1:8210",
      api: "read_only",
      mcp: "unavailable",
    },
    http_proxy: {
      enabled: true,
      ready: false,
      ca_ready: true,
      authority: "127.0.0.1:8212",
      connections: { in_use: 4, limit: 256, saturated: false },
      work: { in_use: 3, limit: 128, saturated: false },
      active_streams: 2,
      active_tunnels: 1,
    },
    traffic: {
      state: "ready",
      ready: true,
      faulted: false,
      pressure: false,
      budget_bytes: 4294967296,
      database_bytes: 1048576,
      wal_bytes: 65536,
      quota_refusals: 0,
      pruned_records: 3,
      generation: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
      rolling_history: true,
      unknown_completion_possible: true,
    },
  };
  const trafficScreenshots = await mkdtemp(
    join(tmpdir(), "gateway-traffic-status-"),
  );
  let holdStatus = false;
  let failStatus = false;
  const statusReleases: Array<() => void> = [];
  const releaseStatus = () => {
    for (const release of statusReleases.splice(0)) release();
  };
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/v2/events"))
      eventStreams += 1;
  });

  await page.route("**/api/v2/system-status", async (route) => {
    if (
      route.request().method() !== "GET" ||
      new URL(route.request().url()).search !== ""
    )
      fail("System status request changed shape");
    statusReads += 1;
    if (holdStatus) {
      await new Promise<void>((resolve) => {
        statusReleases.push(resolve);
      });
    }
    if (failStatus) {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "unavailable",
          title: "Unavailable",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(currentStatus),
    });
  });
  await page.route(
    "**/api/v2/events",
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": reconnect fixture\n\n",
      }),
    { times: 1 },
  );

  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page
    .waitForFunction(
      () =>
        document
          .querySelector('[data-testid="gateway-shell"]')
          ?.getAttribute("data-freshness") === "reconnecting",
      undefined,
      { timeout: 5000 },
    )
    .catch(() => fail("System fixture did not enter reconnecting state"));
  await page.evaluate(() => {
    window.location.hash = "#/system";
  });
  await page.locator('[data-testid="system-view"]').waitFor();
  await page
    .locator('[data-testid="system-status-operational"]')
    .waitFor({ timeout: 5000 })
    .catch(() => fail("System fixture did not publish initial status"));
  if (
    !((await page.locator("body").textContent()) ?? "").includes(
      "Data reconnecting",
    )
  )
    fail("System did not expose reconnecting freshness");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current" &&
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );

  let body = (await page.locator("body").textContent()) ?? "";
  const systemTabs = await page
    .locator('nav[aria-label="System sections"] a')
    .allTextContents();
  if (
    systemTabs.join("|") !== "Status|Resource limits|Admin credentials|Backups"
  )
    fail(`System tabs were not task-oriented: ${systemTabs.join("|")}`);
  for (const phrase of [
    "Degraded",
    "Needs attention",
    "Gateway is not ready",
    "Storage mutations are unavailable",
    "Credential storage is unavailable",
    "1 resource limit is saturated",
    "Operational state",
    "Technical details",
    "2026-07-28",
    "Agent credentials",
  ])
    if (!body.includes(phrase)) fail(`System status omitted ${phrase}`);
  const statusPanel = page.locator('[data-testid="system-status-panel"]');
  await expect(statusPanel.locator(".detail-section")).toHaveCount(4);
  await expect(
    statusPanel.locator(".panel-heading.detail-section"),
  ).toContainText("Gateway status");
  expect(
    await statusPanel.evaluate(
      (node) => getComputedStyle(node).backgroundColor,
    ),
  ).toBe("rgba(0, 0, 0, 0)");
  await expect(
    statusPanel
      .getByTestId("system-status-material")
      .getByText("Credential storage", { exact: true }),
  ).toBeVisible();
  await expect(
    statusPanel
      .getByTestId("system-status-material")
      .getByText("Control storage", { exact: true }),
  ).toBeVisible();
  if (
    (await statusPanel
      .locator('[data-testid="system-status-summary"]')
      .count()) !== 0 ||
    (await statusPanel
      .locator('[data-testid="system-status-issues"]')
      .count()) !== 1 ||
    (await statusPanel
      .locator('[data-testid="system-status-operational"]')
      .count()) !== 1 ||
    (await statusPanel
      .locator('section[data-testid="system-status-details"]')
      .count()) !== 1 ||
    body.includes("SYSTEM-01")
  )
    fail("System status did not use the shared operator hierarchy");
  if (body.includes("Stopped recovery"))
    fail("System retained the documentation-only recovery tab");
  const processStart = page.locator('time[datetime="2026-08-28T00:00:00Z"]');
  if (
    (await processStart.count()) !== 1 ||
    (await processStart.textContent()) === "2026-08-28T00:00:00Z"
  )
    fail("System status did not render process start in user time");
  if (
    (await page.locator('[data-testid="system-limit-row"]').count()) !== 0 ||
    (await page.locator('a[href="#/system?tab=resource-limits"]').count()) < 1
  )
    fail("System status did not defer detailed limits to their tab");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) !== "storage_latched"
  )
    fail("System did not close mutation admission for latched storage");
  await captureDetailLayout(page, "system-status-latched");

  await expect(
    statusPanel.getByText("Gateway API", { exact: true }),
  ).toBeVisible();
  await expect(
    statusPanel.getByText("MCP endpoint", { exact: true }),
  ).toBeVisible();
  await expect(
    statusPanel.getByText("Read only", { exact: true }),
  ).toBeVisible();
  await expect(
    statusPanel.getByText(
      "New dispatch is blocked by control authority or lifecycle state.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    statusPanel.getByRole("link", {
      name: "View resource limits",
      exact: true,
    }),
  ).toHaveCount(0);
  currentStatus = {
    ...currentStatus,
    endpoints: { ...currentStatus.endpoints, api: "ready", mcp: "ready" },
    process: { ...currentStatus.process, state: "ready", ready: true },
    http_proxy: { ...currentStatus.http_proxy, ready: true },
    sqlite: { ...currentStatus.sqlite, state: "ready", latched: false },
    keyring: { capability: "ready" },
    limits: Object.fromEntries(
      Object.entries(currentStatus.limits).map(([name, limit]) => [
        name,
        { ...limit, in_use: 0, saturated: false },
      ]),
    ),
  };
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByText("Healthy", { exact: true }).waitFor();
  body = (await statusPanel.textContent()) ?? "";
  if (
    (await statusPanel
      .locator('[data-testid="system-status-issues"]')
      .count()) !== 0 ||
    body.includes("No current issues require operator action.") ||
    body.includes("No action required") ||
    body.includes("Gateway is operating normally")
  )
    fail("System healthy status repeated its conclusion");
  await captureDetailLayout(page, "system-status-healthy");

  failStatus = true;
  await page.getByTestId("manual-refresh").click();
  await expect(statusPanel).toHaveAttribute("data-panel-status", "error");
  await expect(
    statusPanel.getByText("Refresh failed — last known status", {
      exact: true,
    }),
  ).toBeVisible();
  await expect(statusPanel.getByText("Healthy", { exact: true })).toHaveCount(
    0,
  );
  await expect(
    statusPanel.getByText("Gateway API", { exact: true }),
  ).toBeVisible();
  await expect(statusPanel.locator(".status-label.current")).toHaveCount(0);
  await captureStateFeedback(page, "system-stale");
  failStatus = false;
  await page.getByTestId("manual-refresh").click();
  await expect(statusPanel.getByText("Healthy", { exact: true })).toBeVisible();
  await expect(
    statusPanel.getByText("Refresh failed — last known status", {
      exact: true,
    }),
  ).toHaveCount(0);

  for (const historyState of [
    "ready",
    "faulted",
    "opening",
    "unavailable",
    "disabled",
  ]) {
    const faulted = historyState === "faulted";
    const ready = historyState === "ready";
    currentStatus = {
      ...currentStatus,
      http_proxy: { ...currentStatus.http_proxy, ready: true },
      traffic: {
        ...currentStatus.traffic,
        state: historyState,
        ready,
        faulted,
        pressure: faulted,
      },
    };
    await page.locator('[data-testid="manual-refresh"]').click();
    await page
      .getByText(!ready ? "Optional traffic history" : "Healthy", {
        exact: true,
      })
      .waitFor();
    if (
      (await page
        .locator('[data-testid="gateway-shell"]')
        .getAttribute("data-mutation-availability")) !== "enabled"
    )
      fail("Traffic-only failure disabled healthy administration");
    if (!ready) {
      await expect(
        page.getByText(
          historyState === "opening"
            ? "History is opening. Security-ready serving does not wait for it."
            : "Authorized MCP, HTTP and Git execution can continue without history. Missing records do not prove nonexecution; never automatically replay calls.",
          { exact: true },
        ),
      ).toBeVisible();
      await expect(
        page.getByText("HTTP proxy is unavailable", { exact: true }),
      ).toHaveCount(0);
    }
    await expect(
      page.getByText("Shared traffic storage", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText("HTTP proxy", { exact: true })).toBeVisible();
    await expect(
      page.getByText("1.1 MiB / 4 GiB · 0% used", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("1,114,112 / 4,294,967,296 bytes (database + WAL)", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByText("2 active requests/streams · 1 opaque tunnels", {
        exact: true,
      }),
    ).toBeVisible();
    for (const width of [1280, 390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      if (
        await page.evaluate(
          () => document.documentElement.scrollWidth > window.innerWidth,
        )
      )
        fail("Traffic status overflowed the viewport");
      if (!hasCaptureOwner(page)) {
        const violations = (
          await new AxeBuilder({ page }).analyze()
        ).violations.filter(
          (item) => item.impact === "serious" || item.impact === "critical",
        );
        if (violations.length) fail("Traffic status accessibility regression");
      }
      await captureScreenshot(page, {
        path: join(
          trafficScreenshots,
          `${faulted ? "fault" : historyState}-${width}.png`,
        ),
        fullPage: true,
      });
    }
  }
  currentStatus = {
    ...currentStatus,
    http_proxy: { ...currentStatus.http_proxy, ready: true },
    traffic: {
      ...currentStatus.traffic,
      state: "ready",
      ready: true,
      faulted: false,
      pressure: false,
    },
  };
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByText("Healthy", { exact: true }).waitFor();

  holdStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await eventually(
    () => statusReleases.length > 0,
    "System refresh did not start",
  );
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes("Data stale") ||
    !body.includes("Technical details") ||
    !body.includes("2026-07-28")
  )
    fail("System refresh flashed stale text or discarded current status");
  holdStatus = false;
  releaseStatus?.();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=resource-limits";
  });
  await page.locator('[data-testid="system-limits-view"]').waitFor();
  const limitRows = await page
    .locator('[data-testid="system-limit-row"]')
    .count();
  if (limitRows !== overviewLimitNames.length)
    fail("Resource limits did not render every closed limit");
  await assertTableConventions(
    page,
    "Gateway resource occupancy and hard limits",
    ["Resource", "In use", "Limit", "Used (%)", "Status"],
    "Resource",
    true,
  );
  if (
    (
      (await page
        .locator('[data-testid="system-limits-view"]')
        .textContent()) ?? ""
    ).includes("Current occupancy against enforced Gateway limits.")
  )
    fail("Resource limits retained redundant occupancy guidance");

  const utilizationCases = [
    [0, 32, "0%", true],
    [8, 32, "25%", false],
    [27, 32, "84.4%", false],
    [32, 32, "100%", true],
    [9999, 10000, "99.9%", false],
    [1, 0, "N/A", false],
    [33, 32, "103.1%", false],
    [Number.MAX_SAFE_INTEGER, 32, "28147497671065596.9%", false],
  ] as const;
  const expectedRows = overviewLimitNames
    .map((name, index) => {
      const [inUse, limit, used, saturated] =
        utilizationCases[index % utilizationCases.length]!;
      currentStatus.limits[name] = { in_use: inUse, limit, saturated };
      return { name, inUse, limit, used, saturated };
    })
    .sort((left, right) =>
      left.saturated !== right.saturated
        ? left.saturated
          ? -1
          : 1
        : left.name.localeCompare(right.name),
    );
  const rowContents = () =>
    page
      .locator('[data-testid="system-limit-row"]')
      .evaluateAll((rows) =>
        rows.map((row) => Array.from(row.children, (cell) => cell.textContent)),
      );
  const beforeRefresh = await rowContents();
  holdStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await eventually(
    () => statusReleases.length > 0,
    "Resource refresh did not start",
  );
  expect(await rowContents()).toEqual(beforeRefresh);
  await expect(page.locator('[data-testid="gateway-shell"]')).toHaveAttribute(
    "data-freshness",
    "current",
  );
  holdStatus = false;
  releaseStatus();
  const expectedCells = expectedRows.map(
    ({ name, inUse, limit, used, saturated }) => [
      name === "principals" ? "Agents" : name,
      String(inUse),
      String(limit),
      used,
      saturated ? "Saturated" : "Available",
    ],
  );
  await expect.poll(rowContents).toEqual(expectedCells);
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );

  failStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await expect(
    page.locator('[data-testid="system-limits-view"]'),
  ).toHaveAttribute("data-panel-status", "error");
  expect(await rowContents()).toEqual(expectedCells);
  await expect(
    page
      .getByTestId("system-limits-view")
      .getByText("Refresh failed — last known limits", { exact: true }),
  ).toBeVisible();
  await expect(
    page
      .getByTestId("system-limits-view")
      .locator(".panel-heading .status-label"),
  ).toHaveCount(0);
  await captureStateFeedback(page, "limits-stale");
  await captureScreenshot(page, {
    path: join(trafficScreenshots, "resources-error-1280.png"),
    fullPage: true,
  });
  failStatus = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  await expect(
    page.locator('[data-testid="system-limits-view"]'),
  ).toHaveAttribute("data-panel-status", "current");
  expect(await rowContents()).toEqual(expectedCells);

  const limitsTable = page.getByRole("table", {
    name: "Gateway resource occupancy and hard limits",
  });
  for (const width of [1280, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    expect(
      await limitsTable.getByRole("columnheader").allTextContents(),
    ).toEqual(["Resource", "In use", "Limit", "Used (%)", "Status"]);
    expect(await limitsTable.getByRole("rowheader").allTextContents()).toEqual(
      expectedRows.map(({ name }) => (name === "principals" ? "Agents" : name)),
    );
    expect(
      await limitsTable
        .locator("tbody tr:first-child td:nth-child(4)")
        .evaluate((cell) => getComputedStyle(cell).textAlign),
    ).toBe("right");
    expect(
      await limitsTable
        .getByRole("columnheader", { name: "Used (%)", exact: true })
        .evaluate((cell) => getComputedStyle(cell).textAlign),
    ).toBe("right");
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth > window.innerWidth,
      ),
    ).toBe(false);
    if (!hasCaptureOwner(page)) {
      const violations = (
        await new AxeBuilder({ page }).analyze()
      ).violations.filter(
        (item) => item.impact === "serious" || item.impact === "critical",
      );
      expect(violations).toEqual([]);
    }
    await captureScreenshot(page, {
      path: join(trafficScreenshots, `resources-${width}.png`),
      fullPage: true,
    });
    if (width < 700) {
      const region = limitsTable.locator("..");
      await region.focus();
      await expect(region).toBeFocused();
      await region.evaluate((element) => {
        element.scrollLeft = element.scrollWidth;
      });
      await expect(
        limitsTable.getByRole("columnheader", {
          name: "Used (%)",
          exact: true,
        }),
      ).toBeInViewport();
      await expect(
        limitsTable.getByRole("columnheader", { name: "Status", exact: true }),
      ).toBeInViewport();
      await captureScreenshot(page, {
        path: join(trafficScreenshots, `resources-scrolled-${width}.png`),
        fullPage: false,
      });
      await region.evaluate((element) => {
        element.scrollLeft = 0;
      });
    }
  }

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "system_status_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), status_reads: statusReads, event_streams: eventStreams, limit_rows: limitRows, traffic_screenshots: trafficScreenshots })}\n`,
  );
}
