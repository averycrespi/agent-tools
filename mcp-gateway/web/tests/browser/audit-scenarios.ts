import AxeBuilder from "@axe-core/playwright";
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
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  await page.getByTestId("admin-bearer-input").fill(bearer);
  await page.getByTestId("sign-in-submit").click();
  await waitForLifecycle(page, "authenticated");
  const realResponse = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname === "/api/v1/audit-events" &&
      response.status() === 200,
  );
  await page.locator('a[href="#/audit"]').click();
  await expect(page.getByTestId("audit-row").first()).toBeVisible();
  const real = await (await realResponse).json();
  const realPage = decodeAuditPage(real);
  if (realPage.items.length === 0)
    fail("Real audit API had no produced events");
  const generation = realPage.history.generation;
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
  let restarted = false;
  let hold: (() => void) | undefined;
  let holdTarget: (() => void) | undefined;
  const queries: URLSearchParams[] = [];
  let itemReads = 0;
  let delayedSettled = false;
  await page.route("**/api/v1/audit-events**", async (route) => {
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
    if (url.pathname !== "/api/v1/audit-events") {
      itemReads++;
      if (url.searchParams.get("generation") !== generation)
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
    const items =
      mode === "empty" || mode === "loading"
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
      }),
    });
  });
  await page.route(`**/api/v1/servers/${id(7)}`, async (route) => {
    if (mode === "target-delayed")
      await new Promise<void>((resolve) => {
        holdTarget = resolve;
      });
    return mode === "target-current"
      ? route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ id: id(7), deleted_at: null }),
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
  await refresh();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  await expect(
    page.getByText("Older events pruned", { exact: true }),
  ).toBeVisible();
  const nav = await page
    .locator(".sidebar a")
    .evaluateAll((links) => links.map((a) => a.textContent));
  if (nav.length > 0 && nav.indexOf("Audit") + 1 !== nav.indexOf("System"))
    fail("Audit sidebar order changed");
  const artifacts = await mkdtemp(join(tmpdir(), "gateway-audit-visual-"));
  const screenshots: string[] = [];
  const capture = async (name: string, width: number) => {
    await page.setViewportSize({ width, height: 900 });
    const path = join(artifacts, `${name}.png`);
    await page.screenshot({ path, fullPage: true, animations: "disabled" });
    screenshots.push(path);
    if (
      await page.evaluate(
        () =>
          document.documentElement.scrollWidth >
          document.documentElement.clientWidth,
      )
    )
      fail("Audit document overflow");
  };
  await capture("desktop-list", 1440);
  await capture("narrow-list", 390);
  await capture("small-list", 320);
  const tableRegion = page.getByRole("region", {
    name: "Control-plane audit history",
    exact: true,
  });
  await tableRegion.focus();
  await page.keyboard.press("ArrowRight");
  await expect
    .poll(() => tableRegion.evaluate((element) => element.scrollLeft))
    .toBeGreaterThan(0);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("button", { name: "Load older audit events" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(3);
  const continuation = queries.at(-1)!;
  if (
    continuation.get("cursor") !== "opaque-page-2" ||
    continuation.get("generation") !== generation
  )
    fail("Audit pagination transport lost its pin");
  mode = "target-delayed";
  await page.locator(`a[href="#/audit/${id(3)}"]`).focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("heading", { name: `Audit event ${id(3)}` }),
  ).toBeFocused();
  await expect(page.getByText("interrupted", { exact: true })).toBeVisible();
  await expect.poll(() => holdTarget !== undefined).toBe(true);
  if (holdTarget === undefined) fail("Target barrier not reached");
  holdTarget();
  await expect(
    page.getByText("Current target unavailable", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Initiating credential (not performer)", { exact: true }),
  ).toBeVisible();
  await capture("desktop-detail", 1440);
  await capture("narrow-detail", 390);
  await page.setViewportSize({ width: 1440, height: 900 });
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
  mode = "target-current";
  await refresh();
  await expect(page.locator(`a[href="#/servers/${id(7)}"]`)).toBeVisible();
  await expect(
    page.getByText("Current target unavailable", { exact: true }),
  ).toHaveCount(0);
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
  await page.getByRole("link", { name: "Back to audit history" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  const from = page.getByLabel("From (inclusive, local time)", { exact: true });
  const until = page.getByLabel("Until (exclusive, local time)", {
    exact: true,
  });
  await expect(from).toHaveAttribute("type", "datetime-local");
  await expect(until).toHaveAttribute("type", "datetime-local");
  await expect(
    page.getByText(
      "Time bounds must both use nine fractional UTC digits and span at most 366 days.",
      { exact: true },
    ),
  ).toHaveCount(0);
  await expect(
    page.getByText(
      "Separate from Invocation History and Requests. Use Refresh to read the newest events.",
      { exact: true },
    ),
  ).toHaveCount(0);
  const beforeInvalid = queries.length;
  await from.fill("2026-09-03T20:00");
  for (const [value, message] of [
    ["", "Choose both From and Until, or clear both."],
    ["2026-09-03T19:00", "Until must be later than From."],
    ["2028-09-03T20:00", "Choose a time range of at most 366 days."],
  ]) {
    await until.fill(value!);
    await page.getByRole("button", { name: "Apply filters" }).click();
    await expect(page.getByText(message!, { exact: true })).toBeVisible();
    expect(queries.length).toBe(beforeInvalid);
  }
  await page.evaluate(() => {
    window.location.hash =
      "#/audit?filter_from=2026-01-01T23%3A00%3A00.123456789Z&filter_until=2026-01-02T23%3A00%3A00.123456789Z";
  });
  await expect(from).toHaveValue("2026-01-01T18:00");
  await expect(until).toHaveValue("2026-01-02T18:00");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect
    .poll(() => queries.at(-1)?.get("from"))
    .toBe("2026-01-01T23:00:00.123456789Z");
  await from.fill("2026-01-01T18:01");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect
    .poll(() => queries.at(-1)?.get("from"))
    .toBe("2026-01-01T23:01:00.000000000Z");
  expect(queries.at(-1)?.get("until")).toBe("2026-01-02T23:00:00.123456789Z");
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(from).toHaveValue("");
  await expect(until).toHaveValue("");
  await expect.poll(() => queries.at(-1)?.has("from")).toBe(false);
  await page.getByLabel("Actor type", { exact: true }).selectOption("system");
  await page.getByLabel("Credential ID", { exact: true }).fill(id(9));
  await page.getByLabel("Category", { exact: true }).selectOption("server");
  await page.getByLabel("Action", { exact: true }).selectOption("reconcile");
  await page.getByLabel("Target type", { exact: true }).selectOption("server");
  await page.getByLabel("Target ID", { exact: true }).fill(id(7));
  await page.getByLabel("Outcome", { exact: true }).selectOption("unknown");
  await page.getByLabel("Correlation ID", { exact: true }).fill(id(8));
  await from.fill("2026-09-03T20:00");
  await until.fill("2026-09-05T20:00");
  mode = "loading";
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(
    page.getByText("Loading audit history", { exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("audit-row")).toHaveCount(0);
  if (hold === undefined) fail("Audit loading barrier not reached");
  await capture("loading", 1440);
  hold();
  await expect(
    page.getByText("No retained audit events match", { exact: true }),
  ).toBeVisible();
  await capture("empty", 1440);
  for (const key of [
    "actor_type",
    "credential_id",
    "category",
    "action",
    "target_type",
    "target_id",
    "outcome",
    "correlation_id",
    "from",
    "until",
  ])
    if (!queries.at(-1)!.has(key))
      fail(`Audit authoritative filter missing: ${key}`);
  expect(queries.at(-1)!.get("from")).toBe("2026-09-04T00:00:00.000000000Z");
  expect(queries.at(-1)!.get("until")).toBe("2026-09-06T00:00:00.000000000Z");
  await expect(from).toHaveValue("2026-09-03T20:00");
  await expect(until).toHaveValue("2026-09-05T20:00");
  if (queries.at(-1)!.has("cursor")) fail("Query change retained cursor");
  mode = "normal";
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  mode = "delayed";
  hold = undefined;
  await page.getByLabel("Outcome", { exact: true }).selectOption("failed");
  await page.getByRole("button", { name: "Apply filters" }).click();
  await expect(
    page.getByText("Loading audit history", { exact: true }),
  ).toBeVisible();
  await expect.poll(() => hold !== undefined).toBe(true);
  mode = "normal";
  await page.getByRole("button", { name: "Clear filters" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(2);
  (hold as unknown as () => void)();
  await expect.poll(() => delayedSettled).toBe(true);
  await page.evaluate(
    () =>
      new Promise<void>((resolve) =>
        requestAnimationFrame(() => requestAnimationFrame(() => resolve())),
      ),
  );
  await expect(page.locator(`a[href="#/audit/${id(6)}"]`)).toHaveCount(0);
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
  mode = "stale";
  await refresh();
  await expect(
    page.getByText("Audit read unavailable", { exact: true }),
  ).toHaveCount(0);
  await page.getByRole("button", { name: "Load older audit events" }).click();
  await expect(page.getByTestId("audit-row")).toHaveCount(1);
  await expect(
    page.getByText(/previous traversal was discarded and restarted/),
  ).toBeVisible();
  await expect(page.locator(`a[href="#/audit/${id(4)}"]`)).toBeVisible();
  mode = "replaced";
  await refresh();
  await expect(
    page.getByText(/Newer local events may have been discarded/),
  ).toBeVisible();
  await expect(page.getByTestId("audit-row")).toHaveCount(1);
  await expect(page.locator(`a[href="#/audit/${id(5)}"]`)).toBeVisible();
  await expect(page.locator(`a[href="#/audit/${id(4)}"]`)).toHaveCount(0);
  await capture("continuity-warning", 1440);
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  mode = "delayed";
  delayedSettled = false;
  hold = undefined;
  await refresh();
  await expect.poll(() => hold !== undefined).toBe(true);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page
    .getByRole("dialog", { name: "Sign out of this browser session?" })
    .getByRole("button", { name: "Sign out", exact: true })
    .click();
  await waitForLifecycle(page, "signed_out");
  (hold as unknown as () => void)();
  await expect.poll(() => delayedSettled).toBe(true);
  await expect(page.getByTestId("audit-view")).toHaveCount(0);
  await assertSecretAbsent(page, context, baseURL, [bearer], false);
  process.stdout.write(
    JSON.stringify({
      event: "audit_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      list_reads: queries.length,
      item_reads: itemReads,
      real_api: true,
      screenshots,
    }) + "\n",
  );
}
