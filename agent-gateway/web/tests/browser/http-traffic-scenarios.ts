import AxeBuilder from "@axe-core/playwright";
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
  // Exercise real connection correlation through proxy, durable reads and UI.
  const realConnect = page
    .getByRole("row")
    .filter({ hasText: "Interception selected" });
  await expect(realConnect).toHaveCount(1);
  await realConnect
    .getByRole("link", { name: /^CONNECT 127\.0\.0\.1:/ })
    .click();
  await expect(
    page.getByText(/Inner requests are authorized separately/),
  ).toBeVisible();
  const realParent = new URL(page.url()).hash.split("/").at(-1)!;
  await page.getByRole("link", { name: "View related inner requests" }).click();
  await expect(page).toHaveURL(new RegExp(`filter_connect_id=${realParent}`));
  await expect(
    page.getByText("1 matching HTTP traffic record loaded", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("row")
    .filter({ hasText: "GET https://127.0.0.1" })
    .getByRole("link", { name: /^GET https:/ })
    .click();
  await page.getByRole("link", { name: realParent, exact: true }).click();
  await expect(
    page.getByText(/Inner requests are authorized separately/),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  // These three rows came through the real proxy, durable store and public API.
  for (const label of [
    "Protocol upgrades are not supported",
    "Absolute-form HTTP request required",
    "Request target failed validation",
  ]) {
    await expect(page.getByText(label, { exact: true })).toBeVisible();
  }
  await page
    .getByRole("row")
    .filter({ hasText: "Request target failed validation" })
    .getByRole("link", { name: "Not parsed", exact: true })
    .click();
  await expect(
    page.getByText("Request target failed validation", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Gateway", { exact: true })).toBeVisible();
  expect(await page.content()).not.toContain("path-secret");
  expect(await page.content()).not.toContain("query-secret");
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
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
  const rejected = {
    ...admission,
    id: id(4),
    class: "invalid_request",
    default: "",
    target: null,
    decision: null,
    rejection: { stage: "target", reason: "invalid_request_target" },
    connect: { id: id(2), host: "example.com", port: 443 },
  };
  const interception = {
    ...admission,
    id: id(5),
    target: { host: "example.com", port: 443 },
    decision: {
      ...admission.decision,
      allowed: false,
      transport: "intercept",
      reason: "intercept_required",
    },
  };
  const denied = {
    ...interception,
    id: id(6),
    decision: {
      ...interception.decision,
      transport: "none",
      reason: "destination_block",
      grant: { id: id(12), revision: 1 },
    },
    grants: [
      {
        reference: { id: id(12), revision: 1 },
        policy: {
          version: 1,
          type: "block_destination",
          destination: { host: "example.com", port: 443 },
        },
      },
    ],
  };
  let connectCases = false,
    missingParent = false;
  let emptyPage = true;
  let diagnostics = false,
    legacyRejection = false,
    responseEvidence = false;
  let stale = false,
    failHistory = false,
    malformed = false,
    reads = 0;
  await context.route(`${baseURL}/api/v2/http/traffic**`, async (route) => {
    const request = route.request();
    expect(request.method()).toBe("GET");
    expect(request.postData()).toBeNull();
    reads++;
    const url = new URL(request.url());
    if (
      url.pathname.endsWith(`/${id(5)}`) ||
      url.pathname.endsWith(`/${id(6)}`)
    ) {
      await route.fulfill(
        missingParent
          ? {
              status: 404,
              contentType: "application/problem+json",
              body: JSON.stringify({ code: "not_found" }),
            }
          : {
              status: 200,
              contentType: "application/json",
              body: JSON.stringify({
                admission: url.pathname.endsWith(`/${id(5)}`)
                  ? interception
                  : denied,
                completion: null,
              }),
            },
      );
      return;
    }
    if (url.pathname.endsWith(`/${id(4)}`)) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          admission: legacyRejection
            ? { ...rejected, rejection: undefined, connect: undefined }
            : rejected,
          completion: null,
        }),
      });
      return;
    }
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
          completion: responseEvidence
            ? {
                completed_at: at,
                outcome: "succeeded",
                status: 200,
                bytes_sent: 0,
                bytes_received: 1,
                duration_ms: 1,
                response_source: "upstream",
              }
            : null,
        }),
      });
      return;
    }
    expect(url.pathname).toBe("/api/v2/http/traffic");
    if (connectCases) {
      const rows = [
        {
          ...summary(5),
          type: "connect",
          target: interception.target,
          decision: "intercept",
          outcome: "interception_selected",
        },
        {
          ...summary(6),
          type: "connect",
          target: denied.target,
          decision: "block",
          outcome: "not_dispatched",
        },
        summary(2),
      ];
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: url.searchParams.has("connect_id")
            ? []
            : rows.filter(
                (row) =>
                  !url.searchParams.has("outcome") ||
                  row.outcome === url.searchParams.get("outcome"),
              ),
          next_cursor: null,
        }),
      });
      return;
    }
    if (failHistory) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: "{}",
      });
      return;
    }
    if (emptyPage) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], next_cursor: null }),
      });
      return;
    }
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
          : diagnostics
            ? {
                items: [
                  {
                    id: id(4),
                    admitted_at: at,
                    principal_id: principal,
                    target: null,
                    type: "invalid",
                    decision: "invalid",
                    outcome: "not_dispatched",
                    rejection: rejected.rejection,
                    connect: rejected.connect,
                    response_source: "gateway",
                  },
                  summary(3),
                ],
                next_cursor: null,
              }
            : { items: [summary(3), summary(2)], next_cursor: "older" },
      ),
    });
  });
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(
    page.getByText("No HTTP traffic yet", { exact: true }),
  ).toBeVisible();
  await expect(
    page.locator('.history-continuation output[aria-live="polite"]'),
  ).toHaveText("0 HTTP traffic records loaded");
  emptyPage = false;
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(
    page.getByText("2 HTTP traffic records loaded", { exact: true }),
  ).toBeVisible();
  const historySummary = page.locator(
    '.history-continuation output[aria-live="polite"]',
  );
  await expect(historySummary).toHaveCount(1);
  expect(
    await historySummary.evaluate(
      (el) =>
        !!(
          document.querySelector("table")!.compareDocumentPosition(el) &
          Node.DOCUMENT_POSITION_FOLLOWING
        ),
    ),
  ).toBe(true);
  failHistory = true;
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(historySummary).toHaveText(
    "2 HTTP traffic records loaded (stale)",
  );
  await page.screenshot({
    path: join(screenshots, "stale-history.png"),
    fullPage: true,
  });
  failHistory = false;
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(historySummary).toHaveText("2 HTTP traffic records loaded");
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
  const live = page.getByRole("switch", { name: "Live mode", exact: true });
  await live.focus();
  await page.keyboard.press("Space");
  await expect(live).not.toBeChecked();
  await page.getByRole("button", { name: "Load older", exact: true }).click();
  await expect(
    page.getByRole("button", { name: "Return to newest", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Live paused while viewing older results", { exact: true }),
  ).toHaveCount(0);
  await page
    .getByRole("button", { name: "Return to newest", exact: true })
    .click();
  const before = reads;
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect.poll(() => reads).toBeGreaterThan(before);
  await expect(live).not.toBeChecked();
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
    page.getByText("2 matching HTTP traffic records loaded", { exact: true }),
  ).toBeVisible();
  expect(await page.getByRole("button", { name: /Create grant/ }).count()).toBe(
    0,
  );
  diagnostics = true;
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(
    page.getByText("Request target failed validation", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Response: Gateway", { exact: true }),
  ).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({
    path: join(screenshots, "rejection-history.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: join(screenshots, "rejection-history-narrow.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.locator(`a[href*="/http/traffic/${id(4)}"]`).click();
  await expect(
    page.getByText("Request target failed validation", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Destination inherited from CONNECT", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Not parsed", { exact: true })).toBeVisible();
  await page.getByText("Rejection codes", { exact: true }).click();
  await expect(
    page.getByText("target · invalid_request_target", { exact: true }),
  ).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({
    path: join(screenshots, "rejection-detail-narrow.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.screenshot({
    path: join(screenshots, "rejection-detail.png"),
    fullPage: true,
  });
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
  for (const [stage, reason, label] of [
    ["headers", "invalid_headers", "Invalid headers"],
    [
      "request_form",
      "origin_form_required",
      "Origin-form request required inside CONNECT",
    ],
  ]) {
    rejected.rejection = { stage: stage!, reason: reason! };
    await page.getByRole("button", { name: "Refresh current view" }).click();
    await expect(page.getByText(label!, { exact: true })).toBeVisible();
    await page.locator(`a[href*="/http/traffic/${id(4)}"]`).click();
    await expect(page.getByText(label!, { exact: true })).toBeVisible();
    await page
      .getByRole("link", { name: "Back to HTTP traffic", exact: true })
      .click();
  }
  legacyRejection = true;
  await page.locator(`a[href*="/http/traffic/${id(4)}"]`).click();
  await expect(
    page.getByText("Rejection details unavailable", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("Unavailable", { exact: true })).toHaveCount(2);
  await expect(
    page.getByText("Destination inherited from CONNECT", { exact: true }),
  ).toHaveCount(0);
  await page.setViewportSize({ width: 320, height: 844 });
  await page.screenshot({
    path: join(screenshots, "legacy-detail-320.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
  responseEvidence = true;
  await page.locator(`a[href*="/http/traffic/${id(3)}"]`).click();
  await expect(page.getByText("Upstream", { exact: true })).toBeVisible();
  await expect(page.getByText("200", { exact: true })).toBeVisible();
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
  connectCases = true;
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(
    page.getByText("3 HTTP traffic records loaded", { exact: true }),
  ).toBeVisible();
  await expect(page.getByText("CONNECT denied", { exact: true })).toBeVisible();
  await expect(
    page.getByText("Opaque tunnel allowed", { exact: true }),
  ).toBeVisible();
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({
    path: join(screenshots, "connect-history.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: join(screenshots, "connect-history-narrow.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(390);
  await page
    .getByLabel("Outcome", { exact: true })
    .selectOption("interception_selected");
  await expect(
    page.getByText("1 matching HTTP traffic record loaded", { exact: true }),
  ).toBeVisible();
  await page.locator(`a[href*="/http/traffic/${id(5)}"]`).click();
  await expect(
    page.getByText(/Selection does not prove CONNECT acceptance/),
  ).toBeVisible();
  await expect(page.getByText("Not dispatched", { exact: true })).toHaveCount(
    0,
  );
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.screenshot({
    path: join(screenshots, "interception-narrow.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.screenshot({
    path: join(screenshots, "interception.png"),
    fullPage: true,
  });
  await page.getByRole("link", { name: "View related inner requests" }).click();
  await expect(
    page.getByText("No matching HTTP traffic", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("CONNECT ID", { exact: true })).toHaveValue(
    id(5),
  );
  await page
    .getByRole("button", { name: "Clear filters", exact: true })
    .click();
  await page.locator(`a[href*="/http/traffic/${id(6)}"]`).click();
  await expect(page.getByText("CONNECT denied", { exact: true })).toHaveCount(
    2,
  );
  await expect(
    page.getByRole("link", { name: "View related inner requests" }),
  ).toHaveCount(0);
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
  connectCases = false;
  legacyRejection = false;
  rejected.connect.id = id(5);
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await page.locator(`a[href*="/http/traffic/${id(4)}"]`).click();
  missingParent = true;
  await page.getByRole("link", { name: id(5), exact: true }).click();
  await expect(
    page.getByText("Traffic record unavailable", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(/Missing evidence does not prove nonexecution/),
  ).toBeVisible();
  await page.setViewportSize({ width: 320, height: 844 });
  await page.screenshot({
    path: join(screenshots, "missing-parent-320.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page
    .getByRole("link", { name: "Back to HTTP traffic", exact: true })
    .click();
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
