import { expect, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

export async function exerciseUpstreamHeaders(page: Page): Promise<string[]> {
  const artifacts = await mkdtemp(join(tmpdir(), "upstream-headers-visual-"));
  const screenshots: string[] = [];
  const capture = async (state: string) => {
    for (const width of [1280, 390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      await page
        .getByRole("group", { name: "Custom HTTP headers" })
        .scrollIntoViewIfNeeded();
      const path = join(artifacts, `${state}-${width}.png`);
      await page.screenshot({ path });
      screenshots.push(path);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
    await page.setViewportSize({ width: 1280, height: 900 });
  };
  const value = "default,actions,gists,issues,labels,pull_requests,users";
  await page.evaluate(() => {
    location.hash = "#/servers/new";
  });
  await page.getByTestId("server-editor").waitFor();
  await page.locator("#server-namespace").fill("header-fixture");
  await page.locator("#server-display-name").fill("Header fixture");
  await page.locator("#server-transport-http").check();
  await page.locator("#server-url").fill("https://resource.example/mcp");
  await page.locator("#server-auth-none").check();
  await expect(
    page.getByText(
      "Non-secret values only. Headers are stored in plaintext and visible to administrators. Never enter tokens, API keys, passwords, or cookies.",
      { exact: true },
    ),
  ).toBeVisible();
  expect(
    await page
      .getByRole("group", { name: "Custom HTTP headers" })
      .evaluate((headers) => {
        const endpoint = document.getElementById("server-url");
        const protocol = document.getElementById("server-protocol-mode");
        return (
          endpoint !== null &&
          protocol !== null &&
          Boolean(
            endpoint.compareDocumentPosition(headers) &
            Node.DOCUMENT_POSITION_FOLLOWING,
          ) &&
          Boolean(
            headers.compareDocumentPosition(protocol) &
            Node.DOCUMENT_POSITION_FOLLOWING,
          )
        );
      }),
  ).toBe(true);
  await capture("create-empty");
  await page.getByTestId("server-header-add").click();
  await page.getByLabel("Header name 1", { exact: true }).fill("Authorization");
  await page
    .getByLabel("Header value 1", { exact: true })
    .fill("ordinary-fixture");
  await page.getByTestId("server-editor-submit").click();
  await expect(page.getByText(/Header 1: this name is reserved/)).toBeVisible();
  await capture("create-error");
  await page
    .getByLabel("Header name 1", { exact: true })
    .fill("X-MCP-Toolsets");
  await page.getByLabel("Header value 1", { exact: true }).fill(value);
  await page.getByTestId("server-header-add").click();
  await page
    .getByLabel("Header name 2", { exact: true })
    .fill("x-mcp-toolsets");
  await page.getByTestId("server-editor-submit").click();
  await expect(page.getByText(/Header 2: names must be unique/)).toBeVisible();
  await page
    .getByRole("button", { name: "Remove custom http headers row 2" })
    .click();
  await capture("create-populated");
  await page.getByTestId("server-editor-submit").click();
  await expect(page.getByTestId("server-creation-review")).toContainText(value);
  const createdResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === "/api/v1/servers",
  );
  await page.getByTestId("server-change-confirm-submit").click();
  const created = await createdResponse;
  expect(created.status()).toBe(201);
  expect(created.headers()["x-mcp-toolsets"]).toBeUndefined();
  const body = (await created.json()) as {
    server: { id: string; transport: { headers: Record<string, string> } };
  };
  expect(body.server.transport.headers).toEqual({ "X-MCP-Toolsets": value });
  await page.getByRole("link", { name: "Settings", exact: true }).click();
  await expect(page.getByLabel("Header value 1", { exact: true })).toHaveValue(
    value,
  );
  await capture("edit-populated");
  await page
    .getByLabel("Header value 1", { exact: true })
    .fill("default,issues");
  const save = async () => {
    await page.getByTestId("server-editor-submit").click();
    const response = page.waitForResponse(
      (item) =>
        item.request().method() === "PATCH" &&
        new URL(item.url()).pathname === `/api/v1/servers/${body.server.id}`,
    );
    await page.getByTestId("server-change-confirm-submit").click();
    const result = await response;
    expect(result.status()).toBe(200);
    await expect(page.getByTestId("toast")).toContainText(
      "Server settings saved",
    );
    await page.evaluate((id) => {
      location.hash = `#/servers/${id}?tab=settings`;
    }, body.server.id);
    await page
      .getByTestId("server-editor")
      .waitFor()
      .catch(async (error: unknown) => {
        const state = await page.evaluate(() => ({
          hash: location.hash,
          lifecycle: document
            .querySelector('[data-testid="gateway-shell"]')
            ?.getAttribute("data-session-lifecycle"),
          headings: [...document.querySelectorAll("h1,h2")].map(
            (node) => node.textContent,
          ),
        }));
        throw new Error(`Header save return failed: ${JSON.stringify(state)}`, {
          cause: error,
        });
      });
  };
  await save();
  await expect(page.getByLabel("Header value 1", { exact: true })).toHaveValue(
    "default,issues",
  );
  await page
    .getByRole("button", { name: "Remove custom http headers row 1" })
    .click();
  await capture("edit-cleared");
  await save();
  await expect(page.getByTestId("server-header-name")).toHaveCount(0);
  await page.reload();
  await expect(page.getByTestId("server-editor"))
    .toBeVisible()
    .catch(async (error: unknown) => {
      const state = await page.evaluate(() => ({
        hash: location.hash,
        lifecycle: document
          .querySelector('[data-testid="gateway-shell"]')
          ?.getAttribute("data-session-lifecycle"),
        headings: [...document.querySelectorAll("h1,h2")].map(
          (node) => node.textContent,
        ),
      }));
      throw new Error(`Header reload failed: ${JSON.stringify(state)}`, {
        cause: error,
      });
    });
  await expect(page.getByTestId("server-header-name")).toHaveCount(0);
  await page.evaluate(() => {
    location.hash = "#/servers";
  });
  await page.getByTestId("servers-view").waitFor();
  return screenshots;
}
