import AxeBuilder from "@axe-core/playwright";
import { type BrowserContext, type Page } from "@playwright/test";
import { assertSecretAbsent, fail, waitForLifecycle } from "./shared.ts";
import { assertSensitiveSinkFoundation } from "./foundations.ts";
import { createHash } from "node:crypto";
import {
  visualArtifactInventory,
  visualDestinations,
  visualRubric,
  visualStates,
} from "../visual-matrix.ts";

export async function runVisualAccessibilityPrivacyCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await assertSensitiveSinkFoundation();
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.locator('[data-testid="theme-preference"]').selectOption("dark");
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: "reduce", colorScheme: "dark" });
  await page.waitForFunction(
    () => document.documentElement.dataset.theme === "dark",
  );
  const axe = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
    .analyze();
  const blocking = axe.violations.filter(
    (violation) =>
      violation.impact === "serious" || violation.impact === "critical",
  );
  if (blocking.length !== 0)
    fail(
      `Visual/accessibility/privacy canary findings: ${blocking.map((violation) => violation.id).join(",")}`,
    );
  const screenshot = await page.screenshot({
    fullPage: true,
    animations: "disabled",
  });
  if (
    screenshot.readUInt32BE(16) !== 390 ||
    screenshot.readUInt32BE(20) < 844 ||
    screenshot.includes(Buffer.from(bearer)) ||
    visualArtifactInventory.length !== 48 ||
    visualStates.length !== 10 ||
    visualRubric.length !== 6
  )
    fail("Visual/accessibility/privacy canary inventory changed");
  const layout = await page.evaluate(() => ({
    overflow:
      document.documentElement.scrollWidth >
      document.documentElement.clientWidth,
    externalAssets: [
      ...document.querySelectorAll<HTMLScriptElement | HTMLLinkElement>(
        "script[src],link[href]",
      ),
    ].some((element) => {
      const target =
        element instanceof HTMLScriptElement ? element.src : element.href;
      return new URL(target).origin !== window.location.origin;
    }),
  }));
  if (layout.overflow || layout.externalAssets)
    fail(
      "Visual/accessibility/privacy canary layout or asset boundary changed",
    );
  await assertSecretAbsent(page, context, baseURL, [bearer], true, "dark");
  process.stdout.write(
    `${JSON.stringify({ event: "visual_accessibility_privacy_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), axe_findings: 0, inventory: visualArtifactInventory.length, screenshot_sha256: createHash("sha256").update(screenshot).digest("hex") })}\n`,
  );
}

export async function runSecretStoragePrivacy(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await assertSensitiveSinkFoundation();
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  const screenshot = await page.screenshot({
    fullPage: true,
    animations: "disabled",
  });
  if (screenshot.includes(Buffer.from(bearer)))
    fail("bearer reached screenshot bytes");

  const state = await page.evaluate(() => ({
    fragment: window.location.hash,
    referrer: document.referrer,
    opener: window.opener,
    resources: performance
      .getEntriesByType("resource")
      .map((entry) => entry.name),
    attributes: [...document.querySelectorAll("*")].flatMap((element) =>
      [...element.attributes].map(
        (attribute) => `${attribute.name}=${attribute.value}`,
      ),
    ),
  }));
  if (
    !/^#\/[a-z/-]+(?:\?[a-z=&-]+)?$/.test(state.fragment) ||
    state.fragment.includes(bearer) ||
    state.referrer !== "" ||
    state.opener !== null ||
    state.resources.some((value) => value.includes(bearer)) ||
    state.attributes.some((value) => value.includes(bearer))
  )
    fail(
      "browser URL, resource, attribute, opener, or referrer boundary changed",
    );

  const oauthCanary = `oauth_privacy_${"P".repeat(32)}`;
  await context.route("**/__privacy_oauth_target**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/html",
      headers: { "Referrer-Policy": "no-referrer" },
      body: "<!doctype html><title>Privacy target</title>",
    });
  });
  await page.evaluate(
    (target) => {
      const button = document.createElement("button");
      button.type = "button";
      button.dataset.testid = "privacy-oauth";
      button.textContent = "Open privacy target";
      button.addEventListener("click", () => {
        const opened = window.open(target, "_blank", "noopener,noreferrer");
        if (opened !== null) opened.opener = null;
      });
      document.body.append(button);
    },
    `${new URL(baseURL).origin}/__privacy_oauth_target?state=${oauthCanary}`,
  );
  const popupPromise = context.waitForEvent("page");
  await page.locator('[data-testid="privacy-oauth"]').click();
  const popup = await popupPromise;
  await popup.waitForLoadState("domcontentloaded");
  if (
    (await popup.evaluate(() => window.opener)) !== null ||
    (await popup.evaluate(() => document.referrer)) !== ""
  )
    fail("OAuth privacy target retained opener or referrer");
  await popup.close();
  await page
    .locator('[data-testid="privacy-oauth"]')
    .evaluate((element) => element.remove());

  await assertSecretAbsent(page, context, baseURL, [bearer, oauthCanary], true);
  process.stdout.write(
    `${JSON.stringify({ event: "secret_storage_privacy_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), sinks: 18, screenshot_sha256: createHash("sha256").update(screenshot).digest("hex") })}\n`,
  );
}

export async function runVisualResponsiveMatrix(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const artifactIDs = new Set(
    visualArtifactInventory.map((artifact) => artifact.id),
  );
  if (
    visualDestinations.length !== 8 ||
    visualStates.length !== 10 ||
    visualArtifactInventory.length !== 48 ||
    artifactIDs.size !== visualArtifactInventory.length ||
    visualRubric.length !== 6 ||
    visualArtifactInventory.some((artifact) => artifact.secretBearing)
  )
    fail("visual qualification inventory changed shape");

  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="theme-preference"]').selectOption("light");
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.emulateMedia({ reducedMotion: "reduce", colorScheme: "light" });
  const signedOut = await page.screenshot({
    fullPage: true,
    animations: "disabled",
  });

  const ids = ["01ARZ3NDEKTSV4RRFFQ69G5FA0", "01ARZ3NDEKTSV4RRFFQ69G5FA1"];
  await page.route("**/api/v1/admin-credentials**", async (route) => {
    const request = route.request();
    if (
      request.method() !== "GET" ||
      new URL(request.url()).pathname !== "/api/v1/admin-credentials"
    )
      fail("unexpected visual credential request");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: ids.map((id, index) => ({
          id,
          fingerprint: `${String(index + 1).padStart(16, "0")}`,
          created_at: "2026-08-28T12:00:00Z",
          expires_at: null,
          non_expiring: true,
          status: "active",
          revision: "1",
        })),
        next_cursor: null,
      }),
    });
  });
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.locator('[data-testid="admin-credential-row"]').first().waitFor();
  await page.locator('[data-testid="admin-credential-create"]').waitFor();
  await page.locator('[data-testid="theme-preference"]').selectOption("dark");
  await page.setViewportSize({ width: 390, height: 844 });
  const firstRevoke = page
    .locator('[data-testid="admin-credential-revoke"]')
    .first();
  await firstRevoke.click();
  await page
    .getByRole("dialog", { name: "Revoke administrator credential?" })
    .waitFor();
  const confirmation = await page.screenshot({
    fullPage: true,
    animations: "disabled",
  });

  const inspectPNG = (image: Buffer, width: number, minimumHeight: number) => {
    if (image.length < 24 || image.subarray(1, 4).toString("ascii") !== "PNG")
      fail("visual artifact was not a PNG");
    const actualWidth = image.readUInt32BE(16);
    const actualHeight = image.readUInt32BE(20);
    if (actualWidth !== width || actualHeight < minimumHeight)
      fail(
        `visual artifact dimensions changed: ${actualWidth}x${actualHeight}, expected ${width}x>=${minimumHeight}`,
      );
    return createHash("sha256").update(image).digest("hex");
  };
  const desktopDigest = inspectPNG(signedOut, 1440, 900);
  const mobileDigest = inspectPNG(confirmation, 390, 844);
  if (desktopDigest === mobileDigest)
    fail("representative visual artifacts were not distinct");

  const layout = await page.evaluate(() => {
    const root = document.documentElement;
    const tableRegions = [
      ...document.querySelectorAll<HTMLElement>(".table-region"),
    ];
    const danger = getComputedStyle(
      document.querySelector<HTMLElement>(".danger-action")!,
    );
    const normal = getComputedStyle(
      document.querySelector<HTMLElement>(
        '[data-testid="admin-credential-create"]',
      )!,
    );
    return {
      pageOverflow: root.scrollWidth > root.clientWidth,
      tableOverflowOwned: tableRegions.every(
        (region) => getComputedStyle(region).overflowX === "auto",
      ),
      actionsDistinct:
        danger.color !== normal.color ||
        danger.backgroundColor !== normal.backgroundColor,
      wrappedIdentifiers: [...document.querySelectorAll("code")].every(
        (node) => node.getBoundingClientRect().right <= root.clientWidth + 1,
      ),
      offenders: [...document.querySelectorAll<HTMLElement>("body *")]
        .filter(
          (node) => node.getBoundingClientRect().right > root.clientWidth + 1,
        )
        .slice(0, 8)
        .map((node) => `${node.tagName.toLowerCase()}.${node.className}`),
    };
  });
  if (
    layout.pageOverflow ||
    !layout.tableOverflowOwned ||
    !layout.actionsDistinct ||
    !layout.wrappedIdentifiers
  )
    fail(`visual rubric failed: ${JSON.stringify(layout)}`);

  await page.keyboard.press("Escape");
  await page.setViewportSize({ width: 320, height: 800 });
  if (
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    )
  )
    fail("320 CSS pixel reflow caused page overflow");
  const narrowAdminActionsFit = await page.evaluate(() => {
    const region = document.querySelector<HTMLElement>(".table-region");
    const actions = [
      ...document.querySelectorAll<HTMLElement>(
        '[data-testid="admin-credential-revoke"]',
      ),
    ];
    if (region === null || actions.length === 0) return false;
    region.scrollLeft = region.scrollWidth;
    const bounds = region.getBoundingClientRect();
    return actions.every((action) => {
      const actionBounds = action.getBoundingClientRect();
      return (
        actionBounds.left >= bounds.left - 1 &&
        actionBounds.right <= bounds.right + 1
      );
    });
  });
  if (!narrowAdminActionsFit)
    fail("320 CSS pixel admin actions were clipped inside the table");

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=resource-limits";
  });
  await page.locator('[data-testid="system-limit-row"]').first().waitFor();
  const narrowResourceNamesFit = await page.evaluate(() => {
    const names = [
      ...document.querySelectorAll<HTMLElement>(
        '[data-testid="system-limit-row"] th[scope="row"]',
      ),
    ];
    return (
      names.length > 0 &&
      names.every((name) => {
        const style = getComputedStyle(name);
        return (
          style.textOverflow !== "ellipsis" && style.whiteSpace !== "nowrap"
        );
      })
    );
  });
  if (!narrowResourceNamesFit)
    fail("320 CSS pixel resource names lost visible information");

  await page.setViewportSize({ width: 720, height: 450 });
  const zoomLayout = await page.evaluate(() => ({
    client: document.documentElement.clientWidth,
    scroll: document.documentElement.scrollWidth,
    offenders: [...document.querySelectorAll<HTMLElement>("body *")]
      .filter(
        (node) =>
          node.getBoundingClientRect().right >
          document.documentElement.clientWidth + 1,
      )
      .slice(0, 8)
      .map((node) => `${node.tagName.toLowerCase()}.${node.className}`),
  }));
  if (zoomLayout.scroll > zoomLayout.client)
    fail(
      `pinned 200 percent reference layout caused page overflow: ${JSON.stringify(zoomLayout)}`,
    );

  await assertSecretAbsent(page, context, baseURL, [bearer], true, "dark");
  process.stdout.write(
    `${JSON.stringify({
      event: "visual_responsive_matrix_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      inventory: visualArtifactInventory.length,
      states: visualStates.length,
      rubric: visualRubric.length,
      screenshots: [
        { id: "signed-out-light-desktop", sha256: desktopDigest },
        { id: "confirmation-dark-mobile", sha256: mobileDigest },
      ],
    })}\n`,
  );
}

export async function runAccessibilityKeyboardResponsive(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  let axeScans = 0;
  let scriptedAssertions = 0;
  const scan = async (state: string) => {
    await page.waitForFunction(() =>
      document
        .getAnimations()
        .every((animation) => animation.playState === "finished"),
    );
    const result = await new AxeBuilder({ page })
      .withTags([
        "wcag2a",
        "wcag2aa",
        "wcag21a",
        "wcag21aa",
        "wcag22aa",
        "best-practice",
      ])
      .analyze();
    axeScans += 1;
    const blocking = result.violations.filter(
      (violation) =>
        violation.impact === "serious" || violation.impact === "critical",
    );
    if (blocking.length !== 0) {
      fail(
        `accessibility ${state} findings: ${blocking
          .map(
            (violation) =>
              `${violation.id}:${violation.impact}:${violation.nodes
                .slice(0, 3)
                .map(
                  (node) =>
                    `${node.target.join(" ")}(${node.failureSummary ?? ""})`,
                )
                .join("|")}`,
          )
          .join(",")}`,
      );
    }
  };

  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]:enabled').waitFor();
  await scan("signed-out");
  await page.keyboard.press("Tab");
  const skip = page.getByRole("link", { name: "Skip to main content" });
  if (!(await skip.evaluate((node) => node === document.activeElement)))
    fail("skip link was not first in keyboard order");
  await page.keyboard.press("Enter");
  if (
    !(await page
      .locator("#page-title")
      .evaluate((node) => node === document.activeElement))
  )
    fail("skip link did not route focus to the page title");
  scriptedAssertions += 2;

  const invalidBearer = `${bearer.slice(0, -1)}${bearer.endsWith("X") ? "Y" : "X"}`;
  await page.locator('[data-testid="admin-bearer-input"]').fill(invalidBearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await page.locator('[data-testid="session-message"][role="alert"]').waitFor();
  await scan("sign-in-error");
  scriptedAssertions += 1;

  const ids = ["01ARZ3NDEKTSV4RRFFQ69G5FA0", "01ARZ3NDEKTSV4RRFFQ69G5FA1"];
  await page.route("**/api/v1/admin-credentials**", async (route) => {
    const request = route.request();
    if (
      request.method() !== "GET" ||
      new URL(request.url()).pathname !== "/api/v1/admin-credentials"
    )
      fail("unexpected accessibility credential request");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: ids.map((id, index) => ({
          id,
          fingerprint: `${String(index + 1).padStart(16, "0")}`,
          created_at: "2026-08-28T12:00:00Z",
          expires_at: null,
          non_expiring: true,
          status: "active",
          revision: "1",
        })),
        next_cursor: null,
      }),
    });
  });
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-row"]').first().waitFor();
  await page.locator('[data-testid="admin-credential-create"]').waitFor();
  await page.getByLabel("Fingerprint or ID", { exact: true }).fill(ids[0]!);
  await page.waitForFunction(
    (id) =>
      window.location.hash ===
      `#/system?tab=admin-credentials&filter_identity=${id}`,
    ids[0],
  );
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  await page.getByRole("button", { name: "Reset" }).click();
  await scan("credential-table");

  const table = page.locator(
    '.table-region[role="region"][aria-label="Admin credentials"]',
  );
  if (
    (await table.locator('th[scope="col"]').count()) !== 6 ||
    (await table.locator('th[scope="row"]').count()) !== 2
  )
    fail("credential table semantics changed");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.locator('[data-testid="admin-credential-create-view"]').waitFor();
  const expiry = page.locator('[data-testid="admin-credential-expiry"]');
  await expiry.fill("invalid");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.getByText(/Expiry must be an RFC 3339 time/).waitFor();
  if (
    (await expiry.getAttribute("aria-invalid")) !== "true" ||
    !(await expiry.getAttribute("aria-describedby"))?.includes(
      "admin-credential-expiry-error",
    )
  )
    fail("field error was not associated with the expiry input");
  await scan("form-error");
  await expiry.fill("");
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  scriptedAssertions += 3;

  const firstRevoke = page
    .locator('[data-testid="admin-credential-revoke"]')
    .first();
  await firstRevoke.focus();
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", {
    name: "Revoke administrator credential?",
  });
  await dialog.waitFor();
  if (!(await dialog.evaluate((node) => node.contains(document.activeElement))))
    fail("destructive dialog did not receive focus");
  await page.keyboard.press("Tab");
  if (!(await dialog.evaluate((node) => node.contains(document.activeElement))))
    fail("destructive dialog did not trap keyboard focus");
  await scan("destructive-dialog");
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  if (!(await firstRevoke.evaluate((node) => node === document.activeElement)))
    fail("dialog did not restore the invoking control focus");
  scriptedAssertions += 3;

  await page.setViewportSize({ width: 390, height: 844 });
  const toggle = page.locator('[data-testid="navigation-toggle"]');
  await toggle.waitFor({ state: "visible" });
  await toggle.focus();
  await page.keyboard.press("Space");
  try {
    await page.waitForFunction(
      () =>
        document
          .querySelector('[data-testid="navigation-toggle"]')
          ?.getAttribute("aria-expanded") === "true",
      undefined,
      { timeout: 3000 },
    );
  } catch {
    fail(
      `mobile navigation did not expose its state: ${await page.evaluate(() => JSON.stringify({ expanded: document.querySelector('[data-testid="navigation-toggle"]')?.getAttribute("aria-expanded"), active: document.activeElement?.outerHTML, dialogs: [...document.querySelectorAll("dialog[open]")].map((dialog) => dialog.outerHTML) }))}`,
    );
  }
  const systemLink = page.locator('#primary-navigation a[href="#/system"]');
  if ((await systemLink.getAttribute("aria-current")) !== "page")
    fail("current navigation state relied on color alone");
  for (const target of [
    toggle,
    systemLink,
    page.locator('[data-testid="admin-credential-create"]'),
  ]) {
    const box = await target.boundingBox();
    if (box === null || box.width < 24 || box.height < 24)
      fail("interactive target is smaller than 24 CSS pixels");
  }
  await page.emulateMedia({ reducedMotion: "reduce" });
  if (
    !(await page.evaluate(
      () => matchMedia("(prefers-reduced-motion: reduce)").matches,
    ))
  )
    fail("reduced-motion preference was not active");
  const motion = await page
    .locator(".panel")
    .first()
    .evaluate((node) => {
      const style = getComputedStyle(node);
      return `${style.animationDuration},${style.transitionDuration}`;
    });
  if (
    !motion
      .split(",")
      .every(
        (value) => value === "0s" || value === "0.01ms" || value === "1e-05s",
      )
  )
    fail(`reduced-motion timing remained active: ${motion}`);
  scriptedAssertions += 6;

  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, invalidBearer],
    true,
  );
  process.stdout.write(
    `${JSON.stringify({ event: "accessibility_keyboard_responsive_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), axe_scans: axeScans, serious_critical: 0, scripted_assertions: scriptedAssertions })}\n`,
  );
}

export async function runSecretSinks(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await assertSensitiveSinkFoundation();
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  if ((await page.locator("dialog.sensitive-dialog[open]").count()) !== 0)
    fail("sensitive sink opened without preparation");

  const origin = new URL(baseURL).origin;
  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin,
  });
  const clipboardCanary = `mgw_agent_${"C".repeat(43)}`;
  await page.evaluate((value) => {
    const button = document.createElement("button");
    button.type = "button";
    button.dataset.testid = "clipboard-gesture";
    button.textContent = "Copy test value";
    button.addEventListener("click", () => {
      void navigator.clipboard.writeText(value).then(() => {
        button.dataset.complete = "true";
      });
    });
    document.body.append(button);
  }, clipboardCanary);
  await page.locator('[data-testid="clipboard-gesture"]').click();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="clipboard-gesture"]')
        ?.getAttribute("data-complete") === "true",
  );
  if (
    (await page.evaluate(() => navigator.clipboard.readText())) !==
    clipboardCanary
  )
    fail("explicit user clipboard publication failed");
  await page.evaluate(async () => {
    await navigator.clipboard.writeText(
      "clipboard overwritten after sink test",
    );
    document.querySelector('[data-testid="clipboard-gesture"]')?.remove();
  });
  if (
    (await page.evaluate(() => navigator.clipboard.readText())) !==
    "clipboard overwritten after sink test"
  ) {
    fail("clipboard test canary remained after explicit overwrite");
  }

  const oauthCanary = `oauth_sink_${"D".repeat(32)}`;
  await context.route("**/__oauth_sink_target**", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "<!doctype html><title>Authorization target</title>",
      headers: { "Referrer-Policy": "no-referrer" },
    });
  });
  await page.evaluate((target) => {
    const button = document.createElement("button");
    button.type = "button";
    button.dataset.testid = "oauth-gesture";
    button.textContent = "Open test authorization";
    button.addEventListener("click", () => {
      const opened = window.open(target, "_blank", "noopener,noreferrer");
      if (opened !== null) opened.opener = null;
    });
    document.body.append(button);
  }, `${origin}/__oauth_sink_target?state=${oauthCanary}`);
  const popupPromise = context.waitForEvent("page");
  await page.locator('[data-testid="oauth-gesture"]').click();
  const popup = await popupPromise;
  await popup.waitForLoadState("domcontentloaded");
  if (
    (await popup.evaluate(() => window.opener)) !== null ||
    (await popup.evaluate(() => document.referrer)) !== ""
  ) {
    fail("OAuth user gesture retained opener or referrer authority");
  }
  await popup.close();
  await page
    .locator('[data-testid="oauth-gesture"]')
    .evaluate((element) => element.remove());
  await context.clearPermissions();
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, clipboardCanary, oauthCanary],
    true,
  );
  process.stdout.write(
    `${JSON.stringify({
      event: "secret_sinks_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}
