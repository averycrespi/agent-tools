import { expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import {
  configurationKindLabels,
  resourceDestinations,
  type ConfigurationKind,
  type ResourceKind,
} from "../../src/config-contract.ts";

// The Go owner exported source from a separate real installation and stopped it.
// No intercepted APIs or fabricated successful persistence in this scenario.
export async function exerciseSystemConfiguration(
  page: Page,
  source: string,
): Promise<void> {
  const api = async (path: string, body?: string) =>
    page.evaluate(
      async ({ path, body }) => {
        const bootstrap = await fetch("/api/v2/admin-sessions/current", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: "{}",
        });
        const { csrf_token } = await bootstrap.json();
        const response = await fetch(path, {
          method: body === undefined ? "GET" : "POST",
          headers: {
            "Content-Type": "application/json",
            "X-CSRF-Token": csrf_token,
          },
          ...(body === undefined ? {} : { body }),
        });
        return { status: response.status, raw: await response.text() };
      },
      { path, body },
    );
  const clean = await api("/api/v2/config/export");
  expect(clean.status).toBe(200);
  expect(
    JSON.parse(clean.raw).resources.map((v: { kind: string }) => v.kind),
  ).toEqual(["git_routing"]);
  await page.goto("/#/system");
  await page.getByRole("link", { name: "Configuration", exact: true }).click();
  await expect(page).toHaveURL(/#\/system\?tab=configuration$/);
  const input = page.getByLabel("Configuration JSON", { exact: true });
  const agent = (key: string) => ({
    format: "agent-gateway-config",
    version: 2,
    kind: "agent",
    key,
    spec: {
      display_name: key,
      state: "active",
      visibility: "allowed-only",
      http_default: "block",
    },
  });
  const conflict = JSON.stringify({
    format: "agent-gateway-config",
    version: 2,
    resources: [agent("bulk_race"), agent("bulk_must_rollback")],
  });
  await input.fill(conflict);
  await page.getByRole("button", { name: "Validate and preview" }).click();
  await expect(page.getByLabel("Import preview")).toContainText(
    "2 definitions",
  );
  const winner = await api(
    "/api/v2/config/import",
    JSON.stringify(agent("bulk_race")),
  );
  expect(winner.status).toBe(201);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await page
    .getByRole("button", { name: "Confirm import", exact: true })
    .click();
  await expect(
    page.getByText(/Nothing was imported. Inspect the target/),
  ).toBeVisible();
  await expect(input).toHaveValue(conflict);
  await expect(input).toBeEditable();
  const after = await api("/api/v2/config/export");
  expect(
    JSON.parse(after.raw)
      .resources.filter((v: { kind: string }) => v.kind === "agent")
      .map((v: { key: string }) => v.key),
  ).toEqual(["bulk_race"]);

  await page.getByLabel("Upload JSON file", { exact: true }).setInputFiles({
    name: "whole-source.json",
    mimeType: "application/json",
    buffer: Buffer.from(source),
  });
  await expect(input).toHaveValue(source);
  await page.getByRole("button", { name: "Validate and preview" }).click();
  const resources = JSON.parse(source).resources as {
    kind: string;
    key?: string;
    reserved?: boolean;
  }[];
  await expect(
    page.getByLabel("Import preview").locator("ol > li"),
  ).toHaveCount(resources.length);
  await expect(page.getByLabel("Import preview")).toContainText(
    "Same submission",
  );
  await expect(page.getByLabel("Import preview")).toContainText(
    "Initialize untouched routing seed",
  );
  const scan = async () => {
    const result = await new AxeBuilder({ page }).analyze();
    expect(
      result.violations.filter(
        (v) => v.impact === "serious" || v.impact === "critical",
      ),
    ).toEqual([]);
  };
  await scan();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(
    await page.evaluate(
      () => document.documentElement.scrollWidth <= innerWidth,
    ),
  ).toBe(true);
  await scan();
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await scan();
  const committing = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v2/config/import") &&
      r.request().method() === "POST",
  );
  await page
    .getByRole("button", { name: "Confirm import", exact: true })
    .click();
  expect((await committing).status()).toBe(201);
  await expect(
    page.getByText("Configuration creation committed", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      /Activation pending. Enabled resources are retained even if startup fails/,
    ),
  ).toBeVisible();
  // Observe the decoded receipt through its actual UI and ID-based links, then
  // independently verify durable configuration below, not Chromium's body cache.
  const rows = page.locator(".config-creation-list > li");
  await expect(rows).toHaveCount(resources.length);
  const createdIDs = new Map<string, string>();
  for (const [index, definition] of resources.entries()) {
    const row = rows.nth(index);
    await expect(row.locator("strong")).toHaveText(
      `${configurationKindLabels[definition.kind as ConfigurationKind]}${definition.key ? ` / ${definition.key}` : ""}`,
    );
    if (definition.reserved || definition.kind === "git_routing") continue;
    const href = await row
      .getByRole("link", { name: "View created resource", exact: true })
      .getAttribute("href");
    const id = /\/([0-7][0-9A-HJKMNP-TV-Z]{25})(?:\?|$)/.exec(href ?? "")?.[1];
    expect(id).toBeTruthy();
    expect(href).toBe(
      `#/${resourceDestinations[definition.kind as ResourceKind]}/${id}${definition.kind === "mcp_server" ? "?tab=status" : ""}`,
    );
    createdIDs.set(`${definition.kind}/${definition.key}`, id!);
  }
  await expect(
    page.getByRole("button", { name: "Confirm import", exact: true }),
  ).toHaveCount(0);
  await expect(
    page.getByRole("button", { name: "Import", exact: true }),
  ).toHaveCount(0);
  await scan();
  const whole = await api("/api/v2/config/export");
  expect(whole.status).toBe(200);
  const expected = [...resources, agent("bulk_race")].sort((a, b) =>
    `${a.kind}/${a.key ?? ""}`.localeCompare(`${b.kind}/${b.key ?? ""}`),
  );
  const actual = JSON.parse(whole.raw).resources.sort(
    (a: { kind: string; key?: string }, b: { kind: string; key?: string }) =>
      `${a.kind}/${a.key ?? ""}`.localeCompare(`${b.kind}/${b.key ?? ""}`),
  );
  expect(actual).toEqual(expected);
  for (const token of ["9007199254740993", "1.0", "1e+2"])
    expect(whole.raw).toContain(token);
  for (const forbidden of [
    '"runtime"',
    '"history"',
    '"revision"',
    '"installation_id"',
    '"operation_id"',
  ])
    expect(whole.raw).not.toContain(forbidden);
  const download = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "Download config", exact: true })
    .click();
  expect((await download).suggestedFilename()).toBe("gateway-config.json");
  await expect(
    page.getByText("Config downloaded.", { exact: true }),
  ).toBeVisible();
  const agentRead = await api(
    `/api/v2/principals/${createdIDs.get("agent/builder")}`,
  );
  expect(JSON.parse(agentRead.raw).credential).toBeNull();
  for (const key of ["needs-material", "startup-fails"]) {
    await page.goto(
      `/#/mcp/servers/${createdIDs.get(`mcp_server/${key}`)}?tab=status`,
    );
    await expect(page.getByTestId("server-context")).toContainText(
      key === "needs-material" ? "Needs material" : "Startup fails",
    );
    await expect(page.getByTestId("server-context")).not.toContainText("Ready");
    if (key === "needs-material") {
      await page
        .getByRole("link", { name: "Authentication", exact: true })
        .click();
      await expect(
        page.getByLabel("Secret slot primary", { exact: true }),
      ).toHaveValue("");
    } else await expect(page.getByText(/^Reason:/)).toBeVisible();
  }
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.goto("/#/system?tab=configuration");
  await expect(
    page.getByRole("heading", { name: "Import configuration", exact: true }),
  ).toBeVisible();
  const persisted = await api("/api/v2/config/export");
  expect(JSON.parse(persisted.raw)).toEqual(JSON.parse(whole.raw));
}
