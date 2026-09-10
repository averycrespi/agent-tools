import {
  expect,
  request,
  type BrowserContext,
  type Page,
} from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fail, waitForLifecycle } from "./shared.ts";

// Real public requests seed history; no interception implements selection.
export async function assertAuthoritativeHistory(
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
): Promise<string[]> {
  const api = await request.newContext();
  try {
    const headers = { Authorization: `Bearer ${bearer}` };
    const directory = await mkdtemp(join(tmpdir(), "gateway-history-visual-"));
    const screenshots: string[] = [];
    const capture = async (state: string) => {
      for (const width of [1440, 390, 320]) {
        await page.setViewportSize({ width, height: 900 });
        const file = join(directory, `${state}-${width}.png`);
        await page.screenshot({ path: file, fullPage: true });
        screenshots.push(file);
        if (
          await page.evaluate(
            () =>
              document.documentElement.scrollWidth >
              document.documentElement.clientWidth,
          )
        )
          fail("History controls overflowed viewport");
      }
      await page.setViewportSize({ width: 1440, height: 900 });
    };
    let releaseInitial!: () => void;
    let initialStarted!: () => void;
    const initialBarrier = new Promise<void>((resolve) => {
      releaseInitial = resolve;
    });
    const initialRead = new Promise<void>((resolve) => {
      initialStarted = resolve;
    });
    const holdInitial = async (route: import("@playwright/test").Route) => {
      initialStarted();
      await initialBarrier;
      await route.fallback();
    };
    await page.route("**/api/v1/invocations?*", holdInitial);
    try {
      await page.evaluate(() => {
        window.location.hash = "#/invocations";
      });
      await initialRead;
      await expect(page.getByLabel("Tool", { exact: true })).toBeVisible();
      await expect(
        page.getByText("Loading invocations", { exact: true }),
      ).toBeVisible();
      await expect(
        page.getByText("No retained invocations", { exact: true }),
      ).toHaveCount(0);
      await capture("loading");
    } finally {
      releaseInitial();
    }
    await expect(
      page.getByText("No retained invocations", { exact: true }),
    ).toBeVisible();
    await expect(page.getByLabel("Outcome", { exact: true })).toBeVisible();
    await capture("initial-empty");
    await page.unroute("**/api/v1/invocations?*", holdInitial);
    await page.evaluate(() => {
      window.location.hash = "#/overview";
    });
    const creation = await api.post(`${baseURL}/api/v1/principals`, {
      headers,
      data: { display_name: "Café Investigator", visibility: "all" },
    });
    if (creation.status() !== 201) fail("History principal creation failed");
    const principalID: string = (await creation.json()).principal.id;
    let etag = creation.headers().etag!;
    const issued = await api.post(
      `${baseURL}/api/v1/principals/${principalID}/credential`,
      { headers: { ...headers, "If-Match": etag }, data: {} },
    );
    if (issued.status() !== 201) fail("History credential creation failed");
    etag = issued.headers().etag!;
    let agentBearer: string = (await issued.json()).bearer;
    let callID = 0;
    const call = async (name: string) => {
      const response = await api.post(`${baseURL}/mcp`, {
        headers: {
          Authorization: `Bearer ${agentBearer}`,
          "MCP-Protocol-Version": "2026-07-28",
          Accept: "application/json, text/event-stream",
        },
        data: {
          jsonrpc: "2.0",
          id: ++callID,
          method: "tools/call",
          params: {
            name,
            arguments: {},
            _meta: {
              "io.modelcontextprotocol/protocolVersion": "2026-07-28",
              "io.modelcontextprotocol/clientInfo": {
                name: "history-test",
                version: "1",
              },
              "io.modelcontextprotocol/clientCapabilities": {},
            },
          },
        },
      });
      if (
        response.status() !== 200 ||
        !(await response.text()).includes("unknown_tool")
      )
        fail("History fixture call did not retain unknown tool admission");
    };
    const rename = async (name: string) => {
      const response = await api.patch(
        `${baseURL}/api/v1/principals/${principalID}`,
        {
          headers: { ...headers, "If-Match": etag },
          data: { display_name: name },
        },
      );
      if (response.status() !== 200) fail("History principal rename failed");
      etag = response.headers().etag!;
    };
    await call("historical_library.lookup");
    for (let i = 0; i < 50; i++) {
      await call("workshop.echo");
      await rename(`Investigator ${i}`);
    }
    await rename("Café Investigator");

    // Audit's predicates already precede LIMIT. Reproduce the older-only report
    // against the unchanged TOOLS-26 UI before adding simulated failure states.
    await page.evaluate(() => {
      window.location.hash = "#/audit";
    });
    await expect(page.getByTestId("audit-row")).toHaveCount(50);
    let failAuditOlder = true;
    const auditFailure = async (route: import("@playwright/test").Route) => {
      if (
        failAuditOlder &&
        new URL(route.request().url()).searchParams.has("cursor")
      ) {
        failAuditOlder = false;
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 503,
            code: "storage_unavailable",
            title: "Storage is unavailable.",
          }),
        });
      } else await route.fallback();
    };
    await page.route("**/api/v1/audit-events?*", auditFailure);
    await page.getByRole("button", { name: "Load older audit events" }).click();
    await expect(
      page.getByText("Older audit results unavailable.", { exact: false }),
    ).toBeVisible();
    await expect(page.getByTestId("audit-row")).toHaveCount(50);
    await expect(
      page.getByText("Audit read unavailable", { exact: true }),
    ).toHaveCount(0);
    await capture("audit-continuation-error");
    await page.getByRole("button", { name: "Load older audit events" }).click();
    await expect(page.getByTestId("audit-row")).toHaveCount(100);
    await page.unroute("**/api/v1/audit-events?*", auditFailure);
    const auditResponse = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/v1/audit-events" &&
        new URL(r.url()).searchParams.get("action") === "create",
    );
    await page.evaluate((id) => {
      window.location.hash = `#/audit?filter_action=create&filter_category=principal&filter_outcome=succeeded&filter_target_id=${id}`;
    }, principalID);
    const auditData = await (await auditResponse).json();
    if (
      auditData.items.length !== 1 ||
      auditData.items[0].target.id !== principalID
    )
      fail("Real Audit older-only query failed");
    await expect(page.getByTestId("audit-row")).toHaveCount(1);
    await page
      .getByTestId("audit-row")
      .locator('[data-label="Event"] .table-primary a')
      .click();
    await page.getByRole("link", { name: "Back to audit history" }).click();
    await expect(page.getByTestId("audit-row")).toHaveCount(1);
    await expect(page).toHaveURL(/filter_action=create/);

    await page.evaluate(() => {
      window.location.hash = "#/invocations";
    });
    await expect(page.getByTestId("invocation-row")).toHaveCount(50);
    const filteredResponse = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/v1/invocations" &&
        new URL(r.url()).searchParams.get("tool") === "historical lokoup",
    );
    await page.getByLabel("Tool", { exact: true }).fill("historical lokoup");
    const selected = await (await filteredResponse).json();
    if (
      selected.items.length !== 1 ||
      selected.items[0].requested_name !== "historical_library.lookup"
    )
      fail("Real Invocation older-only fuzzy query failed");
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);
    await expect(page.getByLabel("Tool", { exact: true })).toBeFocused();
    await page
      .getByLabel("Principal", { exact: true })
      .fill("cafe investgiator");
    await expect(page).toHaveURL(/filter_principal=cafe%20investgiator/);
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);
    await page
      .getByLabel("Authorization", { exact: true })
      .selectOption("not_evaluated");
    await page
      .getByLabel("Outcome", { exact: true })
      .selectOption("unknown_tool");
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);

    await capture("filtered");
    let failFresh = true;
    let failOlder = false;
    const failures = async (route: import("@playwright/test").Route) => {
      const query = new URL(route.request().url()).searchParams;
      if (
        (!query.has("cursor") && failFresh) ||
        (query.has("cursor") && failOlder)
      ) {
        failFresh = false;
        failOlder = false;
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 503,
            code: "storage_unavailable",
            title: "Storage is unavailable.",
          }),
        });
      } else await route.fallback();
    };
    await page.route("**/api/v1/invocations?*", failures);
    await rename("Café Investigator two");
    await expect(
      page.getByText("Refresh failed; shown results are stale", {
        exact: true,
      }),
    ).toBeVisible();
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);
    await capture("refresh-error");
    await page.getByRole("button", { name: "Retry refresh" }).click();
    await expect(
      page.getByText("Refresh failed; shown results are stale", {
        exact: true,
      }),
    ).toHaveCount(0);
    const live = page.getByRole("switch", { name: "Live mode" });
    await live.uncheck();
    await page
      .getByTestId("invocation-row")
      .getByRole("link", {
        name: `Invocation ${selected.items[0].id}`,
        exact: true,
      })
      .click();
    await page.getByRole("link", { name: "Back to invocations" }).click();
    await expect(live).not.toBeChecked();
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);
    await expect(page).toHaveURL(/filter_tool=historical%20lokoup/);
    await page.goBack();
    await expect(page.getByTestId("invocation-detail")).toBeVisible();
    await page.goForward();
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);
    await expect(live).not.toBeChecked();
    await live.check();
    await page.getByLabel("Tool", { exact: true }).fill("arrival.lookup");
    await expect(
      page.getByText("No matching invocations", { exact: true }),
    ).toBeVisible();
    await capture("empty");
    const nonmatchResponse = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/v1/invocations" &&
        new URL(r.url()).searchParams.get("tool") === "arrival.lookup",
    );
    await call("workshop.echo");
    await nonmatchResponse;
    await expect(page.getByTestId("invocation-row")).toHaveCount(0);
    await expect(
      page.getByText("No matching invocations", { exact: true }),
    ).toBeVisible();
    const arrivalResponse = page.waitForResponse(
      (r) =>
        new URL(r.url()).pathname === "/api/v1/invocations" &&
        new URL(r.url()).searchParams.get("tool") === "arrival.lookup",
    );
    await call("arrival.lookup");
    await arrivalResponse;
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);
    await live.uncheck();
    await call("arrival.lookup");
    await expect(
      page.getByText("Updates available", { exact: true }),
    ).toBeVisible();
    await expect(page.getByTestId("invocation-row")).toHaveCount(1);
    await page.getByTestId("manual-refresh").click();
    await expect(page.getByTestId("invocation-row")).toHaveCount(2);
    await expect(live).not.toBeChecked();
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    await expect(page.getByTestId("invocation-row")).toHaveCount(50);
    await live.check();
    await expect(
      page.getByRole("button", { name: "Load older invocations" }),
    ).toBeEnabled();
    failOlder = true;
    await page
      .getByRole("button", { name: "Load older invocations" })
      .scrollIntoViewIfNeeded();
    const anchor = await page
      .getByTestId("invocation-row")
      .evaluateAll((rows) => {
        const row = rows.find((item) => {
          const rect = item.getBoundingClientRect();
          return rect.top >= 0 && rect.bottom <= innerHeight;
        });
        return row
          ? {
              id: row.querySelector('a[aria-label^="Invocation "]')!
                .textContent!,
              top: row.getBoundingClientRect().top,
            }
          : null;
      });
    if (!anchor) fail("Older-reading test has no visible row anchor");
    await page.getByRole("button", { name: "Load older invocations" }).click();
    await expect(
      page.getByText("Older results unavailable.", { exact: false }),
    ).toBeVisible();
    await expect(page.getByTestId("invocation-row")).toHaveCount(50);
    await expect(
      page.getByText("Live paused while viewing older results", {
        exact: true,
      }),
    ).toBeVisible();
    const anchorAfter = await page
      .getByTestId("invocation-row")
      .filter({
        has: page.getByRole("link", {
          name: `Invocation ${anchor.id}`,
          exact: true,
        }),
      })
      .evaluate((row) => row.getBoundingClientRect().top);
    if (Math.abs(anchorAfter - anchor.top) > 80)
      fail("Older-reading pause displaced the retained row anchor");
    await capture("continuation-error");
    await page.getByRole("button", { name: "Load older invocations" }).click();
    await expect(page.getByTestId("invocation-row")).toHaveCount(54);
    await expect(
      page.getByText("Live paused while viewing older results", {
        exact: true,
      }),
    ).toBeVisible();
    await capture("paused");
    await call("arrival.lookup");
    await expect(
      page.getByText("Updates available", { exact: true }),
    ).toBeVisible();
    await expect(page.getByTestId("invocation-row")).toHaveCount(54);
    await page.getByLabel("Tool", { exact: true }).fill("arrival.lookup");
    await expect(page.getByTestId("invocation-row")).toHaveCount(3);
    await expect(
      page.getByRole("button", { name: "Resume live" }),
    ).toBeVisible();
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    await expect(page.getByTestId("invocation-row")).toHaveCount(50);
    await expect(
      page.getByRole("button", { name: "Resume live" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "Resume live" }).click();
    await expect(
      page.getByText("Live paused while viewing older results", {
        exact: true,
      }),
    ).toHaveCount(0);
    await expect(page.getByTestId("invocation-row")).toHaveCount(50);
    failFresh = true;
    await page.getByLabel("Tool", { exact: true }).fill("absent.lookup");
    await expect(
      page.getByText("Invocation list unavailable", { exact: true }),
    ).toBeVisible();
    await expect(page.getByTestId("invocation-row")).toHaveCount(0);
    await expect(
      page.getByLabel("Authorization", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByText("No matching invocations", { exact: true }),
    ).toHaveCount(0);
    await capture("new-query-error");
    await page.getByRole("button", { name: "Retry refresh" }).click();
    await expect(
      page.getByText("No matching invocations", { exact: true }),
    ).toBeVisible();
    let releaseLate!: () => void;
    let markLate!: () => void;
    let markSettled!: () => void;
    const lateBarrier = new Promise<void>((resolve) => {
      releaseLate = resolve;
    });
    const lateStarted = new Promise<void>((resolve) => {
      markLate = resolve;
    });
    const lateSettled = new Promise<void>((resolve) => {
      markSettled = resolve;
    });
    const holdLate = async (route: import("@playwright/test").Route) => {
      if (
        new URL(route.request().url()).searchParams.get("tool") !==
        "superseded.lookup"
      ) {
        await route.fallback();
        return;
      }
      const response = await route.fetch();
      markLate();
      await lateBarrier;
      try {
        await route.fulfill({ response });
      } catch {
        /* The superseded browser request may already be aborted. */
      } finally {
        markSettled();
      }
    };
    await page.route("**/api/v1/invocations?*", holdLate);
    try {
      await page.getByLabel("Tool", { exact: true }).fill("superseded.lookup");
      await lateStarted;
      await page.getByLabel("Tool", { exact: true }).fill("arrival.lookup");
      await expect(page.getByTestId("invocation-row")).toHaveCount(3);
    } finally {
      releaseLate();
    }
    await lateSettled;
    await page.evaluate(() => Promise.resolve());
    await expect(page.getByTestId("invocation-row")).toHaveCount(3);
    await expect(
      page.getByText("No matching invocations", { exact: true }),
    ).toHaveCount(0);
    await page.unroute("**/api/v1/invocations?*", holdLate);
    await page.unroute("**/api/v1/invocations?*", failures);
    agentBearer = "";
    await page
      .getByRole("button", { name: "Clear filters", exact: true })
      .click();
    await expect(page.getByTestId("invocation-row")).toHaveCount(50);
    await page.getByRole("button", { name: "Load older invocations" }).click();
    await expect(
      page.getByRole("button", { name: "Resume live" }),
    ).toBeVisible();
    await live.uncheck();
    await page.getByRole("button", { name: "Sign out", exact: true }).click();
    await page
      .getByRole("dialog", { name: "Sign out of this browser session?" })
      .getByRole("button", { name: "Sign out", exact: true })
      .click();
    await waitForLifecycle(page, "signed_out");
    await page.getByTestId("admin-bearer-input").fill(bearer);
    await page.getByTestId("sign-in-submit").click();
    await waitForLifecycle(page, "authenticated");
    await page.evaluate(() => {
      window.location.hash = "#/invocations";
    });
    await expect(page.getByTestId("invocation-row")).toHaveCount(50);
    await expect(live).toBeChecked();
    await expect(page.getByRole("button", { name: "Resume live" })).toHaveCount(
      0,
    );
    await expect(page.getByLabel("Tool", { exact: true })).toHaveValue("");
    await page.evaluate(() => {
      window.location.hash = "#/overview";
    });
    return screenshots;
  } finally {
    await api.dispose();
  }
}
