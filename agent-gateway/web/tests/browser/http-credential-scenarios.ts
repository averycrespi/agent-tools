import { captureStateFeedback } from "./state-feedback.ts";
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
  const screenshots = await mkdtemp(
    join(tmpdir(), "gateway-http-credentials-"),
  );
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
    .getByRole("link", { name: "Create credential", exact: true })
    .click();
  await expect(
    page.getByRole("region", { name: "Credential configuration" }),
  ).toBeVisible();
  for (const [name, width, height] of [
    ["create-desktop", 1280, 900],
    ["create-narrow", 390, 844],
  ] as const) {
    await page.setViewportSize({ width, height });
    await page.screenshot({
      path: join(screenshots, `${name}.png`),
      fullPage: true,
    });
  }
  await page.setViewportSize({ width: 1280, height: 900 });
  await page
    .getByLabel("Name", { exact: true })
    .fill("Example HTTP credential");
  const maximumHost = [
    "a".repeat(63),
    "b".repeat(63),
    "c".repeat(63),
    "d".repeat(61),
  ].join(".");
  const wildcard = page.getByRole("switch", {
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
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    await expect(
      page.getByRole("alert").filter({ hasText: "Check credential fields" }),
    ).toBeVisible();
    await expect(page.getByRole("alert")).toContainText(
      "HTTPS destination host must be nonempty and at most 253 bytes, excluding *.",
    );
    await expect(page.getByRole("alert")).toContainText(
      "Enter the secret again; it was cleared.",
    );
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
      .getByRole("button", { name: "Review and create", exact: true })
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
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  await expect(
    page.getByRole("alert").filter({ hasText: "Check credential fields" }),
  ).toBeVisible();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  expect(credentialMutations).toBe(0);
  await expect(page.getByRole("alert")).toContainText(
    "Secret and fixed prefix must total at most 4096 characters.",
  );
  await captureStateFeedback(page, "http-credential-invalid");
  for (const [label, invalid, valid, message] of [
    [
      "Name",
      "x".repeat(257),
      "Example HTTP credential",
      "Name must be nonempty and at most 256 bytes.",
    ],
    [
      "Header name",
      "Invalid header",
      "Host",
      "Header name must contain 1–128 HTTP token characters.",
    ],
    [
      "Fixed prefix (optional)",
      "é",
      "Bearer ",
      "Fixed prefix must contain at most 128 printable ASCII characters.",
    ],
    ["Secret", "é", "", "Secret must contain only printable ASCII characters."],
  ]) {
    await page.getByLabel("Secret", { exact: true }).fill("validation-canary");
    await page.getByLabel(label!, { exact: true }).fill(invalid!);
    await page
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    await expect(page.getByRole("alert")).toContainText(message!);
    await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    expect(credentialMutations).toBe(0);
    await page.getByLabel(label!, { exact: true }).fill(valid!);
  }
  await page
    .getByLabel("Secret", { exact: true })
    .fill("http-credential-rejected-canary");
  await page
    .getByRole("button", { name: "Review and create", exact: true })
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
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create HTTP credential", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Example HTTP credential", exact: true }),
  ).toBeVisible();
  await expect(page.locator('input[type="password"]')).toHaveValue("");
  await expect(page.locator("#page-title")).toHaveText(
    "HTTP Credential details",
  );
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
    .getByRole("button", { name: "Review rotation", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText(
    "Once replacement starts, failure may leave this credential unavailable; replacement does not fall back to the old secret.",
  );
  await captureStateFeedback(page, "rotation-review");
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
  // Projection-only reference fixture: transactional reference enforcement
  // remains covered by the SQLite owner.
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
    page.getByRole("button", { name: "Review deletion", exact: true }),
  ).toBeDisabled();
  await expect(page.getByLabel("Header name")).toHaveAttribute("readonly", "");
  await expect(page.getByLabel("Fixed prefix (optional)")).toHaveAttribute(
    "readonly",
    "",
  );
  await expect(
    page.getByText("Header recipe is fixed while referenced."),
  ).toBeVisible();
  await expect(page.getByLabel("HTTPS destination host")).toBeEditable();
  await expect(page.getByLabel("Port", { exact: true })).toBeEditable();
  await expect(
    page.getByRole("heading", { name: "Edit boundary", exact: true }),
  ).toBeVisible();
  await page.getByLabel("Name", { exact: true }).fill("Referenced credential");
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Edit boundary", exact: true })
    .click();
  await expect(
    page.getByRole("heading", { name: "Referenced credential", exact: true }),
  ).toBeVisible();
  await captureStateFeedback(page, "referenced-credential");
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
  let rotationAttempts = 0;
  await page.route(`**/api/v2/http/credentials/${id}/rotate`, async (route) => {
    rotationAttempts += 1;
    if (rotationAttempts === 1) {
      await route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        json: {
          status: 400,
          code: "invalid_operation",
          title: "The operation is invalid.",
        },
      });
    } else {
      await route.abort("failed");
    }
  });
  for (const outcome of ["rejected", "unknown"]) {
    await page
      .getByLabel("Secret", { exact: true })
      .fill("rotation-outcome-canary");
    await page
      .getByRole("button", { name: "Review rotation", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Rotate secret", exact: true })
      .click();
    await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    await expect(
      page.getByText(
        outcome === "rejected"
          ? "The operation is invalid."
          : "Credential change outcome unknown",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(page.getByText("Configured", { exact: true })).toBeVisible();
    if (outcome === "unknown") {
      await expect(
        page.getByRole("button", { name: "Review rotation", exact: true }),
      ).toBeDisabled();
      await captureStateFeedback(page, "rotation-unknown");
    }
  }
  expect(rotationAttempts).toBe(2);
  await page.unroute(`**/api/v2/http/credentials/${id}/rotate`);
  await page.unroute(`**/api/v2/http/credentials/${id}`);
  await page.route(`**/api/v2/http/credentials/${id}`, async (route) => {
    if (route.request().method() === "GET") await route.abort("failed");
    else await route.continue();
  });
  await page.reload();
  await waitForLifecycle(page, "authenticated");
  await expect(
    page.getByText("HTTP credential data unavailable", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("Refresh to load the credential.", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(/Do not replay an uncertain mutation/),
  ).toHaveCount(0);
  await captureStateFeedback(page, "http-credential-read-error");
  await page.unroute(`**/api/v2/http/credentials/${id}`);
  await page.reload();
  await waitForLifecycle(page, "authenticated");
  await page
    .getByRole("button", { name: "Review deletion", exact: true })
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
