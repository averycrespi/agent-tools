import { type BrowserContext, type Page } from "@playwright/test";
import {
  assertClosedStorage,
  assertSecretAbsent,
  browserStorage,
  fail,
  waitForLifecycle,
} from "./shared.ts";
import {
  descriptorReadFixture,
  serverReadFixture,
  serverReadIDs,
} from "./fixtures.ts";

export async function runServerManagementCanary(
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

export async function runServerCreateUpdate(
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

export async function runServerOperations(
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

export async function runServerDisconnectDelete(
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

export async function runAuthFlows(
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

export async function runServerCredentials(
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

export async function runServerCatalogReads(
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
