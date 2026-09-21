import { expect, type BrowserContext, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { assertSecretAbsent, waitForLifecycle } from "./shared.ts";

export async function runHTTPCredentials(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  let credentialMutations = 0;
  page.on("request", (request) => {
    if (
      request.url().startsWith(`${baseURL}/api/v2/http/credentials`) &&
      request.method() !== "GET"
    )
      credentialMutations += 1;
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page
    .locator('#primary-navigation a[href="#/http/credentials"]')
    .click();
  await expect(
    page.getByText("No HTTP credentials", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("link", { name: "Create HTTP credential", exact: true })
    .click();
  await page
    .getByLabel("Name", { exact: true })
    .fill("Example HTTP credential");
  const maximumHost = [
    "a".repeat(63),
    "b".repeat(63),
    "c".repeat(63),
    "d".repeat(61),
  ].join(".");
  const wildcard = page.getByRole("checkbox", {
    name: "Allow the explicit *. subdomain boundary",
  });
  for (const [host, allowWildcard] of [
    [maximumHost + "x", false],
    ["é".repeat(127), false],
    ["*." + maximumHost + "x", true],
  ] as const) {
    await page.getByLabel("HTTPS destination host").fill(host);
    await wildcard.setChecked(allowWildcard);
    await page.getByLabel("Secret", { exact: true }).fill("host-bound-canary");
    await page
      .getByRole("button", { name: "Review create", exact: true })
      .click();
    await expect(
      page.getByRole("alert").filter({ hasText: "Check credential fields" }),
    ).toBeVisible();
    await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(credentialMutations).toBe(0);
  }
  for (const [host, allowWildcard] of [
    [maximumHost, false],
    ["*." + maximumHost, true],
  ] as const) {
    await page.getByLabel("HTTPS destination host").fill(host);
    await wildcard.setChecked(allowWildcard);
    await page.getByLabel("Secret", { exact: true }).fill("host-bound-canary");
    await page
      .getByRole("button", { name: "Review create", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    expect(credentialMutations).toBe(0);
  }
  await wildcard.uncheck();
  await page.getByLabel("HTTPS destination host").fill("api.example.com");
  await page.getByLabel("Header name").fill("Host");
  await page
    .getByLabel("Secret", { exact: true })
    .fill("oversized-canary-" + "x".repeat(1 << 20));
  await page
    .getByRole("button", { name: "Review create", exact: true })
    .click();
  await expect(
    page.getByRole("alert").filter({ hasText: "Check credential fields" }),
  ).toBeVisible();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  expect(credentialMutations).toBe(0);
  await page
    .getByLabel("Secret", { exact: true })
    .fill("http-credential-rejected-canary");
  await page
    .getByRole("button", { name: "Review create", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create HTTP credential", exact: true })
    .click();
  await expect(
    page.getByRole("alert").filter({ hasText: /operation is invalid/i }),
  ).toBeVisible();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  await page.getByLabel("Header name").fill("Authorization");
  await page
    .getByLabel("Secret", { exact: true })
    .fill("http-credential-create-canary");
  await page
    .getByRole("button", { name: "Review create", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create HTTP credential", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Example HTTP credential", exact: true }),
  ).toBeVisible();
  await expect(page.locator('input[type="password"]')).toHaveValue("");
  const id = new URL(page.url()).hash.split("/").at(-1)!;
  const response = await fetch(`${baseURL}/api/v2/http/credentials/${id}`, {
    headers: { Authorization: `Bearer ${bearer}` },
    signal: AbortSignal.timeout(5000),
    redirect: "error",
  });
  expect(response.status).toBe(200);
  const created = await response.json();
  expect(created.available).toBe(true);
  expect(JSON.stringify(created)).not.toContain("canary");
  await page
    .getByLabel("Secret", { exact: true })
    .fill("http-credential-rotate-canary");
  await page
    .getByRole("button", { name: "Review rotate", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Rotate secret", exact: true })
    .click();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  let rotatedRevision = "";
  await expect
    .poll(async () => {
      const read = await fetch(`${baseURL}/api/v2/http/credentials/${id}`, {
        headers: { Authorization: `Bearer ${bearer}` },
        signal: AbortSignal.timeout(5000),
        redirect: "error",
      });
      rotatedRevision = (await read.json()).revision;
      return rotatedRevision;
    })
    .not.toBe(created.revision);
  await expect(
    page
      .locator(".fact-grid dd")
      .filter({ hasText: new RegExp(`^${rotatedRevision}$`) }),
  ).toBeVisible();
  await page.getByLabel("Header name").fill("Host");
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Edit boundary and recipe", exact: true })
    .click();
  await expect(
    page.getByRole("alert").filter({ hasText: /operation is invalid/i }),
  ).toBeVisible();
  await page.getByLabel("Header name").fill("Authorization");
  await page
    .getByLabel("Name", { exact: true })
    .fill("Renamed HTTP credential");
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Edit boundary and recipe", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Renamed HTTP credential", exact: true }),
  ).toBeVisible();
  // Projection-only reference fixture: real transactional deletion refusal is
  // covered by the SQLite/keyring owner. No HTTP grant administration exists yet.
  await page.route(`**/api/v2/http/credentials/${id}`, async (route) => {
    if (route.request().method() !== "GET") {
      await route.continue();
      return;
    }
    const actual = await route.fetch();
    const resource = await actual.json();
    resource.referencing_grants = [{ id: "01ARZ3NDEKTSV4RRFFQ69G5FAW" }];
    await route.fulfill({ response: actual, json: resource });
  });
  await page.reload();
  await waitForLifecycle(page, "authenticated");
  await expect(
    page.getByRole("button", { name: "Review delete", exact: true }),
  ).toBeDisabled();
  const screenshots = await mkdtemp(
    join(tmpdir(), "gateway-http-credentials-"),
  );
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.screenshot({
    path: join(screenshots, "desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({
    path: join(screenshots, "narrow.png"),
    fullPage: true,
  });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= window.innerWidth,
    ),
  ).toBe(true);
  await page.unroute(`**/api/v2/http/credentials/${id}`);
  await page.reload();
  await waitForLifecycle(page, "authenticated");
  await page
    .getByRole("button", { name: "Review delete", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Delete credential", exact: true })
    .click();
  await expect(
    page.getByText("No HTTP credentials", { exact: true }),
  ).toBeVisible();
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [
      bearer,
      "http-credential-rejected-canary",
      "http-credential-create-canary",
      "http-credential-rotate-canary",
    ],
    true,
  );
  process.stdout.write(
    JSON.stringify({
      event: "http_credentials_complete",
      chromium_version: browserVersion,
      requests: requestCount(),
      screenshots,
    }) + "\n",
  );
}
