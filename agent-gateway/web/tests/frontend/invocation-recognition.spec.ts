import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";
import { invocationFixture, invocationIDs } from "../browser/fixtures.ts";

test("invocation-recognition", async ({ page, frontend }) => {
  registerCapture(page, "invocation-recognition");
  const evidence = {
    ...invocationFixture(
      invocationIDs.missing,
      "terminal",
      "succeeded",
      "downstream",
    ),
    redacted_arguments: { recorded: "unchanged" },
  };
  let recognition: unknown = {
    server: {
      server_id: invocationIDs.server,
      display_name: "Server " + "long recognition name ".repeat(10).trim(),
    },
    grant: {
      grant_id: invocationIDs.grant,
      description: "Grant " + "long recognition description ".repeat(8).trim(),
    },
  };
  let reads = 0;
  let failed = false;
  await page.route(
    `**/api/v2/mcp/invocations/${invocationIDs.missing}`,
    async (route) => {
      expect(route.request().method()).toBe("GET");
      expect(route.request().postData()).toBe(null);
      reads++;
      if (failed)
        return route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          json: {
            status: 503,
            code: "storage_unavailable",
            title: "Unavailable",
          },
        });
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: {
          ...evidence,
          ...(recognition === undefined ? {} : { recognition }),
        },
      });
    },
  );
  await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
  await page.getByTestId("sign-in-submit").click();
  await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
    "data-session-lifecycle",
    "authenticated",
  );
  await page.goto(
    `${frontend.origin}/#/mcp/invocations/${invocationIDs.missing}`,
  );
  const detail = page.getByTestId("invocation-detail");
  const fact = (label: string) =>
    detail.locator(".detail-facts > div").filter({
      has: page.locator("dt", { hasText: new RegExp(`^${label}$`) }),
    });
  await expect(fact("Server").locator(".table-primary a")).toHaveText(
    /^Server long/,
  );
  await expect(fact("Grant").locator(".table-primary a")).toHaveText(
    /^Grant long/,
  );
  for (const [label, id, path] of [
    ["Server", invocationIDs.server, "servers"],
    ["Grant", invocationIDs.grant, "grants"],
  ]) {
    await expect(fact(label!).locator(".table-identifier")).toHaveText(id!);
    await expect(fact(label!).getByRole("link")).toHaveAttribute(
      "href",
      `#/mcp/${path}/${id}`,
    );
    expect(
      await fact(label!)
        .locator(".table-primary a")
        .evaluate((el) => getComputedStyle(el).fontWeight),
    ).toBe("700");
  }
  await capture(page, "named-long", true);
  await page.route("**/api/v2/mcp/invocations?*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      json: {
        items: [
          invocationFixture(
            invocationIDs.missing,
            "terminal",
            "succeeded",
            "downstream",
          ),
        ],
        next_cursor: null,
      },
    }),
  );
  await page.evaluate(() => {
    location.hash = "#/mcp/invocations";
  });
  await page.getByRole("switch", { name: "Live mode" }).uncheck();
  await page
    .locator(`a[href="#/mcp/invocations/${invocationIDs.missing}"]`)
    .click();
  await expect(fact("Server").getByRole("link")).toHaveText(/^Server long/);
  recognition = {
    server: { server_id: invocationIDs.server, display_name: "Renamed server" },
    grant: { grant_id: invocationIDs.grant, description: "Renamed grant" },
  };
  frontend.invalidate("servers", invocationIDs.server);
  await expect(fact("Server").getByRole("link")).toHaveText("Renamed server");
  await expect(fact("Grant").getByRole("link")).toHaveText("Renamed grant");
  await expect(detail).toContainText("unchanged");
  await page.reload();
  await expect(fact("Server").getByRole("link")).toHaveText("Renamed server");
  recognition = {
    server: null,
    grant: { grant_id: invocationIDs.grant, description: "" },
  };
  frontend.invalidate("authorization", invocationIDs.grant);
  await expect(fact("Grant").getByRole("link")).toHaveText("Unnamed grant");
  await expect(fact("Server").getByRole("link")).toHaveText(
    invocationIDs.server,
  );
  await capture(page, "unnamed-missing", true);
  for (const value of [{ server: null, grant: null }, undefined]) {
    recognition = value;
    await page.getByTestId("manual-refresh").click();
    await expect(fact("Grant").getByRole("link")).toHaveText(
      invocationIDs.grant,
    );
    await expect(fact("Server").getByRole("link")).toHaveText(
      invocationIDs.server,
    );
    await expect(detail).not.toContainText("deleted");
  }
  await capture(page, "unavailable-older", true);
  failed = true;
  const before = reads;
  await page.getByTestId("manual-refresh").click();
  await expect.poll(() => reads).toBeGreaterThan(before);
  await expect(detail).toContainText("unchanged");
  await expect(fact("Server").getByRole("link")).toHaveText(
    invocationIDs.server,
  );
});
