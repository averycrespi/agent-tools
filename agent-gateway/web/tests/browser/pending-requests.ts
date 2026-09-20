import { expect, type Page } from "@playwright/test";

export async function exercisePendingRequests(page: Page): Promise<void> {
  const route = /\/api\/v2\/mcp\/grant-requests\?state=pending&limit=1$/;
  let total = 125;
  let unavailable = false;
  let reads = 0;
  await page.route(route, async (request) => {
    reads++;
    await request.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        unavailable
          ? {}
          : {
              items: total === 0 ? [] : [{}],
              total_count: total,
              offset: 0,
              next_cursor: total > 1 ? "more" : null,
            },
      ),
    });
  });
  const link = page.locator(
    '#primary-navigation a[href="#/mcp/access-requests"]',
  );
  const badge = page.getByTestId("pending-request-count");
  try {
    await page.getByTestId("manual-refresh").click();
    await expect(link).toHaveAccessibleName("Requests, 125 pending");
    await expect(badge).toHaveText("125");
    expect(reads).toBe(1);
    await link.focus();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/#\/mcp\/access-requests$/);
    await expect(link).toHaveAttribute("aria-current", "page");
    await expect(link).toHaveAccessibleName("Requests, 125 pending");
    await page.evaluate(() => {
      location.hash = "#/mcp/access-requests?queue=all&filter_state=rejected";
    });
    await expect(link).toHaveAccessibleName("Requests, 125 pending");
    await page.locator('#primary-navigation a[href="#/principals"]').click();
    total = 124;
    await page.getByTestId("manual-refresh").click();
    await expect(link).toHaveAccessibleName("Requests, 124 pending");
    unavailable = true;
    await page.getByTestId("manual-refresh").click();
    await expect(link).toHaveAccessibleName(
      "Requests, pending count unavailable; last known 124 pending",
    );
    await expect(badge).toHaveText("?");
    unavailable = false;
    total = 0;
    await page.getByTestId("manual-refresh").click();
    await expect(link).toHaveAccessibleName("Requests, 0 pending");
    await expect(badge).toHaveCount(0);
    total = 12345;
    await page.getByTestId("manual-refresh").click();
    await expect(link).toHaveAccessibleName("Requests, 12345 pending");
    for (const theme of ["light", "dark"]) {
      await page.getByTestId("theme-preference").selectOption(theme);
      for (const width of [1440, 390, 320]) {
        await page.setViewportSize({ width, height: 900 });
        if (
          width < 600 &&
          (await page
            .getByTestId("navigation-toggle")
            .getAttribute("aria-expanded")) !== "true"
        )
          await page.getByTestId("navigation-toggle").click();
        await expect(badge).toBeVisible();
        const bounds = await badge.boundingBox();
        const container = await link.boundingBox();
        expect(bounds).not.toBeNull();
        expect(container).not.toBeNull();
        expect(bounds!.x).toBeGreaterThanOrEqual(container!.x);
        expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(
          container!.x + container!.width,
        );
        expect(
          await page.locator("#primary-navigation .request-count").count(),
        ).toBe(1);
      }
    }
  } finally {
    await page.unroute(route);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.getByTestId("manual-refresh").click();
  }
}
