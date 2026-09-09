import AxeBuilder from "@axe-core/playwright";
import { expect, type BrowserContext, type Page } from "@playwright/test";
import {
  assertSecretAbsent,
  fail,
  waitForCollectionRows,
  waitForLifecycle,
} from "./shared.ts";
import { exerciseCollectionPagination } from "../collection-pagination.ts";
import { serverReadFixture } from "./fixtures.ts";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

async function captureRequestState(page: Page, state: string): Promise<void> {
  const directory = await mkdtemp(join(tmpdir(), `request-${state}-`));
  for (const [width, height, label] of [
    [1280, 900, "desktop"],
    [320, 900, "narrow"],
    [720, 450, "200-reference"],
  ] as const) {
    await page.setViewportSize({ width, height });
    if (
      await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth,
      )
    )
      fail(`Request ${state} overflows at ${label}`);
    const dialog = page.locator("dialog[open]");
    const modal = (await dialog.count()) > 0;
    await page.screenshot({
      path: join(directory, `${label}.png`),
      fullPage: !modal,
    });
    if (state.endsWith("-table")) {
      const table = page.locator(".table-region");
      await table.evaluate((node) => {
        node.scrollLeft = node.scrollWidth;
      });
      await page.screenshot({ path: join(directory, `${label}-columns.png`) });
      const countCell = table.locator('[data-label="Constraints"]').first();
      const cellBounds = (await countCell.boundingBox())!;
      const tableBounds = (await table.boundingBox())!;
      expect(cellBounds.x).toBeGreaterThanOrEqual(tableBounds.x);
      expect(cellBounds.x + cellBounds.width).toBeLessThanOrEqual(
        tableBounds.x + tableBounds.width,
      );
      await table.evaluate((node) => {
        node.scrollLeft = 0;
      });
    }
    if (modal) {
      await dialog.locator('[data-testid$="-submit"]').scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(directory, `${label}-actions.png`) });
      await dialog.evaluate((element) => {
        element.scrollTop = 0;
      });
    }
  }
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.emulateMedia({ colorScheme: "dark" });
  await page.screenshot({
    path: join(directory, "dark-desktop.png"),
    fullPage: (await page.locator("dialog[open]").count()) === 0,
  });
  if (state.endsWith("-table")) {
    await page.locator(".table-region").evaluate((node) => {
      node.scrollLeft = node.scrollWidth;
    });
    await page.screenshot({
      path: join(directory, "dark-desktop-columns.png"),
    });
    await page.setViewportSize({ width: 1600, height: 900 });
    await page.locator(".table-region").evaluate((node) => {
      node.scrollLeft = 0;
    });
    await page.screenshot({ path: join(directory, "wide-desktop.png") });
    await page.setViewportSize({ width: 1280, height: 900 });
  }
  await page.emulateMedia({ colorScheme: "light" });
  const audit = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  const violations = audit.violations.filter(
    (item) => item.impact === "serious" || item.impact === "critical",
  );
  if (violations.length > 0)
    fail(
      `Request ${state} accessibility: ${JSON.stringify(violations.map((item) => ({ id: item.id, targets: item.nodes.map((node) => node.target) })))}`,
    );
}

export async function runReadOnlyBackendFlow(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  // No route fixtures: all domain reads and mutations reach the real Gateway.
  await page.evaluate(() => {
    window.location.hash = "#/requests";
  });
  await waitForLifecycle(page, "signed_out");
  await page.getByTestId("admin-bearer-input").fill(bearer);
  await page.getByTestId("sign-in-submit").click();
  await waitForLifecycle(page, "authenticated");
  await page.getByRole("link", { name: "Review", exact: true }).click();
  await page.getByTestId("request-actions").waitFor();
  await page
    .getByRole("button", { name: "Customize approval", exact: true })
    .click();
  await page
    .getByRole("combobox", { name: "Allowed tools" })
    .selectOption("read-only");
  await expect(
    page.getByRole("region", { name: "Approval preview" }),
  ).toContainText("Read-only server tools");
  await page.getByTestId("request-approve").click();
  await expect(page.getByRole("dialog")).toContainText(
    "Read-only server tools",
  );
  await captureRequestState(page, "backend-read-only-approval");
  await page.getByTestId("request-adjudication-confirm-submit").click();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();
  await expect(
    page.getByRole("region", {
      name: "Requested versus approved",
      exact: true,
    }),
  ).toContainText("All tools → Read-only server tools");
  await captureRequestState(page, "backend-read-only-approved");
  const createdGrant = page
    .getByTestId("request-detail")
    .locator('a[href^="#/grants/"]')
    .first();
  await createdGrant.click();
  await expect(page.getByTestId("grant-detail")).toContainText(
    "Entire server — read-only tools",
  );
  await captureRequestState(page, "backend-read-only-grant");
  await page.evaluate(() => {
    window.location.hash = "#/access/grants/new";
  });
  await page.getByTestId("grant-create-view").waitFor();
  await page.getByTestId("grant-description").fill("Browser read-only grant");
  const principal = await page
    .getByTestId("grant-principal")
    .locator("option")
    .filter({ hasText: "Read-only client agent" })
    .getAttribute("value");
  const server = await page
    .getByTestId("grant-server")
    .locator("option")
    .filter({ hasText: "readonlyclients" })
    .first()
    .getAttribute("value");
  if (principal === null || server === null)
    fail("real read-only fixture selectors missing");
  await page.getByTestId("grant-principal").selectOption(principal);
  await page.getByTestId("grant-server").selectOption(server);
  await page
    .getByRole("combobox", { name: "Allowed tools" })
    .selectOption("read-only");
  await page.getByTestId("grant-create-submit").click();
  await expect(page.getByRole("dialog")).toContainText("Read-only tools");
  await page.getByTestId("grant-create-confirm-submit").click();
  await expect(page.getByTestId("grant-detail")).toContainText(
    "Entire server — read-only tools",
  );
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "read_only_backend_complete", chromium_version: browserVersion, requests: requestCount() })}\n`,
  );
}

export async function runAccessManagementReadCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FB0";
  const requestID = "01ARZ3NDEKTSV4RRFFQ69G5FC0";
  let mutationCount = 0;
  const principal = {
    id: principalID,
    display_name: "Build agent",
    state: "active",
    visibility: "requestable",
    revision: "7",
    credential_revision: "3",
    credential: {
      id: "01ARZ3NDEKTSV4RRFFQ69G5FAD",
      fingerprint: "0123456789abcdef",
      revision: "3",
      created_at: "2026-08-28T12:00:00Z",
    },
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
  };
  const grant = {
    id: grantID,
    description: "Safe build access",
    revision: "1",
    principal_id: principalID,
    effect: "allow",
    server_id: serverID,
    upstream_name: "safe",
    constraint: { equals: { "/mode": "safe" } },
    expires_at: null,
    state: "active",
    created_at: "2026-08-28T12:00:00Z",
  };
  const requestedPolicy = {
    scope: "tool",
    target: "demo.safe",
    constraint: { equals: { "/mode": "safe" } },
    duration_seconds: "600",
    future_tools_acknowledged: false,
  };
  const request = {
    id: requestID,
    principal_id: principalID,
    state: "pending",
    revision: "4",
    requested_policy: requestedPolicy,
    approved_policy: null,
    approved_grant_id: null,
    rejection_reason: null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
    closed_at: null,
    resolved_server_id: serverID,
    resolved_upstream_name: "safe",
    submitted_evidence: null,
    approved_evidence: null,
    current_target: {
      scope: "tool",
      target_state: "extant",
      active_state: "current",
      durable_state: "current",
      catalog_revision: "10",
      fingerprint: "current-access-fingerprint",
      descriptor: { name: "safe", inputSchema: {}, annotations: {} },
    },
  };
  await page.route("**/api/v1/principals", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([principal]),
    });
  });
  await page.route("**/api/v1/servers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        {
          id: serverID,
          display_name: "Build server",
          namespace: "demo",
          created_at: "2026-08-28T12:00:00Z",
        },
      ]),
    });
  });
  await page.route("**/api/v1/principals/**", async (route) => {
    if (route.request().method() !== "GET") {
      mutationCount += 1;
      await route.abort();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${principalID}-7"` },
      body: JSON.stringify(principal),
    });
  });
  await page.route("**/api/v1/grants/**", async (route) => {
    if (route.request().method() !== "GET") {
      mutationCount += 1;
      await route.abort();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(grant),
    });
  });
  await page.route("**/api/v1/grant-requests/**", async (route) => {
    if (route.request().method() !== "GET") {
      mutationCount += 1;
      await route.abort();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"grant-request-${requestID}-4"` },
      body: JSON.stringify(request),
    });
  });

  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  const destinations: Array<[string, string]> = [
    [`#/access/principals/${principalID}`, "principal-detail"],
    [`#/access/grants/${grantID}`, "grant-detail"],
    [`#/requests/${requestID}`, "request-detail"],
  ];
  for (const [hash, testID] of destinations) {
    await page.evaluate((target) => {
      window.location.hash = target;
    }, hash);
    await page.locator(`[data-testid="${testID}"]`).waitFor();
  }
  await page.locator('[data-testid="request-actions"]').waitFor();
  const body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Submitted: no descriptor evidence",
    "Current target",
    "Approval grants the selected authority",
    "it does not execute or retry a call",
  ])
    if (!body.includes(phrase))
      fail(`Access management read canary omitted ${phrase}`);
  if (body.includes("authorization_url") || body.includes("raw_result"))
    fail(
      "Access management read canary exposed one-time or raw evidence material",
    );
  if (mutationCount !== 0)
    fail("Access management read canary replayed an adjudication mutation");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "access_management_read_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: destinations.length, mutations: mutationCount })}\n`,
  );
}

export async function runPrincipals(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const firstID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const secondID = "01ARZ3NDEKTSV4RRFFQ69G5FA1";
  const createdID = "01ARZ3NDEKTSV4RRFFQ69G5FA2";
  const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FB0";
  const principal = (
    id: string,
    displayName: string,
    state: "active" | "disabled",
    visibility: "requestable" | "allowed-only" | "all",
    revision: string,
  ) => ({
    id,
    display_name: displayName,
    state,
    visibility,
    revision,
    credential_revision: state === "disabled" ? "2" : "1",
    credential:
      state === "active"
        ? {
            id: "01ARZ3NDEKTSV4RRFFQ69G5FAZ",
            fingerprint: "0123456789abcdef",
            revision: "1",
            created_at: "2026-08-28T12:00:00Z",
          }
        : null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
  });
  let current = principal(firstID, "Build agent", "active", "requestable", "1");
  let detailReads = 0;
  let creates = 0;
  let updates = 0;
  let staleListRestarted = false;
  let staleReturned = false;

  await page.route("**/api/v1/principals?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      [...query.keys()].some(
        (key) =>
          ![
            "limit",
            "cursor",
            "sort",
            "direction",
            "name",
            "state",
            "visibility",
          ].includes(key),
      )
    )
      fail("principal list request changed shape");
    if (query.get("cursor") === "principal-stale") {
      staleListRestarted = true;
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
    const search = query.get("name") ?? "";
    const items = [
      current,
      principal(secondID, "Disabled agent", "disabled", "all", "4"),
    ].filter((item) =>
      search === "Build agnt"
        ? item.id === firstID
        : item.display_name.toLowerCase().includes(search.toLowerCase()) ||
          item.id.includes(search),
    );
    items.sort((a, b) => a.display_name.localeCompare(b.display_name));
    if (query.get("direction") === "descending") items.reverse();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items,
        next_cursor:
          !staleListRestarted && search === "" ? "principal-stale" : null,
        ...(query.has("sort")
          ? {
              total_count:
                items.length + (!staleListRestarted && search === "" ? 1 : 0),
              offset: 0,
            }
          : {}),
      }),
    });
  });
  await page.route(`${baseURL}/api/v1/principals`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    creates += 1;
    const body = JSON.parse(route.request().postData() ?? "null") as Record<
      string,
      unknown
    >;
    if (
      body.display_name !== "New automation" ||
      body.visibility !== "allowed-only" ||
      Object.keys(body).sort().join(",") !== "display_name,visibility"
    )
      fail("principal create body changed shape");
    const created = principal(
      createdID,
      "New automation",
      "active",
      "allowed-only",
      "1",
    );
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      headers: { ETag: `"principal-${createdID}-1"` },
      body: JSON.stringify({
        principal: created,
        default_grant: {
          id: grantID,
          description: "Default Gateway access",
          revision: "1",
          principal_id: createdID,
          effect: "allow",
          server_id: "00000000000000000000000000",
          upstream_name: null,
          constraint: null,
          expires_at: null,
          state: "active",
          created_at: "2026-08-28T13:00:00Z",
        },
      }),
    });
  });
  await page.route("**/api/v1/principals/*", async (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop();
    const request = route.request();
    if (request.method() === "GET") {
      detailReads += 1;
      const value =
        id === createdID
          ? principal(
              createdID,
              "New automation",
              "active",
              "allowed-only",
              "1",
            )
          : id === secondID
            ? principal(secondID, "Disabled agent", "disabled", "all", "4")
            : current;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"principal-${value.id}-${value.revision}"` },
        body: JSON.stringify(value),
      });
      return;
    }
    if (request.method() !== "PATCH" || id !== firstID)
      fail("principal member request changed shape");
    updates += 1;
    const patch = JSON.parse(request.postData() ?? "null") as Record<
      string,
      unknown
    >;
    const headers = await request.allHeaders();
    if (updates === 1) {
      if (
        patch.display_name !== "Renamed agent" ||
        Object.keys(patch).join(",") !== "display_name" ||
        headers["if-match"] !== `"principal-${firstID}-1"`
      )
        fail("principal display update changed shape");
      current = principal(
        firstID,
        "Renamed agent",
        "active",
        "requestable",
        "2",
      );
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"principal-${firstID}-2"` },
        body: JSON.stringify(current),
      });
      return;
    }
    if (patch.state !== "disabled" || Object.keys(patch).join(",") !== "state")
      fail("principal authority update changed shape");
    if (!staleReturned) {
      staleReturned = true;
      current = principal(
        firstID,
        "Renamed agent",
        "active",
        "requestable",
        "3",
      );
      await route.fulfill({
        status: 412,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 412,
          code: "stale_principal_revision",
          title: "The principal revision is stale.",
        }),
      });
      return;
    }
    if (headers["if-match"] !== `"principal-${firstID}-3"`)
      fail("principal stale revision was not refreshed");
    current = principal(
      firstID,
      "Renamed agent",
      "disabled",
      "requestable",
      "4",
    );
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${firstID}-4"` },
      body: JSON.stringify(current),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/access/principals";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="principal-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Disabled agent"))
    fail("principal list omitted a principal");
  const principalNames = await page
    .locator('[data-testid="principal-row"] th[scope="row"]')
    .allTextContents();
  if (
    principalNames.map((name) => name.trim()).join("|") !==
      "Build agent|Disabled agent" ||
    (await page
      .locator('[data-testid="principals-view"] thead th')
      .first()
      .getAttribute("aria-sort")) !== "ascending"
  )
    fail(`principals did not default to Name ascending: ${principalNames}`);
  if (staleListRestarted) fail("principal list traversed without navigation");
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await page
    .getByText(
      "The previous page expired or changed. Restarted at the first page.",
      { exact: true },
    )
    .waitFor();
  await waitForCollectionRows(page, "principal", 2);
  const principalSearch = page.getByLabel("Name or ID", { exact: true });
  await principalSearch.fill("Build agnt");
  await waitForCollectionRows(page, "principal", 1);
  if (
    (await page.locator('[data-testid="principal-row"]').count()) !== 1 ||
    !(await page.locator('[data-testid="principal-row"]').innerText()).includes(
      "Build agent",
    )
  )
    fail("principal typo search did not match");
  await principalSearch.fill(secondID.toLowerCase());
  await waitForCollectionRows(page, "principal", 0);
  if ((await page.locator('[data-testid="principal-row"]').count()) !== 0)
    fail("principal ID search was not literal");
  await principalSearch.fill(secondID);
  await waitForCollectionRows(page, "principal", 1);
  if (
    (await page.locator('[data-testid="principal-row"]').count()) !== 1 ||
    !(await page.locator('[data-testid="principal-row"]').innerText()).includes(
      "Disabled agent",
    )
  )
    fail("principal ID search did not match");
  await page.getByRole("button", { name: "Reset" }).click();
  await waitForCollectionRows(page, "principal", 2);
  await exerciseCollectionPagination(page, undefined, async () => {
    await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
    await page.locator('[data-testid="sign-in-submit"]').click();
    await waitForLifecycle(page, "authenticated");
  });
  await waitForCollectionRows(page, "principal", 2);
  for (const phrase of [
    "Permanent agent principals",
    "Compare permanent identity",
    "Visibility does not grant call authority",
  ])
    if (body.includes(phrase)) fail(`principal list retained ${phrase}`);
  const principalHeaders = await page
    .locator('[data-testid="principals-view"] thead th')
    .allTextContents();
  if (
    principalHeaders
      .map((value) => value.replace(/\s?[↑↓↕]$/, ""))
      .join("|") !== "Name|ID|Status|Visibility"
  )
    fail(`principal columns drifted: ${principalHeaders.join("|")}`);
  if (
    (await page
      .getByRole("link", { name: "Principals", exact: true })
      .count()) !== 1 ||
    (await page.getByRole("link", { name: "Grants", exact: true }).count()) !==
      1 ||
    (await page.getByRole("link", { name: "Access", exact: true }).count()) !==
      0
  )
    fail("Principals and Grants were not independent navigation destinations");
  const principalCreate = page.locator('[data-testid="principal-create-link"]');
  if (
    !(await principalCreate.evaluate((element) =>
      element.classList.contains("create-action"),
    )) ||
    Math.abs(
      (await principalCreate.boundingBox())!.x -
        (await page.locator('[data-testid="principals-view"]').boundingBox())!
          .x,
    ) > 1
  )
    fail("Create principal was not aligned with Create server");

  await page.evaluate(() => {
    window.location.hash = "#/access/principals/new";
  });
  await page.locator('[data-testid="principal-create-view"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (
    (await page
      .getByRole("heading", { level: 1, name: "Create principal", exact: true })
      .count()) !== 1 ||
    body.includes("permanent synthetic default ALLOW grant") ||
    !body.includes("Gateway self-service tools") ||
    !body.includes("permanent identity")
  )
    fail("principal creation retained internal default-grant language");
  const principalActionGap = await page.evaluate(() => {
    const field = document.querySelector<HTMLElement>(
      '[data-testid="principal-visibility"]',
    )!;
    const action = document.querySelector<HTMLElement>(
      '[data-testid="principal-editor-submit"]',
    )!;
    return (
      action.getBoundingClientRect().top - field.getBoundingClientRect().bottom
    );
  });
  if (principalActionGap < 15)
    fail(`principal create action gap was ${principalActionGap}px`);
  await page
    .locator('[data-testid="principal-display-name"]')
    .fill("New automation");
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await page.locator('[data-testid="unsaved-changes-cancel"]').waitFor();
  await page.locator('[data-testid="unsaved-changes-cancel"]').click();
  await page
    .locator('dialog[aria-labelledby="unsaved-changes-title"]')
    .waitFor({ state: "hidden" });
  if (
    (await page.evaluate(() => window.location.hash)) !== "#/principals/new" ||
    (await page
      .locator('[data-testid="principal-display-name"]')
      .inputValue()) !== "New automation"
  )
    fail("principal editor did not register its dirty draft");
  await page
    .locator('[data-testid="principal-visibility"]')
    .selectOption("allowed-only");
  if (
    (
      await page
        .locator('[data-testid="principal-editor-submit"]')
        .textContent()
    )?.trim() !== "Review and create"
  )
    fail("principal creation omitted its review action");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page
    .getByRole("heading", { name: "Review principal", exact: true })
    .waitFor();
  if (creates !== 0) fail("principal creation submitted before final review");
  const principalReview =
    (await page
      .locator("#principal-change-confirm-consequence")
      .textContent()) ?? "";
  if (
    !principalReview.includes("New automation") ||
    !principalReview.includes("Allowed tools only") ||
    !principalReview.includes("Default Gateway access")
  )
    fail("principal creation review omitted submitted values");
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page.locator('[data-testid="principal-detail"]').waitFor();

  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, firstID);
  await page.locator('[data-testid="principal-detail"]').waitFor();
  await page
    .getByRole("heading", { level: 1, name: "Build agent", exact: true })
    .waitFor();
  if (
    (await page
      .getByRole("heading", {
        level: 2,
        name: "Principal details",
        exact: true,
      })
      .count()) !== 1 ||
    (await page.locator('[data-testid="detail-context"] h1').count()) !== 1
  )
    fail("principal detail did not use the shared detail hierarchy");
  body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes("permanent synthetic default ALLOW grant") ||
    body.includes("Re-enabling restores neither") ||
    body.includes("Visibility is not call authorization") ||
    body.includes("View grants") ||
    body.includes("View requests") ||
    body.includes("View invocations")
  )
    fail("principal detail retained implementation-oriented copy or shortcuts");

  await page.evaluate((id) => {
    (
      window as Window & { principalDetailFlashed?: boolean }
    ).principalDetailFlashed = false;
    new MutationObserver(() => {
      if (
        window.location.hash === `#/principals/${id}` &&
        document
          .querySelector('[data-testid="principal-detail"]')
          ?.textContent?.includes("Build agent")
      )
        (
          window as Window & { principalDetailFlashed?: boolean }
        ).principalDetailFlashed = true;
    }).observe(document.body, { childList: true, subtree: true });
    window.location.hash = `#/access/principals/${id}`;
  }, secondID);
  await page.getByText("Disabled agent", { exact: true }).waitFor();
  if (
    (
      ((await page.locator("body").textContent()) ?? "").match(/Not issued/g) ??
      []
    ).length !== 1
  )
    fail("principal credential absence was repeated");
  if (
    await page.evaluate(
      () =>
        (window as Window & { principalDetailFlashed?: boolean })
          .principalDetailFlashed === true,
    )
  )
    fail("principal navigation flashed the prior identity");
  const untouchedPrincipalWarned = await page.evaluate(() => {
    const event = new Event("beforeunload", { cancelable: true });
    return !window.dispatchEvent(event);
  });
  if (untouchedPrincipalWarned)
    fail("principal route change retained the prior principal dirty baseline");
  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, firstID);
  await page.getByText("Build agent", { exact: true }).waitFor();

  await page
    .locator('[data-testid="principal-display-name"]')
    .fill("Renamed agent");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page
    .getByRole("heading", { name: "Renamed agent", exact: true })
    .waitFor();
  const principalState = page.getByRole("switch", {
    name: "Principal enabled",
  });
  if (!(await principalState.isChecked()))
    fail("active principal switch was not checked");
  await principalState.click();
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page
    .getByText("The principal revision is stale.", { exact: true })
    .waitFor();
  if ((await principalState.isChecked()) !== false)
    fail("principal stale refresh discarded safe draft");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page
    .locator(".status-label.warning", { hasText: "Disabled" })
    .waitFor();
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "principals_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), detail_reads: detailReads, creates, updates })}\n`,
  );
}

export async function runPrincipalCredentials(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const credentialID = "01ARZ3NDEKTSV4RRFFQ69G5FAZ";
  const issuedBearer = `mgw_agent_${"I".repeat(43)}`;
  const lostBearer = `mgw_agent_${"L".repeat(43)}`;
  const principal = (revision: string, credential: boolean) => ({
    id: principalID,
    display_name: "Credential agent",
    state: "active",
    visibility: "requestable",
    revision,
    credential_revision: credential ? revision : String(Number(revision) + 1),
    credential: credential
      ? {
          id: credentialID,
          fingerprint: "0123456789abcdef",
          revision,
          created_at: "2026-08-28T12:00:00Z",
        }
      : null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
  });
  let current = principal("1", true);
  let issues = 0;
  let revokes = 0;
  let releaseLost: (() => void) | undefined;
  let markLostStarted: (() => void) | undefined;
  const lostStarted = new Promise<void>((resolve) => {
    markLostStarted = resolve;
  });

  await page.route("**/api/v1/principals/*", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${principalID}-${current.revision}"` },
      body: JSON.stringify(current),
    });
  });
  await page.route(
    `${baseURL}/api/v1/principals/${principalID}/credential`,
    async (route) => {
      const request = route.request();
      const headers = await request.allHeaders();
      if (
        request.postData() !== "{}" ||
        headers["if-match"] !== `"principal-${principalID}-${current.revision}"`
      )
        fail("principal credential request changed shape");
      if (request.method() === "POST") {
        issues += 1;
        if (issues === 1) {
          current = principal("2", true);
          await route.fulfill({
            status: 412,
            contentType: "application/problem+json",
            body: JSON.stringify({
              status: 412,
              code: "stale_principal_revision",
              title: "The principal revision is stale.",
            }),
          });
          return;
        }
        if (issues === 3) {
          markLostStarted?.();
          await new Promise<void>((resolve) => {
            releaseLost = resolve;
          });
          current = principal("4", true);
        } else if (issues === 4 || issues === 5) {
          await route.fulfill({
            status: 201,
            contentType: "application/json",
            body: "{",
          });
          return;
        } else {
          current = principal("3", true);
        }
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          headers: {
            ETag: `"principal-${principalID}-${current.revision}"`,
          },
          body: JSON.stringify({
            principal: current,
            bearer: issues === 2 ? issuedBearer : lostBearer,
          }),
        });
        return;
      }
      if (request.method() !== "DELETE")
        fail("principal credential method changed shape");
      revokes += 1;
      if (revokes === 1) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: "{",
        });
        return;
      }
      current = principal("5", false);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"principal-${principalID}-5"` },
        body: JSON.stringify(current),
      });
    },
  );

  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="principal-credential-actions"]').waitFor();
  let body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("Only one agent credential may be active at a time.") ||
    !body.includes(credentialID) ||
    body.includes("Credential authority") ||
    body.includes("interrupted immediately") ||
    (await page
      .getByRole("heading", { name: "Agent credential", exact: true })
      .count()) !== 1 ||
    (await page
      .getByRole("button", { name: "Rotate credential", exact: true })
      .count()) !== 1
  )
    fail("principal credential guidance or terminology was unclear");
  if (
    !(await page
      .locator('[data-testid="principal-credential-issue"]')
      .evaluate((element) => element.classList.contains("danger-action")))
  )
    fail("credential rotation was not styled as dangerous");

  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator(
      'dialog[aria-labelledby="principal-credential-confirm-title"][open]',
    )
    .waitFor();
  const openCredentialDialogs = await page
    .locator("dialog[open]")
    .evaluateAll((dialogs) =>
      dialogs.map((dialog) => dialog.id || dialog.className),
    );
  if (openCredentialDialogs.length !== 1)
    fail(
      `credential review opened overlapping dialogs: ${openCredentialDialogs.join(",")}`,
    );
  if (
    await page
      .locator('dialog[aria-labelledby="one-time-display-title"]')
      .evaluate((dialog) => dialog.hasAttribute("open"))
  )
    fail("one-time bearer dialog opened before credential confirmation");
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page
    .getByText("The principal revision is stale.", { exact: true })
    .waitFor();
  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page.locator('[data-testid="one-time-value"]').waitFor();
  if ((await page.locator("dialog[open]").count()) !== 1)
    fail("credential result retained overlapping dialogs");
  if (
    (await page.locator('[data-testid="one-time-value"]').textContent()) !==
    issuedBearer
  )
    fail("issued bearer did not reach the prepared one-time display");
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  if ((await page.locator('[data-testid="one-time-value"]').count()) !== 0)
    fail("dismissed bearer remained in the DOM");

  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await lostStarted;
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  releaseLost?.();
  await page
    .getByText(
      "The replacement may now be current and the prior bearer may already be invalid. Review principal metadata, then explicitly rotate or revoke the lost current credential. Do not replay the operation.",
      { exact: true },
    )
    .waitFor();
  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
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
      "Do not replay. The replacement may be current and the prior bearer may already be invalid. Refresh the principal, then explicitly rotate or revoke the observed current credential.",
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

  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await page.locator('[data-testid="principal-credential-revoke"]').waitFor();
  await page.locator('[data-testid="principal-credential-revoke"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page
    .getByText(
      "Do not replay revoke. Authority may already be revoked. Refresh the principal before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.evaluate(() => {
    window.location.hash = "#/overview";
  });
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await page.locator('[data-testid="principal-credential-revoke"]').waitFor();
  await page.locator('[data-testid="principal-credential-revoke"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page.waitForFunction(
    () => document.body.textContent?.includes("Not issued") === true,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("Prior authority no longer authenticates") ||
    (await page
      .getByRole("button", { name: "Issue credential", exact: true })
      .count()) !== 1
  )
    fail("principal credential revoke omitted authority result");
  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
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
      "Do not replay issue. A current credential may occupy the slot while its bearer is permanently lost. Refresh the principal, then explicitly rotate or revoke the observed credential.",
      { exact: true },
    )
    .waitFor();
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, issuedBearer, lostBearer],
    true,
  );
  process.stdout.write(
    `${JSON.stringify({ event: "principal_credentials_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), issues, revokes })}\n`,
  );
}

export async function assertMatcherAuthoringAccessibility(
  page: Page,
  workflow: string,
): Promise<void> {
  if (
    (await page.getByText("Operator help", { exact: true }).count()) !== 0 ||
    (await page.locator(".matcher-editor details").count()) !== 0 ||
    (await page
      .getByText(/EQUALS compares the selected scalar type exactly/)
      .count()) !== 0
  )
    fail(`${workflow} retained the removed operator help disclosure`);
  const missingDescriptions = await page
    .locator(".matcher-editor [aria-describedby]")
    .evaluateAll((controls) =>
      controls.flatMap((control) =>
        (control.getAttribute("aria-describedby") ?? "")
          .split(/\s+/)
          .filter((id) => id && document.getElementById(id) === null),
      ),
    );
  if (missingDescriptions.length !== 0)
    fail(
      `${workflow} has missing descriptions: ${missingDescriptions.join(",")}`,
    );
  const axe = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  const blocking = axe.violations.filter(
    (violation) =>
      violation.impact === "serious" || violation.impact === "critical",
  );
  if (blocking.length !== 0)
    fail(
      `${workflow} matcher accessibility findings: ${blocking
        .map((violation) => violation.id)
        .join(",")}`,
    );
  for (const viewport of [
    { width: 320, height: 800 },
    { width: 720, height: 450 },
  ]) {
    await page.setViewportSize(viewport);
    const layout = await page.evaluate(() => ({
      client: document.documentElement.clientWidth,
      scroll: document.documentElement.scrollWidth,
      unlabeled: [
        ...document.querySelectorAll<
          HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement
        >(
          'input[data-testid*="constraint"]:not([type="hidden"]), select[data-testid*="constraint"], textarea[data-testid*="constraint"]',
        ),
      ]
        .filter(
          (control) =>
            control.getAttribute("aria-label") === null &&
            (control.id === "" ||
              document.querySelector(
                `label[for="${CSS.escape(control.id)}"]`,
              ) === null),
        )
        .map((control) => `${control.tagName}:${control.id}`),
    }));
    if (layout.scroll > layout.client || layout.unlabeled.length !== 0)
      fail(
        `${workflow} matcher reflow/label failure: ${JSON.stringify(layout)}`,
      );
  }
  await page.setViewportSize({ width: 1440, height: 900 });
}

export async function runGrantReadsCreate(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const firstGrantID = "01ARZ3NDEKTSV4RRFFQ69G5FB0";
  const secondGrantID = "01ARZ3NDEKTSV4RRFFQ69G5FB1";
  const createdIDs = [
    "01ARZ3NDEKTSV4RRFFQ69G5FB2",
    "01ARZ3NDEKTSV4RRFFQ69G5FB3",
  ];
  const grant = (
    id: string,
    effect: "allow" | "deny",
    state: "active" | "expired",
    upstreamName: string | null,
    constraint: unknown | null,
    expiresAt: string | null,
    readOnly = false,
  ) => ({
    ...(readOnly ? { read_only: true } : {}),
    id,
    description: (id === firstGrantID
      ? "Reporting access"
      : "Restricted access") as string | null,
    revision: "1",
    principal_id: principalID,
    effect,
    server_id: serverID,
    upstream_name: upstreamName,
    constraint,
    expires_at: expiresAt,
    state,
    created_at: "2026-08-28T12:00:00Z",
  });
  const active = grant(firstGrantID, "allow", "active", null, null, null, true);
  const expired = grant(
    secondGrantID,
    "deny",
    "expired",
    "dangerous.tool",
    {
      version: 2,
      equals: { "/mode": "blocked", "/enabled": true },
      regex: { "/mode": "^blocked$", "/path": "^/safe/" },
    },
    "2026-08-28T12:30:00Z",
  );
  let staleRestarted = false;
  let attempts = 0;
  let creates = 0;
  let descriptionPatches = 0;
  let descriptorRequests = 0;
  const expectedExpiry = await page.evaluate(() =>
    new Date("2030-01-01T12:34:56").toISOString(),
  );

  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: principalID,
            display_name: "Automation agent",
            state: "active",
            visibility: "allowed-only",
            revision: "1",
            credential_revision: "0",
            credential: null,
            created_at: "2026-08-28T12:00:00Z",
            updated_at: "2026-08-28T12:00:00Z",
          },
        ],
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v1/servers?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          serverReadFixture(serverID, {
            name: "Reporting server",
            desired: "enabled",
            runtime: "active",
            credential: "ready",
            durable: "current",
            active: "current",
          }),
        ],
        next_cursor: null,
      }),
    });
  });
  await page.route(
    `**/api/v1/servers/${serverID}/descriptors?*`,
    async (route) => {
      const query = new URL(route.request().url()).searchParams;
      descriptorRequests += 1;
      if (descriptorRequests > 2) {
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 503,
            code: "storage_unavailable",
            title: "Catalog tools are unavailable.",
          }),
        });
        return;
      }
      if (
        route.request().method() !== "GET" ||
        query.get("limit") !== "100" ||
        query.get("retired") !== "exclude" ||
        query.get("representation") !== "summary" ||
        [...query.keys()].some(
          (key) =>
            key !== "limit" &&
            key !== "retired" &&
            key !== "representation" &&
            key !== "cursor",
        )
      )
        fail("grant descriptor traversal changed shape");
      const descriptor = (id: string, upstream: string, external: string) => ({
        id,
        server_id: serverID,
        upstream_name: upstream,
        external_name: external,
        descriptor: {
          name: upstream,
          inputSchema: {
            type: "object",
            additionalProperties: { type: "string" },
            properties: {
              ["x".repeat(300)]: { type: "string" },
              region: {
                type: "string",
                enum: ["us", "eu"],
                description: "Deployment region",
              },
              filters: {
                type: "object",
                properties: {
                  "item/name": {
                    type: "string",
                    description: "Exact item name",
                  },
                  count: { type: "integer" },
                },
              },
              conditional: {
                type: "string",
                oneOf: [{ const: "one" }, { const: "two" }],
              },
              ...Object.fromEntries(
                Array.from({ length: 300 }, (_, index) => [
                  `wide-${index}`,
                  { type: "string" },
                ]),
              ),
            },
          },
          annotations: {
            title: null,
            readOnlyHint: false,
            destructiveHint: false,
            idempotentHint: false,
            openWorldHint: false,
          },
        },
        fingerprint: `fingerprint-${upstream}`,
        catalog_revision: "1",
        first_seen_at: "2026-08-28T12:00:00Z",
        last_seen_at: "2026-08-28T12:00:00Z",
        retired_at: null,
      });
      const summary = (item: ReturnType<typeof descriptor>) => ({
        id: item.id,
        server_id: item.server_id,
        upstream_name: item.upstream_name,
        external_name: item.external_name,
        catalog_revision: item.catalog_revision,
      });
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          query.get("cursor") === "descriptor-next"
            ? {
                items: [
                  summary(
                    descriptor(
                      "01ARZ3NDEKTSV4RRFFQ69G5FC1",
                      "literal.tool",
                      "Literal tool",
                    ),
                  ),
                ],
                next_cursor: null,
              }
            : {
                items: [
                  summary(
                    descriptor(
                      "01ARZ3NDEKTSV4RRFFQ69G5FC0",
                      "other.tool",
                      "Other tool",
                    ),
                  ),
                ],
                next_cursor: "descriptor-next",
              },
        ),
      });
    },
  );
  await page.route(
    `**/api/v1/servers/${serverID}/descriptors/01ARZ3NDEKTSV4RRFFQ69G5FC1`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "01ARZ3NDEKTSV4RRFFQ69G5FC1",
          server_id: serverID,
          upstream_name: "literal.tool",
          external_name: "Literal tool",
          descriptor: {
            name: "literal.tool",
            inputSchema: {
              type: "object",
              additionalProperties: { type: "string" },
              properties: {
                ["x".repeat(300)]: { type: "string" },
                region: {
                  type: "string",
                  enum: ["us", "eu"],
                  description: "Deployment region",
                },
                filters: {
                  type: "object",
                  properties: {
                    "item/name": {
                      type: "string",
                      description: "Exact item name",
                    },
                    count: { type: "integer" },
                  },
                },
                ...Object.fromEntries(
                  Array.from({ length: 300 }, (_, index) => [
                    `wide-${index}`,
                    { type: "string" },
                  ]),
                ),
              },
            },
            annotations: {
              title: null,
              readOnlyHint: false,
              destructiveHint: false,
              idempotentHint: false,
              openWorldHint: false,
            },
          },
          fingerprint: "fingerprint-literal.tool",
          catalog_revision: "1",
          first_seen_at: "2026-08-28T12:00:00Z",
          last_seen_at: "2026-08-28T12:00:00Z",
          retired_at: null,
        }),
      });
    },
  );

  await page.route("**/api/v1/grants?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      [...query.keys()].some(
        (key) =>
          ![
            "limit",
            "cursor",
            "representation",
            "sort",
            "direction",
            "identity",
            "principal",
            "target",
            "effect",
            "state",
          ].includes(key),
      )
    )
      fail("grant list filters changed shape");
    const cursor = query.get("cursor");
    if (cursor === "grant-stale") {
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
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: (query.get("identity") === "Reportng access"
          ? [active]
          : [active, expired]
        ).map((grant) =>
          query.get("representation") === "table"
            ? {
                grant,
                principal_display_name: "Automation agent",
                server_display_name: "Reporting server",
              }
            : grant,
        ),
        next_cursor:
          staleRestarted || query.get("identity") !== null
            ? null
            : "grant-stale",
        ...(query.get("representation") === "table"
          ? {
              total_count:
                query.get("identity") === "Reportng access"
                  ? 1
                  : staleRestarted
                    ? 2
                    : 3,
              offset: 0,
            }
          : {}),
      }),
    });
  });
  await page.route("**/api/v1/grants/*", async (route) => {
    const request = route.request();
    const id = new URL(request.url()).pathname.split("/").pop();
    if (request.method() === "PATCH") {
      const headers = await request.allHeaders();
      if (
        id !== firstGrantID ||
        headers["if-match"] !== `"grant-${firstGrantID}-${active.revision}"`
      )
        fail(
          `grant description PATCH precondition changed: ${JSON.stringify({ id, actual: headers["if-match"], revision: active.revision })}`,
        );
      const patch = JSON.parse(request.postData() ?? "") as Record<
        string,
        unknown
      >;
      if (
        Object.keys(patch).join(",") !== "description" ||
        (patch.description !== "Updated reporting access" &&
          patch.description !== null)
      )
        fail("grant description PATCH body changed");
      descriptionPatches += 1;
      active.description = patch.description as string | null;
      active.revision = String(Number(active.revision) + 1);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"grant-${firstGrantID}-${active.revision}"` },
        body: JSON.stringify(active),
      });
      return;
    }
    if (request.method() !== "GET") {
      await route.fallback();
      return;
    }
    const item =
      id === secondGrantID
        ? expired
        : id === createdIDs[0]
          ? grant(id!, "allow", "active", null, null, null, true)
          : id === createdIDs[1]
            ? grant(
                id!,
                "deny",
                "active",
                "literal.tool",
                null,
                "2030-01-01T00:00:00Z",
              )
            : active;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(item),
    });
  });
  await page.route(`${baseURL}/api/v1/grants`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    attempts += 1;
    const raw = route.request().postData() ?? "";
    const body = JSON.parse(raw) as Record<string, unknown>;
    if (
      Object.keys(body).join(",") !==
        `description,principal_id,effect,server_id,upstream_name,constraint,expires_at${attempts <= 2 ? ",read_only" : ""}` ||
      body.description !== "New access" ||
      body.principal_id !== principalID ||
      body.server_id !== serverID
    )
      fail("grant create body changed closed shape");
    if (attempts === 1) {
      await route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 400,
          code: "invalid_grant",
          title: "The grant is invalid.",
        }),
      });
      return;
    }
    creates += 1;
    if (creates === 1) {
      if (
        body.read_only !== true ||
        body.effect !== "allow" ||
        body.upstream_name !== null ||
        body.constraint !== null ||
        body.expires_at !== null
      )
        fail("server-wide permanent grant changed shape");
    } else if (
      body.effect !== "deny" ||
      body.upstream_name !== "literal.tool" ||
      body.expires_at !== expectedExpiry ||
      !raw.includes('"version":2') ||
      !raw.includes('"/a~1b/0":1.0') ||
      !raw.includes('"/empty/":null') ||
      !raw.includes('"/flag":true') ||
      !raw.includes('"/name":"literal"') ||
      !raw.includes('"/resource":"item-\\\\d+"')
    )
      fail(`exact-tool lexical constraint changed shape: ${raw}`);
    const item = grant(
      createdIDs[creates - 1]!,
      body.effect as "allow" | "deny",
      "active",
      body.upstream_name as string | null,
      body.constraint,
      body.expires_at as string | null,
      body.read_only === true,
    );
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify(item),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/access/grants";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="grant-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  if (staleRestarted) fail("grant list traversed without navigation");
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await page
    .getByText(
      "The previous page expired or changed. Restarted at the first page.",
      { exact: true },
    )
    .waitFor();
  await waitForCollectionRows(page, "grant", 2);
  const grantSearch = page.getByLabel("Description or ID", { exact: true });
  await grantSearch.fill("Reportng access");
  await waitForCollectionRows(page, "grant", 1);
  if (
    (await page.locator('[data-testid="grant-row"]').count()) !== 1 ||
    !(await page.locator('[data-testid="grant-row"]').innerText()).includes(
      "Reporting access",
    )
  )
    fail("grant typo search did not match");
  await page.getByRole("button", { name: "Reset" }).click();
  await waitForCollectionRows(page, "grant", 2);
  for (const phrase of [
    "Reporting access",
    "Restricted access",
    "Automation agent",
    "Active",
    "Expired",
    "Create grant",
  ])
    if (!body.includes(phrase)) fail(`grant list omitted ${phrase}`);
  const headers = await page.locator("thead th").allTextContents();
  if (
    headers[0] !== "ID↕" ||
    headers[1] !== "Description↕" ||
    headers.includes("Action")
  )
    fail(`grant table identity columns changed: ${headers.join("|")}`);
  expect(headers.map((header) => header.replace(/[↕↑↓]/g, ""))).toEqual([
    "ID",
    "Description",
    "Principal",
    "Target",
    "Effect",
    "Status",
    "Expiry",
    "Constraints",
  ]);
  const firstGrantRow = page.locator('[data-testid="grant-row"]').first();
  await expect(firstGrantRow.locator('[data-label="Expiry"]')).toHaveText(
    "No expiry",
  );
  await expect(firstGrantRow.locator('[data-label="Constraints"]')).toHaveText(
    "0",
  );
  const expiredRow = page.getByTestId("grant-row").nth(1);
  await expect(
    expiredRow.locator('[data-label="Expiry"] time'),
  ).toHaveAttribute("datetime", expired.expires_at!);
  await expect(expiredRow.locator('[data-label="Constraints"]')).toHaveText(
    "4",
  );
  await captureRequestState(page, "grant-table");
  if (
    (await firstGrantRow
      .locator(`a[href="#/grants/${firstGrantID}"]`)
      .count()) !== 1 ||
    (await firstGrantRow.locator("td").first().locator("a").count()) !== 0
  )
    fail("grant description remained navigational metadata");
  if (
    body.includes("Open grant") ||
    body.includes("Synthetic default namespace")
  )
    fail("grant table retained redundant actions or internal default language");
  if (body.includes("Immutable grants") || body.includes("Expired records"))
    fail("dedicated Grants page retained subordinate introductory copy");
  const grantCreate = page.locator('[data-testid="grant-create-link"]');
  if (
    (await grantCreate.getAttribute("href")) !== "#/grants/new" ||
    !(await grantCreate.evaluate((element) =>
      element.classList.contains("create-action"),
    )) ||
    Math.abs(
      (await grantCreate.boundingBox())!.x -
        (await page.locator('[data-testid="grants-view"]').boundingBox())!.x,
    ) > 1
  )
    fail("Create grant was not aligned with other create actions");
  await page.evaluate((id) => {
    window.location.hash = `#/access/grants/${id}`;
  }, firstGrantID);
  await page.locator('[data-testid="grant-detail"]').waitFor();
  const grantDetail = page.locator('[data-testid="grant-detail"]');
  const grantFactLabels = await grantDetail.locator("dt").allTextContents();
  if (
    !grantFactLabels.includes("Grant ID") ||
    grantFactLabels.includes("ID") ||
    (await grantDetail.getByText("Back to principal grants").count()) !== 0 ||
    (await grantDetail.locator('[data-testid="detail-context"] h1').count()) !==
      1 ||
    (await grantDetail
      .getByRole("heading", { level: 2, name: "Grant details", exact: true })
      .count()) !== 1 ||
    (await grantDetail.locator("section.panel h1").count()) !== 0 ||
    (await grantDetail.locator(".panel-value").count()) !== 0
  )
    fail("grant detail did not use the shared detail hierarchy");
  const descriptionEditor = page.locator("#grant-description-edit");
  const saveDescription = page.getByRole("button", {
    name: "Save description",
  });
  const saveDescriptionGap = await saveDescription.evaluate((button) => {
    const previous = button.previousElementSibling;
    if (previous === null) return -1;
    return (
      button.getBoundingClientRect().top -
      previous.getBoundingClientRect().bottom
    );
  });
  if (saveDescriptionGap < 16)
    fail(`grant description submit spacing collapsed: ${saveDescriptionGap}px`);
  await descriptionEditor.fill("Updated reporting access");
  await saveDescription.click();
  await page.waitForTimeout(100);
  await descriptionEditor.fill("");
  await saveDescription.click();
  await page.waitForTimeout(100);
  if (descriptionPatches !== 2)
    fail("grant description save and clear did not both reach the API");

  await page.evaluate((id) => {
    window.location.hash = `#/access/grants/${id}`;
  }, secondGrantID);
  await page
    .locator('[data-testid="grant-detail"]')
    .waitFor({ timeout: 5000 })
    .catch(async () =>
      fail(
        `grant detail did not load: ${JSON.stringify({ hash: await page.evaluate(() => window.location.hash), body: await page.locator("body").innerText() })}`,
      ),
    );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Exact tool dangerous.tool") || !body.includes("Expired"))
    fail("grant detail omitted scope or retained expiry state");

  await page.evaluate(
    ({ principal, server }) => {
      window.location.hash = `#/access/grants/new?principal_id=${principal}&server_id=${server}`;
    },
    { principal: principalID, server: serverID },
  );
  await page.locator('[data-testid="grant-create-view"]').waitFor();
  const grantCreateBody =
    (await page.locator('[data-testid="grant-create-view"]').textContent()) ??
    "";
  if (
    (await page
      .getByRole("heading", { level: 1, name: "Create grant", exact: true })
      .count()) !== 1 ||
    !grantCreateBody.includes("cannot be edited after creation") ||
    (
      await page.locator('[data-testid="grant-create-submit"]').textContent()
    )?.trim() !== "Review and create"
  )
    fail("grant creation omitted page or immutability guidance");
  const grantSubmitGap = await page
    .locator('[data-testid="grant-create-submit"]')
    .evaluate((button) => {
      const previous = button.previousElementSibling;
      if (previous === null) return -1;
      return (
        button.getBoundingClientRect().top -
        previous.getBoundingClientRect().bottom
      );
    });
  if (grantSubmitGap < 16)
    fail(`grant submit spacing collapsed: ${grantSubmitGap}px`);
  if (
    (await page
      .locator('[data-testid="grant-principal"]')
      .evaluate((node) => node.tagName)) !== "SELECT" ||
    (await page
      .locator('[data-testid="grant-server"]')
      .evaluate((node) => node.tagName)) !== "SELECT" ||
    !(
      await page.locator('[data-testid="grant-principal"]').innerText()
    ).includes("Automation agent") ||
    !(await page.locator('[data-testid="grant-server"]').innerText()).includes(
      "Reporting server",
    )
  )
    fail("grant creation did not use named principal and server selectors");
  await page
    .locator('[data-testid="grant-description"]')
    .fill("Unsafe\u0085access");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .getByText("Description cannot contain control characters.", {
      exact: true,
    })
    .waitFor();
  if (Number(attempts) !== 0)
    fail("control-character grant description reached the API");
  await page.locator('[data-testid="grant-description"]').fill("New access");
  const allowedTools = page.getByRole("combobox", { name: "Allowed tools" });
  await expect(allowedTools).toHaveValue("all");
  await allowedTools.selectOption("read-only");
  await allowedTools.selectOption("all");
  await expect(
    page.getByText("Includes current and future tools marked", {
      exact: false,
    }),
  ).toHaveCount(0);
  await allowedTools.selectOption("read-only");
  await page.getByTestId("grant-effect").selectOption("deny");
  await expect(allowedTools).toHaveCount(0);
  await page.getByTestId("grant-effect").selectOption("allow");
  await expect(allowedTools).toHaveValue("all");
  await allowedTools.selectOption("read-only");
  await captureRequestState(page, "read-only-grant-create");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .getByRole("heading", { name: "Review grant", exact: true })
    .waitFor();
  if (Number(attempts) !== 0)
    fail("grant creation submitted before final review");
  const grantReview =
    (await page.locator("#grant-create-confirm-consequence").textContent()) ??
    "";
  if (
    !grantReview.includes("New access") ||
    !grantReview.includes("Reporting server") ||
    !grantReview.includes(principalID) ||
    !grantReview.includes(serverID) ||
    !grantReview.includes("Every matcher atom is required (AND)") ||
    !grantReview.includes("matching DENY takes precedence") ||
    !grantReview.includes(
      "Unconstrained access matches every argument object",
    ) ||
    !(
      await page.locator('[data-testid="grant-review-policy"]').inputValue()
    ).includes('"constraint":null')
  )
    fail("grant creation review omitted submitted values");
  await expect(page.getByTestId("grant-review-policy")).toHaveValue(
    /"read_only":true/,
  );
  await expect(page.getByRole("dialog")).toContainText("readOnlyHint=true");
  await captureRequestState(page, "read-only-grant-confirmation");
  await page.locator('[data-testid="grant-create-confirm-submit"]').click();
  await page.getByText("The grant is invalid.", { exact: true }).waitFor();
  if (attempts !== 1) fail("rejected grant creation was replayed");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page.locator('[data-testid="grant-create-confirm-submit"]').click();
  await page.locator('[data-testid="grant-detail"]').waitFor();
  await expect(page.getByTestId("grant-detail")).toContainText(
    "Entire server — read-only tools",
  );
  await captureRequestState(page, "read-only-grant-detail");

  await page.evaluate(
    ({ principal, server }) => {
      window.location.hash = `#/access/grants/new?principal_id=${principal}&server_id=${server}`;
    },
    { principal: principalID, server: serverID },
  );
  await page.locator('[data-testid="grant-create-view"]').waitFor();
  await page.locator('[data-testid="grant-description"]').fill("New access");
  await page.locator('[data-testid="grant-effect"]').selectOption("deny");
  await page.locator('[data-testid="grant-server"]').selectOption("");
  await page.locator('[data-testid="grant-scope"]').selectOption("tool");
  await page
    .getByText("Select a server", { exact: true })
    .waitFor({ timeout: 3000 });
  await page.locator('[data-testid="add-constraint-atom"]').click();
  if (
    (await page.locator('[data-testid="constraint-status"]').innerText()) !==
      "Choose field" ||
    (await page.locator('[data-testid="constraint-pointer"]').inputValue()) !==
      "" ||
    descriptorRequests !== 0
  )
    fail("empty path was not neutral before server selection");
  await page.locator('[data-testid="constraint-pointer"]').fill("/");
  if (
    (await page.locator('[data-testid="constraint-status"]').innerText()) !==
    "Unavailable"
  )
    fail("missing server reported a catalog/schema load that never started");
  await page
    .getByRole("button", { name: "Remove constraint 1", exact: true })
    .click();
  await page.locator('[data-testid="grant-server"]').selectOption(serverID);
  const toolInput = page.getByRole("combobox", {
    name: "Tool name",
    exact: true,
  });
  const toolBadge = page.locator('[data-testid="grant-tool-recognition"]');
  await page.getByText("Choose a tool", { exact: true }).waitFor();
  await toolInput.focus();
  await page
    .getByRole("option", { name: "literal.tool", exact: true })
    .waitFor();
  const toolOptions = await page.getByRole("listbox").innerText();
  if (toolOptions.includes(`server-${serverID.slice(-2).toLowerCase()}.`))
    fail("tool suggestions duplicated the displayed namespace");
  await toolInput.fill("future.tool");
  await toolInput.press("Escape");
  if (
    (await toolInput.inputValue()) !== "future.tool" ||
    (await toolBadge.innerText()) !== "Unknown"
  )
    fail("manual tool name was restricted or not recognized as unknown");
  const unknownBadge = await toolBadge.evaluate((node) => ({
    color: getComputedStyle(node).color,
    width: node.getBoundingClientRect().width,
  }));
  await toolInput.fill("literal");
  await toolInput.press("ArrowDown");
  await toolInput.press("Enter");
  if (
    (await toolInput.inputValue()) !== "literal.tool" ||
    (await toolInput.getAttribute("aria-expanded")) !== "false"
  )
    fail(
      "tool keyboard selection did not commit the literal name and close suggestions",
    );
  const knownBadge = await toolBadge.evaluate((node) => ({
    color: getComputedStyle(node).color,
    width: node.getBoundingClientRect().width,
  }));
  if (
    (await toolBadge.innerText()) !== "Known" ||
    unknownBadge.color === knownBadge.color ||
    unknownBadge.width !== knownBadge.width
  )
    fail("recognition badges lacked distinct colours or shifted layout");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  const emptyPointer = page.locator('[data-testid="constraint-pointer"]');
  if (
    (await emptyPointer.inputValue()) !== "" ||
    (await page.locator('[data-testid="constraint-status"]').innerText()) !==
      "Choose field"
  )
    fail("new constraint used a real pointer or reported unknown before input");
  await emptyPointer.focus();
  await page
    .getByRole("option")
    .filter({ hasText: "/filters/item~1name" })
    .waitFor();
  const suggestedPointers = await page
    .getByRole("listbox")
    .getByRole("option")
    .evaluateAll((options) =>
      options.map((option) => option.getAttribute("data-value")!),
    );
  if (
    suggestedPointers.length !== 256 ||
    suggestedPointers.some(
      (pointer) => new TextEncoder().encode(pointer).length > 256,
    )
  )
    fail("schema suggestions exceeded field or pointer bounds");
  if (
    (await page
      .locator(
        '#constraint-pointer-0-options [data-value="/filters/item~1name"]',
      )
      .count()) !== 1
  )
    fail("nested schema field was not suggested as an RFC 6901 pointer");
  if (
    !(
      await page
        .locator('#constraint-pointer-0-options [data-value="/region"]')
        .innerText()
    ).includes("string · us, eu · Deployment region")
  )
    fail("suggestions omitted scalar type, enum or description");
  await assertMatcherAuthoringAccessibility(page, "open pointer suggestions");
  const keyboardPointer = page.locator('[data-testid="constraint-pointer"]');
  await keyboardPointer.press("ArrowUp");
  const activeOption = await keyboardPointer.getAttribute(
    "aria-activedescendant",
  );
  if (
    !activeOption ||
    !(await page.locator(`#${activeOption}`).evaluate((node) => {
      const bounds = node.getBoundingClientRect();
      const panel = node.closest(".suggestion-panel")!.getBoundingClientRect();
      return bounds.top >= panel.top && bounds.bottom <= panel.bottom;
    }))
  )
    fail("keyboard navigation did not scroll the active suggestion into view");
  await keyboardPointer.press("Escape");
  if (
    (await keyboardPointer.inputValue()) !== "" ||
    (await keyboardPointer.getAttribute("aria-expanded")) !== "false"
  )
    fail("Escape committed an active suggestion or left the popup open");
  await keyboardPointer.fill("/filters/it");
  await keyboardPointer.press("ArrowDown");
  await keyboardPointer.press("Tab");
  if (
    (await keyboardPointer.inputValue()) !== "/filters/it" ||
    (await keyboardPointer.getAttribute("aria-expanded")) !== "false" ||
    !(await page
      .locator('[data-testid="constraint-operator"]')
      .evaluate((node) => document.activeElement === node))
  )
    fail("Tab implicitly selected a suggestion or retained the popup");
  await keyboardPointer.focus();
  await keyboardPointer.press("ArrowDown");
  await keyboardPointer.press("Enter");
  if ((await keyboardPointer.inputValue()) !== "/filters/item~1name")
    fail(
      "pointer autocomplete did not select the suggested path with ArrowDown/Enter",
    );
  if (
    (await page.locator('[data-testid="constraint-type"]').inputValue()) !==
      "string" ||
    (await page
      .locator('[data-testid="constraint-pointer"]')
      .getAttribute("aria-describedby")) !== null ||
    (await page.locator(".matcher-guidance").count()) !== 0
  )
    fail("schema suggestion did not inform matcher type, enum, and metadata");
  await page
    .getByText(
      "Some schema fields cannot be suggested. Custom pointers are still available.",
      { exact: true },
    )
    .waitFor();
  const suggestionToggle = page.getByRole("button", {
    name: "Show suggestions for JSON pointer 1",
    exact: true,
  });
  await suggestionToggle.click();
  await page.getByRole("listbox").waitFor();
  await suggestionToggle.click();
  if ((await page.getByRole("listbox").count()) !== 0)
    fail("dropdown toggle did not close suggestions");
  const pointer = page.locator('[data-testid="constraint-pointer"]');
  const scalarType = page.locator('[data-testid="constraint-type"]');
  const scalarValue = page.locator('[data-testid="constraint-value"]');
  const operator = page.locator('[data-testid="constraint-operator"]');
  if (
    (await page.locator('[data-testid="grant-namespace"]').innerText()) !==
    `server-${serverID.slice(-2).toLowerCase()} .`
  )
    fail("literal tool editor omitted namespace separator");
  await pointer.focus();
  await pointer.press("ControlOrMeta+A");
  await pointer.pressSequentially("/filters/co");
  await pointer.press("ArrowDown");
  await pointer.press("Enter");
  if ((await pointer.inputValue()) !== "/filters/count")
    fail("pointer keyboard selection did not apply the suggested number path");
  await pointer.press("Tab");
  if ((await scalarType.inputValue()) !== "number")
    fail("known integer path did not default to Number");
  await pointer.focus();
  await page.getByRole("listbox").waitFor();
  await scalarValue.fill("1.00e+2");
  if ((await page.getByRole("listbox").count()) !== 0)
    fail("moving focus outside a combobox left stale suggestions open");
  await operator.selectOption("regex");
  if (
    (await scalarType.inputValue()) !== "string" ||
    !(await scalarType.isDisabled()) ||
    (await scalarValue.inputValue()) !== ""
  )
    fail("MATCHES did not lock String and clear the equality token");
  const regexWarning = page.getByText(
    /Schema suggests number; only string runtime values can match/,
  );
  await regexWarning.waitFor();
  const warningID = await regexWarning.getAttribute("id");
  if (!warningID) fail("contextual regex warning has no description ID");
  for (const control of [pointer, operator, scalarType, scalarValue]) {
    const descriptions = (
      await control.getAttribute("aria-describedby")
    )?.split(/\s+/);
    if (!descriptions?.includes(warningID))
      fail("contextual regex warning is not associated with its control");
  }
  await scalarValue.fill("item[<>&]-\\d+");
  await operator.selectOption("equals");
  if (
    (await scalarType.inputValue()) !== "number" ||
    (await scalarType.isDisabled()) ||
    (await scalarValue.inputValue()) !== ""
  )
    fail("EQUALS did not restore Number and clear the pattern");
  await pointer.fill("/reg");
  await page.getByRole("listbox").locator('[data-value="/region"]').click();
  if (
    (await pointer.inputValue()) !== "/region" ||
    (await pointer.getAttribute("aria-expanded")) !== "false"
  )
    fail("pointer click did not select the field and close its suggestions");
  if (
    (await scalarType.inputValue()) !== "string" ||
    (await page.locator('#constraint-values-0 option[value="eu"]').count()) !==
      1
  )
    fail("enum suggestions did not follow the schema type");
  await scalarValue.fill("outside-enum");
  await page.locator('[data-testid="grant-create-submit"]').click();
  const enumOverrideReview = await page
    .locator('[data-testid="grant-review-policy"]')
    .inputValue();
  if (
    !enumOverrideReview.includes('"version":2') ||
    !enumOverrideReview.includes('"/region":"outside-enum"')
  )
    fail("equality-only review restricted enum overrides or omitted v2");
  await page.locator('[data-testid="grant-create-confirm-cancel"]').click();
  await scalarType.selectOption("number");
  await scalarValue.fill("1.00e+2");
  await page.locator('[data-testid="grant-upstream"]').fill("future.tool");
  await page.getByText("Unavailable", { exact: true }).waitFor();
  if (
    (await scalarValue.inputValue()) !== "1.00e+2" ||
    (await scalarType.inputValue()) !== "number"
  )
    fail("tool change rewrote the draft");
  await page.locator('[data-testid="grant-upstream"]').fill("literal.tool");
  await page
    .getByText(/Schema suggests string; your number type is retained/)
    .waitFor();
  await pointer.fill("/filters/item~1name");
  if ((await scalarType.inputValue()) !== "number")
    fail("path selection overwrote an explicit type");
  await pointer.fill("/manual/" + "long-field-".repeat(20));
  await pointer.press("Escape");
  await page.setViewportSize({ width: 320, height: 800 });
  await page.locator(".matcher-editor").scrollIntoViewIfNeeded();
  if ((await pointer.inputValue()) !== "/manual/" + "long-field-".repeat(20))
    fail("narrow manual pointer lost its full value");
  await page.setViewportSize({ width: 1440, height: 900 });
  await pointer.fill("/filters/item~1name");
  let releaseOther = () => {};
  const otherResponse = new Promise<void>((resolve) => {
    releaseOther = resolve;
  });
  let finishOther = () => {};
  const otherFinished = new Promise<void>((resolve) => {
    finishOther = resolve;
  });
  await page.route(
    `**/api/v1/servers/${serverID}/descriptors/01ARZ3NDEKTSV4RRFFQ69G5FC0`,
    async (route) => {
      await otherResponse;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "01ARZ3NDEKTSV4RRFFQ69G5FC0",
          server_id: serverID,
          upstream_name: "other.tool",
          external_name: "Other tool",
          descriptor: {
            name: "other.tool",
            inputSchema: {
              type: "object",
              additionalProperties: false,
              properties: { late: { type: "boolean" } },
            },
            annotations: {
              title: null,
              readOnlyHint: false,
              destructiveHint: false,
              idempotentHint: false,
              openWorldHint: false,
            },
          },
          fingerprint: "other",
          catalog_revision: "1",
          first_seen_at: "2026-08-28T12:00:00Z",
          last_seen_at: "2026-08-28T12:00:00Z",
          retired_at: null,
        }),
      });
      finishOther();
    },
  );
  await Promise.all([
    page.waitForRequest(`**/descriptors/01ARZ3NDEKTSV4RRFFQ69G5FC0`),
    page.locator('[data-testid="grant-upstream"]').fill("other.tool"),
  ]);
  await page.getByText("Loading schema…", { exact: true }).waitFor();
  await page.locator('[data-testid="grant-upstream"]').fill("literal.tool");
  await page
    .getByText(/Schema suggests string; your number type is retained/)
    .waitFor();
  releaseOther();
  await otherFinished;
  await pointer.fill("");
  if (
    (await page
      .locator('#constraint-pointer-0-options [data-value="/late"]')
      .count()) !== 0 ||
    (await page
      .locator(
        '#constraint-pointer-0-options [data-value="/filters/item~1name"]',
      )
      .count()) !== 1 ||
    (await scalarValue.inputValue()) !== "1.00e+2"
  )
    fail("late previous-tool schema replaced current guidance or values");
  await pointer.fill("/filters/item~1name");
  await page
    .locator('[data-testid="grant-server"]')
    .selectOption("00000000000000000000000000");
  await page.locator('[data-testid="grant-server"]').selectOption(serverID);
  await page
    .getByText("Catalog unavailable. Manual entry is still available.", {
      exact: true,
    })
    .waitFor();
  if (
    (await page.locator('[data-testid="grant-upstream"]').inputValue()) !==
    "literal.tool"
  )
    fail("catalog failure discarded the literal tool draft");
  if (
    (await scalarValue.inputValue()) !== "1.00e+2" ||
    (await scalarType.inputValue()) !== "number" ||
    (await pointer.inputValue()) !== "/filters/item~1name"
  )
    fail("server switch or unavailable catalog rewrote constraint rows");
  await page
    .getByRole("button", { name: "Remove constraint 1", exact: true })
    .click();
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await page.locator('[data-testid="unsaved-changes-cancel"]').waitFor();
  await page.locator('[data-testid="unsaved-changes-cancel"]').click();
  await page
    .locator('dialog[aria-labelledby="unsaved-changes-title"]')
    .waitFor({ state: "hidden" });
  if (
    (await page.locator('[data-testid="grant-upstream"]').inputValue()) !==
    "literal.tool"
  )
    fail("grant creation did not preserve a cancelled dirty draft");
  await page
    .locator('[data-testid="grant-expiry"]')
    .fill("2030-01-01T12:34:56");
  if (
    (await page.locator('[data-testid="grant-expiry"]').inputValue()) !==
      "2030-01-01T12:34:56" ||
    (await page.getByTestId("grant-expiry").getAttribute("type")) !==
      "datetime-local"
  )
    fail("grant expiry input did not retain its local date/time draft value");
  const expiryInput = page.getByTestId("grant-expiry");
  await expiryInput.press("Backspace");
  if (
    !(await expiryInput.evaluate(
      (input: HTMLInputElement) =>
        input.value === "" && input.validity.badInput,
    ))
  )
    fail("grant expiry fixture did not produce native incomplete input");
  await page.getByTestId("grant-create-submit").click();
  if (
    !(await expiryInput.evaluate(
      (input: HTMLInputElement) =>
        input.matches(":invalid") &&
        input.validationMessage.length > 0 &&
        document.activeElement === input,
    ))
  )
    fail("native grant validation did not explain and focus incomplete expiry");
  // Programmatic submission bypasses browser constraint validation.
  await expiryInput.evaluate((input: HTMLInputElement) =>
    input.form!.dispatchEvent(
      new Event("submit", { bubbles: true, cancelable: true }),
    ),
  );
  await page
    .getByTestId("grant-create-view")
    .getByText(
      "Choose a complete expiry date and time, or clear it for permanent access.",
      { exact: true },
    )
    .waitFor();
  if (
    creates !== 1 ||
    (await page.getByTestId("grant-create-confirm-submit").isVisible())
  )
    fail("incomplete grant expiry was treated as blank permanent access");
  await expiryInput.fill("2030-01-01T12:34:56");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page.locator('[data-testid="constraint-type"]').selectOption("number");
  await page.locator('[data-testid="constraint-value"]').fill("1.0");
  if ((await page.locator('[data-testid="constraint-version"]').count()) !== 0)
    fail("web authoring retained the version selector");
  if (
    (await page.getByLabel("Constraint preview", { exact: true }).count()) !== 0
  )
    fail("grant form retained redundant editing serialization");
  if (
    !(await page
      .locator('[data-testid="grant-expiry"]')
      .evaluate((node) =>
        Boolean(
          document
            .querySelector(".matcher-editor")!
            .compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING,
        ),
      ))
  )
    fail("expiry was not placed after constraints");
  await page.locator('[data-testid="constraint-pointer"]').fill("bad");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .getByText("Each atom requires a valid RFC 6901 JSON pointer.", {
      exact: true,
    })
    .waitFor();
  if (creates !== 1) fail("invalid grant constraint reached the API");
  await page.locator('[data-testid="constraint-pointer"]').fill("/a~1b/0");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page
    .locator('[data-testid="constraint-pointer"]')
    .nth(1)
    .fill("/empty/");
  await page
    .locator('[data-testid="constraint-type"]')
    .nth(1)
    .selectOption("null");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page.locator('[data-testid="constraint-pointer"]').nth(2).fill("/flag");
  await page
    .locator('[data-testid="constraint-type"]')
    .nth(2)
    .selectOption("boolean");
  await page
    .locator('[data-testid="constraint-value"]')
    .nth(1)
    .selectOption("true");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page.locator('[data-testid="constraint-pointer"]').nth(3).fill("/name");
  await page
    .locator('[data-testid="constraint-type"]')
    .nth(3)
    .selectOption("string");
  await page.locator('[data-testid="constraint-value"]').nth(2).fill("literal");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page
    .locator('[data-testid="constraint-operator"]')
    .nth(4)
    .selectOption("regex");
  if (
    !(await page.locator('[data-testid="constraint-type"]').nth(4).isDisabled())
  )
    fail("regex type was not visibly locked");
  await page
    .locator('[data-testid="constraint-pointer"]')
    .nth(4)
    .fill("/resource");
  await page.locator('[data-testid="constraint-value"]').nth(3).fill("[");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .getByText("/regex/~1resource: pattern is not valid RE2", { exact: true })
    .waitFor();
  if (creates !== 1) fail("invalid RE2 grant matcher reached confirmation");
  await page
    .locator('[data-testid="constraint-value"]')
    .nth(3)
    .fill("item-\\d+");
  await page
    .getByText("/regex/~1resource: pattern is not valid RE2", { exact: true })
    .waitFor({ state: "hidden" });
  if (
    (await page.locator('[data-testid="grant-expiry"]').inputValue()) !==
    "2030-01-01T12:34:56"
  )
    fail("grant constraint edits discarded the expiry draft");
  await assertMatcherAuthoringAccessibility(page, "grant creation");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .locator('[data-testid="grant-create-confirm-submit"]')
    .waitFor({ state: "visible" });
  if (
    !(await page
      .locator('[data-testid="grant-review-policy"]')
      .evaluate(
        (element) =>
          element.getBoundingClientRect().width >=
          element.parentElement!.getBoundingClientRect().width - 1,
      ))
  )
    fail("grant review policy disclosure collapsed beside its label");
  const matcherReview =
    (await page.locator("#grant-create-confirm-consequence").textContent()) ??
    "";
  if (
    !matcherReview.includes("4 equality · 1 regex") ||
    !matcherReview.includes(principalID) ||
    !matcherReview.includes(serverID) ||
    !matcherReview.includes("literal.tool") ||
    !matcherReview.includes("Unavailable — literal manual name") ||
    !matcherReview.includes("Every matcher atom is required (AND)") ||
    !(
      await page.locator('[data-testid="grant-review-policy"]').inputValue()
    ).includes('"/resource":"item-\\\\d+"')
  )
    fail("grant review omitted complete matcher policy disclosure");
  await page.locator('[data-testid="grant-create-confirm-submit"]').click();
  await page.locator('[data-testid="grant-detail"]').waitFor();
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "grant_reads_create_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), attempts, creates, destinations: 4 })}\n`,
  );
}

export async function runGrantCorrection(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const zero = "00000000000000000000000000";
  const ids = Array.from(
    { length: 15 },
    (_, index) => `01ARZ3NDEKTSV4RRFFQ69G${String(index).padStart(4, "0")}`,
  );
  const principalIDs = ids.slice(0, 10);
  const grantIDs = ids.slice(5, 15);
  const grant = (
    id: string,
    principalID: string,
    effect: "allow" | "deny",
    target: string = serverID,
    state: "active" | "expired" = "active",
    readOnly = false,
  ) => ({
    ...(readOnly ? { read_only: true } : {}),
    id,
    description: target === zero ? "Default Gateway access" : `Grant ${id}`,
    revision: "1",
    principal_id: principalID,
    effect,
    server_id: target,
    upstream_name: null,
    constraint: null,
    expires_at: state === "expired" ? "2026-08-28T12:30:00Z" : null,
    state,
    created_at: "2026-08-28T12:00:00Z",
  });
  const grants = new Map<string, ReturnType<typeof grant>>([
    [
      grantIDs[0]!,
      grant(grantIDs[0]!, principalIDs[0]!, "allow", serverID, "active", true),
    ],
    [grantIDs[1]!, grant(grantIDs[1]!, principalIDs[1]!, "deny")],
    [grantIDs[2]!, grant(grantIDs[2]!, principalIDs[2]!, "allow")],
    [grantIDs[3]!, grant(grantIDs[3]!, principalIDs[3]!, "allow")],
    [grantIDs[4]!, grant(grantIDs[4]!, principalIDs[4]!, "deny")],
    [
      grantIDs[5]!,
      grant(grantIDs[5]!, principalIDs[5]!, "allow", serverID, "expired"),
    ],
    [grantIDs[6]!, grant(grantIDs[6]!, principalIDs[6]!, "allow")],
    [grantIDs[7]!, grant(grantIDs[7]!, principalIDs[7]!, "allow", zero)],
    [grantIDs[8]!, grant(grantIDs[8]!, principalIDs[8]!, "allow", zero)],
    [grantIDs[9]!, grant(grantIDs[9]!, principalIDs[9]!, "allow", zero)],
  ]);
  const replacements = new Map<string, string>();
  let creates = 0;
  let deletes = 0;
  const detailRequests: string[] = [];

  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: principalIDs.map((id, index) => ({
          id,
          display_name: `Agent ${index + 1}`,
          state: "active",
          visibility:
            index === 7 ? "allowed-only" : index === 8 ? "requestable" : "all",
          revision: "1",
          credential_revision: "0",
          credential: null,
          created_at: "2026-08-28T12:00:00Z",
          updated_at: "2026-08-28T12:00:00Z",
        })),
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v1/servers?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          serverReadFixture(serverID, {
            name: "Correction server",
            desired: "enabled",
            runtime: "active",
            credential: "ready",
            durable: "current",
            active: "current",
          }),
        ],
        next_cursor: null,
      }),
    });
  });

  await page.route("**/api/v1/principals/*", async (route) => {
    const principalID = new URL(route.request().url()).pathname
      .split("/")
      .pop()!;
    const index = principalIDs.indexOf(principalID);
    const visibility =
      index === 7 ? "allowed-only" : index === 8 ? "requestable" : "all";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${principalID}-1"` },
      body: JSON.stringify({
        id: principalID,
        display_name: `Principal ${index}`,
        state: "active",
        visibility,
        revision: "1",
        credential_revision: "0",
        credential: null,
        created_at: "2026-08-28T12:00:00Z",
        updated_at: "2026-08-28T12:00:00Z",
      }),
    });
  });
  await page.route("**/api/v1/grants?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [...grants.values()].map((grant) =>
          new URL(route.request().url()).searchParams.get("representation") ===
          "table"
            ? {
                grant,
                principal_display_name: `Agent ${principalIDs.indexOf(grant.principal_id) + 1}`,
                server_display_name:
                  grant.server_id === zero
                    ? "Gateway self-service tools"
                    : "Correction server",
              }
            : grant,
        ),
        next_cursor: null,
        ...(new URL(route.request().url()).searchParams.get(
          "representation",
        ) === "table"
          ? {
              total_count: grants.size,
              offset: 0,
            }
          : {}),
      }),
    });
  });
  await page.route("**/api/v1/grants/*", async (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop()!;
    const request = route.request();
    if (request.method() === "GET") {
      detailRequests.push(id);
      const item = grants.get(id);
      if (item === undefined) {
        await route.fulfill({
          status: 404,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 404,
            code: "not_found",
            title: "Not found.",
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(item),
      });
      return;
    }
    if (request.method() !== "DELETE") fail("grant correction method changed");
    deletes += 1;
    const item = grants.get(id);
    if (item?.principal_id === principalIDs[4]) {
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
    grants.delete(id);
    await route.fulfill({ status: 204, body: "" });
  });
  await page.route(`${baseURL}/api/v1/grants`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    creates += 1;
    const body = JSON.parse(route.request().postData() ?? "null") as Record<
      string,
      unknown
    >;
    const principalID = body.principal_id as string;
    if (principalID === principalIDs[0] && body.read_only !== true)
      fail("replacement ALLOW lost read-only access");
    if (
      body.description !== null &&
      (typeof body.description !== "string" || body.description.length === 0)
    )
      fail("grant replacement sent an invalid description");
    if (principalID === principalIDs[2]) {
      await route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 400,
          code: "invalid_grant",
          title: "The grant is invalid.",
        }),
      });
      return;
    }
    if (principalID === principalIDs[3]) {
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "conflict",
          title: "Policy changed concurrently.",
        }),
      });
      return;
    }
    const replacementID = `01ARZ3NDEKTSV4RRFFQ69H${String(creates).padStart(4, "0")}`;
    const item = grant(
      replacementID,
      principalID,
      body.effect as "allow" | "deny",
      body.server_id as string,
      "active",
      body.read_only === true,
    );
    item.description = body.description as string;
    grants.set(replacementID, item);
    replacements.set(principalID, replacementID);
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify(item),
    });
  });

  const navigate = async (grantID: string) => {
    if (!grants.has(grantID)) fail(`missing grant fixture ${grantID}`);
    await page.evaluate((id) => {
      window.location.hash = `#/access/grants/${id}`;
    }, grantID);
    await page.locator('[data-testid="grant-actions"]').waitFor();
    try {
      await page
        .getByRole("heading", { name: `Grant ${grantID}`, exact: true })
        .waitFor({ timeout: 3000 });
    } catch {
      fail(
        `grant navigation failed for ${grantID}; reads=${detailRequests.join(",")}; body=${((await page.locator("body").textContent()) ?? "").slice(0, 1200)}`,
      );
    }
  };
  const confirmAction = async () => {
    await page.locator('[data-testid="grant-action-confirm-submit"]').click();
  };

  await page.evaluate((id) => {
    window.location.hash = `#/access/grants/${id}`;
  }, grantIDs[0]);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await navigate(grantIDs[0]!);

  await page.locator('[data-testid="grant-correct"]').click();
  await page.getByTestId("correction-effect").selectOption("allow");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText(/replacement now overlaps/).waitFor();
  if (Number(creates) !== 1 || Number(deletes) !== 0)
    fail("create-first auto-submitted deletion");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.waitForFunction(() => /\/grants\/.*H/.test(window.location.hash));
  if (Number(deletes) !== 1)
    fail("create-first deletion did not remain step two");

  await navigate(grantIDs[6]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText(/replacement now overlaps/).waitFor();
  const reloadDeletes = Number(deletes);
  await navigate(grantIDs[3]!);
  await navigate(grantIDs[6]!);
  if (
    (await page.locator('[data-testid="grant-correction"]').count()) !== 0 ||
    Number(deletes) !== reloadDeletes
  )
    fail("reload retained or submitted an unconfirmed correction step");

  await navigate(grantIDs[1]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page
    .locator('[data-testid="correction-order"]')
    .selectOption("delete_first");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText(/Authorization is absent/).waitFor();
  const beforeCreate = Number(creates);
  if (Number(deletes) !== reloadDeletes + 1)
    fail("delete-first step one was not isolated");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.waitForFunction(() => /\/grants\/.*H/.test(window.location.hash));
  if (Number(creates) !== beforeCreate + 1)
    fail("delete-first creation did not remain step two");

  await navigate(grantIDs[2]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText("The grant is invalid.", { exact: true }).waitFor();
  const rejectedDeletes = Number(deletes);
  if ((await page.getByText(/replacement now overlaps/).count()) !== 0)
    fail("rejected correction advanced to step two");

  await navigate(grantIDs[3]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page
    .getByText("Policy changed concurrently.", { exact: true })
    .waitFor();
  if (Number(deletes) !== rejectedDeletes)
    fail("stale correction submitted deletion");

  const defaultWarnings: Array<[string, string]> = [
    [
      grantIDs[7]!,
      "removes the principal's access to Gateway self-service tools",
    ],
    [
      grantIDs[8]!,
      "removes the principal's access to Gateway self-service tools",
    ],
    [
      grantIDs[9]!,
      "removes the principal's access to Gateway self-service tools",
    ],
  ];
  for (const [grantID, phrase] of defaultWarnings) {
    await navigate(grantID);
    await page.getByText(new RegExp(phrase)).waitFor();
    if (
      (await page.locator('[data-testid="grant-correct"]').isDisabled()) !==
      true
    )
      fail("default Gateway grant exposed unsupported replacement");
  }

  await navigate(grantIDs[5]!);
  await page
    .getByText("Expired grant still consumes capacity", { exact: true })
    .waitFor();
  if (!(await page.locator('[data-testid="grant-correct"]').isDisabled()))
    fail("expiring grant exposed a permanent replacement workflow");
  await page.locator('[data-testid="grant-delete"]').click();
  await confirmAction();
  await page.locator('[data-testid="grants-view"]').waitFor();

  await navigate(grantIDs[4]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page
    .locator('[data-testid="correction-order"]')
    .selectOption("delete_first");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page
    .getByText("Grant mutation outcome is unknown", { exact: true })
    .waitFor();
  if ((await page.getByText(/Authorization is absent/).count()) !== 0)
    fail("uncertain correction advanced to step two");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "grant_correction_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), creates, deletes, destinations: 10 })}\n`,
  );
}

export async function runRequestReads(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const toolID = "01ARZ3NDEKTSV4RRFFQ69G5FAC";
  const requestIDs = [
    "01ARZ3NDEKTSV4RRFFQ69G5FB0",
    "01ARZ3NDEKTSV4RRFFQ69G5FB1",
    "01ARZ3NDEKTSV4RRFFQ69G5FB2",
    "01ARZ3NDEKTSV4RRFFQ69G5FB3",
  ];
  const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FB9";
  const policy = (target: string, duration: string | null = "600") => ({
    scope: "tool",
    target,
    constraint: { equals: { "/mode": "safe" } },
    duration_seconds: duration,
    future_tools_acknowledged: false,
  });
  const summary = (
    id: string,
    state: "pending" | "approved" | "rejected" | "cancelled",
  ) => ({
    id,
    principal_id: principalID,
    state,
    revision: state === "pending" ? "1" : "2",
    requested_policy: policy("demo.safe"),
    approved_policy: state === "approved" ? policy("demo.safe", "300") : null,
    approved_grant_id: state === "approved" ? grantID : null,
    rejection_reason: state === "rejected" ? "not_approved" : null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
    closed_at: state === "pending" ? null : "2026-08-28T13:00:00Z",
  });
  const descriptor = (name: string) => ({
    name,
    description: "EVIDENCE-CANARY immutable descriptor",
    inputSchema: { type: "object" },
    annotations: {
      title: name,
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  });
  const evidence = (durable: "current" | "retired", fingerprint: string) => ({
    server_id: serverID,
    tool_id: toolID,
    namespace: "demo",
    upstream_name: "safe",
    external_name: "demo.safe",
    catalog_revision: durable === "current" ? "8" : "7",
    fingerprint,
    durable_state: durable,
    descriptor: descriptor(`submitted-${durable}`),
    captured_at: "2026-08-28T12:00:00Z",
  });
  const details = new Map<string, Record<string, unknown>>([
    [
      requestIDs[0]!,
      {
        ...summary(requestIDs[0]!, "pending"),
        resolved_server_id: serverID,
        resolved_upstream_name: "safe",
        submitted_evidence: evidence("current", "submitted-fingerprint"),
        approved_evidence: null,
        current_target: {
          scope: "tool",
          target_state: "extant",
          active_state: "current",
          durable_state: "current",
          catalog_revision: "9",
          fingerprint: "current-drifted-fingerprint",
          descriptor: descriptor("current-drifted"),
        },
      },
    ],
    [
      requestIDs[1]!,
      {
        ...summary(requestIDs[1]!, "approved"),
        resolved_server_id: serverID,
        resolved_upstream_name: "safe",
        submitted_evidence: evidence("current", "submitted-fingerprint"),
        approved_evidence: evidence("retired", "approved-retired-fingerprint"),
        current_target: {
          scope: "tool",
          target_state: "deleted",
          active_state: "absent",
          durable_state: "retired",
          catalog_revision: "7",
          fingerprint: "approved-retired-fingerprint",
          descriptor: descriptor("retired-current-comparison"),
        },
      },
    ],
    [
      requestIDs[2]!,
      {
        ...summary(requestIDs[2]!, "pending"),
        resolved_server_id: serverID,
        resolved_upstream_name: "safe",
        submitted_evidence: null,
        approved_evidence: null,
        current_target: {
          scope: "tool",
          target_state: "extant",
          active_state: "absent",
          durable_state: "absent",
          catalog_revision: null,
          fingerprint: null,
          descriptor: null,
        },
      },
    ],
  ]);
  details.set(requestIDs[3]!, {
    ...details.get(requestIDs[2]!)!,
    ...summary(requestIDs[3]!, "cancelled"),
  });
  let staleRestarted = false;
  let listReads = 0;
  let detailReads = 0;
  let principalReads = 0;
  let releaseInvalidation: (() => void) | undefined;
  const invalidationReady = new Promise<void>((resolve) => {
    releaseInvalidation = resolve;
  });
  await page.route(
    `${baseURL}/api/v1/events`,
    async (route) => {
      await invalidationReady;
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `event: invalidate\ndata: ${JSON.stringify({ kind: "grant_requests", resource_id: requestIDs[0] })}\n\n`,
      });
    },
    { times: 1 },
  );
  await page.route(
    `${baseURL}/api/v1/events`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": reconnect before request invalidation\n\n",
      }),
    { times: 1 },
  );
  await page.route("**/api/v1/grant-requests?*", async (route) => {
    listReads += 1;
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      query.get("representation") !== "table"
    )
      fail("request queue filters changed shape");
    const cursor = query.get("cursor");
    if (cursor !== null && !staleRestarted) {
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
    let rows = Array.from({ length: 128 }, (_, index) => ({
      ...summary(
        requestIDs[index] ??
          `01ARZ3NDEKTSV4RRFFQ69K${String(index).padStart(4, "0")}`,
        index === 3 ? "cancelled" : index % 4 === 0 ? "pending" : "approved",
      ),
      requested_policy: {
        ...policy("demo.safe", index === 3 ? null : "600"),
        constraint:
          index === 3
            ? null
            : index === 1
              ? {
                  version: 2,
                  equals: { "/mode": "safe", "/enabled": true },
                  regex: { "/mode": "^safe$", "/path": "^/safe/" },
                }
              : { equals: { "/mode": "safe" } },
      },
    }));
    rows = rows.filter(
      (item) =>
        (!query.has("state") || item.state === query.get("state")) &&
        (!query.has("request") || item.id.includes(query.get("request")!)),
    );
    if (query.get("direction") === "descending") rows.reverse();
    const offset = Number(cursor ?? 0);
    const selected = rows.slice(offset, offset + 50);
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: selected.map((request) => ({
          request,
          principal_display_name: "Requesting agent",
          server_display_name: "Demo server",
          resolved_server_id: serverID,
          resolved_upstream_name: "safe",
        })),
        total_count: rows.length,
        offset,
        next_cursor:
          offset + selected.length < rows.length
            ? String(offset + selected.length)
            : null,
      }),
    });
  });
  await page.route("**/api/v1/principals?*", async (route) => {
    principalReads += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: principalID,
            display_name: "Requesting agent",
            state: "active",
            visibility: "all",
            revision: "1",
            credential_revision: "0",
            credential: null,
            created_at: "2026-08-28T12:00:00Z",
            updated_at: "2026-08-28T12:00:00Z",
          },
        ],
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v1/grant-requests/*", async (route) => {
    detailReads += 1;
    const id = new URL(route.request().url()).pathname.split("/").pop()!;
    const item = details.get(id) ?? {
      ...details.get(requestIDs[1]!)!,
      ...summary(id, "approved"),
    };
    if (route.request().method() !== "GET")
      fail("request detail changed shape");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"grant-request-${id}-${String(item.revision)}"` },
      body: JSON.stringify(item),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/requests";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page
    .getByText("Showing 1–32 of 32 requests", { exact: true })
    .waitFor();
  await captureRequestState(page, "pending-queue");
  if (
    (await page.locator('[data-testid="request-row"]').count()) !== 32 ||
    !(
      await page
        .getByRole("link", { name: "Review", exact: true })
        .first()
        .getAttribute("href")
    )?.includes(requestIDs[0]!)
  )
    fail("pending queue did not expose oldest request");
  await page.getByRole("link", { name: "All requests", exact: true }).click();
  await page
    .getByText("Showing 1–50 of 128 requests", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await page
    .getByText(
      "The previous page expired or changed. Restarted at the first page.",
      { exact: true },
    )
    .waitFor();
  await page.getByRole("button", { name: "Next", exact: true }).click();
  await page
    .getByText("Showing 51–100 of 128 requests", { exact: true })
    .waitFor();
  await captureRequestState(page, "all-queue-page-two");
  const decisionLink = page
    .getByRole("link", { name: "View decision", exact: true })
    .first();
  const requestIDLink = decisionLink
    .locator("xpath=ancestor::tr")
    .locator('[data-label="Request ID"] a');
  await expect(requestIDLink).toHaveAttribute(
    "href",
    (await decisionLink.getAttribute("href"))!,
  );
  await requestIDLink.click();
  await page.getByTestId("request-detail").waitFor();
  await page
    .getByRole("link", { name: "Back to all requests", exact: true })
    .click();
  await page
    .getByText("Showing 51–100 of 128 requests", { exact: true })
    .waitFor();
  await page.getByRole("button", { name: "Previous", exact: true }).click();
  await page
    .getByText("Showing 1–50 of 128 requests", { exact: true })
    .waitFor();
  await page.getByLabel("Request ID", { exact: true }).fill(requestIDs[0]!);
  await page
    .getByText("Showing 1–1 of 1 matching request", { exact: true })
    .waitFor();
  if (detailReads !== 1 || principalReads !== 0)
    fail("queue traversed identities or expanded per-row detail");
  await page.getByRole("button", { name: "Reset", exact: true }).click();
  await page
    .getByText("Showing 1–50 of 128 requests", { exact: true })
    .waitFor();
  await page
    .getByText("Requesting agent", { exact: true })
    .first()
    .waitFor({ timeout: 5000 })
    .catch(async () =>
      fail(
        `request principal directory did not publish (${principalReads} reads): ${(await page.locator("body").textContent()) ?? ""}`,
      ),
    );
  let body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes("summary-only") ||
    body.includes("Pending filter") ||
    (await page.locator('[data-testid="requests-view"] h2').count()) !== 0 ||
    (await page
      .locator('[data-testid="requests-view"] .panel-code')
      .count()) !== 0
  )
    fail("request queue retained duplicate introductory presentation");
  await expect(
    page.getByRole("heading", { name: "All requests", exact: true }),
  ).toHaveCount(0);
  await expect(
    page
      .getByTestId("request-row")
      .first()
      .locator('[data-label="Request ID"] a'),
  ).toHaveText(/^[0-9A-HJKMNP-TV-Z]{26}$/);
  await expect(
    page.getByTestId("request-row").first().locator("th").locator("a"),
  ).toHaveClass(/button-link/);
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    const linkStyles = await page
      .getByTestId("request-row")
      .first()
      .evaluate((row) => {
        return ["Request ID", "Principal", "Target"].map((label) => {
          const style = getComputedStyle(
            row.querySelector(`[data-label="${label}"] a`)!,
          );
          return {
            color: style.color,
            decoration: style.textDecorationLine,
            weight: style.fontWeight,
          };
        });
      });
    expect(linkStyles[0]).toEqual(linkStyles[1]);
    expect(linkStyles[0]).toEqual(linkStyles[2]);
  }
  await page.emulateMedia({ colorScheme: "light" });
  const queueGap = await page
    .getByRole("navigation", { name: "Request queues" })
    .evaluate((node) => parseFloat(getComputedStyle(node).marginBottom));
  if (queueGap < 24) fail("queue tabs are cramped against filters");
  const requestHeaders = await page.locator("thead th").allTextContents();
  if (
    requestHeaders.map((header) => header.replace(/[↕↑↓]/g, "")).join("|") !==
    "Action|Request ID|Principal|Target|State|Requested duration|Constraints|Submitted"
  )
    fail(`request table columns changed: ${requestHeaders.join("|")}`);
  if (
    !body.includes("Requesting agent") ||
    !body.includes("Showing 1–50 of 128 requests") ||
    body.includes("EVIDENCE-CANARY") ||
    detailReads !== 1
  )
    fail("request collection omitted shared metadata or expanded evidence");
  await page.getByRole("button", { name: "Submitted", exact: true }).click();
  const requestRows = page.getByTestId("request-row");
  await expect(requestRows.first()).toContainText(requestIDs[0]!);
  await expect(
    requestRows.nth(0).locator('[data-label="Requested duration"]'),
  ).toHaveText("10 minutes");
  await expect(
    requestRows.nth(0).locator('[data-label="Constraints"]'),
  ).toHaveText("1");
  await expect(requestRows.nth(0).locator(".status-label")).toHaveAttribute(
    "data-state",
    "warning",
  );
  await expect(
    requestRows.nth(1).locator('[data-label="Constraints"]'),
  ).toHaveText("4");
  await expect(requestRows.nth(1).locator(".status-label")).toHaveAttribute(
    "data-state",
    "current",
  );
  await expect(
    requestRows.nth(3).locator('[data-label="Requested duration"]'),
  ).toHaveText("No expiry");
  await expect(
    requestRows.nth(3).locator('[data-label="Constraints"]'),
  ).toHaveText("0");
  const cancelledStatus = requestRows.nth(3).locator(".status-label");
  await expect(cancelledStatus).toHaveText("Cancelled");
  await expect(cancelledStatus).toHaveAttribute("data-state", "neutral");
  for (const colorScheme of ["light", "dark"] as const) {
    await page.emulateMedia({ colorScheme });
    expect(
      await cancelledStatus.evaluate((node) => {
        const probe = document.createElement("span");
        probe.style.color = "var(--muted)";
        node.append(probe);
        const matches =
          getComputedStyle(node).color === getComputedStyle(probe).color;
        probe.remove();
        return matches;
      }),
    ).toBe(true);
  }
  await page.emulateMedia({ colorScheme: "light" });
  await captureRequestState(page, "request-table");
  const stateFilter = page.getByLabel("State", { exact: true });
  await stateFilter.selectOption("approved");
  await page
    .getByText("Showing 1–50 of 95 matching requests", { exact: true })
    .waitFor();
  if (
    (await page.locator('[data-testid="request-row"]').count()) !== 50 ||
    !(await page.evaluate(() => window.location.hash)).includes(
      "filter_state=approved",
    )
  )
    fail("request state filter was not URL-backed");
  await page.getByRole("button", { name: "Reset" }).click();
  await page
    .getByText("Showing 1–50 of 128 requests", { exact: true })
    .waitFor();

  const beforeRefresh = listReads;
  releaseInvalidation?.();
  await page.waitForResponse(
    (response) =>
      response.url().includes("/api/v1/grant-requests?") &&
      response.status() === 200,
  );
  if (listReads <= beforeRefresh)
    fail("request invalidation did not revalidate queue");

  const navigate = async (id: string) => {
    await page.evaluate((requestID) => {
      window.location.hash = `#/requests/${requestID}`;
    }, id);
    await page.locator(`[data-request-id="${id}"]`).waitFor();
    const requestDetail = page.locator('[data-testid="request-detail"]');
    if (
      (await requestDetail
        .locator('[data-testid="detail-context"] h1')
        .count()) !== 1 ||
      (await requestDetail
        .getByRole("heading", {
          level: 2,
          name: "Request details",
          exact: true,
        })
        .count()) !== 1 ||
      (await requestDetail.locator("section.panel h1").count()) !== 0
    )
      fail("request detail did not use the shared detail hierarchy");
  };
  await navigate(requestIDs[0]!);
  await captureRequestState(page, "changed-evidence");
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Submitted policy and evidence — immutable",
    "Current target comparison — read-time",
    "Choose a decision",
    "Descriptor fingerprint changed",
    "Approve as requested",
    "Customize approval",
  ])
    if (!body.includes(phrase)) fail(`request detail omitted ${phrase}`);
  if (
    (await page.locator(`a[href="#/principals/${principalID}"]`).count()) ===
      0 ||
    (await page
      .locator(`a[href="#/servers/${serverID}?tab=tools"]`)
      .count()) === 0 ||
    (await page
      .locator(`a[href="#/servers/${serverID}/descriptors/${toolID}"]`)
      .count()) === 0
  )
    fail("request detail omitted reciprocal navigation");

  await navigate(requestIDs[1]!);
  await expect(
    page.getByRole("heading", { name: "Approved request", exact: true }),
  ).toBeVisible();
  const approvedDecision = page.getByRole("region", {
    name: "Approved decision",
    exact: true,
  });
  await expect(
    approvedDecision.getByRole("link", { name: grantID, exact: true }),
  ).toHaveAttribute("href", `#/grants/${grantID}`);
  await expect(approvedDecision).not.toContainText("ordinary ALLOW");
  await expect(approvedDecision).not.toContainText("This historical link");
  await captureRequestState(page, "approved-deleted");
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "retired historical evidence",
    "Retained evidence is not callable authority",
    "Created grant",
    grantID,
  ])
    if (!body.includes(phrase))
      fail(`approved request history omitted ${phrase}`);

  await navigate(requestIDs[2]!);
  await captureRequestState(page, "absent-evidence");
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("Current descriptor is absent") ||
    !body.includes("no descriptor evidence")
  )
    fail("absent request target omitted explicit evidence state");
  await navigate(requestIDs[3]!);
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();
  if ((await page.getByTestId("request-actions").count()) !== 0)
    fail("cancelled request remained editable");
  await expect(
    page.getByRole("heading", { name: "Cancelled request", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByTestId("request-detail").locator(".panel-heading .status-label"),
  ).toHaveAttribute("data-state", "neutral");
  await captureRequestState(page, "cancelled");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "request_reads_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), list_reads: listReads, detail_reads: detailReads, destinations: 5 })}\n`,
  );
}

export async function runRequestAdjudication(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const ids = Array.from(
    { length: 13 },
    (_, index) => `01ARZ3NDEKTSV4RRFFQ69J${String(index).padStart(4, "0")}`,
  );
  const policy = (
    scope: "tool" | "server",
    target: string,
    constraint: unknown | null,
    duration: string | null,
  ) => ({
    scope,
    target,
    constraint,
    duration_seconds: duration,
    future_tools_acknowledged: scope === "server",
  });
  const states = new Map<string, Record<string, unknown>>();
  const detail = (
    id: string,
    requested: ReturnType<typeof policy>,
    state: "pending" | "approved" | "rejected" = "pending",
    approved: ReturnType<typeof policy> | null = null,
    reason: string | null = null,
  ) => ({
    id,
    principal_id: principalID,
    state,
    revision: state === "pending" ? "1" : "2",
    requested_policy: requested,
    approved_policy: approved,
    approved_grant_id:
      state === "approved" ? "01ARZ3NDEKTSV4RRFFQ69G5FB9" : null,
    rejection_reason: reason,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
    closed_at: state === "pending" ? null : "2026-08-28T13:00:00Z",
    resolved_server_id: serverID,
    resolved_upstream_name: requested.scope === "tool" ? "safe" : null,
    submitted_evidence: null,
    approved_evidence: null,
    current_target: {
      scope: requested.scope,
      target_state: "extant",
      active_state: requested.scope === "tool" ? "current" : null,
      durable_state: requested.scope === "tool" ? "current" : null,
      catalog_revision: requested.scope === "tool" ? "1" : null,
      fingerprint: requested.scope === "tool" ? "fingerprint" : null,
      descriptor:
        requested.scope === "tool"
          ? { name: "safe", inputSchema: {}, annotations: {} }
          : null,
    },
  });
  states.set(ids[0]!, detail(ids[0]!, policy("server", "demo", null, "1200")));
  states.set(
    ids[1]!,
    detail(
      ids[1]!,
      policy(
        "tool",
        "demo.safe",
        {
          version: 2,
          equals: { "/mode": "safe", "/attempt": 1 },
          regex: { "/resource": "item[<>&]-\\d+" },
        },
        "600",
      ),
    ),
  );
  for (let index = 2; index <= 5; index++)
    states.set(
      ids[index]!,
      detail(ids[index]!, policy("tool", "demo.safe", null, null)),
    );
  for (const index of [6, 7, 8, 11, 12])
    states.set(
      ids[index]!,
      detail(ids[index]!, {
        ...policy("server", "demo", null, "1200"),
        read_only: true,
      } as ReturnType<typeof policy>),
    );
  states.set(ids[9]!, detail(ids[9]!, policy("server", "demo", null, null)));
  states.set(
    ids[10]!,
    detail(
      ids[10]!,
      policy(
        "tool",
        "demo.safe",
        { equals: { "/attempt": 1, "/literal": "<>&" } },
        null,
      ),
    ),
  );
  let approvals = 0;
  let rejections = 0;
  const attempts = new Map<string, number>();

  await page.route("**/api/v1/grant-requests?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    const rows = [...states.values()].filter(
      (item) =>
        (!query.has("request") || item.id === query.get("request")) &&
        (!query.has("state") || item.state === query.get("state")),
    );
    const items = rows.map((item) => ({
      request: Object.fromEntries(
        [
          "id",
          "principal_id",
          "state",
          "revision",
          "requested_policy",
          "approved_policy",
          "approved_grant_id",
          "rejection_reason",
          "created_at",
          "updated_at",
          "closed_at",
        ].map((key) => [key, item[key]]),
      ),
      principal_display_name: "Review agent",
      server_display_name: "Demo server",
      resolved_server_id: serverID,
      resolved_upstream_name: item.resolved_upstream_name,
    }));
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items,
        total_count: items.length,
        offset: 0,
        next_cursor: null,
      }),
    });
  });

  await page.route(
    `**/api/v1/servers/${serverID}/descriptors?*`,
    async (route) => {
      const query = new URL(route.request().url()).searchParams;
      if (
        query.get("limit") !== "100" ||
        query.get("retired") !== "exclude" ||
        query.get("representation") !== "summary"
      )
        fail("approval descriptor traversal changed shape");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            {
              id: "01ARZ3NDEKTSV4RRFFQ69G5FC0",
              server_id: serverID,
              upstream_name: "safe",
              external_name: "demo.safe",
              catalog_revision: "1",
            },
          ],
          next_cursor: null,
        }),
      });
    },
  );
  await page.route(
    `**/api/v1/servers/${serverID}/descriptors/01ARZ3NDEKTSV4RRFFQ69G5FC0`,
    async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          id: "01ARZ3NDEKTSV4RRFFQ69G5FC0",
          server_id: serverID,
          upstream_name: "safe",
          external_name: "demo.safe",
          descriptor: {
            name: "safe",
            inputSchema: {
              type: "object",
              additionalProperties: false,
              properties: { mode: { type: "string" } },
            },
            annotations: {
              title: null,
              readOnlyHint: false,
              destructiveHint: false,
              idempotentHint: false,
              openWorldHint: false,
            },
          },
          fingerprint: "fingerprint-demo-safe",
          catalog_revision: "1",
          first_seen_at: "2026-08-28T12:00:00Z",
          last_seen_at: "2026-08-28T12:00:00Z",
          retired_at: null,
        }),
      });
    },
  );

  await page.route("**/api/v1/grant-requests/**", async (route) => {
    const parts = new URL(route.request().url()).pathname.split("/");
    const action = parts.at(-1)!;
    const id =
      action === "approve" || action === "reject" ? parts.at(-2)! : action;
    const item = states.get(id);
    if (item === undefined) fail("unknown adjudication fixture");
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"grant-request-${id}-${String(item.revision)}"` },
        body:
          id === ids[1] || id === ids[10]
            ? JSON.stringify(item).replace('"/attempt":1', '"/attempt":1.0')
            : JSON.stringify(item),
      });
      return;
    }
    attempts.set(id, (attempts.get(id) ?? 0) + 1);
    const headers = await route.request().allHeaders();
    if (
      route.request().method() !== "POST" ||
      headers["if-match"] !== `"grant-request-${id}-${String(item.revision)}"`
    )
      fail("request adjudication precondition changed shape");
    if (id === ids[6]) {
      states.set(
        id,
        detail(
          id,
          item.requested_policy as ReturnType<typeof policy>,
          "approved",
          item.requested_policy as ReturnType<typeof policy>,
        ),
      );
      await route.fulfill({
        status: 412,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 412,
          code: "stale_grant_request_revision",
          title: "The request revision is stale.",
        }),
      });
      return;
    }
    if (id === ids[7]) {
      await route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 400,
          code: "invalid_grant_request",
          title: "The request adjudication is invalid.",
        }),
      });
      return;
    }
    if (id === ids[8]) {
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
    const submitted = item.requested_policy as ReturnType<typeof policy>;
    if (action === "approve") {
      approvals += 1;
      const raw = route.request().postData() ?? "null";
      if (
        id === ids[1] &&
        (!raw.includes('"/attempt":1.0') ||
          !raw.includes('"/resource":"item[<>&]-\\\\d+"') ||
          raw.includes("\\u003c") ||
          raw.includes("\\u003e") ||
          raw.includes("\\u0026") ||
          !raw.includes('"/extra":1.0') ||
          !raw.includes('"/zone":"(local|dev)"'))
      )
        fail(`version 2 approval did not preserve matcher tokens: ${raw}`);
      const body = JSON.parse(raw) as Record<string, unknown>;
      if (
        Object.keys(body).join(",") !== "description,approved_policy" ||
        (body.description !== null && typeof body.description !== "string")
      )
        fail("approval body changed shape");
      const approved = body.approved_policy as ReturnType<typeof policy> & {
        read_only?: boolean;
      };
      if (
        [ids[9], ids[11], ids[12]].includes(id) &&
        (approved.read_only !== true ||
          approved.scope !== "server" ||
          approved.constraint !== null ||
          !approved.future_tools_acknowledged)
      )
        fail("read-only approval lost its server restriction");
      if (id === ids[11] && approved.duration_seconds !== "1200")
        fail("as-requested read-only duration changed");
      if (id === ids[12] && approved.duration_seconds !== "600")
        fail("custom read-only duration changed");
      const expectedSeconds =
        id === ids[0]
          ? "600"
          : id === ids[1]
            ? "300"
            : id === ids[9]
              ? "60"
              : undefined;
      if (
        expectedSeconds !== undefined &&
        approved.duration_seconds !== expectedSeconds
      )
        fail("duration amount/unit did not serialize exact seconds");
      if (
        id === ids[0] &&
        (approved.target !== "demo.safe" ||
          (approved.constraint as Record<string, unknown>).version !== 2)
      )
        fail(
          "server-to-tool equality approval did not preserve the external target and v2",
        );
      if (
        id === ids[10] &&
        (body.description !== "Revised metadata" ||
          !raw.includes('"version":2') ||
          !raw.includes('"/attempt":1.0') ||
          !raw.includes('"/literal":"<>&"'))
      )
        fail("v1 approval without additions did not retain exact atoms in v2");
      states.set(id, detail(id, submitted, "approved", approved));
    } else {
      rejections += 1;
      const body = JSON.parse(route.request().postData() ?? "null") as Record<
        string,
        unknown
      >;
      states.set(
        id,
        detail(id, submitted, "rejected", null, body.reason as string),
      );
    }
    const result = states.get(id)!;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"grant-request-${id}-2"` },
      body: JSON.stringify(result),
    });
  });

  const navigate = async (id: string, narrow = true) => {
    await page.evaluate((requestID) => {
      window.location.hash = `#/requests/${requestID}`;
    }, id);
    await page.locator(`[data-request-id="${id}"]`).waitFor();
    await page.locator('[data-testid="request-actions"]').waitFor();
    await expect(
      page.getByText("Access considerations", { exact: true }),
    ).toHaveCount(0);
    if ([ids[0], ids[1], ids[9]].includes(id))
      await captureRequestState(page, `summary-${ids.indexOf(id)}`);
    if (narrow)
      await page
        .getByRole("button", { name: "Customize approval", exact: true })
        .press("Enter");
    await expect(
      page.getByRole("heading", { name: "Customize approval", exact: true }),
    ).toHaveCount(0);
  };
  const confirm = async () => {
    await page.locator('dialog[open] [data-testid$="-submit"]').click();
  };
  const reviewApproval = async () => {
    await page.locator('[data-testid="request-approve"]').click();
  };

  await page.evaluate((id) => {
    window.location.hash = `#/requests/${id}`;
  }, ids[0]);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await navigate(ids[0]!);
  await expect(
    page.getByRole("region", { name: "Approval preview" }),
  ).toContainText("No changes to requested access.");
  await expect(page.getByTestId("request-approve")).toHaveText(
    "Approve as requested",
  );
  await page
    .locator('[data-testid="approval-description"]')
    .fill("Unsafe\u0085access");
  await reviewApproval();
  await page
    .getByText("Grant description cannot contain control characters.", {
      exact: true,
    })
    .waitFor();
  if ((attempts.get(ids[0]!) ?? 0) !== 0)
    fail("control-character approval description reached the API");
  await page
    .locator('[data-testid="approval-description"]')
    .fill("Access to demo.safe");
  await page.locator('[data-testid="approval-scope"]').selectOption("tool");
  const approvalTarget = page.getByRole("combobox", {
    name: "Approved target",
    exact: true,
  });
  await approvalTarget.fill("demo.sa");
  await page.getByRole("listbox").locator('[data-value="demo.safe"]').waitFor();
  await approvalTarget.press("ArrowDown");
  await approvalTarget.press("Enter");
  if ((await approvalTarget.inputValue()) !== "demo.safe")
    fail("approval target autocomplete did not select the exact target");
  await page.getByText("Known", { exact: true }).waitFor();
  await page.locator('[data-testid="approval-additional-add"]').click();
  const approvalPointer = page.locator(
    '[data-testid="approval-additional-pointer"]',
  );
  await approvalPointer.focus();
  await page.getByRole("listbox").locator('[data-value="/mode"]').waitFor();
  await approvalPointer.fill("/mo");
  await approvalPointer.press("ArrowDown");
  await approvalPointer.press("Enter");
  if ((await approvalPointer.inputValue()) !== "/mode")
    fail("approval pointer autocomplete did not select the field");
  await page.locator('[data-testid="approval-additional-value"]').fill("safe");
  await page.getByTestId("approval-duration-unit").selectOption("minutes");
  await page.locator('[data-testid="approval-duration"]').fill("10");
  await expect(page.getByTestId("request-approve")).toHaveText(
    "Approve as narrowed",
  );
  await expect(page.getByTestId("request-approve")).toHaveClass(
    /primary-action/,
  );
  await page
    .getByRole("button", { name: "Hide customization", exact: true })
    .press("Enter");
  await expect(
    page.getByRole("button", { name: "Customize approval", exact: true }),
  ).toHaveAttribute("aria-expanded", "false");
  await expect(
    page.getByText("Custom approval edited", { exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("approval-duration")).toHaveCount(0);
  await page
    .getByRole("button", { name: "Approve as requested", exact: true })
    .click();
  await expect(
    page.getByRole("dialog", { name: "Approve as requested?", exact: true }),
  ).toBeVisible();
  const originalPreview = JSON.parse(
    await page.getByTestId("approval-review-policy").inputValue(),
  );
  if (
    originalPreview.approved_policy.scope !== "server" ||
    originalPreview.approved_policy.constraint !== null ||
    originalPreview.approved_policy.duration_seconds !== "1200"
  )
    fail("hidden customization leaked into unchanged approval");
  await page.getByTestId("request-adjudication-confirm-cancel").click();
  await page
    .getByRole("button", { name: "Customize approval", exact: true })
    .click();
  await expect(page.getByTestId("approval-duration")).toHaveValue("10");
  await expect(approvalTarget).toHaveValue("demo.safe");
  await expect(page.getByTestId("approval-additional-value")).toHaveValue(
    "safe",
  );
  if (
    !(await page
      .getByRole("button", { name: "Approve as requested", exact: true })
      .isVisible()) ||
    !(await page.getByTestId("request-reject").isVisible())
  )
    fail("decision paths are not directly available");
  await reviewApproval();
  if (
    !(
      await page
        .locator("#request-adjudication-confirm-consequence")
        .innerText()
    ).includes("Current definition available.")
  )
    fail("server-to-tool approval review used stale catalog posture");
  const narrowedEvidence = await page
    .locator("#request-adjudication-confirm-consequence")
    .innerText();
  if (
    !narrowedEvidence.includes(
      "Narrowed to one tool — no submitted definition to compare.",
    ) ||
    narrowedEvidence.includes("Not applicable to server-wide authority") ||
    narrowedEvidence.includes("Descriptor changed")
  )
    fail("narrowed tool confirmation misrepresented descriptor comparison");
  await expect(
    page.getByRole("dialog", { name: "Approve as narrowed?", exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("dialog")).toContainText("Access to demo.safe");
  await captureRequestState(page, "server-to-tool-confirmation");
  await confirm();
  try {
    await page
      .getByText("Request adjudication is closed", { exact: true })
      .waitFor({ timeout: 3000 });
  } catch {
    fail(
      `first approval did not close: ${((await page.locator("body").textContent()) ?? "").slice(-1200)}`,
    );
  }

  const terminalComparison = page.getByRole("region", {
    name: "Requested versus approved",
    exact: true,
  });
  await expect(terminalComparison).toBeVisible();
  for (const phrase of [
    "Server → Tool",
    "demo → demo.safe",
    "20 minutes → 10 minutes",
    "Requested conditions",
    "No argument restrictions",
    "Approved conditions",
    '/mode equals "safe"',
  ]) {
    await expect(terminalComparison).toContainText(phrase);
  }
  await captureRequestState(page, "server-to-tool-approved");

  await navigate(ids[1]!);
  await page
    .getByText(
      "Some schema fields cannot be suggested. Custom pointers are still available.",
      { exact: true },
    )
    .waitFor();
  const submittedConstraint = page.locator(
    '[data-testid="approval-submitted-constraint"]',
  );
  if (
    (await submittedConstraint.isEditable()) ||
    !(await submittedConstraint.inputValue()).includes('"/attempt":1.0')
  )
    fail("approval editor did not lock exact submitted matcher tokens");
  if (
    (await page.getByTestId("approval-duration-unit").inputValue()) !==
      "minutes" ||
    (await page.getByTestId("approval-duration").inputValue()) !== "10"
  )
    fail("submitted duration did not load in a readable exact unit");
  await page.getByTestId("approval-duration-unit").selectOption("seconds");
  await page.locator('[data-testid="approval-duration"]').fill("601");
  await reviewApproval();
  await page
    .getByText("Approval cannot extend the submitted duration.", {
      exact: true,
    })
    .waitFor();
  await page.locator('[data-testid="approval-duration"]').fill("");
  await reviewApproval();
  await page
    .getByText("A temporary request cannot become permanent.", { exact: true })
    .waitFor();
  await page.getByTestId("approval-duration-unit").selectOption("minutes");
  await page.locator('[data-testid="approval-duration"]').fill("5");
  await page.locator('[data-testid="approval-additional-add"]').click();
  const additionalPointers = page.locator(
    '[data-testid="approval-additional-pointer"]',
  );
  const additionalOperators = page.locator(
    '[data-testid="approval-additional-operator"]',
  );
  const additionalTypes = page.locator(
    '[data-testid="approval-additional-type"]',
  );
  const additionalValues = page.locator(
    '[data-testid="approval-additional-value"]',
  );
  await additionalPointers.first().fill("/mode");
  await additionalValues.first().fill("other");
  await reviewApproval();
  await page
    .getByText(
      "Additional matcher atoms cannot replace a submitted operator and pointer.",
      { exact: true },
    )
    .waitFor();
  await additionalPointers.first().fill("/extra");
  await additionalTypes.first().selectOption("number");
  await additionalValues.first().fill("1.0");
  await page.locator('[data-testid="approval-additional-add"]').click();
  await additionalOperators.nth(1).selectOption("regex");
  await additionalPointers.nth(1).fill("/zone");
  await additionalValues.nth(1).fill("[");
  await reviewApproval();
  await page
    .getByText("/regex/~1zone: pattern is not valid RE2", { exact: true })
    .waitFor();
  if ((attempts.get(ids[1]!) ?? 0) !== 0)
    fail("invalid RE2 approval reached confirmation");
  await additionalValues.nth(1).fill("(local|dev)");
  await captureRequestState(page, "narrowed-draft");
  if (
    (await page.getByLabel("Constraint preview", { exact: true }).count()) !== 0
  )
    fail("approval form retained redundant editing serialization");
  await assertMatcherAuthoringAccessibility(page, "request approval");
  if (await page.getByText("Check adjudication", { exact: true }).isVisible())
    fail("corrected matcher retained a stale adjudication error");
  await reviewApproval();
  await page
    .getByText("Exact identifiers and serialized policy", { exact: true })
    .click();
  await captureRequestState(page, "narrowed-confirmation");
  const approvalReview = await page
    .locator("#request-adjudication-confirm-consequence")
    .innerText();
  if (
    !approvalReview.includes(principalID) ||
    !approvalReview.includes(serverID) ||
    !approvalReview.includes("demo.safe") ||
    !approvalReview.includes("Comparison unavailable") ||
    /Description\s*None/.test(approvalReview) ||
    !/Duration\s*5 minutes from approval/.test(approvalReview) ||
    !approvalReview.includes("/zone") ||
    !approvalReview.includes("/extra") ||
    !approvalReview.includes("All conditions must match.") ||
    !approvalReview.includes("Matching deny grants still take precedence.") ||
    !(
      await page.locator('[data-testid="approval-review-policy"]').inputValue()
    ).includes('"/attempt":1.0')
  )
    fail(
      `approval review omitted complete matcher policy disclosure: ${approvalReview}`,
    );
  await confirm();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();

  for (const phrase of [
    "Tool → Unchanged",
    "demo.safe → Unchanged",
    "10 minutes → 5 minutes",
    '/mode equals "safe"',
    "/extra equals 1",
    '/zone matches "(local|dev)"',
  ]) {
    await expect(terminalComparison).toContainText(phrase);
  }
  await captureRequestState(page, "exact-tool-approved");

  await navigate(ids[10]!, false);
  const lockedV1 = await page
    .getByLabel("Exact condition source", { exact: true })
    .first()
    .inputValue();
  if (lockedV1.includes('"version"') || !lockedV1.includes('"/attempt":1.0'))
    fail("existing v1 request was rewritten before approval");
  await page
    .getByRole("button", { name: "Approve as requested", exact: true })
    .press("Enter");
  await page
    .getByRole("heading", { name: "Approve as requested?", exact: true })
    .waitFor();
  await captureRequestState(page, "unchanged-confirmation");
  await page.keyboard.press("Escape");
  await page
    .getByTestId("request-adjudication-confirm-submit")
    .waitFor({ state: "hidden" });
  await page.waitForFunction(
    () => document.activeElement?.textContent === "Approve as requested",
  );
  if (
    (attempts.get(ids[10]!) ?? 0) !== 0 ||
    !(await page
      .getByRole("button", { name: "Approve as requested", exact: true })
      .evaluate((element) => element === document.activeElement))
  )
    fail("unchanged cancellation mutated or lost focus");
  await page
    .getByRole("button", { name: "Approve as requested", exact: true })
    .click();
  await page
    .getByTestId("request-adjudication-confirm-submit")
    .waitFor({ state: "visible" });
  await page.getByTestId("approval-description").evaluate((input) => {
    (input as HTMLInputElement).value = "Revised metadata";
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  await confirm();
  await page
    .getByText("The draft changed. Review the current draft again.", {
      exact: true,
    })
    .waitFor();
  if ((attempts.get(ids[10]!) ?? 0) !== 0)
    fail("stale reviewed draft reached mutation");
  await page
    .getByRole("button", { name: "Approve as requested", exact: true })
    .click();
  await page
    .getByText("Exact identifiers and serialized policy", { exact: true })
    .click();
  if (
    !(
      await page.locator('[data-testid="approval-review-policy"]').inputValue()
    ).includes('"version":2')
  )
    fail("v1 equality-only approval review did not disclose v2");
  await page
    .locator('[data-testid="request-adjudication-confirm-submit"]')
    .waitFor({ state: "visible" });
  if (
    !(await page
      .locator('[data-testid="approval-review-policy"]')
      .evaluate(
        (element) =>
          element.getBoundingClientRect().width >=
          element.parentElement!.getBoundingClientRect().width - 1,
      ))
  )
    fail("approval review policy disclosure collapsed beside its label");
  await confirm();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();

  const reasons = [
    "not_approved",
    "existing_access",
    "scope_too_broad",
    "policy_conflict",
  ];
  for (let index = 0; index < reasons.length; index++) {
    await navigate(ids[index + 2]!);
    await page.getByTestId("approval-duration").fill("0");
    await page.locator('[data-testid="request-reject"]').click();
    await page.getByTestId("rejection-reason").selectOption(reasons[index]!);
    if (index === 0) {
      await captureRequestState(page, "rejection-dialog");
      await page.getByTestId("request-rejection-cancel").click();
      await page.waitForFunction(
        () =>
          document.querySelector('[data-testid="request-reject"]') ===
          document.activeElement,
      );
      await page.getByTestId("request-reject").click();
    }
    await confirm();
    await page
      .getByText("Request adjudication is closed", { exact: true })
      .waitFor();
    await expect(
      page.getByRole("heading", { name: "Rejected request", exact: true }),
    ).toBeVisible();
  }

  await navigate(ids[9]!);
  await page
    .getByRole("button", { name: "Approve as requested", exact: true })
    .first()
    .click();
  const permanentDialog = page.getByRole("dialog", {
    name: "Approve as requested?",
    exact: true,
  });
  await expect(permanentDialog).toBeVisible();
  await expect(permanentDialog).toContainText("No expiry");
  await expect(permanentDialog).not.toContainText("from approval");
  await expect(permanentDialog).not.toContainText("Temporary access");
  await expect(
    permanentDialog.getByText("Access considerations", { exact: true }),
  ).toHaveCount(0);
  await expect(permanentDialog.locator("dt")).toHaveText([
    "Principal",
    "Approved target",
    "Tools",
    "Duration",
    "Conditions",
    "Tool definition",
  ]);
  for (const text of [
    "Access does not expire automatically.",
    "This grant adds no restrictions on argument values.",
    "Access includes tools added to this server later.",
  ]) {
    await expect(page.getByTestId("request-detail")).not.toContainText(text);
  }
  await captureRequestState(page, "permanent-confirmation");
  await page.getByTestId("request-adjudication-confirm-cancel").click();
  await page.getByTestId("approval-duration-unit").selectOption("seconds");
  await page.getByTestId("approval-duration").fill("59");
  await reviewApproval();
  await page
    .getByText("Duration must be between 1 minute and 30 days.", {
      exact: true,
    })
    .waitFor();
  await page.getByTestId("approval-duration-unit").selectOption("days");
  await page.getByTestId("approval-duration").fill("31");
  await reviewApproval();
  await page
    .getByText("Duration must be between 1 minute and 30 days.", {
      exact: true,
    })
    .waitFor();
  await page.getByTestId("approval-duration").fill("30");
  await reviewApproval();
  await page.getByTestId("request-adjudication-confirm-cancel").click();
  if ((attempts.get(ids[9]!) ?? 0) !== 0)
    fail("duration boundary review mutated");
  await page.getByTestId("approval-duration-unit").selectOption("minutes");
  await page.locator('[data-testid="approval-duration"]').fill("1");
  await page
    .getByRole("combobox", { name: "Allowed tools" })
    .selectOption("read-only");
  await reviewApproval();
  await page
    .getByText("Not applicable to server-wide authority", { exact: true })
    .last()
    .waitFor();
  await confirm();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();

  for (const index of [11, 12]) {
    await navigate(ids[index]!);
    await expect(
      page.getByRole("combobox", { name: "Allowed tools" }),
    ).toHaveValue("read-only");
    await captureRequestState(page, "read-only-request-dropdown");
    await expect(page.getByTestId("approval-read-only")).toBeDisabled();
    await expect(page.getByTestId("approval-scope")).toBeDisabled();
    if (index === 12) {
      await page.getByTestId("approval-duration-unit").selectOption("minutes");
      await page.getByTestId("approval-duration").fill("10");
      await reviewApproval();
    } else {
      await page
        .getByRole("button", { name: "Approve as requested", exact: true })
        .first()
        .click();
    }
    await expect(page.getByRole("dialog")).toContainText(
      "Read-only server tools",
    );
    await expect(page.getByTestId("approval-review-policy")).toHaveValue(
      /"read_only":true/,
    );
    if (index === 11)
      await captureRequestState(page, "read-only-request-confirmation");
    await confirm();
    await page
      .getByText("Request adjudication is closed", { exact: true })
      .waitFor();
    await expect(terminalComparison).toContainText(
      "Read-only server tools → Read-only server tools",
    );
    if (index === 11)
      await captureRequestState(page, "read-only-request-approved");
  }

  await navigate(ids[6]!);
  await reviewApproval();
  await confirm();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();
  if ((attempts.get(ids[6]!) ?? 0) !== 1) fail("stale adjudication replayed");

  await navigate(ids[7]!);
  await page.locator('[data-testid="request-reject"]').click();
  await confirm();
  await page
    .getByText("The request adjudication is invalid.", { exact: true })
    .waitFor();
  if ((attempts.get(ids[7]!) ?? 0) !== 1) fail("known failure replayed");

  await navigate(ids[8]!);
  await reviewApproval();
  await confirm();
  await page
    .getByText("Adjudication outcome is unknown", { exact: true })
    .waitFor();
  await captureRequestState(page, "uncertain");
  await page.getByTestId("manual-refresh").click();
  await page
    .getByText("Adjudication outcome is unknown", { exact: true })
    .waitFor();
  if ((attempts.get(ids[8]!) ?? 0) !== 1)
    fail("uncertain adjudication replayed");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "request_adjudication_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), approvals, rejections, destinations: ids.length })}\n`,
  );
}
