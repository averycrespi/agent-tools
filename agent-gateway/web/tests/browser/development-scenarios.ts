import { type BrowserContext, type Page, type Request } from "@playwright/test";
import { appendFile } from "node:fs/promises";
import {
  assertSecretAbsent,
  assertSessionCookieAbsent,
  bootstrap,
  createdCredential,
  fail,
  sessionRequest,
  waitForLifecycle,
} from "./shared.ts";
import { assertSessionFoundationEpochs } from "./foundations.ts";
import { resolve } from "node:path";

export async function runDevelopmentControlPlane(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await assertSessionFoundationEpochs();
  await waitForLifecycle(page, "signed_out");

  const observations = {
    eventStreams: 0,
    mutations: 0,
    safeReads: 0,
  };
  const observeControlPlane = (request: Request) => {
    const url = new URL(request.url());
    if (request.method() === "POST" && url.pathname === "/api/v1/events") {
      observations.eventStreams += 1;
    }
    if (
      request.method() === "POST" &&
      url.pathname === "/api/v1/admin-credentials"
    ) {
      observations.mutations += 1;
    }
    if (
      request.method() === "GET" &&
      url.pathname === "/api/v1/system-status"
    ) {
      observations.safeReads += 1;
    }
  };
  page.on("request", observeControlPlane);
  await page.route(
    "**/api/v1/events",
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": keepalive\n\n",
      }),
    { times: 1 },
  );

  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "reconnecting",
  );
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );
  if (observations.eventStreams !== 2) {
    fail("development application did not reconnect its POST event stream");
  }
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  const sessionCookies = (await context.cookies(baseURL)).filter(
    (cookie) => cookie.name === "mcp_gateway_session",
  );
  const cookieHostOnly =
    sessionCookies.length === 1 &&
    sessionCookies[0]?.domain === new URL(baseURL).hostname;
  if (!cookieHostOnly) fail("development session cookie was not host-only");

  const current = await bootstrap(page);
  if (current.status !== 200 || current.session === undefined) {
    fail("development session cookie bootstrap failed");
  }
  const status = await page.evaluate(async (csrfToken) => {
    const response = await fetch("/api/v1/system-status", {
      headers: { "X-CSRF-Token": csrfToken },
      credentials: "same-origin",
    });
    await response.arrayBuffer();
    return response.status;
  }, current.session.csrf_token);
  if (status !== 200) fail("development safe status read failed");

  const generationBeforeMutation = Number(
    await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-view-generation"),
  );
  const created = await sessionRequest(
    page,
    "/api/v1/admin-credentials",
    "POST",
    current.session.csrf_token,
    undefined,
    { expires_at: null },
  );
  if (created.status !== 201 || observations.mutations !== 1) {
    fail("development CSRF mutation was not submitted exactly once");
  }
  const oneTime = createdCredential(created.value);
  await page.waitForFunction(
    (generation) =>
      Number(
        document
          .querySelector('[data-testid="gateway-shell"]')
          ?.getAttribute("data-view-generation"),
      ) ===
      generation + 1,
    generationBeforeMutation,
  );
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, oneTime.bearer],
    true,
  );
  oneTime.bearer = "";

  const hmrResources = await page.evaluate(
    () =>
      performance
        .getEntriesByType("resource")
        .filter((entry) => new URL(entry.name).pathname === "/@vite/client")
        .length,
  );
  if (hmrResources < 1) fail("development shell did not load the HMR client");

  let releaseRead: (() => void) | undefined;
  const readBarrier = new Promise<void>((resolve) => {
    releaseRead = resolve;
  });
  let readIntercepted: (() => void) | undefined;
  const intercepted = new Promise<void>((resolve) => {
    readIntercepted = resolve;
  });
  let readDelivered: (() => void) | undefined;
  const delivered = new Promise<void>((resolve) => {
    readDelivered = resolve;
  });
  await page.route(
    "**/api/v1/system-status",
    async (route) => {
      const response = await route.fetch();
      readIntercepted?.();
      await readBarrier;
      await route.fulfill({ response });
      readDelivered?.();
    },
    { times: 1 },
  );
  await page.locator('[data-testid="manual-refresh"]').click();
  await intercepted;
  const logoutResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "DELETE" &&
      response.url().endsWith("/api/v1/admin-sessions/current"),
  );
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  if ((await logoutResponse).status() !== 204) {
    fail("development logout was rejected");
  }
  await waitForLifecycle(page, "signed_out");
  await assertSessionCookieAbsent(context, baseURL);
  releaseRead?.();
  await delivered;
  await waitForLifecycle(page, "signed_out");
  await page.waitForFunction(() => window.location.hash === "#/sign-in");
  await assertSecretAbsent(page, context, baseURL, [bearer], false);
  page.off("request", observeControlPlane);

  process.stdout.write(
    `${JSON.stringify({
      event: "development_control_plane_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      event_streams: observations.eventStreams,
      mutations: observations.mutations,
      safe_reads: observations.safeReads,
      hmr_resources: hmrResources,
      cookie_host_only: cookieHostOnly,
      epoch_fenced: true,
    })}\n`,
  );
}

export async function runDevelopmentLiveReload(
  browserVersion: string,
  page: Page,
  bearer: string,
  fixtureRoot: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );

  const reloadEvidence = {
    navigations: 0,
    bootstraps: 0,
    bootstrapResponses: 0,
  };
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) reloadEvidence.navigations += 1;
  });
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (
      request.method() === "POST" &&
      url.pathname === "/api/v1/admin-sessions/current"
    ) {
      reloadEvidence.bootstraps += 1;
    }
  });
  page.on("response", (response) => {
    const request = response.request();
    const url = new URL(response.url());
    if (
      request.method() === "POST" &&
      url.pathname === "/api/v1/admin-sessions/current" &&
      response.status() === 200
    ) {
      reloadEvidence.bootstrapResponses += 1;
    }
  });

  const stateCanary = "development-hmr-state";
  await page.evaluate((value) => {
    (
      window as Window & { __developmentHmrState?: string }
    ).__developmentHmrState = value;
  }, stateCanary);
  await appendFile(
    resolve(fixtureRoot, "src", "styles.css"),
    "\n:root { --mcp-development-hmr-probe: rgb(1, 2, 3); }\n",
    "utf8",
  );
  await page.waitForFunction(
    () =>
      getComputedStyle(document.documentElement)
        .getPropertyValue("--mcp-development-hmr-probe")
        .trim() === "rgb(1, 2, 3)",
  );
  const retainedState = await page.evaluate(
    () =>
      (window as Window & { __developmentHmrState?: string })
        .__developmentHmrState,
  );
  if (
    retainedState !== stateCanary ||
    reloadEvidence.navigations !== 0 ||
    reloadEvidence.bootstraps !== 0 ||
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-session-lifecycle")) !== "authenticated"
  ) {
    fail("CSS HMR did not preserve the authenticated page state");
  }

  const navigation = page.waitForEvent("framenavigated", {
    predicate: (frame) => frame === page.mainFrame(),
    timeout: 10_000,
  });
  await appendFile(
    resolve(fixtureRoot, "src", "location.ts"),
    "\n// development full-reload probe\n",
    "utf8",
  );
  await navigation;
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );
  if (
    Number(reloadEvidence.navigations) !== 1 ||
    Number(reloadEvidence.bootstraps) !== 1 ||
    Number(reloadEvidence.bootstrapResponses) !== 1
  ) {
    fail(
      `full reload evidence differed: navigations=${reloadEvidence.navigations} bootstraps=${reloadEvidence.bootstraps} responses=${reloadEvidence.bootstrapResponses}`,
    );
  }

  process.stdout.write(
    `${JSON.stringify({
      event: "development_live_reload_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      navigations: reloadEvidence.navigations,
      bootstraps: reloadEvidence.bootstraps,
    })}\n`,
  );
}
