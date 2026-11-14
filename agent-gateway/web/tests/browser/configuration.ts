import { expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";

// Real Gateway only: no intercepted APIs, synthetic listeners or credentials.
export async function exerciseResourceConfiguration(page: Page): Promise<void> {
  const definition = {
    format: "agent-gateway-config",
    version: 2,
    kind: "agent",
    key: "browser_import_race",
    spec: {
      display_name: "Browser imported agent",
      state: "active",
      visibility: "all",
      http_default: "block",
    },
  };
  let raw = JSON.stringify(definition);
  await page.goto("/#/agents/import");
  const input = page.getByLabel("Configuration JSON", { exact: true });
  await input.fill(raw);
  await page.getByRole("button", { name: "Validate and preview" }).click();
  await expect(
    page.getByRole("heading", { name: "Creation preview" }),
  ).toBeVisible();
  // A separately committed concurrent creation invalidates the advisory preview.
  const race = await page.evaluate(async (document) => {
    const session = await fetch("/api/v2/admin-sessions/current", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const { csrf_token } = await session.json();
    const response = await fetch("/api/v2/config/import", {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrf_token,
      },
      body: document,
    });
    return { status: response.status, value: await response.json() };
  }, raw);
  expect(race.status).toBe(201);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await page
    .getByRole("button", { name: "Confirm import", exact: true })
    .click();
  await expect(
    page.getByText(/a key or dependency changed since preview/),
  ).toBeVisible();
  await expect(input).toHaveValue(raw);
  await expect(input).toBeEditable();
  definition.key = "browser_import_fresh";
  raw = JSON.stringify(definition);
  await input.fill(raw);
  await page.getByRole("button", { name: "Validate and preview" }).click();
  await expect(page.getByLabel("Import preview")).toContainText(
    "Issue agent credential",
  );
  const previewScan = await new AxeBuilder({ page }).analyze();
  expect(
    previewScan.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    ),
  ).toEqual([]);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  const dialogScan = await new AxeBuilder({ page }).analyze();
  expect(
    dialogScan.violations.filter(
      (v) => v.impact === "serious" || v.impact === "critical",
    ),
  ).toEqual([]);
  const committed = page.waitForResponse(
    (response) =>
      response.url().endsWith("/api/v2/config/import") &&
      response.request().method() === "POST",
  );
  await page
    .getByRole("button", { name: "Confirm import", exact: true })
    .click();
  const response = await committed;
  expect(response.status()).toBe(201);
  const result = await response.json();
  expect(result.configuration).toBe("committed");
  expect(result.creations).toHaveLength(1);
  await expect(
    page.getByText("Resource creation committed", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(/Required next: Issue agent credential/),
  ).toBeVisible();
  await page.getByRole("button", { name: "View created resource" }).click();
  await expect(page).toHaveURL(
    new RegExp(`#/agents/${result.creations[0].id}$`),
  );
  await expect(
    page.getByRole("region", { name: "Resource configuration" }),
  ).toContainText(definition.key);
  await page.reload();
  await expect(
    page.getByRole("region", { name: "Resource configuration" }),
  ).toContainText(definition.key);
  const exported = page.waitForResponse((response) =>
    response.url().endsWith(`/api/v2/config/resources/agent/${definition.key}`),
  );
  const download = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "Download config", exact: true })
    .click();
  const exportedDefinition = await (await exported).json();
  expect(exportedDefinition).toEqual(definition);
  expect((await download).suggestedFilename()).toBe(
    `agent-${definition.key}.json`,
  );
  // Import is explicit graph creation: unlike ordinary Create agent it adds no default grant.
  const grants = await page.evaluate(async () => {
    const bootstrap = await fetch("/api/v2/admin-sessions/current", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const { csrf_token } = await bootstrap.json();
    const response = await fetch("/api/v2/mcp/grants?limit=50", {
      headers: { "X-CSRF-Token": csrf_token },
    });
    if (response.status !== 200) throw Error("Authenticated grant read failed");
    return (await response.json()).items;
  });
  expect(
    grants.filter(
      (value: { principal_id: string }) =>
        value.principal_id === result.creations[0].id,
    ),
  ).toEqual([]);
  for (const missing of [true, false]) {
    const key = missing ? "browser_needs_material" : "browser_startup_fails";
    const server = {
      format: "agent-gateway-config",
      version: 2,
      kind: "mcp_server",
      key,
      spec: {
        display_name: missing ? "Needs material" : "Startup fails",
        enabled: true,
        deleted: false,
        transport: {
          kind: "stdio",
          executable: missing ? "/bin/cat" : "/bin/false",
          arguments: [],
          working_directory: "/tmp",
          environment: {},
          secret_environment: missing ? { TOKEN: "primary" } : {},
        },
        credentials: {
          static: missing ? { primary: `mcp/${key}/static/primary` } : {},
          oauth_client_secret: null,
          oauth_authorization: false,
        },
      },
    };
    await page.goto("/#/mcp/servers/import");
    await input.fill(JSON.stringify(server));
    await page.getByRole("button", { name: "Validate and preview" }).click();
    await expect(page.getByLabel("Import preview")).toContainText(
      "Activate after commit",
    );
    if (missing)
      await expect(page.getByLabel("Import preview")).toContainText(
        "Supply static credentials",
      );
    await page.getByRole("button", { name: "Import", exact: true }).click();
    await page
      .getByRole("button", { name: "Confirm import", exact: true })
      .click();
    await expect(
      page.getByText("Resource creation committed", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText(/Activation pending/)).toBeVisible();
    if (missing)
      await expect(
        page.getByText(/Required next: Supply static credentials/),
      ).toBeVisible();
    await expect(
      page.getByRole("link", { name: "Set up authentication" }),
    ).toBeVisible();
    await page.getByRole("button", { name: "View created resource" }).click();
    await expect(
      page.getByRole("region", { name: "Resource configuration" }),
    ).toContainText(key);
    if (missing) {
      await expect(page.getByTestId("server-context")).toContainText(
        "Not configured",
      );
      await page
        .getByRole("link", { name: "Authentication", exact: true })
        .click();
      await expect(
        page.getByLabel("Secret slot primary", { exact: true }),
      ).toHaveValue("");
    } else {
      await expect(page.getByText(/^Reason:/)).toBeVisible();
      await expect(page.getByTestId("server-context")).not.toContainText(
        "Ready",
      );
    }
  }
}
