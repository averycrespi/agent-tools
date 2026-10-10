import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";

test("shell", async ({ page, frontend }) => {
  registerCapture(page, "shell");
  await expect(
    page.evaluate(() => Reflect.set(window, "axe", {})),
  ).rejects.toThrow("Automated axe scans are not owned by the frontend suite");
  const bearer = page.getByTestId("admin-bearer-input");
  const submit = page.getByTestId("sign-in-submit");
  await expect(page.getByRole("alert")).toHaveCount(0);
  await capture(page, "sign-in-blank", true);
  await submit.click();
  expect(
    await bearer.evaluate(
      (element: HTMLInputElement) => element.validity.valueMissing,
    ),
  ).toBe(true);
  await capture(page, "sign-in-validation");
  await bearer.fill(syntheticBearer);
  await capture(page, "sign-in-entered");
  let release: (() => void) | undefined;
  await page.route("**/api/v2/admin-sessions", async (route) => {
    await new Promise<void>((resolve) => {
      release = resolve;
    });
    await route.fulfill({
      status: 401,
      contentType: "application/problem+json",
      json: {
        status: 401,
        code: "authentication_required",
        title: "Sign in required",
      },
    });
  });
  await submit.click();
  await expect(submit).toBeDisabled();
  await capture(page, "sign-in-pending");
  release?.();
  await expect(page.getByRole("alert")).toBeVisible();
  await expect(bearer).toHaveValue("");
  await capture(page, "sign-in-rejected");
  await page.unroute("**/api/v2/admin-sessions");
  await bearer.fill(syntheticBearer);
  await submit.click();
  await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
    "data-session-lifecycle",
    "authenticated",
  );
  const gitLinks = page.locator('nav a[href^="#/git/"]');
  await expect(gitLinks).toHaveText([
    "Routing",
    "Credentials",
    "Repositories",
    "Grants",
    "Traffic",
  ]);
  await capture(page, "authenticated", true);
  await page.locator(".skip-link").focus();
  await expect(page.locator(".skip-link")).toBeInViewport();
  await capture(page, "skip-link-focused");
  await page.locator(".skip-link").press("Enter");
  await expect(page.locator("#page-title")).toBeFocused();
  await page.evaluate(() => {
    location.hash = "#/not-a-route";
  });
  await expect(page.locator("main")).toContainText(
    /not.*recognized|invalid|unavailable/i,
  );
  await capture(page, "invalid-location");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Menu", exact: true }).click();
  await expect(gitLinks).toHaveText([
    "Routing",
    "Credentials",
    "Repositories",
    "Grants",
    "Traffic",
  ]);
  await capture(page, "navigation-open");
  await page.getByRole("button", { name: "Menu", exact: true }).click();
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Sign out");
  await capture(page, "sign-out-confirmation");
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  let restored: (() => void) | undefined;
  await page.route("**/api/v2/admin-sessions/current", async (route) => {
    await new Promise<void>((resolve) => {
      restored = resolve;
    });
    return route.fulfill({ status: 503, json: { code: "unavailable" } });
  });
  await page.route("**/api/v2/system-status", (route) =>
    route.fulfill({ status: 401, json: { code: "authentication_required" } }),
  );
  await page.getByTestId("manual-refresh").click();
  await expect(page.getByText("Session lost", { exact: true })).toBeVisible();
  await capture(page, "session-lost");
  restored?.();
  await expect(bearer).toBeVisible();
  await expect(page.getByRole("alert")).toContainText("could not be recovered");
  await capture(page, "session-recovery-failed");
  await page.unroute("**/api/v2/system-status");
  await page.goto("about:blank");
  await page.goto(`${frontend.origin}/#/overview`);
  await expect(
    page.getByText("Restoring session", { exact: true }),
  ).toBeVisible();
  await capture(page, "restoring-session");
  restored?.();
  await expect(bearer).toBeVisible();
  await capture(page, "session-unrestorable");
});
