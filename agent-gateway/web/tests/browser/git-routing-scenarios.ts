import AxeBuilder from "@axe-core/playwright";
import { expect, type BrowserContext, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { waitForLifecycle } from "./shared.ts";

export async function runGitRouting(
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<number[]> {
  const screenshots = await mkdtemp(join(tmpdir(), "gateway-git-routing-"));
  const failures: number[] = [];
  const path = "/api/v2/git/routing-profile";
  const api = `${baseURL}${path}`;
  const headers = { Authorization: `Bearer ${bearer}`, Cookie: "" };
  let writes = 0;
  page.on("request", (r) => {
    if (r.url() === api && r.method() === "PATCH") writes++;
  });
  page.on("response", (r) => {
    if (r.url() === api && r.status() >= 400) failures.push(r.status());
  });
  const capture = async (name: string) => {
    for (const [suffix, width, theme] of [
      ["desktop", 1280, "light"],
      ["narrow", 390, "dark"],
      ["320", 320, "light"],
    ] as const) {
      await page.emulateMedia({ colorScheme: theme });
      await page.setViewportSize({ width, height: 900 });
      await page.screenshot({
        path: join(screenshots, `${name}-${suffix}.png`),
        fullPage: true,
      });
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
    await page.setViewportSize({ width: 1280, height: 900 });
  };
  const review = () =>
    page.getByRole("button", { name: "Review changes", exact: true }).click();
  const confirm = async () => {
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Save routing", exact: true })
      .click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
  };
  const refresh = () =>
    page
      .getByRole("button", { name: "Refresh current view", exact: true })
      .click();
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  let releaseRead!: () => void;
  const heldRead = new Promise<void>((r) => {
    releaseRead = r;
  });
  await page.route(api, async (route) => {
    await heldRead;
    await route.continue();
  });
  try {
    await page.locator('#primary-navigation a[href="#/git/routing"]').click();
    await expect(
      page.getByText("Loading Git routing", { exact: true }),
    ).toBeVisible();
    await capture("loading");
  } finally {
    releaseRead();
  }
  await expect(page.getByText("Active", { exact: true })).toBeVisible();
  await page.unroute(api);
  await expect(
    page
      .locator("section.detail-section")
      .getByText("No origins enabled", { exact: true }),
  ).toBeVisible();
  await capture("empty");
  await page.getByRole("button", { name: "Add origin", exact: true }).click();
  await page.getByLabel("Origin 1", { exact: true }).fill("http://github.com");
  await review();
  await expect(
    page.getByText("Check HTTPS origins", { exact: true }),
  ).toBeVisible();
  expect(writes).toBe(0);
  await page.getByLabel("Origin 1", { exact: true }).fill("https://github.com");
  await review();
  await expect(
    page
      .getByRole("dialog")
      .getByText("https://github.com:443", { exact: true }),
  ).toBeVisible();
  await capture("confirmation");
  const axe = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(
    axe.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    ),
  ).toEqual([]);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  expect(writes).toBe(0);
  await expect(page.getByLabel("Origin 1", { exact: true })).toHaveValue(
    "https://github.com",
  );
  await page
    .locator('#primary-navigation a[href="#/git/repositories"]')
    .click();
  await page
    .getByRole("dialog", { name: "Discard unsaved changes?", exact: true })
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await review();
  await confirm();
  await expect(page.getByText("Routing saved", { exact: true })).toBeVisible();
  expect(writes).toBe(1);
  expect(
    (await (await context.request.get(api, { headers })).json()).origins,
  ).toEqual(["https://github.com:443"]);
  await capture("saved");

  // A concurrent real update must retain the draft until explicit revision review.
  await page.getByRole("button", { name: "Add origin", exact: true }).click();
  await page
    .getByLabel("Origin 2", { exact: true })
    .fill("https://example.com");
  await page.route(api, async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    const current = await context.request.get(api, { headers });
    const concurrent = await context.request.patch(api, {
      headers: { ...headers, "If-Match": current.headers().etag! },
      data: { origins: ["https://gitlab.com"] },
    });
    expect(concurrent.status()).toBe(200);
    await route.continue();
  });
  await review();
  await confirm();
  await expect(
    page.getByText("Review current revision", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Review changes", exact: true }),
  ).toBeDisabled();
  await expect(page.getByLabel("Origin 2", { exact: true })).toHaveValue(
    "https://example.com",
  );
  await expect(
    page
      .locator("section.detail-section")
      .getByText("https://gitlab.com:443", { exact: true }),
  ).toBeVisible();
  await capture("conflict");
  await page.unroute(api);
  await page
    .getByRole("button", { name: "Use reviewed revision", exact: true })
    .click();

  let releaseSave!: () => void;
  const heldSave = new Promise<void>((r) => {
    releaseSave = r;
  });
  await page.route(api, async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    await heldSave;
    await route.fulfill({ response });
  });
  try {
    await review();
    await confirm();
    await expect(page.getByLabel("Origin 1", { exact: true })).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "Add origin", exact: true }),
    ).toBeDisabled();
    await capture("submitting");
  } finally {
    releaseSave();
  }
  await expect(page.getByText("Routing saved", { exact: true })).toBeVisible();
  await page.unroute(api);
  await expect(page.getByLabel("Origin 1", { exact: true })).toHaveValue(
    "https://example.com:443",
  );

  await page.route(api, (route) =>
    route.fulfill({ status: 200, contentType: "application/json", json: {} }),
  );
  await refresh();
  await expect(
    page.getByText("Git routing refresh unavailable", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Review changes", exact: true }),
  ).toBeDisabled();
  await capture("refresh-error");
  await page.unroute(api);
  await refresh();
  await expect(
    page.getByText("Git routing refresh unavailable", { exact: true }),
  ).not.toBeVisible();

  // Removing the entire list is an explicit reviewed replacement, not a toggle.
  await page
    .getByRole("button", { name: "Remove origin 2", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Remove origin 1", exact: true })
    .click();
  await review();
  await confirm();
  await expect(page.getByText("Routing saved", { exact: true })).toBeVisible();
  expect(
    (await (await context.request.get(api, { headers })).json()).origins,
  ).toEqual([]);

  // The write succeeds but its malformed acknowledgement must never be replayed.
  await page.route(api, async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    const response = await route.fetch();
    expect(response.status()).toBe(200);
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      json: {},
    });
  });
  await page.getByRole("button", { name: "Add origin", exact: true }).click();
  await page.getByLabel("Origin 1", { exact: true }).fill("https://github.com");
  await review();
  const before = writes;
  await confirm();
  await expect(
    page.getByText("Change outcome unknown", { exact: true }),
  ).toBeVisible();
  await refresh();
  await expect(
    page.getByRole("button", { name: "Review changes", exact: true }),
  ).toBeDisabled();
  await capture("uncertain");
  expect(writes).toBe(before + 1);
  expect(
    (await (await context.request.get(api, { headers })).json()).origins,
  ).toEqual(["https://github.com:443"]);
  const finalAxe = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  expect(
    finalAxe.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    ),
  ).toEqual([]);
  process.stdout.write(
    JSON.stringify({
      event: "git_routing_complete",
      requests: requestCount(),
      screenshots,
    }) + "\n",
  );
  return failures;
}
