import { expect, type BrowserContext, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { assertSecretAbsent, waitForLifecycle } from "./shared.ts";

export async function runHTTPGrants(
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<void> {
  const screenshots = await mkdtemp(join(tmpdir(), "gateway-http-grants-"));
  const captureState = async (name: string) => {
    if (name.startsWith("principal-")) {
      const skip = await page.locator(".skip-link").evaluate((node) => ({
        focused: node === document.activeElement,
        bottom: node.getBoundingClientRect().bottom,
        transform: getComputedStyle(node).transform,
      }));
      expect(skip.focused).toBe(false);
      expect(skip.bottom).toBeLessThan(0);
      await page.screenshot({
        path: join(screenshots, `${name}-viewport.png`),
      });
    }
    await page.screenshot({
      path: join(screenshots, `${name}.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    if (name.startsWith("principal-")) {
      await expect(page.locator(".skip-link")).not.toBeFocused();
      expect(
        await page
          .locator(".skip-link")
          .evaluate((node) => node.getBoundingClientRect().bottom),
      ).toBeLessThan(0);
      await page.screenshot({
        path: join(screenshots, `${name}-narrow-viewport.png`),
      });
    }
    await page.screenshot({
      path: join(screenshots, `${name}-narrow.png`),
      fullPage: true,
    });
    expect(
      await page.evaluate(() => document.documentElement.scrollWidth),
    ).toBeLessThanOrEqual(390);
    await page.setViewportSize({ width: 1280, height: 900 });
  };
  const api = async (
    path: string,
    data: unknown,
    method = "POST",
    etag?: string,
  ) => {
    const r = await context.request.fetch(baseURL + path, {
      method,
      headers: {
        Authorization: `Bearer ${bearer}`,
        Cookie: "",
        "Content-Type": "application/json",
        ...(etag === undefined ? {} : { "If-Match": etag }),
      },
      data,
    });
    expect(
      r.ok(),
      `${method} ${path}: ${r.status()} ${r.ok() ? "" : await r.text()}`,
    ).toBe(true);
    return r;
  };
  const created = await api("/api/v2/principals", {
    display_name: "HTTP Policy Agent",
    visibility: "all",
  });
  const principal = (await created.json()).principal as { id: string };
  const creds: { id: string }[] = [];
  for (const name of [
    "Compatible credential",
    "Second compatible credential",
    "Unrelated credential",
  ]) {
    const r = await api("/api/v2/http/credentials", {
      name,
      boundary: {
        host:
          name === "Unrelated credential"
            ? "other.example.com"
            : "api.example.com",
        port: 443,
        allow_wildcard: false,
      },
      recipe: { header: "Authorization", prefix: "Bearer " },
      secret: "http-grant-material-canary",
    });
    creds.push((await r.json()) as { id: string });
  }
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  const createdDefaults: Record<string, string> = {};
  let creations = 0;
  page.on("request", (request) => {
    if (
      request.method() === "POST" &&
      request.url() === `${baseURL}/api/v2/principals`
    )
      creations++;
  });
  for (const policy of ["block", "allow"]) {
    await page.goto(`${baseURL}/#/agents/new`);
    const choice = page.getByLabel("HTTP default", { exact: true });
    await expect(choice).toHaveValue("block");
    await expect(choice.locator("option")).toHaveText([
      "Block requests",
      "Allow requests",
    ]);
    await page
      .getByLabel("Display name", { exact: true })
      .fill(`Created ${policy}`);
    await choice.selectOption(policy);
    await captureState(`principal-create-${policy}`);
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await page
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    const review = page.getByRole("dialog");
    await expect(review).toContainText(
      policy === "allow" ? "Allow requests" : "Block requests",
    );
    await captureState(`principal-create-${policy}-review`);
    const before = creations;
    await review.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(creations).toBe(before);
    await expect(choice).toHaveValue(policy);
    await page
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    const response = page.waitForResponse(
      (r) =>
        r.url() === `${baseURL}/api/v2/principals` &&
        r.request().method() === "POST",
    );
    await review
      .getByRole("button", { name: "Create agent", exact: true })
      .click();
    const committed = await response;
    expect(committed.status()).toBe(201);
    const value = (await committed.json()).principal;
    createdDefaults[policy] = value.id;
    expect(value.http_default).toBe(policy);
    expect(creations).toBe(before + 1);
    await expect(page).toHaveURL(`${baseURL}/#/agents/${value.id}`);
    const details = page.getByRole("region", {
      name: "Agent details",
      exact: true,
    });
    await expect(details).toContainText(
      policy === "allow" ? "Allow requests" : "Block requests",
    );
    await page.reload();
    await expect(details).toContainText(
      policy === "allow" ? "Allow requests" : "Block requests",
    );
    expect((await new AxeBuilder({ page }).analyze()).violations).toEqual([]);
    await captureState(`principal-created-${policy}`);
  }
  await page.goto(`${baseURL}/#/agents/new`);
  await page
    .getByLabel("Display name", { exact: true })
    .fill("Uncertain allow creation");
  await page.getByLabel("HTTP default", { exact: true }).selectOption("allow");
  let uncertainID = "";
  await page.route(`${baseURL}/api/v2/principals`, async (route) => {
    const response = await route.fetch();
    expect(response.status()).toBe(201);
    uncertainID = (await response.json()).principal.id;
    // The server committed; an unusable acknowledgement must never replay POST.
    await route.fulfill({ response, body: "{}" });
  });
  const beforeUncertain = creations;
  await page
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create agent", exact: true })
    .click();
  await expect(
    page.getByText("Agent outcome is unknown", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Review and create", exact: true }),
  ).toBeDisabled();
  await expect(page.getByLabel("HTTP default", { exact: true })).toHaveValue(
    "allow",
  );
  expect(creations).toBe(beforeUncertain + 1);
  const persisted = await (
    await api(`/api/v2/principals/${uncertainID}`, undefined, "GET")
  ).json();
  expect(persisted.http_default).toBe("allow");
  await captureState("principal-create-uncertain");
  await page.unroute(`${baseURL}/api/v2/principals`);
  await page.reload();
  await waitForLifecycle(page, "authenticated");
  expect(creations).toBe(beforeUncertain + 1);
  await page.goto(`${baseURL}/#/http/grants`);
  await page.locator('#primary-navigation a[href="#/http/grants"]').click();
  await expect(page.getByText("No HTTP grants", { exact: true })).toBeVisible();
  const toolbar = page.locator(".collection-toolbar").filter({
    has: page.getByRole("link", { name: "Create grant", exact: true }),
  });
  await expect(toolbar).toHaveCSS("gap", "8px");
  await expect(toolbar).toHaveCSS("flex-wrap", "wrap");
  const grants: { id: string; revision: string }[] = [];
  for (const kind of [
    "block_destination",
    "allow_tunnel",
    "block_requests",
    "allow_requests",
  ]) {
    await page.getByRole("link", { name: "Create grant", exact: true }).click();
    await expect(
      page.getByRole("heading", {
        name: "Create HTTP grant",
        level: 1,
        exact: true,
      }),
    ).toBeVisible();
    await expect(
      page.getByRole("region", { name: "Grant configuration" }),
    ).toBeVisible();
    await page.getByLabel("Agent", { exact: true }).selectOption(principal.id);
    await page.getByLabel("Description (optional)").fill(`Test ${kind}`);
    await page.getByLabel("Grant type").selectOption(kind);
    await page
      .getByLabel("Destination host", { exact: true })
      .fill(
        kind === "allow_requests"
          ? "api.example.com"
          : kind === "block_requests"
            ? "xn--bcher-kva.example"
            : "isolated.example.com",
      );
    const requests = kind.endsWith("requests");
    await expect(page.getByLabel("Path match")).toHaveCount(requests ? 1 : 0);
    await expect(page.getByLabel("Credential (optional)")).toHaveCount(
      kind === "allow_requests" ? 1 : 0,
    );
    await expect(
      page.getByLabel("Allow local/private destinations"),
    ).toHaveCount(kind.startsWith("allow") ? 1 : 0);
    if (kind === "allow_tunnel")
      await expect(
        page.getByText("Opaque tunnel bypass", { exact: true }),
      ).toBeVisible();
    if (kind === "allow_requests") {
      await expect(
        page.getByLabel("Credential (optional)").locator("option"),
      ).toHaveCount(3);
      await page.getByLabel("Credential (optional)").selectOption(creds[0]!.id);
    }
    await page.screenshot({
      path: join(screenshots, `${kind}-desktop.png`),
      fullPage: true,
    });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({
      path: join(screenshots, `${kind}-narrow.png`),
      fullPage: true,
    });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    await page.setViewportSize({ width: 1280, height: 900 });
    await page
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    const response = page.waitForResponse(
      (r) =>
        r.url() === `${baseURL}/api/v2/http/grants` &&
        r.request().method() === "POST",
    );
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Apply grant", exact: true })
      .click();
    const r = await response;
    expect(r.status()).toBe(201);
    grants.push((await r.json()) as { id: string; revision: string });
    await expect(
      page.getByRole("heading", { name: "Current policy", exact: true }),
    ).toBeVisible();
    await page.getByRole("link", { name: "Back to HTTP grants" }).click();
  }
  await page.screenshot({
    path: join(screenshots, "table-desktop.png"),
    fullPage: true,
  });
  await page.setViewportSize({ width: 320, height: 800 });
  const createBounds = await page
    .getByRole("link", { name: "Create grant", exact: true })
    .boundingBox();
  const testBounds = await page
    .getByRole("link", { name: "Test access", exact: true })
    .boundingBox();
  expect(createBounds).not.toBeNull();
  expect(testBounds).not.toBeNull();
  expect(
    testBounds!.x >= createBounds!.x + createBounds!.width + 8 ||
      testBounds!.y >= createBounds!.y + createBounds!.height + 8,
  ).toBe(true);
  await page.screenshot({
    path: join(screenshots, "table-320.png"),
    fullPage: true,
  });
  const narrow = await page.evaluate(() => ({
    viewport: innerWidth,
    width: document.documentElement.scrollWidth,
    overflow: [...document.querySelectorAll("body *")]
      .filter((e) => e.getBoundingClientRect().right > innerWidth)
      .map((e) => ({
        tag: e.tagName,
        cls: e.className,
        right: e.getBoundingClientRect().right,
      })),
  }));
  expect(narrow.width, JSON.stringify(narrow)).toBeLessThanOrEqual(
    narrow.viewport,
  );
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.getByRole("link", { name: "Test access", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Test access", level: 1, exact: true }),
  ).toBeVisible();
  for (const policy of ["block", "allow"]) {
    await page
      .getByLabel("Agent", { exact: true })
      .selectOption(createdDefaults[policy]!);
    await page
      .getByLabel("URL", { exact: true })
      .fill("https://public.example.com/");
    await page
      .getByRole("button", { name: "Test access", exact: true })
      .click();
    await expect(
      page.getByRole("heading", {
        name: `Agent default: ${policy === "allow" ? "Allow requests" : "Block requests"}`,
        exact: true,
      }),
    ).toBeVisible();
    await captureState(`preview-default-${policy}`);
  }
  await page.getByLabel("Agent", { exact: true }).selectOption(principal.id);
  await page
    .getByLabel("URL", { exact: true })
    .fill("https://api.example.com/v1?preview-private-canary=1");
  const firstPreviewResponse = page.waitForResponse(
    (r) =>
      r.url() === `${baseURL}/api/v2/http/access-preview` &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Test access", exact: true }).click();
  const firstPreview = await (await firstPreviewResponse).json();
  await expect(
    page.getByRole("heading", { name: "Allowed by request policy" }),
  ).toBeVisible();
  await expect(
    page.getByText(
      `Policy snapshot ${firstPreview.decision.policy_revision} · HTTP default: ${firstPreview.default === "allow" ? "Allow requests" : "Block requests"} (revision ${firstPreview.decision.default_revision}). Test again after policy changes.`,
      { exact: true },
    ),
  ).toBeVisible();
  for (const key of ["principal", "grant", "credential", "credential_grant"]) {
    const fact = page
      .locator("dl.fact-grid > div")
      .filter({
        has: page.getByText(
          key === "principal" ? "Agent" : key.replaceAll("_", " "),
          { exact: true },
        ),
      })
      .locator("dd");
    await expect(fact).toContainText(firstPreview.decision[key].id);
    await expect(fact).toContainText(
      `revision ${firstPreview.decision[key].revision}`,
    );
  }
  for (const fault of [
    "principal",
    "transport",
    "reason",
    "references",
    "default",
    "default-array",
  ]) {
    const previewPath = `${baseURL}/api/v2/http/access-preview`;
    await page.route(previewPath, async (route) => {
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      const body = await response.json();
      if (fault === "principal") body.decision.principal.id = creds[0]!.id;
      if (fault === "transport") body.decision.transport = "tunnel";
      if (fault === "reason") body.decision.allowed = false;
      if (fault === "references") delete body.decision.credential;
      if (fault === "default") body.default = "unknown";
      if (fault === "default-array") body.default = ["allow"];
      await route.fulfill({ response, body: JSON.stringify(body) });
    });
    await page
      .getByRole("button", { name: "Test access", exact: true })
      .click();
    await expect(
      page.getByText("Access preview unavailable", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("heading", { name: "Allowed by request policy" }),
    ).toHaveCount(0);
    if (fault === "principal") await captureState("preview-invalid");
    await page.unroute(previewPath);
  }
  const conflictPolicy = {
    version: 1,
    type: "allow_requests",
    request: {
      origin: { scheme: "https", host: "api.example.com", port: 443 },
      methods: { any: true },
      path: { kind: "any" },
    },
    credential_id: creds[1]!.id,
  };
  await api("/api/v2/http/grants", {
    principal_id: principal.id,
    description: "Conflict",
    expires_at: null,
    policy: conflictPolicy,
  });
  await page.getByRole("button", { name: "Test access", exact: true }).click();
  await expect(
    page.getByRole("heading", {
      name: "Blocked: matching grants require different credentials",
    }),
  ).toBeVisible();
  await captureState("preview-conflict");
  await page.getByLabel("Access type").selectOption("connect");
  await expect(
    page.getByRole("heading", {
      name: "Blocked: matching grants require different credentials",
    }),
  ).toHaveCount(0);
  await page.getByLabel("Host", { exact: true }).fill("tunnel.example.com");
  const connectResponse = page.waitForResponse(
    (r) =>
      r.url() === `${baseURL}/api/v2/http/access-preview` &&
      r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Test access", exact: true }).click();
  const connectEvidence = await (await connectResponse).json();
  expect(connectEvidence.decision.reason).toBe("intercept_required");
  await expect(
    page.getByRole("heading", {
      name: "Interception required; each decrypted request needs its own evaluation",
    }),
  ).toBeVisible();
  await page.goto(`${baseURL}/#/principals/${principal.id}`);
  await expect(page.getByLabel("HTTP default", { exact: true })).toHaveValue(
    "block",
  );
  const editor = page.getByRole("region", {
    name: "Edit agent",
    exact: true,
  });
  await expect(
    editor.getByLabel("HTTP default", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "HTTP access", exact: true }),
  ).toHaveCount(0);
  await expect(page.locator("form form")).toHaveCount(0);
  let principalWrites = 0;
  let defaultWrites = 0;
  const principalBodies: unknown[] = [];
  page.on("request", (request) => {
    if (request.method() !== "PATCH") return;
    if (request.url() === `${baseURL}/api/v2/principals/${principal.id}`) {
      principalWrites++;
      principalBodies.push(request.postDataJSON());
    }
    if (request.url() === `${baseURL}/api/v2/http/defaults/${principal.id}`)
      defaultWrites++;
  });
  await editor
    .getByLabel("Display name", { exact: true })
    .fill("HTTP Policy Agent renamed");
  await page.getByLabel("HTTP default", { exact: true }).selectOption("allow");
  await editor.getByRole("button", { name: "Save agent", exact: true }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm).toContainText("HTTP Policy Agent renamed");
  await expect(confirm).toContainText("Allow requests");
  await captureState("principal-combined-confirm");
  await confirm.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(principalWrites).toBe(0);
  expect(defaultWrites).toBe(0);
  await editor.getByRole("button", { name: "Save agent", exact: true }).click();
  await confirm
    .getByRole("button", { name: "Save agent changes", exact: true })
    .click();
  await expect(
    page.getByText("Agent settings saved.", { exact: true }),
  ).toBeVisible();
  expect(principalWrites).toBe(1);
  expect(defaultWrites).toBe(0);
  expect(principalBodies[0]).toEqual({
    display_name: "HTTP Policy Agent renamed",
    http_default: "allow",
  });
  const saved = await (
    await api(`/api/v2/principals/${principal.id}`, undefined, "GET")
  ).json();
  expect(saved.http_default).toBe("allow");
  await expect(
    page.getByRole("region", { name: "Agent details", exact: true }),
  ).toContainText("Allow requests");
  expect(saved.display_name).toBe("HTTP Policy Agent renamed");
  await captureState("principal-default");
  await page.getByLabel("HTTP default", { exact: true }).selectOption("block");
  const principalPath = `${baseURL}/api/v2/principals/${principal.id}`;
  let concurrentWrite = false;
  await page.route(principalPath, async (route) => {
    if (route.request().method() === "PATCH" && !concurrentWrite) {
      concurrentWrite = true;
      const concurrent = await api(
        `/api/v2/principals/${principal.id}`,
        { display_name: "Concurrent rename" },
        "PATCH",
        `"principal-${principal.id}-${saved.revision}"`,
      );
      expect(concurrent.status()).toBe(200);
    }
    await route.continue();
  });
  await editor.getByRole("button", { name: "Save agent", exact: true }).click();
  const [staleResponse] = await Promise.all([
    page.waitForResponse(
      (response) =>
        response.url() === principalPath &&
        response.request().method() === "PATCH",
    ),
    confirm
      .getByRole("button", { name: "Save agent changes", exact: true })
      .click(),
  ]);
  expect(staleResponse.status()).toBe(412);
  await expect(
    page.getByText("Review current agent settings", { exact: true }),
  ).toBeVisible();
  await expect(
    editor.getByRole("button", { name: "Save agent", exact: true }),
  ).toBeDisabled();
  await expect(page.getByLabel("HTTP default", { exact: true })).toHaveValue(
    "block",
  );
  await expect(
    page.getByText(/Current values: Concurrent rename/),
  ).toBeVisible();
  await captureState("principal-conflict");
  expect(concurrentWrite).toBe(true);
  await page.unroute(principalPath);
  await page
    .getByRole("button", { name: "Use reviewed revision; keep draft" })
    .click();
  await editor.getByRole("button", { name: "Save agent", exact: true }).click();
  await confirm
    .getByRole("button", { name: "Save agent changes", exact: true })
    .click();
  await expect(
    page.getByText("Agent settings saved.", { exact: true }),
  ).toBeVisible();
  const afterConflict = await (
    await api(`/api/v2/principals/${principal.id}`, undefined, "GET")
  ).json();
  expect(afterConflict.display_name).toBe("Concurrent rename");
  expect(afterConflict.http_default).toBe("block");
  await expect(
    page.getByRole("region", { name: "Agent details", exact: true }),
  ).toContainText("Block requests");
  expect(principalBodies.at(-1)).toEqual({ http_default: "block" });
  expect(defaultWrites).toBe(0);
  for (const fault of ["missing-etag", "wrong-etag", "wrong-principal"]) {
    const routePath = `${baseURL}/api/v2/principals/${principal.id}`;
    const previous = await page
      .getByLabel("HTTP default", { exact: true })
      .inputValue();
    const proposed = previous === "allow" ? "block" : "allow";
    let submissions = 0;
    await page.route(routePath, async (route) => {
      if (route.request().method() !== "PATCH") {
        await route.continue();
        return;
      }
      submissions++;
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      const headers = { ...response.headers() };
      const body = await response.json();
      if (fault === "missing-etag") delete headers.etag;
      if (fault === "wrong-etag")
        headers.etag = `"principal-${principal.id}-999999"`;
      if (fault === "wrong-principal") {
        body.id = creds[0]!.id;
        headers.etag = `"principal-${body.id}-${body.revision}"`;
      }
      await route.fulfill({ status: 200, headers, body: JSON.stringify(body) });
    });
    await page
      .getByLabel("HTTP default", { exact: true })
      .selectOption(proposed);
    await editor
      .getByRole("button", { name: "Save agent", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Save agent changes", exact: true })
      .click();
    await expect(
      page.getByText("Agent outcome is unknown", { exact: true }),
    ).toBeVisible();
    await expect(
      editor.getByRole("button", { name: "Save agent", exact: true }),
    ).toBeDisabled();
    if (fault === "missing-etag") await captureState("default-uncertain");
    expect(submissions).toBe(1);
    const observed = await (
      await api(`/api/v2/principals/${principal.id}`, undefined, "GET")
    ).json();
    expect(observed.http_default).toBe(proposed);
    await page.unroute(routePath);
    await page.reload();
    await expect(page.getByLabel("HTTP default", { exact: true })).toHaveValue(
      proposed,
    );
  }
  await expect(
    page.getByRole("link", { name: "HTTP grants for this principal" }),
  ).toHaveCount(0);
  await page.locator('#primary-navigation a[href="#/http/grants"]').click();
  await expect(
    page.getByRole("link", { name: "Test allow_requests", exact: true }),
  ).toBeVisible();
  const editPath = `${baseURL}/api/v2/http/grants/${grants[0]!.id}`;
  await page.route(editPath, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      body: '{"malformed":true}',
    }),
  );
  await page
    .getByRole("link", { name: "Test block_destination", exact: true })
    .click();
  await expect(
    page.getByText("HTTP grant unavailable", { exact: true }),
  ).toBeVisible();
  await captureState("detail-error");
  await page.unroute(editPath);
  await page.reload();
  await expect(
    page.getByRole("heading", { name: "Edit HTTP grant" }),
  ).toBeVisible();
  await page.getByLabel("Description (optional)").fill("Retained local draft");
  const currentResponse = await api(
    `/api/v2/http/grants/${grants[0]!.id}`,
    undefined,
    "GET",
  );
  const current = await currentResponse.json();
  await api(
    `/api/v2/http/grants/${grants[0]!.id}`,
    {
      principal_id: principal.id,
      description: "Concurrent edit",
      policy: current.policy,
      expires_at: null,
    },
    "PATCH",
    currentResponse.headers().etag,
  );
  await expect(page.getByText("Policy changed", { exact: true })).toBeVisible();
  await expect(page.getByLabel("Description (optional)")).toHaveValue(
    "Retained local draft",
  );
  await expect(
    page.getByRole("button", { name: "Review changes", exact: true }),
  ).toBeDisabled();
  await captureState("detail-stale");
  await page.getByRole("button", { name: "Use current revision" }).click();
  for (const fault of [
    "identity",
    "reversed-times",
    "noncanonical-time",
    "duplicate-member",
    "invalid-idna",
  ]) {
    const draft =
      fault === "identity" ? "Retained local draft" : `Retained draft ${fault}`;
    if (fault !== "identity") {
      await page.reload();
      await expect(page.getByLabel("Description (optional)")).toBeVisible();
      await page.getByLabel("Description (optional)").fill(draft);
    }
    let editCalls = 0;
    await page.route(editPath, async (route) => {
      if (route.request().method() !== "PATCH") {
        await route.continue();
        return;
      }
      editCalls++;
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      const malformed = await response.json();
      if (fault === "identity") malformed.id = creds[0]!.id;
      if (fault === "invalid-idna")
        malformed.policy.destination.host = "xn--a.example";
      if (fault === "reversed-times")
        malformed.updated_at = "2000-01-01T00:00:00.000000000Z";
      if (fault === "noncanonical-time")
        malformed.updated_at = malformed.updated_at.replace("Z", "+00:00");
      await route.fulfill({
        response,
        headers: {
          ...response.headers(),
          etag: `"http-grant-${malformed.id}-${malformed.revision}"`,
        },
        body:
          fault === "duplicate-member"
            ? `{"revision":${JSON.stringify(malformed.revision)},${JSON.stringify(malformed).slice(1)}`
            : JSON.stringify(malformed),
      });
    });
    await page
      .getByRole("button", { name: "Review changes", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Apply grant", exact: true })
      .click();
    await expect(
      page.getByText("Outcome uncertain", { exact: true }),
    ).toBeVisible();
    await expect(
      page.getByRole("button", { name: "Review changes", exact: true }),
    ).toBeDisabled();
    await expect(page.getByLabel("Description (optional)")).toHaveValue(draft);
    await expect(
      page.getByRole("heading", { name: draft, exact: true }),
    ).toBeVisible();
    expect(editCalls).toBe(1);
    const observed = await (
      await api(`/api/v2/http/grants/${grants[0]!.id}`, undefined, "GET")
    ).json();
    expect(observed.description).toBe(draft);
    await captureState(
      fault === "identity" ? "detail-uncertain" : `detail-${fault}-uncertain`,
    );
    await page.unroute(editPath);
  }
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    ["http-grant-material-canary"],
    true,
  );
  const stored = await page.evaluate(() => ({
    local: Object.keys(localStorage)
      .map((k) => localStorage.getItem(k))
      .join(""),
    session: Object.keys(sessionStorage).join(""),
  }));
  expect(JSON.stringify(stored)).not.toContain("preview-private-canary");
  console.log(
    JSON.stringify({
      event: "http_grants_complete",
      requests: requestCount(),
      screenshots,
    }),
  );
}
