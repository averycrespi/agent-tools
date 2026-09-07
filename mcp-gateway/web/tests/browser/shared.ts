import {
  type BrowserContext,
  type Page,
  type Response as PlaywrightResponse,
} from "@playwright/test";

export interface SessionBootstrap {
  csrf_token: string;
  idle_expires_at: string;
  absolute_expires_at: string;
}

export interface CreatedCredential {
  id: string;
  bearer: string;
  expires_at: string | null;
}

export function fail(message: string): never {
  throw new Error(message);
}

export async function loadShell(
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

export async function sessionRequest(
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

export function sessionBootstrap(value: unknown): SessionBootstrap {
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

export function createdCredential(value: unknown): CreatedCredential {
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

export async function exchange(
  page: Page,
  bearer: string,
): Promise<SessionBootstrap> {
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

export async function bootstrap(
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

export async function expiryResponse(
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

export async function assertSessionCookieAbsent(
  context: BrowserContext,
  baseURL: string,
): Promise<void> {
  const cookies = await context.cookies(baseURL);
  if (cookies.some((cookie) => cookie.name === "mcp_gateway_session"))
    fail("session cookie was not cleared");
}

export async function connectAndCancelStream(
  page: Page,
  csrf: string,
): Promise<void> {
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

export interface BrowserStorageSnapshot {
  local: Array<[string, string]>;
  session: Array<[string, string]>;
  databases: string[];
  caches: string[];
  registrations: number;
}

export async function browserStorage(
  page: Page,
): Promise<BrowserStorageSnapshot> {
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

export function assertClosedStorage(
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

export async function waitForLifecycle(
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

export async function assertSecretAbsent(
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

export function sessionFixture(): Record<string, string> {
  return {
    csrf_token: "A".repeat(43),
    idle_expires_at: "2026-08-28T18:30:00Z",
    absolute_expires_at: "2026-08-29T18:00:00Z",
  };
}

export async function eventually(
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

export async function waitForCollectionRows(
  page: Page,
  kind: "principal" | "grant",
  count: number,
): Promise<void> {
  await page.waitForFunction(
    ({ kind, count }) => {
      const table = document.querySelector(
        `[data-testid="${kind}s-view"] .collection-table`,
      );
      return (
        table !== null &&
        table.getAttribute("aria-busy") !== "true" &&
        table.querySelectorAll(`[data-testid="${kind}-row"]`).length === count
      );
    },
    { kind, count },
  );
}
