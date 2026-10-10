import {
  capture as captureFrontend,
  hasCaptureOwner,
} from "../frontend/capture.ts";
import AxeBuilder from "@axe-core/playwright";
import { captureStateFeedback } from "./state-feedback.ts";
import { captureDetailLayout, captureTableState } from "./detail-layout.ts";
import { assertTableConventions } from "./table-conventions.ts";
import { expect, type BrowserContext, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { decodeAuditPage } from "../../src/audit-contract.ts";
import { assertSecretAbsent, fail, waitForLifecycle } from "./shared.ts";

export async function runAudit(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
  evidence: "api" | "presentation" = "api",
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  await page.getByTestId("admin-bearer-input").fill(bearer);
  await page.getByTestId("sign-in-submit").click();
  await waitForLifecycle(page, "authenticated");
  if (evidence === "api") {
    const realResponse = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === "/api/v2/audit-events" &&
        response.status() === 200,
    );
    await page.locator('a[href="#/audit-log"]').click();
    await expect(page.getByTestId("audit-row").first()).toBeVisible();
    const real = await (await realResponse).json();
    const realPage = decodeAuditPage(real);
    if (realPage.items.length === 0)
      fail("Real audit API had no produced events");
    await assertSecretAbsent(page, context, baseURL, [bearer], true);
    process.stdout.write(
      JSON.stringify({
        event: "audit_complete",
        chromium_version: browserVersion,
        playwright_version: "1.62.1",
        requests: requestCount(),
        real_api: true,
        recorded_events: realPage.items.length,
        evidence,
      }) + "\n",
    );
    return;
  }
  const generation = "a".repeat(64);
  const replacement =
    generation === "b".repeat(64) ? "c".repeat(64) : "b".repeat(64);
  const id = (n: number) => `0000000000000000000000000${n}`;
  const fixture = (n: number, actor = "system") => ({
    id: id(n),
    sequence: String(n),
    timestamp: "2026-09-05T00:00:00.000000000Z",
    category: actor === "offline_maintenance" ? "backup" : "server",
    action: actor === "offline_maintenance" ? "restore" : "reconcile",
    phase: "outcome",
    outcome: "unknown",
    actor: {
      type: actor,
      credential:
        actor === "operator"
          ? { id: id(9), fingerprint: "0123456789abcdef" }
          : null,
    },
    initiator:
      actor === "system"
        ? { id: id(9), fingerprint: "0123456789abcdef" }
        : null,
    correlation_id: id(8),
    target: { type: "server", id: id(7) },
  });
  const history = (gen = generation) => ({
    generation: gen,
    oldest_retained: {
      id: id(1),
      sequence: "1",
      timestamp: "2026-09-04T00:00:00.000000000Z",
    },
    pruned: true,
  });
  let mode = "normal";
  const recognition = (items: { target: { type: string; id: string } }[]) => [
    ...new Map(
      items.map((item) => [
        `${item.target.type}/${item.target.id}`,
        {
          target: item.target,
          display_name:
            item.target.type === "server" && mode !== "target-deleted"
              ? mode === "target-current"
                ? "Renamed workshop server"
                : "Workshop server"
              : null,
        },
      ]),
    ).values(),
  ];
  let restarted = false;
  let hold: (() => void) | undefined;
  let holdTarget: (() => void) | undefined;
  const queries: URLSearchParams[] = [];
  let itemReads = 0;
  let expectedItemGeneration: string | null = generation;
  let delayedSettled = false;
  await page.route("**/api/v2/audit-events**", async (route) => {
    const request = route.request();
    if (
      request.method() !== "GET" ||
      request.postData() !== null ||
      (await request.allHeaders())["x-csrf-token"] === undefined
    )
      fail("Audit escaped authenticated bodyless read owner");
    const url = new URL(request.url());
    const problem = async (status: number, code: string) =>
      route.fulfill({
        status,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status,
          code,
          title: "The audit read is unavailable.",
        }),
      });
    if (url.pathname !== "/api/v2/audit-events") {
      itemReads++;
      if (url.searchParams.get("generation") !== expectedItemGeneration)
        fail("Audit detail did not pin history generation");
      if (mode === "missing") return problem(404, "not_found");
      if (mode === "item-replaced")
        return problem(409, "audit_history_replaced");
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          event: {
            ...fixture(3),
            detail: { reason: "interrupted", problem: null },
          },
          history: history(),
          target_recognition: recognition([fixture(3)]),
        }),
      });
    }
    queries.push(url.searchParams);
    if (url.searchParams.get("limit") !== "50")
      fail("Audit list page bound changed");
    if (mode === "delayed") {
      await new Promise<void>((resolve) => {
        hold = resolve;
      });
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [fixture(6)],
          next_cursor: null,
          history: history(),
        }),
      });
      delayedSettled = true;
      return;
    }
    if (mode === "failure") return problem(503, "storage_unavailable");
    if (mode === "loading")
      await new Promise<void>((resolve) => {
        hold = resolve;
      });
    if (mode === "stale" && url.searchParams.has("cursor")) {
      restarted = true;
      return problem(409, "stale_cursor");
    }
    if (mode === "replaced" && url.searchParams.get("generation") !== null)
      return problem(409, "audit_history_replaced");
    if (mode === "custody-setup") {
      const setup = {
        ...fixture(6, "offline_maintenance"),
        category: "keyring",
        action: "setup",
        outcome: "succeeded",
        target: { type: "installation", id: id(7) },
      };
      return route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [setup],
          next_cursor: null,
          history: history(),
          target_recognition: recognition([setup]),
        }),
      });
    }
    const items =
      mode === "presentation"
        ? ["pending", "succeeded", "failed", "rejected", "unknown"].map(
            (outcome, index) => ({
              ...fixture(5 - index),
              outcome,
              phase: outcome === "pending" ? "attempt" : "outcome",
              action: index === 1 ? "delete" : "reconcile",
              target: {
                type: [
                  "installation",
                  "server",
                  "principal",
                  "grant",
                  "grant_request",
                ][index]!,
                id: id(7),
              },
            }),
          )
        : mode === "selected-outside"
          ? [fixture(2, "offline_maintenance")]
          : mode === "empty" || mode === "loading"
            ? []
            : mode === "replaced"
              ? [fixture(5, "offline_maintenance")]
              : mode === "stale" && restarted
                ? [fixture(4)]
                : url.searchParams.has("cursor")
                  ? [fixture(1, "operator")]
                  : [fixture(3), fixture(2, "offline_maintenance")];
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items,
        next_cursor: items.length === 2 ? "opaque-page-2" : null,
        history: history(mode === "replaced" ? replacement : generation),
        target_recognition: recognition(items),
      }),
    });
  });
  await page.route(`**/api/v2/mcp/servers/${id(7)}`, async (route) => {
    if (mode === "target-delayed")
      await new Promise<void>((resolve) => {
        holdTarget = resolve;
      });
    if (mode === "target-missing")
      return route.fulfill({
        status: 404,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 404,
          code: "not_found",
          title: "Not found",
        }),
      });
    return mode === "target-current" || mode === "target-deleted"
      ? route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            id: id(7),
            deleted_at:
              mode === "target-deleted" ? "2026-09-01T00:00:00Z" : null,
          }),
        })
      : route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 503,
            code: "storage_unavailable",
            title: "Storage is unavailable.",
          }),
        });
  });
  const refresh = async () =>
    page
      .getByRole("button", { name: "Refresh current view", exact: true })
      .click();
  await page.locator('a[href="#/audit-log"]').click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(
    page.getByText("Older events pruned", { exact: true }),
  ).toBeVisible();
  const primary = page.getByRole("navigation", {
    name: "Primary",
    exact: true,
  });
  await expect(
    primary.getByRole("group", { name: "Activity", exact: true }),
  ).toHaveCount(0);
  await expect(
    primary.getByRole("link", { name: "Audit Log", exact: true }),
  ).toHaveAttribute("aria-current", "page");
  const artifacts = await mkdtemp(join(tmpdir(), "gateway-audit-visual-"));
  const screenshots: string[] = [];
  const capture = async (name: string, width: number, fullPage = true) => {
    await captureFrontend(page, name);
    await page.setViewportSize({ width, height: 900 });
    const path = join(artifacts, `${name}.png`);
    if (!fullPage)
      await page
        .getByText("Retention details", { exact: true })
        .scrollIntoViewIfNeeded();
    await page.screenshot({ path, fullPage, animations: "disabled" });
    screenshots.push(path);
    if (
      [
        "invalid-filters",
        "partial-error",
        "unfiltered-empty",
        "filtered-empty",
        "loading",
      ].includes(name)
    )
      await captureTableState(page, `audit-${name}`);
    if (
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth,
      )
    )
      fail("Audit document overflow");
  };
  await assertTableConventions(
    page,
    "Control-plane audit history",
    ["Time", "Event", "Performer", "Target type", "Target", "Outcome"],
    "Event",
  );
  expect(
    await page
      .getByRole("combobox", { name: "Event", exact: true })
      .evaluate((control) => control.getBoundingClientRect().height),
  ).toBeGreaterThanOrEqual(38);
  await expect(
    page.getByTestId("audit-row").first().locator('[data-label="Event"]'),
  ).toContainText("server.reconcile");
  await expect(
    page.getByTestId("audit-row").first().locator('[data-label="Target"]'),
  ).toContainText("Workshop server");
  await capture("desktop-list", 1440);
  await capture("narrow-list", 390);
  await capture("small-list", 320);
  const tableRegion = page.getByRole("region", {
    name: "Control-plane audit history",
    exact: true,
  });
  await tableRegion.focus();
  await expect(tableRegion).toBeFocused();
  expect(
    await tableRegion.evaluate(
      (element) => element.scrollWidth <= element.clientWidth,
    ),
  ).toBe(true);
  await expect(
    page
      .getByTestId("audit-row")
      .first()
      .locator('[data-label="Event"] .table-identifier'),
  ).toHaveText(id(3));
  expect(await page.locator(".audit-view thead th").allTextContents()).toEqual([
    "Time",
    "Event",
    "Performer",
    "Target type",
    "Target",
    "Outcome",
  ]);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.clock.install();
  const live = page.getByRole("switch", { name: "Live mode", exact: true });
  await expect(live).toBeChecked();
  await live.focus();
  await page.keyboard.press("Space");
  await expect(live).not.toBeChecked();
  const pausedReads = queries.length;
  await page.clock.fastForward(31000);
  expect(queries.length).toBe(pausedReads);
  await capture("live-off", 390);
  await live.click();
  await expect.poll(() => queries.length).toBeGreaterThan(pausedReads);
  const liveReads = queries.length;
  await page.clock.fastForward(31000);
  await expect.poll(() => queries.length).toBeGreaterThan(liveReads);
  await page.getByRole("button", { name: "Load older events" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(3);
  const continuation = queries.at(-1)!;
  if (
    continuation.get("cursor") !== "opaque-page-2" ||
    continuation.get("generation") !== generation
  )
    fail("Audit pagination transport lost its pin");
  await expect(
    page.getByText("Live paused while viewing older results", { exact: true }),
  ).toBeVisible();
  const olderReads = queries.length;
  await page.clock.fastForward(31000);
  expect(queries.length).toBe(olderReads);
  await capture("older-paused", 1440);
  await page.locator(`a[href="#/audit-log/${id(3)}"]`).click();
  await page
    .getByRole("link", { name: "Back to Audit Log", exact: true })
    .click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(
    page.getByText(
      "Showing the latest entries. Older results could not be continued.",
      { exact: true },
    ),
  ).toBeVisible();
  await capture("detail-return", 390);
  await page.getByRole("button", { name: "Resume live", exact: true }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await page.locator(`a[href="#/audit-log/${id(3)}"]`).click();
  await page.goBack();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(
    page.getByText(
      "Showing the latest entries. Older results could not be continued.",
      { exact: true },
    ),
  ).toHaveCount(0);
  await page.clock.resume();
  await page.getByLabel("Outcome", { exact: true }).selectOption("unknown");
  await expect.poll(() => queries.at(-1)?.get("outcome")).toBe("unknown");
  mode = "target-delayed";
  await page
    .locator(`a[href="#/audit-log/${id(3)}?filter_outcome=unknown"]`)
    .focus();
  await page.keyboard.press("Enter");
  await expect(page.locator("h1")).toBeFocused();
  await expect(page.locator("h1")).toHaveText("Reconcile server");
  await expect(page.getByText("interrupted", { exact: true })).toBeVisible();
  await expect.poll(() => holdTarget !== undefined).toBe(true);
  await expect(page.getByText(/The outcome is unconfirmed/)).toBeVisible();
  await expect(
    page.getByText(/Only allowlisted safe detail is retained/),
  ).toHaveCount(0);
  await captureStateFeedback(page, "audit-detail");
  if (holdTarget === undefined) fail("Target barrier not reached");
  holdTarget();
  await expect(
    page.getByText("Current target unavailable", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Initiating credential", { exact: true }),
  ).toBeVisible();
  const related = page.getByRole("region", {
    name: "Related events",
    exact: true,
  });
  await expect(related.getByRole("table")).toContainText("Selected event");
  await expect(related.getByRole("table")).toContainText("Workshop server");
  await expect(related.locator(`a[href="#/mcp/servers/${id(7)}"]`)).toHaveCount(
    0,
  );
  await assertTableConventions(
    page,
    "Related control-plane events",
    ["Time", "Event", "Performer", "Target type", "Target", "Outcome"],
    "Event",
  );
  await expect(related.getByText(/Newest recorded sequence first/)).toHaveCount(
    0,
  );
  const technical = page.getByText("Correlation ID", { exact: true });
  await expect(technical).toBeVisible();
  await expect(
    page.getByText("Technical details", { exact: true }),
  ).toHaveCount(0);
  const technicalBox = await technical.boundingBox();
  const relatedBox = await related
    .getByRole("heading", { name: "Related events", exact: true })
    .boundingBox();
  expect(
    relatedBox!.y - (technicalBox!.y + technicalBox!.height),
  ).toBeGreaterThanOrEqual(24);
  await expect.poll(() => queries.at(-1)?.get("correlation_id")).toBe(id(8));
  const olderBox = await related
    .getByRole("button", { name: "Load older events" })
    .boundingBox();
  const relatedTableBox = await related.getByRole("table").boundingBox();
  expect(Math.abs(olderBox!.x - relatedTableBox!.x)).toBeLessThanOrEqual(2);
  const loadedBox = await related
    .getByText("2 events loaded", { exact: true })
    .boundingBox();
  expect(loadedBox!.x).toBeGreaterThan(olderBox!.x + olderBox!.width);
  await related.getByRole("button", { name: "Load older events" }).click();
  await expect(related.getByRole("row")).toHaveCount(4);
  expect(queries.at(-1)?.get("cursor")).toBe("opaque-page-2");
  mode = "failure";
  await related.getByRole("button", { name: "Refresh related events" }).click();
  await expect(
    related.getByText("Related events unavailable", { exact: true }),
  ).toBeVisible();
  await expect(page.locator("h1")).toHaveText("Reconcile server");
  await expect(related.getByRole("row")).toHaveCount(4);
  await expect(
    related.getByText("3 events loaded (stale)", { exact: true }),
  ).toBeVisible();
  await captureTableState(page, "audit-related-stale");
  mode = "normal";
  await related.getByRole("button", { name: "Refresh related events" }).click();
  await expect(
    related.getByText("Related events unavailable", { exact: true }),
  ).toHaveCount(0);
  await expect(related.getByRole("row")).toHaveCount(3);
  mode = "selected-outside";
  await related.getByRole("button", { name: "Refresh related events" }).click();
  await expect(
    related.getByText("1 event loaded", { exact: true }),
  ).toBeVisible();
  await expect(
    related.getByText("Plus the selected event", { exact: true }),
  ).toBeVisible();
  await expect(related.getByRole("row")).toHaveCount(3);
  await expect(
    related.getByText("Selected event", { exact: true }),
  ).toHaveCount(1);
  await expect(
    page.getByRole("region", { name: "Audit retention and continuity" }),
  ).toHaveCount(0);
  await expect(page.getByText("Sequence / phase", { exact: true })).toHaveCount(
    0,
  );
  await expect(page.getByText("Problem", { exact: true })).toHaveCount(0);
  await expect(related.locator(`a[href="#/mcp/servers/${id(7)}"]`)).toHaveCount(
    0,
  );
  await captureFrontend(page, "table-audit-selected-outside", true);
  mode = "normal";
  await related.getByRole("button", { name: "Refresh related events" }).click();
  await expect(
    related.getByText("Plus the selected event", { exact: true }),
  ).toHaveCount(0);
  await captureDetailLayout(page, "audit-event-related-populated");
  await capture("desktop-detail", 1440);
  await capture("narrow-detail", 390);
  await page.setViewportSize({ width: 1440, height: 900 });
  if (!hasCaptureOwner(page)) {
    const axe = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
      .analyze();
    if (
      axe.violations.some(
        (finding) =>
          finding.impact === "serious" || finding.impact === "critical",
      )
    )
      fail(
        `Audit accessibility: ${axe.violations.map((finding) => finding.id).join(",")}`,
      );
  }
  mode = "normal";
  await page.getByRole("link", { name: "Back to Audit Log" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(page.locator(`a[href="#/mcp/servers/${id(7)}"]`)).toHaveCount(0);
  await page.goBack();
  await expect(page.locator("h1")).toHaveText("Reconcile server");
  await expect(
    page.getByText("Current target unavailable", { exact: true }),
  ).toBeVisible();
  mode = "target-current";
  await refresh();
  await expect(
    page
      .getByRole("region", { name: "Audit event detail", exact: true })
      .locator(`a[href="#/mcp/servers/${id(7)}"]`),
  ).toBeVisible();
  await expect(
    page.getByText("Current target unavailable", { exact: true }),
  ).toHaveCount(0);
  for (const targetMode of ["target-deleted", "target-missing"]) {
    mode = targetMode;
    const targetResponse = page.waitForResponse(
      (response) =>
        new URL(response.url()).pathname === `/api/v2/mcp/servers/${id(7)}`,
    );
    await refresh();
    await targetResponse;
    await expect(
      page
        .getByRole("region", { name: "Audit event detail", exact: true })
        .locator(`a[href="#/mcp/servers/${id(7)}"]`),
    ).toHaveCount(0);
    await expect(
      related.locator(`a[href="#/mcp/servers/${id(7)}"]`),
    ).toHaveCount(0);
    await expect(
      page.getByRole("heading", { name: "Reconcile server", exact: true }),
    ).toBeVisible();
  }
  mode = "missing";
  await refresh();
  await expect(
    page.getByText("Audit event not retained", { exact: true }),
  ).toBeVisible();
  mode = "item-replaced";
  await refresh();
  await expect(
    page.getByText("Previous-history detail discarded", { exact: true }),
  ).toBeVisible();
  mode = "normal";
  await page.getByRole("link", { name: "Back to Audit Log" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(page).toHaveURL(/#\/audit-log\?filter_outcome=unknown$/);
  await expect(page.locator(`a[href="#/mcp/servers/${id(7)}"]`)).toHaveCount(0);
  await page.getByRole("button", { name: "Reset", exact: true }).click();
  await expect(page.getByText("More filters", { exact: true })).toHaveCount(0);
  const target = page.getByLabel("Target", { exact: true });
  await expect(target).toBeVisible();
  await expect(target).toHaveAttribute("placeholder", "Name or ID");
  for (const width of [1440, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    const height = await target.evaluate(
      (node) => node.getBoundingClientRect().height,
    );
    expect(
      height,
      "compact target input must not inherit a vertical flex basis",
    ).toBeLessThanOrEqual(42);
    expect(height).toBeGreaterThanOrEqual(36);
  }
  await page.setViewportSize({ width: 1440, height: 900 });
  await expect(
    page.getByText("Current names or literal partial IDs.", { exact: true }),
  ).toHaveCount(0);
  const filters = page.getByRole("form", { name: "Filter audit history" });
  await expect(filters.getByRole("combobox")).toHaveCount(4);
  await expect(filters.getByRole("textbox")).toHaveCount(1);
  expect(
    await filters
      .locator("label")
      .evaluateAll((labels) =>
        labels.every((label) => label.getBoundingClientRect().height <= 1),
      ),
  ).toBe(true);
  for (const select of await filters.getByRole("combobox").all()) {
    await expect(select.locator("option:checked")).toContainText(": any");
  }
  await expect(page.locator('input[type="datetime-local"]')).toHaveCount(0);
  await expect(page.getByLabel("Credential ID", { exact: true })).toHaveCount(
    0,
  );
  await expect.poll(() => queries.at(-1)?.has("outcome")).toBe(false);
  const beforeInvalid = queries.length;
  await target.fill("é".repeat(129));
  await expect(
    page.getByText("Use at most 256 UTF-8 bytes without control characters.", {
      exact: true,
    }),
  ).toBeVisible();
  expect(queries.slice(beforeInvalid)).toEqual([]);
  await page.getByLabel("Event", { exact: true }).selectOption("server.*");
  await expect.poll(() => queries.at(-1)?.get("category")).toBe("server");
  expect(queries.at(-1)?.has("from")).toBe(false);
  expect(queries.at(-1)?.has("target_id")).toBe(false);
  await expect(target).toHaveValue("é".repeat(129));
  await page
    .getByLabel("Event", { exact: true })
    .selectOption("server.reconcile");
  await page.getByLabel("Event", { exact: true }).selectOption("principal.*");
  await expect(page.getByLabel("Event", { exact: true })).toHaveValue(
    "principal.*",
  );
  await expect.poll(() => queries.at(-1)?.get("category")).toBe("principal");
  expect(queries.at(-1)?.has("action")).toBe(false);
  await expect(page.getByLabel("Action", { exact: true })).toHaveCount(0);
  await capture("invalid-filters", 320);
  await target.fill("");
  await expect(target).toBeVisible();
  await expect(target).toBeFocused();
  await expect.poll(() => queries.at(-1)?.has("target")).toBe(false);
  await expect(page.getByText(/Draft changes are not yet applied/)).toHaveCount(
    0,
  );
  await capture("removed-invalid-filter", 390);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.evaluate(() => {
    window.location.hash =
      "#/audit-log?filter_from=2026-01-01T23%3A00%3A00.123456789Z&filter_until=2026-01-02T23%3A00%3A00.123456789Z";
  });
  await expect(page).toHaveURL(/#\/audit-log$/);
  await expect.poll(() => queries.at(-1)?.has("from")).toBe(false);
  expect(queries.at(-1)?.has("until")).toBe(false);
  await page.evaluate((credential) => {
    location.hash = `#/audit-log?filter_credential_id=${credential}&filter_target_id=${credential}`;
  }, id(7));
  await expect(target).toHaveValue(id(7));
  await expect.poll(() => queries.at(-1)?.get("target")).toBe(id(7));
  expect(queries.at(-1)?.has("credential_id")).toBe(false);
  expect(queries.at(-1)?.has("target_id")).toBe(false);
  await page.getByRole("button", { name: "Reset" }).click();
  for (const label of [
    "Event",
    "Performer",
    "Target type",
    "Target",
    "Outcome",
  ]) {
    await page.getByLabel(label, { exact: true }).focus();
    await expect(page.getByLabel(label, { exact: true })).toBeFocused();
  }
  await expect(
    page.getByRole("combobox", { name: "Add filter", exact: true }),
  ).toHaveCount(0);
  await page.getByLabel("Performer", { exact: true }).selectOption("system");
  await expect.poll(() => queries.at(-1)?.get("actor_type")).toBe("system");
  const beforeText = queries.length;
  await target.fill("workshpo");
  expect(
    queries.slice(beforeText).map((query) => query.toString()),
    "Target filtering must be debounced",
  ).toEqual([]);
  await expect.poll(() => queries.at(-1)?.get("target")).toBe("workshpo");
  await page
    .getByLabel("Event", { exact: true })
    .selectOption("server.reconcile");
  await page.getByLabel("Target type", { exact: true }).selectOption("server");
  await target.fill(id(7).slice(-8));
  await expect.poll(() => queries.at(-1)?.get("target")).toBe(id(7).slice(-8));
  await page.getByLabel("Outcome", { exact: true }).selectOption("unknown");
  await page.evaluate((correlation) => {
    location.hash += `&filter_correlation_id=${correlation}`;
  }, id(8));
  await expect.poll(() => queries.at(-1)?.get("correlation_id")).toBe(id(8));
  await page
    .getByRole("button", { name: "Remove correlation filter", exact: true })
    .click();
  await expect.poll(() => queries.at(-1)?.has("correlation_id")).toBe(false);
  await expect(page.getByLabel("Correlation ID", { exact: true })).toHaveCount(
    0,
  );
  await page.goBack();
  await expect(
    page.getByRole("button", {
      name: "Remove correlation filter",
      exact: true,
    }),
  ).toBeVisible();
  await expect.poll(() => queries.at(-1)?.get("correlation_id")).toBe(id(8));
  mode = "loading";
  await target.fill("Renamed workshop");
  await expect(
    page.getByText("Loading audit history", { exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("audit-row")).toHaveCount(0);
  // Rendering the loading state can precede Playwright observing the request.
  await expect.poll(() => hold !== undefined).toBe(true);
  if (hold === undefined) fail("Audit loading barrier not reached");
  await capture("loading", 1440);
  hold();
  await expect(
    page.getByText("No matching audit events", { exact: true }),
  ).toBeVisible();
  await capture("empty", 1440);
  for (const key of [
    "actor_type",
    "category",
    "action",
    "target_type",
    "target",
    "outcome",
    "correlation_id",
  ])
    if (!queries.at(-1)!.has(key))
      fail(`Audit authoritative filter missing: ${key}`);
  expect(queries.at(-1)!.get("target")).toBe("Renamed workshop");
  for (const key of ["from", "until", "credential_id", "target_id"])
    expect(queries.at(-1)!.has(key)).toBe(false);
  if (queries.at(-1)!.has("cursor")) fail("Query change retained cursor");
  mode = "normal";
  await page.getByRole("button", { name: "Reset" }).first().click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  mode = "delayed";
  hold = undefined;
  await page.getByLabel("Outcome", { exact: true }).selectOption("failed");
  await expect(
    page.getByText("Loading audit history", { exact: true }),
  ).toBeVisible();
  await expect.poll(() => hold !== undefined).toBe(true);
  mode = "normal";
  await page.getByRole("button", { name: "Reset" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  (hold as unknown as () => void)();
  await expect.poll(() => delayedSettled).toBe(true);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
  await expect(page.locator(`a[href="#/audit-log/${id(6)}"]`)).toHaveCount(0);
  mode = "failure";
  await refresh();
  await expect(
    page.getByText("Audit read unavailable", { exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(
    page.getByText(/Previously loaded evidence is stale and may be partial/),
  ).toBeVisible();
  await capture("partial-error", 1440);
  mode = "presentation";
  await refresh();
  await expect(page.getByTestId("audit-row")).toHaveCount(5);
  expect(
    await page
      .getByTestId("audit-row")
      .locator(".status-label")
      .evaluateAll((labels) =>
        labels.map((label) => label.getAttribute("data-state")),
      ),
  ).toEqual(["neutral", "current", "error", "neutral", "warning"]);
  await expect(
    page.getByTestId("audit-row").first().locator('[data-label="Outcome"]'),
  ).toHaveText("Attempted — unconfirmed");
  await expect(
    page.getByTestId("audit-row").locator('[data-label="Performer"]'),
  ).not.toContainText([id(9), id(9), id(9), id(9), id(9)]);
  for (const route of ["agents", "mcp/grants", "mcp/access-requests"])
    await expect(page.locator(`a[href="#/${route}/${id(7)}"]`)).toHaveCount(0);
  await expect(
    page.getByTestId("audit-row").nth(0).locator('[data-label="Target"] a'),
  ).toHaveCount(0);
  await expect(
    page.getByTestId("audit-row").nth(1).locator('[data-label="Target"] a'),
  ).toHaveCount(0);
  await capture("outcomes-targets", 1440);
  await page.getByTestId("theme-preference").selectOption("dark");
  await capture("outcomes-targets-dark", 390);
  await page.getByTestId("theme-preference").selectOption("system");
  mode = "custody-setup";
  await page.getByLabel("Event", { exact: true }).selectOption("keyring.setup");
  await expect.poll(() => queries.at(-1)?.get("category")).toBe("keyring");
  await expect.poll(() => queries.at(-1)?.get("action")).toBe("setup");
  await expect(page.getByTestId("audit-row")).toHaveCount(1);
  await expect(
    page.getByTestId("audit-row").locator('[data-label="Event"]'),
  ).toContainText("keyring.setup");
  await expect(
    page.getByTestId("audit-row").locator('[data-label="Outcome"]'),
  ).toHaveText("Succeeded");
  await capture("encrypted-custody-setup", 1440);
  await page.getByRole("button", { name: "Reset", exact: true }).click();
  await expect(page.getByLabel("Event", { exact: true })).toHaveValue("");
  mode = "empty";
  await refresh();
  await expect(
    page.getByText("No audit events yet", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Reset" })).toBeDisabled();
  await capture("unfiltered-empty", 320);
  await page.getByLabel("Outcome", { exact: true }).selectOption("failed");
  await expect(
    page.getByText("No matching audit events", { exact: true }),
  ).toBeVisible();
  await capture("filtered-empty", 390);
  await page.getByRole("button", { name: "Reset" }).last().click();
  await expect(
    page.getByText("No audit events yet", { exact: true }),
  ).toBeVisible();
  await page.goBack();
  await expect(page.getByLabel("Outcome", { exact: true })).toHaveValue(
    "failed",
  );
  await page.goForward();
  await expect(page.getByLabel("Outcome", { exact: true })).toHaveValue("");
  mode = "stale";
  await refresh();
  await expect(
    page.getByText("Audit read unavailable", { exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Load older events" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(1);
  await expect(
    page.getByText(
      "Showing the latest entries. Older results could not be continued.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(page.locator(`a[href="#/audit-log/${id(4)}"]`)).toBeVisible();
  mode = "replaced";
  await refresh();
  await expect(
    page.getByText(/Newer local events may have been discarded/),
  ).toBeVisible();
  await expect(page.getByTestId("audit-row")).toHaveCount(1);
  await expect(page.locator(`a[href="#/audit-log/${id(5)}"]`)).toBeVisible();
  await expect(page.locator(`a[href="#/audit-log/${id(4)}"]`)).toHaveCount(0);
  await capture("continuity-warning", 1440);
  await page.getByText("Retention details", { exact: true }).focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByText("History generation", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Retention details", { exact: true }),
  ).toBeFocused();
  await captureDetailLayout(page, "audit-retention-expanded");
  await capture("retention-expanded", 320, false);
  await assertSecretAbsent(page, context, baseURL, [bearer], true, "system");
  mode = "delayed";
  delayedSettled = false;
  hold = undefined;
  await refresh();
  await expect.poll(() => hold !== undefined).toBe(true);
  const logoutResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v2/admin-sessions/current" &&
      response.request().method() === "DELETE",
    { timeout: 5000 },
  );
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page
    .getByRole("dialog", { name: "Sign out of this browser session?" })
    .getByRole("button", { name: "Sign out", exact: true })
    .click();
  await waitForLifecycle(page, "signed_out");
  // Local epoch clearing precedes the response that expires the HttpOnly cookie.
  (hold as unknown as () => void)();
  const logout = await logoutResponse;
  expect(logout.status()).toBe(204);
  await expect
    .poll(
      async () =>
        (await context.cookies(baseURL)).filter(
          (cookie) => cookie.name === "agent_gateway_session",
        ).length,
    )
    .toBe(0);
  await expect.poll(() => delayedSettled).toBe(true);
  await expect(page.getByTestId("audit-view")).toHaveCount(0);
  await assertSecretAbsent(page, context, baseURL, [bearer], false, "system");
  mode = "normal";
  expectedItemGeneration = null;
  await page.getByTestId("admin-bearer-input").fill(bearer);
  await page.getByTestId("sign-in-submit").click();
  await waitForLifecycle(page, "authenticated");
  // Authentication precedes the event-stream reconnect refresh; let it start
  // before navigation so the exact item-read assertions measure this action.
  await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
    "data-freshness",
    "current",
  );
  await page.evaluate((id) => {
    window.location.hash = `#/audit-log/${id}?filter_outcome=unknown`;
  }, id(3));
  await expect(page.getByText("interrupted", { exact: true })).toBeVisible();
  await expect(
    page.getByText("Current target unavailable", { exact: true }),
  ).toBeVisible();
  await page.getByRole("link", { name: "Back to Audit Log" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(page.getByLabel("Outcome", { exact: true })).toHaveValue(
    "unknown",
  );
  expect(queries.at(-1)?.has("cursor")).toBe(false);
  await expect(
    page.getByText(/Newer local events may have been discarded/),
  ).toHaveCount(0);
  await assertSecretAbsent(page, context, baseURL, [bearer], true, "system");
  process.stdout.write(
    JSON.stringify({
      event: "audit_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      list_reads: queries.length,
      item_reads: itemReads,
      real_api: false,
      screenshots,
    }) + "\n",
  );
}
