import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";

for (const domain of ["http", "git"] as const) {
  test(`${domain}-credential-setup`, async ({ page, frontend }) => {
    registerCapture(page, `${domain}-credential-setup`);
    const id = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
    const canary = "SYNTHETIC_INITIAL_SETUP_CANARY";
    const path = `/api/v2/${domain}/credentials`;
    let current = {
      key: "synthetic-credential",
      id,
      name: "Imported credential",
      revision: "1",
      available: false,
      material_state: "not_configured",
      recipe: { header: "Authorization", prefix: "Bearer " },
      created_at: "2026-10-01T12:00:00Z",
      updated_at: "2026-10-01T12:00:00Z",
      ...(domain === "http"
        ? {
            boundary: {
              host: "example.invalid",
              port: 443,
              allow_wildcard: false,
            },
            referencing_grants: [],
          }
        : {
            origin: "https://example.invalid:443",
            referencing_repositories: [],
          }),
    };
    let writes = 0;
    await page.route(`**${path}**`, async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const etag = `"${domain}-credential-${id}-${current.revision}"`;
      if (request.method() === "GET") {
        return route.fulfill({
          json:
            url.pathname === `${path}/${id}`
              ? current
              : {
                  items: [current],
                  total_count: 1,
                  offset: 0,
                  next_cursor: null,
                },
          headers: { ETag: etag },
        });
      }
      writes++;
      expect(url.pathname).toBe(`${path}/${id}/rotate`);
      expect(request.method()).toBe("POST");
      expect(request.headers()["if-match"]).toBe(etag);
      expect(request.postDataJSON()).toEqual({ secret: canary });
      current = {
        ...current,
        revision: "3",
        available: true,
        material_state: "ready",
      };
      return route.fulfill({
        json: current,
        headers: { ETag: `"${domain}-credential-${id}-3"` },
      });
    });
    await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
    await page.getByTestId("sign-in-submit").click();
    const nav = async (suffix = "") => {
      await page.goto(`${frontend.origin}/#/${domain}/credentials${suffix}`);
      await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
        "data-session-lifecycle",
        "authenticated",
      );
    };
    await nav();
    await expect(page.getByRole("table")).toContainText("Not configured");
    await capture(page, "unset-list");
    await nav(`/${id}`);
    await expect(
      page.getByText("Not configured", { exact: true }),
    ).toBeVisible();
    await capture(page, "unset-detail");
    await page.getByLabel("Secret", { exact: true }).fill(canary);
    await page
      .getByRole("button", { name: "Review rotation", exact: true })
      .click();
    await page.getByRole("dialog").locator("button").last().click();
    await expect(page.getByText("Configured", { exact: true })).toBeVisible();
    expect(writes).toBe(1);
    await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    await expect(page.locator("body")).not.toContainText(canary);
    await capture(page, "completed-detail");
    await nav();
    await expect(page.getByRole("table")).toContainText("Configured");
    await capture(page, "completed-list");
  });
}
