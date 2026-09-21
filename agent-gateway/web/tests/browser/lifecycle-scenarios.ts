import {
  expect,
  type BrowserContext,
  type Page,
  type Request,
} from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { exercisePendingRequests } from "./pending-requests.ts";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  assertClosedStorage,
  assertSecretAbsent,
  assertSessionCookieAbsent,
  bootstrap,
  browserStorage,
  connectAndCancelStream,
  createdCredential,
  exchange,
  expiryResponse,
  fail,
  loadShell,
  sessionFixture,
  sessionRequest,
  waitForLifecycle,
} from "./shared.ts";

export async function runSessionLifecycleCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const first = await exchange(page, bearer);
  const oldCookie = (await context.cookies(baseURL)).find(
    (cookie) => cookie.name === "agent_gateway_session",
  );
  if (oldCookie === undefined) fail("canonical session cookie missing");
  await context.clearCookies({ name: "agent_gateway_session" });
  await context.addCookies([{ ...oldCookie, name: "mcp_gateway_session" }]);
  const recoveryMutations: string[] = [];
  const observeRecovery = (request: Request) => {
    if (
      !["GET", "HEAD"].includes(request.method()) &&
      !request.url().endsWith("/api/v2/admin-sessions/current")
    )
      recoveryMutations.push(request.url());
  };
  page.on("request", observeRecovery);
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "signed_out");
  await assertSessionCookieAbsent(context, baseURL);
  page.off("request", observeRecovery);
  if (recoveryMutations.length !== 0)
    fail("old-only recovery replayed a mutation");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  const session = (await bootstrap(page)).session;
  if (session === undefined || session.csrf_token === first.csrf_token)
    fail("old-only browser did not establish fresh sign-in authority");
  await context.addCookies([{ ...oldCookie, name: "mcp_gateway_session" }]);
  const current = await bootstrap(page);
  if (
    current.status !== 200 ||
    current.session?.csrf_token !== session.csrf_token
  ) {
    fail("browser session bootstrap canary failed");
  }
  if (
    (await context.cookies(baseURL)).some(
      (cookie) => cookie.name === "mcp_gateway_session",
    )
  )
    fail("mixed bootstrap did not retire legacy cookie");
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "authenticated");
  await connectAndCancelStream(page, session.csrf_token);
  await context.addCookies([{ ...oldCookie, name: "mcp_gateway_session" }]);
  const logout = await sessionRequest(
    page,
    "/api/v2/admin-sessions/current",
    "DELETE",
    session.csrf_token,
    undefined,
    {},
  );
  if (logout.status !== 204) fail("browser session logout canary failed");
  await assertSessionCookieAbsent(context, baseURL);
  process.stdout.write(
    `${JSON.stringify({
      event: "session_lifecycle_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

export async function runPriorSessionResponseIsolationCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  assertClosedStorage(await browserStorage(page));
  await page.locator('[data-testid="theme-preference"]').selectOption("light");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );

  const fragmentCanary = `INVALID_FRAGMENT_${"F".repeat(40)}`;
  await page.evaluate((value) => {
    window.location.hash = `#/mcp/servers/${value}`;
  }, fragmentCanary);
  await page.waitForFunction(() => window.location.hash === "#/overview");
  if (
    (await page.locator("body").textContent())?.includes(fragmentCanary) ||
    (await page.locator("dialog.sensitive-dialog[open]").count()) !== 0
  ) {
    fail("invalid location reached text or opened a sensitive sink");
  }

  const lateCanary = `LATE_RESPONSE_${"L".repeat(40)}`;
  let releaseLogout: (() => void) | undefined;
  const logoutBarrier = new Promise<void>((resolve) => {
    releaseLogout = resolve;
  });
  let logoutIntercepted: (() => void) | undefined;
  const intercepted = new Promise<void>((resolve) => {
    logoutIntercepted = resolve;
  });
  let logoutSettled: (() => void) | undefined;
  const settled = new Promise<void>((resolve) => {
    logoutSettled = resolve;
  });
  await page.route(
    "**/api/v2/admin-sessions/current",
    async (route) => {
      if (route.request().method() !== "DELETE") {
        await route.continue();
        return;
      }
      logoutIntercepted?.();
      await logoutBarrier;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: {
          "Set-Cookie":
            "agent_gateway_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict",
        },
        body: JSON.stringify({
          status: 500,
          code: "dependency_unavailable",
          title: lateCanary,
        }),
      });
      logoutSettled?.();
    },
    { times: 1 },
  );
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await intercepted;
  await waitForLifecycle(page, "signed_out");
  releaseLogout?.();
  await settled;
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="session-message"]')
        ?.textContent?.includes("logout was not confirmed") === true,
  );
  if (
    (await page.locator("body").textContent())?.includes(lateCanary) ||
    (await page.locator("dialog[open]").count()) !== 0
  ) {
    fail("late prior-epoch response rendered or retained transient UI");
  }
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, fragmentCanary, lateCanary],
    false,
    "light",
  );
  process.stdout.write(
    `${JSON.stringify({
      event: "prior_session_response_isolation_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

export async function runProtocol(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  initialBearer: string,
  requestCount: () => number,
  readBoundedInput: () => Promise<unknown>,
): Promise<void> {
  const initialCredentials = await page.evaluate(async (bearer) => {
    const response = await fetch("/api/v2/admin-credentials", {
      headers: { Authorization: `Bearer ${bearer}` },
      credentials: "same-origin",
    });
    return {
      status: response.status,
      value: (await response.json()) as { items?: Array<{ id?: string }> },
    };
  }, initialBearer);
  const initialID = initialCredentials.value.items?.[0]?.id;
  if (initialCredentials.status !== 200 || initialID === undefined)
    fail("initial credential list failed");
  let session = await exchange(page, initialBearer);

  await page.reload({ waitUntil: "domcontentloaded" });
  const reloaded = await bootstrap(page);
  if (
    reloaded.status !== 200 ||
    reloaded.session?.csrf_token !== session.csrf_token
  )
    fail("reload bootstrap failed");
  const newTab = await context.newPage();
  await loadShell(newTab);
  const newTabBootstrap = await bootstrap(newTab);
  if (
    newTabBootstrap.status !== 200 ||
    newTabBootstrap.session?.csrf_token !== session.csrf_token
  ) {
    fail("new-tab bootstrap failed");
  }

  await connectAndCancelStream(page, session.csrf_token);
  const replacementResult = await sessionRequest(
    newTab,
    "/api/v2/admin-credentials",
    "POST",
    session.csrf_token,
    undefined,
    { expires_at: null },
  );
  if (replacementResult.status !== 201)
    fail("replacement credential creation failed");
  const replacement = createdCredential(replacementResult.value);
  await connectAndCancelStream(page, session.csrf_token);

  const expiringAt = new Date(Date.now() + 10 * 60 * 1000).toISOString();
  const expiringResult = await sessionRequest(
    page,
    "/api/v2/admin-credentials",
    "POST",
    session.csrf_token,
    undefined,
    { expires_at: expiringAt },
  );
  if (expiringResult.status !== 201)
    fail("expiring credential creation failed");
  let expiring = createdCredential(expiringResult.value);

  const logout = await sessionRequest(
    page,
    "/api/v2/admin-sessions/current",
    "DELETE",
    session.csrf_token,
    undefined,
    {},
  );
  if (logout.status !== 204) fail("logout failed");
  await assertSessionCookieAbsent(context, baseURL);

  await context.addCookies([
    {
      name: "mcp_gateway_session",
      value: "stale",
      url: baseURL,
      httpOnly: true,
      sameSite: "Strict",
    },
  ]);
  let staleStatus = 0;
  const staleResponse = await expiryResponse(page, async () => {
    staleStatus = (await bootstrap(page)).status;
  });
  const staleSetCookie = (await staleResponse.allHeaders())["set-cookie"] ?? "";
  if (
    staleStatus !== 401 ||
    !staleSetCookie.includes("mcp_gateway_session=") ||
    !staleSetCookie.includes("Max-Age=0")
  ) {
    fail("stale cookie did not receive exact clearing response");
  }
  await assertSessionCookieAbsent(context, baseURL);

  if (Date.parse(expiring.expires_at ?? "") !== Date.parse(expiringAt))
    fail("credential expiry was not preserved");
  const expiringSession = await exchange(page, expiring.bearer);
  expiring = { id: expiring.id, bearer: "", expires_at: expiring.expires_at };
  const expiringLogout = await sessionRequest(
    page,
    "/api/v2/admin-sessions/current",
    "DELETE",
    expiringSession.csrf_token,
    undefined,
    {},
  );
  if (expiringLogout.status !== 204) fail("expiring session logout failed");

  session = await exchange(page, initialBearer);
  const revoke = await sessionRequest(
    page,
    `/api/v2/admin-credentials/${initialID}`,
    "DELETE",
    session.csrf_token,
    undefined,
    {},
  );
  if (revoke.status !== 204) fail("parent credential revocation failed");
  let revokedStatus = 0;
  const revokedResponse = await expiryResponse(page, async () => {
    revokedStatus = (await bootstrap(page)).status;
  });
  const revokedSetCookie =
    (await revokedResponse.allHeaders())["set-cookie"] ?? "";
  if (revokedStatus !== 401 || !revokedSetCookie.includes("Max-Age=0"))
    fail("revoked session was not cleared");
  await assertSessionCookieAbsent(context, baseURL);

  session = await exchange(page, replacement.bearer);
  process.stdout.write('{"event":"restart_requested"}\n');
  const restart = await readBoundedInput();
  if (
    typeof restart !== "object" ||
    restart === null ||
    Array.isArray(restart) ||
    Object.keys(restart).sort().join(",") !== "event,version" ||
    !("version" in restart) ||
    restart.version !== 1 ||
    !("event" in restart) ||
    restart.event !== "gateway_restarted"
  ) {
    fail("invalid restart acknowledgement");
  }

  let restartStatus = 0;
  const restartResponse = await expiryResponse(page, async () => {
    restartStatus = (await bootstrap(page)).status;
  });
  const restartSetCookie =
    (await restartResponse.allHeaders())["set-cookie"] ?? "";
  session = { csrf_token: "", idle_expires_at: "", absolute_expires_at: "" };
  if (
    restartStatus !== 401 ||
    !restartSetCookie.includes("Max-Age=0") ||
    session.csrf_token !== ""
  ) {
    fail("restart did not fence old browser authority");
  }
  await assertSessionCookieAbsent(context, baseURL);

  session = await exchange(page, replacement.bearer);
  replacement.bearer = "";
  const recovered = await bootstrap(page);
  if (
    recovered.status !== 200 ||
    recovered.session?.csrf_token !== session.csrf_token
  )
    fail("restart recovery failed");
  const finalLogout = await sessionRequest(
    page,
    "/api/v2/admin-sessions/current",
    "DELETE",
    session.csrf_token,
    undefined,
    {},
  );
  if (finalLogout.status !== 204) fail("final logout failed");
  await assertSessionCookieAbsent(context, baseURL);

  process.stdout.write(
    `${JSON.stringify({
      event: "protocol_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

export async function runFragmentStorage(
  browserVersion: string,
  page: Page,
  requestCount: () => number,
): Promise<void> {
  const idA = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
  const idB = "01ARZ3NDEKTSV4RRFFQ69G5FAW";
  const accepted: Array<[string, string]> = [
    ["#/overview", "#/overview"],
    ["#/mcp/servers", "#/mcp/servers"],
    ["#/mcp/servers/new", "#/mcp/servers/new"],
    [`#/mcp/servers/${idA}`, `#/mcp/servers/${idA}`],
    ...["status", "tools", "operations", "authentication", "settings"].map(
      (tab): [string, string] => [
        `#/mcp/servers/${idA}?tab=${tab}`,
        tab === "status"
          ? `#/mcp/servers/${idA}`
          : `#/mcp/servers/${idA}?tab=${tab}`,
      ],
    ),
    [
      `#/mcp/servers/${idA}/operations/${idB}`,
      `#/mcp/servers/${idA}/operations/${idB}`,
    ],
    [
      `#/mcp/servers/${idA}/auth-flows/${idB}`,
      `#/mcp/servers/${idA}/auth-flows/${idB}`,
    ],
    [
      `#/mcp/servers/${idA}/descriptors/${idB}`,
      `#/mcp/servers/${idA}/descriptors/${idB}`,
    ],
    ["#/mcp/tools", "#/mcp/tools"],
    ["#/principals", "#/principals"],
    [
      "#/principals?filter_name=Caf%C3%A9&filter_visibility=all&filter_state=disabled&direction=descending&sort=name",
      "#/principals?sort=name&direction=descending&filter_name=Caf%C3%A9&filter_state=disabled&filter_visibility=all",
    ],
    [
      "#/mcp/grants?filter_target=Far&filter_state=expired&filter_principal=Agent&filter_identity=Policy&filter_effect=deny&direction=ascending&sort=principal",
      "#/mcp/grants?sort=principal&direction=ascending&filter_effect=deny&filter_identity=Policy&filter_principal=Agent&filter_state=expired&filter_target=Far",
    ],
    ["#/mcp/grants?sort=description", "#/mcp/grants?sort=description"],
    ["#/principals/new", "#/principals/new"],
    [`#/principals/${idA}`, `#/principals/${idA}`],
    ["#/principals", "#/principals"],
    ["#/principals/new", "#/principals/new"],
    [`#/principals/${idA}`, `#/principals/${idA}`],
    ["#/mcp/grants", "#/mcp/grants"],
    ["#/mcp/grants/new", "#/mcp/grants/new"],
    [
      `#/mcp/grants/new?server_id=${idB}&principal_id=${idA}`,
      `#/mcp/grants/new?principal_id=${idA}&server_id=${idB}`,
    ],
    [`#/mcp/grants/${idA}`, `#/mcp/grants/${idA}`],
    ["#/mcp/grants", "#/mcp/grants"],
    ["#/mcp/grants/new", "#/mcp/grants/new"],
    [
      `#/mcp/grants/new?server_id=${idB}&principal_id=${idA}`,
      `#/mcp/grants/new?principal_id=${idA}&server_id=${idB}`,
    ],
    [`#/mcp/grants/${idA}`, `#/mcp/grants/${idA}`],
    ["#/mcp/access-requests", "#/mcp/access-requests"],
    [`#/mcp/access-requests/${idA}`, `#/mcp/access-requests/${idA}`],
    ["#/mcp/invocations", "#/mcp/invocations"],
    [`#/mcp/invocations/${idA}`, `#/mcp/invocations/${idA}`],
    ["#/system", "#/system"],
    ...["status", "resource-limits", "admin-credentials", "backups"].map(
      (tab): [string, string] => [
        `#/system?tab=${tab}`,
        tab === "status" ? "#/system" : `#/system?tab=${tab}`,
      ],
    ),
    ["#/system/admin-credentials/new", "#/system/admin-credentials/new"],
    ["#/system/backups/new", "#/system/backups/new"],
    ["#/sign-in", "#/sign-in"],
  ];
  const requestsBeforeLocations = requestCount();
  for (const [raw, canonical] of accepted) {
    await page.evaluate((fragment) => {
      window.location.hash = fragment;
    }, raw);
    await page.waitForFunction(
      (expected) => window.location.hash === expected,
      canonical,
    );
    if ((await page.locator('[data-testid="location-notice"]').count()) !== 0)
      fail("accepted fragment reported invalid");
  }

  await page.evaluate(() => {
    window.location.hash = "#/overview";
  });
  await page.waitForFunction(() => window.location.hash === "#/overview");
  await page.evaluate(() => {
    const anchor = document.createElement("a");
    anchor.href = "#/mcp/servers";
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
  });
  await page.waitForFunction(() => window.location.hash === "#/mcp/servers");
  await page.goBack();
  await page.waitForFunction(() => window.location.hash === "#/overview");

  const fragmentCanary = "fragment-secret-canary-41f95d";
  const invalid = [
    ...[
      "servers",
      "catalog",
      "access/principals",
      "activity/audit",
      "grants",
      "requests",
      "access/grants",
      "access/requests",
      "invocations",
      "activity/invocations",
      "audit",
    ].flatMap((path) => [`#/${path}`, `#/${path}/${idA}`, `#/${path}/new`]),
    `#/mcp/servers/${idA}?tab=activity`,
    `#/mcp/servers/${idA}?tab=overview`,
    `#/mcp/servers/${idA}?tab=settings&filter_name=x`,
    "#/system?filter_unknown=x",
    "#/mcp/grants/new?__proto__=x",
    "overview",
    "#overview",
    "#/",
    "#//overview",
    "#/overview/",
    "#/over%76iew",
    "#/overview?",
    "#/overview?unknown=x",
    "#/overview?cursor=x",
    "#/overview?requested_name=secret",
    `#/mcp/servers/${idA.toLowerCase()}`,
    `#/mcp/servers/${idA}?tab=unknown`,
    `#/mcp/servers/${idA}?tab=oauth&tab=oauth`,
    `#/mcp/servers/${idA}?tab=null`,
    `#/mcp/grants?principal_id=${idA}`,
    `#/mcp/grants?server_id=${idB}`,
    `#/mcp/grants?principal_id=${idA}`,
    "#/principals?direction=ascending",
    "#/principals?sort=unknown",
    "#/principals?filter_unknown=value",
    "#/principals?filter_state=expired",
    "#/principals?filter_name=%0A",
    `#/principals?filter_name=${encodeURIComponent("é".repeat(129))}`,
    "#/mcp/grants?filter_effect=ALLOW",
    "#/mcp/grants?sort=description&sort=id",
    "#/mcp/grants?cursor=opaque",
    "#/mcp/grants?filter_identity=%E0%A4%A",
    "#/mcp/access-requests?state=pending",
    `#/mcp/access-requests?principal_id=${idA}`,
    `#/mcp/invocations?principal_id=${idA}`,
    `#/mcp/invocations?server_id=${idB}`,
    "#/mcp/invocations?admission_class=evaluated",
    "#/mcp/invocations?decision=allow",
    "#/mcp/invocations?outcome=succeeded",
    "#/https://example.com",
    "#/overview/é",
    "#/overview/\n",
    `#/overview?x=${"a".repeat(2050)}`,
  ];
  for (const raw of invalid) {
    await page.evaluate((fragment) => {
      window.location.hash = fragment;
    }, raw);
    await page
      .waitForFunction(() => window.location.hash === "#/sign-in")
      .catch(async () =>
        fail(
          `invalid fragment was accepted: ${raw} -> ${await page.evaluate(() => window.location.hash)}`,
        ),
      );
    if ((await page.locator('[data-testid="location-notice"]').count()) !== 1)
      fail("invalid fragment did not report fixed notice");
  }

  await page.evaluate(() => {
    window.location.hash = "#/overview";
  });
  await page.waitForFunction(() => window.location.hash === "#/overview");
  const historyBeforeInvalid = await page.evaluate(() => history.length);
  await page.evaluate((canary) => {
    window.location.hash = `#/mcp/servers//${canary}`;
  }, fragmentCanary);
  await page.waitForFunction(() => window.location.hash === "#/sign-in");
  const invalidState = await page.evaluate(
    (canary) => ({
      historyLength: history.length,
      urlContains: window.location.href.includes(canary),
      domContains: document.documentElement.outerHTML.includes(canary),
    }),
    fragmentCanary,
  );
  if (
    invalidState.historyLength > historyBeforeInvalid + 1 ||
    invalidState.urlContains ||
    invalidState.domContains
  ) {
    fail("invalid fragment was retained or rendered");
  }
  await page.goBack();
  await page.waitForFunction(() => window.location.hash === "#/overview");
  if (requestCount() !== requestsBeforeLocations)
    fail("fragment navigation made a network request");

  assertClosedStorage(await browserStorage(page));
  for (const preference of ["light", "dark", "system"] as const) {
    await page
      .locator('[data-testid="theme-preference"]')
      .selectOption(preference);
    await page.waitForFunction(
      (expected) =>
        document.documentElement.dataset.themePreference === expected,
      preference,
    );
    assertClosedStorage(await browserStorage(page), preference);
  }
  await page.reload({ waitUntil: "domcontentloaded" });
  // Theme rendering precedes bootstrap settlement; do not unload the request
  // while the protocol owner is still collecting its complete Origin headers.
  await waitForLifecycle(page, "signed_out");
  if (
    (await page.locator('[data-testid="theme-preference"]').inputValue()) !==
    "system"
  ) {
    fail("theme preference did not survive reload");
  }
  assertClosedStorage(await browserStorage(page), "system");

  const storageCanary = "theme-secret-canary-7a20f1";
  await page.evaluate((canary) => {
    localStorage.setItem("agent_gateway_theme", canary);
  }, storageCanary);
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "signed_out");
  assertClosedStorage(await browserStorage(page));

  for (const [legacy, canonical, expected] of [
    ["light", null, "light"],
    ["dark", "invalid", "dark"],
    ["system", null, "system"],
    ["dark", "light", "light"],
    ["invalid", null, "system"],
  ] as const) {
    await page.evaluate(
      ({ legacy, canonical }) => {
        localStorage.clear();
        localStorage.setItem("mcp_gateway_theme", legacy);
        if (canonical !== null)
          localStorage.setItem("agent_gateway_theme", canonical);
      },
      { legacy, canonical },
    );
    for (let load = 0; load < 2; load += 1) {
      await page.reload({ waitUntil: "domcontentloaded" });
      await waitForLifecycle(page, "signed_out");
      await page.waitForFunction(
        (expected) =>
          document.documentElement.dataset.themePreference === expected,
        expected,
      );
      if (legacy !== "invalid")
        assertClosedStorage(await browserStorage(page), expected);
    }
  }
  // Inject failure before application startup, keeping the real persistence owner.
  await page.evaluate(() => {
    localStorage.clear();
    localStorage.setItem("mcp_gateway_theme", "dark");
  });
  await page.addInitScript(() => {
    Storage.prototype.setItem = () => {
      throw new DOMException("denied", "SecurityError");
    };
  });
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForFunction(
    () => document.documentElement.dataset.themePreference === "dark",
  );
  await waitForLifecycle(page, "signed_out");
  if (
    (await page.evaluate(() => localStorage.getItem("mcp_gateway_theme"))) !==
    "dark"
  )
    fail("failed migration destroyed the persisted preference");
  await page.getByTestId("theme-preference").selectOption("light");
  await page.waitForFunction(
    () => document.documentElement.dataset.theme === "light",
  );
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForFunction(
    () => document.documentElement.dataset.themePreference === "dark",
  );
  await waitForLifecycle(page, "signed_out");
  const screenshots = await mkdtemp(
    join(tmpdir(), "agent-gateway-theme-migration-"),
  );
  for (const width of [1440, 390]) {
    await page.setViewportSize({ width, height: 900 });
    await page.screenshot({
      path: join(screenshots, `migrated-dark-${width}.png`),
    });
  }
  await page.addInitScript(() => {
    Object.defineProperty(window, "localStorage", {
      get() {
        throw new DOMException("denied", "SecurityError");
      },
    });
  });
  await page.reload({ waitUntil: "domcontentloaded" });
  await page.waitForFunction(
    () => document.documentElement.dataset.themePreference === "system",
  );
  await page.getByTestId("theme-preference").selectOption("light");
  await page.waitForFunction(
    () => document.documentElement.dataset.theme === "light",
  );
  await waitForLifecycle(page, "signed_out");
  await page.screenshot({
    path: join(screenshots, "storage-denied-light-390.png"),
  });
  const finalDocument = await page.content();
  if (
    finalDocument.includes(fragmentCanary) ||
    finalDocument.includes(storageCanary) ||
    page.url().includes(fragmentCanary) ||
    page.url().includes(storageCanary)
  ) {
    fail("location or storage canary reached an active browser sink");
  }

  process.stdout.write(
    `${JSON.stringify({
      event: "fragment_storage_complete",
      screenshots,
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

export async function runAuthenticationEpoch(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  initialBearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  const input = page.locator('[data-testid="admin-bearer-input"]');
  if (
    (await input.getAttribute("type")) !== "password" ||
    (await input.getAttribute("autocomplete")) !== "off" ||
    (await input.getAttribute("name")) !== null ||
    (await input.getAttribute("value")) !== null ||
    (await input
      .locator("xpath=ancestor::form")
      .getAttribute("autocomplete")) !== "off"
  ) {
    fail("sign-in credential control attributes changed");
  }
  await assertSecretAbsent(page, context, baseURL, [initialBearer], false);

  const initialCredentials = await page.evaluate(async (bearer) => {
    const response = await fetch("/api/v2/admin-credentials", {
      headers: { Authorization: `Bearer ${bearer}` },
      credentials: "same-origin",
    });
    const value = (await response.json()) as { items?: Array<{ id?: string }> };
    return { status: response.status, id: value.items?.[0]?.id };
  }, initialBearer);
  if (initialCredentials.status !== 200 || initialCredentials.id === undefined)
    fail("authentication scenario could not identify initial authority");

  let releaseExchange: (() => void) | undefined;
  const exchangeBarrier = new Promise<void>((resolve) => {
    releaseExchange = resolve;
  });
  let exchangeIntercepted: (() => void) | undefined;
  const exchangeStarted = new Promise<void>((resolve) => {
    exchangeIntercepted = resolve;
  });
  await page.route(
    "**/api/v2/admin-sessions",
    async (route) => {
      exchangeIntercepted?.();
      await exchangeBarrier;
      await route.continue();
    },
    { times: 1 },
  );
  const initialExchangeResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v2/admin-sessions"),
  );
  await input.fill(initialBearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await exchangeStarted;
  await page.waitForFunction(
    () =>
      (
        document.querySelector(
          '[data-testid="admin-bearer-input"]',
        ) as HTMLInputElement | null
      )?.value === "",
  );
  await assertSecretAbsent(page, context, baseURL, [initialBearer], false);
  releaseExchange?.();
  if ((await initialExchangeResponse).status() !== 201)
    fail("initial application exchange was rejected");
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(() => window.location.hash === "#/overview");
  await assertSecretAbsent(page, context, baseURL, [initialBearer], true);

  const session = await bootstrap(page);
  if (session.status !== 200 || session.session === undefined)
    fail("authenticated application bootstrap failed");
  const replacementResult = await sessionRequest(
    page,
    "/api/v2/admin-credentials",
    "POST",
    session.session.csrf_token,
    undefined,
    { expires_at: null },
  );
  if (replacementResult.status !== 201)
    fail("replacement recovery authority creation failed");
  const replacement = createdCredential(replacementResult.value);

  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "authenticated");
  const newTab = await context.newPage();
  await loadShell(newTab);
  await waitForLifecycle(newTab, "authenticated");
  await assertSecretAbsent(
    newTab,
    context,
    baseURL,
    [initialBearer, replacement.bearer],
    true,
  );
  await newTab.close();

  let bootstrapRequests = 0;
  const countBootstrap = (request: Request) => {
    if (
      request.method() === "POST" &&
      request.url().endsWith("/api/v2/admin-sessions/current")
    ) {
      bootstrapRequests += 1;
    }
  };
  page.on("request", countBootstrap);
  const revoke = await sessionRequest(
    page,
    `/api/v2/admin-credentials/${initialCredentials.id}`,
    "DELETE",
    session.session.csrf_token,
    undefined,
    {},
  );
  if (revoke.status !== 204) fail("parent authority revocation failed");
  await waitForLifecycle(page, "signed_out");
  page.off("request", countBootstrap);
  if (
    bootstrapRequests !== 1 ||
    (await page.evaluate(() => window.location.hash)) !== "#/sign-in"
  ) {
    fail("live revocation did not settle through one bootstrap");
  }

  bootstrapRequests = 0;
  page.on("request", countBootstrap);
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "signed_out");
  page.off("request", countBootstrap);
  if (bootstrapRequests !== 1)
    fail("signed-out reload did not perform one bootstrap");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [initialBearer, replacement.bearer],
    false,
  );

  let replacementBearer = replacement.bearer;
  await page
    .locator('[data-testid="admin-bearer-input"]')
    .fill(replacementBearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  replacementBearer = "";

  let releaseLogout: (() => void) | undefined;
  const logoutBarrier = new Promise<void>((resolve) => {
    releaseLogout = resolve;
  });
  let logoutIntercepted: (() => void) | undefined;
  const intercepted = new Promise<void>((resolve) => {
    logoutIntercepted = resolve;
  });
  await page.route(
    "**/api/v2/admin-sessions/current",
    async (route) => {
      if (route.request().method() !== "DELETE") {
        await route.continue();
        return;
      }
      logoutIntercepted?.();
      await logoutBarrier;
      await route.continue();
    },
    { times: 1 },
  );
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await intercepted;
  await waitForLifecycle(page, "signed_out");
  await page.waitForFunction(() => window.location.hash === "#/sign-in");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [initialBearer, replacement.bearer],
    true,
  );
  const logoutResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "DELETE" &&
      response.url().endsWith("/api/v2/admin-sessions/current"),
  );
  releaseLogout?.();
  if ((await logoutResponse).status() !== 204)
    fail("delayed logout did not settle");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [initialBearer, replacement.bearer],
    false,
  );

  const rejectedBearer = `mgw_admin_${"A".repeat(43)}`;
  await page.locator('[data-testid="admin-bearer-input"]').fill(rejectedBearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await page.locator('[data-testid="session-message"]').waitFor();
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [initialBearer, replacement.bearer, rejectedBearer],
    false,
  );

  const malformedSessionCanary = "malformed-session-secret-8f31";
  await page.route(
    "**/api/v2/admin-sessions/current",
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          ...sessionFixture(),
          extra: malformedSessionCanary,
        }),
      }),
    { times: 1 },
  );
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "signed_out");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [malformedSessionCanary],
    false,
  );

  const malformedProblemCanary = "malformed-problem-secret-a204";
  await page.route(
    "**/api/v2/admin-sessions/current",
    async (route) =>
      route.fulfill({
        status: 401,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 401,
          code: "authentication_required",
          title: "Authentication is required.",
          extra: malformedProblemCanary,
        }),
      }),
    { times: 1 },
  );
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "signed_out");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [malformedProblemCanary],
    false,
  );

  process.stdout.write(
    `${JSON.stringify({
      event: "authentication_epoch_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

export async function runReadGeneration(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  let eventRequests = 0;
  const observeEvents = (request: Request) => {
    if (
      request.method() === "POST" &&
      request.url().endsWith("/api/v2/events")
    ) {
      eventRequests += 1;
    }
  };
  page.on("request", observeEvents);
  await page.route(
    "**/api/v2/events",
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
  if (eventRequests !== 2)
    fail("application did not reconnect its POST event stream");

  const initialGeneration = Number(
    await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-view-generation"),
  );
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(
    (generation) =>
      Number(
        document
          .querySelector('[data-testid="gateway-shell"]')
          ?.getAttribute("data-view-generation"),
      ) > generation,
    initialGeneration,
  );

  const current = await bootstrap(page);
  if (current.status !== 200 || current.session === undefined)
    fail("read generation bootstrap failed");
  const generationBeforeInvalidation = Number(
    await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-view-generation"),
  );
  const created = await sessionRequest(
    page,
    "/api/v2/admin-credentials",
    "POST",
    current.session.csrf_token,
    undefined,
    { expires_at: null },
  );
  if (created.status !== 201) fail("invalidation trigger failed");
  const oneTime = createdCredential(created.value);
  oneTime.bearer = "";
  await page.waitForFunction(
    (generation) =>
      Number(
        document
          .querySelector('[data-testid="gateway-shell"]')
          ?.getAttribute("data-view-generation"),
      ) ===
      generation + 1,
    generationBeforeInvalidation,
  );

  page.off("request", observeEvents);
  await assertSecretAbsent(page, context, baseURL, [bearer], true);

  process.stdout.write(
    `${JSON.stringify({
      event: "read_generation_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

export async function runShellPrimitives(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  await page.keyboard.press("Tab");
  const skipLink = page.getByRole("link", { name: "Skip to main content" });
  if (
    !(await skipLink.evaluate((element) => element === document.activeElement))
  )
    fail("skip link was not the first keyboard destination");
  await page.keyboard.press("Enter");
  if (
    !(await page
      .locator("#page-title")
      .evaluate((element) => element === document.activeElement))
  ) {
    fail("skip link did not focus the page heading");
  }
  const bearerInput = page.locator('[data-testid="admin-bearer-input"]');
  const signInText = (await page.locator("main").innerText()).toLowerCase();
  if (
    (signInText.match(/cleared/g) ?? []).length !== 1 ||
    signInText.includes("use a current administrator bearer")
  )
    fail("sign-in repeated its bearer-clearing guidance");
  if (
    (await bearerInput.getAttribute("aria-describedby")) !== "admin-bearer-hint"
  )
    fail("shared form field did not associate its hint");
  const signInGap = await page
    .locator('[data-testid="sign-in-submit"]')
    .evaluate((button) => {
      const previous = button.previousElementSibling;
      if (previous === null) return -1;
      return (
        button.getBoundingClientRect().top -
        previous.getBoundingClientRect().bottom
      );
    });
  if (signInGap < 16) fail(`sign-in submit spacing collapsed: ${signInGap}px`);
  await bearerInput.fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  if (
    (await page.locator("header").count()) !== 1 ||
    (await page.locator("main").count()) !== 1 ||
    (await page.locator("h1").count()) !== 1
  ) {
    fail("operational shell landmarks or heading hierarchy changed");
  }
  if ((await page.locator('[data-testid="manual-refresh"]').count()) !== 1)
    fail("refresh was not centralized in the application header");
  if (
    await page
      .locator("main")
      .getByText("Data current", { exact: true })
      .isVisible()
      .catch(() => false)
  )
    fail("healthy freshness was repeated in page content");

  const logout = page.locator('[data-testid="logout"]');
  await logout.focus();
  await logout.click();
  const dialog = page.locator(
    'dialog.confirmation-dialog[aria-labelledby="logout-confirmation-title"]',
  );
  await dialog.waitFor({ state: "visible" });
  if (
    (await dialog.getAttribute("aria-labelledby")) !==
      "logout-confirmation-title" ||
    (await dialog.getAttribute("aria-describedby")) !==
      "logout-confirmation-consequence"
  ) {
    fail("confirmation dialog lost its accessible name or consequence");
  }
  await page.keyboard.press("Escape");
  await dialog.waitFor({ state: "hidden" });
  if (!(await logout.evaluate((element) => element === document.activeElement)))
    fail("Escape did not restore confirmation focus");
  await logout.click();
  await page.locator('[data-testid="logout-confirmation-cancel"]').click();
  await dialog.waitFor({ state: "hidden" });
  if (!(await logout.evaluate((element) => element === document.activeElement)))
    fail("confirmation cancel did not restore focus");

  for (const choice of ["light", "dark"] as const) {
    await page.locator('[data-testid="theme-preference"]').selectOption(choice);
    await page.waitForFunction(
      (expected) => document.documentElement.dataset.theme === expected,
      choice,
    );
    const colors = await page.evaluate(() => {
      const style = getComputedStyle(document.documentElement);
      return [
        style.getPropertyValue("--canvas"),
        style.getPropertyValue("--text"),
      ];
    });
    if (colors.some((color) => color.trim() === ""))
      fail(`${choice} theme did not resolve semantic tokens`);
  }

  const expectedNavigation = [
    ["Overview", "#/overview"],
    ["Principals", "#/principals"],
    ["Audit Log", "#/audit-log"],
    ["System", "#/system"],
    ["Credentials", "#/http/credentials"],
    ["Servers", "#/mcp/servers"],
    ["Tools", "#/mcp/tools"],
    ["Grants", "#/mcp/grants"],
    ["Requests", "#/mcp/access-requests"],
    ["Invocations", "#/mcp/invocations"],
  ] as const;
  const primary = page.getByRole("navigation", {
    name: "Primary",
    exact: true,
  });
  const navigationLinks = await primary
    .getByRole("link")
    .evaluateAll((links) =>
      links.map((link) => [
        link.firstChild?.textContent,
        link.getAttribute("href"),
      ]),
    );
  if (JSON.stringify(navigationLinks) !== JSON.stringify(expectedNavigation))
    fail("domain navigation labels, order or legacy destinations changed");
  for (const [name, labels] of [
    ["HTTP", ["Credentials"]],
    ["MCP", ["Servers", "Tools", "Grants", "Requests", "Invocations"]],
  ] as const) {
    const links = await primary
      .getByRole("group", { name, exact: true })
      .getByRole("link")
      .evaluateAll((links) =>
        links.map((link) => link.firstChild?.textContent),
      );
    if (JSON.stringify(links) !== JSON.stringify(labels))
      fail(`${name} navigation group lost its accessible membership`);
  }
  if ((await primary.getByRole("group").count()) !== 2)
    fail("primary navigation must contain the HTTP and MCP named groups");
  for (const [label, href] of expectedNavigation) {
    await primary.locator(`a[href="${href}"]`).focus();
    await page.keyboard.press("Enter");
    await page.waitForFunction(
      ({ label, href }) =>
        window.location.hash === href &&
        document.querySelector("#page-title")?.textContent ===
          (href.startsWith("#/mcp/")
            ? `MCP ${label === "Requests" ? "Access Requests" : label}`
            : href.startsWith("#/http/")
              ? `HTTP ${label}`
              : label) &&
        document
          .querySelector("#primary-navigation a[aria-current=page]")
          ?.getAttribute("href") === href,
      { label, href },
    );
    if ((await primary.locator('[aria-current="page"]').count()) !== 1)
      fail("navigation must have exactly one current destination");
  }

  await exercisePendingRequests(page);

  for (const fragment of [
    "#/mcp/grants?sort=target&filter_effect=deny",
    "#/mcp/access-requests?queue=all&filter_state=approved",
    "#/mcp/invocations?filter_outcome=succeeded",
  ]) {
    await page.evaluate((value) => {
      location.hash = value;
    }, fragment);
    await expect(page).toHaveURL(`${baseURL}/${fragment}`);
    await page.reload();
    await waitForLifecycle(page, "authenticated");
    await expect(page).toHaveURL(`${baseURL}/${fragment}`);
    await primary.getByRole("link", { name: "Overview", exact: true }).click();
    await page.goBack();
    await expect(page).toHaveURL(`${baseURL}/${fragment}`);
    await page.goForward();
    await expect(page).toHaveURL(`${baseURL}/#/overview`);
  }
  const retiredResourceReads: string[] = [];
  const observeRetiredRead = (request: Request) => {
    const path = new URL(request.url()).pathname;
    if (
      /^\/api\/v2\/(?:mcp\/)?grants(?:\/|$)/.test(path) ||
      /^\/api\/v2\/(?:mcp\/)?grant-requests\//.test(path) ||
      /^\/api\/v2\/(?:mcp\/)?invocations(?:\/|$)/.test(path)
    ) {
      retiredResourceReads.push(request.method() + " " + path);
    }
  };
  page.on("request", observeRetiredRead);
  try {
    for (const fragment of [
      "#/access/grants/new",
      "#/access/requests/01ARZ3NDEKTSV4RRFFQ69G5FAV",
      "#/activity/invocations",
      "#/activity/invocations/01ARZ3NDEKTSV4RRFFQ69G5FAV?filter_outcome=succeeded",
    ]) {
      await page.goto(`${baseURL}/${fragment}`);
      await waitForLifecycle(page, "authenticated");
      await expect(page).toHaveURL(`${baseURL}/#/overview`);
      await expect(page.getByTestId("location-notice")).toBeVisible();
      await expect(page.locator("dialog[open]")).toHaveCount(0);
    }
    expect(retiredResourceReads).toEqual([]);
  } finally {
    page.off("request", observeRetiredRead);
  }

  await page.locator('aside nav a[href="#/mcp/servers"]').focus();
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => {
    const title = document.querySelector("#page-title");
    const announcement = document.querySelector(
      '[data-testid="shell-announcement"]',
    );
    return (
      window.location.hash === "#/mcp/servers" &&
      title?.textContent?.trim() === "MCP Servers" &&
      title === document.activeElement &&
      announcement?.textContent?.includes("MCP Servers")
    );
  });
  const authStatus = page.locator('[data-testid="authentication-status"]');
  if (
    (await authStatus.getAttribute("data-state")) !== "current" ||
    (await authStatus.textContent())?.trim() !== "Authenticated" ||
    (await authStatus.locator(".status-symbol").count()) !== 0
  ) {
    fail("operational state lost its textual label or retained decoration");
  }
  const shellText = (await page.locator("body").innerText()).toUpperCase();
  for (const decorativeCopy of [
    "LOCAL CONTROL PLANE",
    "GATEWAY AUTHORITY STAYS IN THIS PROCESS",
    "NO REMOTE ASSETS",
    "INSPECT AND OPERATE THIS LOCAL GATEWAY THROUGH ITS PUBLIC API",
  ]) {
    if (shellText.includes(decorativeCopy))
      fail(`shell retained decorative copy: ${decorativeCopy}`);
  }

  await page.setViewportSize({ width: 320, height: 800 });
  const navigationToggle = page.locator('[data-testid="navigation-toggle"]');
  await navigationToggle.focus();
  await page.keyboard.press("Space");
  if (
    (await navigationToggle.getAttribute("aria-expanded")) !== "true" ||
    !(await page.locator("#primary-navigation").isVisible())
  ) {
    fail("narrow navigation disclosure did not open from the keyboard");
  }
  for (const [label, href] of expectedNavigation) {
    const link = primary.locator(`a[href="${href}"]`);
    await link.focus();
    const reachable = await link.evaluate((element) => {
      const rect = element.getBoundingClientRect();
      const rail = document
        .querySelector("#primary-navigation")!
        .getBoundingClientRect();
      return (
        rect.width >= 44 &&
        rect.height >= 44 &&
        rect.left >= rail.left &&
        rect.right <= rail.right &&
        rect.top >= rail.top &&
        rect.bottom <= rail.bottom
      );
    });
    if (!reachable)
      fail(`${label} is clipped or too small in narrow navigation`);
  }
  await page.keyboard.press("Escape");
  try {
    await page.waitForFunction(
      () => {
        const toggle = document.querySelector(
          '[data-testid="navigation-toggle"]',
        );
        return (
          toggle?.getAttribute("aria-expanded") === "false" &&
          toggle === document.activeElement
        );
      },
      undefined,
      { timeout: 3000 },
    );
  } catch {
    const state = await page.evaluate(() => {
      const toggle = document.querySelector(
        '[data-testid="navigation-toggle"]',
      );
      return {
        expanded: toggle?.getAttribute("aria-expanded"),
        activeTestID: document.activeElement?.getAttribute("data-testid"),
        activeTag: document.activeElement?.tagName,
      };
    });
    fail(`narrow navigation Escape state: ${JSON.stringify(state)}`);
  }
  await page.keyboard.press("Space");
  const invocationLink = page.locator('aside nav a[href="#/mcp/invocations"]');
  await invocationLink.focus();
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => {
    const toggle = document.querySelector('[data-testid="navigation-toggle"]');
    const navigation = document.querySelector("#primary-navigation");
    return (
      window.location.hash === "#/mcp/invocations" &&
      toggle?.getAttribute("aria-expanded") === "false" &&
      navigation !== null &&
      getComputedStyle(navigation).display === "none"
    );
  });

  const longCanary = `LONG_INERT_${"A".repeat(1800)}`;
  await page.evaluate((value) => {
    window.location.hash = `#/mcp/invocations?outcome=${value}`;
  }, longCanary);
  await page.waitForFunction(() => window.location.hash === "#/overview");
  if ((await page.locator("body").textContent())?.includes(longCanary))
    fail("rejected long text reached rendered shell text");
  const overflow = await page.evaluate(
    () =>
      document.documentElement.scrollWidth -
      document.documentElement.clientWidth,
  );
  if (overflow > 1) fail(`narrow shell overflowed by ${overflow}px`);

  const artifacts = await mkdtemp(
    join(tmpdir(), "agent-gateway-domain-routes-"),
  );
  const screenshots: string[] = [];
  for (const width of [1280, 390, 320]) {
    await page.setViewportSize({ width, height: 900 });
    // Reloaded invalid bookmarks must retain the safe notice after bootstrap.
    await page.goto(`${baseURL}/#/servers/new`, {
      waitUntil: "domcontentloaded",
    });
    await waitForLifecycle(page, "authenticated");
    await page.locator('[data-testid="location-notice"]').waitFor();
    if (await page.locator('[data-testid="server-create-view"]').count())
      fail("old create path opened a mutation editor");
    const invalidPath = join(artifacts, `invalid-${width}.png`);
    await page.screenshot({ path: invalidPath });
    screenshots.push(invalidPath);
    if (width < 800) await page.getByTestId("navigation-toggle").click();
    await primary.waitFor({ state: "visible" });
    await page.keyboard.press("Tab");
    const link = primary.getByRole("link", { name: "Servers", exact: true });
    await link.focus();
    if (!(await link.evaluate((element) => element === document.activeElement)))
      fail("canonical navigation link did not receive focus");
    const focusPath = join(artifacts, `navigation-focus-${width}.png`);
    await page.screenshot({ path: focusPath });
    screenshots.push(focusPath);
    await page.keyboard.press("Enter");
    await page.waitForFunction(() => location.hash === "#/mcp/servers");
    await page.getByTestId("location-notice").waitFor({ state: "hidden" });
  }
  const filtered =
    "#/mcp/servers?sort=name&direction=descending&filter_name=missing";
  await page.goto(`${baseURL}/${filtered}`, { waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "authenticated");
  if ((await page.evaluate(() => location.hash)) !== filtered)
    fail("copied filter direct-load changed its query");
  await page.reload({ waitUntil: "domcontentloaded" });
  await waitForLifecycle(page, "authenticated");
  if ((await page.evaluate(() => location.hash)) !== filtered)
    fail("reload lost canonical filters");
  await page.evaluate(() => {
    location.hash = "#/mcp/tools";
  });
  await page.waitForFunction(() => location.hash === "#/mcp/tools");
  await page.goBack();
  await page.waitForFunction(
    (expected) => location.hash === expected,
    filtered,
  );
  await page.goForward();
  await page.waitForFunction(() => location.hash === "#/mcp/tools");

  await page.emulateMedia({ reducedMotion: "reduce" });
  const animationDuration = await page
    .locator(".panel")
    .first()
    .evaluate((element) => getComputedStyle(element).animationDuration);
  const animationSeconds = Number.parseFloat(animationDuration);
  if (!Number.isFinite(animationSeconds) || animationSeconds > 0.00001)
    fail(`reduced motion retained panel animation: ${animationDuration}`);

  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, longCanary],
    true,
    "dark",
  );
  process.stdout.write(
    `${JSON.stringify({
      event: "shell_primitives_complete",
      screenshots,
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

export async function runMutationState(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) !== "enabled"
  ) {
    fail("application mutation admission did not start enabled");
  }
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) !== "enabled"
  ) {
    fail("authentication changed mutation availability");
  }
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({
      event: "mutation_state_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}
