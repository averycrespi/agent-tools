import AxeBuilder from "@axe-core/playwright";
import { type BrowserContext, type Page } from "@playwright/test";
import {
  assertSecretAbsent,
  browserStorage,
  eventually,
  fail,
  waitForLifecycle,
} from "./shared.ts";
import { assertViewGenerationFoundation } from "./foundations.ts";
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
    "Operational conditions",
    "active catalog tools",
    "configured servers",
    "Waiting for a decision",
  ])
    if (!body.includes(phrase))
      fail(`Overview workflow canary omitted ${phrase}`);
  if (body.includes("redacted_arguments"))
    fail("Overview workflow canary exposed invocation capture");

  await page.locator('#primary-navigation a[href="#/invocations"]').click();
  await page.locator('[data-testid="invocations-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="invocations-view"]')
        ?.textContent?.includes("No retained invocations match") === true,
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
      /\/api\/v1\/(?:admin-credentials|backups)/.test(request.url())
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
  await assertViewGenerationFoundation();
  let eventStreams = 0;
  let mutations = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/v1/events"))
      eventStreams += 1;
  });
  await page.route("**/api/v1/system-status", async (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(overviewStatusFixture()),
    }),
  );
  await page.route("**/api/v1/admin-credentials?*", async (route) => {
    if (route.request().method() !== "GET") mutations += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [], next_cursor: null }),
    });
  });
  await page.route("**/api/v1/backups?*", async (route) => {
    if (route.request().method() !== "GET") mutations += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [], next_cursor: null }),
    });
  });
  await page.route(
    "**/api/v1/events",
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
    id: ids[index],
    created_at: "2026-08-28T12:00:00Z",
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
  let recoveryKey: string | undefined;
  await page.route("**/api/v1/backups**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const id =
      url.pathname === "/api/v1/backups"
        ? undefined
        : url.pathname.split("/").pop();
    if (request.method() === "GET" && id === undefined) {
      if (url.searchParams.get("limit") !== "100")
        fail("backup list changed shape");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items, next_cursor: null }),
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
  await page.locator('[data-testid="backup-inspect"]').click();
  await page.locator('[data-testid="backup-detail"]').waitFor();
  await page.locator('[data-testid="backup-create"]').click();
  await page.locator('[data-testid="backup-create-view"]').waitFor();
  await page.locator('[data-testid="backup-review-create"]').click();
  if (Number(creates) !== 0) fail("backup submitted before final review");
  await page.locator('[data-testid="backup-create-confirm-submit"]').click();
  await page.getByText("Backup outcome is unknown", { exact: true }).waitFor();
  if (creates !== 1) fail("uncertain backup create replayed automatically");
  await page.locator('[data-testid="backup-replay"]').click();
  await page.getByText(/is durably published/).waitFor();
  await page.locator('[data-testid="backup-delete"]').first().click();
  await page.locator('[data-testid="backup-delete-confirm-submit"]').click();
  await page.getByText(/Backup deleted/).waitFor();
  await page.locator('[data-testid="backup-create"]').click();
  await page.locator('[data-testid="backup-review-create"]').click();
  await page.locator('[data-testid="backup-create-confirm-submit"]').click();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-mutation-availability") === "storage_latched",
  );
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
  let releaseLost: (() => void) | undefined;
  let markLostStarted: (() => void) | undefined;
  const lostStarted = new Promise<void>((resolve) => {
    markLostStarted = resolve;
  });

  await page.route("**/api/v1/admin-credentials**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const id =
      path === "/api/v1/admin-credentials" ? undefined : path.split("/").pop();
    if (request.method() === "GET" && id === undefined) {
      const query = new URL(request.url()).searchParams;
      if (query.get("limit") !== "100")
        fail("admin credential list changed shape");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items, next_cursor: null }),
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
    .allTextContents();
  if (
    adminHeaders.map((value) => value.replace(/\s?[↑↓↕]$/, "")).join("|") !==
    "Fingerprint|ID|Status|Created|Expires|Actions"
  )
    fail(`Admin credential columns drifted: ${adminHeaders.join("|")}`);
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
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.locator('[data-testid="admin-credential-create-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-expiry"]').fill("invalid");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.getByText(/Expiry must be an RFC 3339 time/).waitFor();
  if (creates !== 0) fail("invalid admin expiry reached the API");
  await page.locator('[data-testid="admin-credential-expiry"]').fill("");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .getByRole("heading", { name: "Review admin credential", exact: true })
    .waitFor();
  if (creates !== 0) fail("admin credential submitted before final review");
  await page
    .locator('[data-testid="admin-credential-create-confirm-submit"]')
    .click();
  await page.locator('[data-testid="one-time-value"]').waitFor();
  if (
    (await page.locator('[data-testid="one-time-value"]').textContent()) !==
    issuedBearer
  )
    fail("admin bearer did not reach the prepared sink");
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
  const logoutResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/admin-sessions/current" &&
      response.request().method() === "DELETE",
  );
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await logoutResponse;
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
  let invocationReads = 0;
  let serverMode:
    | "complete"
    | "stale"
    | "partial"
    | "quiet"
    | "empty"
    | "error" = "complete";
  let statusMode: "abnormal" | "quiet" | "error" = "abnormal";
  let requestMode: "populated" | "quiet" | "error" = "populated";
  let staleRestarted = false;
  let heldStatus = true;
  let releaseHeldStatus: (() => void) | undefined;
  let heldStatusPromise = new Promise<void>((resolve) => {
    releaseHeldStatus = resolve;
  });
  let heldStatusReads = 0;

  await page.route("**/api/v1/system-status", async (route) => {
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
    const status = overviewStatusFixture();
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
  await page.route("**/api/v1/servers?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "100" ||
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
    if (serverMode === "quiet" || serverMode === "empty") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items:
            serverMode === "empty"
              ? []
              : [
                  overviewServer(
                    "01ARZ3NDEKTSV4RRFFQ69G5FA0",
                    "Quiet server",
                    "active",
                  ),
                ],
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
  await page.route("**/api/v1/grant-requests?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "5" ||
      query.get("state") !== "pending" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "state")
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
                  target:
                    index === 0
                      ? `requested-${"T".repeat(180)}`
                      : `target-${index}`,
                },
              })),
        next_cursor: requestMode === "quiet" ? null : "more-pending",
      }),
    });
  });
  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: overviewRequestFixture().principal_id,
            display_name: "Overview agent",
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
  await page.route("**/api/v1/invocations?*", async (route) => {
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
    overviewPhase = state;
    if ((await overview.locator("nav").count()) !== 0)
      fail(`Overview ${state} retained redundant destination navigation`);
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
        const screenshot = await page.screenshot({ fullPage: true });
        if (screenshot.length === 0) fail(`Overview ${state} screenshot empty`);
      }
    }
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.emulateMedia({ colorScheme: "light" });
  };
  await assertCurrent("servers");
  await assertCurrent("requests");
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
    "9 configured servers",
    "8 active catalog tools",
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
      .map((suffix) => `#/servers/01ARZ3NDEKTSV4RRFFQ69G5${suffix}?tab=status`)
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
      (_, index) => `#/requests/01ARZ3NDEKTSV4RRFFQ69G5FC${index}`,
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
    "#/servers",
    "#/catalog",
    "#/requests",
    "#/invocations",
  ])
    if (
      (await page.locator(`#primary-navigation a[href="${href}"]`).count()) !==
      1
    )
      fail(`Primary navigation omitted destination ${href}`);
  for (const [name, href] of [
    ["Inspect System", "#/system"],
    ["Inspect storage status", "#/system"],
    ["Inspect keyring status", "#/system"],
    ["Inspect resource limits", "#/system?tab=resource-limits"],
  ] as const)
    if (
      (await source("status")
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
  const accessibility = await new AxeBuilder({ page })
    .include('[data-testid="overview-grid"]')
    .analyze();
  if (accessibility.violations.length > 0)
    fail(
      `Overview accessibility violations: ${accessibility.violations.map((item) => item.id).join(",")}`,
    );
  await capture("attention");
  await page.waitForTimeout(5100);
  if (invocationReads !== 0)
    fail("Overview performed invocation reads or polling");

  serverMode = "stale";
  staleRestarted = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("1 configured"),
  );
  if (
    !staleRestarted ||
    ((await page.locator("body").textContent()) ?? "").includes(
      "Discarded stale server",
    )
  )
    fail("stale server traversal was not restarted cleanly");

  serverMode = "partial";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("counts incomplete"),
  );
  if (
    !(await sourceText("servers")).includes(
      "additional affected servers may exist",
    ) ||
    !(await sourceText("servers")).includes("loaded; counts incomplete")
  )
    fail("Partial traversal implied an exhaustive count");
  await capture("partial");

  statusMode = "quiet";
  serverMode = "quiet";
  requestMode = "quiet";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("No servers flagged"),
  );
  await assertCurrent("status");
  await assertCurrent("requests");
  const quiet = (await overview.textContent()) ?? "";
  for (const text of [
    "Process ready",
    "SQLite ready · not latched",
    "Keyring ready",
    "No servers flagged for attention in the current read",
    "No pending access requests in the current read",
    "1 configured server",
    "1 active catalog tool",
  ])
    if (!quiet.includes(text)) fail(`Quiet Overview omitted ${text}`);
  if (
    quiet.includes("capacity pressure") ||
    quiet.includes("saturated") ||
    quiet.includes("healthy") ||
    quiet.length > 650
  )
    fail("Quiet Overview was not short or evidence-bounded");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) === "storage_latched"
  )
    fail("Fresh unlatched read did not reopen admission");
  await capture("quiet");

  serverMode = "empty";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("No servers configured"),
  );
  if (
    !(await sourceText("servers")).includes(
      "0 configured servers · 0 active catalog tools",
    )
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
  await page.locator('[data-testid="manual-refresh"]').click();
  for (const id of ["status", "servers", "requests"]) await assertCurrent(id);
  await page.route("**/api/v1/events", (route) =>
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
  await page.unroute("**/api/v1/events");
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
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "overview_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), invocation_reads: invocationReads })}\n`,
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

  const captureCanary = `INVOCATION_CAPTURE_<script>${"C".repeat(64)}`;
  let argumentCapture: unknown = {
    note: captureCanary,
    token: "[REDACTED]",
  };
  let listReads = 0;
  let continuationReads = 0;
  let itemReads = 0;
  let staleMode = false;
  let staleRestarted = false;
  let itemMissing = false;
  await page.route("**/api/v1/invocations**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const headers = await request.allHeaders();
    if (
      request.method() !== "GET" ||
      request.postData() !== null ||
      headers["x-csrf-token"] === undefined
    )
      fail("invocation view issued an unauthenticated or non-read request");
    if (url.pathname !== "/api/v1/invocations") {
      if (
        url.pathname !== `/api/v1/invocations/${invocationIDs.missing}` ||
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
              "missing_terminal",
              "outcome_unknown",
              "gateway",
            ),
            redacted_arguments: argumentCapture,
          }),
        });
      }
      return;
    }
    const query = url.searchParams;
    const allowed = new Set(["limit", "cursor"]);
    if (
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => !allowed.has(key)) ||
      [...query.keys()].some((key) => query.getAll(key).length !== 1)
    )
      fail("invocation list request changed shape");
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
      await new Promise((resolve) => setTimeout(resolve, 80));
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

  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: invocationIDs.principal,
            display_name: "Build agent",
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
    window.location.hash = "#/invocations";
  });
  await page.locator('[data-testid="invocations-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="invocation-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Invocation",
    "Tool",
    "Principal",
    "Decision",
    "Outcome",
    "Admitted",
  ])
    if (!body.includes(phrase)) fail(`invocation list omitted ${phrase}`);
  for (const phrase of [
    "Invocation evidence",
    "retains at most 4,096 recent rows",
    "Filtered pages are independently coherent",
    "gateway:get_identity",
  ])
    if (body.includes(phrase)) fail(`invocation list retained ${phrase}`);
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
  await page.getByRole("button", { name: "Reset" }).click();
  if ((await toolFilter.inputValue()) !== "")
    fail("invocation filter Reset did not clear the field");
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
    `a[href="#/invocations/${invocationIDs.missing}"]`,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!staleRestarted || body.includes(invocationIDs.stale))
    fail("stale invocation traversal was merged instead of restarted");

  await page
    .locator(`a[href="#/invocations/${invocationIDs.missing}"]`)
    .click();
  await page.locator('[data-testid="invocation-detail"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    `Invocation ${invocationIDs.missing}`,
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
      .locator('[data-testid="invocation-detail"] .fact-grid')
      .count()) !== 1 ||
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
        `[data-testid="invocation-detail"] a[href="#/principals/${invocationIDs.principal}"]`,
      )
      .count()) !== 1 ||
    (await page
      .locator(
        `[data-testid="invocation-detail"] a[href="#/grants/${invocationIDs.grant}"]`,
      )
      .count()) !== 1
  )
    fail("invocation detail did not use linked resource facts");
  if (
    !body.includes(captureCanary) ||
    !body.includes("Retained argument capture") ||
    !body.includes("Other secrets may remain visible") ||
    body.includes("Fixed-redacted arguments") ||
    (await page.locator("script").count()) !== 1 ||
    (await page.evaluate(
      () =>
        (window as unknown as { __invocation_capture__?: boolean })
          .__invocation_capture__ === true,
    ))
  )
    fail("invocation capture was not explained inert item-only content");

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

  argumentCapture = null;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page
    .getByText("No argument capture was retained.", { exact: true })
    .waitFor();

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
    `${JSON.stringify({ event: "invocations_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), list_reads: listReads, continuation_reads: continuationReads, item_reads: itemReads })}\n`,
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
  let currentStatus = overviewStatusFixture();
  let holdStatus = false;
  let releaseStatus: (() => void) | undefined;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/v1/events"))
      eventStreams += 1;
  });

  await page.route("**/api/v1/system-status", async (route) => {
    if (
      route.request().method() !== "GET" ||
      new URL(route.request().url()).search !== ""
    )
      fail("System status request changed shape");
    statusReads += 1;
    if (holdStatus) {
      await new Promise<void>((resolve) => {
        releaseStatus = resolve;
      });
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(currentStatus),
    });
  });
  await page.route(
    "**/api/v1/events",
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
    "Principal credentials",
  ])
    if (!body.includes(phrase)) fail(`System status omitted ${phrase}`);
  const statusPanel = page.locator('[data-testid="system-status-panel"]');
  if (
    !(await statusPanel.getAttribute("class"))
      ?.split(/\s+/)
      .includes("panel") ||
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

  currentStatus = {
    ...currentStatus,
    process: { ...currentStatus.process, state: "ready", ready: true },
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
    (body.match(/No current issues require operator action\./g) ?? [])
      .length !== 1 ||
    body.includes("No action required") ||
    body.includes("Gateway is operating normally")
  )
    fail("System healthy status repeated its conclusion");

  holdStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await eventually(
    () => releaseStatus !== undefined,
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
  if (
    (
      (await page
        .locator('[data-testid="system-limits-view"]')
        .textContent()) ?? ""
    ).includes("Current occupancy against enforced Gateway limits.")
  )
    fail("Resource limits retained redundant occupancy guidance");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "system_status_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), status_reads: statusReads, event_streams: eventStreams, limit_rows: limitRows })}\n`,
  );
}
