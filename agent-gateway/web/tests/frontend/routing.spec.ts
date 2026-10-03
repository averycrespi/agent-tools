import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";

test("git-routing", async ({ page, frontend }) => {
  registerCapture(page, "git-routing");
  const api = "/api/v2/git/routing-profile";
  let origins: string[] = [];
  let revision = "1";
  let readMode: "current" | "loading" | "error" = "loading";
  let writeMode: "success" | "conflict" | "pending" | "uncertain" = "success";
  let release: (() => void) | undefined;
  let writes = 0;
  await page.route(`**${api}`, async (route) => {
    const request = route.request();
    if (request.method() === "GET") {
      if (readMode === "loading")
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      if (readMode === "error")
        return route.fulfill({
          status: 503,
          json: {
            status: 503,
            code: "unavailable",
            title: "Synthetic unavailable",
          },
        });
      return route.fulfill({
        json: { revision, origins, active: true },
        headers: { ETag: `"git-profile-routing-${revision}"` },
      });
    }
    expect(request.method()).toBe("PATCH");
    writes++;
    expect(request.headers()["if-match"]).toBe(
      `"git-profile-routing-${revision}"`,
    );
    expect(Object.keys(request.postDataJSON())).toEqual(["origins"]);
    if (writeMode === "pending")
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    if (writeMode === "conflict")
      return route.fulfill({
        status: 412,
        contentType: "application/problem+json",
        json: {
          status: 412,
          code: "stale_revision",
          title: "Synthetic routing conflict",
        },
      });
    if (writeMode === "uncertain") return route.fulfill({ json: {} });
    origins = request.postDataJSON().origins;
    revision = String(Number(revision) + 1);
    return route.fulfill({
      json: { revision, origins, active: true },
      headers: { ETag: `"git-profile-routing-${revision}"` },
    });
  });
  await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
  await page.getByTestId("sign-in-submit").click();
  await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
    "data-session-lifecycle",
    "authenticated",
  );
  await page.goto(`${frontend.origin}/#/git/routing`);
  await expect(
    page.getByText("Loading Git routing", { exact: true }),
  ).toBeVisible();
  await capture(page, "loading");
  readMode = "current";
  release?.();
  await expect(
    page.getByText("No origins enabled", { exact: true }).first(),
  ).toBeVisible();
  await capture(page, "empty", true);
  const review = () =>
    page.getByRole("button", { name: "Review changes", exact: true }).click();
  const confirm = () =>
    page
      .getByRole("dialog")
      .getByRole("button", { name: "Save routing", exact: true })
      .click();
  await page.getByRole("button", { name: "Add origin", exact: true }).click();
  await page
    .getByLabel("Origin 1", { exact: true })
    .fill("http://example.invalid");
  await review();
  await expect(
    page.getByText("Check HTTPS origins", { exact: true }),
  ).toBeVisible();
  await capture(page, "validation");
  expect(writes).toBe(0);
  await page
    .getByLabel("Origin 1", { exact: true })
    .fill("https://example.invalid");
  await review();
  await expect(page.getByRole("dialog")).toContainText(
    "https://example.invalid:443",
  );
  await capture(page, "confirmation");
  await confirm();
  await expect(page.getByText("Routing saved", { exact: true })).toBeVisible();
  expect(origins).toEqual(["https://example.invalid:443"]);
  await capture(page, "saved");
  await page.getByRole("button", { name: "Add origin", exact: true }).click();
  await page
    .getByLabel("Origin 2", { exact: true })
    .fill("https://other.invalid");
  writeMode = "conflict";
  await review();
  await confirm();
  await expect(
    page.getByText("Review current revision", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("Origin 2", { exact: true })).toHaveValue(
    "https://other.invalid",
  );
  await capture(page, "conflict");
  await page
    .getByRole("button", { name: "Use reviewed revision", exact: true })
    .click();
  writeMode = "pending";
  await review();
  await confirm();
  await expect(page.getByLabel("Origin 1", { exact: true })).toBeDisabled();
  await capture(page, "pending");
  writeMode = "uncertain";
  release?.();
  await expect(
    page.getByText("Change outcome unknown", { exact: true }),
  ).toBeVisible();
  await capture(page, "uncertain");
  const before = writes;
  readMode = "error";
  await page.getByTestId("manual-refresh").click();
  await expect(
    page.getByText("Git routing refresh unavailable", { exact: true }),
  ).toBeVisible();
  await capture(page, "refresh-error");
  expect(writes).toBe(before);
});
