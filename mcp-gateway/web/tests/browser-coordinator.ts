import {
  chromium,
  firefox,
  webkit,
  type Browser,
  type BrowserContext,
  type Page,
  type Request,
  type Response as PlaywrightResponse,
} from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { createHash } from "node:crypto";
import { appendFile } from "node:fs/promises";
import { isAbsolute, resolve } from "node:path";
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
  visualArtifactInventory,
  visualDestinations,
  visualRubric,
  visualStates,
} from "./visual-matrix.ts";
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
    | "session-lifecycle-canary"
    | "fragment-storage"
    | "authentication-epoch"
    | "read-generation"
    | "mutation-state"
    | "shell-primitives"
    | "accessibility-keyboard-responsive"
    | "visual-responsive-matrix"
    | "secret-storage-privacy"
    | "secret-sinks"
    | "prior-session-response-isolation-canary"
    | "overview-invocation-system-canary"
    | "server-management-canary"
    | "access-management-read-canary"
    | "system-administration-canary"
    | "visual-accessibility-privacy-canary"
    | "admin-credentials"
    | "backups"
    | "capability-audit"
    | "principals"
    | "principal-credentials"
    | "grant-reads-create"
    | "grant-correction"
    | "request-reads"
    | "request-adjudication"
    | "overview"
    | "invocations"
    | "system-status"
    | "server-catalog-reads"
    | "server-create-update"
    | "server-operations"
    | "server-credentials"
    | "server-disconnect-delete"
    | "auth-flows"
    | "development-live-reload"
    | "development-control-plane";
  base_url: string;
  admin_bearer: string;
  browser_kind?: "chromium" | "firefox" | "webkit";
  fixture_root?: string;
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
    (Object.keys(value).sort().join(",") !==
      "admin_bearer,base_url,scenario,version" &&
      Object.keys(value).sort().join(",") !==
        "admin_bearer,base_url,browser_kind,scenario,version" &&
      Object.keys(value).sort().join(",") !==
        "admin_bearer,base_url,fixture_root,scenario,version") ||
    !("version" in value) ||
    value.version !== 1 ||
    !("scenario" in value) ||
    (value.scenario !== "shell-load" &&
      value.scenario !== "browser-protocol" &&
      value.scenario !== "session-lifecycle-canary" &&
      value.scenario !== "fragment-storage" &&
      value.scenario !== "authentication-epoch" &&
      value.scenario !== "read-generation" &&
      value.scenario !== "mutation-state" &&
      value.scenario !== "shell-primitives" &&
      value.scenario !== "accessibility-keyboard-responsive" &&
      value.scenario !== "visual-responsive-matrix" &&
      value.scenario !== "secret-storage-privacy" &&
      value.scenario !== "secret-sinks" &&
      value.scenario !== "prior-session-response-isolation-canary" &&
      value.scenario !== "overview-invocation-system-canary" &&
      value.scenario !== "server-management-canary" &&
      value.scenario !== "access-management-read-canary" &&
      value.scenario !== "system-administration-canary" &&
      value.scenario !== "visual-accessibility-privacy-canary" &&
      value.scenario !== "admin-credentials" &&
      value.scenario !== "backups" &&
      value.scenario !== "capability-audit" &&
      value.scenario !== "principals" &&
      value.scenario !== "principal-credentials" &&
      value.scenario !== "grant-reads-create" &&
      value.scenario !== "grant-correction" &&
      value.scenario !== "request-reads" &&
      value.scenario !== "request-adjudication" &&
      value.scenario !== "overview" &&
      value.scenario !== "invocations" &&
      value.scenario !== "system-status" &&
      value.scenario !== "server-catalog-reads" &&
      value.scenario !== "server-create-update" &&
      value.scenario !== "server-operations" &&
      value.scenario !== "server-credentials" &&
      value.scenario !== "server-disconnect-delete" &&
      value.scenario !== "auth-flows" &&
      value.scenario !== "development-live-reload" &&
      value.scenario !== "development-control-plane") ||
    !("base_url" in value) ||
    typeof value.base_url !== "string" ||
    !/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(value.base_url) ||
    !("admin_bearer" in value) ||
    typeof value.admin_bearer !== "string" ||
    value.admin_bearer.length === 0 ||
    ("browser_kind" in value &&
      value.browser_kind !== "chromium" &&
      value.browser_kind !== "firefox" &&
      value.browser_kind !== "webkit") ||
    (value.scenario === "development-live-reload") !==
      "fixture_root" in value ||
    ("fixture_root" in value &&
      (typeof value.fixture_root !== "string" ||
        !isAbsolute(value.fixture_root)))
  ) {
    fail("invalid bridge input");
  }
  return value as BridgeInput;
}

async function loadShell(
  page: Page,
  requireProductionCSP = true,
): Promise<void> {
  const response = await page.goto("/", { waitUntil: "domcontentloaded" });
  if (response === null || response.status() !== 200) fail("shell load failed");
  await page.locator('[data-testid="gateway-shell"]').waitFor();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-session-lifecycle") !== "bootstrapping",
  );
  if ((await page.title()) !== "MCP Gateway") fail("unexpected shell title");
  const mastheadMark = page.locator(
    '.wordmark > img.mark[src="/assets/favicon.svg"]',
  );
  if (
    (await mastheadMark.count()) !== 1 ||
    (await mastheadMark.getAttribute("aria-hidden")) !== "true"
  )
    fail("masthead did not reuse the Gateway favicon");
  const csp = (await response.allHeaders())["content-security-policy"] ?? "";
  if (
    requireProductionCSP &&
    (csp.includes("'unsafe-") || !csp.includes("default-src 'self'"))
  )
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

async function runSessionLifecycleCanary(
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

async function runPriorSessionResponseIsolationCanary(
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
    }) !== undefined ||
    parseProblem({
      status: 400,
      code: "invalid_server_configuration",
      title: "The server configuration is invalid.",
    }) === undefined ||
    parseProblem({
      status: 400,
      code: "invalid_server_configuration",
      title: "The server configuration is invalid.",
      context: {
        field: "transport.working_directory",
        rule: "canonical_absolute_path",
      },
    })?.context?.field !== "transport.working_directory" ||
    parseProblem({
      status: 400,
      code: "invalid_server_configuration",
      title: "The server configuration is invalid.",
      context: { field: "transport.working_directory", rule: "invented" },
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
  if (coordinator.snapshot().freshness !== "current")
    fail("background refresh exposed transient global staleness");
  await eventually(
    () => coordinator.snapshot().panels.b?.status === "error",
    "panel failure was not isolated",
  );
  if (
    coordinator.snapshot().panels.a?.status !== "current" ||
    coordinator.snapshot().panels.a?.hasValue !== true
  )
    fail("background refresh did not preserve current prior data");
  coordinator.navigate("#/servers");
  if (
    coordinator.snapshot().panels.a?.status !== "loading" ||
    coordinator.snapshot().panels.a?.hasValue !== false
  )
    fail("navigation reused a value owned by another location");
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

async function runOverviewInvocationSystemCanary(
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
    "Active tools",
    "Configured servers",
    "Pending requests",
    "Recent invocations",
  ])
    if (!body.includes(phrase))
      fail(`Overview workflow canary omitted ${phrase}`);
  if (body.includes("redacted_arguments"))
    fail("Overview workflow canary exposed invocation capture");

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
    body.includes("Live updates on") ||
    body.includes("Live updates paused") ||
    !(await page.getByRole("switch", { name: "Live mode" }).isChecked()) ||
    body.includes("retains at most 4,096 recent rows") ||
    body.includes("Bounded invocation evidence") ||
    body.includes("redacted_arguments")
  )
    fail("Invocation workflow canary exposed redundant copy or capture");

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
  body = (await page.locator("body").textContent()) ?? "";
  if (
    (await page.locator('[data-testid="system-limit-row"]').count()) !== 0 ||
    !body.includes("Gateway status") ||
    (await page.getByRole("link", { name: "Resource limits" }).count()) !== 1
  )
    fail("System status did not keep detailed limits in their destination");

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=resource-limits";
  });
  await page.locator('[data-testid="system-limits-view"]').waitFor();
  if ((await page.locator('[data-testid="system-limit-row"]').count()) !== 31)
    fail("Resource limits workflow omitted closed limits");

  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "overview_invocation_system_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: 4 })}\n`,
  );
}

async function runServerManagementCanary(
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
      name: "Integrated server",
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
          client_id: "server-management-client",
          token_endpoint_auth_method: "client_secret_basic",
        },
        trusted_origins: [],
        request_offline_access: false,
      },
    },
  };
  server.runtime.dispatch = { in_use: 4, limit: 4, saturated: true };
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
          active_generation: "server-management-generation",
          changed_at: "2026-08-28T17:00:00Z",
          issue_count: 0,
        },
        items: [
          descriptorReadFixture(
            serverReadIDs.activeTool,
            serverID,
            "integrated_server_tool",
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
    [`#/servers/${serverID}`, "server-status-view"],
    [`#/servers/${serverID}?tab=status`, "server-status-view"],
    [`#/servers/${serverID}?tab=tools`, "descriptor-list"],
    [`#/servers/${serverID}?tab=activity`, "server-activity-view"],
    [`#/servers/${serverID}?tab=authentication`, "server-authentication-view"],
    [`#/servers/${serverID}?tab=settings`, "server-settings-view"],
    ["#/catalog", "catalog-view"],
  ];
  for (const [hash, testID] of destinations) {
    await page.evaluate((target) => {
      window.location.hash = target;
    }, hash);
    await page.locator(`[data-testid="${testID}"]`).waitFor();
    if (testID === "server-authentication-view") {
      const authenticationView = page.locator(
        '[data-testid="server-authentication-view"]',
      );
      if (
        (await authenticationView
          .locator(".server-context-guidance")
          .count()) !== 0 ||
        (await authenticationView.getByText("Inspect activity").count()) !== 0
      )
        fail("server authentication repeated status guidance or navigation");
    }
  }
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=status`;
  }, serverID);
  const serverStatus = page.locator('[data-testid="server-status-view"]');
  await serverStatus
    .locator('[data-testid="server-status-operational"]')
    .waitFor();
  if (
    (await page.locator(".subnav a").first().textContent())?.trim() !==
      "Status" ||
    (await serverStatus
      .locator('[data-testid="server-status-issues"]')
      .count()) !== 1 ||
    (await serverStatus
      .locator('[data-testid="server-status-operational"]')
      .count()) !== 1 ||
    (await serverStatus
      .locator('section[data-testid="server-status-details"]')
      .count()) !== 1 ||
    (await serverStatus
      .locator(
        '[data-testid="server-status-details"] [data-testid="server-id"]',
      )
      .textContent()) !== serverID ||
    (await page
      .locator('[data-testid="server-context"] [data-testid="server-id"]')
      .count()) !== 0 ||
    (await serverStatus
      .getByRole("button", { name: "Copy server ID" })
      .count()) !== 1 ||
    (await serverStatus
      .locator('code[data-testid="server-id"]')
      .evaluate((element) => element.getClientRects().length)) !== 1 ||
    !((await serverStatus.textContent()) ?? "").includes(
      "Dispatch capacity is saturated",
    )
  )
    fail("server status did not use the shared operator hierarchy");
  await serverStatus.getByRole("button", { name: "Copy server ID" }).click();
  await page.waitForFunction(() => {
    const status = document.querySelector(
      ".copyable-value-status",
    )?.textContent;
    return (
      status === "Server ID copied." || status === "Could not copy server ID."
    );
  });
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=settings`;
  }, serverID);
  await page.locator('[data-testid="server-editor"]').waitFor();
  await page.locator('[data-testid="server-destructive-actions"]').waitFor();
  await page.locator('[data-testid="delete-server"]').click();
  const body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Configuration",
    "best-effort remote revocation",
    "immutable namespace",
  ])
    if (!body.includes(phrase))
      fail(`Server management canary omitted ${phrase}`);
  if (body.includes("authorization_url"))
    fail("Server management canary exposed a one-time URL outside its sink");
  await page.locator('[data-testid="server-delete-confirm-cancel"]').click();
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "server_management_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: destinations.length })}\n`,
  );
}

async function runAccessManagementReadCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FB0";
  const requestID = "01ARZ3NDEKTSV4RRFFQ69G5FC0";
  let mutationCount = 0;
  const principal = {
    id: principalID,
    display_name: "Build agent",
    state: "active",
    visibility: "requestable",
    revision: "7",
    credential_revision: "3",
    credential: {
      id: "01ARZ3NDEKTSV4RRFFQ69G5FAD",
      fingerprint: "0123456789abcdef",
      revision: "3",
      created_at: "2026-08-28T12:00:00Z",
    },
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
  };
  const grant = {
    id: grantID,
    description: "Safe build access",
    revision: "1",
    principal_id: principalID,
    effect: "allow",
    server_id: serverID,
    upstream_name: "safe",
    constraint: { equals: { "/mode": "safe" } },
    expires_at: null,
    state: "active",
    created_at: "2026-08-28T12:00:00Z",
  };
  const requestedPolicy = {
    scope: "tool",
    target: "demo.safe",
    constraint: { equals: { "/mode": "safe" } },
    duration_seconds: "600",
    future_tools_acknowledged: false,
  };
  const request = {
    id: requestID,
    principal_id: principalID,
    state: "pending",
    revision: "4",
    requested_policy: requestedPolicy,
    approved_policy: null,
    approved_grant_id: null,
    rejection_reason: null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
    closed_at: null,
    resolved_server_id: serverID,
    resolved_upstream_name: "safe",
    submitted_evidence: null,
    approved_evidence: null,
    current_target: {
      scope: "tool",
      target_state: "extant",
      active_state: "current",
      durable_state: "current",
      catalog_revision: "10",
      fingerprint: "current-access-fingerprint",
      descriptor: { name: "safe", inputSchema: {}, annotations: {} },
    },
  };
  await page.route("**/api/v1/principals", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([principal]),
    });
  });
  await page.route("**/api/v1/servers", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify([
        {
          id: serverID,
          display_name: "Build server",
          namespace: "demo",
          created_at: "2026-08-28T12:00:00Z",
        },
      ]),
    });
  });
  await page.route("**/api/v1/principals/**", async (route) => {
    if (route.request().method() !== "GET") {
      mutationCount += 1;
      await route.abort();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"principal-${principalID}-7"` },
      body: JSON.stringify(principal),
    });
  });
  await page.route("**/api/v1/grants/**", async (route) => {
    if (route.request().method() !== "GET") {
      mutationCount += 1;
      await route.abort();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(grant),
    });
  });
  await page.route("**/api/v1/grant-requests/**", async (route) => {
    if (route.request().method() !== "GET") {
      mutationCount += 1;
      await route.abort();
      return;
    }
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"grant-request-${requestID}-4"` },
      body: JSON.stringify(request),
    });
  });

  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  const destinations: Array<[string, string]> = [
    [`#/access/principals/${principalID}`, "principal-detail"],
    [`#/access/grants/${grantID}`, "grant-detail"],
    [`#/requests/${requestID}`, "request-detail"],
  ];
  for (const [hash, testID] of destinations) {
    await page.evaluate((target) => {
      window.location.hash = target;
    }, hash);
    await page.locator(`[data-testid="${testID}"]`).waitFor();
  }
  await page.locator('[data-testid="request-actions"]').waitFor();
  const body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Submitted: no descriptor evidence",
    "Current target",
    "Approval creates one ordinary ALLOW only",
    "It never resumes, retries, or executes a held call",
  ])
    if (!body.includes(phrase))
      fail(`Access management read canary omitted ${phrase}`);
  if (body.includes("authorization_url") || body.includes("raw_result"))
    fail(
      "Access management read canary exposed one-time or raw evidence material",
    );
  if (mutationCount !== 0)
    fail("Access management read canary replayed an adjudication mutation");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "access_management_read_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: destinations.length, mutations: mutationCount })}\n`,
  );
}

async function runSystemAdministrationCanary(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  let domainMutations = 0;
  page.on("request", (request) => {
    if (
      request.method() !== "GET" &&
      /\/api\/v1\/(?:admin-credentials|backups)/.test(request.url())
    )
      domainMutations += 1;
  });
  await page.evaluate(() => {
    window.location.hash = "#/system";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  const destinations: Array<[string, string]> = [
    ["#/system", "system-status-panel"],
    ["#/system?tab=resource-limits", "system-limits-view"],
    ["#/system?tab=admin-credentials", "admin-credentials-view"],
    ["#/system?tab=backups", "backups-view"],
  ];
  let rendered = "";
  for (const [hash, testID] of destinations) {
    await page.evaluate((target) => {
      window.location.hash = target;
    }, hash);
    await page.locator(`[data-testid="${testID}"]`).waitFor();
    rendered += ` ${(await page.locator("body").textContent()) ?? ""}`;
  }
  for (const phrase of [
    "Gateway status",
    "Resource limits",
    "Admin credentials",
    "Backups",
  ])
    if (!rendered.includes(phrase))
      fail(`System administration canary omitted ${phrase}`);
  if (rendered.includes("Workflow not yet available"))
    fail("System administration canary retained a System placeholder");
  if (domainMutations !== 0)
    fail("System administration canary submitted a mutation");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "system_administration_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), destinations: destinations.length, mutations: domainMutations })}\n`,
  );
}

async function runCapabilityAudit(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  await assertViewGenerationFoundation();
  let eventStreams = 0;
  let mutations = 0;
  page.on("request", (request) => {
    if (request.method() === "POST" && request.url().endsWith("/api/v1/events"))
      eventStreams += 1;
  });
  await page.route("**/api/v1/system-status", async (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(overviewStatusFixture()),
    }),
  );
  await page.route("**/api/v1/admin-credentials?*", async (route) => {
    if (route.request().method() !== "GET") mutations += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [], next_cursor: null }),
    });
  });
  await page.route("**/api/v1/backups?*", async (route) => {
    if (route.request().method() !== "GET") mutations += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({ items: [], next_cursor: null }),
    });
  });
  await page.route(
    "**/api/v1/events",
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": force reconnect\n\n",
      }),
    { times: 1 },
  );
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.waitForFunction(
    () =>
      (
        document.querySelector(
          '[data-testid="admin-credential-create"]',
        ) as HTMLButtonElement | null
      )?.disabled === true,
  );
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=backups";
  });
  await page.locator('[data-testid="backups-view"]').waitFor();
  await page.locator('[data-testid="backup-create"]').click();
  await page.waitForFunction(
    () =>
      (
        document.querySelector(
          '[data-testid="backup-review-create"]',
        ) as HTMLButtonElement | null
      )?.disabled === true,
  );
  if (mutations !== 0)
    fail("cross-destination storage latch submitted a mutation");
  const reconnectDeadline = Date.now() + 5000;
  while (eventStreams < 2 && Date.now() < reconnectDeadline)
    await new Promise((resolve) => setTimeout(resolve, 25));
  if (eventStreams < 2) fail("event stream did not reconnect");
  const body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("stopped-process operation") ||
    !body.includes("Storage mutation is closed")
  )
    fail("cross-destination latch guidance is incomplete");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "capability_audit_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), event_streams: eventStreams, mutations, destinations: 2 })}\n`,
  );
}

async function runBackups(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const ids = ["01ARZ3NDEKTSV4RRFFQ69G5FB0", "01ARZ3NDEKTSV4RRFFQ69G5FB1"];
  const backup = (index: number) => ({
    id: ids[index],
    created_at: "2026-08-28T12:00:00Z",
    installation_id: "11111111-2222-3333-4444-555555555555",
    schema_version: "10",
    source_revision: String(index + 7),
    size_bytes: 4096 + index,
    sha256: String(index + 1).repeat(64),
  });
  let items = [backup(0)];
  let creates = 0;
  let deletes = 0;
  let details = 0;
  let recoveryKey: string | undefined;
  await page.route("**/api/v1/backups**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const id =
      url.pathname === "/api/v1/backups"
        ? undefined
        : url.pathname.split("/").pop();
    if (request.method() === "GET" && id === undefined) {
      if (url.searchParams.get("limit") !== "100")
        fail("backup list changed shape");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items, next_cursor: null }),
      });
      return;
    }
    if (request.method() === "GET" && id !== undefined) {
      details += 1;
      const item = items.find((candidate) => candidate.id === id);
      if (item === undefined) fail("unknown backup detail");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(item),
      });
      return;
    }
    if (request.method() === "POST" && id === undefined) {
      creates += 1;
      const headers = await request.allHeaders();
      const key = headers["idempotency-key"];
      if (request.postData() !== "{}" || key === undefined)
        fail("backup create changed shape");
      if (creates === 1) {
        recoveryKey = key;
        await route.abort("failed");
        return;
      }
      if (creates === 2) {
        if (key !== recoveryKey) fail("backup replay changed idempotency key");
        const created = backup(1);
        items = [...items, created];
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: JSON.stringify(created),
        });
        return;
      }
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
    if (request.method() === "DELETE" && id !== undefined) {
      deletes += 1;
      const headers = await request.allHeaders();
      if (
        request.postData() !== "{}" ||
        headers["idempotency-key"] !== undefined
      )
        fail("backup delete changed shape");
      items = items.filter((item) => item.id !== id);
      await route.fulfill({ status: 204, body: "" });
      return;
    }
    fail("unexpected backup request");
  });

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=backups";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="backups-view"]').waitFor();
  if (
    (
      (await page.locator('[data-testid="backups-view"]').textContent()) ?? ""
    ).includes(
      "The browser cannot restore, reset, verify, or clear a storage latch.",
    )
  )
    fail("backup inventory retained redundant recovery guidance");
  await page.locator('[data-testid="backup-inspect"]').click();
  await page.locator('[data-testid="backup-detail"]').waitFor();
  await page.locator('[data-testid="backup-create"]').click();
  await page.locator('[data-testid="backup-create-view"]').waitFor();
  await page.locator('[data-testid="backup-review-create"]').click();
  if (Number(creates) !== 0) fail("backup submitted before final review");
  await page.locator('[data-testid="backup-create-confirm-submit"]').click();
  await page.getByText("Backup outcome is unknown", { exact: true }).waitFor();
  if (creates !== 1) fail("uncertain backup create replayed automatically");
  await page.locator('[data-testid="backup-replay"]').click();
  await page.getByText(/is durably published/).waitFor();
  await page.locator('[data-testid="backup-delete"]').first().click();
  await page.locator('[data-testid="backup-delete-confirm-submit"]').click();
  await page.getByText(/Backup deleted/).waitFor();
  await page.locator('[data-testid="backup-create"]').click();
  await page.locator('[data-testid="backup-review-create"]').click();
  await page.locator('[data-testid="backup-create-confirm-submit"]').click();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="gateway-shell"]')
        ?.getAttribute("data-mutation-availability") === "storage_latched",
  );
  if (await page.locator('[data-testid="backup-review-create"]').isEnabled())
    fail("storage latch left backup mutation enabled");
  const body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of ["immutable owner-only", "stopped-process operation"])
    if (!body.includes(phrase)) fail(`backup boundary omitted ${phrase}`);
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "backups_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), creates, deletes, details })}\n`,
  );
}

async function runVisualAccessibilityPrivacyCanary(
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

async function runSecretStoragePrivacy(
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

async function runVisualResponsiveMatrix(
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

async function runAccessibilityKeyboardResponsive(
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

async function runAdminCredentials(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const ids = [
    "01ARZ3NDEKTSV4RRFFQ69G5FA0",
    "01ARZ3NDEKTSV4RRFFQ69G5FA1",
    "01ARZ3NDEKTSV4RRFFQ69G5FA2",
    "01ARZ3NDEKTSV4RRFFQ69G5FA3",
  ];
  const issuedBearer = `mgw_admin_${"I".repeat(43)}`;
  const lostBearer = `mgw_admin_${"L".repeat(43)}`;
  const credential = (
    index: number,
    status: "active" | "revoked" = "active",
    nonExpiring = true,
  ) => ({
    id: ids[index],
    fingerprint: `${String(index + 1).padStart(16, "0")}`,
    created_at: "2026-08-28T12:00:00Z",
    expires_at: nonExpiring ? null : "2026-09-28T12:00:00Z",
    non_expiring: nonExpiring,
    status,
    revision: String(index + 1),
  });
  let items = [credential(0), credential(1), credential(2, "active", false)];
  let creates = 0;
  let revokes = 0;
  let releaseLost: (() => void) | undefined;
  let markLostStarted: (() => void) | undefined;
  const lostStarted = new Promise<void>((resolve) => {
    markLostStarted = resolve;
  });

  await page.route("**/api/v1/admin-credentials**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const id =
      path === "/api/v1/admin-credentials" ? undefined : path.split("/").pop();
    if (request.method() === "GET" && id === undefined) {
      const query = new URL(request.url()).searchParams;
      if (query.get("limit") !== "100")
        fail("admin credential list changed shape");
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items, next_cursor: null }),
      });
      return;
    }
    if (request.method() === "POST" && id === undefined) {
      creates += 1;
      const body = JSON.parse(request.postData() ?? "null") as Record<
        string,
        unknown
      >;
      if (Object.keys(body).join(",") !== "expires_at")
        fail("admin credential create changed shape");
      if (creates === 2) {
        markLostStarted?.();
        await new Promise<void>((resolve) => {
          releaseLost = resolve;
        });
      }
      if (creates === 3) {
        await route.fulfill({
          status: 201,
          contentType: "application/json",
          body: "{",
        });
        return;
      }
      const created = credential(3);
      items = [...items, created];
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify({
          ...created,
          bearer: creates === 1 ? issuedBearer : lostBearer,
        }),
      });
      return;
    }
    if (request.method() === "DELETE" && id !== undefined) {
      revokes += 1;
      if (request.postData() !== "{}")
        fail("admin credential revoke changed shape");
      if (revokes === 3) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: "{",
        });
        return;
      }
      if (id === ids[0]) {
        await route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          body: JSON.stringify({
            status: 409,
            code: "conflict",
            title: "The last active non-expiring authority cannot be revoked.",
          }),
        });
        return;
      }
      items = items.map((item) =>
        item.id === id
          ? { ...item, status: "revoked" as const, revision: "9" }
          : item,
      );
      await route.fulfill({ status: 204, body: "" });
      return;
    }
    fail("unexpected admin credential request");
  });

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  const inventoryCopy =
    (await page
      .locator('[data-testid="admin-credentials-view"]')
      .textContent()) ?? "";
  if (
    inventoryCopy.includes("Bearers appear once in the protected display.") ||
    inventoryCopy.includes("Revocation closes child sessions")
  )
    fail("admin credential inventory retained workflow-only guidance");
  if (
    (await page
      .getByRole("heading", {
        level: 2,
        name: "Admin credentials",
        exact: true,
      })
      .count()) !== 1
  )
    fail("Admin credentials title was inconsistent");
  const adminHeaders = await page
    .locator('[data-testid="admin-credentials-view"] thead th')
    .allTextContents();
  if (
    adminHeaders.map((value) => value.replace(/\s?[↑↓↕]$/, "")).join("|") !==
    "Fingerprint|ID|Status|Created|Expires|Actions"
  )
    fail(`Admin credential columns drifted: ${adminHeaders.join("|")}`);
  if (
    (await page.locator('[data-testid="admin-credential-inspect"]').count()) !==
      0 ||
    (await page.locator('[data-testid="admin-credential-detail"]').count()) !==
      0 ||
    (await page
      .locator('[data-testid="admin-credential-row"] th code')
      .count()) !== items.length
  )
    fail("admin credential fingerprints remained interactive or expanded");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.locator('[data-testid="admin-credential-create-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-expiry"]').fill("invalid");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.getByText(/Expiry must be an RFC 3339 time/).waitFor();
  if (creates !== 0) fail("invalid admin expiry reached the API");
  await page.locator('[data-testid="admin-credential-expiry"]').fill("");
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .getByRole("heading", { name: "Review admin credential", exact: true })
    .waitFor();
  if (creates !== 0) fail("admin credential submitted before final review");
  await page
    .locator('[data-testid="admin-credential-create-confirm-submit"]')
    .click();
  await page.locator('[data-testid="one-time-value"]').waitFor();
  if (
    (await page.locator('[data-testid="one-time-value"]').textContent()) !==
    issuedBearer
  )
    fail("admin bearer did not reach the prepared sink");
  await page.locator('[data-testid="copy-one-time-value"]').click();
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-revoke"]').first().click();
  await page
    .locator('[data-testid="admin-credential-revoke-confirm-submit"]')
    .click();
  await page
    .getByText("The last active non-expiring authority cannot be revoked.", {
      exact: true,
    })
    .waitFor();
  const expiringRow = page
    .locator('[data-testid="admin-credential-row"]')
    .filter({ hasText: ids[2]! });
  await expiringRow.locator('[data-testid="admin-credential-revoke"]').click();
  await page
    .locator('[data-testid="admin-credential-revoke-confirm-submit"]')
    .click();
  await page.getByText(/Administrator credential revoked/).waitFor();
  const body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("cannot be forced"))
    fail("admin credential consequence omitted cannot be forced");

  await page.locator('[data-testid="admin-credential-revoke"]').last().click();
  await page
    .locator('[data-testid="admin-credential-revoke-confirm-submit"]')
    .click();
  await page
    .getByText(
      "Do not replay revoke. The credential may already be revoked and child sessions may already be closed. Refresh metadata before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.evaluate(() => {
    window.location.hash = "#/overview";
  });
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.evaluate(() => {
    window.location.hash = "#/system?tab=admin-credentials";
  });
  await page.locator('[data-testid="admin-credentials-view"]').waitFor();

  await page.locator('[data-testid="admin-credential-create"]').click();
  await page.locator('[data-testid="admin-credential-create-view"]').waitFor();
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .locator('[data-testid="admin-credential-create-confirm-submit"]')
    .click();
  await lostStarted;
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  releaseLost?.();
  await page
    .getByText(
      "The created credential may be active, but its bearer was lost and cannot be recovered. Review credential metadata and explicitly revoke it if unusable before creating a deliberate replacement. Nothing was replayed.",
      { exact: true },
    )
    .waitFor();
  await page.locator('[data-testid="admin-credential-create"]').click();
  await page
    .locator('[data-testid="admin-credential-create-confirm-submit"]')
    .click();
  await page
    .getByText(
      "No bearer can be displayed. Inspect current state before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await page
    .getByText(
      "Do not replay. The credential may be active while its bearer is permanently lost. Refresh metadata, then explicitly revoke an unusable credential before creating a deliberate replacement.",
      { exact: true },
    )
    .waitFor();
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await waitForLifecycle(page, "signed_out");
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, issuedBearer, lostBearer],
    false,
  );
  process.stdout.write(
    `${JSON.stringify({ event: "admin_credentials_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), creates, revokes })}\n`,
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
              items: [current],
              next_cursor: null,
            }
          : {
              items: [
                principal(secondID, "Disabled agent", "disabled", "all", "4"),
              ],
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
          description: "Default Gateway access",
          revision: "1",
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
          : id === secondID
            ? principal(secondID, "Disabled agent", "disabled", "all", "4")
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
  if (!body.includes("Disabled agent"))
    fail("principal list omitted a principal");
  const principalNames = await page
    .locator('[data-testid="principal-row"] th[scope="row"]')
    .allTextContents();
  if (
    principalNames.map((name) => name.trim()).join("|") !==
      "Build agent|Disabled agent" ||
    (await page
      .locator('[data-testid="principals-view"] thead th')
      .first()
      .getAttribute("aria-sort")) !== "ascending"
  )
    fail(`principals did not default to Name ascending: ${principalNames}`);
  const principalSearch = page.getByLabel("Name or ID", { exact: true });
  await principalSearch.fill("Build agnt");
  if (
    (await page.locator('[data-testid="principal-row"]').count()) !== 1 ||
    !(await page.locator('[data-testid="principal-row"]').innerText()).includes(
      "Build agent",
    )
  )
    fail("principal typo search did not match");
  await principalSearch.fill(secondID.toLowerCase());
  if ((await page.locator('[data-testid="principal-row"]').count()) !== 0)
    fail("principal ID search was not literal");
  await principalSearch.fill(secondID);
  if (
    (await page.locator('[data-testid="principal-row"]').count()) !== 1 ||
    !(await page.locator('[data-testid="principal-row"]').innerText()).includes(
      "Disabled agent",
    )
  )
    fail("principal ID search did not match");
  await page.getByRole("button", { name: "Reset" }).click();
  for (const phrase of [
    "Permanent agent principals",
    "Compare permanent identity",
    "Visibility does not grant call authority",
  ])
    if (body.includes(phrase)) fail(`principal list retained ${phrase}`);
  const principalHeaders = await page
    .locator('[data-testid="principals-view"] thead th')
    .allTextContents();
  if (
    principalHeaders
      .map((value) => value.replace(/\s?[↑↓↕]$/, ""))
      .join("|") !== "Name|ID|Status|Visibility"
  )
    fail(`principal columns drifted: ${principalHeaders.join("|")}`);
  if (
    (await page
      .getByRole("link", { name: "Principals", exact: true })
      .count()) !== 1 ||
    (await page.getByRole("link", { name: "Grants", exact: true }).count()) !==
      1 ||
    (await page.getByRole("link", { name: "Access", exact: true }).count()) !==
      0
  )
    fail("Principals and Grants were not independent navigation destinations");
  const principalCreate = page.locator('[data-testid="principal-create-link"]');
  if (
    !(await principalCreate.evaluate((element) =>
      element.classList.contains("create-action"),
    )) ||
    Math.abs(
      (await principalCreate.boundingBox())!.x -
        (await page.locator('[data-testid="principals-view"]').boundingBox())!
          .x,
    ) > 1
  )
    fail("Create principal was not aligned with Create server");

  await page.evaluate(() => {
    window.location.hash = "#/access/principals/new";
  });
  await page.locator('[data-testid="principal-create-view"]').waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  if (
    (await page
      .getByRole("heading", { level: 1, name: "Create principal", exact: true })
      .count()) !== 1 ||
    body.includes("permanent synthetic default ALLOW grant") ||
    !body.includes("Gateway self-service tools") ||
    !body.includes("permanent identity")
  )
    fail("principal creation retained internal default-grant language");
  const principalActionGap = await page.evaluate(() => {
    const field = document.querySelector<HTMLElement>(
      '[data-testid="principal-visibility"]',
    )!;
    const action = document.querySelector<HTMLElement>(
      '[data-testid="principal-editor-submit"]',
    )!;
    return (
      action.getBoundingClientRect().top - field.getBoundingClientRect().bottom
    );
  });
  if (principalActionGap < 15)
    fail(`principal create action gap was ${principalActionGap}px`);
  await page
    .locator('[data-testid="principal-display-name"]')
    .fill("New automation");
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await page.locator('[data-testid="unsaved-changes-cancel"]').waitFor();
  await page.locator('[data-testid="unsaved-changes-cancel"]').click();
  await page
    .locator('dialog[aria-labelledby="unsaved-changes-title"]')
    .waitFor({ state: "hidden" });
  if (
    (await page.evaluate(() => window.location.hash)) !== "#/principals/new" ||
    (await page
      .locator('[data-testid="principal-display-name"]')
      .inputValue()) !== "New automation"
  )
    fail("principal editor did not register its dirty draft");
  await page
    .locator('[data-testid="principal-visibility"]')
    .selectOption("allowed-only");
  if (
    (
      await page
        .locator('[data-testid="principal-editor-submit"]')
        .textContent()
    )?.trim() !== "Review and create"
  )
    fail("principal creation omitted its review action");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page
    .getByRole("heading", { name: "Review principal", exact: true })
    .waitFor();
  if (creates !== 0) fail("principal creation submitted before final review");
  const principalReview =
    (await page
      .locator("#principal-change-confirm-consequence")
      .textContent()) ?? "";
  if (
    !principalReview.includes("New automation") ||
    !principalReview.includes("Allowed tools only") ||
    !principalReview.includes("Default Gateway access")
  )
    fail("principal creation review omitted submitted values");
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page.locator('[data-testid="principal-detail"]').waitFor();

  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, firstID);
  await page.locator('[data-testid="principal-detail"]').waitFor();
  await page
    .getByRole("heading", { level: 1, name: "Build agent", exact: true })
    .waitFor();
  if (
    (await page
      .getByRole("heading", {
        level: 2,
        name: "Principal details",
        exact: true,
      })
      .count()) !== 1 ||
    (await page.locator('[data-testid="detail-context"] h1').count()) !== 1
  )
    fail("principal detail did not use the shared detail hierarchy");
  body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes("permanent synthetic default ALLOW grant") ||
    body.includes("Re-enabling restores neither") ||
    body.includes("Visibility is not call authorization") ||
    body.includes("View grants") ||
    body.includes("View requests") ||
    body.includes("View invocations")
  )
    fail("principal detail retained implementation-oriented copy or shortcuts");

  await page.evaluate((id) => {
    (
      window as Window & { principalDetailFlashed?: boolean }
    ).principalDetailFlashed = false;
    new MutationObserver(() => {
      if (
        window.location.hash === `#/principals/${id}` &&
        document
          .querySelector('[data-testid="principal-detail"]')
          ?.textContent?.includes("Build agent")
      )
        (
          window as Window & { principalDetailFlashed?: boolean }
        ).principalDetailFlashed = true;
    }).observe(document.body, { childList: true, subtree: true });
    window.location.hash = `#/access/principals/${id}`;
  }, secondID);
  await page.getByText("Disabled agent", { exact: true }).waitFor();
  if (
    (
      ((await page.locator("body").textContent()) ?? "").match(/Not issued/g) ??
      []
    ).length !== 1
  )
    fail("principal credential absence was repeated");
  if (
    await page.evaluate(
      () =>
        (window as Window & { principalDetailFlashed?: boolean })
          .principalDetailFlashed === true,
    )
  )
    fail("principal navigation flashed the prior identity");
  const untouchedPrincipalWarned = await page.evaluate(() => {
    const event = new Event("beforeunload", { cancelable: true });
    return !window.dispatchEvent(event);
  });
  if (untouchedPrincipalWarned)
    fail("principal route change retained the prior principal dirty baseline");
  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, firstID);
  await page.getByText("Build agent", { exact: true }).waitFor();

  await page
    .locator('[data-testid="principal-display-name"]')
    .fill("Renamed agent");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page
    .getByRole("heading", { name: "Renamed agent", exact: true })
    .waitFor();
  const principalState = page.getByRole("switch", {
    name: "Principal enabled",
  });
  if (!(await principalState.isChecked()))
    fail("active principal switch was not checked");
  await principalState.click();
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page
    .getByText("The principal revision is stale.", { exact: true })
    .waitFor();
  if ((await principalState.isChecked()) !== false)
    fail("principal stale refresh discarded safe draft");
  await page.locator('[data-testid="principal-editor-submit"]').click();
  await page.locator('[data-testid="principal-change-confirm-submit"]').click();
  await page
    .locator(".status-label.warning", { hasText: "Disabled" })
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
        } else if (issues === 4 || issues === 5) {
          await route.fulfill({
            status: 201,
            contentType: "application/json",
            body: "{",
          });
          return;
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
      if (revokes === 1) {
        await route.fulfill({
          status: 200,
          contentType: "application/json",
          body: "{",
        });
        return;
      }
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
  if (
    !body.includes("Only one agent credential may be active at a time.") ||
    !body.includes(credentialID) ||
    body.includes("Credential authority") ||
    body.includes("interrupted immediately") ||
    (await page
      .getByRole("heading", { name: "Agent credential", exact: true })
      .count()) !== 1 ||
    (await page
      .getByRole("button", { name: "Rotate credential", exact: true })
      .count()) !== 1
  )
    fail("principal credential guidance or terminology was unclear");
  if (
    !(await page
      .locator('[data-testid="principal-credential-issue"]')
      .evaluate((element) => element.classList.contains("danger-action")))
  )
    fail("credential rotation was not styled as dangerous");

  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator(
      'dialog[aria-labelledby="principal-credential-confirm-title"][open]',
    )
    .waitFor();
  const openCredentialDialogs = await page
    .locator("dialog[open]")
    .evaluateAll((dialogs) =>
      dialogs.map((dialog) => dialog.id || dialog.className),
    );
  if (openCredentialDialogs.length !== 1)
    fail(
      `credential review opened overlapping dialogs: ${openCredentialDialogs.join(",")}`,
    );
  if (
    await page
      .locator('dialog[aria-labelledby="one-time-display-title"]')
      .evaluate((dialog) => dialog.hasAttribute("open"))
  )
    fail("one-time bearer dialog opened before credential confirmation");
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
  if ((await page.locator("dialog[open]").count()) !== 1)
    fail("credential result retained overlapping dialogs");
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
  releaseLost?.();
  await page
    .getByText(
      "The replacement may now be current and the prior bearer may already be invalid. Review principal metadata, then explicitly rotate or revoke the lost current credential. Do not replay the operation.",
      { exact: true },
    )
    .waitFor();
  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page
    .getByText(
      "No bearer can be displayed. Inspect current state before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await page
    .getByText(
      "Do not replay. The replacement may be current and the prior bearer may already be invalid. Refresh the principal, then explicitly rotate or revoke the observed current credential.",
      { exact: true },
    )
    .waitFor();
  await page.locator('[data-testid="logout"]').click();
  await page.locator('[data-testid="logout-confirmation-submit"]').click();
  await waitForLifecycle(page, "signed_out");
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
  await page
    .getByText(
      "Do not replay revoke. Authority may already be revoked. Refresh the principal before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.evaluate(() => {
    window.location.hash = "#/overview";
  });
  await page.locator('[data-testid="overview-grid"]').waitFor();
  await page.evaluate((id) => {
    window.location.hash = `#/access/principals/${id}`;
  }, principalID);
  await page.locator('[data-testid="principal-credential-revoke"]').waitFor();
  await page.locator('[data-testid="principal-credential-revoke"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page.waitForFunction(
    () => document.body.textContent?.includes("Not issued") === true,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("Prior authority no longer authenticates") ||
    (await page
      .getByRole("button", { name: "Issue credential", exact: true })
      .count()) !== 1
  )
    fail("principal credential revoke omitted authority result");
  await page.locator('[data-testid="principal-credential-issue"]').click();
  await page
    .locator('[data-testid="principal-credential-confirm-submit"]')
    .click();
  await page
    .getByText(
      "No bearer can be displayed. Inspect current state before another explicit action.",
      { exact: true },
    )
    .waitFor();
  await page.getByRole("button", { name: "Dismiss and clear" }).click();
  await page
    .getByText(
      "Do not replay issue. A current credential may occupy the slot while its bearer is permanently lost. Refresh the principal, then explicitly rotate or revoke the observed credential.",
      { exact: true },
    )
    .waitFor();
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
    description: (id === firstGrantID
      ? "Reporting access"
      : "Restricted access") as string | null,
    revision: "1",
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
  let descriptionPatches = 0;

  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: principalID,
            display_name: "Automation agent",
            state: "active",
            visibility: "allowed-only",
            revision: "1",
            credential_revision: "0",
            credential: null,
            created_at: "2026-08-28T12:00:00Z",
            updated_at: "2026-08-28T12:00:00Z",
          },
        ],
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v1/servers?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          serverReadFixture(serverID, {
            name: "Reporting server",
            desired: "enabled",
            runtime: "active",
            credential: "ready",
            durable: "current",
            active: "current",
          }),
        ],
        next_cursor: null,
      }),
    });
  });

  await page.route("**/api/v1/grants?*", async (route) => {
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "cursor")
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
    const request = route.request();
    const id = new URL(request.url()).pathname.split("/").pop();
    if (request.method() === "PATCH") {
      const headers = await request.allHeaders();
      if (
        id !== firstGrantID ||
        headers["if-match"] !== `"grant-${firstGrantID}-${active.revision}"`
      )
        fail(
          `grant description PATCH precondition changed: ${JSON.stringify({ id, actual: headers["if-match"], revision: active.revision })}`,
        );
      const patch = JSON.parse(request.postData() ?? "") as Record<
        string,
        unknown
      >;
      if (
        Object.keys(patch).join(",") !== "description" ||
        (patch.description !== "Updated reporting access" &&
          patch.description !== null)
      )
        fail("grant description PATCH body changed");
      descriptionPatches += 1;
      active.description = patch.description as string | null;
      active.revision = String(Number(active.revision) + 1);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"grant-${firstGrantID}-${active.revision}"` },
        body: JSON.stringify(active),
      });
      return;
    }
    if (request.method() !== "GET") {
      await route.fallback();
      return;
    }
    const item =
      id === secondGrantID
        ? expired
        : id === createdIDs[0]
          ? grant(id!, "allow", "active", null, null, null)
          : id === createdIDs[1]
            ? grant(
                id!,
                "deny",
                "active",
                "literal.tool",
                null,
                "2030-01-01T00:00:00Z",
              )
            : active;
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
        "description,principal_id,effect,server_id,upstream_name,constraint,expires_at" ||
      body.description !== "New access" ||
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
      !raw.includes('"version":2') ||
      !raw.includes('"/a~1b/0":1.0') ||
      !raw.includes('"/empty/":null') ||
      !raw.includes('"/flag":true') ||
      !raw.includes('"/name":"literal"') ||
      !raw.includes('"/resource":"item-\\\\d+"')
    )
      fail(`exact-tool lexical constraint changed shape: ${raw}`);
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

  await page.evaluate(() => {
    window.location.hash = "#/access/grants";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="grant-row"]').length === 2,
  );
  let body = (await page.locator("body").textContent()) ?? "";
  const grantSearch = page.getByLabel("Description or ID", { exact: true });
  await grantSearch.fill("Reportng access");
  if (
    (await page.locator('[data-testid="grant-row"]').count()) !== 1 ||
    !(await page.locator('[data-testid="grant-row"]').innerText()).includes(
      "Reporting access",
    )
  )
    fail("grant typo search did not match");
  await page.getByRole("button", { name: "Reset" }).click();
  for (const phrase of [
    "Reporting access",
    "Restricted access",
    "Automation agent",
    "Active",
    "Expired",
    "Create grant",
  ])
    if (!body.includes(phrase)) fail(`grant list omitted ${phrase}`);
  const headers = await page.locator("thead th").allTextContents();
  if (
    headers[0] !== "ID↕" ||
    headers[1] !== "Description↕" ||
    headers.includes("Action")
  )
    fail(`grant table identity columns changed: ${headers.join("|")}`);
  const firstGrantRow = page.locator('[data-testid="grant-row"]').first();
  if (
    (await firstGrantRow
      .locator(`a[href="#/grants/${firstGrantID}"]`)
      .count()) !== 1 ||
    (await firstGrantRow.locator("td").first().locator("a").count()) !== 0
  )
    fail("grant description remained navigational metadata");
  if (
    body.includes("Open grant") ||
    body.includes("Synthetic default namespace")
  )
    fail("grant table retained redundant actions or internal default language");
  if (body.includes("Immutable grants") || body.includes("Expired records"))
    fail("dedicated Grants page retained subordinate introductory copy");
  const grantCreate = page.locator('[data-testid="grant-create-link"]');
  if (
    (await grantCreate.getAttribute("href")) !== "#/grants/new" ||
    !(await grantCreate.evaluate((element) =>
      element.classList.contains("create-action"),
    )) ||
    Math.abs(
      (await grantCreate.boundingBox())!.x -
        (await page.locator('[data-testid="grants-view"]').boundingBox())!.x,
    ) > 1
  )
    fail("Create grant was not aligned with other create actions");
  await page.evaluate((id) => {
    window.location.hash = `#/access/grants/${id}`;
  }, firstGrantID);
  await page.locator('[data-testid="grant-detail"]').waitFor();
  const grantDetail = page.locator('[data-testid="grant-detail"]');
  const grantFactLabels = await grantDetail.locator("dt").allTextContents();
  if (
    !grantFactLabels.includes("Grant ID") ||
    grantFactLabels.includes("ID") ||
    (await grantDetail.getByText("Back to principal grants").count()) !== 0 ||
    (await grantDetail.locator('[data-testid="detail-context"] h1').count()) !==
      1 ||
    (await grantDetail
      .getByRole("heading", { level: 2, name: "Grant details", exact: true })
      .count()) !== 1 ||
    (await grantDetail.locator("section.panel h1").count()) !== 0 ||
    (await grantDetail.locator(".panel-value").count()) !== 0
  )
    fail("grant detail did not use the shared detail hierarchy");
  const descriptionEditor = page.locator("#grant-description-edit");
  const saveDescription = page.getByRole("button", {
    name: "Save description",
  });
  const saveDescriptionGap = await saveDescription.evaluate((button) => {
    const previous = button.previousElementSibling;
    if (previous === null) return -1;
    return (
      button.getBoundingClientRect().top -
      previous.getBoundingClientRect().bottom
    );
  });
  if (saveDescriptionGap < 16)
    fail(`grant description submit spacing collapsed: ${saveDescriptionGap}px`);
  await descriptionEditor.fill("Updated reporting access");
  await saveDescription.click();
  await page.waitForTimeout(100);
  await descriptionEditor.fill("");
  await saveDescription.click();
  await page.waitForTimeout(100);
  if (descriptionPatches !== 2)
    fail("grant description save and clear did not both reach the API");

  await page.evaluate((id) => {
    window.location.hash = `#/access/grants/${id}`;
  }, secondGrantID);
  await page
    .locator('[data-testid="grant-detail"]')
    .waitFor({ timeout: 5000 })
    .catch(async () =>
      fail(
        `grant detail did not load: ${JSON.stringify({ hash: await page.evaluate(() => window.location.hash), body: await page.locator("body").innerText() })}`,
      ),
    );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Exact tool dangerous.tool") || !body.includes("Expired"))
    fail("grant detail omitted scope or retained expiry state");

  await page.evaluate(
    ({ principal, server }) => {
      window.location.hash = `#/access/grants/new?principal_id=${principal}&server_id=${server}`;
    },
    { principal: principalID, server: serverID },
  );
  await page.locator('[data-testid="grant-create-view"]').waitFor();
  const grantCreateBody =
    (await page.locator('[data-testid="grant-create-view"]').textContent()) ??
    "";
  if (
    (await page
      .getByRole("heading", { level: 1, name: "Create grant", exact: true })
      .count()) !== 1 ||
    !grantCreateBody.includes("cannot be edited after creation") ||
    (
      await page.locator('[data-testid="grant-create-submit"]').textContent()
    )?.trim() !== "Review and create"
  )
    fail("grant creation omitted page or immutability guidance");
  const grantSubmitGap = await page
    .locator('[data-testid="grant-create-submit"]')
    .evaluate((button) => {
      const previous = button.previousElementSibling;
      if (previous === null) return -1;
      return (
        button.getBoundingClientRect().top -
        previous.getBoundingClientRect().bottom
      );
    });
  if (grantSubmitGap < 16)
    fail(`grant submit spacing collapsed: ${grantSubmitGap}px`);
  if (
    (await page
      .locator('[data-testid="grant-principal"]')
      .evaluate((node) => node.tagName)) !== "SELECT" ||
    (await page
      .locator('[data-testid="grant-server"]')
      .evaluate((node) => node.tagName)) !== "SELECT" ||
    !(
      await page.locator('[data-testid="grant-principal"]').innerText()
    ).includes("Automation agent") ||
    !(await page.locator('[data-testid="grant-server"]').innerText()).includes(
      "Reporting server",
    )
  )
    fail("grant creation did not use named principal and server selectors");
  await page
    .locator('[data-testid="grant-description"]')
    .fill("Unsafe\u0085access");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .getByText("Description cannot contain control characters.", {
      exact: true,
    })
    .waitFor();
  if (Number(attempts) !== 0)
    fail("control-character grant description reached the API");
  await page.locator('[data-testid="grant-description"]').fill("New access");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page
    .getByRole("heading", { name: "Review grant", exact: true })
    .waitFor();
  if (Number(attempts) !== 0)
    fail("grant creation submitted before final review");
  const grantReview =
    (await page.locator("#grant-create-confirm-consequence").textContent()) ??
    "";
  if (
    !grantReview.includes("New access") ||
    !grantReview.includes("Reporting server") ||
    !grantReview.includes("immutable")
  )
    fail("grant creation review omitted submitted values");
  await page.locator('[data-testid="grant-create-confirm-submit"]').click();
  await page.getByText("The grant is invalid.", { exact: true }).waitFor();
  if (attempts !== 1) fail("rejected grant creation was replayed");
  await page.locator('[data-testid="grant-create-submit"]').click();
  await page.locator('[data-testid="grant-create-confirm-submit"]').click();
  await page.locator('[data-testid="grant-detail"]').waitFor();

  await page.evaluate(
    ({ principal, server }) => {
      window.location.hash = `#/access/grants/new?principal_id=${principal}&server_id=${server}`;
    },
    { principal: principalID, server: serverID },
  );
  await page.locator('[data-testid="grant-create-view"]').waitFor();
  await page.locator('[data-testid="grant-description"]').fill("New access");
  await page.locator('[data-testid="grant-effect"]').selectOption("deny");
  await page.locator('[data-testid="grant-scope"]').selectOption("tool");
  await page.locator('[data-testid="grant-upstream"]').fill("literal.tool");
  await page.getByRole("link", { name: "Servers", exact: true }).click();
  await page.locator('[data-testid="unsaved-changes-cancel"]').waitFor();
  await page.locator('[data-testid="unsaved-changes-cancel"]').click();
  await page
    .locator('dialog[aria-labelledby="unsaved-changes-title"]')
    .waitFor({ state: "hidden" });
  if (
    (await page.locator('[data-testid="grant-upstream"]').inputValue()) !==
    "literal.tool"
  )
    fail("grant creation did not preserve a cancelled dirty draft");
  await page
    .locator('[data-testid="grant-expiry"]')
    .fill("2030-01-01T00:00:00Z");
  if (
    (await page.locator('[data-testid="grant-expiry"]').inputValue()) !==
    "2030-01-01T00:00:00Z"
  )
    fail("grant expiry input did not retain its draft value");
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
  await page.locator('[data-testid="add-constraint-atom"]').click();
  await page
    .locator('[data-testid="constraint-operator"]')
    .nth(4)
    .selectOption("regex");
  await page
    .locator('[data-testid="constraint-pointer"]')
    .nth(4)
    .fill("/resource");
  await page
    .locator('[data-testid="constraint-value"]')
    .nth(3)
    .fill("item-\\d+");
  if (
    (await page.locator('[data-testid="grant-expiry"]').inputValue()) !==
    "2030-01-01T00:00:00Z"
  )
    fail("grant constraint edits discarded the expiry draft");
  await page.locator('[data-testid="grant-create-submit"]').click();
  const matcherReview =
    (await page.locator("#grant-create-confirm-consequence").textContent()) ??
    "";
  if (!matcherReview.includes("4 equality · 1 regex"))
    fail("grant review omitted matcher operator counts");
  await page.locator('[data-testid="grant-create-confirm-submit"]').click();
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
    description: target === zero ? "Default Gateway access" : `Grant ${id}`,
    revision: "1",
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

  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: principalIDs.map((id, index) => ({
          id,
          display_name: `Agent ${index + 1}`,
          state: "active",
          visibility:
            index === 7 ? "allowed-only" : index === 8 ? "requestable" : "all",
          revision: "1",
          credential_revision: "0",
          credential: null,
          created_at: "2026-08-28T12:00:00Z",
          updated_at: "2026-08-28T12:00:00Z",
        })),
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v1/servers?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          serverReadFixture(serverID, {
            name: "Correction server",
            desired: "enabled",
            runtime: "active",
            credential: "ready",
            durable: "current",
            active: "current",
          }),
        ],
        next_cursor: null,
      }),
    });
  });

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
    if (
      body.description !== null &&
      (typeof body.description !== "string" || body.description.length === 0)
    )
      fail("grant replacement sent an invalid description");
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
    item.description = body.description as string;
    grants.set(replacementID, item);
    replacements.set(principalID, replacementID);
    await route.fulfill({
      status: 201,
      contentType: "application/json",
      body: JSON.stringify(item),
    });
  });

  const navigate = async (grantID: string) => {
    if (!grants.has(grantID)) fail(`missing grant fixture ${grantID}`);
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
  await page.waitForFunction(() => /\/grants\/.*H/.test(window.location.hash));
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
  await page.waitForFunction(() => /\/grants\/.*H/.test(window.location.hash));
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
    [
      grantIDs[7]!,
      "removes the principal's access to Gateway self-service tools",
    ],
    [
      grantIDs[8]!,
      "removes the principal's access to Gateway self-service tools",
    ],
    [
      grantIDs[9]!,
      "removes the principal's access to Gateway self-service tools",
    ],
  ];
  for (const [grantID, phrase] of defaultWarnings) {
    await navigate(grantID);
    await page.getByText(new RegExp(phrase)).waitFor();
    if (
      (await page.locator('[data-testid="grant-correct"]').isDisabled()) !==
      true
    )
      fail("default Gateway grant exposed unsupported replacement");
  }

  await navigate(grantIDs[5]!);
  await page
    .getByText("Expired grant still consumes capacity", { exact: true })
    .waitFor();
  if (!(await page.locator('[data-testid="grant-correct"]').isDisabled()))
    fail("expiring grant exposed a permanent replacement workflow");
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

async function runRequestReads(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const toolID = "01ARZ3NDEKTSV4RRFFQ69G5FAC";
  const requestIDs = [
    "01ARZ3NDEKTSV4RRFFQ69G5FB0",
    "01ARZ3NDEKTSV4RRFFQ69G5FB1",
    "01ARZ3NDEKTSV4RRFFQ69G5FB2",
  ];
  const grantID = "01ARZ3NDEKTSV4RRFFQ69G5FB9";
  const policy = (target: string, duration: string | null = "600") => ({
    scope: "tool",
    target,
    constraint: { equals: { "/mode": "safe" } },
    duration_seconds: duration,
    future_tools_acknowledged: false,
  });
  const summary = (
    id: string,
    state: "pending" | "approved" | "rejected" | "cancelled",
  ) => ({
    id,
    principal_id: principalID,
    state,
    revision: state === "pending" ? "1" : "2",
    requested_policy: policy("demo.safe"),
    approved_policy: state === "approved" ? policy("demo.safe", "300") : null,
    approved_grant_id: state === "approved" ? grantID : null,
    rejection_reason: state === "rejected" ? "not_approved" : null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
    closed_at: state === "pending" ? null : "2026-08-28T13:00:00Z",
  });
  const descriptor = (name: string) => ({
    name,
    description: "EVIDENCE-CANARY immutable descriptor",
    inputSchema: { type: "object" },
    annotations: {
      title: name,
      readOnlyHint: true,
      destructiveHint: false,
      idempotentHint: true,
      openWorldHint: false,
    },
  });
  const evidence = (durable: "current" | "retired", fingerprint: string) => ({
    server_id: serverID,
    tool_id: toolID,
    namespace: "demo",
    upstream_name: "safe",
    external_name: "demo.safe",
    catalog_revision: durable === "current" ? "8" : "7",
    fingerprint,
    durable_state: durable,
    descriptor: descriptor(`submitted-${durable}`),
    captured_at: "2026-08-28T12:00:00Z",
  });
  const details = new Map<string, Record<string, unknown>>([
    [
      requestIDs[0]!,
      {
        ...summary(requestIDs[0]!, "pending"),
        resolved_server_id: serverID,
        resolved_upstream_name: "safe",
        submitted_evidence: evidence("current", "submitted-fingerprint"),
        approved_evidence: null,
        current_target: {
          scope: "tool",
          target_state: "extant",
          active_state: "current",
          durable_state: "current",
          catalog_revision: "9",
          fingerprint: "current-drifted-fingerprint",
          descriptor: descriptor("current-drifted"),
        },
      },
    ],
    [
      requestIDs[1]!,
      {
        ...summary(requestIDs[1]!, "approved"),
        resolved_server_id: serverID,
        resolved_upstream_name: "safe",
        submitted_evidence: evidence("current", "submitted-fingerprint"),
        approved_evidence: evidence("retired", "approved-retired-fingerprint"),
        current_target: {
          scope: "tool",
          target_state: "deleted",
          active_state: "absent",
          durable_state: "retired",
          catalog_revision: "7",
          fingerprint: "approved-retired-fingerprint",
          descriptor: descriptor("retired-current-comparison"),
        },
      },
    ],
    [
      requestIDs[2]!,
      {
        ...summary(requestIDs[2]!, "pending"),
        resolved_server_id: serverID,
        resolved_upstream_name: "safe",
        submitted_evidence: null,
        approved_evidence: null,
        current_target: {
          scope: "tool",
          target_state: "extant",
          active_state: "absent",
          durable_state: "absent",
          catalog_revision: null,
          fingerprint: null,
          descriptor: null,
        },
      },
    ],
  ]);
  let staleRestarted = false;
  let listReads = 0;
  let detailReads = 0;
  let principalReads = 0;
  let releaseInvalidation: (() => void) | undefined;
  const invalidationReady = new Promise<void>((resolve) => {
    releaseInvalidation = resolve;
  });
  await page.route(
    `${baseURL}/api/v1/events`,
    async (route) => {
      await invalidationReady;
      await route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: `event: invalidate\ndata: ${JSON.stringify({ kind: "grant_requests", resource_id: requestIDs[0] })}\n\n`,
      });
    },
    { times: 1 },
  );
  await page.route(
    `${baseURL}/api/v1/events`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "text/event-stream",
        body: ": reconnect before request invalidation\n\n",
      }),
    { times: 1 },
  );
  await page.route("**/api/v1/grant-requests?*", async (route) => {
    listReads += 1;
    const query = new URL(route.request().url()).searchParams;
    if (
      route.request().method() !== "GET" ||
      query.get("limit") !== "50" ||
      [...query.keys()].some((key) => key !== "limit" && key !== "cursor")
    )
      fail("request queue filters changed shape");
    const cursor = query.get("cursor");
    if (cursor === "request-stale") {
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
        cursor === "request-next"
          ? {
              items: [summary(requestIDs[2]!, "cancelled")],
              next_cursor: null,
            }
          : {
              items: [
                summary(requestIDs[0]!, "pending"),
                ...(staleRestarted
                  ? [summary(requestIDs[1]!, "approved")]
                  : []),
              ],
              next_cursor: staleRestarted ? "request-next" : "request-stale",
            },
      ),
    });
  });
  await page.route("**/api/v1/principals?*", async (route) => {
    principalReads += 1;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: principalID,
            display_name: "Requesting agent",
            state: "active",
            visibility: "all",
            revision: "1",
            credential_revision: "0",
            credential: null,
            created_at: "2026-08-28T12:00:00Z",
            updated_at: "2026-08-28T12:00:00Z",
          },
        ],
        next_cursor: null,
      }),
    });
  });
  await page.route("**/api/v1/grant-requests/*", async (route) => {
    detailReads += 1;
    const id = new URL(route.request().url()).pathname.split("/").pop()!;
    const item = details.get(id);
    if (route.request().method() !== "GET" || item === undefined)
      fail("request detail changed shape");
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"grant-request-${id}-${String(item.revision)}"` },
      body: JSON.stringify(item),
    });
  });

  await page.evaluate(() => {
    window.location.hash = "#/requests";
  });
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="request-row"]').length === 1,
  );
  const loadOlderRequests = page.getByRole("button", {
    name: "Load older requests",
  });
  await loadOlderRequests.click();
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="request-row"]').length === 2,
  );
  await loadOlderRequests.click();
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="request-row"]').length === 3,
  );
  await page
    .getByText("Requesting agent", { exact: true })
    .first()
    .waitFor({ timeout: 5000 })
    .catch(async () =>
      fail(
        `request principal directory did not publish (${principalReads} reads): ${(await page.locator("body").textContent()) ?? ""}`,
      ),
    );
  let body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes("summary-only") ||
    body.includes("Pending filter") ||
    (await page.locator('[data-testid="requests-view"] h2').count()) !== 0 ||
    (await page
      .locator('[data-testid="requests-view"] .panel-code')
      .count()) !== 0
  )
    fail("request queue retained duplicate introductory presentation");
  const requestHeaders = await page.locator("thead th").allTextContents();
  if (
    requestHeaders.map((header) => header.replace(/[↕↑↓]/g, "")).join("|") !==
    "Request ID|Principal|Requested target|State|Submitted"
  )
    fail(`request table columns changed: ${requestHeaders.join("|")}`);
  if (
    !body.includes("Requesting agent") ||
    !body.includes("Showing 3 of 3") ||
    body.includes("EVIDENCE-CANARY") ||
    detailReads !== 0
  )
    fail("request collection omitted shared metadata or expanded evidence");
  const stateFilter = page.getByLabel("State", { exact: true });
  await stateFilter.selectOption("approved");
  if (
    (await page.locator('[data-testid="request-row"]').count()) !== 1 ||
    !(await page.evaluate(() => window.location.hash)).includes(
      "filter_state=approved",
    )
  )
    fail("request state filter was not URL-backed");
  await page.getByRole("button", { name: "Reset" }).click();
  if ((await page.locator('[data-testid="request-row"]').count()) !== 3)
    fail("request filter Reset did not restore rows");

  const liveSwitch = page.getByRole("switch", { name: "Live mode" });
  if ((await liveSwitch.count()) !== 1 || !(await liveSwitch.isChecked()))
    fail("request live mode was not enabled by default");
  if (body.includes("Live updates on") || body.includes("Live updates paused"))
    fail("request live mode retained redundant state text");
  const beforeIdle = listReads;
  await page.waitForTimeout(5100);
  if (listReads !== beforeIdle)
    fail("request live mode polled instead of waiting for invalidations");
  await liveSwitch.uncheck();
  const beforePausedRefresh = listReads;
  releaseInvalidation?.();
  await page.getByText("Updates available", { exact: true }).waitFor();
  if (
    listReads !== beforePausedRefresh ||
    (await page.locator('[data-testid="request-row"]').count()) !== 3
  )
    fail("paused request collection replaced its stable rows");
  await liveSwitch.check();
  await page.waitForFunction(
    () =>
      !document.body.textContent?.includes("Updates available") &&
      document.querySelectorAll('[data-testid="request-row"]').length === 2,
  );
  if (listReads <= beforePausedRefresh)
    fail("resuming request live mode did not perform one authoritative read");

  const navigate = async (id: string) => {
    await page.evaluate((requestID) => {
      window.location.hash = `#/requests/${requestID}`;
    }, id);
    await page
      .getByRole("heading", { name: `Request ${id}`, exact: true })
      .waitFor();
    const requestDetail = page.locator('[data-testid="request-detail"]');
    if (
      (await requestDetail
        .locator('[data-testid="detail-context"] h1')
        .count()) !== 1 ||
      (await requestDetail
        .getByRole("heading", {
          level: 2,
          name: "Request details",
          exact: true,
        })
        .count()) !== 1 ||
      (await requestDetail.locator("section.panel h1").count()) !== 0
    )
      fail("request detail did not use the shared detail hierarchy");
  };
  await navigate(requestIDs[0]!);
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Submitted policy and evidence — immutable",
    "Current target comparison — read-time",
    "Proposed approved policy",
    "Descriptor fingerprint changed",
    "never executes or resumes the motivating call",
    "explicit fresh call is required",
  ])
    if (!body.includes(phrase)) fail(`request detail omitted ${phrase}`);
  if (
    (await page.locator(`a[href="#/principals/${principalID}"]`).count()) ===
      0 ||
    (await page
      .locator(`a[href="#/servers/${serverID}?tab=tools"]`)
      .count()) === 0 ||
    (await page
      .locator(`a[href="#/servers/${serverID}/descriptors/${toolID}"]`)
      .count()) === 0
  )
    fail("request detail omitted reciprocal navigation");

  await navigate(requestIDs[1]!);
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "retired historical evidence",
    "Retained evidence is not callable authority",
    `Grant ${grantID}`,
    "does not prove the grant still exists",
  ])
    if (!body.includes(phrase))
      fail(`approved request history omitted ${phrase}`);

  await navigate(requestIDs[2]!);
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("Current descriptor is absent") ||
    !body.includes("no descriptor evidence")
  )
    fail("absent request target omitted explicit evidence state");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "request_reads_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), list_reads: listReads, detail_reads: detailReads, destinations: 4 })}\n`,
  );
}

async function runRequestAdjudication(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const principalID = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
  const serverID = "01ARZ3NDEKTSV4RRFFQ69G5FAB";
  const ids = Array.from(
    { length: 10 },
    (_, index) => `01ARZ3NDEKTSV4RRFFQ69J${String(index).padStart(4, "0")}`,
  );
  const policy = (
    scope: "tool" | "server",
    target: string,
    constraint: unknown | null,
    duration: string | null,
  ) => ({
    scope,
    target,
    constraint,
    duration_seconds: duration,
    future_tools_acknowledged: scope === "server",
  });
  const states = new Map<string, Record<string, unknown>>();
  const detail = (
    id: string,
    requested: ReturnType<typeof policy>,
    state: "pending" | "approved" | "rejected" = "pending",
    approved: ReturnType<typeof policy> | null = null,
    reason: string | null = null,
  ) => ({
    id,
    principal_id: principalID,
    state,
    revision: state === "pending" ? "1" : "2",
    requested_policy: requested,
    approved_policy: approved,
    approved_grant_id:
      state === "approved" ? "01ARZ3NDEKTSV4RRFFQ69G5FB9" : null,
    rejection_reason: reason,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T13:00:00Z",
    closed_at: state === "pending" ? null : "2026-08-28T13:00:00Z",
    resolved_server_id: serverID,
    resolved_upstream_name: requested.scope === "tool" ? "safe" : null,
    submitted_evidence: null,
    approved_evidence: null,
    current_target: {
      scope: requested.scope,
      target_state: "extant",
      active_state: requested.scope === "tool" ? "current" : null,
      durable_state: requested.scope === "tool" ? "current" : null,
      catalog_revision: requested.scope === "tool" ? "1" : null,
      fingerprint: requested.scope === "tool" ? "fingerprint" : null,
      descriptor:
        requested.scope === "tool"
          ? { name: "safe", inputSchema: {}, annotations: {} }
          : null,
    },
  });
  states.set(ids[0]!, detail(ids[0]!, policy("server", "demo", null, "1200")));
  states.set(
    ids[1]!,
    detail(
      ids[1]!,
      policy("tool", "demo.safe", { equals: { "/mode": "safe" } }, "600"),
    ),
  );
  for (let index = 2; index <= 5; index++)
    states.set(
      ids[index]!,
      detail(ids[index]!, policy("tool", "demo.safe", null, null)),
    );
  states.set(ids[6]!, detail(ids[6]!, policy("tool", "demo.safe", null, null)));
  states.set(ids[7]!, detail(ids[7]!, policy("tool", "demo.safe", null, null)));
  states.set(ids[8]!, detail(ids[8]!, policy("tool", "demo.safe", null, null)));
  states.set(ids[9]!, detail(ids[9]!, policy("server", "demo", null, null)));
  let approvals = 0;
  let rejections = 0;
  const attempts = new Map<string, number>();

  await page.route("**/api/v1/grant-requests/**", async (route) => {
    const parts = new URL(route.request().url()).pathname.split("/");
    const action = parts.at(-1)!;
    const id =
      action === "approve" || action === "reject" ? parts.at(-2)! : action;
    const item = states.get(id);
    if (item === undefined) fail("unknown adjudication fixture");
    if (route.request().method() === "GET") {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: { ETag: `"grant-request-${id}-${String(item.revision)}"` },
        body: JSON.stringify(item),
      });
      return;
    }
    attempts.set(id, (attempts.get(id) ?? 0) + 1);
    const headers = await route.request().allHeaders();
    if (
      route.request().method() !== "POST" ||
      headers["if-match"] !== `"grant-request-${id}-${String(item.revision)}"`
    )
      fail("request adjudication precondition changed shape");
    if (id === ids[6]) {
      states.set(
        id,
        detail(
          id,
          item.requested_policy as ReturnType<typeof policy>,
          "approved",
          item.requested_policy as ReturnType<typeof policy>,
        ),
      );
      await route.fulfill({
        status: 412,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 412,
          code: "stale_grant_request_revision",
          title: "The request revision is stale.",
        }),
      });
      return;
    }
    if (id === ids[7]) {
      await route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 400,
          code: "invalid_grant_request",
          title: "The request adjudication is invalid.",
        }),
      });
      return;
    }
    if (id === ids[8]) {
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
    const submitted = item.requested_policy as ReturnType<typeof policy>;
    if (action === "approve") {
      approvals += 1;
      const body = JSON.parse(route.request().postData() ?? "null") as Record<
        string,
        unknown
      >;
      if (
        Object.keys(body).join(",") !== "description,approved_policy" ||
        (body.description !== null && typeof body.description !== "string")
      )
        fail("approval body changed shape");
      const approved = body.approved_policy as ReturnType<typeof policy>;
      states.set(id, detail(id, submitted, "approved", approved));
    } else {
      rejections += 1;
      const body = JSON.parse(route.request().postData() ?? "null") as Record<
        string,
        unknown
      >;
      states.set(
        id,
        detail(id, submitted, "rejected", null, body.reason as string),
      );
    }
    const result = states.get(id)!;
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"grant-request-${id}-2"` },
      body: JSON.stringify(result),
    });
  });

  const navigate = async (id: string) => {
    await page.evaluate((requestID) => {
      window.location.hash = `#/requests/${requestID}`;
    }, id);
    await page
      .getByRole("heading", { name: `Request ${id}`, exact: true })
      .waitFor();
    await page.locator('[data-testid="request-actions"]').waitFor();
  };
  const confirm = async () => {
    await page
      .locator('[data-testid="request-adjudication-confirm-submit"]')
      .click();
  };
  const reviewApproval = async () => {
    await page.locator('[data-testid="request-approve"]').click();
  };

  await page.evaluate((id) => {
    window.location.hash = `#/requests/${id}`;
  }, ids[0]);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await navigate(ids[0]!);
  await page
    .locator('[data-testid="approval-description"]')
    .fill("Unsafe\u0085access");
  await reviewApproval();
  await page
    .getByText("Grant description cannot contain control characters.", {
      exact: true,
    })
    .waitFor();
  if ((attempts.get(ids[0]!) ?? 0) !== 0)
    fail("control-character approval description reached the API");
  await page
    .locator('[data-testid="approval-description"]')
    .fill("Access to demo.safe");
  await page.locator('[data-testid="approval-scope"]').selectOption("tool");
  await page.locator('[data-testid="approval-target"]').fill("demo.safe");
  await page
    .locator('[data-testid="approval-constraint"]')
    .fill('{"equals":{"/mode":"safe"}}');
  await page.locator('[data-testid="approval-duration"]').fill("600");
  await reviewApproval();
  await confirm();
  try {
    await page
      .getByText("Request adjudication is closed", { exact: true })
      .waitFor({ timeout: 3000 });
  } catch {
    fail(
      `first approval did not close: ${((await page.locator("body").textContent()) ?? "").slice(-1200)}`,
    );
  }

  await navigate(ids[1]!);
  await page.locator('[data-testid="approval-duration"]').fill("601");
  await reviewApproval();
  await page
    .getByText("Approval cannot extend the submitted duration.", {
      exact: true,
    })
    .waitFor();
  await page.locator('[data-testid="approval-duration"]').fill("");
  await reviewApproval();
  await page
    .getByText("A temporary request cannot become permanent.", { exact: true })
    .waitFor();
  await page.locator('[data-testid="approval-duration"]').fill("300");
  await page.locator('[data-testid="approval-constraint"]').fill("");
  await reviewApproval();
  await page
    .getByText(
      "Approval cannot remove or change a submitted constraint atom.",
      { exact: true },
    )
    .waitFor();
  await page
    .locator('[data-testid="approval-constraint"]')
    .fill('{"equals":{"/mode":"safe","/region":"local"}}');
  await reviewApproval();
  await confirm();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();

  const reasons = [
    "not_approved",
    "existing_access",
    "scope_too_broad",
    "policy_conflict",
  ];
  for (let index = 0; index < reasons.length; index++) {
    await navigate(ids[index + 2]!);
    await page
      .locator('[data-testid="rejection-reason"]')
      .selectOption(reasons[index]!);
    await page.locator('[data-testid="request-reject"]').click();
    await confirm();
    await page
      .getByText("Request adjudication is closed", { exact: true })
      .waitFor();
  }

  await navigate(ids[9]!);
  await page.locator('[data-testid="approval-duration"]').fill("60");
  await reviewApproval();
  await confirm();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();

  await navigate(ids[6]!);
  await reviewApproval();
  await confirm();
  await page
    .getByText("Request adjudication is closed", { exact: true })
    .waitFor();
  if ((attempts.get(ids[6]!) ?? 0) !== 1) fail("stale adjudication replayed");

  await navigate(ids[7]!);
  await page.locator('[data-testid="request-reject"]').click();
  await confirm();
  await page
    .getByText("The request adjudication is invalid.", { exact: true })
    .waitFor();
  if ((attempts.get(ids[7]!) ?? 0) !== 1) fail("known failure replayed");

  await navigate(ids[8]!);
  await reviewApproval();
  await confirm();
  await page
    .getByText("Adjudication outcome is unknown", { exact: true })
    .waitFor();
  if ((attempts.get(ids[8]!) ?? 0) !== 1)
    fail("uncertain adjudication replayed");
  await assertSecretAbsent(page, context, baseURL, [bearer], true);
  process.stdout.write(
    `${JSON.stringify({ event: "request_adjudication_complete", chromium_version: browserVersion, playwright_version: "1.62.1", requests: requestCount(), approvals, rejections, destinations: ids.length })}\n`,
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
      query.get("limit") !== "5" ||
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
  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: overviewRequestFixture().principal_id,
            display_name: "Overview agent",
            state: "active",
            visibility: "all",
            revision: "1",
            credential_revision: "0",
            credential: null,
            created_at: "2026-08-28T00:00:00Z",
            updated_at: "2026-08-28T00:00:00Z",
          },
        ],
        next_cursor: null,
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
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="overview-server-row"]')
        .length === 1,
  );
  const body = (await page.locator("body").textContent()) ?? "";
  for (const text of [
    "Use the documented stopped-process recovery procedure",
    "Storage mutation is closed",
    "Keyring Unavailable",
    "Capacity saturated",
    "80% capacity pressure",
    "Needs operator attention",
    "Overview agent",
    "count incomplete",
    "Missing terminal evidence",
  ]) {
    if (!body.includes(text)) fail(`overview omitted ${text}`);
  }
  const serverAttentionText =
    (await page.locator('[data-testid="overview-servers"]').textContent()) ??
    "";
  if (
    serverAttentionText.includes("literal-<script>") ||
    serverAttentionText.includes("Deleted server history")
  )
    fail("overview server attention included healthy or deleted servers");
  if (body.includes("Inspect System for stopped recovery guidance"))
    fail("overview pointed to removed recovery UI");
  if (
    !(
      await page
        .locator('[data-testid="overview-servers"] h3')
        .allTextContents()
    ).some((text) => /\d+ active tools?/.test(text)) ||
    !(
      await page
        .locator('[data-testid="overview-servers"] h3')
        .allTextContents()
    ).some((text) => /\d+ configured servers?/.test(text))
  )
    fail("overview counts were not self-describing");
  const invocationFieldLabels = await page
    .locator('[data-testid="overview-invocations"] .compact-record-fields dt')
    .allTextContents();
  if (invocationFieldLabels.join("|") !== "Status|Outcome|Admitted")
    fail("overview invocation fields were not structured");
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
    "#/system",
    "#/system?tab=resource-limits",
    "#/servers",
    "#/servers/01ARZ3NDEKTSV4RRFFQ69G5FA1?tab=status",
    "#/requests/01ARZ3NDEKTSV4RRFFQ69G5FAV",
    "#/requests?filter_state=pending",
    "#/invocations/01ARZ3NDEKTSV4RRFFQ69G5FAX",
    "#/invocations",
  ]) {
    if (
      (await page
        .locator(`[data-testid="overview-grid"] a[href="${href}"]`)
        .count()) !== 1
    )
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
  await eventually(
    () => releaseHeldStatus !== undefined,
    "held overview refresh did not start",
  );
  if (
    (await page
      .locator('[data-testid="overview-status"]')
      .getAttribute("data-panel-status")) !== "current" ||
    ((await page.locator("body").textContent()) ?? "").includes("Data stale")
  )
    fail("overview refresh flashed stale feedback");
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
  let argumentCapture: unknown = {
    note: captureCanary,
    token: "[REDACTED]",
  };
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
            redacted_arguments: argumentCapture,
          }),
        });
      }
      return;
    }
    const query = url.searchParams;
    const allowed = new Set(["limit", "cursor"]);
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

  await page.route("**/api/v1/principals?*", async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: [
          {
            id: invocationIDs.principal,
            display_name: "Build agent",
            state: "active",
            visibility: "all",
            revision: "1",
            credential_revision: "1",
            credential: null,
            created_at: "2026-08-28T12:00:00Z",
            updated_at: "2026-08-28T12:00:00Z",
          },
        ],
        next_cursor: null,
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
    "Invocation",
    "Tool",
    "Principal",
    "Decision",
    "Outcome",
    "Admitted",
  ])
    if (!body.includes(phrase)) fail(`invocation list omitted ${phrase}`);
  for (const phrase of [
    "Invocation evidence",
    "retains at most 4,096 recent rows",
    "Filtered pages are independently coherent",
    "gateway:get_identity",
  ])
    if (body.includes(phrase)) fail(`invocation list retained ${phrase}`);
  const liveSwitch = page.getByRole("switch", { name: "Live mode" });
  if ((await liveSwitch.count()) !== 1 || !(await liveSwitch.isChecked()))
    fail("invocation live mode was not enabled by default");
  if (
    (await liveSwitch.evaluate((element) => {
      const style = getComputedStyle(element);
      return style.position !== "absolute" || style.opacity !== "0";
    })) ||
    !body.includes("Build agent")
  )
    fail("invocation live control or principal label was not normalized");
  const outcomeOptions = await page
    .getByLabel("Outcome", { exact: true })
    .locator("option")
    .allTextContents();
  for (const outcome of [
    "Invalid arguments",
    "Authorization unavailable",
    "Prestart failure",
  ])
    if (!outcomeOptions.includes(outcome))
      fail(`invocation outcome filter omitted ${outcome}`);
  if ((await page.locator('[data-testid="invocation-row"] time').count()) !== 2)
    fail("invocation list did not render admitted timestamps in user time");
  if (
    body.includes(captureCanary) ||
    body.includes("redacted_arguments") ||
    body.includes("Recorded principal") ||
    body.includes("Recorded credential")
  )
    fail("invocation collection exposed item capture or internal identities");

  const beforeWait = listReads;
  await page.waitForTimeout(5100);
  if (listReads !== beforeWait)
    fail("invocation live mode retained polling instead of event updates");
  await liveSwitch.uncheck();
  const pausedBody = (await page.locator("body").textContent()) ?? "";
  if (
    (await liveSwitch.isChecked()) ||
    pausedBody.includes("Live updates on") ||
    pausedBody.includes("Live updates paused")
  )
    fail("invocation live mode did not expose state through the switch alone");
  const beforeContinuation = continuationReads;
  const loadOlder = page.getByRole("button", {
    name: "Load older invocations",
  });
  await loadOlder.evaluate((element) => {
    const button = element as HTMLButtonElement;
    button.click();
    button.click();
  });
  await page
    .waitForFunction(
      () =>
        document.querySelectorAll('[data-testid="invocation-row"]').length ===
        5,
    )
    .catch(() => fail("invocation continuation did not render five rows"));
  if (continuationReads !== beforeContinuation + 1)
    fail("invocation continuation was duplicated or discarded while paused");
  await liveSwitch.check();
  body = (await page.locator("body").textContent()) ?? "";
  if (body.includes("missing_terminal") || body.includes("basis"))
    fail("invocation collection exposed internal outcome semantics");

  const toolFilter = page.getByLabel("Tool", { exact: true });
  await toolFilter.fill("namespace.allowed");
  await page
    .waitForFunction(
      () =>
        document.querySelectorAll('[data-testid="invocation-row"]').length ===
        1,
    )
    .catch(() => fail("invocation tool filter did not narrow rows"));
  if (
    !(await page.evaluate(() => window.location.hash)).includes(
      "filter_tool=namespace.allowed",
    )
  )
    fail("invocation tool filter was not persisted in the URL");
  await page.getByRole("button", { name: "Reset" }).click();
  if ((await toolFilter.inputValue()) !== "")
    fail("invocation filter Reset did not clear the field");
  const storage = await browserStorage(page);
  if (JSON.stringify(storage).includes("namespace.allowed"))
    fail("invocation filter entered browser storage");

  staleMode = true;
  staleRestarted = false;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByRole("button", { name: "Load older invocations" }).waitFor();
  await page.getByRole("button", { name: "Load older invocations" }).click();
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
    `Invocation ${invocationIDs.missing}`,
    "Gateway-owned local target",
    "not proof of downstream handoff",
    "does not automatically replay",
    "explicit caller retry can duplicate an effect",
    "Build agent",
    "Authorization decision",
  ])
    if (!body.includes(phrase)) fail(`invocation detail omitted ${phrase}`);
  if (
    (await page
      .locator('[data-testid="invocation-detail"] .panel-value')
      .count()) !== 0 ||
    (await page
      .locator(
        '[data-testid="invocation-detail"] [data-testid="detail-context"] h1',
      )
      .count()) !== 1 ||
    (await page
      .locator('[data-testid="invocation-detail"]')
      .getByRole("heading", {
        level: 2,
        name: "Invocation details",
        exact: true,
      })
      .count()) !== 1 ||
    (await page
      .locator('[data-testid="invocation-detail"] section.panel h1')
      .count()) !== 0 ||
    (await page
      .locator('[data-testid="invocation-detail"] .fact-grid')
      .count()) !== 1 ||
    (
      await page
        .locator('[data-testid="invocation-detail"] dt')
        .allTextContents()
    ).filter((label) => label === "Invocation ID").length !== 1 ||
    (
      await page
        .locator('[data-testid="invocation-detail"] dt')
        .allTextContents()
    ).includes("ID") ||
    (await page
      .locator(
        `[data-testid="invocation-detail"] a[href="#/principals/${invocationIDs.principal}"]`,
      )
      .count()) !== 1 ||
    (await page
      .locator(
        `[data-testid="invocation-detail"] a[href="#/grants/${invocationIDs.grant}"]`,
      )
      .count()) !== 1
  )
    fail("invocation detail did not use linked resource facts");
  if (
    !body.includes(captureCanary) ||
    !body.includes("Retained argument capture") ||
    !body.includes("Other secrets may remain visible") ||
    body.includes("Fixed-redacted arguments") ||
    (await page.locator("script").count()) !== 1 ||
    (await page.evaluate(
      () =>
        (window as unknown as { __invocation_capture__?: boolean })
          .__invocation_capture__ === true,
    ))
  )
    fail("invocation capture was not explained inert item-only content");

  argumentCapture = "[TRUNCATED]";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page
    .getByText(
      "The redacted capture exceeded 8 KiB; argument content was not retained.",
      { exact: true },
    )
    .waitFor();
  if (
    ((await page.locator("body").textContent()) ?? "").includes(captureCanary)
  )
    fail("truncated invocation retained prior argument content");

  argumentCapture = null;
  await page.locator('[data-testid="manual-refresh"]').click();
  await page
    .getByText("No argument capture was retained.", { exact: true })
    .waitFor();

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
  let currentStatus = overviewStatusFixture();
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
      body: JSON.stringify(currentStatus),
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
    .locator('[data-testid="system-status-operational"]')
    .waitFor({ timeout: 5000 })
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
  const systemTabs = await page
    .locator('nav[aria-label="System sections"] a')
    .allTextContents();
  if (
    systemTabs.join("|") !== "Status|Resource limits|Admin credentials|Backups"
  )
    fail(`System tabs were not task-oriented: ${systemTabs.join("|")}`);
  for (const phrase of [
    "Degraded",
    "Needs attention",
    "Gateway is not ready",
    "Storage mutations are unavailable",
    "Credential storage is unavailable",
    "1 resource limit is saturated",
    "Operational state",
    "Technical details",
    "2026-07-28",
    "Principal credentials",
  ])
    if (!body.includes(phrase)) fail(`System status omitted ${phrase}`);
  const statusPanel = page.locator('[data-testid="system-status-panel"]');
  if (
    !(await statusPanel.getAttribute("class"))
      ?.split(/\s+/)
      .includes("panel") ||
    (await statusPanel
      .locator('[data-testid="system-status-summary"]')
      .count()) !== 0 ||
    (await statusPanel
      .locator('[data-testid="system-status-issues"]')
      .count()) !== 1 ||
    (await statusPanel
      .locator('[data-testid="system-status-operational"]')
      .count()) !== 1 ||
    (await statusPanel
      .locator('section[data-testid="system-status-details"]')
      .count()) !== 1 ||
    body.includes("SYSTEM-01")
  )
    fail("System status did not use the shared operator hierarchy");
  if (body.includes("Stopped recovery"))
    fail("System retained the documentation-only recovery tab");
  const processStart = page.locator('time[datetime="2026-08-28T00:00:00Z"]');
  if (
    (await processStart.count()) !== 1 ||
    (await processStart.textContent()) === "2026-08-28T00:00:00Z"
  )
    fail("System status did not render process start in user time");
  if (
    (await page.locator('[data-testid="system-limit-row"]').count()) !== 0 ||
    (await page.locator('a[href="#/system?tab=resource-limits"]').count()) < 1
  )
    fail("System status did not defer detailed limits to their tab");
  if (
    (await page
      .locator('[data-testid="gateway-shell"]')
      .getAttribute("data-mutation-availability")) !== "storage_latched"
  )
    fail("System did not close mutation admission for latched storage");

  currentStatus = {
    ...currentStatus,
    process: { ...currentStatus.process, state: "ready", ready: true },
    sqlite: { ...currentStatus.sqlite, state: "ready", latched: false },
    keyring: { capability: "ready" },
    limits: Object.fromEntries(
      Object.entries(currentStatus.limits).map(([name, limit]) => [
        name,
        { ...limit, in_use: 0, saturated: false },
      ]),
    ),
  };
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByText("Healthy", { exact: true }).waitFor();
  body = (await statusPanel.textContent()) ?? "";
  if (
    (body.match(/No current issues require operator action\./g) ?? [])
      .length !== 1 ||
    body.includes("No action required") ||
    body.includes("Gateway is operating normally")
  )
    fail("System healthy status repeated its conclusion");

  holdStatus = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await eventually(
    () => releaseStatus !== undefined,
    "System refresh did not start",
  );
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (
    body.includes("Data stale") ||
    !body.includes("Technical details") ||
    !body.includes("2026-07-28")
  )
    fail("System refresh flashed stale text or discarded current status");
  holdStatus = false;
  releaseStatus?.();
  await page.waitForFunction(
    () =>
      document
        .querySelector('[data-testid="system-status-panel"]')
        ?.getAttribute("data-panel-status") === "current",
  );

  await page.evaluate(() => {
    window.location.hash = "#/system?tab=resource-limits";
  });
  await page.locator('[data-testid="system-limits-view"]').waitFor();
  const limitRows = await page
    .locator('[data-testid="system-limit-row"]')
    .count();
  if (limitRows !== overviewLimitNames.length)
    fail("Resource limits did not render every closed limit");
  if (
    (
      (await page
        .locator('[data-testid="system-limits-view"]')
        .textContent()) ?? ""
    ).includes("Current occupancy against enforced Gateway limits.")
  )
    fail("Resource limits retained redundant occupancy guidance");

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
        properties: {
          value: { type: "string" },
          tags: {
            type: "array",
            items: {
              type: "object",
              properties: { label: { type: "string" } },
            },
          },
        },
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
        status: 400,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 400,
          code: "invalid_server_configuration",
          title: "The server configuration is invalid.",
          context: {
            field: "transport.working_directory",
            rule: "canonical_absolute_path",
          },
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
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations/${operation().id}`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify(operation()),
      }),
  );

  await page.evaluate(() => {
    window.location.hash = "#/servers/new";
  });
  const editor = page.locator('[data-testid="server-editor"]');
  await editor.waitFor();
  await page.locator("#server-display-name").fill("Unsaved draft");
  await page.getByRole("link", { name: "Overview", exact: true }).click();
  await page
    .locator('[data-testid="unsaved-changes-cancel"]')
    .waitFor({ timeout: 1_000 })
    .catch(async () =>
      fail(
        `dirty navigation was not blocked at ${await page.evaluate(() => window.location.hash)}`,
      ),
    );
  await page.locator('[data-testid="unsaved-changes-cancel"]').click();
  if (
    (await page.evaluate(() => window.location.hash)) !== "#/servers/new" ||
    (await page.locator("#server-display-name").inputValue()) !==
      "Unsaved draft"
  )
    fail("cancelled navigation did not preserve the dirty server draft");
  await page.getByRole("link", { name: "Overview", exact: true }).click();
  await page.locator('[data-testid="unsaved-changes-submit"]').click();
  await page
    .getByRole("heading", { name: "Overview", exact: true, level: 1 })
    .waitFor();
  await page.evaluate(() => {
    window.location.hash = "#/servers/new";
  });
  await editor.waitFor();
  const dispatchBeforeUnload = () =>
    page.evaluate(() => {
      const event = new Event("beforeunload", {
        bubbles: false,
        cancelable: true,
      });
      return !window.dispatchEvent(event);
    });
  if (await dispatchBeforeUnload())
    fail("untouched server form installed an unload warning");
  await page.locator("#server-display-name").fill("Back-button draft");
  if (!(await dispatchBeforeUnload()))
    fail("dirty server form omitted its unload warning");
  let backPrompt = "";
  page.once("dialog", async (dialog) => {
    backPrompt = dialog.message();
    await dialog.dismiss();
  });
  await page.goBack();
  const hashAfterBack = await page.evaluate(() => window.location.hash);
  const draftAfterBack = await page
    .locator("#server-display-name")
    .inputValue()
    .catch(() => "missing");
  if (
    backPrompt !== "Leave this page? Unsaved changes will be discarded." ||
    hashAfterBack !== "#/servers/new" ||
    draftAfterBack !== "Back-button draft"
  )
    fail(
      `browser history navigation did not preserve a cancelled dirty draft (prompt=${backPrompt}, hash=${hashAfterBack}, draft=${draftAfterBack})`,
    );
  await page.locator("#server-display-name").fill("");
  if (await dispatchBeforeUnload())
    fail("reverted server form retained its unload warning");
  if (
    (await page.locator("#server-executable, #server-url").count()) !== 0 ||
    (await editor.locator('input[name="server-transport-kind"]').count()) !== 2
  )
    fail(
      "server form did not defer transport details until an explicit choice",
    );
  if (
    !(await editor.textContent())?.includes(
      "All fields are required unless marked optional.",
    )
  )
    fail("server form omitted its required-field convention");
  const serverActionGap = await page.evaluate(() => {
    const field = document.querySelector<HTMLElement>(
      '[data-testid="server-editor"] .choice-field',
    )!;
    const action = document.querySelector<HTMLElement>(
      '[data-testid="server-editor-submit"]',
    )!;
    return (
      action.getBoundingClientRect().top - field.getBoundingClientRect().bottom
    );
  });
  if (serverActionGap < 15)
    fail(`server create action gap was ${serverActionGap}px`);
  if (
    !((await editor.textContent()) ?? "").includes(
      "Namespace cannot be changed after creation.",
    )
  )
    fail("server creation omitted immutable namespace guidance");
  const initialInputs = await editor.locator("input").evaluateAll((nodes) =>
    nodes.map((node) => ({
      name: node.getAttribute("name"),
      type: node.getAttribute("type"),
    })),
  );
  if (
    initialInputs.some(
      (input) =>
        (input.name !== null && input.type !== "radio") ||
        input.type === "password",
    )
  )
    fail("server form exposed a reusable text or secret input");
  await page.locator('[data-testid="server-editor-submit"]').click();
  if (creates !== 0) fail("invalid form submitted a create");

  await page.locator("#server-transport-http").check();
  if (
    (await page.locator("#server-executable").count()) !== 0 ||
    (await page.locator("#server-url").count()) !== 1 ||
    (await page.locator("#server-protocol-mode").inputValue()) !== "auto"
  )
    fail("HTTP selection did not reveal only automatic HTTP configuration");
  await page.locator("#server-url").fill("file:///tmp/mcp");
  await page.locator("#server-auth-none").check();
  await page.locator("#server-namespace").fill("url-validation");
  await page.locator("#server-display-name").fill("URL validation");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.getByText("HTTP endpoint must use http or https.").waitFor();
  if (
    (await page.locator("#server-url").getAttribute("aria-invalid")) !== "true"
  )
    fail("invalid HTTP endpoint was not associated with its field");
  if (creates !== 0) fail("invalid HTTP endpoint submitted a create");
  await page.locator("#server-url").fill("https://resource.example/mcp");
  await page.locator("#server-auth-oauth").check();
  if (
    !(await editor.textContent())?.includes("same origin") ||
    !(await editor.textContent())?.includes("Register Gateway automatically") ||
    !(await editor.textContent())?.includes(
      "Request offline access when supported",
    )
  )
    fail("OAuth controls did not explain registration or offline access");
  await page.locator("#server-registration-mode").selectOption("static");
  await page.locator("#server-client-id").fill("   ");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.getByText("Enter the OAuth client ID.").waitFor();
  if (
    (await page.locator("#server-client-id").getAttribute("aria-invalid")) !==
    "true"
  )
    fail("normalized OAuth client ID error was not associated with its field");
  await page.locator("#server-client-id").fill("safe-client");
  await page.locator("#server-issuer").fill("http://issuer.example");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page
    .getByText(
      "OAuth issuer must be an HTTPS URL without credentials, query, or fragment.",
    )
    .waitFor();
  if (
    (await page.locator("#server-issuer").getAttribute("aria-invalid")) !==
    "true"
  )
    fail("OAuth issuer error was not associated with its field");
  await page.locator("#server-registration-mode").selectOption("dynamic");
  await page.locator("#server-issuer").fill("  https://issuer.example  ");
  await page.getByText("Advanced OAuth settings").click();
  await page.locator('[data-testid="server-oauth-origin-add"]').click();
  const origin = page.locator('[data-testid="server-oauth-origin"]');
  await origin.fill("   ");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.getByText("Enter an OAuth network origin.").waitFor();
  if ((await origin.getAttribute("aria-invalid")) !== "true")
    fail("normalized OAuth origin error was not associated with its row");
  await origin.fill("  https://login.internal.example  ");
  await page.locator("#server-offline-access").check();
  await page.locator("#server-namespace").fill("trim-probe");
  await page.locator("#server-display-name").fill("Trim probe");
  await page.locator("#server-url").fill("  https://resource.example/mcp  ");
  await page.locator('[data-testid="server-editor-submit"]').click();
  const normalizedReview = page.locator(
    '[data-testid="server-creation-review"]',
  );
  await normalizedReview.waitFor();
  const reviewedConnection = await normalizedReview
    .getByText("Connection", { exact: true })
    .locator("xpath=following-sibling::dd")
    .textContent();
  if (reviewedConnection !== "HTTP — https://resource.example/mcp")
    fail(
      `server review did not show the normalized HTTP endpoint: ${reviewedConnection}`,
    );
  await page.locator('[data-testid="server-change-confirm-cancel"]').click();
  await page.locator("#server-registration-mode").selectOption("static");
  if (
    (await editor
      .locator('input[id*="secret"], textarea, input[id*="bearer-token"]')
      .count()) !== 0
  )
    fail("server form offered raw JSON or inline secret input");
  await page.locator("#server-transport-stdio").check();
  await page.locator("#server-namespace").fill("first-name");
  await page.locator("#server-display-name").fill("Created server");
  await page.locator("#server-executable").fill("/usr/bin/example");
  await page.locator("#server-working-directory").fill("/srv/example/");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page
    .getByText(
      'Working directory must be an absolute canonical path without a trailing slash, empty segment, ".", or ".." segment.',
    )
    .waitFor();
  if (creates !== 0) fail("noncanonical stdio path submitted a create");
  await page.locator("#server-working-directory").fill("/srv/example");
  await page.getByText("Optional process settings").click();
  await page.locator('[data-testid="server-argument-add"]').click();
  await page.locator('[data-testid="server-argument"]').fill("--safe");
  await page.locator('[data-testid="server-environment-add"]').click();
  await page
    .locator('[data-testid="server-environment-name"]')
    .fill("__proto__");
  await page.locator('[data-testid="server-environment-value"]').fill("read");
  await page.locator('[data-testid="server-secret-environment-add"]').click();
  await page
    .locator('[data-testid="server-secret-environment-name"]')
    .fill("TOKEN");
  await page
    .locator('[data-testid="server-secret-environment-value"]')
    .fill("primary");
  if (!(await editor.textContent())?.includes("It is not sandboxed"))
    fail("stdio OS-user warning omitted containment boundary");
  for (const testID of [
    "server-environment-name",
    "server-secret-environment-name",
    "server-secret-environment-value",
  ]) {
    if (
      (await page
        .locator(`[data-testid="${testID}"]`)
        .getAttribute("required")) === null
    )
      fail(`structured server row omitted required semantics: ${testID}`);
  }
  if (
    (await page
      .locator('[data-testid="server-environment-value"]')
      .getAttribute("required")) !== null
  )
    fail("ordinary environment row rejected an allowed empty value");

  await page.locator('[data-testid="server-editor-submit"]').click();
  const creationReview = page.locator('[data-testid="server-creation-review"]');
  const reviewText = (await creationReview.innerText()).replaceAll("\n", " ");
  if (
    !reviewText.includes("first-name") ||
    !reviewText.includes("Created server") ||
    !reviewText.includes("Disabled") ||
    !reviewText.includes("Local process — /usr/bin/example") ||
    !reviewText.includes("--safe") ||
    !reviewText.includes("__proto__=read") ||
    !reviewText.includes("TOKEN → primary")
  )
    fail("server creation confirmation did not review consequential choices");
  await page.locator('[data-testid="server-change-confirm-cancel"]').click();
  if (
    (await page.locator("#server-display-name").inputValue()) !==
    "Created server"
  )
    fail("confirmation cancellation discarded safe draft");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page
    .getByText(
      'Working directory must be an absolute canonical path without a trailing slash, empty segment, ".", or ".." segment.',
    )
    .waitFor();
  if (((await editor.textContent()) ?? "").includes("refreshed ETag"))
    fail("create rejection showed edit-only ETag guidance");
  const firstCreate = JSON.parse(createBodies[0] ?? "null") as {
    transport?: Record<string, unknown>;
  };
  const firstTransport = firstCreate.transport;
  if (
    firstTransport?.kind !== "stdio" ||
    JSON.stringify(firstTransport.arguments) !== '["--safe"]' ||
    JSON.stringify(firstTransport.environment) !== '{"__proto__":"read"}' ||
    JSON.stringify(firstTransport.secret_environment) !== '{"TOKEN":"primary"}'
  )
    fail("structured stdio controls did not serialize the expected transport");
  await page.locator("#server-namespace").fill("created-server");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page.getByText("Mutation outcome unknown").waitFor();
  await page.locator('[data-testid="server-create-replay"]').click();
  await page.locator('[data-testid="server-status-view"]').waitFor();
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

  await page.getByRole("link", { name: "Settings", exact: true }).click();
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
  const toast = page.locator('[data-testid="toast"]');
  await toast.getByText("Server settings saved.", { exact: true }).waitFor();
  if ((await toast.getAttribute("role")) !== "status")
    fail("save toast was not exposed as an accessible status");
  await page.getByRole("link", { name: "Settings", exact: true }).click();

  const serverEnabled = page.getByRole("switch", { name: "Server enabled" });
  if (await serverEnabled.isChecked())
    fail("disabled server switch was checked");
  await serverEnabled.click();
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page.getByText("Stale server revision").waitFor();
  if (!(await serverEnabled.isChecked()))
    fail("412 refresh discarded behavioral draft");
  await page.locator('[data-testid="server-editor-submit"]').click();
  await page.locator('[data-testid="server-change-confirm-submit"]').click();
  await page
    .locator('[data-testid="toast"]')
    .getByText("Server settings saved; applying changes.", { exact: true })
    .waitFor();
  await page.locator('[data-testid="operation-detail"]').waitFor();
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
  ) => {
    const minute = id.at(-1) ?? "0";
    return {
      id,
      server_id: serverID,
      kind,
      target_desired_revision: currentServer.desired_revision,
      target_credential_revisions: currentServer.credential_revisions,
      state,
      reason,
      created_at: `2026-08-28T14:0${minute}:00Z`,
      started_at: state === "scheduled" ? null : `2026-08-28T14:0${minute}:01Z`,
      finished_at:
        state === "scheduled" || state === "running"
          ? null
          : `2026-08-28T14:0${minute}:02Z`,
    };
  };
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
    `${baseURL}/api/v1/servers/${serverID}/auth-flows?*`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], next_cursor: null }),
      }),
  );
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
    window.location.hash = `#/servers/${id}?tab=activity`;
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
    document.body.textContent?.includes("Available actions"),
  );
  const operationsView = page.locator('[data-testid="server-activity-view"]');
  if (
    (await operationsView
      .locator('[data-testid="operation-row"]')
      .first()
      .locator("a")
      .textContent()) !== "Reload server"
  )
    fail("operation history was not newest first");
  const operationsText = (await operationsView.textContent()) ?? "";
  if (
    !operationsText.includes("Operations") ||
    operationsText.includes("OAuth activity")
  )
    fail("operations tab retained mixed activity content");
  if (
    operationsText.indexOf("Available actions") >
    operationsText.indexOf("Operation history")
  )
    fail("available actions did not precede operation history");
  if ((await operationsView.locator(".status-symbol").count()) !== 0)
    fail("operation statuses retained decorative symbols");
  if (
    (await operationsView.locator('select[aria-label="Action"]').count()) !==
      1 ||
    (await operationsView.locator('select[aria-label="Status"]').count()) !==
      1 ||
    (await operationsView.getByRole("button", { name: "Reset" }).count()) !== 1
  )
    fail("operation table omitted field-specific filters");
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
  if (!detailText.includes("Back to operations"))
    fail("operation detail omitted contextual back navigation");
  if (
    detailText.includes("Overview") ||
    detailText.includes("Available actions")
  )
    fail("operation detail retained unrelated navigation or actions");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=activity`;
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
    window.location.hash = `#/servers/${id}?tab=activity`;
  }, serverID);
  const disconnectAction = page.locator(
    '[data-testid="start-operation-disconnect_credentials"]',
  );
  if (
    !(await disconnectAction.evaluate((element) =>
      element.classList.contains("danger-action"),
    ))
  )
    fail("credential disconnect was not styled as dangerous");
  await disconnectAction.click();
  if (
    !(await page
      .locator('[data-testid="operation-start-confirm-submit"]')
      .evaluate((element) => element.classList.contains("danger-action")))
  )
    fail("credential disconnect confirmation was not dangerous");
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
    window.location.hash = `#/servers/${id}?tab=activity`;
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
    window.location.hash = `#/servers/${id}?tab=activity`;
  }, serverID);
  await page.getByText("No actions are currently available").waitFor();
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
    `${baseURL}/api/v1/servers/${serverID}/auth-flows?*`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], next_cursor: null }),
      }),
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations?*`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], next_cursor: null }),
      }),
  );
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
    window.location.hash = `#/servers/${id}?tab=settings`;
  }, serverID);
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page.locator('[data-testid="server-destructive-actions"]').waitFor();
  if (
    (await page
      .locator('[data-testid="disconnect-server-credentials"]')
      .count()) !== 0
  )
    fail("settings retained duplicate credential disconnect");
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=activity`;
  }, serverID);
  const disconnect = page.locator(
    '[data-testid="start-operation-disconnect_credentials"]',
  );
  await disconnect.waitFor();
  await disconnect.click();
  const disconnectText =
    (await page
      .locator("#operation-start-confirm-consequence")
      .textContent()) ?? "";
  if (!disconnectText.includes("not guaranteed"))
    fail("disconnect consequence overstated cleanup");
  await page.locator('[data-testid="operation-start-confirm-cancel"]').click();
  if (disconnects !== 0) fail("cancelled disconnect submitted");
  await disconnect.click();
  await page.locator('[data-testid="operation-start-confirm-submit"]').click();
  await page.getByText("Stale server revision").waitFor();
  await page.waitForFunction(
    () =>
      !(
        document.querySelector(
          '[data-testid="start-operation-disconnect_credentials"]',
        ) as HTMLButtonElement | null
      )?.disabled,
  );
  if (Number(disconnects) !== 1)
    fail("stale disconnect replayed automatically");
  await disconnect.click();
  await page.locator('[data-testid="operation-start-confirm-submit"]').click();
  await page.locator('[data-testid="operation-detail"]').waitFor();
  if (Number(disconnects) !== 2) fail("confirmed disconnect count changed");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=settings`;
  }, serverID);
  await page.locator('[data-testid="server-destructive-actions"]').waitFor();
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
  await page.waitForFunction(
    () =>
      !(
        document.querySelector(
          '[data-testid="delete-server"]',
        ) as HTMLButtonElement | null
      )?.disabled,
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
  await page.locator('[data-testid="server-status-view"]').waitFor();
  await page
    .locator('[data-testid="server-context"]')
    .getByText("Deleted", { exact: true })
    .waitFor();
  let tombstone =
    (await page.locator('[data-testid="server-status-view"]').textContent()) ??
    "";
  if (
    !tombstone.includes("This server is retained as historical evidence.") ||
    tombstone.includes("View status")
  )
    fail("deleted server status did not explain its historical state");
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=status`;
  }, serverID);
  await page.locator('[data-testid="server-status-view"]').waitFor();
  await page.locator('time[datetime="2026-08-28T16:05:00Z"]').waitFor();
  tombstone = (await page.locator("body").textContent()) ?? "";
  if (!tombstone.includes("Deleted") || !tombstone.includes("durable tools"))
    fail("deleted server status omitted permanent historical state");
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
  let showExchangeInList = false;
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
    diagnostic:
      id === terminalID
        ? {
            correlation_id: terminalID,
            stage: "client_registration",
            reason: "oauth_rejected",
            http_status: 400,
          }
        : null,
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
    `${baseURL}/api/v1/servers/${serverID}/operations?*`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], next_cursor: null }),
      }),
  );
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
            showExchangeInList
              ? flow(exchangingID, "exchanging")
              : flow(activeID, "preparing"),
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
    window.location.hash = `#/servers/${id}?tab=authentication`;
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
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=authentication`;
  }, serverID);
  const authorizationAction = page.locator('[data-testid="start-auth-flow"]');
  await authorizationAction.waitFor();
  if (
    ((await page.locator("body").textContent()) ?? "").includes(
      "If the authorization page is lost",
    )
  )
    fail("server authentication retained redundant restart guidance");
  if (
    (await authorizationAction.textContent())?.trim() !== "Authorize server" ||
    !(await authorizationAction.getAttribute("class"))?.includes(
      "primary-action",
    )
  )
    fail("missing OAuth authority did not use the creation action");
  server.credential_state = "ready";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByRole("button", { name: "Reauthorize server" }).waitFor();
  if (
    (await authorizationAction.getAttribute("class"))?.includes(
      "primary-action",
    )
  )
    fail("OAuth authority replacement used creation styling");
  server.credential_state = "reauthentication_required";
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByRole("button", { name: "Authorize server" }).waitFor();

  await authorizationAction.click();
  const display = page.locator('[data-testid="one-time-oauth-url"]');
  await display.waitFor();
  if ((await display.locator("a").count()) !== 0)
    fail("authorization URL became active content");
  if ((await display.textContent()) !== authorizationURL)
    fail("authorization URL display changed");

  await context.grantPermissions(["clipboard-read", "clipboard-write"], {
    origin: new URL(baseURL).origin,
  });
  await page.evaluate(() => {
    const nativeOpen = window.open.bind(window);
    const state = { blocked: true };
    Object.defineProperty(window, "__oauthOpenState", {
      configurable: true,
      value: state,
    });
    window.open = (...arguments_) =>
      state.blocked ? null : nativeOpen(...arguments_);
  });
  await page.locator('[data-testid="open-oauth-url"]').click();
  await page.getByText("The browser blocked the new page.").waitFor();
  const copyURL = page.locator('[data-testid="copy-oauth-url"]');
  await copyURL.waitFor();
  await copyURL.click();
  await page.getByText("Copied to the operating-system clipboard.").waitFor();
  if (
    (await page.evaluate(() => navigator.clipboard.readText())) !==
    authorizationURL
  )
    fail("blocked authorization URL lacked a copy fallback");
  await page.evaluate(async () => {
    await navigator.clipboard.writeText(
      "clipboard overwritten after OAuth fallback test",
    );
    Object.defineProperty(navigator.clipboard, "writeText", {
      configurable: true,
      value: async () => {
        throw new Error("clipboard denied");
      },
    });
    (
      window as typeof window & {
        __oauthOpenState: { blocked: boolean };
      }
    ).__oauthOpenState.blocked = false;
  });
  await copyURL.click();
  await page.getByText("Clipboard copy failed.").waitFor();

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
  if ((await page.locator("body").textContent())?.includes(`Flow ${createdID}`))
    fail("authorization exposed a redundant internal flow notice");

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
  if (
    (await page.locator('[data-testid="cancel-auth-flow"]').count()) !== 0 ||
    (await page.locator('[data-testid="start-auth-flow"]').count()) !== 0
  )
    fail("exchanging auth flow offered a mutation");
  showExchangeInList = true;
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=authentication`;
  }, serverID);
  await page
    .getByText("An OAuth authorization is already in progress.")
    .waitFor();
  await page.locator('[data-testid="auth-flow-list"]').waitFor();
  if ((await page.locator('[data-testid="auth-flow-row"]').count()) !== 2)
    fail("authentication omitted OAuth activity history");
  if ((await page.locator('[data-testid="start-auth-flow"]').count()) !== 0)
    fail("authentication offered a second active OAuth flow");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}/auth-flows/${"01ARZ3NDEKTSV4RRFFQ69G5FE7"}`;
  }, serverID);
  await page.locator('[data-testid="auth-flow-detail"]').waitFor();
  if ((await page.locator('[data-testid="cancel-auth-flow"]').count()) !== 0)
    fail("terminal auth flow offered cancellation");
  let finalDOM = (await page.locator("body").textContent()) ?? "";
  for (const value of [
    "Diagnostic details",
    "Client registration",
    "OAuth rejected",
    "400",
    terminalID,
  ])
    if (!finalDOM.includes(value)) fail(`OAuth detail omitted ${value}`);
  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=status`;
  }, serverID);
  await page.locator('[data-testid="server-status-view"]').waitFor();
  finalDOM = (await page.locator("body").textContent()) ?? "";
  if (
    finalDOM.includes("OAuth failures") ||
    finalDOM.includes("client registration")
  )
    fail("server status duplicated OAuth history");
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
    `${baseURL}/api/v1/servers/${serverID}/auth-flows?*`,
    async (route) =>
      route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], next_cursor: null }),
      }),
  );
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
    window.location.hash = `#/servers/${id}?tab=authentication`;
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
    .getByRole("heading", { name: "Authentication", exact: true })
    .waitFor();
  if (
    (await page
      .locator('[data-testid="credential-replacement-form"]')
      .count()) !== 0 ||
    (await page.getByText("Inspect activity", { exact: true }).count()) !== 0 ||
    (await page
      .getByText("This server does not require credentials.", { exact: true })
      .count()) !== 0
  )
    fail("no-auth server repeated state or offered irrelevant actions");
  eligibilityModes += 1;
  currentServer = {
    ...currentServer,
    transport:
      oauthDynamicTransport as unknown as typeof currentServer.transport,
  };
  await page.locator('[data-testid="manual-refresh"]').click();
  await page.getByRole("heading", { name: "OAuth", exact: true }).waitFor();
  if (
    (await page
      .locator('[data-testid="credential-replacement-form"]')
      .count()) !== 0
  )
    fail("dynamic OAuth offered client-secret replacement");
  eligibilityModes += 1;

  currentServer = { ...currentServer, transport: stdioTransport };
  await page.locator('[data-testid="manual-refresh"]').click();
  await assertEligible("credential-slot-primary");
  await page.getByRole("heading", { name: "Local secrets" }).waitFor();

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
  await page
    .locator("#primary-navigation")
    .getByRole("link", { name: "Overview", exact: true })
    .click();
  await page.locator('[data-testid="unsaved-changes-cancel"]').waitFor();
  await page.locator('[data-testid="unsaved-changes-cancel"]').click();
  await page
    .locator('dialog[aria-labelledby="unsaved-changes-title"]')
    .waitFor({ state: "hidden" });
  if ((await field.inputValue()) !== firstCanary)
    fail("credential navigation cancellation cleared the write-only draft");
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
              items: [degradedServer, activeServer],
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
        items: catalogRestarted
          ? [
              {
                ...descriptorReadFixture(
                  serverReadIDs.activeTool,
                  serverReadIDs.active,
                  "active-restarted",
                  false,
                ),
                server_display_name: "Authority required",
                server_catalog_state: "stale",
              },
            ]
          : [
              {
                ...descriptorReadFixture(
                  serverReadIDs.currentTool,
                  serverReadIDs.active,
                  "zulu-tool",
                  false,
                ),
                server_display_name: "Authority required",
                server_catalog_state: "stale",
              },
              {
                ...descriptorReadFixture(
                  serverReadIDs.retiredTool,
                  serverReadIDs.active,
                  "alpha-tool",
                  false,
                ),
                server_display_name: "Authority required",
                server_catalog_state: "current",
              },
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
  const serverTableSurface = await page
    .locator('[data-testid="servers-view"] .table-region')
    .evaluate((table) => {
      const panel = table.closest(".panel");
      if (panel === null) return undefined;
      const style = getComputedStyle(panel);
      return {
        background: style.backgroundColor,
        borderWidth: style.borderTopWidth,
        padding: style.paddingTop,
      };
    });
  if (
    serverTableSurface === undefined ||
    serverTableSurface.background === "rgba(0, 0, 0, 0)" ||
    serverTableSurface.borderWidth === "0px" ||
    serverTableSurface.padding === "0px"
  )
    fail("server inventory was not presented on a panel surface");
  let body = (await page.locator("body").textContent()) ?? "";
  const serverNames = await page
    .locator('[data-testid="server-row"] th[scope="row"]')
    .allTextContents();
  if (
    serverNames.map((name) => name.trim()).join("|") !==
      "Authority required|Degraded catalog" ||
    (await page
      .locator('[data-testid="servers-view"] thead th')
      .first()
      .getAttribute("aria-sort")) !== "ascending"
  )
    fail(`servers did not default to Name ascending: ${serverNames}`);
  for (const phrase of [
    "Authority required",
    "server-b0",
    "Authorization required",
    "Degraded catalog",
    "server-b1",
    "Needs attention",
  ])
    if (!body.includes(phrase)) fail(`server inventory omitted ${phrase}`);
  const serverHeaders = await page
    .locator('[data-testid="servers-view"] thead th')
    .allTextContents();
  if (
    serverHeaders.map((value) => value.replace(/\s?[↑↓↕]$/, "")).join("|") !==
    "Name|ID|Namespace|Status|Tools"
  )
    fail(`server inventory columns drifted: ${serverHeaders.join("|")}`);
  for (const id of [serverReadIDs.active, serverReadIDs.degraded]) {
    const idLink = page.getByRole("link", { name: id, exact: true });
    if (
      (await idLink.count()) !== 1 ||
      (await idLink.getAttribute("href")) !== `#/servers/${id}`
    )
      fail(`server inventory ID ${id} did not target Status`);
  }
  if (
    (await page
      .locator('[data-testid="servers-view"] .status-symbol')
      .count()) !== 0
  )
    fail("server inventory retained decorative status symbols");
  if (
    (await page.getByLabel("Name or ID", { exact: true }).count()) !== 1 ||
    (await page.getByLabel("Namespace", { exact: true }).count()) !== 1 ||
    (await page.getByLabel("Status", { exact: true }).count()) !== 1 ||
    (await page
      .getByLabel("Status", { exact: true })
      .locator(
        'option[value="Authentication unavailable"], option[value="Capacity saturated"]',
      )
      .count()) !== 2 ||
    (await page.getByRole("button", { name: "Reset" }).count()) !== 1
  )
    fail("server inventory omitted field-specific filters");
  const tableControlLayout = await page.evaluate(() => {
    const input = document.querySelector<HTMLElement>(
      '.table-filters input[aria-label="Name or ID"]',
    )!;
    const reset = document.querySelector<HTMLElement>(
      ".table-filters .text-button",
    )!;
    const sort = document.querySelector<HTMLElement>(".sort-button")!;
    const inputBox = input.getBoundingClientRect();
    const resetBox = reset.getBoundingClientRect();
    return {
      centerDifference: Math.abs(
        inputBox.top +
          inputBox.height / 2 -
          (resetBox.top + resetBox.height / 2),
      ),
      sortGap: getComputedStyle(sort).columnGap,
    };
  });
  if (
    tableControlLayout.centerDifference > 1 ||
    tableControlLayout.sortGap === "normal" ||
    tableControlLayout.sortGap === "0px"
  )
    fail(
      `table controls were misaligned: ${JSON.stringify(tableControlLayout)}`,
    );
  const createButtonBox = await page
    .locator('[data-testid="server-create-link"]')
    .boundingBox();
  const serversViewBox = await page
    .locator('[data-testid="servers-view"]')
    .boundingBox();
  if (
    createButtonBox === null ||
    serversViewBox === null ||
    Math.abs(createButtonBox.x - serversViewBox.x) > 1
  )
    fail("Create server was not left aligned");
  if (!(await page.getByText("Showing 2 of 2", { exact: true }).count()))
    fail("server inventory omitted its visible result count");
  await page.getByLabel("Name or ID", { exact: true }).fill("Degraded catalog");
  if ((await page.locator('[data-testid="server-row"]').count()) !== 1)
    fail("server name filter did not narrow loaded rows");
  if (!(await page.getByText("Showing 1 of 2", { exact: true }).count()))
    fail("server inventory did not update its visible result count");
  if (
    !(await page.evaluate(() => window.location.hash)).includes(
      "filter_name=Degraded%20catalog",
    )
  )
    fail("server table filter was not persisted in the URL");
  await page
    .getByLabel("Name or ID", { exact: true })
    .fill(serverReadIDs.active);
  if (
    (await page.locator('[data-testid="server-row"]').count()) !== 1 ||
    !(await page.locator('[data-testid="server-row"]').innerText()).includes(
      "Authority required",
    )
  )
    fail("server ID search did not match");
  await page.getByRole("button", { name: "Reset" }).click();
  if ((await page.locator('[data-testid="server-row"]').count()) !== 2)
    fail("server filter Reset did not restore loaded rows");
  await page.locator('[data-testid="load-more-servers"]').click();
  await page.waitForFunction(
    () => document.querySelectorAll('[data-testid="server-row"]').length === 3,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Deleted history") || !body.includes("Deleted"))
    fail("server inventory omitted deleted server");

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

  await page.evaluate(() => {
    (window as Window & { serverTabsOrphaned?: boolean }).serverTabsOrphaned =
      false;
    new MutationObserver(() => {
      const tabs = document.querySelector('nav[aria-label="Server sections"]');
      const context = document.querySelector('[data-testid="server-context"]');
      if (tabs !== null && context === null)
        (
          window as Window & { serverTabsOrphaned?: boolean }
        ).serverTabsOrphaned = true;
    }).observe(document.body, { childList: true, subtree: true });
  });
  await page
    .locator(`a[href="#/servers/${serverReadIDs.active}"]`)
    .first()
    .click();
  await page.locator('[data-testid="server-status-view"]').waitFor();
  await page.locator('[data-testid="server-context"]').waitFor();
  await page.waitForFunction(
    () =>
      document.activeElement ===
      document.querySelector('[data-testid="server-context"] h2'),
  );
  if (
    await page.evaluate(
      () =>
        (window as Window & { serverTabsOrphaned?: boolean })
          .serverTabsOrphaned === true,
    )
  )
    fail("server tabs appeared without their contextual header");
  body = (await page.locator("body").textContent()) ?? "";
  for (const phrase of [
    "Authority required",
    "Authorization required",
    "Manage credentials",
  ])
    if (!body.includes(phrase)) fail(`server detail omitted ${phrase}`);
  if (body.includes("Authorize server"))
    fail("non-OAuth server offered OAuth authorization");
  const serverContextText =
    (await page.locator('[data-testid="server-context"]').textContent()) ?? "";
  if (
    serverContextText.includes("server-b0") ||
    serverContextText.includes("available tools") ||
    serverContextText.includes("View tools")
  )
    fail("server context retained redundant namespace or tool guidance");
  if (body.includes("Desired revision 7") || body.includes("Runtime identity"))
    fail("server overview exposed diagnostic implementation state");

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=tools`;
  }, serverReadIDs.active);
  await page.locator('[data-testid="descriptor-list"]').waitFor();
  await page.waitForFunction(
    () =>
      document.querySelectorAll('[data-testid="descriptor-row"]').length === 2,
  );
  body = (await page.locator("body").textContent()) ?? "";
  if (!body.includes("Available") || !body.includes("Retired"))
    fail("tool list omitted available/retired labels");
  if (
    !body.includes("server.current-tool") ||
    !body.includes("server.retired-tool")
  )
    fail("tool list omitted fully namespaced tool names");
  await page
    .locator(
      `a[href="#/servers/${serverReadIDs.active}/descriptors/${serverReadIDs.retiredTool}"]`,
    )
    .click();
  await page.locator('[data-testid="descriptor-detail"]').waitFor();
  if (
    await page.evaluate(
      () => document.activeElement?.getAttribute("id") === "page-title",
    )
  )
    fail("server navigation moved focus to the hidden shell title");
  body = (await page.locator("body").textContent()) ?? "";
  if (
    !body.includes("Catalog revision") ||
    !body.includes("Historical evidence; not callable") ||
    !body.includes("Input schema") ||
    !body.includes("Items") ||
    !body.includes("label") ||
    (await page
      .locator('[data-testid="descriptor-detail"] .inert-json')
      .count()) !== 0 ||
    (await page
      .getByRole("navigation", { name: "Tool navigation" })
      .getByRole("link", { name: "Back to tools", exact: true })
      .getAttribute("href")) !==
      `#/servers/${serverReadIDs.active}?tab=tools` ||
    (await page
      .getByRole("navigation", { name: "Tool navigation" })
      .getByRole("link", { name: "Back to catalog", exact: true })
      .getAttribute("href")) !== "#/catalog"
  )
    fail(
      "descriptor detail omitted structured evidence or contextual navigation",
    );

  await page.evaluate((id) => {
    window.location.hash = `#/servers/${id}?tab=tools`;
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
  await page.locator('[data-testid="catalog-row"]').first().waitFor();
  body = (await page.locator("body").textContent()) ?? "";
  const toolNames = await page
    .locator('[data-testid="catalog-row"] th[scope="row"]')
    .allTextContents();
  if (
    toolNames.map((name) => name.trim()).join("|") !==
      "server.alpha-tool|server.zulu-tool" ||
    (await page
      .locator('[data-testid="catalog-view"] thead th')
      .first()
      .getAttribute("aria-sort")) !== "ascending"
  )
    fail(`catalog did not default to Tool ascending: ${toolNames}`);
  for (const phrase of [
    "Active administrative catalog",
    "Process generation",
    "Process-local administrative publication",
    "Degraded administrative evidence",
  ])
    if (body.includes(phrase)) fail(`active catalog retained ${phrase}`);
  if (!body.includes("Authority required"))
    fail("active catalog omitted server display name");
  if (
    (await page.getByLabel("Status", { exact: true }).count()) !== 1 ||
    (await page.getByLabel("Server", { exact: true }).count()) !== 1
  )
    fail("active catalog omitted server or status filters");
  const catalogFilterLayout = await page.evaluate(() => {
    const toolbar = document.querySelector<HTMLElement>(
      '[data-testid="catalog-view"] .table-filters',
    )!;
    const input = toolbar.querySelector<HTMLElement>(
      'input[aria-label="Tool"]',
    )!;
    const select = toolbar.querySelector<HTMLElement>(
      'select[aria-label="Status"]',
    )!;
    const controls = [
      ...toolbar.querySelectorAll<HTMLElement>("input, select, button"),
    ];
    const tops = controls.map((control) => control.getBoundingClientRect().top);
    return {
      inputWidth: input.getBoundingClientRect().width,
      selectWidth: select.getBoundingClientRect().width,
      rowSpread: Math.max(...tops) - Math.min(...tops),
    };
  });
  if (
    catalogFilterLayout.selectWidth >= catalogFilterLayout.inputWidth * 0.75 ||
    catalogFilterLayout.rowSpread > 1
  )
    fail(
      `catalog filters were wastefully sized: ${JSON.stringify(catalogFilterLayout)}`,
    );
  await page.getByLabel("Status", { exact: true }).selectOption("issue");
  if ((await page.locator('[data-testid="catalog-row"]').count()) !== 1)
    fail("active catalog status filter did not retain matching tools");
  await page.getByRole("button", { name: "Reset" }).click();
  if (
    (await page
      .locator(`a[href="#/servers/${serverReadIDs.active}?tab=tools"]`)
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

async function runDevelopmentControlPlane(
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

async function runDevelopmentLiveReload(
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

let browser: Browser | undefined;
try {
  let input = parseInitialInput(await readBoundedInput());
  const baseURL = input.base_url;
  const initialBearer = input.admin_bearer;
  input = { ...input, admin_bearer: "" };
  const browserType =
    input.browser_kind === "firefox"
      ? firefox
      : input.browser_kind === "webkit"
        ? webkit
        : chromium;
  browser = await browserType.launch({ headless: true });
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
  await loadShell(
    page,
    input.scenario !== "development-live-reload" &&
      input.scenario !== "development-control-plane",
  );

  if (input.scenario === "shell-load") {
    if (externalRequests.length !== 0) fail("external shell request");
    process.stdout.write('{"event":"shell_loaded"}\n');
    process.on("SIGTERM", () => {});
    setInterval(() => {}, 60 * 60 * 1000);
  } else {
    if (input.scenario === "development-control-plane") {
      await runDevelopmentControlPlane(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "development-live-reload") {
      await runDevelopmentLiveReload(
        browser.version(),
        page,
        initialBearer,
        input.fixture_root ?? fail("missing development fixture root"),
        () => requests,
      );
    } else if (input.scenario === "session-lifecycle-canary") {
      await runSessionLifecycleCanary(
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
    } else if (input.scenario === "visual-accessibility-privacy-canary") {
      await runVisualAccessibilityPrivacyCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "secret-storage-privacy") {
      await runSecretStoragePrivacy(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "visual-responsive-matrix") {
      await runVisualResponsiveMatrix(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "accessibility-keyboard-responsive") {
      await runAccessibilityKeyboardResponsive(
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
    } else if (input.scenario === "prior-session-response-isolation-canary") {
      await runPriorSessionResponseIsolationCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "overview-invocation-system-canary") {
      await runOverviewInvocationSystemCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "server-management-canary") {
      await runServerManagementCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "access-management-read-canary") {
      await runAccessManagementReadCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "system-administration-canary") {
      await runSystemAdministrationCanary(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "admin-credentials") {
      await runAdminCredentials(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "backups") {
      await runBackups(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "capability-audit") {
      await runCapabilityAudit(
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
    } else if (input.scenario === "request-reads") {
      await runRequestReads(
        browser.version(),
        context,
        page,
        baseURL,
        initialBearer,
        () => requests,
      );
    } else if (input.scenario === "request-adjudication") {
      await runRequestAdjudication(
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
      (input.scenario === "server-create-update" &&
        consoleFailures.length === 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 400"),
        )) ||
      (input.scenario === "principals" &&
        consoleFailures.length >= 2 &&
        consoleFailures.length <= 3 &&
        consoleFailures.filter((value) =>
          value.includes("server responded with a status of 409"),
        ).length ===
          consoleFailures.length - 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 412"),
        )) ||
      (input.scenario === "principal-credentials" &&
        consoleFailures.length === 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 412"),
        )) ||
      (input.scenario === "grant-reads-create" &&
        consoleFailures.length >= 2 &&
        consoleFailures.length <= 3 &&
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
        )) ||
      (input.scenario === "request-reads" &&
        consoleFailures.length >= 1 &&
        consoleFailures.length <= 2 &&
        consoleFailures.every((value) =>
          value.includes("server responded with a status of 409"),
        )) ||
      (input.scenario === "backups" &&
        consoleFailures.length === 2 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 503"),
        )) ||
      (input.scenario === "admin-credentials" &&
        consoleFailures.length === 1 &&
        consoleFailures.some((value) =>
          value.includes("server responded with a status of 409"),
        )) ||
      (input.scenario === "request-adjudication" &&
        consoleFailures.length === 3 &&
        [400, 412, 503].every((status) =>
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
