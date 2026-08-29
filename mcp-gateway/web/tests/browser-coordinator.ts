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
    | "secret-sinks"
    | "m3-canary"
    | "m5-canary"
    | "m6-canary"
    | "principals"
    | "principal-credentials"
    | "grant-reads-create"
    | "grant-correction"
    | "overview"
    | "invocations"
    | "system-status"
    | "server-catalog-reads"
    | "server-create-update"
    | "server-operations"
    | "server-credentials"
    | "server-disconnect-delete"
    | "auth-flows";
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
      value.scenario !== "secret-sinks" &&
      value.scenario !== "m3-canary" &&
      value.scenario !== "m5-canary" &&
      value.scenario !== "m6-canary" &&
      value.scenario !== "principals" &&
      value.scenario !== "principal-credentials" &&
      value.scenario !== "grant-reads-create" &&
      value.scenario !== "grant-correction" &&
      value.scenario !== "overview" &&
      value.scenario !== "invocations" &&
      value.scenario !== "system-status" &&
      value.scenario !== "server-catalog-reads" &&
      value.scenario !== "server-create-update" &&
      value.scenario !== "server-operations" &&
      value.scenario !== "server-credentials" &&
      value.scenario !== "server-disconnect-delete" &&
      value.scenario !== "auth-flows") ||
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

async function runM3Canary(
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

  const fragmentCanary = `M3_FRAGMENT_${"F".repeat(40)}`;
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

  const lateCanary = `M3_LATE_${"L".repeat(40)}`;
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
      event: "m3_complete",
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

const overviewLimitNames = [
  "http_regular",
  "http_control_auth",
  "http_admin",
  "http_health",
  "mcp_work",
  "mcp_streams",
  "admin_sessions",
  "legacy_sessions",
  "event_streams",
  "backup_work",
  "backup_records",
  "admin_credentials",
  "idempotency_records",
  "keyring_candidates",
  "keyring_work",
  "database_bytes",
  "server_identities",
  "servers",
  "downstream_runtimes",
  "server_reconciliations",
  "catalog_traversals",
  "oauth_flows",
  "oauth_callback_work",
  "s2_idempotency_records",
  "active_tools",
  "durable_tool_identities",
  "downstream_dispatch",
  "principals",
  "grants",
  "grant_requests",
  "grant_request_evidence_bytes",
] as const;

function overviewStatusFixture() {
  const limits = Object.fromEntries(
    overviewLimitNames.map((name) => [
      name,
      { in_use: 0, limit: 64, saturated: false },
    ]),
  ) as Record<string, { in_use: number; limit: number; saturated: boolean }>;
  limits.servers = { in_use: 64, limit: 64, saturated: true };
  limits.database_bytes = {
    in_use: 858993460,
    limit: 1073741824,
    saturated: false,
  };
  return {
    process: {
      state: "storage_failed",
      ready: false,
      started_at: "2026-08-28T00:00:00Z",
    },
    sqlite: {
      state: "latched",
      schema_version: "10",
      revision: "7",
      latched: true,
    },
    keyring: { capability: "unavailable" },
    limits,
    backup: { state: "idle", last_completed_at: null },
    protocols: {
      modern: "2026-07-28",
      legacy: "2025-11-25",
      agent_auth: "principal_credentials",
    },
  };
}

function overviewServer(id: string, name: string, state: string) {
  return {
    id,
    namespace: `namespace-${id.slice(-4)}`,
    display_name: name,
    desired_state: "enabled",
    desired_revision: "1",
    transport: {
      kind: "stdio",
      executable: "/usr/bin/true",
      arguments: [],
      working_directory: "/tmp",
      environment: {},
      secret_environment: {},
    },
    credential_revisions: {
      static_credential: "0",
      oauth_client: "0",
      oauth_tokens: "0",
    },
    credential_state: "not_required",
    runtime: {
      state,
      reason: state === "active" ? null : "transport_failure",
      runtime_id: state === "active" ? "runtime-1" : null,
      reconciliation: { in_use: 0, limit: 1, saturated: false },
      dispatch: { in_use: 0, limit: 4, saturated: false },
    },
    catalog: {
      durable_state: state === "active" ? "current" : "stale",
      active_state: state === "active" ? "current" : "stale",
      durable_revision: "1",
      active_revision: state === "active" ? "1" : null,
      durable_tool_count: 1,
      active_tool_count: state === "active" ? 1 : 0,
      last_success_at: "2026-08-28T00:00:00Z",
      traversal: { in_use: 0, limit: 4, saturated: false },
    },
    created_at: "2026-08-28T00:00:00Z",
    updated_at: "2026-08-28T00:00:00Z",
    deleted_at: null,
  };
}

function overviewRequestFixture() {
  return {
    id: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    principal_id: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
    state: "pending",
    revision: "1",
    requested_policy: {
      scope: "server",
      target: "long-server",
      constraint: null,
      duration_seconds: null,
      future_tools_acknowledged: true,
    },
    approved_policy: null,
    approved_grant_id: null,
    rejection_reason: null,
    created_at: "2026-08-28T00:00:00Z",
    updated_at: "2026-08-28T00:00:00Z",
    closed_at: null,
  };
}

function overviewInvocationFixture() {
  return {
    id: "01ARZ3NDEKTSV4RRFFQ69G5FAX",
    principal_id: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
    credential_id: "01ARZ3NDEKTSV4RRFFQ69G5FAY",
    credential_fingerprint: "0123456789abcdef",
    credential_revision: "1",
    admitted_at: "2026-08-28T00:00:00Z",
    admission_class: "evaluated",
    requested_name: `literal-<script>${"L".repeat(96)}`,
    target: {
      kind: "downstream",
      server_id: "01ARZ3NDEKTSV4RRFFQ69G5FA0",
      tool_id: "01ARZ3NDEKTSV4RRFFQ69G5FA3",
      upstream_name: "long-tool",
      descriptor_revision: "1",
      descriptor_fingerprint: "0123456789abcdef",
    },
    authorization: {
      decision: "allow",
      revision: "2",
      evaluated_at: "2026-08-28T00:00:01Z",
      grant_id: "01ARZ3NDEKTSV4RRFFQ69G5FA4",
    },
    outcome: {
      class: "outcome_unknown",
      basis: "missing_terminal",
      completed_at: null,
    },
  };
}

async function runM5Canary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
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
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.waitForFunction(() =>
    [
      "overview-status",
      "overview-servers",
      "overview-requests",
      "overview-invocations",
    ].every(
      (id) =>
        document
          .querySelector(`[data-testid="${id}"]`)
          ?.getAttribute("data-panel-status") === "current",
    ),
  );
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Operational posture",
    "Server attention",
    "Pending requests",
    "Recent invocations",
  ])
    if (!body.includes(phrase)) fail(`M5 Overview canary omitted ${phrase}`);
  if (body.includes("redacted_arguments"))
    fail("M5 Overview canary exposed invocation capture");

  await page.evaluate(() => {
    window.location.hash = "#/invocations";
  });
  await page.locator('[data-testid="invocations-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="invocations-view"]')
        ?.textContent?.includes("No retained invocations match") === true,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("bounded recent window of at most 4,096 rows") ||
    body.includes("redacted_arguments")
  )
    fail("M5 invocation canary omitted bounds or exposed capture");

  await page.evaluate(() => {
    window.location.hash = "#/system";
  });
  await page.locator('[data-testid="system-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );
  if ((await page.locator('[data-testid="system-limit-row"]').count()) !== 31)
    fail("M5 System canary omitted closed limits");

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=recovery";
  });
  await page.locator('[data-testid="system-recovery"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("The browser never invokes these commands") ||
    !body.includes("mcp-gateway restore --verify-current")
  )
    fail("M5 recovery canary omitted stopped-process boundary");
  if (
    (await page.locator('[data-testid="system-recovery"] button').count()) !== 0
  )
    fail("M5 recovery canary exposed online offline-authority controls");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "m5_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: 4 })}\n`,
  );
}

async function runM6Canary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const serverID = serverReadIDs.active;
  const server = {
    ...serverReadFixture(serverID, {
      name: "M6 integrated server",
      desired: "enabled",
      runtime: "authentication_required",
      credential: "reauthentication_required",
      durable: "current",
      active: "unavailable",
    }),
    transport: {
      kind: "streamable_http",
      url: "https://resource.example/mcp",
      protocol_mode: "modern",
      authentication: {
        mode: "oauth",
        registration: {
          mode: "static",
          issuer: "https://issuer.example/",
          client_id: "m6-client",
          token_endpoint_auth_method: "client_secret_basic",
        },
        trusted_origins: [],
        request_offline_access: false,
      },
    },
  };
  await page.route(`${baseURL}/api/v1/servers/${serverID}`, async (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"server-${serverID}-7"` },
      body: JSON.stringify(server),
    }),
  );
  for (const resource of ["operations", "auth-flows", "descriptors"]) {
    await page.route(
      `${baseURL}/api/v1/servers/${serverID}/${resource}?*`,
      async (route) =>
        route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({ items: [], next_cursor: null }),
        }),
    );
  }
  await page.route(`${baseURL}/api/v1/catalog?*`, async (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        catalog: {
          active_state: "current",
          active_generation: "m6-generation",
          changed_at: "2026-08-28T17:00:00Z",
          issue_count: 0,
        },
        items: [
          descriptorReadFixture(
            serverReadIDs.activeTool,
            serverID,
            "m6_integrated_tool",
            false,
          ),
        ],
        next_cursor: null,
      }),
    }),
  );

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}`;
  }, serverID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");

  const destinations: Array<[string, string]> = [
    [`#/servers/${serverID}`, "server-detail"],
    [`#/servers/${serverID}?tab=operations`, "operation-list"],
    [`#/servers/${serverID}?tab=oauth`, "auth-flow-list"],
    [`#/servers/${serverID}?tab=credentials`, "server-credentials-view"],
    [`#/servers/${serverID}?tab=descriptors`, "descriptor-list"],
    ["#/catalog", "catalog-view"],
  ];
  for (const [hash, testID] of destinations) {
    await page.evaluate((target) => {
      window.location.hash = target;
    }, hash);
    await page.locator(`[data-testid="${testID}"]`).waitFor();
  }
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}`;
  }, serverID);
  await page.getByText("Edit desired server state", { exact: true }).click();
  await page.locator('[data-testid="server-editor"]').waitFor();
  await page.locator('[data-testid="server-destructive-actions"]').waitFor();
  await page.locator('[data-testid="delete-server"]').click();
  const body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Desired revision 7",
    "Durable evidence is not proof",
    "best-effort remote revocation",
    "immutable namespace",
  ])
    if (!body.includes(phrase)) fail(`M6 integrated canary omitted ${phrase}`);
  if (body.includes("authorization_url"))
    fail("M6 integrated canary exposed a one-time URL outside its sink");
  await page.locator('[data-testid="server-delete-confirm-cancel"]').click();
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "m6_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: destinations.length })}\n`,
  );
}

async function runPrincipals(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const firstID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const secondID = "01ARZ3NDEKTSV4RRFFQ69G5FA1";
  const createdID = "01ARZ3NDEKTSV4RRFFQ69G5FA2";
  const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FB0";
  const principal = (
    id: string,
    displayName: string,
    state: "active" | "disabled",
    visibility: "requestable" | "allowed-only" | "all",
    revision: string,
  ) => ({
    id,
    display_name: displayName,
    state,
    visibility,
    revision,
    credential_revision: state === "disabled" ? "2" : "1",
    credential:
      state === "active"
        ? {
            id: "01ARZ3NDEKTSV4RRFFQ69G5FAZ",
            fingerprint: "0123456789abcdef",
            revision: "1",
            created_at: "2026-08-28T12:00:00Z",
          }
        : null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
  });
  let current = principal(firstID, "Build agent", "active", "requestable", "1");
  let detailReads = 0;
  let creates = 0;
  let updates = 0;
  let staleListRestarted = false;
  let staleReturned = false;

  await page.route("**/api/v1/principals?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "cursor")
    )
      fail("principal list request changed shape");
    if (query.get("cursor") === "principal-stale") {
      staleListRestarted = true;
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "stale_cursor",
          title: "The cursor is stale.",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        query.get("cursor") === "principal-next"
          ? {
              items: [
                principal(secondID, "Disabled agent", "disabled", "all", "4"),
              ],
              next_cursor: null,
            }
          : {
              items: [current],
              next_cursor: staleListRestarted
                ? "principal-next"
                : "principal-stale",
            },
      ),
    });
  });
  await page.route(`${baseURL}/api/v1/principals`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    creates += 1;
    const body = JSON.parse(route.request().postData() ?? "null") as Record<
      string,
      unknown
    >;
    if (
      body.display_name !== "New automation" ||
      body.visibility !== "allowed-only" ||
      Object.keys(body).sort().join(",") !== "display_name,visibility"
    )
      fail("principal create body changed shape");
    const created = principal(
      createdID,
      "New automation",
      "active",
      "allowed-only",
      "1",
    );
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      headers: { ETag: `"principal-${createdID}-1"` },
      body: JSON.stringify({
        principal: created,
        default_grant: {
          id: grantID,
          principal_id: createdID,
          effect: "allow",
          server_id: "00000000000000000000000000",
          upstream_name: null,
          constraint: null,
          expires_at: null,
          state: "active",
          created_at: "2026-08-28T13:00:00Z",
        },
      }),
    });
  });
  await page.route("**/api/v1/principals/*", async (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop();
    const request = route.request();
    if (request.method() === "GET") {
      detailReads += 1;
      const value =
        id === createdID
          ? principal(
              createdID,
              "New automation",
              "active",
              "allowed-only",
              "1",
            )
          : current;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"principal-${value.id}-${value.revision}"` },
        body: JSON.stringify(value),
      });
      return;
    }
    if (request.method() !== "PATCH" || id !== firstID)
      fail("principal member request changed shape");
    updates += 1;
    const patch = JSON.parse(request.postData() ?? "null") as Record<
      string,
      unknown
    >;
    const headers = await request.allHeaders();
    if (updates === 1) {
      if (
        patch.display_name !== "Renamed agent" ||
        Object.keys(patch).join(",") !== "display_name" ||
        headers["if-match"] !== `"principal-${firstID}-1"`
      )
        fail("principal display update changed shape");
      current = principal(
        firstID,
        "Renamed agent",
        "active",
        "requestable",
        "2",
      );
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"principal-${firstID}-2"` },
        body: JSON.stringify(current),
      });
      return;
    }
    if (patch.state !== "disabled" || Object.keys(patch).join(",") !== "state")
      fail("principal authority update changed shape");
    if (!staleReturned) {
      staleReturned = true;
      current = principal(
        firstID,
        "Renamed agent",
        "active",
        "requestable",
        "3",
      );
      await route.fulfill({
        status: 412,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 412,
          code: "stale_principal_revision",
          title: "The principal revision is stale.",
        }),
      });
      return;
    }
    if (headers["if-match"] !== `"principal-${firstID}-3"`)
      fail("principal stale revision was not refreshed");
    current = principal(
      firstID,
      "Renamed agent",
      "disabled",
      "requestable",
      "4",
    );
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${firstID}-4"` },
      body: JSON.stringify(current),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/access/principals";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="principal-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Permanent agent principals",
    "Visibility does not grant call authority",
    "Disabled agent",
  ])
    if (!body.includes(phrase)) fail(`principal list omitted ${phrase}`);

  await page.evaluate(() => {
    window.location.hash = "#/access/principals/new";
  });
  await page.locator('[data-testid="principal-create-view"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("permanent synthetic default ALLOW grant"))
    fail("principal creation omitted atomic default grant explanation");
  await page
    .locator('[data-testid="principal-display-name"]')
    .fill("New automation");
  await page
    .locator('[data-testid="principal-visibility"]')
    .selectOption("allowed-only");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page.locator('[data-testid="principal-detail"]').waitFor();

  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, firstID);
  await page.locator('[data-testid="principal-detail"]').waitFor();
  await page.getByText("Build agent", { exact: true }).waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "PERMANENT IDENTITY",
    "Visibility is not call authorization",
    "Credential authority",
    "Re-enabling restores neither",
  ])
    if (!body.includes(phrase)) fail(`principal detail omitted ${phrase}`);

  await page
    .locator('[data-testid="principal-display-name"]')
    .fill("Renamed agent");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page
    .getByRole("heading", { name: "Renamed agent", exact: true })
    .waitFor();
  await page
    .locator('[data-testid="principal-state"]')
    .selectOption("disabled");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page
    .getByText("The principal revision is stale.", { exact: true })
    .waitFor();
  if (
    (await page.locator('[data-testid="principal-state"]').inputValue()) !==
    "disabled"
  )
    fail("principal stale refresh discarded safe draft");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page
    .locator(".status-label.warning", { hasText: "disabled" })
    .waitFor();
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "principals_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), detail_reads: detailReads, creates, updates })}\n`,
  );
}

async function runPrincipalCredentials(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const credentialID = "01ARZ3NDEKTSV4RRFFQ69G5FAZ";
  const issuedBearer = `mgw_agent_${"I".repeat(43)}`;
  const lostBearer = `mgw_agent_${"L".repeat(43)}`;
  const principal = (revision: string, credential: boolean) => ({
    id: principalID,
    display_name: "Credential agent",
    state: "active",
    visibility: "requestable",
    revision,
    credential_revision: credential ? revision : String(Number(revision) + 1),
    credential: credential
      ? {
          id: credentialID,
          fingerprint: "0123456789abcdef",
          revision,
          created_at: "2026-08-28T12:00:00Z",
        }
      : null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
  });
  let current = principal("1", true);
  let issues = 0;
  let revokes = 0;
  let releaseLost: (() => void) | undefined;
  let markLostStarted: (() => void) | undefined;
  const lostStarted = new Promise<void>((resolve) => {
    markLostStarted = resolve;
  });

  await page.route("**/api/v1/principals/*", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${principalID}-${current.revision}"` },
      body: JSON.stringify(current),
    });
  });
  await page.route(
    `${baseURL}/api/v1/principals/${principalID}/credential`,
    async (route) => {
      const request = route.request();
      const headers = await request.allHeaders();
      if (
        request.postData() !== "{}" ||
        headers["if-match"] !== `"principal-${principalID}-${current.revision}"`
      )
        fail("principal credential request changed shape");
      if (request.method() === "POST") {
        issues += 1;
        if (issues === 1) {
          current = principal("2", true);
          await route.fulfill({
            status: 412,
            contentType: "application/problem+json",
            body: JSON.stringify({
              status: 412,
              code: "stale_principal_revision",
              title: "The principal revision is stale.",
            }),
          });
          return;
        }
        if (issues === 3) {
          markLostStarted?.();
          await new Promise<void>((resolve) => {
            releaseLost = resolve;
          });
          current = principal("4", true);
        } else {
          current = principal("3", true);
        }
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          headers: {
            ETag: `"principal-${principalID}-${current.revision}"`,
          },
          body: JSON.stringify({
            principal: current,
            bearer: issues === 2 ? issuedBearer : lostBearer,
          }),
        });
        return;
      }
      if (request.method() !== "DELETE")
        fail("principal credential method changed shape");
      revokes += 1;
      current = principal("5", false);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"principal-${principalID}-5"` },
        body: JSON.stringify(current),
      });
    },
  );

  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="principal-credential-actions"]').waitFor();
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "without overlap",
    "interrupted immediately",
    "cannot be recovered",
  ])
    if (!body.includes(phrase))
      fail(`principal credential warning omitted ${phrase}`);

  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page
    .getByText("The principal revision is stale.", { exact: true })
    .waitFor();
  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page.locator('[data-testid="one-time-value"]').waitFor();
  if (
    (await page.locator('[data-testid="one-time-value"]').textContent()) !==
    issuedBearer
  )
    fail("issued bearer did not reach the prepared one-time display");
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  if ((await page.locator('[data-testid="one-time-value"]').count()) !== 0)
    fail("dismissed bearer remained in the DOM");

  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await lostStarted;
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await waitForLifecycle(page, "signed_out");
  releaseLost?.();
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, issuedBearer, lostBearer],
    false,
  );

  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await page.locator('[data-testid="principal-credential-revoke"]').waitFor();
  await page.locator('[data-testid="principal-credential-revoke"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page.waitForFunction(
    () => document.body.textContent?.includes("No credential") === true,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Prior authority no longer authenticates"))
    fail("principal credential revoke omitted authority result");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, issuedBearer, lostBearer],
    true,
  );
  process.stdout.write(
    `${JSON.stringify({ event: "principal_credentials_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), issues, revokes })}\n`,
  );
}

async function runGrantReadsCreate(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const firstGrantID = "01ARZ3NDEKTSV4RRFFQ69G5FB0";
  const secondGrantID = "01ARZ3NDEKTSV4RRFFQ69G5FB1";
  const createdIDs = [
    "01ARZ3NDEKTSV4RRFFQ69G5FB2",
    "01ARZ3NDEKTSV4RRFFQ69G5FB3",
  ];
  const grant = (
    id: string,
    effect: "allow" | "deny",
    state: "active" | "expired",
    upstreamName: string | null,
    constraint: unknown | null,
    expiresAt: string | null,
  ) => ({
    id,
    principal_id: principalID,
    effect,
    server_id: serverID,
    upstream_name: upstreamName,
    constraint,
    expires_at: expiresAt,
    state,
    created_at: "2026-08-28T12:00:00Z",
  });
  const active = grant(firstGrantID, "allow", "active", null, null, null);
  const expired = grant(
    secondGrantID,
    "deny",
    "expired",
    "dangerous.tool",
    { equals: { "/mode": "blocked" } },
    "2026-08-28T12:30:00Z",
  );
  let staleRestarted = false;
  let attempts = 0;
  let creates = 0;

  await page.route("**/api/v1/grants?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      query.get("principal_id") !== principalID ||
      query.get("server_id") !== serverID ||
      [...query.keys()].some(
        (key) =>
          key !== "limit" &&
          key !== "cursor" &&
          key !== "principal_id" &&
          key !== "server_id",
      )
    )
      fail("grant list filters changed shape");
    const cursor = query.get("cursor");
    if (cursor === "grant-stale") {
      staleRestarted = true;
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "stale_cursor",
          title: "The cursor is stale.",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        cursor === "grant-next"
          ? { items: [expired], next_cursor: null }
          : {
              items: [active],
              next_cursor: staleRestarted ? "grant-next" : "grant-stale",
            },
      ),
    });
  });
  await page.route("**/api/v1/grants/*", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    const id = new URL(route.request().url()).pathname.split("/").pop();
    const item = id === secondGrantID ? expired : active;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(item),
    });
  });
  await page.route(`${baseURL}/api/v1/grants`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    attempts += 1;
    const raw = route.request().postData() ?? "";
    const body = JSON.parse(raw) as Record<string, unknown>;
    if (
      Object.keys(body).join(",") !==
        "principal_id,effect,server_id,upstream_name,constraint,expires_at" ||
      body.principal_id !== principalID ||
      body.server_id !== serverID
    )
      fail("grant create body changed closed shape");
    if (attempts === 1) {
      await route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 400,
          code: "invalid_grant",
          title: "The grant is invalid.",
        }),
      });
      return;
    }
    creates += 1;
    if (creates === 1) {
      if (
        body.effect !== "allow" ||
        body.upstream_name !== null ||
        body.constraint !== null ||
        body.expires_at !== null
      )
        fail("server-wide permanent grant changed shape");
    } else if (
      body.effect !== "deny" ||
      body.upstream_name !== "literal.tool" ||
      body.expires_at !== "2030-01-01T00:00:00Z" ||
      !raw.includes('"/a~1b/0":1.0') ||
      !raw.includes('"/empty/":null') ||
      !raw.includes('"/flag":true') ||
      !raw.includes('"/name":"literal"')
    )
      fail("exact-tool lexical constraint changed shape");
    const item = grant(
      createdIDs[creates - 1]!,
      body.effect as "allow" | "deny",
      "active",
      body.upstream_name as string | null,
      body.constraint,
      body.expires_at as string | null,
    );
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify(item),
    });
  });

  await page.evaluate(
    ({ principal, server }) => {
      window.location.hash = `#/access/grants?principal_id=${principal}&server_id=${server}`;
    },
    { principal: principalID, server: serverID },
  );
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="grant-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of ["Immutable grants", "Expired records", "ALLOW", "DENY"])
    if (!body.includes(phrase)) fail(`grant list omitted ${phrase}`);
  await page.evaluate((id) => {
    window.location.hash = `#/access/grants/${id}`;
  }, secondGrantID);
  await page.locator('[data-testid="grant-detail"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Exact tool dangerous.tool") || !body.includes("expired"))
    fail("grant detail omitted scope or retained expiry state");

  await page.evaluate(
    ({ principal, server }) => {
      window.location.hash = `#/access/grants/new?principal_id=${principal}&server_id=${server}`;
    },
    { principal: principalID, server: serverID },
  );
  await page.locator('[data-testid="grant-create-view"]').waitFor();
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page.getByText("The grant is invalid.", { exact: true }).waitFor();
  if (attempts !== 1) fail("rejected grant creation was replayed");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page.locator('[data-testid="grant-detail"]').waitFor();

  await page.evaluate(
    ({ principal, server }) => {
      window.location.hash = `#/access/grants/new?principal_id=${principal}&server_id=${server}`;
    },
    { principal: principalID, server: serverID },
  );
  await page.locator('[data-testid="grant-create-view"]').waitFor();
  await page.locator('[data-testid="grant-effect"]').selectOption("deny");
  await page.locator('[data-testid="grant-scope"]').selectOption("tool");
  await page.locator('[data-testid="grant-upstream"]').fill("literal.tool");
  await page
    .locator('[data-testid="grant-expiry"]')
    .fill("2030-01-01T00:00:00Z");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page.locator('[data-testid="constraint-pointer"]').fill("bad");
  await page.locator('[data-testid="constraint-type"]').selectOption("number");
  await page.locator('[data-testid="constraint-value"]').fill("1.0");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .getByText("Each atom requires a valid RFC 6901 JSON pointer.", {
      exact: true,
    })
    .waitFor();
  if (creates !== 1) fail("invalid grant constraint reached the API");
  await page.locator('[data-testid="constraint-pointer"]').fill("/a~1b/0");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page
    .locator('[data-testid="constraint-pointer"]')
    .nth(1)
    .fill("/empty/");
  await page
    .locator('[data-testid="constraint-type"]')
    .nth(1)
    .selectOption("null");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page.locator('[data-testid="constraint-pointer"]').nth(2).fill("/flag");
  await page
    .locator('[data-testid="constraint-type"]')
    .nth(2)
    .selectOption("boolean");
  await page.locator('[data-testid="constraint-value"]').nth(1).fill("true");
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page.locator('[data-testid="constraint-pointer"]').nth(3).fill("/name");
  await page
    .locator('[data-testid="constraint-type"]')
    .nth(3)
    .selectOption("string");
  await page.locator('[data-testid="constraint-value"]').nth(2).fill("literal");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page.locator('[data-testid="grant-detail"]').waitFor();
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "grant_reads_create_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), attempts, creates, destinations: 4 })}\n`,
  );
}

async function runGrantCorrection(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const zero = "00000000000000000000000000";
  const ids = Array.from(
    { length: 15 },
    (_, index) => `01ARZ3NDEKTSV4RRFFQ69G${String(index).padStart(4, "0")}`,
  );
  const principalIDs = ids.slice(0, 10);
  const grantIDs = ids.slice(5, 15);
  const grant = (
    id: string,
    principalID: string,
    effect: "allow" | "deny",
    target: string = serverID,
    state: "active" | "expired" = "active",
  ) => ({
    id,
    principal_id: principalID,
    effect,
    server_id: target,
    upstream_name: null,
    constraint: null,
    expires_at: state === "expired" ? "2026-08-28T12:30:00Z" : null,
    state,
    created_at: "2026-08-28T12:00:00Z",
  });
  const grants = new Map<string, ReturnType<typeof grant>>([
    [grantIDs[0]!, grant(grantIDs[0]!, principalIDs[0]!, "allow")],
    [grantIDs[1]!, grant(grantIDs[1]!, principalIDs[1]!, "deny")],
    [grantIDs[2]!, grant(grantIDs[2]!, principalIDs[2]!, "allow")],
    [grantIDs[3]!, grant(grantIDs[3]!, principalIDs[3]!, "allow")],
    [grantIDs[4]!, grant(grantIDs[4]!, principalIDs[4]!, "deny")],
    [
      grantIDs[5]!,
      grant(grantIDs[5]!, principalIDs[5]!, "allow", serverID, "expired"),
    ],
    [grantIDs[6]!, grant(grantIDs[6]!, principalIDs[6]!, "allow")],
    [grantIDs[7]!, grant(grantIDs[7]!, principalIDs[7]!, "allow", zero)],
    [grantIDs[8]!, grant(grantIDs[8]!, principalIDs[8]!, "allow", zero)],
    [grantIDs[9]!, grant(grantIDs[9]!, principalIDs[9]!, "allow", zero)],
  ]);
  const replacements = new Map<string, string>();
  let creates = 0;
  let deletes = 0;
  const detailRequests: string[] = [];

  await page.route("**/api/v1/principals/*", async (route) => {
    const principalID = new URL(route.request().url()).pathname
      .split("/")
      .pop()!;
    const index = principalIDs.indexOf(principalID);
    const visibility =
      index === 7 ? "allowed-only" : index === 8 ? "requestable" : "all";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${principalID}-1"` },
      body: JSON.stringify({
        id: principalID,
        display_name: `Principal ${index}`,
        state: "active",
        visibility,
        revision: "1",
        credential_revision: "0",
        credential: null,
        created_at: "2026-08-28T12:00:00Z",
        updated_at: "2026-08-28T12:00:00Z",
      }),
    });
  });
  await page.route("**/api/v1/grants?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [...grants.values()], next_cursor: null }),
    });
  });
  await page.route("**/api/v1/grants/*", async (route) => {
    const id = new URL(route.request().url()).pathname.split("/").pop()!;
    const request = route.request();
    if (request.method() === "GET") {
      detailRequests.push(id);
      const item = grants.get(id);
      if (item === undefined) {
        await route.fulfill({
          status: 404,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 404,
            code: "not_found",
            title: "Not found.",
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(item),
      });
      return;
    }
    if (request.method() !== "DELETE") fail("grant correction method changed");
    deletes += 1;
    const item = grants.get(id);
    if (item?.principal_id === principalIDs[4]) {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    grants.delete(id);
    await route.fulfill({ status: 204, body: "" });
  });
  await page.route(`${baseURL}/api/v1/grants`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    creates += 1;
    const body = JSON.parse(route.request().postData() ?? "null") as Record<
      string,
      unknown
    >;
    const principalID = body.principal_id as string;
    if (principalID === principalIDs[2]) {
      await route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 400,
          code: "invalid_grant",
          title: "The grant is invalid.",
        }),
      });
      return;
    }
    if (principalID === principalIDs[3]) {
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "conflict",
          title: "Policy changed concurrently.",
        }),
      });
      return;
    }
    const replacementID = `01ARZ3NDEKTSV4RRFFQ69H${String(creates).padStart(4, "0")}`;
    const item = grant(
      replacementID,
      principalID,
      body.effect as "allow" | "deny",
      body.server_id as string,
    );
    grants.set(replacementID, item);
    replacements.set(principalID, replacementID);
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify(item),
    });
  });

  const navigate = async (grantID: string) => {
    await page.evaluate((id) => {
      window.location.hash = `#/access/grants/${id}`;
    }, grantID);
    await page.locator('[data-testid="grant-actions"]').waitFor();
    try {
      await page
        .getByRole("heading", { name: `Grant ${grantID}`, exact: true })
        .waitFor({ timeout: 3000 });
    } catch {
      fail(
        `grant navigation failed for ${grantID}; reads=${detailRequests.join(",")}; body=${((await page.locator("body").textContent()) ?? "").slice(0, 1200)}`,
      );
    }
  };
  const confirmAction = async () => {
    await page.locator('[data-testid="grant-action-confirm-submit"]').click();
  };

  await page.evaluate((id) => {
    window.location.hash = `#/access/grants/${id}`;
  }, grantIDs[0]);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await navigate(grantIDs[0]!);

  await page.locator('[data-testid="grant-correct"]').click();
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText(/replacement now overlaps/).waitFor();
  if (Number(creates) !== 1 || Number(deletes) !== 0)
    fail("create-first auto-submitted deletion");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByRole("heading", { name: /^Grant .*H/ }).waitFor();
  if (Number(deletes) !== 1)
    fail("create-first deletion did not remain step two");

  await navigate(grantIDs[6]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText(/replacement now overlaps/).waitFor();
  const reloadDeletes = Number(deletes);
  await navigate(grantIDs[3]!);
  await navigate(grantIDs[6]!);
  if (
    (await page.locator('[data-testid="grant-correction"]').count()) !== 0 ||
    Number(deletes) !== reloadDeletes
  )
    fail("reload retained or submitted an unconfirmed correction step");

  await navigate(grantIDs[1]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page
    .locator('[data-testid="correction-order"]')
    .selectOption("delete_first");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText(/Authorization is absent/).waitFor();
  const beforeCreate = Number(creates);
  if (Number(deletes) !== reloadDeletes + 1)
    fail("delete-first step one was not isolated");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByRole("heading", { name: /^Grant .*H/ }).waitFor();
  if (Number(creates) !== beforeCreate + 1)
    fail("delete-first creation did not remain step two");

  await navigate(grantIDs[2]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page.getByText("The grant is invalid.", { exact: true }).waitFor();
  const rejectedDeletes = Number(deletes);
  if ((await page.getByText(/replacement now overlaps/).count()) !== 0)
    fail("rejected correction advanced to step two");

  await navigate(grantIDs[3]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page
    .getByText("Policy changed concurrently.", { exact: true })
    .waitFor();
  if (Number(deletes) !== rejectedDeletes)
    fail("stale correction submitted deletion");

  const defaultWarnings: Array<[string, string]> = [
    [grantIDs[7]!, "remove both discovery and call authorization"],
    [grantIDs[8]!, "requestable discovery still follows current DENY policy"],
    [grantIDs[9]!, "visibility never supplies call authorization"],
  ];
  for (const [grantID, phrase] of defaultWarnings) {
    await navigate(grantID);
    await page.getByText(new RegExp(phrase)).waitFor();
    if (
      (await page.locator('[data-testid="grant-correct"]').isDisabled()) !==
      true
    )
      fail("synthetic default grant exposed unsupported correction");
  }

  await navigate(grantIDs[5]!);
  await page
    .getByText("Expired grant still consumes capacity", { exact: true })
    .waitFor();
  await page.locator('[data-testid="grant-delete"]').click();
  await confirmAction();
  await page.locator('[data-testid="grants-view"]').waitFor();

  await navigate(grantIDs[4]!);
  await page.locator('[data-testid="grant-correct"]').click();
  await page
    .locator('[data-testid="correction-order"]')
    .selectOption("delete_first");
  await page.locator('[data-testid="grant-correction-step"]').click();
  await confirmAction();
  await page
    .getByText("Grant mutation outcome is unknown", { exact: true })
    .waitFor();
  if ((await page.getByText(/Authorization is absent/).count()) !== 0)
    fail("uncertain correction advanced to step two");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "grant_correction_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), creates, deletes, destinations: 10 })}\n`,
  );
}

async function runOverview(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  let invocationReads = 0;
  let serverMode: "complete" | "stale" | "partial" = "complete";
  let staleRestarted = false;
  let heldStatus = false;
  let releaseHeldStatus: (() => void) | undefined;

  await page.route("**/api/v1/system-status", async (route) => {
    if (
      route.request().method() !== "GET" ||
      new URL(route.request().url()).search !== ""
    )
      fail("Overview status request changed shape");
    const late = heldStatus;
    if (late) {
      await new Promise<void>((resolve) => {
        releaseHeldStatus = resolve;
      });
    }
    const status = overviewStatusFixture();
    if (late) status.process.state = "draining";
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(status),
    });
  });
  await page.route("**/api/v1/servers?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "100" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "cursor")
    )
      fail("Overview server request changed shape");
    const cursor = query.get("cursor");
    if (serverMode === "complete") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            overviewServer(
              "01ARZ3NDEKTSV4RRFFQ69G5FA0",
              `literal-<script>${"S".repeat(180)}`,
              "active",
            ),
            overviewServer(
              "01ARZ3NDEKTSV4RRFFQ69G5FA1",
              "Needs operator attention",
              "degraded",
            ),
            {
              ...overviewServer(
                "01ARZ3NDEKTSV4RRFFQ69G5FAB",
                "Deleted server history",
                "deleted",
              ),
              desired_state: "deleted",
              transport: null,
              runtime: {
                ...overviewServer(
                  "01ARZ3NDEKTSV4RRFFQ69G5FAB",
                  "Deleted server history",
                  "deleted",
                ).runtime,
                state: "deleted",
              },
              catalog: {
                ...overviewServer(
                  "01ARZ3NDEKTSV4RRFFQ69G5FAB",
                  "Deleted server history",
                  "deleted",
                ).catalog,
                durable_state: "retired",
                active_state: "absent",
                active_revision: null,
                active_tool_count: 0,
              },
              deleted_at: "2026-08-28T01:00:00Z",
            },
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    if (serverMode === "stale" && cursor === "stale") {
      staleRestarted = true;
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "stale_cursor",
          title: "The cursor is stale.",
        }),
      });
      return;
    }
    if (serverMode === "stale" && !staleRestarted) {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            overviewServer(
              "01ARZ3NDEKTSV4RRFFQ69G5FA2",
              "Discarded stale server",
              "active",
            ),
          ],
          next_cursor: "stale",
        }),
      });
      return;
    }
    if (serverMode === "partial" && cursor === "broken") {
      await route.fulfill({
        status: 503,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 503,
          code: "storage_unavailable",
          title: "Storage is unavailable.",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          overviewServer(
            "01ARZ3NDEKTSV4RRFFQ69G5FA1",
            "Needs operator attention",
            "degraded",
          ),
        ],
        next_cursor: serverMode === "partial" ? "broken" : null,
      }),
    });
  });
  await page.route("**/api/v1/grant-requests?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "100" ||
      query.get("state") !== "pending" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "state")
    )
      fail("Overview request queue read changed shape");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [overviewRequestFixture()],
        next_cursor: "more-pending",
      }),
    });
  });
  await page.route("**/api/v1/invocations?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "5" ||
      [...query.keys()].some((key) => key !== "limit")
    )
      fail("Overview invocation read changed shape");
    invocationReads += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [overviewInvocationFixture()],
        next_cursor: "older",
      }),
    });
  });

  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("2 configured"),
  );
  const body = (await page.locator("body").textContent()) ?? "";
  for (const text of [
    "Storage mutation is closed",
    "Keyring unavailable",
    "Capacity saturated",
    "80% capacity pressure",
    "Needs operator attention",
    "count incomplete",
    "Missing terminal evidence",
  ]) {
    if (!body.includes(text)) fail(`overview omitted ${text}`);
  }
  if (
    (await page.locator("script").count()) !== 1 ||
    (
      await page.locator('[data-testid="overview-invocations"]').textContent()
    )?.includes("redacted_arguments")
  )
    fail("overview rendered active or private content");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) !== "storage_latched"
  )
    fail("Overview did not close mutation admission for latched storage");
  for (const href of [
    "#/servers/01ARZ3NDEKTSV4RRFFQ69G5FA1",
    "#/requests/01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "#/invocations/01ARZ3NDEKTSV4RRFFQ69G5FAX",
  ]) {
    if ((await page.locator(`a[href="${href}"]`).count()) !== 1)
      fail(`overview omitted route ${href}`);
  }

  const beforePoll = invocationReads;
  await page.waitForTimeout(5100);
  if (invocationReads !== beforePoll + 1)
    fail("overview polling was not five-second bounded");
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  const hiddenReads = invocationReads;
  await page.waitForTimeout(5100);
  if (invocationReads !== hiddenReads) fail("overview polled while hidden");
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.waitForTimeout(5100);
  if (invocationReads !== hiddenReads + 1)
    fail("overview polling did not resume after visibility returned");

  serverMode = "stale";
  staleRestarted = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("1 configured"),
  );
  if (
    !staleRestarted ||
    ((await page.locator("body").textContent()) ?? "").includes(
      "Discarded stale server",
    )
  )
    fail("stale server traversal was not restarted cleanly");

  serverMode = "partial";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="overview-servers"]')
      ?.textContent?.includes("count incomplete"),
  );

  heldStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="overview-status"]')
        ?.getAttribute("data-panel-status") === "stale",
  );
  heldStatus = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  releaseHeldStatus?.();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="overview-status"]')
        ?.getAttribute("data-panel-status") === "current",
  );
  if (
    (
      (await page.locator('[data-testid="overview-status"]').textContent()) ??
      ""
    ).includes("draining")
  )
    fail("late Overview read replaced the current generation");

  if (
    await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    )
  )
    fail("overview long content overflowed the document");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "overview_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), invocation_reads: invocationReads })}\n`,
  );
}

const invocationIDs = {
  principal: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  server: "01ARZ3NDEKTSV4RRFFQ69G5FA0",
  credential: "01ARZ3NDEKTSV4RRFFQ69G5FAY",
  tool: "01ARZ3NDEKTSV4RRFFQ69G5FA3",
  grant: "01ARZ3NDEKTSV4RRFFQ69G5FA4",
  admission: "01ARZ3NDEKTSV4RRFFQ69G5FA5",
  policy: "01ARZ3NDEKTSV4RRFFQ69G5FA6",
  terminal: "01ARZ3NDEKTSV4RRFFQ69G5FA7",
  explicitUnknown: "01ARZ3NDEKTSV4RRFFQ69G5FA8",
  missing: "01ARZ3NDEKTSV4RRFFQ69G5FA9",
  stale: "01ARZ3NDEKTSV4RRFFQ69G5FAA",
} as const;

function invocationTarget(kind: "downstream" | "gateway") {
  return {
    kind,
    server_id:
      kind === "gateway" ? "00000000000000000000000000" : invocationIDs.server,
    tool_id: invocationIDs.tool,
    upstream_name: kind === "gateway" ? "get_identity" : "allowed",
    descriptor_revision: "7",
    descriptor_fingerprint: "d".repeat(64),
  };
}
function invocationAuthorization(decision: "allow" | "deny") {
  return {
    decision,
    revision: "9",
    evaluated_at: "2026-08-28T12:00:01Z",
    grant_id: decision === "allow" ? invocationIDs.grant : null,
  };
}
function invocationFixture(
  id: string,
  basis: "admission" | "policy" | "terminal" | "missing_terminal",
  outcome: string,
  kind: "downstream" | "gateway" | null = null,
) {
  const evaluated = basis !== "admission";
  return {
    id,
    principal_id: invocationIDs.principal,
    credential_id: invocationIDs.credential,
    credential_fingerprint: "0123456789abcdef",
    credential_revision: "3",
    admitted_at: "2026-08-28T12:00:00Z",
    admission_class: evaluated ? "evaluated" : "invalid_params",
    requested_name: evaluated
      ? kind === "gateway"
        ? "mcp_gateway.get_identity"
        : "namespace.allowed"
      : null,
    target: kind === null ? null : invocationTarget(kind),
    authorization: !evaluated
      ? null
      : invocationAuthorization(basis === "policy" ? "deny" : "allow"),
    outcome: {
      class: outcome,
      basis,
      completed_at: basis === "terminal" ? "2026-08-28T12:00:02Z" : null,
    },
  };
}

async function runInvocations(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
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

  const captureCanary = `INVOCATION_CAPTURE_<script>${"C".repeat(64)}`;
  let listReads = 0;
  let continuationReads = 0;
  let itemReads = 0;
  let staleMode = false;
  let staleRestarted = false;
  let itemMissing = false;
  await page.route("**/api/v1/invocations**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const headers = await request.allHeaders();
    if (
      request.method() !== "GET" ||
      request.postData() !== null ||
      headers["x-csrf-token"] === undefined
    )
      fail("invocation view issued an unauthenticated or non-read request");
    if (url.pathname !== "/api/v1/invocations") {
      if (
        url.pathname !== `/api/v1/invocations/${invocationIDs.missing}` ||
        url.search !== ""
      )
        fail("invocation item request changed shape");
      itemReads += 1;
      if (itemMissing) {
        await route.fulfill({
          status: 404,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 404,
            code: "not_found",
            title: "The resource was not found.",
          }),
        });
      } else {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify({
            ...invocationFixture(
              invocationIDs.missing,
              "missing_terminal",
              "outcome_unknown",
              "gateway",
            ),
            redacted_arguments: { note: captureCanary, token: "[REDACTED]" },
          }),
        });
      }
      return;
    }
    const query = url.searchParams;
    const allowed = new Set([
      "limit",
      "cursor",
      "principal_id",
      "server_id",
      "requested_name",
      "admission_class",
      "decision",
      "outcome",
    ]);
    if (
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => !allowed.has(key)) ||
      [...query.keys()].some((key) => query.getAll(key).length !== 1)
    )
      fail("invocation list request changed shape");
    const cursor = query.get("cursor");
    if (cursor !== null) continuationReads += 1;
    else listReads += 1;
    if (staleMode) {
      if (cursor === "stale-floor") {
        staleRestarted = true;
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 409,
            code: "stale_cursor",
            title: "The cursor snapshot is no longer available.",
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          staleRestarted
            ? {
                items: [
                  invocationFixture(
                    invocationIDs.missing,
                    "missing_terminal",
                    "outcome_unknown",
                    "gateway",
                  ),
                ],
                next_cursor: null,
              }
            : {
                items: [
                  invocationFixture(
                    invocationIDs.stale,
                    "terminal",
                    "succeeded",
                    "downstream",
                  ),
                ],
                next_cursor: "stale-floor",
              },
        ),
      });
      return;
    }
    if (query.has("requested_name") || query.has("outcome")) {
      if (
        query.get("principal_id") !== invocationIDs.principal ||
        query.get("server_id") !== invocationIDs.server ||
        query.get("admission_class") !== "evaluated" ||
        query.get("decision") !== "allow" ||
        query.get("outcome") !== "outcome_unknown" ||
        (query.has("requested_name") &&
          query.get("requested_name") !== "live-only.tool")
      )
        fail("invocation fragment or requested-name filters changed");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            invocationFixture(
              invocationIDs.missing,
              "missing_terminal",
              "outcome_unknown",
              "gateway",
            ),
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    if (cursor === "page-2") {
      await new Promise((resolve) => setTimeout(resolve, 80));
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            invocationFixture(
              invocationIDs.terminal,
              "terminal",
              "succeeded",
              "downstream",
            ),
            invocationFixture(
              invocationIDs.explicitUnknown,
              "terminal",
              "outcome_unknown",
              "downstream",
            ),
            invocationFixture(
              invocationIDs.missing,
              "missing_terminal",
              "outcome_unknown",
              "gateway",
            ),
          ],
          next_cursor: null,
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          invocationFixture(
            invocationIDs.admission,
            "admission",
            "invalid_params",
          ),
          invocationFixture(
            invocationIDs.policy,
            "policy",
            "deny",
            "downstream",
          ),
        ],
        next_cursor: "page-2",
      }),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/invocations";
  });
  await page.locator('[data-testid="invocations-view"]').waitFor();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="invocation-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "bounded recent window of at most 4,096 rows",
    "FIFO eviction has no age guarantee",
    "Filtered pages are independently coherent",
    "Recorded principal",
    "Recorded credential",
  ])
    if (!body.includes(phrase)) fail(`invocation list omitted ${phrase}`);
  if (body.includes(captureCanary) || body.includes("redacted_arguments"))
    fail("invocation collection exposed item capture");

  const beforePoll = listReads;
  await page.waitForTimeout(5100);
  if (listReads !== beforePoll + 1)
    fail("invocation list did not poll at five seconds");
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  const hiddenReads = listReads;
  await page.waitForTimeout(5100);
  if (listReads !== hiddenReads) fail("invocation list polled while hidden");
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.waitForTimeout(5100);
  if (listReads !== hiddenReads + 1)
    fail("invocation list polling did not resume");

  const beforeContinuation = continuationReads;
  await page.evaluate(() => {
    const button = document.querySelector<HTMLButtonElement>(
      '[data-testid="load-older-invocations"]',
    );
    button?.click();
    button?.click();
  });
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="invocation-row"]').length === 5,
  );
  if (continuationReads !== beforeContinuation + 1)
    fail("invocation continuation was duplicated");
  body = (await page.locator("body").textContent()) ?? "";
  for (const basis of ["admission", "policy", "terminal", "missing_terminal"])
    if (!body.includes(basis)) fail(`invocation list omitted ${basis} basis`);

  const fragment = `#/invocations?outcome=outcome_unknown&decision=allow&server_id=${invocationIDs.server}&principal_id=${invocationIDs.principal}&admission_class=evaluated`;
  await page.evaluate((value) => {
    window.location.hash = value;
  }, fragment);
  const canonical = `#/invocations?principal_id=${invocationIDs.principal}&server_id=${invocationIDs.server}&admission_class=evaluated&decision=allow&outcome=outcome_unknown`;
  await page.waitForFunction(
    (value) => window.location.hash === value,
    canonical,
  );
  await page
    .locator('[data-testid="requested-name-filter"]')
    .fill("live-only.tool");
  await page.locator('[data-testid="apply-requested-name"]').click();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="invocation-row"]').length === 1,
  );
  if ((await page.evaluate(() => window.location.hash)) !== canonical)
    fail("live requested-name filter entered the fragment");
  const storage = await browserStorage(page);
  if (JSON.stringify(storage).includes("live-only.tool"))
    fail("live requested-name filter entered browser storage");

  staleMode = true;
  staleRestarted = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(
    () =>
      document.querySelector('[data-testid="load-older-invocations"]') !== null,
  );
  await page.locator('[data-testid="load-older-invocations"]').click();
  await page.waitForFunction(
    (selector) => document.querySelector(selector) !== null,
    `a[href="#/invocations/${invocationIDs.missing}"]`,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!staleRestarted || body.includes(invocationIDs.stale))
    fail("stale invocation traversal was merged instead of restarted");

  await page
    .locator(`a[href="#/invocations/${invocationIDs.missing}"]`)
    .click();
  await page.locator('[data-testid="invocation-detail"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Gateway-owned local target",
    "not proof of downstream handoff",
    "does not automatically replay",
    "explicit caller retry can duplicate an effect",
    "Recorded target",
  ])
    if (!body.includes(phrase)) fail(`invocation detail omitted ${phrase}`);
  if (
    !body.includes(captureCanary) ||
    (await page.locator("script").count()) !== 1 ||
    (await page.evaluate(
      () =>
        (window as unknown as { __invocation_capture__?: boolean })
          .__invocation_capture__ === true,
    ))
  )
    fail("invocation capture was not inert item-only content");

  itemMissing = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.locator('[data-testid="invocation-missing"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes(captureCanary) ||
    !body.includes(
      "missing or evicted item does not prove it never existed or never executed",
    )
  )
    fail("evicted invocation guidance was unsafe");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "invocations_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), list_reads: listReads, continuation_reads: continuationReads, item_reads: itemReads })}\n`,
  );
}

async function runSystemStatus(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  let statusReads = 0;
  let eventStreams = 0;
  let holdStatus = false;
  let releaseStatus: (() => void) | undefined;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/v1/events"))
      eventStreams += 1;
  });

  await page.route("**/api/v1/system-status", async (route) => {
    if (
      route.request().method() !== "GET" ||
      new URL(route.request().url()).search !== ""
    )
      fail("System status request changed shape");
    statusReads += 1;
    if (holdStatus) {
      await new Promise<void>((resolve) => {
        releaseStatus = resolve;
      });
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(overviewStatusFixture()),
    });
  });
  await page.route(
    "**/api/v1/events",
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": reconnect fixture\n\n",
      }),
    { times: 1 },
  );

  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page
    .waitForFunction(
      () =>
        document
          .querySelector('[data-testid="gateway-shell"]')
          ?.getAttribute("data-freshness") === "reconnecting",
      undefined,
      { timeout: 5000 },
    )
    .catch(() => fail("System fixture did not enter reconnecting state"));
  await page.evaluate(() => {
    window.location.hash = "#/system";
  });
  await page.locator('[data-testid="system-view"]').waitFor();
  await page
    .waitForFunction(
      () =>
        document
          .querySelector('[data-testid="system-status-panel"]')
          ?.textContent?.includes("Schema 10") === true,
      undefined,
      { timeout: 5000 },
    )
    .catch(() => fail("System fixture did not publish initial status"));
  if (
    !((await page.locator("body").textContent()) ?? "").includes(
      "Data reconnecting",
    )
  )
    fail("System did not expose reconnecting freshness");
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current" &&
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );

  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Process storage_failed",
    "Started 2026-08-28T00:00:00Z",
    "Schema 10",
    "Revision 7",
    "Mutation admission closed",
    "Keyring unavailable",
    "OS-managed capability snapshot",
    "Backup idle",
    "Agent authentication principal_credentials",
    "modern 2026-07-28",
  ])
    if (!body.includes(phrase)) fail(`System status omitted ${phrase}`);
  const limitRows = await page
    .locator('[data-testid="system-limit-row"]')
    .count();
  if (limitRows !== overviewLimitNames.length)
    fail("System did not render every closed limit");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) !== "storage_latched"
  )
    fail("System did not close mutation admission for latched storage");

  holdStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "stale",
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Data stale") || !body.includes("Schema 10"))
    fail("System did not preserve and label stale status");
  holdStatus = false;
  releaseStatus?.();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=recovery";
  });
  await page.locator('[data-testid="system-recovery"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "mcp-gateway initialize --data-dir <owner-only-data-dir> --secret-output <new-owner-only-file>",
    "mcp-gateway admin-reset --data-dir <owner-only-data-dir> --secret-output <new-owner-only-file>",
    "mcp-gateway restore --verify-current --data-dir <owner-only-data-dir>",
    "mcp-gateway restore <backup-id> --data-dir <owner-only-data-dir> --secret-output <new-owner-only-file>",
    "Stop every Gateway process",
    "does not prove whether the triggering write committed or rolled back",
    "The browser never invokes these commands",
    "Normal serve startup must verify the selected generation",
  ])
    if (!body.includes(phrase)) fail(`System recovery omitted ${phrase}`);
  if (
    (await page.locator('[data-testid="system-recovery"] button').count()) !== 0
  )
    fail("System recovery exposed online offline-authority controls");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "system_status_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), status_reads: statusReads, event_streams: eventStreams, limit_rows: limitRows })}\n`,
  );
}

const serverReadIDs = {
  active: "01ARZ3NDEKTSV4RRFFQ69G5FB0",
  degraded: "01ARZ3NDEKTSV4RRFFQ69G5FB1",
  deleted: "01ARZ3NDEKTSV4RRFFQ69G5FB2",
  discarded: "01ARZ3NDEKTSV4RRFFQ69G5FB3",
  currentTool: "01ARZ3NDEKTSV4RRFFQ69G5FC0",
  retiredTool: "01ARZ3NDEKTSV4RRFFQ69G5FC1",
  durableTool: "01ARZ3NDEKTSV4RRFFQ69G5FC2",
  activeTool: "01ARZ3NDEKTSV4RRFFQ69G5FC3",
} as const;

function serverReadFixture(
  id: string,
  options: {
    name: string;
    desired: "enabled" | "disabled" | "deleted";
    runtime: "active" | "degraded" | "authentication_required" | "deleted";
    credential:
      | "ready"
      | "reauthentication_required"
      | "not_required"
      | "cleanup_pending";
    durable: "current" | "stale" | "retired";
    active: "current" | "stale" | "unavailable" | "absent";
  },
) {
  return {
    id,
    namespace: `server-${id.slice(-2).toLowerCase()}`,
    display_name: options.name,
    desired_state: options.desired,
    desired_revision: options.desired === "deleted" ? "8" : "7",
    transport:
      options.desired === "deleted"
        ? null
        : {
            kind: "stdio",
            executable: "/usr/bin/example",
            arguments: ["--safe"],
            working_directory: "/srv/example",
            environment: { MODE: "read" },
            secret_environment: { TOKEN: "primary" },
          },
    credential_revisions: {
      static_credential: "2",
      oauth_client: "3",
      oauth_tokens: "4",
    },
    credential_state: options.credential,
    runtime: {
      state: options.runtime,
      reason:
        options.runtime === "authentication_required"
          ? "authentication_rejected"
          : options.runtime === "degraded"
            ? "catalog_stale"
            : null,
      runtime_id: options.runtime === "active" ? "runtime-safe-id" : null,
      reconciliation: { in_use: 0, limit: 1, saturated: false },
      dispatch: { in_use: 0, limit: 4, saturated: false },
    },
    catalog: {
      durable_state: options.durable,
      active_state: options.active,
      durable_revision: options.durable === "retired" ? "6" : "7",
      active_revision: options.active === "current" ? "7" : null,
      durable_tool_count: 2,
      active_tool_count: options.active === "absent" ? 0 : 1,
      last_success_at: "2026-08-28T12:00:00Z",
      traversal: { in_use: 0, limit: 4, saturated: false },
    },
    created_at: "2026-08-28T10:00:00Z",
    updated_at: "2026-08-28T12:00:00Z",
    deleted_at: options.desired === "deleted" ? "2026-08-28T12:30:00Z" : null,
  };
}

function descriptorReadFixture(
  id: string,
  serverID: string,
  name: string,
  retired: boolean,
) {
  return {
    id,
    server_id: serverID,
    upstream_name: name,
    external_name: `server.${name}`,
    descriptor: {
      name,
      title: `Safe ${name}`,
      description: `Descriptor ${name}`,
      inputSchema: {
        type: "object",
        properties: { value: { type: "string" } },
      },
      outputSchema: { type: "object" },
      annotations: {
        title: null,
        readOnlyHint: true,
        destructiveHint: false,
        idempotentHint: true,
        openWorldHint: false,
      },
    },
    fingerprint: "a".repeat(64),
    catalog_revision: retired ? "6" : "7",
    first_seen_at: "2026-08-28T10:00:00Z",
    last_seen_at: "2026-08-28T12:00:00Z",
    retired_at: retired ? "2026-08-28T12:30:00Z" : null,
  };
}

async function runServerCreateUpdate(
  browserVersion: string,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");

  const serverID = serverReadIDs.active;
  let currentServer = {
    ...serverReadFixture(serverID, {
      name: "Created server",
      desired: "disabled",
      runtime: "active",
      credential: "ready",
      durable: "current",
      active: "current",
    }),
    namespace: "created-server",
    desired_revision: "1",
  };
  let creates = 0;
  let updates = 0;
  const createKeys: string[] = [];
  const createBodies: string[] = [];
  const etags: string[] = [];
  const mutationBody = (operation: unknown = null) => ({
    server: currentServer,
    operation,
  });
  const operation = () => ({
    id: "01ARZ3NDEKTSV4RRFFQ69G5FD0",
    server_id: serverID,
    kind: "activate",
    target_desired_revision: currentServer.desired_revision,
    target_credential_revisions: currentServer.credential_revisions,
    state: "scheduled",
    reason: null,
    created_at: "2026-08-28T13:00:00Z",
    started_at: null,
    finished_at: null,
  });
  await page.route(`${baseURL}/api/v1/servers`, async (route) => {
    if (route.request().method() !== "POST") {
      await route.fallback();
      return;
    }
    creates += 1;
    createKeys.push(
      (await route.request().allHeaders())["idempotency-key"] ?? "",
    );
    createBodies.push(route.request().postData() ?? "");
    if (creates === 1) {
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "namespace_unavailable",
          title: "Namespace unavailable",
        }),
      });
      return;
    }
    if (creates === 2) {
      await route.fulfill({ status: 502, body: "lost response" });
      return;
    }
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      headers: {
        ETag: `"server-${serverID}-${currentServer.desired_revision}"`,
      },
      body: JSON.stringify(mutationBody()),
    });
  });
  await page.route(`${baseURL}/api/v1/servers/${serverID}`, async (route) => {
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: {
          ETag: `"server-${serverID}-${currentServer.desired_revision}"`,
        },
        body: JSON.stringify(currentServer),
      });
      return;
    }
    if (route.request().method() !== "PATCH") {
      await route.fallback();
      return;
    }
    updates += 1;
    etags.push((await route.request().allHeaders())["if-match"] ?? "");
    const patch = JSON.parse(route.request().postData() ?? "null") as Record<
      string,
      unknown
    >;
    if (updates === 1) {
      currentServer = {
        ...currentServer,
        display_name: "Concurrent display",
        desired_revision: "2",
      };
      await route.fulfill({
        status: 428,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 428,
          code: "precondition_required",
          title: "Precondition required",
        }),
      });
      return;
    }
    if (updates === 2) {
      if (Object.keys(patch).join(",") !== "display_name")
        fail("display-only update included behavioral fields");
      currentServer = {
        ...currentServer,
        display_name: patch.display_name as string,
        desired_revision: "3",
      };
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"server-${serverID}-3"` },
        body: JSON.stringify(mutationBody()),
      });
      return;
    }
    if (updates === 3) {
      currentServer = { ...currentServer, desired_revision: "4" };
      await route.fulfill({
        status: 412,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 412,
          code: "stale_revision",
          title: "Stale server revision",
        }),
      });
      return;
    }
    if (
      patch.enabled !== true ||
      typeof patch.transport !== "object" ||
      patch.transport === null
    )
      fail("behavioral update omitted desired transport state");
    currentServer = {
      ...currentServer,
      display_name: patch.display_name as string,
      desired_state: "enabled",
      transport: patch.transport as typeof currentServer.transport,
      desired_revision: "5",
    };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"server-${serverID}-5"` },
      body: JSON.stringify(mutationBody(operation())),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/servers/new";
  });
  const editor = page.locator('[data-testid="server-editor"]');
  await editor.waitFor();
  const initialInputs = await editor.locator("input").evaluateAll((nodes) =>
    nodes.map((node) => ({
      name: node.getAttribute("name"),
      type: node.getAttribute("type"),
    })),
  );
  if (
    initialInputs.some(
      (input) => input.name !== null || input.type === "password",
    )
  )
    fail("server form exposed a reusable or secret input");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.getByText("Check server configuration").waitFor();
  if (creates !== 0) fail("invalid form submitted a create");

  await page.locator("#server-transport-kind").selectOption("streamable_http");
  await page.locator("#server-auth-mode").selectOption("oauth");
  await page.locator("#server-registration-mode").selectOption("static");
  if (
    (await editor
      .locator(
        'input[id*="secret"], textarea[id*="client-secret"], input[id*="bearer-token"]',
      )
      .count()) !== 0
  )
    fail("server form offered inline secret input");
  await page.locator("#server-transport-kind").selectOption("stdio");
  await page.locator("#server-namespace").fill("first-name");
  await page.locator("#server-display-name").fill("Created server");
  await page.locator("#server-executable").fill("/usr/bin/example");
  await page.locator("#server-arguments").fill('["--safe"]');
  await page.locator("#server-working-directory").fill("/srv/example");
  await page.locator("#server-environment").fill('{"MODE":"read"}');
  await page.locator("#server-secret-environment").fill('{"TOKEN":"primary"}');
  if (!(await editor.textContent())?.includes("not an OS sandbox"))
    fail("stdio OS-user warning omitted containment boundary");

  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-cancel"]').click();
  if (
    (await page.locator("#server-display-name").inputValue()) !==
    "Created server"
  )
    fail("confirmation cancellation discarded safe draft");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page.getByText("Namespace unavailable").waitFor();
  await page.locator("#server-namespace").fill("created-server");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page.getByText("Mutation outcome unknown").waitFor();
  await page.locator('[data-testid="server-create-replay"]').click();
  await page.locator('[data-testid="server-detail"]').waitFor();
  if (
    createKeys[0] === createKeys[1] ||
    createKeys[1] === "" ||
    createKeys[1] !== createKeys[2] ||
    createBodies[1] !== createBodies[2]
  )
    fail("create idempotency tuple was not intent-bound and replay-stable");
  if (
    createBodies.some(
      (body) =>
        body.includes("client_secret") ||
        body.includes("bearer_token") ||
        body.includes("mgw_admin_"),
    )
  )
    fail("create body contained inline secret material");

  await page.getByText("Edit desired server state").click();
  await page.locator("#server-display-name").fill("Display only draft");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.getByText("Precondition required").waitFor();
  await page.waitForFunction(() =>
    document.body.textContent?.includes("safe nonsecret draft is preserved"),
  );
  if (
    (await page.locator("#server-display-name").inputValue()) !==
    "Display only draft"
  )
    fail("428 refresh discarded safe draft");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.getByText("Desired server record saved.").waitFor();

  await page.locator("#server-enabled").selectOption("enabled");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page.getByText("Stale server revision").waitFor();
  if ((await page.locator("#server-enabled").inputValue()) !== "enabled")
    fail("412 refresh discarded behavioral draft");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page.getByText(/operation .* was scheduled/).waitFor();
  if (
    etags.join(",") !==
    `"server-${serverID}-1","server-${serverID}-2","server-${serverID}-3","server-${serverID}-4"`
  )
    fail(`updates did not use fresh ETags: ${etags.join(",")}`);
  assertClosedStorage(await browserStorage(page));
  if (((await page.locator("body").textContent()) ?? "").includes(bearer))
    fail("admin bearer reached server workflow DOM");

  process.stdout.write(
    `${JSON.stringify({
      event: "server_create_update_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      creates,
      updates,
    })}\n`,
  );
}

async function runServerOperations(
  browserVersion: string,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const serverID = serverReadIDs.active;
  const operationIDs = [
    "01ARZ3NDEKTSV4RRFFQ69G5FE0",
    "01ARZ3NDEKTSV4RRFFQ69G5FE1",
    "01ARZ3NDEKTSV4RRFFQ69G5FE2",
    "01ARZ3NDEKTSV4RRFFQ69G5FE3",
    "01ARZ3NDEKTSV4RRFFQ69G5FE4",
    "01ARZ3NDEKTSV4RRFFQ69G5FE5",
  ] as const;
  let currentServer = {
    ...serverReadFixture(serverID, {
      name: "Operation server",
      desired: "enabled",
      runtime: "active",
      credential: "ready",
      durable: "current",
      active: "current",
    }),
    desired_revision: "1",
  };
  const operation = (
    id: string,
    kind: string,
    state: string,
    reason: string | null = null,
  ) => ({
    id,
    server_id: serverID,
    kind,
    target_desired_revision: currentServer.desired_revision,
    target_credential_revisions: currentServer.credential_revisions,
    state,
    reason,
    created_at: "2026-08-28T14:00:00Z",
    started_at: state === "scheduled" ? null : "2026-08-28T14:00:01Z",
    finished_at:
      state === "scheduled" || state === "running"
        ? null
        : "2026-08-28T14:00:02Z",
  });
  let operationReads = 0;
  let listReads = 0;
  let listBlocked = false;
  let detailPollReads = 0;
  let starts = 0;
  const startKeys: string[] = [];
  const startBodies: string[] = [];

  await page.route(
    `${baseURL}/api/v1/events`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `event: invalidate\ndata: ${JSON.stringify({ kind: "server_operations", resource_id: operationIDs[0] })}\n\n`,
      }),
    { times: 1 },
  );
  await page.route(`${baseURL}/api/v1/servers/${serverID}`, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: {
        ETag: `"server-${serverID}-${currentServer.desired_revision}"`,
      },
      body: JSON.stringify(currentServer),
    });
  });
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations?*`,
    async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }
      operationReads += 1;
      listReads += 1;
      if (new URL(route.request().url()).search !== "?limit=50")
        fail("operation list query changed");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: listBlocked
            ? [operation(operationIDs[4], "reload", "scheduled")]
            : [
                operation(operationIDs[4], "disable", "succeeded"),
                operation(operationIDs[5], "reload", "failed", "connectivity"),
              ],
          next_cursor: null,
        }),
      });
    },
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations/*`,
    async (route) => {
      operationReads += 1;
      const id = route.request().url().slice(-26);
      let item = operation(id, "refresh_catalog", "succeeded");
      if (id === operationIDs[0]) {
        detailPollReads += 1;
        item = operation(
          id,
          "refresh_catalog",
          detailPollReads <= 2
            ? "scheduled"
            : detailPollReads === 3
              ? "running"
              : "succeeded",
        );
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(item),
      });
    },
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations`,
    async (route) => {
      if (route.request().method() !== "POST") {
        await route.fallback();
        return;
      }
      starts += 1;
      const headers = await route.request().allHeaders();
      startKeys.push(headers["idempotency-key"] ?? "");
      startBodies.push(route.request().postData() ?? "");
      const input = JSON.parse(route.request().postData() ?? "null") as {
        kind?: string;
      };
      const expectedETag = `"server-${serverID}-${currentServer.desired_revision}"`;
      if (headers["if-match"] !== expectedETag)
        fail(
          `operation start used stale ETag ${headers["if-match"] ?? "none"}`,
        );
      if (starts === 1) {
        if (input.kind !== "refresh_catalog")
          fail("catalog refresh kind changed");
        await route.fulfill({ status: 502, body: "lost response" });
        return;
      }
      if (starts === 3) {
        if (input.kind !== "reload") fail("reload kind changed");
        currentServer = { ...currentServer, desired_revision: "2" };
        await route.fulfill({
          status: 412,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 412,
            code: "stale_revision",
            title: "Stale server revision",
          }),
        });
        return;
      }
      const id = operationIDs[Math.min(starts - 2, operationIDs.length - 1)]!;
      await route.fulfill({
        status: starts === 2 ? 200 : 202,
        contentType: "application/json",
        body: JSON.stringify({
          operation: operation(id, input.kind ?? "reload", "scheduled"),
        }),
      });
    },
  );

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=operations`;
  }, serverID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="operation-list"]').waitFor();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="operation-row"]').length === 2,
  );
  await page.waitForFunction(() =>
    document.body.textContent?.includes("Start an eligible operation"),
  );
  await page.waitForTimeout(350);
  if (listReads < 2)
    fail("server_operations event did not trigger a snapshot read");
  const eventRefreshes = listReads - 1;

  await page.locator('[data-testid="start-operation-refresh_catalog"]').click();
  if (
    await page
      .locator('[data-testid="operation-start-confirm-submit"]')
      .isVisible()
  )
    fail("catalog refresh inherited a misleading confirmation");
  await page.getByText("Operation start outcome unknown").waitFor();
  await page.locator('[data-testid="operation-start-replay"]').click();
  await page.locator('[data-testid="operation-detail"]').waitFor();
  if (
    startKeys[0] === "" ||
    startKeys[0] !== startKeys[1] ||
    startBodies[0] !== startBodies[1]
  )
    fail("uncertain operation replay changed its exact tuple");

  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-freshness") === "current",
  );
  await page.waitForTimeout(100);
  const beforePoll = detailPollReads;
  await page.waitForTimeout(2100);
  if (detailPollReads !== beforePoll + 1)
    fail(
      `nonterminal operation did not poll at two seconds (${beforePoll} -> ${detailPollReads})`,
    );
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "hidden",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  const hiddenReads = detailPollReads;
  await page.waitForTimeout(2100);
  if (detailPollReads !== hiddenReads)
    fail("operation detail polled while hidden");
  await page.evaluate(() => {
    Object.defineProperty(document, "visibilityState", {
      configurable: true,
      get: () => "visible",
    });
    document.dispatchEvent(new Event("visibilitychange"));
  });
  await page.waitForTimeout(2100);
  if (detailPollReads !== hiddenReads + 1)
    fail("operation polling did not resume to terminal state");
  const terminalReads = detailPollReads;
  await page.waitForTimeout(2100);
  if (detailPollReads !== terminalReads)
    fail("terminal operation continued polling");
  const detailText =
    (await page.locator('[data-testid="operation-detail"]').textContent()) ??
    "";
  if (
    !detailText.includes("Operation history") ||
    !detailText.includes("Server record")
  )
    fail("operation detail omitted terminal navigation links");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=operations`;
  }, serverID);
  await page.locator('[data-testid="operation-list"]').waitFor();
  await page.locator('[data-testid="start-operation-reload"]').click();
  await page.locator('[data-testid="operation-start-confirm-cancel"]').click();
  if (starts !== 2) fail("cancelled reload submitted work");
  await page.locator('[data-testid="start-operation-reload"]').click();
  await page.locator('[data-testid="operation-start-confirm-submit"]').click();
  await page.getByText("Stale server revision").waitFor();
  await page.waitForFunction(() =>
    document.body.textContent?.includes(
      "selected operation before starting a new intent",
    ),
  );
  await page.locator('[data-testid="start-operation-reload"]').click();
  await page.locator('[data-testid="operation-start-confirm-submit"]').click();
  await page.locator('[data-testid="operation-detail"]').waitFor();

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=operations`;
  }, serverID);
  await page
    .locator('[data-testid="start-operation-disconnect_credentials"]')
    .click();
  const disconnectText =
    (await page
      .locator("#operation-start-confirm-consequence")
      .textContent()) ?? "";
  if (!disconnectText.includes("not guaranteed"))
    fail("disconnect confirmation claimed remote revocation");
  await page.locator('[data-testid="operation-start-confirm-submit"]').click();
  await page.locator('[data-testid="operation-detail"]').waitFor();

  currentServer = {
    ...currentServer,
    runtime: {
      ...currentServer.runtime,
      state: "degraded",
      reason: "connectivity",
    },
  };
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=operations`;
  }, serverID);
  await page.locator('[data-testid="operation-list"]').waitFor();
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.locator('[data-testid="start-operation-retry"]').waitFor();
  await page.locator('[data-testid="start-operation-retry"]').click();
  if (
    await page
      .locator('[data-testid="operation-start-confirm-submit"]')
      .isVisible()
  )
    fail("eligible retry inherited a misleading confirmation");
  await page.locator('[data-testid="operation-detail"]').waitFor();

  listBlocked = true;
  currentServer = {
    ...currentServer,
    runtime: { ...currentServer.runtime, state: "active", reason: null },
  };
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=operations`;
  }, serverID);
  await page.getByText("No explicit operation is currently eligible").waitFor();
  if ((await page.locator('[data-testid^="start-operation-"]').count()) !== 0)
    fail("operation controls were offered during conflicting work");

  assertClosedStorage(await browserStorage(page));
  process.stdout.write(
    `${JSON.stringify({
      event: "server_operations_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      operation_reads: operationReads,
      starts,
      event_refreshes: eventRefreshes,
    })}\n`,
  );
}

async function runServerDisconnectDelete(
  browserVersion: string,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const serverID = serverReadIDs.active;
  const disconnectID = "01ARZ3NDEKTSV4RRFFQ69G5FEA";
  const deleteID = "01ARZ3NDEKTSV4RRFFQ69G5FEB";
  let currentServer = serverReadFixture(serverID, {
    name: "Destructive workflow server",
    desired: "enabled",
    runtime: "active",
    credential: "ready",
    durable: "current",
    active: "current",
  });
  let disconnects = 0;
  let deletes = 0;
  const operation = (id: string, kind: string) => ({
    id,
    server_id: serverID,
    kind,
    target_desired_revision: currentServer.desired_revision,
    target_credential_revisions: currentServer.credential_revisions,
    state: "scheduled",
    reason: null,
    created_at: "2026-08-28T16:00:00Z",
    started_at: null,
    finished_at: null,
  });

  await page.route(`${baseURL}/api/v1/servers/${serverID}`, async (route) => {
    const request = route.request();
    if (request.method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: {
          ETag: `"server-${serverID}-${currentServer.desired_revision}"`,
        },
        body: JSON.stringify(currentServer),
      });
      return;
    }
    if (request.method() !== "DELETE") return route.fallback();
    deletes += 1;
    const headers = await request.allHeaders();
    if ((request.postData() ?? "") !== "{}")
      fail("server deletion body changed");
    if ((headers["idempotency-key"] ?? "") !== "")
      fail("server deletion gained idempotency authority");
    const expected = `"server-${serverID}-${currentServer.desired_revision}"`;
    if (headers["if-match"] !== expected)
      fail(`server deletion used stale ETag ${headers["if-match"] ?? "none"}`);
    if (deletes === 1) {
      currentServer = { ...currentServer, desired_revision: "9" };
      await route.fulfill({
        status: 412,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 412,
          code: "stale_revision",
          title: "Stale server revision",
        }),
      });
      return;
    }
    currentServer = {
      ...serverReadFixture(serverID, {
        name: "Destructive workflow server",
        desired: "deleted",
        runtime: "deleted",
        credential: "reauthentication_required",
        durable: "retired",
        active: "absent",
      }),
      desired_revision: "10",
      credential_state: "cleanup_pending",
      deleted_at: "2026-08-28T16:05:00Z",
    };
    await route.fulfill({
      status: 202,
      contentType: "application/json",
      headers: { ETag: `"server-${serverID}-10"` },
      body: JSON.stringify({
        server: currentServer,
        operation: operation(deleteID, "delete"),
      }),
    });
  });
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations`,
    async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      disconnects += 1;
      const headers = await route.request().allHeaders();
      if (
        (route.request().postData() ?? "") !==
        '{"kind":"disconnect_credentials"}'
      )
        fail("disconnect body changed");
      if ((headers["idempotency-key"] ?? "") === "")
        fail("disconnect omitted operation idempotency");
      const expected = `"server-${serverID}-${currentServer.desired_revision}"`;
      if (headers["if-match"] !== expected)
        fail(`disconnect used stale ETag ${headers["if-match"] ?? "none"}`);
      if (disconnects === 1) {
        currentServer = { ...currentServer, desired_revision: "8" };
        await route.fulfill({
          status: 412,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 412,
            code: "stale_revision",
            title: "Stale server revision",
          }),
        });
        return;
      }
      currentServer = {
        ...currentServer,
        credential_state: "cleanup_pending",
        runtime: {
          ...currentServer.runtime,
          state: "degraded",
          reason: "cleanup_pending",
        },
      };
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          operation: operation(disconnectID, "disconnect_credentials"),
        }),
      });
    },
  );
  for (const [id, kind] of [
    [disconnectID, "disconnect_credentials"],
    [deleteID, "delete"],
  ] as const) {
    await page.route(
      `${baseURL}/api/v1/servers/${serverID}/operations/${id}`,
      async (route) =>
        route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(operation(id, kind)),
        }),
    );
  }

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}`;
  }, serverID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="server-destructive-actions"]').waitFor();

  const disconnect = page.locator(
    '[data-testid="disconnect-server-credentials"]',
  );
  await disconnect.click();
  const disconnectText =
    (await page
      .locator("#server-disconnect-confirm-consequence")
      .textContent()) ?? "";
  if (
    !disconnectText.includes("not guaranteed") ||
    !disconnectText.includes("cleanup may remain pending")
  )
    fail("disconnect consequence overstated cleanup");
  await page
    .locator('[data-testid="server-disconnect-confirm-cancel"]')
    .click();
  if (disconnects !== 0) fail("cancelled disconnect submitted");
  await disconnect.click();
  await page
    .locator('[data-testid="server-disconnect-confirm-submit"]')
    .click();
  await page.getByText("Stale server revision").waitFor();
  await page.waitForFunction(() =>
    document.body.textContent?.includes("Desired revision 8"),
  );
  if (Number(disconnects) !== 1)
    fail("stale disconnect replayed automatically");
  await disconnect.click();
  await page
    .locator('[data-testid="server-disconnect-confirm-submit"]')
    .click();
  await page.locator('[data-testid="operation-detail"]').waitFor();
  if (Number(disconnects) !== 2) fail("confirmed disconnect count changed");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}`;
  }, serverID);
  await page.locator('[data-testid="server-destructive-actions"]').waitFor();
  const destructiveText =
    (await page
      .locator('[data-testid="server-destructive-actions"]')
      .textContent()) ?? "";
  if (
    !destructiveText.includes("local-only") ||
    !destructiveText.includes("does not replay remote revocation") ||
    !destructiveText.includes("restore credential authority")
  )
    fail("cleanup-pending guidance implied restoration or revocation replay");

  const deleteButton = page.locator('[data-testid="delete-server"]');
  await deleteButton.click();
  const typed = page.locator('[data-testid="server-delete-confirm-value"]');
  const deleteSubmit = page.locator(
    '[data-testid="server-delete-confirm-submit"]',
  );
  await typed.fill("wrong-namespace");
  if (!(await deleteSubmit.isDisabled()))
    fail("namespace mismatch enabled permanent deletion");
  await page.locator('[data-testid="server-delete-confirm-cancel"]').click();
  if (deletes !== 0) fail("cancelled deletion submitted");
  await deleteButton.click();
  await typed.fill(currentServer.namespace);
  await deleteSubmit.click();
  await page.getByText("Stale server revision").waitFor();
  await page.waitForFunction(() =>
    document.body.textContent?.includes("Desired revision 9"),
  );
  if (Number(deletes) !== 1) fail("stale deletion replayed automatically");
  await deleteButton.click();
  if ((await typed.inputValue()) !== "")
    fail("typed namespace survived authoritative conflict");
  await typed.fill(currentServer.namespace);
  await deleteSubmit.click();
  await page.locator('[data-testid="operation-detail"]').waitFor();
  if (Number(deletes) !== 2) fail("confirmed deletion count changed");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}`;
  }, serverID);
  await page.locator('[data-testid="server-detail"]').waitFor();
  await page.waitForFunction(() =>
    document.body.textContent?.includes("Desired revision 10"),
  );
  const tombstone =
    (await page.locator('[data-testid="server-detail"]').textContent()) ?? "";
  if (
    !tombstone.includes("deleted") ||
    !tombstone.includes("2026-08-28T16:05:00Z") ||
    !tombstone.includes("Active catalog absent") ||
    !tombstone.includes("Durable evidence is not proof")
  )
    fail("server tombstone omitted permanent historical state");
  if (
    (await page
      .locator('[data-testid="server-destructive-actions"]')
      .count()) !== 0 ||
    (await page.locator('[data-testid="server-editor"]').count()) !== 0
  )
    fail("tombstone retained mutation controls");
  if (/force|restore authority|re-enable/i.test(tombstone))
    fail("tombstone offered force or authority restoration");

  assertClosedStorage(await browserStorage(page));
  process.stdout.write(
    `${JSON.stringify({
      event: "server_disconnect_delete_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      disconnects,
      deletes,
    })}\n`,
  );
}

async function runAuthFlows(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const serverID = serverReadIDs.active;
  const activeID = "01ARZ3NDEKTSV4RRFFQ69G5FE6";
  const terminalID = "01ARZ3NDEKTSV4RRFFQ69G5FE7";
  const createdID = "01ARZ3NDEKTSV4RRFFQ69G5FE8";
  const exchangingID = "01ARZ3NDEKTSV4RRFFQ69G5FE9";
  const authorizationURL =
    "https://issuer.example/authorize?client_id=safe&state=one-time-state&code_challenge=pkce";
  const server = {
    ...serverReadFixture(serverID, {
      name: "OAuth server",
      desired: "enabled",
      runtime: "authentication_required",
      credential: "reauthentication_required",
      durable: "current",
      active: "unavailable",
    }),
    desired_revision: "7",
    transport: {
      kind: "streamable_http",
      url: "https://resource.example/mcp",
      protocol_mode: "modern",
      authentication: {
        mode: "oauth",
        registration: { mode: "dynamic", issuer: null },
        trusted_origins: [],
        request_offline_access: false,
      },
    },
  };
  let detailState = "preparing";
  let detailReads = 0;
  let listReads = 0;
  let starts = 0;
  let cancels = 0;
  let releaseThirdStart: (() => void) | undefined;
  const thirdStartBarrier = new Promise<void>((resolve) => {
    releaseThirdStart = resolve;
  });
  const flow = (id: string, state: string, reason: string | null = null) => ({
    id,
    server_id: serverID,
    flow_state: state,
    target_desired_revision: "7",
    registration_revision: "3",
    created_at: "2026-08-28T15:00:00Z",
    expires_at: "2026-08-28T15:05:00Z",
    finished_at:
      state === "preparing" ||
      state === "awaiting_callback" ||
      state === "exchanging"
        ? null
        : "2026-08-28T15:01:00Z",
    reason,
  });

  await context.route("https://issuer.example/**", async (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/html",
      body: "authorized",
    }),
  );
  await page.route(
    `${baseURL}/api/v1/events`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `event: invalidate\ndata: ${JSON.stringify({ kind: "server_auth_flows", resource_id: activeID })}\n\n`,
      }),
    { times: 1 },
  );
  await page.route(`${baseURL}/api/v1/servers/${serverID}`, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"server-${serverID}-7"` },
      body: JSON.stringify(server),
    });
  });
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/auth-flows?*`,
    async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      listReads += 1;
      if (new URL(route.request().url()).search !== "?limit=50")
        fail("auth flow list query changed");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [
            flow(activeID, "preparing"),
            flow(terminalID, "failed", "oauth_rejected"),
          ],
          next_cursor: null,
        }),
      });
    },
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/auth-flows/${activeID}`,
    async (route) => {
      if (route.request().method() === "DELETE") {
        cancels += 1;
        if ((route.request().postData() ?? "") !== "{}")
          fail("auth flow cancellation body changed");
        if (cancels === 1) {
          await route.fulfill({
            status: 409,
            contentType: "application/problem+json",
            body: JSON.stringify({
              status: 409,
              code: "oauth_flow_active",
              title: "OAuth exchange active",
            }),
          });
          return;
        }
        detailState = "cancelled";
        await route.fulfill({ status: 204, body: "" });
        return;
      }
      detailReads += 1;
      if (detailReads >= 2 && detailState === "preparing")
        detailState = "awaiting_callback";
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(flow(activeID, detailState)),
      });
    },
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/auth-flows/${exchangingID}`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(flow(exchangingID, "exchanging")),
      }),
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/auth-flows/${terminalID}`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(flow(terminalID, "failed", "oauth_rejected")),
      }),
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/auth-flows`,
    async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      starts += 1;
      const headers = await route.request().allHeaders();
      if (headers["if-match"] !== `"server-${serverID}-7"`)
        fail("auth flow start omitted current ETag");
      if ((headers["idempotency-key"] ?? "") !== "")
        fail("auth flow start added idempotency authority");
      if ((route.request().postData() ?? "") !== "{}")
        fail("auth flow start body changed");
      if (starts === 3) await thirdStartBarrier;
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          flow: flow(createdID, "awaiting_callback"),
          authorization_url: authorizationURL,
        }),
      });
    },
  );

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=oauth`;
  }, serverID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="auth-flow-list"]').waitFor();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="auth-flow-row"]').length === 2,
  );
  await page.waitForTimeout(350);
  if (listReads < 2) fail("auth-flow event did not trigger snapshot reread");

  await page.locator('[data-testid="start-auth-flow"]').click();
  const display = page.locator('[data-testid="one-time-oauth-url"]');
  await display.waitFor();
  if ((await display.locator("a").count()) !== 0)
    fail("authorization URL became active content");
  if ((await display.textContent()) !== authorizationURL)
    fail("authorization URL display changed");
  const popupPromise = page.waitForEvent("popup");
  await page.locator('[data-testid="open-oauth-url"]').click();
  const popup = await popupPromise;
  await popup.waitForLoadState("domcontentloaded");
  if ((await popup.evaluate(() => window.opener === null)) !== true)
    fail("OAuth popup retained an opener");
  await popup.close();
  if (await display.isVisible()) {
    await page.getByText("The browser blocked the new page.").waitFor();
    await page.getByRole("button", { name: "Dismiss and clear" }).click();
  }
  await display.waitFor({ state: "hidden" });

  await page.locator('[data-testid="start-auth-flow"]').click();
  await display.waitFor();
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await display.waitFor({ state: "hidden" });
  if (starts !== 2) fail("auth flow start count changed");

  await page.locator('[data-testid="start-auth-flow"]').click();
  const awaitingDialog = page.locator("dialog.sensitive-dialog[open]");
  await awaitingDialog.waitFor();
  await awaitingDialog.evaluate((dialog) =>
    (dialog as HTMLDialogElement).close(),
  );
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await waitForLifecycle(page, "signed_out");
  releaseThirdStart?.();
  await page.waitForTimeout(100);
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}/auth-flows/${"01ARZ3NDEKTSV4RRFFQ69G5FE6"}`;
  }, serverID);
  await page.locator('[data-testid="auth-flow-detail"]').waitFor();
  const beforePoll = detailReads;
  await page.waitForTimeout(2100);
  if (detailReads < beforePoll + 1)
    fail("nonterminal auth flow did not poll at two seconds");
  await page.locator('[data-testid="cancel-auth-flow"]').click();
  await page.locator('[data-testid="auth-flow-cancel-confirm-cancel"]').click();
  if (cancels !== 0) fail("dismissed cancellation submitted");
  await page.locator('[data-testid="cancel-auth-flow"]').click();
  const consequence =
    (await page
      .locator("#auth-flow-cancel-confirm-consequence")
      .textContent()) ?? "";
  if (!consequence.includes("cannot be cancelled"))
    fail("cancellation consequence omitted exchange race");
  await page.locator('[data-testid="auth-flow-cancel-confirm-submit"]').click();
  await page.getByText("OAuth exchange active").waitFor();
  await page.locator('[data-testid="cancel-auth-flow"]').click();
  await page.locator('[data-testid="auth-flow-cancel-confirm-submit"]').click();
  await page.waitForFunction(() =>
    document.body.textContent?.includes("cancelled"),
  );
  if (Number(cancels) !== 2) fail("confirmed cancellation count changed");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}/auth-flows/${"01ARZ3NDEKTSV4RRFFQ69G5FE9"}`;
  }, serverID);
  await page.locator('[data-testid="auth-flow-detail"]').waitFor();
  await page.getByText("An OAuth exchange is already active.").waitFor();
  if (
    (await page.locator('[data-testid="cancel-auth-flow"]').count()) !== 0 ||
    (await page.locator('[data-testid="start-auth-flow"]').count()) !== 0
  )
    fail("exchanging auth flow offered a mutation");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}/auth-flows/${"01ARZ3NDEKTSV4RRFFQ69G5FE7"}`;
  }, serverID);
  await page.locator('[data-testid="auth-flow-detail"]').waitFor();
  if ((await page.locator('[data-testid="cancel-auth-flow"]').count()) !== 0)
    fail("terminal auth flow offered cancellation");
  const finalDOM = (await page.locator("body").textContent()) ?? "";
  if (finalDOM.includes(bearer)) fail("bearer leaked into auth flow DOM");
  if (finalDOM.includes(authorizationURL))
    fail("authorization URL remained outside its one-time sink");
  assertClosedStorage(await browserStorage(page));
  process.stdout.write(
    `${JSON.stringify({
      event: "auth_flows_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      list_reads: listReads,
      detail_reads: detailReads,
      starts,
      cancels,
    })}\n`,
  );
}

async function runServerCredentials(
  browserVersion: string,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const serverID = serverReadIDs.active;
  const operationID = "01ARZ3NDEKTSV4RRFFQ69G5FF0";
  const firstCanary = "credential-canary-first-7Yp3";
  const secondCanary = "credential-canary-second-8Zq4";
  const thirdCanary = "credential-canary-third-9Ar5";
  let currentServer = {
    ...serverReadFixture(serverID, {
      name: "Credential server",
      desired: "enabled",
      runtime: "active",
      credential: "ready",
      durable: "current",
      active: "current",
    }),
    desired_revision: "1",
    credential_revisions: {
      static_credential: "1",
      oauth_client: "4",
      oauth_tokens: "5",
    },
  };
  const stdioTransport = currentServer.transport;
  const bearerTransport = {
    kind: "streamable_http",
    url: "https://mcp.example.test/",
    protocol_mode: "modern",
    authentication: { mode: "bearer" },
  };
  const oauthStaticTransport = {
    kind: "streamable_http",
    url: "https://mcp.example.test/",
    protocol_mode: "modern",
    authentication: {
      mode: "oauth",
      registration: {
        mode: "static",
        issuer: "https://issuer.example.test",
        client_id: "safe-client",
        token_endpoint_auth_method: "client_secret_basic",
      },
      trusted_origins: ["https://issuer.example.test"],
      request_offline_access: false,
    },
  };
  const oauthDynamicTransport = {
    ...oauthStaticTransport,
    authentication: {
      ...oauthStaticTransport.authentication,
      registration: {
        mode: "dynamic",
        issuer: "https://issuer.example.test",
      },
    },
  };
  let serverReads = 0;
  let replacements = 0;
  let readsBeforeRecovery = 0;
  const replacementOperation = () => ({
    id: operationID,
    server_id: serverID,
    kind: "credential_replace",
    target_desired_revision: currentServer.desired_revision,
    target_credential_revisions: currentServer.credential_revisions,
    state: "scheduled",
    reason: null,
    created_at: "2026-08-28T15:00:00Z",
    started_at: null,
    finished_at: null,
  });
  await page.route(`${baseURL}/api/v1/servers/${serverID}`, async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    serverReads += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"server-${serverID}-1"` },
      body: JSON.stringify(currentServer),
    });
  });
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/credential-replacements`,
    async (route) => {
      replacements += 1;
      const headers = await route.request().allHeaders();
      if (headers["if-match"] !== `"server-${serverID}-1"`)
        fail("credential replacement omitted current server ETag");
      if (headers["idempotency-key"] !== undefined)
        fail("credential replacement gained idempotency/replay authority");
      const body = JSON.parse(route.request().postData() ?? "null") as {
        kind?: string;
        expected_revision?: string;
        values?: Record<string, string>;
      };
      const expectedRevision =
        replacements === 1 ? "1" : replacements === 2 ? "2" : "3";
      const expectedCanary =
        replacements === 1
          ? firstCanary
          : replacements === 2
            ? secondCanary
            : thirdCanary;
      if (
        body.kind !== "static_credential" ||
        body.expected_revision !== expectedRevision ||
        Object.keys(body.values ?? {}).join(",") !== "primary" ||
        body.values?.primary !== expectedCanary
      )
        fail("credential replacement body changed closed write-only shape");
      if (replacements === 1) {
        currentServer = {
          ...currentServer,
          credential_revisions: {
            ...currentServer.credential_revisions,
            static_credential: "2",
          },
        };
        await route.fulfill({
          status: 412,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 412,
            code: "stale_revision",
            title: "Stale credential revision",
          }),
        });
        return;
      }
      if (replacements === 2) {
        currentServer = {
          ...currentServer,
          credential_revisions: {
            ...currentServer.credential_revisions,
            static_credential: "3",
          },
        };
        readsBeforeRecovery = serverReads;
        await route.fulfill({
          status: 503,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 503,
            code: "keyring_unavailable",
            title: "Keyring outcome unavailable",
          }),
        });
        return;
      }
      currentServer = {
        ...currentServer,
        credential_revisions: {
          ...currentServer.credential_revisions,
          static_credential: "4",
        },
      };
      await route.fulfill({
        status: 202,
        contentType: "application/json",
        body: JSON.stringify({
          server_id: serverID,
          kind: "static_credential",
          credential_revision: "4",
          operation: replacementOperation(),
        }),
      });
    },
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations/${operationID}`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(replacementOperation()),
      }),
  );

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=credentials`;
  }, serverID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="server-credentials"]').waitFor();
  const assertEligible = async (fieldID: string) => {
    await page.locator(`#${fieldID}`).waitFor();
    const field = page.locator(`#${fieldID}`);
    if (
      (await field.getAttribute("type")) !== "password" ||
      (await field.getAttribute("name")) !== null ||
      (await field.getAttribute("value")) !== null ||
      (await field.inputValue()) !== ""
    )
      fail(`credential field ${fieldID} was not blank and write-only`);
  };
  await assertEligible("credential-slot-primary");
  let eligibilityModes = 1;

  currentServer = {
    ...currentServer,
    transport: bearerTransport as unknown as typeof currentServer.transport,
  };
  await page.locator('[data-testid="manual-refresh"]').click();
  await assertEligible("credential-slot-bearer");
  eligibilityModes += 1;
  currentServer = {
    ...currentServer,
    transport:
      oauthStaticTransport as unknown as typeof currentServer.transport,
  };
  await page.locator('[data-testid="manual-refresh"]').click();
  await assertEligible("credential-slot-client_secret");
  eligibilityModes += 1;
  currentServer = {
    ...currentServer,
    transport: {
      ...bearerTransport,
      authentication: { mode: "none" },
    } as unknown as typeof currentServer.transport,
  };
  await page.locator('[data-testid="manual-refresh"]').click();
  await page
    .getByText(
      "Credential replacement is not eligible for this configured transport",
    )
    .waitFor();
  eligibilityModes += 1;
  currentServer = {
    ...currentServer,
    transport:
      oauthDynamicTransport as unknown as typeof currentServer.transport,
  };
  await page.locator('[data-testid="manual-refresh"]').click();
  await page
    .getByText(
      "Credential replacement is not eligible for this configured transport",
    )
    .waitFor();
  eligibilityModes += 1;

  currentServer = { ...currentServer, transport: stdioTransport };
  await page.locator('[data-testid="manual-refresh"]').click();
  await assertEligible("credential-slot-primary");
  const warning =
    (await page.locator('[data-testid="server-credentials"]').textContent()) ??
    "";
  if (
    !warning.includes("may require operating-system interaction") ||
    !warning.includes("cannot promise unattended or noninteractive")
  )
    fail("credential form omitted keyring interaction warning");

  const field = page.locator("#credential-slot-primary");
  const confirmReplacement = async (stage: string) => {
    const button = page.locator(
      'dialog[open] [data-testid="credential-replacement-confirm-submit"]',
    );
    await button
      .waitFor({ state: "visible", timeout: 2000 })
      .catch(async () =>
        fail(
          `${stage} credential confirmation did not open: ${((await page.locator('[data-testid="server-credentials"]').textContent()) ?? "").slice(-240)}`,
        ),
      );
    await button.click();
  };
  await field.fill(firstCanary);
  await page.locator('[data-testid="credential-replacement-submit"]').click();
  const consequence =
    (await page
      .locator("#credential-replacement-confirm-consequence")
      .textContent()) ?? "";
  if (
    !consequence.includes("withdraws current routing") ||
    !consequence.includes("unknown outcomes")
  )
    fail("credential confirmation omitted interruption consequence");
  await page
    .locator('[data-testid="credential-replacement-confirm-cancel"]')
    .click();
  await page
    .locator('dialog [data-testid="credential-replacement-confirm-submit"]')
    .waitFor({ state: "hidden" });
  if (replacements !== 0 || (await field.inputValue()) !== "")
    fail("credential confirmation cancel submitted or retained a secret");

  await field.fill(firstCanary);
  await page.locator('[data-testid="credential-replacement-submit"]').click();
  if ((await field.inputValue()) !== firstCanary)
    fail("credential field changed before confirmation handoff");
  await confirmReplacement("stale");
  await page.getByText("Stale credential revision").waitFor();
  if ((await field.inputValue()) !== "")
    fail("stale credential submission retained a secret");
  await page.waitForFunction(
    () =>
      !document.querySelector<HTMLButtonElement>(
        '[data-testid="credential-replacement-submit"]',
      )?.disabled,
  );

  await field.fill(secondCanary);
  await page.locator('[data-testid="credential-replacement-submit"]').click();
  await confirmReplacement("uncertain");
  await page.getByText("Replacement outcome unknown").waitFor();
  if ((await field.inputValue()) !== "")
    fail("uncertain credential submission retained a secret");
  await page.waitForTimeout(350);
  if (Number(replacements) !== 2)
    fail("uncertain credential replacement replayed automatically");
  if (
    (await page
      .locator('[data-testid="credential-replacement-replay"]')
      .count()) !== 0
  )
    fail("credential replacement exposed replay control");
  if (serverReads <= readsBeforeRecovery)
    fail("uncertain credential replacement did not refresh server evidence");

  await page.waitForFunction(
    () =>
      !document.querySelector<HTMLButtonElement>(
        '[data-testid="credential-replacement-submit"]',
      )?.disabled,
  );
  await field.fill(thirdCanary);
  await page.locator('[data-testid="credential-replacement-submit"]').click();
  await confirmReplacement("success");
  await page.locator('[data-testid="operation-detail"]').waitFor();
  const exposed = `${firstCanary}|${secondCanary}|${thirdCanary}`;
  if (
    ((await page.locator("body").textContent()) ?? "").includes(
      "credential-canary-",
    ) ||
    page.url().includes("credential-canary-") ||
    (await page
      .locator("input")
      .evaluateAll((nodes) =>
        nodes.some((node) =>
          (node as HTMLInputElement).value.includes("credential-canary-"),
        ),
      ))
  )
    fail(`credential canary reached browser presentation: ${exposed.length}`);
  assertClosedStorage(await browserStorage(page));

  process.stdout.write(
    `${JSON.stringify({
      event: "server_credentials_complete",
      chromium_version: browserVersion,
      playwright_version: "1.62.1",
      requests: requestCount(),
      replacements,
      recovery_reads: serverReads - readsBeforeRecovery,
      eligibility_modes: eligibilityModes,
    })}\n`,
  );
}

async function runServerCatalogReads(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
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

  let serverReads = 0;
  let descriptorReads = 0;
  let catalogReads = 0;
  let serverStale = false;
  let serverRestarted = false;
  let descriptorRestarted = false;
  let catalogRestarted = false;
  const activeServer = serverReadFixture(serverReadIDs.active, {
    name: "Authority required",
    desired: "enabled",
    runtime: "authentication_required",
    credential: "reauthentication_required",
    durable: "current",
    active: "unavailable",
  });
  const degradedServer = serverReadFixture(serverReadIDs.degraded, {
    name: "Degraded catalog",
    desired: "enabled",
    runtime: "degraded",
    credential: "ready",
    durable: "stale",
    active: "stale",
  });
  const deletedServer = serverReadFixture(serverReadIDs.deleted, {
    name: "Deleted history",
    desired: "deleted",
    runtime: "deleted",
    credential: "not_required",
    durable: "retired",
    active: "absent",
  });

  await page.route("**/api/v1/servers**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const headers = await request.allHeaders();
    if (request.method() !== "GET" || headers["x-csrf-token"] === undefined)
      fail("server read view issued a non-read or unauthenticated request");
    const parts = url.pathname.split("/").filter(Boolean);
    if (parts.length === 4) {
      serverReads += 1;
      if (url.search !== "" || parts[3] !== serverReadIDs.active)
        fail("server item request changed shape");
      await route.fulfill({
        status: 200,
        headers: {
          "Content-Type": "application/json",
          ETag: `\"server-${serverReadIDs.active}-7\"`,
        },
        body: JSON.stringify(activeServer),
      });
      return;
    }
    if (parts.length >= 5 && parts[4] === "descriptors") {
      descriptorReads += 1;
      if (parts.length === 6) {
        if (url.search !== "" || parts[5] !== serverReadIDs.retiredTool)
          fail("descriptor item request changed shape");
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(
            descriptorReadFixture(
              serverReadIDs.retiredTool,
              serverReadIDs.active,
              "retired-tool",
              true,
            ),
          ),
        });
        return;
      }
      const query = url.searchParams;
      if (
        parts.length !== 5 ||
        query.get("limit") !== "50" ||
        query.get("retired") !== "include" ||
        [...query.keys()].some(
          (key) => key !== "limit" && key !== "retired" && key !== "cursor",
        )
      )
        fail("descriptor list request changed shape");
      const cursor = query.get("cursor");
      if (cursor === "descriptor-stale") {
        descriptorRestarted = true;
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 409,
            code: "stale_cursor",
            title: "Stale",
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          descriptorRestarted
            ? {
                items: [
                  descriptorReadFixture(
                    serverReadIDs.durableTool,
                    serverReadIDs.active,
                    "durable-only",
                    false,
                  ),
                ],
                next_cursor: null,
              }
            : {
                items: [
                  descriptorReadFixture(
                    serverReadIDs.currentTool,
                    serverReadIDs.active,
                    "current-tool",
                    false,
                  ),
                  descriptorReadFixture(
                    serverReadIDs.retiredTool,
                    serverReadIDs.active,
                    "retired-tool",
                    true,
                  ),
                ],
                next_cursor: "descriptor-stale",
              },
        ),
      });
      return;
    }
    serverReads += 1;
    const query = url.searchParams;
    if (
      parts.length !== 3 ||
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "cursor")
    )
      fail("server list request changed shape");
    const cursor = query.get("cursor");
    if (serverStale) {
      if (cursor === "server-stale") {
        serverRestarted = true;
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 409,
            code: "stale_cursor",
            title: "Stale",
          }),
        });
        return;
      }
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(
          serverRestarted
            ? { items: [activeServer], next_cursor: null }
            : {
                items: [
                  serverReadFixture(serverReadIDs.discarded, {
                    name: "Discarded stale server",
                    desired: "enabled",
                    runtime: "active",
                    credential: "ready",
                    durable: "current",
                    active: "current",
                  }),
                ],
                next_cursor: "server-stale",
              },
        ),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        cursor === "server-next"
          ? { items: [deletedServer], next_cursor: null }
          : {
              items: [activeServer, degradedServer],
              next_cursor: "server-next",
            },
      ),
    });
  });

  await page.route("**/api/v1/catalog**", async (route) => {
    catalogReads += 1;
    const request = route.request();
    const query = new URL(request.url()).searchParams;
    const headers = await request.allHeaders();
    if (
      request.method() !== "GET" ||
      headers["x-csrf-token"] === undefined ||
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "cursor")
    )
      fail("active catalog request changed shape");
    const cursor = query.get("cursor");
    if (cursor === "catalog-stale") {
      catalogRestarted = true;
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "stale_cursor",
          title: "Stale",
        }),
      });
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        catalog: {
          active_state: "degraded",
          active_generation: catalogRestarted ? "process-9" : "process-8",
          changed_at: "2026-08-28T13:00:00Z",
          issue_count: 2,
        },
        items: [
          descriptorReadFixture(
            catalogRestarted
              ? serverReadIDs.activeTool
              : serverReadIDs.currentTool,
            serverReadIDs.active,
            catalogRestarted ? "active-restarted" : "active-before-stale",
            false,
          ),
        ],
        next_cursor: catalogRestarted ? null : "catalog-stale",
      }),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/servers";
  });
  await page.locator('[data-testid="servers-view"]').waitFor();
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="server-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Authority required",
    "authentication_required",
    "reauthentication_required",
    "Degraded catalog",
    "durable stale",
    "active stale",
  ])
    if (!body.includes(phrase)) fail(`server inventory omitted ${phrase}`);
  await page.locator('[data-testid="load-more-servers"]').click();
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="server-row"]').length === 3,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Deleted history") || !body.includes("durable retired"))
    fail("server inventory omitted deleted durable history");

  serverStale = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.locator('[data-testid="load-more-servers"]').click();
  await page.waitForFunction(
    (id) => document.querySelector(`a[href="#/servers/${id}"]`) !== null,
    serverReadIDs.active,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!serverRestarted || body.includes("Discarded stale server"))
    fail("server stale traversal was merged");

  await page
    .locator(`a[href="#/servers/${serverReadIDs.active}"]`)
    .first()
    .click();
  await page.locator('[data-testid="server-detail"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Desired revision 7",
    "Durable catalog revision 7",
    "Active catalog unavailable",
    "Durable evidence is not proof of process publication or callability",
  ])
    if (!body.includes(phrase)) fail(`server detail omitted ${phrase}`);

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=descriptors`;
  }, serverReadIDs.active);
  await page.locator('[data-testid="descriptor-list"]').waitFor();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="descriptor-row"]').length === 2,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("current evidence") || !body.includes("retired evidence"))
    fail("descriptor list omitted current/retired evidence labels");
  await page
    .locator(
      `a[href="#/servers/${serverReadIDs.active}/descriptors/${serverReadIDs.retiredTool}"]`,
    )
    .click();
  await page.locator('[data-testid="descriptor-detail"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("Durable catalog revision 6") ||
    !body.includes("Historical evidence; not callable") ||
    (await page
      .locator(`a[href="#/servers/${serverReadIDs.active}"]`)
      .count()) === 0 ||
    (await page.locator('a[href="#/catalog"]').count()) === 0
  )
    fail("descriptor detail omitted evidence or reciprocal routes");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=descriptors`;
  }, serverReadIDs.active);
  await page.locator('[data-testid="load-more-descriptors"]').click();
  await page.waitForFunction(
    () => document.querySelector('a[data-tool-name="durable-only"]') !== null,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!descriptorRestarted || body.includes("retired-tool"))
    fail("descriptor stale traversal was merged");

  await page.evaluate(() => {
    window.location.hash = "#/catalog";
  });
  await page.locator('[data-testid="catalog-view"]').waitFor();
  await page.waitForFunction(() =>
    document
      .querySelector('[data-testid="catalog-view"]')
      ?.textContent?.includes("Process generation process-8"),
  );
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Process generation process-8",
    "Catalog degraded",
    "Degraded administrative evidence does not establish routability",
  ])
    if (!body.includes(phrase)) fail(`active catalog omitted ${phrase}`);
  if (
    (await page
      .locator(`a[href="#/servers/${serverReadIDs.active}"]`)
      .count()) === 0 ||
    (await page
      .locator(
        `a[href="#/servers/${serverReadIDs.active}/descriptors/${serverReadIDs.currentTool}"]`,
      )
      .count()) === 0
  )
    fail("active catalog omitted reciprocal routes");
  await page.locator('[data-testid="load-more-catalog"]').click();
  await page.waitForFunction(
    () =>
      document.querySelector('a[data-tool-name="active-restarted"]') !== null,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!catalogRestarted || body.includes("active-before-stale"))
    fail("active catalog stale traversal was merged");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "server_catalog_reads_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), server_reads: serverReads, descriptor_reads: descriptorReads, catalog_reads: catalogReads })}\n`,
  );
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
          ) &&
        !(
          input.scenario === "overview" &&
          (message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 409",
            ) ||
            message
              .text()
              .startsWith(
                "Failed to load resource: the server responded with a status of 503",
              ))
        ) &&
        !(
          input.scenario === "invocations" &&
          (message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 409",
            ) ||
            message
              .text()
              .startsWith(
                "Failed to load resource: the server responded with a status of 404",
              ))
        ) &&
        !(
          input.scenario === "server-catalog-reads" &&
          message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 409",
            )
        ) &&
        !(
          input.scenario === "server-create-update" &&
          [409, 412, 428, 502].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
        ) &&
        !(
          input.scenario === "server-operations" &&
          [412, 502].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
        ) &&
        !(
          input.scenario === "server-disconnect-delete" &&
          message
            .text()
            .startsWith(
              "Failed to load resource: the server responded with a status of 412",
            )
        ) &&
        !(
          input.scenario === "auth-flows" &&
          [409, 412].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
        ) &&
        !(
          input.scenario === "server-credentials" &&
          [412, 503].some((status) =>
            message
              .text()
              .startsWith(
                `Failed to load resource: the server responded with a status of ${status}`,
              ),
          )
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
    } else if (input.scenario === "m3-canary") {
      await runM3Canary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "m5-canary") {
      await runM5Canary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "m6-canary") {
      await runM6Canary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "principals") {
      await runPrincipals(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "principal-credentials") {
      await runPrincipalCredentials(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "grant-reads-create") {
      await runGrantReadsCreate(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "grant-correction") {
      await runGrantCorrection(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "overview") {
      await runOverview(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "invocations") {
      await runInvocations(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "system-status") {
      await runSystemStatus(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-disconnect-delete") {
      await runServerDisconnectDelete(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "auth-flows") {
      await runAuthFlows(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-credentials") {
      await runServerCredentials(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-operations") {
      await runServerOperations(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-create-update") {
      await runServerCreateUpdate(
        browser.version(),
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-catalog-reads") {
      await runServerCatalogReads(
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
    const expectedOAuthOpen =
      "https://issuer.example/authorize?client_id=safe&state=one-time-state&code_challenge=pkce";
    const externalFailure =
      input.scenario === "auth-flows"
        ? externalRequests.length !== 1 ||
          externalRequests[0] !== expectedOAuthOpen
        : externalRequests.length !== 0;
    const expectedConsoleFailures =
      (input.scenario === "principals" &&
        consoleFailures.length === 2 &&
        [409, 412].every((status) =>
          consoleFailures.some((value) =>
            value.includes(`server responded with a status of ${status}`),
          ),
        )) ||
      (input.scenario === "principal-credentials" &&
        consoleFailures.length === 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 412"),
        )) ||
      (input.scenario === "grant-reads-create" &&
        consoleFailures.length === 2 &&
        [400, 409].every((status) =>
          consoleFailures.some((value) =>
            value.includes(`server responded with a status of ${status}`),
          ),
        )) ||
      (input.scenario === "grant-correction" &&
        consoleFailures.length === 3 &&
        [400, 409, 503].every((status) =>
          consoleFailures.some((value) =>
            value.includes(`server responded with a status of ${status}`),
          ),
        ));
    if (
      externalFailure ||
      originFailures.length !== 0 ||
      (consoleFailures.length !== 0 && !expectedConsoleFailures)
    ) {
      fail(
        `unexpected browser protocol side effect (external=${externalRequests.length}, origin=${originFailures.length}, console=${consoleFailures.length}, console_classes=${consoleFailures.map((value) => /^Failed to load resource: the server responded with a status of ([0-9]{3})/.exec(value)?.[1] ?? "other").join(",")})`,
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
