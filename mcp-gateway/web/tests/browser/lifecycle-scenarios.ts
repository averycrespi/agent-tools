import { type BrowserContext, type Page, type Request } from "@playwright/test";
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
import {
  assertMutationFoundation,
  assertSessionFoundationEpochs,
  assertViewGenerationFoundation,
} from "./foundations.ts";

export async function runSessionLifecycleCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const session = await exchange(page, bearer);
  const current = await bootstrap(page);
  if (
    current.status !== 200 ||
    current.session?.csrf_token !== session.csrf_token
  ) {
    fail("browser session bootstrap canary failed");
  }
  await connectAndCancelStream(page, session.csrf_token);
  const logout = await sessionRequest(
    page,
    "/api/v1/admin-sessions/current",
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
    window.location.hash = `#/servers/${value}`;
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
    "**/api/v1/admin-sessions/current",
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
            "mcp_gateway_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict",
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
    const response = await fetch("/api/v1/admin-credentials", {
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
    "/api/v1/admin-credentials",
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
    "/api/v1/admin-credentials",
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
    "/api/v1/admin-sessions/current",
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
    "/api/v1/admin-sessions/current",
    "DELETE",
    expiringSession.csrf_token,
    undefined,
    {},
  );
  if (expiringLogout.status !== 204) fail("expiring session logout failed");

  session = await exchange(page, initialBearer);
  const revoke = await sessionRequest(
    page,
    `/api/v1/admin-credentials/${initialID}`,
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
    "/api/v1/admin-sessions/current",
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
    ["#/servers", "#/servers"],
    ["#/servers/new", "#/servers/new"],
    [`#/servers/${idA}`, `#/servers/${idA}`],
    ...["status", "tools", "activity", "authentication", "settings"].map(
      (tab): [string, string] => [
        `#/servers/${idA}?tab=${tab}`,
        `#/servers/${idA}?tab=${tab}`,
      ],
    ),
    [
      `#/servers/${idA}/operations/${idB}`,
      `#/servers/${idA}/operations/${idB}`,
    ],
    [
      `#/servers/${idA}/auth-flows/${idB}`,
      `#/servers/${idA}/auth-flows/${idB}`,
    ],
    [
      `#/servers/${idA}/descriptors/${idB}`,
      `#/servers/${idA}/descriptors/${idB}`,
    ],
    ["#/catalog", "#/catalog"],
    ["#/principals", "#/principals"],
    [
      "#/access/principals?filter_name=Caf%C3%A9&filter_visibility=all&filter_state=disabled&direction=descending&sort=name",
      "#/principals?sort=name&direction=descending&filter_name=Caf%C3%A9&filter_state=disabled&filter_visibility=all",
    ],
    [
      "#/grants?filter_target=Far&filter_state=expired&filter_principal=Agent&filter_identity=Policy&filter_effect=deny&direction=ascending&sort=principal",
      "#/grants?sort=principal&direction=ascending&filter_effect=deny&filter_identity=Policy&filter_principal=Agent&filter_state=expired&filter_target=Far",
    ],
    ["#/grants?sort=description", "#/grants?sort=description"],
    ["#/principals/new", "#/principals/new"],
    [`#/principals/${idA}`, `#/principals/${idA}`],
    ["#/access/principals", "#/principals"],
    ["#/access/principals/new", "#/principals/new"],
    [`#/access/principals/${idA}`, `#/principals/${idA}`],
    ["#/grants", "#/grants"],
    ["#/grants/new", "#/grants/new"],
    [
      `#/grants/new?server_id=${idB}&principal_id=${idA}`,
      `#/grants/new?principal_id=${idA}&server_id=${idB}`,
    ],
    [`#/grants/${idA}`, `#/grants/${idA}`],
    ["#/access/grants", "#/grants"],
    ["#/access/grants/new", "#/grants/new"],
    [
      `#/access/grants/new?server_id=${idB}&principal_id=${idA}`,
      `#/grants/new?principal_id=${idA}&server_id=${idB}`,
    ],
    [`#/access/grants/${idA}`, `#/grants/${idA}`],
    ["#/requests", "#/requests"],
    [`#/requests/${idA}`, `#/requests/${idA}`],
    ["#/invocations", "#/invocations"],
    [`#/invocations/${idA}`, `#/invocations/${idA}`],
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
    anchor.href = "#/servers";
    document.body.append(anchor);
    anchor.click();
    anchor.remove();
  });
  await page.waitForFunction(() => window.location.hash === "#/servers");
  await page.goBack();
  await page.waitForFunction(() => window.location.hash === "#/overview");

  const fragmentCanary = "fragment-secret-canary-41f95d";
  const invalid = [
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
    `#/servers/${idA.toLowerCase()}`,
    `#/servers/${idA}?tab=unknown`,
    `#/servers/${idA}?tab=oauth&tab=oauth`,
    `#/servers/${idA}?tab=null`,
    `#/grants?principal_id=${idA}`,
    `#/grants?server_id=${idB}`,
    `#/access/grants?principal_id=${idA}`,
    "#/principals?direction=ascending",
    "#/principals?sort=unknown",
    "#/principals?filter_unknown=value",
    "#/principals?filter_state=expired",
    "#/principals?filter_name=%0A",
    `#/principals?filter_name=${encodeURIComponent("é".repeat(129))}`,
    "#/grants?filter_effect=ALLOW",
    "#/grants?sort=description&sort=id",
    "#/grants?cursor=opaque",
    "#/grants?filter_identity=%E0%A4%A",
    "#/requests?state=pending",
    `#/requests?principal_id=${idA}`,
    `#/invocations?principal_id=${idA}`,
    `#/invocations?server_id=${idB}`,
    "#/invocations?admission_class=evaluated",
    "#/invocations?decision=allow",
    "#/invocations?outcome=succeeded",
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
    window.location.hash = `#/servers//${canary}`;
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
  if (
    (await page.locator('[data-testid="theme-preference"]').inputValue()) !==
    "system"
  ) {
    fail("theme preference did not survive reload");
  }
  assertClosedStorage(await browserStorage(page), "system");

  const storageCanary = "theme-secret-canary-7a20f1";
  await page.evaluate((canary) => {
    localStorage.setItem("mcp_gateway_theme", canary);
  }, storageCanary);
  await page.reload({ waitUntil: "domcontentloaded" });
  assertClosedStorage(await browserStorage(page));
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
  await assertSessionFoundationEpochs();
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
    const response = await fetch("/api/v1/admin-credentials", {
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
    "**/api/v1/admin-sessions",
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
      response.url().endsWith("/api/v1/admin-sessions"),
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
    "/api/v1/admin-credentials",
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
      request.url().endsWith("/api/v1/admin-sessions/current")
    ) {
      bootstrapRequests += 1;
    }
  };
  page.on("request", countBootstrap);
  const revoke = await sessionRequest(
    page,
    `/api/v1/admin-credentials/${initialCredentials.id}`,
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
    "**/api/v1/admin-sessions/current",
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
      response.url().endsWith("/api/v1/admin-sessions/current"),
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
    "**/api/v1/admin-sessions/current",
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
    "**/api/v1/admin-sessions/current",
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
  await assertViewGenerationFoundation();
  await waitForLifecycle(page, "signed_out");
  let eventRequests = 0;
  const observeEvents = (request: Request) => {
    if (
      request.method() === "POST" &&
      request.url().endsWith("/api/v1/events")
    ) {
      eventRequests += 1;
    }
  };
  page.on("request", observeEvents);
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
    "/api/v1/admin-credentials",
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

  const navigationHrefs = await page
    .locator("aside nav a")
    .evaluateAll((links) =>
      links.map((link) => link.getAttribute("href") ?? ""),
    );
  if (
    navigationHrefs.indexOf("#/catalog") < 0 ||
    navigationHrefs.indexOf("#/servers") < 0 ||
    navigationHrefs.indexOf("#/catalog") >= navigationHrefs.indexOf("#/servers")
  )
    fail("Catalog did not appear before Servers");

  await page.locator('aside nav a[href="#/servers"]').focus();
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => {
    const title = document.querySelector("#page-title");
    const announcement = document.querySelector(
      '[data-testid="shell-announcement"]',
    );
    return (
      window.location.hash === "#/servers" &&
      title?.textContent?.trim() === "Servers" &&
      title === document.activeElement &&
      announcement?.textContent?.includes("Servers")
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
  const invocationLink = page.locator('aside nav a[href="#/invocations"]');
  await invocationLink.focus();
  await page.keyboard.press("Enter");
  await page.waitForFunction(() => {
    const toggle = document.querySelector('[data-testid="navigation-toggle"]');
    const navigation = document.querySelector("#primary-navigation");
    return (
      window.location.hash === "#/invocations" &&
      toggle?.getAttribute("aria-expanded") === "false" &&
      navigation !== null &&
      getComputedStyle(navigation).display === "none"
    );
  });

  const longCanary = `LONG_INERT_${"A".repeat(1800)}`;
  await page.evaluate((value) => {
    window.location.hash = `#/invocations?outcome=${value}`;
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
  await assertMutationFoundation();
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
