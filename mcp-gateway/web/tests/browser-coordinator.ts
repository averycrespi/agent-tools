import {
  chromium,
  type Browser,
  type BrowserContext,
  type Page,
  type Request,
  type Response as PlaywrightResponse,
} from "@playwright/test";
import { createInterface } from "node:readline";
import { MutationCoordinator, type MutationSpec } from "../src/mutation.ts";
import {
  copyToClipboard,
  openOAuthWindow,
  SensitiveSinkCoordinator,
  type OAuthPresenter,
  type OneTimePresenter,
} from "../src/sinks.ts";
import {
  parseProblem,
  parseSessionBootstrap,
  SessionClient,
} from "../src/session.ts";
import {
  parseInvalidation,
  ViewCoordinator,
  type VisibilitySource,
} from "../src/view.ts";

interface BridgeInput {
  version: 1;
  scenario:
    | "shell-load"
    | "browser-protocol"
    | "m1-canary"
    | "fragment-storage"
    | "authentication-epoch"
    | "read-generation"
    | "mutation-state"
    | "shell-primitives"
    | "secret-sinks";
  base_url: string;
  admin_bearer: string;
}

interface SessionBootstrap {
  csrf_token: string;
  idle_expires_at: string;
  absolute_expires_at: string;
}

interface CreatedCredential {
  id: string;
  bearer: string;
  expires_at: string | null;
}

const inputLines = createInterface({
  input: process.stdin,
  crlfDelay: Infinity,
});
const inputIterator = inputLines[Symbol.asyncIterator]();

function fail(message: string): never {
  throw new Error(message);
}

async function readBoundedInput(): Promise<unknown> {
  const next = await inputIterator.next();
  if (next.done || Buffer.byteLength(next.value, "utf8") > 16 * 1024)
    fail("invalid bridge input");
  try {
    return JSON.parse(next.value) as unknown;
  } catch {
    fail("invalid bridge JSON");
  }
}

function parseInitialInput(value: unknown): BridgeInput {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    Object.keys(value).sort().join(",") !==
      "admin_bearer,base_url,scenario,version" ||
    !("version" in value) ||
    value.version !== 1 ||
    !("scenario" in value) ||
    (value.scenario !== "shell-load" &&
      value.scenario !== "browser-protocol" &&
      value.scenario !== "m1-canary" &&
      value.scenario !== "fragment-storage" &&
      value.scenario !== "authentication-epoch" &&
      value.scenario !== "read-generation" &&
      value.scenario !== "mutation-state" &&
      value.scenario !== "shell-primitives" &&
      value.scenario !== "secret-sinks") ||
    !("base_url" in value) ||
    typeof value.base_url !== "string" ||
    !/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(value.base_url) ||
    !("admin_bearer" in value) ||
    typeof value.admin_bearer !== "string" ||
    value.admin_bearer.length === 0
  ) {
    fail("invalid bridge input");
  }
  return value as BridgeInput;
}

async function loadShell(page: Page): Promise<void> {
  const response = await page.goto("/", { waitUntil: "domcontentloaded" });
  if (response === null || response.status() !== 200) fail("shell load failed");
  await page.locator('[data-testid="gateway-shell"]').waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-session-lifecycle") !== "bootstrapping",
  );
  if ((await page.title()) !== "MCP Gateway Control Plane")
    fail("unexpected shell title");
  const csp = (await response.allHeaders())["content-security-policy"] ?? "";
  if (csp.includes("'unsafe-") || !csp.includes("default-src 'self'"))
    fail("unsafe shell CSP");
}

async function sessionRequest(
  page: Page,
  path: string,
  method: "POST" | "DELETE",
  csrf: string | undefined,
  bearer: string | undefined,
  body: object,
): Promise<{ status: number; value: unknown }> {
  return page.evaluate(
    async ({
      requestPath,
      requestMethod,
      csrfToken,
      bearerToken,
      requestBody,
    }) => {
      const headers: Record<string, string> = {
        "Content-Type": "application/json",
      };
      if (csrfToken !== undefined) headers["X-CSRF-Token"] = csrfToken;
      if (bearerToken !== undefined)
        headers.Authorization = `Bearer ${bearerToken}`;
      const response = await fetch(requestPath, {
        method: requestMethod,
        headers,
        body: JSON.stringify(requestBody),
        credentials: "same-origin",
      });
      const text = await response.text();
      return {
        status: response.status,
        value: text === "" ? null : (JSON.parse(text) as unknown),
      };
    },
    {
      requestPath: path,
      requestMethod: method,
      csrfToken: csrf,
      bearerToken: bearer,
      requestBody: body,
    },
  );
}

function sessionBootstrap(value: unknown): SessionBootstrap {
  if (
    typeof value !== "object" ||
    value === null ||
    !("csrf_token" in value) ||
    typeof value.csrf_token !== "string" ||
    !("idle_expires_at" in value) ||
    typeof value.idle_expires_at !== "string" ||
    !("absolute_expires_at" in value) ||
    typeof value.absolute_expires_at !== "string"
  ) {
    fail("invalid session bootstrap");
  }
  return value as SessionBootstrap;
}

function createdCredential(value: unknown): CreatedCredential {
  if (
    typeof value !== "object" ||
    value === null ||
    !("id" in value) ||
    typeof value.id !== "string" ||
    !("bearer" in value) ||
    typeof value.bearer !== "string" ||
    !("expires_at" in value) ||
    (value.expires_at !== null && typeof value.expires_at !== "string")
  ) {
    fail("invalid credential creation");
  }
  return value as CreatedCredential;
}

async function exchange(page: Page, bearer: string): Promise<SessionBootstrap> {
  const result = await sessionRequest(
    page,
    "/api/v1/admin-sessions",
    "POST",
    undefined,
    bearer,
    {},
  );
  if (result.status !== 201) fail("session exchange failed");
  return sessionBootstrap(result.value);
}

async function bootstrap(
  page: Page,
): Promise<{ status: number; session?: SessionBootstrap }> {
  const result = await sessionRequest(
    page,
    "/api/v1/admin-sessions/current",
    "POST",
    undefined,
    undefined,
    {},
  );
  if (result.status === 200)
    return { status: result.status, session: sessionBootstrap(result.value) };
  return { status: result.status };
}

async function expiryResponse(
  page: Page,
  operation: () => Promise<unknown>,
): Promise<PlaywrightResponse> {
  const response = page.waitForResponse(
    (candidate) =>
      candidate.request().method() === "POST" &&
      candidate.url().endsWith("/api/v1/admin-sessions/current"),
  );
  await operation();
  return response;
}

async function assertSessionCookieAbsent(
  context: BrowserContext,
  baseURL: string,
): Promise<void> {
  const cookies = await context.cookies(baseURL);
  if (cookies.some((cookie) => cookie.name === "mcp_gateway_session"))
    fail("session cookie was not cleared");
}

async function connectAndCancelStream(page: Page, csrf: string): Promise<void> {
  const outcome = await page.evaluate(async (csrfToken) => {
    const controller = new AbortController();
    const response = await fetch("/api/v1/events", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: "{}",
      credentials: "same-origin",
      signal: controller.signal,
    });
    const reader = response.body?.getReader();
    const first = reader === undefined ? undefined : await reader.read();
    controller.abort();
    return {
      status: response.status,
      type: response.headers.get("Content-Type"),
      first:
        first?.value === undefined ? "" : new TextDecoder().decode(first.value),
    };
  }, csrf);
  if (
    outcome.status !== 200 ||
    outcome.type !== "text/event-stream" ||
    !outcome.first.includes(": keepalive")
  ) {
    fail("POST event stream reconnect/cancellation failed");
  }
}

async function runM1Canary(
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
    fail("M1 bootstrap canary failed");
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
  if (logout.status !== 204) fail("M1 logout canary failed");
  await assertSessionCookieAbsent(context, baseURL);
  process.stdout.write(
    `${JSON.stringify({
      event: "m1_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
    })}\n`,
  );
}

async function runProtocol(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  initialBearer: string,
  requestCount: () => number,
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

interface BrowserStorageSnapshot {
  local: Array<[string, string]>;
  session: Array<[string, string]>;
  databases: string[];
  caches: string[];
  registrations: number;
}

async function browserStorage(page: Page): Promise<BrowserStorageSnapshot> {
  return page.evaluate(async () => {
    const entries = (storage: Storage): Array<[string, string]> =>
      Array.from({ length: storage.length }, (_, index) => {
        const key = storage.key(index);
        if (key === null) throw new Error("storage enumeration changed");
        return [key, storage.getItem(key) ?? ""] as [string, string];
      }).sort(([left], [right]) => left.localeCompare(right));
    const databases =
      indexedDB.databases === undefined
        ? []
        : (await indexedDB.databases())
            .map((database) => database.name ?? "")
            .sort();
    const cacheNames = "caches" in window ? (await caches.keys()).sort() : [];
    const registrations =
      "serviceWorker" in navigator
        ? (await navigator.serviceWorker.getRegistrations()).length
        : 0;
    return {
      local: entries(localStorage),
      session: entries(sessionStorage),
      databases,
      caches: cacheNames,
      registrations,
    };
  });
}

function assertClosedStorage(
  snapshot: BrowserStorageSnapshot,
  expectedTheme?: "system" | "light" | "dark",
): void {
  const expected =
    expectedTheme === undefined ? [] : [["mcp_gateway_theme", expectedTheme]];
  if (
    JSON.stringify(snapshot.local) !== JSON.stringify(expected) ||
    snapshot.session.length !== 0 ||
    snapshot.databases.length !== 0 ||
    snapshot.caches.length !== 0 ||
    snapshot.registrations !== 0
  ) {
    fail("browser storage boundary changed");
  }
}

async function runFragmentStorage(
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
    ...["overview", "operations", "oauth", "credentials", "descriptors"].map(
      (tab): [string, string] => [
        `#/servers/${idA}?tab=${tab}`,
        tab === "overview" ? `#/servers/${idA}` : `#/servers/${idA}?tab=${tab}`,
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
    ["#/access/principals", "#/access/principals"],
    ["#/access/principals/new", "#/access/principals/new"],
    [`#/access/principals/${idA}`, `#/access/principals/${idA}`],
    ["#/access/grants", "#/access/grants"],
    ["#/access/grants/new", "#/access/grants/new"],
    [
      `#/access/grants/new?server_id=${idB}&principal_id=${idA}`,
      `#/access/grants/new?principal_id=${idA}&server_id=${idB}`,
    ],
    [`#/access/grants/${idA}`, `#/access/grants/${idA}`],
    [
      `#/access/grants?principal_id=${idA}`,
      `#/access/grants?principal_id=${idA}`,
    ],
    [`#/access/grants?server_id=${idB}`, `#/access/grants?server_id=${idB}`],
    [
      `#/access/grants?server_id=${idB}&principal_id=${idA}`,
      `#/access/grants?principal_id=${idA}&server_id=${idB}`,
    ],
    ["#/requests", "#/requests"],
    [`#/requests/${idA}`, `#/requests/${idA}`],
    ...["pending", "approved", "rejected", "cancelled"].map(
      (state): [string, string] => [
        `#/requests?state=${state}`,
        `#/requests?state=${state}`,
      ],
    ),
    [
      `#/requests?state=pending&principal_id=${idA}`,
      `#/requests?principal_id=${idA}&state=pending`,
    ],
    ["#/invocations", "#/invocations"],
    [`#/invocations/${idA}`, `#/invocations/${idA}`],
    [`#/invocations?principal_id=${idA}`, `#/invocations?principal_id=${idA}`],
    [`#/invocations?server_id=${idB}`, `#/invocations?server_id=${idB}`],
    ...[
      "invalid_params",
      "unknown_tool",
      "invalid_arguments",
      "authorization_unavailable",
      "evaluated",
    ].map((value): [string, string] => [
      `#/invocations?admission_class=${value}`,
      `#/invocations?admission_class=${value}`,
    ]),
    ...["allow", "deny", "block"].map((value): [string, string] => [
      `#/invocations?decision=${value}`,
      `#/invocations?decision=${value}`,
    ]),
    ...[
      "invalid_params",
      "unknown_tool",
      "invalid_arguments",
      "authorization_unavailable",
      "deny",
      "block",
      "prestart_failure",
      "succeeded",
      "downstream_failure",
      "outcome_unknown",
    ].map((value): [string, string] => [
      `#/invocations?outcome=${value}`,
      `#/invocations?outcome=${value}`,
    ]),
    [
      `#/invocations?outcome=succeeded&decision=allow&server_id=${idB}&principal_id=${idA}&admission_class=evaluated`,
      `#/invocations?principal_id=${idA}&server_id=${idB}&admission_class=evaluated&decision=allow&outcome=succeeded`,
    ],
    ["#/system", "#/system"],
    ...["status", "admin-credentials", "backups", "recovery"].map(
      (tab): [string, string] => [
        `#/system?tab=${tab}`,
        tab === "status" ? "#/system" : `#/system?tab=${tab}`,
      ],
    ),
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
    "#/requests?state=unknown",
    "#/invocations?decision=ALLOW",
    "#/invocations?outcome=unknown",
    "#/https://example.com",
    "#/overview/é",
    "#/overview/\n",
    `#/overview?x=${"a".repeat(2050)}`,
  ];
  for (const raw of invalid) {
    await page.evaluate((fragment) => {
      window.location.hash = fragment;
    }, raw);
    await page.waitForFunction(() => window.location.hash === "#/sign-in");
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

async function waitForLifecycle(
  page: Page,
  lifecycle:
    | "bootstrapping"
    | "signed_out"
    | "authenticated"
    | "reauthenticating",
): Promise<void> {
  try {
    await page.waitForFunction(
      (expected) =>
        document
          .querySelector('[data-testid="gateway-shell"]')
          ?.getAttribute("data-session-lifecycle") === expected,
      lifecycle,
    );
  } catch {
    fail(`session lifecycle did not reach ${lifecycle}`);
  }
}

async function assertSecretAbsent(
  page: Page,
  context: BrowserContext,
  baseURL: string,
  secrets: readonly string[],
  expectSessionCookie: boolean,
  expectedTheme?: "system" | "light" | "dark",
): Promise<void> {
  const state = await page.evaluate(() => ({
    url: window.location.href,
    html: document.documentElement.outerHTML,
    values: Array.from(
      document.querySelectorAll("input"),
      (input) => input.value,
    ),
  }));
  for (const secret of secrets) {
    if (
      state.url.includes(secret) ||
      state.html.includes(secret) ||
      state.values.some((value) => value.includes(secret))
    ) {
      fail("session authority reached a browser rendering sink");
    }
  }
  assertClosedStorage(await browserStorage(page), expectedTheme);
  const cookies = await context.cookies(baseURL);
  const sessions = cookies.filter(
    (cookie) => cookie.name === "mcp_gateway_session",
  );
  if (
    sessions.length !== (expectSessionCookie ? 1 : 0) ||
    sessions.some(
      (cookie) =>
        !cookie.httpOnly || cookie.path !== "/" || cookie.sameSite !== "Strict",
    )
  ) {
    fail("browser session cookie boundary changed");
  }
}

function sessionFixture(): Record<string, string> {
  return {
    csrf_token: "A".repeat(43),
    idle_expires_at: "2026-08-28T18:30:00Z",
    absolute_expires_at: "2026-08-29T18:00:00Z",
  };
}

async function assertSessionFoundationEpochs(): Promise<void> {
  if (
    parseSessionBootstrap(sessionFixture()) === undefined ||
    parseSessionBootstrap({ ...sessionFixture(), extra: "secret" }) !==
      undefined ||
    parseProblem({
      status: 401,
      code: "authentication_required",
      title: "Authentication is required.",
    }) === undefined ||
    parseProblem({
      status: 401,
      code: "authentication_required",
      title: "Authentication is required.",
      extra: "secret",
    }) !== undefined
  ) {
    fail("closed session validators changed");
  }

  let bootstrapCalls = 0;
  const request: typeof fetch = async (input, init) => {
    const path = String(input);
    if (path === "/api/v1/admin-sessions/current") {
      bootstrapCalls += 1;
      return new Response(
        JSON.stringify({
          status: 401,
          code: "authentication_required",
          title: "Authentication is required.",
        }),
        {
          status: 401,
          headers: { "Content-Type": "application/problem+json" },
        },
      );
    }
    if (
      path === "/api/v1/admin-sessions" &&
      init?.method === "POST" &&
      init.headers !== undefined
    ) {
      return new Response(JSON.stringify(sessionFixture()), {
        status: 201,
        headers: { "Content-Type": "application/json" },
      });
    }
    return new Response(null, { status: 204 });
  };
  const client = new SessionClient(request);
  const lifecycles: string[] = [];
  client.subscribe((snapshot) => lifecycles.push(snapshot.lifecycle));
  client.start();
  for (let index = 0; index < 8; index += 1) await Promise.resolve();
  if (client.snapshot().lifecycle !== "signed_out")
    fail("initial session bootstrap did not settle safely");
  if (!(await client.exchange("mgw_admin_epoch-test-canary")))
    fail("session foundation exchange failed");
  const lostEpoch = client.snapshot().epoch;

  let clearCount = 0;
  client.registerProtectedState(() => {
    clearCount += 1;
  });
  let release: (() => void) | undefined;
  const barrier = new Promise<void>((resolve) => {
    release = resolve;
  });
  let mutationSubmissions = 0;
  let abortObserved = false;
  let timerRan = false;
  client.scheduleProtected(() => {
    timerRan = true;
  }, 0);
  const lateRead = client.runProtected(async ({ signal }) => {
    signal.addEventListener("abort", () => {
      abortObserved = true;
    });
    await barrier;
    return {
      read: "late-read",
      bearer: "mgw_admin_late-bearer",
      oauthURL: "https://secret.invalid/callback",
      event: "late-event",
    };
  });
  const lateMutation = client.runProtected(async () => {
    mutationSubmissions += 1;
    await barrier;
    return "late-mutation";
  });
  const firstRecovery = client.recoverAfterSessionLoss();
  const duplicateRecovery = client.recoverAfterSessionLoss();
  if (firstRecovery !== duplicateRecovery)
    fail("session loss started duplicate bootstrap work");
  release?.();
  const [readResult, mutationResult] = await Promise.all([
    lateRead,
    lateMutation,
  ]);
  await Promise.all([firstRecovery, duplicateRecovery]);
  await client.recoverAfterSessionLoss(lostEpoch);
  await new Promise((resolve) => setTimeout(resolve, 0));
  if (
    readResult !== undefined ||
    mutationResult !== undefined ||
    mutationSubmissions !== 1 ||
    bootstrapCalls !== 2 ||
    timerRan ||
    !abortObserved ||
    clearCount !== 1 ||
    !lifecycles.includes("reauthenticating") ||
    client.snapshot().lifecycle !== "signed_out"
  ) {
    fail("authentication epoch did not fence prior work");
  }
}

async function runAuthenticationEpoch(
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

async function eventually(
  predicate: () => boolean,
  message: string,
  timeoutMilliseconds = 3000,
): Promise<void> {
  const deadline = performance.now() + timeoutMilliseconds;
  while (!predicate()) {
    if (performance.now() >= deadline) fail(message);
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

async function assertViewGenerationFoundation(): Promise<void> {
  if (
    parseInvalidation({ kind: "system_status", resource_id: null }) ===
      undefined ||
    parseInvalidation({
      kind: "servers",
      resource_id: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    }) === undefined ||
    parseInvalidation({
      kind: "servers",
      resource_id: null,
      authority: "forbidden",
    }) !== undefined
  ) {
    fail("closed invalidation validator changed");
  }

  const sessionRequest: typeof fetch = async (input) => {
    if (String(input) === "/api/v1/admin-sessions/current") {
      return new Response(
        JSON.stringify({
          status: 401,
          code: "authentication_required",
          title: "Authentication is required.",
        }),
        {
          status: 401,
          headers: { "Content-Type": "application/problem+json" },
        },
      );
    }
    return new Response(JSON.stringify(sessionFixture()), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  };
  const session = new SessionClient(sessionRequest);
  session.start();
  await eventually(
    () => session.snapshot().lifecycle === "signed_out",
    "view session did not settle signed out",
  );
  if (!(await session.exchange("mgw_admin_view-generation-canary")))
    fail("view session exchange failed");

  let visible = false;
  let visibilityListener = () => {};
  const visibility: VisibilitySource = {
    isVisible: () => visible,
    subscribe: (listener) => {
      visibilityListener = listener;
      return () => {
        visibilityListener = () => {};
      };
    },
  };
  const streamControllers: Array<ReadableStreamDefaultController<Uint8Array>> =
    [];
  let streamRequests = 0;
  const viewRequest: typeof fetch = async (input, init) => {
    if (
      String(input) !== "/api/v1/events" ||
      init?.method !== "POST" ||
      init.body !== "{}"
    ) {
      throw new Error("unexpected view request");
    }
    streamRequests += 1;
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        streamControllers.push(controller);
        controller.enqueue(new TextEncoder().encode(": keepalive\n\n"));
      },
    });
    return new Response(body, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    });
  };
  const coordinator = new ViewCoordinator(session, {
    request: viewRequest,
    visibility,
    reconnectMilliseconds: 20,
  });
  let aCalls = 0;
  let bCalls = 0;
  let publishedA = "";
  let publishedB = "";
  let releaseLate: (() => void) | undefined;
  const late = new Promise<void>((resolve) => {
    releaseLate = resolve;
  });
  let staleReadAborted = false;
  coordinator.registerPanel<string>({
    id: "a",
    matches: () => true,
    invalidations: ["system_status"],
    pollMilliseconds: 40,
    read: async ({ signal }) => {
      aCalls += 1;
      if (aCalls === 2) {
        signal.addEventListener("abort", () => {
          staleReadAborted = true;
        });
        await late;
        return "late";
      }
      return aCalls === 1 ? "initial" : `a-${aCalls}`;
    },
    publish: (value) => {
      publishedA = value;
    },
  });
  coordinator.registerPanel<string>({
    id: "b",
    matches: () => true,
    invalidations: ["backups"],
    pollMilliseconds: 40,
    read: async () => {
      bCalls += 1;
      if (bCalls === 2) throw new Error("isolated panel failure");
      return `b-${bCalls}`;
    },
    publish: (value) => {
      publishedB = value;
    },
  });
  coordinator.activate("#/overview");
  await eventually(
    () =>
      publishedA === "initial" &&
      publishedB === "b-1" &&
      coordinator.snapshot().freshness === "current",
    "initial view snapshot did not become current",
  );

  coordinator.manualRefresh();
  await eventually(
    () => coordinator.snapshot().panels.b?.status === "error",
    "panel failure was not isolated",
  );
  if (coordinator.snapshot().panels.a?.status !== "stale")
    fail("matching prior snapshot was not labeled stale");
  coordinator.navigate("#/servers");
  await eventually(
    () => publishedA === "a-3" && publishedB === "b-3",
    "new view generation did not publish",
  );
  releaseLate?.();
  await Promise.resolve();
  if (publishedA !== "a-3" || !staleReadAborted)
    fail("superseded view read was not aborted and discarded");

  const generationBeforeEvents = coordinator.snapshot().generation;
  const bCallsBeforeEvents = bCalls;
  const eventFrame = new TextEncoder().encode(
    'event: invalidate\ndata: {"kind":"system_status","resource_id":null}\n\n',
  );
  streamControllers[0]?.enqueue(eventFrame);
  streamControllers[0]?.enqueue(eventFrame);
  await eventually(
    () => coordinator.snapshot().generation > generationBeforeEvents,
    "coalesced invalidation did not refresh",
  );
  if (
    coordinator.snapshot().generation !== generationBeforeEvents + 1 ||
    bCalls !== bCallsBeforeEvents
  ) {
    fail("invalidations were not coalesced to their matching visible panel");
  }

  const callsBeforeVisible = aCalls;
  const bCallsBeforeVisible = bCalls;
  visible = true;
  visibilityListener();
  await eventually(
    () => aCalls > callsBeforeVisible && bCalls > bCallsBeforeVisible,
    "equal-interval visible panel polling did not resume as one group",
  );
  visible = false;
  visibilityListener();
  await new Promise((resolve) => setTimeout(resolve, 80));
  const callsWhileHidden = aCalls;
  await new Promise((resolve) => setTimeout(resolve, 100));
  if (aCalls !== callsWhileHidden)
    fail("hidden document polling did not pause");

  const generationBeforeReconnect = coordinator.snapshot().generation;
  streamControllers[0]?.close();
  await eventually(
    () => coordinator.snapshot().freshness === "reconnecting",
    "stream loss was not labeled reconnecting",
  );
  await eventually(
    () =>
      streamRequests === 2 &&
      coordinator.snapshot().freshness === "current" &&
      coordinator.snapshot().generation > generationBeforeReconnect,
    "reconnect did not reload the visible snapshot",
  );
  coordinator.close();
}

async function runReadGeneration(
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

function problemResponse(status: number, code: string): Response {
  return new Response(
    JSON.stringify({ status, code, title: `Safe ${code} response.` }),
    {
      status,
      headers: { "Content-Type": "application/problem+json" },
    },
  );
}

function mutationSpec(
  overrides: Partial<MutationSpec<string>> = {},
): MutationSpec<string> {
  return {
    route: "/api/v1/servers",
    method: "POST",
    body: '{"namespace":"alpha"}',
    precondition: null,
    requiresPrecondition: false,
    idempotency: "server_create",
    successStatuses: [201],
    decode: async (response) => {
      if (response.headers.get("Content-Type") !== "application/json")
        throw new Error("invalid success type");
      const value = (await response.json()) as unknown;
      if (
        typeof value !== "object" ||
        value === null ||
        !("result" in value) ||
        typeof value.result !== "string"
      ) {
        throw new Error("invalid success body");
      }
      return value.result;
    },
    ...overrides,
  };
}

async function assertMutationFoundation(): Promise<void> {
  const fakeSessionRequest: typeof fetch = async (input) => {
    if (String(input) === "/api/v1/admin-sessions/current") {
      return problemResponse(401, "authentication_required");
    }
    return new Response(JSON.stringify(sessionFixture()), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  };
  const session = new SessionClient(fakeSessionRequest);
  session.start();
  await eventually(
    () => session.snapshot().lifecycle === "signed_out",
    "mutation session did not settle signed out",
  );
  if (!(await session.exchange("mgw_admin_mutation-state-canary")))
    fail("mutation session exchange failed");

  interface ObservedMutation {
    route: string;
    method: string;
    body: string | null;
    precondition: string | null;
    idempotencyKey: string | null;
    csrf: string | null;
  }
  const observed: ObservedMutation[] = [];
  const steps: Array<() => Promise<Response>> = [];
  const request: typeof fetch = async (input, init) => {
    const headers = new Headers(init?.headers);
    observed.push({
      route: String(input),
      method: init?.method ?? "",
      body: typeof init?.body === "string" ? init.body : null,
      precondition: headers.get("If-Match"),
      idempotencyKey: headers.get("Idempotency-Key"),
      csrf: headers.get("X-CSRF-Token"),
    });
    const step = steps.shift();
    if (step === undefined) throw new Error("unexpected mutation request");
    return step();
  };
  let refreshes = 0;
  let keySequence = 0;
  const coordinator = new MutationCoordinator(session, {
    request,
    refreshCurrent: () => {
      refreshes += 1;
    },
    key: () => `test-key-${(keySequence += 1)}`,
  });
  const controller = coordinator.create<string>();

  let releaseFirst: (() => void) | undefined;
  const firstBarrier = new Promise<void>((resolve) => {
    releaseFirst = resolve;
  });
  steps.push(async () => {
    await firstBarrier;
    throw new Error("post-handoff transport loss");
  });
  controller.begin(mutationSpec());
  controller.confirm();
  const initial = controller.submit();
  const duplicate = controller.submit();
  if (initial !== duplicate || controller.snapshot().state !== "submitting")
    fail("duplicate submission was not fenced");
  releaseFirst?.();
  const uncertain = await initial;
  if (
    uncertain.kind !== "uncertain" ||
    !controller.snapshot().canReplay ||
    observed.length !== 1 ||
    observed[0]?.idempotencyKey !== "test-key-1" ||
    observed[0]?.csrf !== "A".repeat(43)
  ) {
    fail("idempotent uncertainty tuple was not retained exactly");
  }
  await Promise.resolve();
  if (observed.length !== 1) fail("uncertain mutation replayed automatically");

  steps.push(
    async () =>
      new Response('{"result":"replayed"}', {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
  );
  const replayed = await controller.replay();
  if (
    replayed.kind !== "acknowledged" ||
    replayed.value !== "replayed" ||
    observed[1]?.idempotencyKey !== "test-key-1" ||
    controller.snapshot().canReplay
  ) {
    fail("explicit same-intent replay changed its tuple");
  }

  steps.push(async () => {
    throw new Error("uncertain first edit");
  });
  controller.begin(mutationSpec({ body: '{"namespace":"bravo"}' }));
  await controller.submit();
  if (observed[2]?.idempotencyKey !== "test-key-2")
    fail("edited idempotent intent did not mint a new tuple");
  steps.push(
    async () =>
      new Response('{"result":"edited"}', {
        status: 201,
        headers: { "Content-Type": "application/json" },
      }),
  );
  controller.begin(mutationSpec({ body: '{"namespace":"charlie"}' }));
  const edited = await controller.submit();
  if (
    edited.kind !== "acknowledged" ||
    observed[3]?.idempotencyKey !== "test-key-3"
  ) {
    fail("different intent reused an uncertain idempotency tuple");
  }

  const resourceID = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
  const precondition = `"server-${resourceID}-7"`;
  const conditional = mutationSpec({
    route: `/api/v1/servers/${resourceID}`,
    method: "PATCH",
    body: '{"display_name":"updated"}',
    precondition,
    requiresPrecondition: true,
    idempotency: "none",
    successStatuses: [200],
  });
  for (const [status, code, shouldRefresh] of [
    [412, "stale_revision", true],
    [428, "precondition_required", true],
    [409, "conflict", true],
    [429, "resource_limit", false],
    [503, "keyring_unavailable", false],
  ] as const) {
    steps.push(async () => problemResponse(status, code));
    controller.begin(conditional);
    controller.confirm();
    const outcome = await controller.submit();
    if (
      outcome.kind !== "rejected" ||
      outcome.requiresRefresh !== shouldRefresh ||
      controller.snapshot().requiresRefresh !== shouldRefresh ||
      observed.at(-1)?.precondition !== precondition
    ) {
      fail(`conditional mutation classification changed for ${status}`);
    }
  }
  if (refreshes !== 3)
    fail("conflicts did not trigger exact authoritative refreshes");

  steps.push(async () => problemResponse(503, "storage_unavailable"));
  controller.begin(conditional);
  const latched = await controller.submit();
  if (
    latched.kind !== "uncertain" ||
    coordinator.snapshot() !== "storage_latched" ||
    controller.snapshot().availability !== "storage_latched"
  ) {
    fail("storage-latched response did not close global mutation admission");
  }
  const blocked = coordinator.create<string>();
  blocked.begin(conditional);
  const requestCountAtLatch = observed.length;
  if (
    (await blocked.submit()).kind !== "discarded" ||
    observed.length !== requestCountAtLatch
  ) {
    fail("storage latch admitted a new mutation");
  }
  coordinator.setStorageLatched(false);

  steps.push(async () => {
    throw new Error("non-idempotent transport loss");
  });
  blocked.begin(conditional);
  const nonIdempotent = await blocked.submit();
  if (
    nonIdempotent.kind !== "uncertain" ||
    blocked.snapshot().canReplay ||
    (await blocked.replay()).kind !== "discarded"
  ) {
    fail("non-idempotent uncertainty offered replay");
  }

  const invalidResponse = coordinator.create<string>();
  steps.push(
    async () =>
      new Response("not-json", {
        status: 201,
        headers: { "Content-Type": "text/plain" },
      }),
  );
  invalidResponse.begin(mutationSpec());
  const invalid = await invalidResponse.submit();
  if (invalid.kind !== "uncertain" || !invalidResponse.snapshot().canReplay)
    fail("invalid post-handoff success was not uncertain");

  const epochTuple = coordinator.create<string>();
  steps.push(async () => {
    throw new Error("epoch-loss uncertainty");
  });
  epochTuple.begin(mutationSpec());
  await epochTuple.submit();
  if (!epochTuple.snapshot().canReplay)
    fail("epoch tuple setup did not become uncertain");
  await session.recoverAfterSessionLoss();
  if (
    epochTuple.snapshot().state !== "editing" ||
    epochTuple.snapshot().canReplay
  ) {
    fail("authentication epoch loss retained mutation recovery state");
  }

  const requestCountBeforeInvalid = observed.length;
  let invalidSpecRejected = false;
  try {
    controller.begin(
      mutationSpec({
        route: "/api/v1/servers?unsafe=true",
        idempotency: "server_create",
      }),
    );
  } catch {
    invalidSpecRejected = true;
  }
  if (!invalidSpecRejected || observed.length !== requestCountBeforeInvalid)
    fail("invalid mutation reached handoff");

  const routeValidation = coordinator.create<string>();
  routeValidation.begin(
    mutationSpec({
      route: "/api/v1/backups",
      body: "{}",
      idempotency: "backup_create",
    }),
  );
  routeValidation.abandon();
  routeValidation.begin(
    mutationSpec({
      route: `/api/v1/servers/${resourceID}/operations`,
      body: '{"kind":"reload"}',
      precondition,
      requiresPrecondition: true,
      idempotency: "operation_start",
      successStatuses: [200, 202],
    }),
  );
  routeValidation.abandon();
  let missingMechanicsRejected = false;
  try {
    routeValidation.begin(
      mutationSpec({
        route: `/api/v1/servers/${resourceID}/operations`,
        body: '{"kind":"reload"}',
        idempotency: "none",
      }),
    );
  } catch {
    missingMechanicsRejected = true;
  }
  if (!missingMechanicsRejected)
    fail("route-specific idempotency and precondition mechanics were optional");
  coordinator.close();
}

async function assertSensitiveSinkFoundation(): Promise<void> {
  const fakeSessionRequest: typeof fetch = async (input) => {
    if (String(input) === "/api/v1/admin-sessions/current") {
      return problemResponse(401, "authentication_required");
    }
    return new Response(JSON.stringify(sessionFixture()), {
      status: 201,
      headers: { "Content-Type": "application/json" },
    });
  };
  const session = new SessionClient(fakeSessionRequest);
  session.start();
  await eventually(
    () => session.snapshot().lifecycle === "signed_out",
    "sink session did not settle signed out",
  );
  if (!(await session.exchange("mgw_admin_sink-foundation-canary")))
    fail("sink session exchange failed");

  const copiedValues: string[] = [];
  if (
    (await copyToClipboard("copy-canary", async (value) => {
      copiedValues.push(value);
    })) !== "copied" ||
    copiedValues.join("") !== "copy-canary" ||
    (await copyToClipboard("failure-canary", async () => {
      throw new Error("clipboard denied");
    })) !== "failed"
  ) {
    fail("clipboard success and failure were not classified safely");
  }
  const popup = { opener: "retained" } as unknown as WindowProxy;
  const openArguments: string[] = [];
  if (
    openOAuthWindow(
      "https://auth.example/authorize",
      (target, name, features) => {
        openArguments.push(target, name, features);
        return popup;
      },
    ) !== "opened" ||
    openArguments.join("|") !==
      "https://auth.example/authorize|_blank|noopener,noreferrer" ||
    popup.opener !== null ||
    openOAuthWindow("https://auth.example/authorize", () => null) !== "blocked"
  ) {
    fail("OAuth opener did not enforce its closed user-gesture mechanics");
  }

  const coordinator = new SensitiveSinkCoordinator(session);
  if (coordinator.prepareOneTime("Unavailable display") !== undefined)
    fail("secret-bearing mutation admitted without a prepared presenter");

  let displayedSecret = "";
  let oneTimeGeneration = 0;
  let oneTimeLost = false;
  let oneTimeClears = 0;
  const oneTimePresenter: OneTimePresenter = {
    prepare: (_label, generation) => {
      oneTimeGeneration = generation;
      oneTimeLost = false;
      return true;
    },
    publish: (value, generation) => {
      if (generation !== oneTimeGeneration) return false;
      displayedSecret = value;
      return true;
    },
    lose: (generation) => {
      if (generation !== oneTimeGeneration) return;
      displayedSecret = "";
      oneTimeLost = true;
    },
    clear: () => {
      displayedSecret = "";
      oneTimeLost = false;
      oneTimeClears += 1;
    },
  };
  coordinator.registerOneTimePresenter(oneTimePresenter);
  const bearerCanary = `mgw_admin_${"B".repeat(43)}`;
  const prepared = coordinator.prepareOneTime("New administrator bearer");
  if (prepared === undefined || displayedSecret !== "")
    fail("one-time display was not pre-created while blank");
  if (
    prepared.publish(bearerCanary) !== "published" ||
    displayedSecret !== bearerCanary
  )
    fail("prepared one-time display did not receive the exact bearer");
  coordinator.dismiss(oneTimeGeneration);
  if (displayedSecret !== "" || oneTimeClears === 0)
    fail("one-time dismissal retained its string");

  const uncertain = coordinator.prepareOneTime("Uncertain bearer response");
  if (uncertain === undefined) fail("uncertain sink setup failed");
  uncertain.lose();
  if (!oneTimeLost || displayedSecret !== "")
    fail("lost one-time response retained or echoed a value");
  coordinator.dismiss(oneTimeGeneration);

  const navigated = coordinator.prepareOneTime("Navigation fence");
  if (navigated === undefined) fail("navigation sink setup failed");
  coordinator.clearForNavigation();
  if (navigated.publish(bearerCanary) !== "lost" || displayedSecret !== "")
    fail("navigation accepted a late one-time value");

  const writeOnly = coordinator.createWriteOnly();
  const input = { value: "" } as HTMLInputElement;
  writeOnly.attach(input);
  input.value = "write-only-canary";
  if (writeOnly.read() !== "write-only-canary")
    fail("write-only field did not expose its live submission value");
  coordinator.clearForNavigation();
  if (input.value !== "") fail("navigation retained a write-only value");

  let oauthURL = "";
  const currentOAuthURL = () => oauthURL;
  let oauthGeneration = 0;
  let oauthLost = false;
  const oauthPresenter: OAuthPresenter = {
    prepare: (_label, generation) => {
      oauthGeneration = generation;
      oauthLost = false;
      return true;
    },
    publish: (value, generation) => {
      if (generation !== oauthGeneration) return false;
      oauthURL = value;
      return true;
    },
    lose: (generation) => {
      if (generation !== oauthGeneration) return;
      oauthURL = "";
      oauthLost = true;
    },
    clear: () => {
      oauthURL = "";
      oauthLost = false;
    },
  };
  coordinator.registerOAuthPresenter(oauthPresenter);
  const oauth = coordinator.prepareOAuth("Authorize local server");
  const validURL =
    "https://auth.example/authorize?client_id=public&state=opaque";
  if (
    oauth === undefined ||
    oauth.publish(validURL) !== "published" ||
    oauthURL !== validURL
  )
    fail("prepared OAuth display rejected a canonical URL");
  coordinator.dismiss(oauthGeneration);
  const invalidOAuth = coordinator.prepareOAuth("Reject active URL");
  if (
    invalidOAuth === undefined ||
    invalidOAuth.publish("javascript:alert(1)") !== "lost" ||
    !oauthLost ||
    currentOAuthURL() !== ""
  ) {
    fail("OAuth sink accepted an active or invalid URL");
  }
  coordinator.dismiss(oauthGeneration);

  const epoch = coordinator.prepareOneTime("Epoch fence");
  if (epoch === undefined) fail("epoch sink setup failed");
  await session.recoverAfterSessionLoss();
  if (
    epoch.publish(bearerCanary) !== "lost" ||
    displayedSecret !== "" ||
    input.value !== ""
  )
    fail("authentication epoch loss retained sensitive sink state");
  writeOnly.close();
  coordinator.close();
}

async function runSecretSinks(
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
  await page.route("**/__oauth_sink_target**", async (route) => {
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

async function runShellPrimitives(
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
  if (
    (await bearerInput.getAttribute("aria-describedby")) !== "admin-bearer-hint"
  )
    fail("shared form field did not associate its hint");
  await bearerInput.fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  if (
    (await page.locator("header").count()) !== 1 ||
    (await page.locator("main").count()) !== 1 ||
    (await page.locator("footer").count()) !== 1 ||
    (await page.locator("h1").count()) !== 1
  ) {
    fail("operational shell landmarks or heading hierarchy changed");
  }

  const logout = page.locator('[data-testid="logout"]');
  await logout.focus();
  await logout.click();
  const dialog = page.locator("dialog.confirmation-dialog");
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
    !((await authStatus.textContent()) ?? "").includes("✓") ||
    !((await authStatus.textContent()) ?? "").includes("Authenticated")
  ) {
    fail("operational state depended on color alone");
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

async function runMutationState(
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

let browser: Browser | undefined;
try {
  let input = parseInitialInput(await readBoundedInput());
  const baseURL = input.base_url;
  const initialBearer = input.admin_bearer;
  input = { ...input, admin_bearer: "" };
  browser = await chromium.launch({ headless: true });
  const context = await browser.newContext({
    baseURL,
    serviceWorkers: "block",
  });
  const externalRequests: string[] = [];
  const originFailures: string[] = [];
  const requestHeaderChecks: Array<Promise<void>> = [];
  let requests = 0;
  context.on("request", (request) => {
    requests += 1;
    if (!request.url().startsWith(baseURL))
      externalRequests.push(request.url());
    if (
      request.url().startsWith(`${baseURL}/api/`) &&
      request.method() !== "GET"
    ) {
      requestHeaderChecks.push(
        request.allHeaders().then((headers) => {
          if (headers.origin !== baseURL) originFailures.push(request.url());
        }),
      );
    }
  });
  const consoleFailures: string[] = [];
  context.on("page", (candidate) => {
    candidate.on("console", (message) => {
      if (
        message.type() === "error" &&
        !message
          .text()
          .startsWith(
            "Failed to load resource: the server responded with a status of 401",
          )
      ) {
        consoleFailures.push(message.text());
      }
    });
    candidate.on("pageerror", (error) => consoleFailures.push(error.name));
  });
  const page = await context.newPage();
  await loadShell(page);

  if (input.scenario === "shell-load") {
    if (externalRequests.length !== 0) fail("external shell request");
    process.stdout.write('{"event":"shell_loaded"}\n');
    process.on("SIGTERM", () => {});
    setInterval(() => {}, 60 * 60 * 1000);
  } else {
    if (input.scenario === "m1-canary") {
      await runM1Canary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "fragment-storage") {
      await runFragmentStorage(browser.version(), page, () => requests);
    } else if (input.scenario === "authentication-epoch") {
      await runAuthenticationEpoch(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "read-generation") {
      await runReadGeneration(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "mutation-state") {
      await runMutationState(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "shell-primitives") {
      await runShellPrimitives(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "secret-sinks") {
      await runSecretSinks(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else {
      await runProtocol(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    }
    await Promise.all(requestHeaderChecks);
    if (
      externalRequests.length !== 0 ||
      originFailures.length !== 0 ||
      consoleFailures.length !== 0
    ) {
      fail(
        `unexpected browser protocol side effect (external=${externalRequests.length}, origin=${originFailures.length}, console=${consoleFailures.length})`,
      );
    }
    if (
      (await page.evaluate(() => document.cookie)).includes(
        "mcp_gateway_session",
      )
    ) {
      fail("HttpOnly session cookie became script-visible");
    }
    await browser.close();
  }
} catch (error) {
  if (browser !== undefined) await browser.close();
  const detail =
    error instanceof Error
      ? error.message.replace(/mgw_admin_[A-Za-z0-9_-]+/g, "[redacted]")
      : "unknown";
  process.stderr.write(`browser coordinator failed: ${detail}\n`);
  process.exitCode = 3;
}
