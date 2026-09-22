import { expect, type BrowserContext, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { waitForLifecycle } from "./shared.ts";

export async function runHTTPTraffic(
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const screenshots = await mkdtemp(join(tmpdir(), "gateway-http-traffic-"));
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('#primary-navigation a[href="#/http/traffic"]').click();
  await expect(
    page.getByText("No HTTP traffic yet", { exact: true }),
  ).toBeVisible();
  const id = (n: number) => String(n).padStart(26, "0"),
    at = "2026-09-21T00:00:00.000000000Z",
    principal = id(10);
  const summary = (n: number) => ({
    id: id(n),
    admitted_at: at,
    principal_id: principal,
    target:
      n === 2
        ? { host: "example.com", port: 443 }
        : { host: "example.com", port: 443, scheme: "https", method: "GET" },
    type: n === 2 ? "connect" : "request",
    decision: "allow",
    outcome: "outcome_unknown",
  });
  const admission = {
    id: id(3),
    admitted_at: at,
    principal: { id: principal, revision: 1 },
    agent_credential: { id: id(11), revision: 1 },
    credential_fingerprint: "0123456789abcdef",
    class: "evaluated",
    default: "allow",
    target: summary(3).target,
    evaluated_at: at,
    decision: {
      version: 1,
      principal: { id: principal, revision: 1 },
      policy_revision: 1,
      default_revision: 1,
      transport: "request",
      allowed: true,
      reason: "principal_default",
    },
    grants: [],
    material: null,
  };
  const tunnel = {
    ...admission,
    id: id(2),
    target: { host: "example.com", port: 443 },
    decision: {
      ...admission.decision,
      transport: "tunnel",
      reason: "tunnel_allow",
      grant: { id: id(12), revision: 1 },
    },
    grants: [
      {
        reference: { id: id(12), revision: 1 },
        policy: {
          version: 1,
          type: "allow_tunnel",
          allow_private: false,
          destination: { host: "example.com", port: 443 },
        },
      },
    ],
  };
  let stale = false,
    malformed = false,
    reads = 0;
  await context.route(`${baseURL}/api/v2/http/traffic**`, async (route) => {
    const request = route.request();
    expect(request.method()).toBe("GET");
    expect(request.postData()).toBeNull();
    reads++;
    const url = new URL(request.url());
    if (url.pathname.endsWith(`/${id(2)}`)) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ admission: tunnel, completion: null }),
      });
      return;
    }
    if (url.pathname.endsWith(`/${id(3)}`)) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          admission: malformed
            ? {
                ...admission,
                target: {
                  ...admission.target,
                  path: "/observed-private-canary?token=query-private-canary",
                },
              }
            : admission,
          completion: null,
        }),
      });
      return;
    }
    expect(url.pathname).toBe("/api/v2/http/traffic");
    if (url.searchParams.has("cursor") && stale) {
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({ code: "stale_cursor" }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        url.searchParams.has("cursor")
          ? { items: [summary(1)], next_cursor: null }
          : { items: [summary(3), summary(2)], next_cursor: "older" },
      ),
    });
  });
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(
    page.getByText("2 HTTP traffic records loaded", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Load older", exact: true }).click();
  await expect(
    page.getByText("3 HTTP traffic records loaded", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Live paused while viewing older results", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Resume live", exact: true }).click();
  await expect(
    page.getByText("2 HTTP traffic records loaded", { exact: true }),
  ).toBeVisible();
  await page.getByLabel("Live", { exact: true }).uncheck();
  const before = reads;
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect.poll(() => reads).toBeGreaterThan(before);
  await expect(page.getByLabel("Live", { exact: true })).not.toBeChecked();
  await page
    .getByLabel("Destination host", { exact: true })
    .fill("example.com");
  await expect(page).toHaveURL(/filter_destination=example.com/);
  await page.screenshot({
    path: join(screenshots, "history.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: join(screenshots, "history-narrow.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.locator(`a[href*="/http/traffic/${id(3)}"]`).click();
  await expect(
    page.getByText("Unknown outcome", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Missing terminal evidence does not prove nonexecution or safe retry.",
      { exact: true },
    ),
  ).toBeVisible();
  await page.getByText("Matched policy selectors", { exact: true }).click();
  await page.screenshot({
    path: join(screenshots, "detail.png"),
    fullPage: true,
  });
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
  await expect(page).toHaveURL(/filter_destination=example.com/);
  await page.locator(`a[href*="/http/traffic/${id(2)}"]`).click();
  await expect(
    page.getByText("Opaque tunnel: inner HTTP requests are not visible.", {
      exact: true,
    }),
  ).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: join(screenshots, "tunnel-narrow.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
  stale = true;
  await page.getByRole("button", { name: "Load older", exact: true }).click();
  await expect(
    page.getByText(
      "History changed. Restarted at the newest matching traffic.",
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    page.getByText("2 HTTP traffic records loaded", { exact: true }),
  ).toBeVisible();
  expect(await page.getByRole("button", { name: /Create grant/ }).count()).toBe(
    0,
  );
  malformed = true;
  await page.locator(`a[href*="/http/traffic/${id(3)}"]`).click();
  await expect(
    page.getByText("HTTP traffic unavailable", { exact: true }),
  ).toBeVisible();
  expect(await page.content()).not.toContain("private-canary");
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await waitForLifecycle(page, "signed_out");
  expect(await page.getByText("example.com", { exact: true }).count()).toBe(0);
  console.log(
    JSON.stringify({
      event: "http_traffic_complete",
      requests: requestCount(),
      screenshots,
    }),
  );
}
