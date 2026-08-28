import {
  chromium,
  type Browser,
  type BrowserContext,
  type Page,
  type Response,
} from "@playwright/test";
import { createInterface } from "node:readline";

interface BridgeInput {
  version: 1;
  scenario: "shell-load" | "browser-protocol" | "m1-canary";
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
      value.scenario !== "m1-canary") ||
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
  const response = await page.goto("/", { waitUntil: "networkidle" });
  if (response === null || response.status() !== 200) fail("shell load failed");
  await page.locator('[data-testid="gateway-shell"]').waitFor();
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
): Promise<Response> {
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

  await page.reload({ waitUntil: "networkidle" });
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
