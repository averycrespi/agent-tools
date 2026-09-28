import { expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// Shares the real-Gateway HTTP grant scenario's fixture and bounded lifecycle.
export async function exerciseGrantDetails(
  page: Page,
  baseURL: string,
  grantID: string,
  mcpGrantID: string,
  credentialID: string,
  capture: (name: string) => Promise<void>,
): Promise<number[]> {
  const injectedFailures: number[] = [];
  await page.goto(`${baseURL}/#/mcp/grants/${mcpGrantID}`);
  await expect(
    page.getByRole("heading", { name: "Grant details", exact: true }),
  ).toBeVisible();
  await capture("comparison-mcp-grant");
  const url = `${baseURL}/api/v2/http/grants/${grantID}`;
  await page.goto(`${baseURL}/#/http/grants/${grantID}`);
  const edit = page.getByRole("button", { name: "Edit grant", exact: true });
  const editor = page.locator("#http-grant-editor");
  const description = page.getByLabel("Description (optional)");
  const review = page.getByRole("button", {
    name: "Review changes",
    exact: true,
  });
  const facts = page.getByRole("region", {
    name: "Grant details",
    exact: true,
  });
  let writes = 0;
  page.on("request", (request) => {
    if (request.url() === url && ["PATCH", "DELETE"].includes(request.method()))
      writes++;
  });
  await edit.focus();
  await page.keyboard.press("Enter");
  await expect(description).toBeFocused();
  await expect(description).toHaveValue("Test block_requests");
  await editor.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(editor).toHaveCount(0);
  await expect(edit).toBeFocused();
  expect(writes).toBe(0);
  for (const action of ["Add method", "Remove method"]) {
    await edit.click();
    await expect(
      page.getByRole("textbox", { name: "Method 1", exact: true }),
    ).toHaveValue("GET");
    await page.getByRole("button", { name: action, exact: true }).click();
    await expect(
      page.getByRole("textbox", { name: /^Method [0-9]+$/ }),
    ).toHaveCount(action === "Add method" ? 2 : 0);
    await editor.getByRole("button", { name: "Cancel", exact: true }).click();
    await expect(
      page.getByRole("dialog", { name: "Discard grant changes?" }),
    ).toBeVisible();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await page
      .getByRole("link", { name: "Back to HTTP grants", exact: true })
      .click();
    await expect(
      page.getByRole("dialog", { name: "Discard unsaved changes?" }),
    ).toBeVisible();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(
      page.getByRole("textbox", { name: /^Method [0-9]+$/ }),
    ).toHaveCount(action === "Add method" ? 2 : 0);
    await capture(
      action === "Add method"
        ? "detail-method-added-draft"
        : "detail-method-removed-draft",
    );
    await editor.getByRole("button", { name: "Cancel", exact: true }).click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Discard changes", exact: true })
      .click();
    await expect(edit).toBeFocused();
    expect(writes).toBe(0);
  }
  await edit.click();
  await description.fill("Unsaved draft");
  await page
    .getByRole("link", { name: "Back to HTTP grants", exact: true })
    .click();
  await expect(
    page.getByRole("dialog", { name: "Discard unsaved changes?" }),
  ).toBeVisible();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(description).toHaveValue("Unsaved draft");
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Test block_requests",
      exact: true,
    }),
  ).toBeVisible();
  await expect(facts).not.toContainText("Unsaved draft");
  await capture("detail-saved-versus-draft");
  await editor.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(
    page.getByRole("dialog", { name: "Discard grant changes?" }),
  ).toBeVisible();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(description).toHaveValue("Unsaved draft");
  await editor.getByRole("button", { name: "Cancel", exact: true }).click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Discard changes", exact: true })
    .click();
  await expect(edit).toBeFocused();
  expect(writes).toBe(0);
  await edit.click();
  await expect(description).toHaveValue("Test block_requests");
  await description.fill("Acknowledged grant");
  let release!: () => void;
  const gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  let handedOff = false;
  await page.route(url, async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    handedOff = true;
    await gate;
    await route.continue();
  });
  await review.click();
  await expect(
    page.getByRole("region", { name: "Current grant", exact: true }),
  ).toContainText("Test block_requests");
  await expect(
    page.getByRole("region", { name: "Proposed grant", exact: true }),
  ).toContainText("Acknowledged grant");
  await capture("detail-save-confirmation");
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Apply grant", exact: true })
    .click();
  await expect.poll(() => handedOff).toBe(true);
  await expect(
    page.getByRole("button", { name: "Applying grant…", exact: true }),
  ).toBeDisabled();
  await expect(
    editor.getByRole("button", { name: "Cancel", exact: true }),
  ).toBeDisabled();
  await expect(page.getByTestId("toast")).toHaveCount(0);
  expect(writes).toBe(1);
  await capture("detail-saving");
  release();
  await expect(page.getByTestId("toast")).toHaveText(/Grant saved/);
  await expect(page.getByTestId("toast")).toHaveAttribute("role", "status");
  await expect(page.getByTestId("toast")).toHaveAttribute(
    "aria-live",
    "polite",
  );
  await expect(editor).toHaveCount(0);
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Acknowledged grant",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Acknowledged grant",
      exact: true,
    }),
  ).toBeFocused();
  expect(writes).toBe(1);
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(page.getByTestId("toast")).toContainText("Grant saved");
  await capture("detail-save-success");
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.unroute(url);
  await page.reload();
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Acknowledged grant",
      exact: true,
    }),
  ).toBeVisible();
  await edit.click();
  await expect(description).toHaveValue("Acknowledged grant");
  await description.fill("Rejected draft");
  await page.route(url, async (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    injectedFailures.push(400);
    await route.fulfill({
      status: 400,
      contentType: "application/problem+json",
      json: {
        status: 400,
        code: "invalid_grant",
        title: "The grant is invalid.",
      },
    });
  });
  await review.click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Apply grant", exact: true })
    .click();
  await expect(
    page.getByText("Change not applied", { exact: true }),
  ).toBeVisible();
  await expect(description).toHaveValue("Rejected draft");
  await expect(page.getByTestId("toast")).toHaveCount(0);
  await capture("detail-save-rejected");
  expect(writes).toBe(2);
  await page.unroute(url);
  await description.fill("Saved despite read failure");
  let saved = false;
  await page.route(url, async (route) => {
    if (route.request().method() === "PATCH") {
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      saved = true;
      await route.fulfill({ response });
    } else if (saved) {
      injectedFailures.push(503);
      await route.fulfill({ status: 503, body: "" });
    } else await route.continue();
  });
  await review.click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Apply grant", exact: true })
    .click();
  await expect(
    page.getByText("Grant saved; refresh unavailable", { exact: true }),
  ).toBeVisible();
  await expect(page.getByTestId("toast")).toContainText("Grant saved");
  await expect(
    page.getByRole("heading", {
      level: 1,
      name: "Saved despite read failure",
      exact: true,
    }),
  ).toBeVisible();
  await expect(editor).toHaveCount(0);
  await expect(edit).toBeDisabled();
  expect(writes).toBe(3);
  await capture("detail-saved-read-failed");
  await page.unroute(url);
  await page.getByRole("button", { name: "Refresh current view" }).click();
  await expect(edit).toBeEnabled();
  await expect(
    page.getByText("Grant saved; refresh unavailable", { exact: true }),
  ).toHaveCount(0);
  expect(writes).toBe(3);
  await page.reload();
  // Faulted authoritative relationships/material never hide the saved policy.
  await page.route(`${baseURL}/api/v2/principals?**`, (route) => {
    injectedFailures.push(503);
    return route.fulfill({ status: 503, body: "" });
  });
  await page.route(url, async (route) => {
    const response = await route.fetch();
    const grant = await response.json();
    grant.description = null;
    grant.state = "expired";
    grant.created_at = "2020-01-01T00:00:00.000000000Z";
    grant.expires_at = "2021-01-01T00:00:00.000000000Z";
    await route.fulfill({ response, json: grant });
  });
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Unnamed HTTP grant", exact: true }),
  ).toBeVisible();
  await expect(facts).toContainText("Expired");
  await expect(
    facts.getByRole("link", { name: "Agent unavailable", exact: true }),
  ).toBeVisible();
  await capture("detail-unnamed-expired-unavailable");
  await page.setViewportSize({ width: 320, height: 800 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.unroute(`${baseURL}/api/v2/principals?**`);
  await page.unroute(url);
  const longName = "Long agent identity ".repeat(12).trim();
  const longDescription = "Long grant description ".repeat(10).trim();
  await page.route(`${baseURL}/api/v2/principals?**`, async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    for (const item of data.items) item.display_name = longName;
    await route.fulfill({ response, json: data });
  });
  let credentialLookup: "record" | "failed" | "missing" = "record";
  await page.route(`${baseURL}/api/v2/http/credentials?**`, async (route) => {
    if (credentialLookup === "failed") {
      injectedFailures.push(503);
      await route.fulfill({ status: 503, body: "" });
      return;
    }
    const response = await route.fetch();
    const data = await response.json();
    for (const item of data.items) item.available = false;
    if (credentialLookup === "missing") {
      data.items = [];
      data.total_count = 0;
    }
    await route.fulfill({ response, json: data });
  });
  await page.route(url, async (route) => {
    const response = await route.fetch();
    const data = await response.json();
    data.description = longDescription;
    data.policy.type = "allow_requests";
    data.policy.allow_private = false;
    data.policy.credential_id = credentialID;
    await route.fulfill({ response, json: data });
  });
  await page.reload();
  await expect(
    page.getByRole("heading", { name: longDescription, exact: true }),
  ).toBeVisible();
  await expect(
    facts.getByRole("link", { name: longName, exact: true }),
  ).toBeVisible();
  await expect(facts).toContainText(credentialID);
  await expect(facts).toContainText("Credential materialUnavailable");
  await capture("detail-long-unavailable-material");
  for (const fault of ["failed", "missing"] as const) {
    credentialLookup = fault;
    await page.getByRole("button", { name: "Refresh current view" }).click();
    const material = facts.locator("dl > div").filter({
      has: page.locator("dt", { hasText: /^Credential material$/ }),
    });
    await expect(material.locator("dd")).toHaveText("Unknown");
    await expect(facts).toContainText(credentialID);
    await capture(`detail-material-${fault}-lookup`);
  }
  await page.setViewportSize({ width: 320, height: 800 });
  expect(
    await page.evaluate(() => document.documentElement.scrollWidth),
  ).toBeLessThanOrEqual(320);
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.unroute(`${baseURL}/api/v2/principals?**`);
  await page.unroute(`${baseURL}/api/v2/http/credentials?**`);
  await page.unroute(url);
  await page.reload();
  await edit.click();
  const deletion = editor.getByRole("button", {
    name: "Delete grant",
    exact: true,
  });
  await deletion.focus();
  await page.keyboard.press("Enter");
  await expect(
    page.getByRole("dialog", { name: "Delete HTTP grant", exact: true }),
  ).toBeVisible();
  await capture("detail-delete-confirmation");
  expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(deletion).toBeFocused();
  expect(writes).toBe(3);
  await deletion.click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Delete grant", exact: true })
    .click();
  await expect(page).toHaveURL(`${baseURL}/#/http/grants`);
  expect(writes).toBe(4);
  await expect(
    page.getByRole("link", { name: "Saved despite read failure", exact: true }),
  ).toHaveCount(0);
  return injectedFailures;
}
