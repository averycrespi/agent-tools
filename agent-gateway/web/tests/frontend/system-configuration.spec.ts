import { readFile } from "node:fs/promises";
import { test, syntheticBearer } from "./fixture.ts";
import { expect } from "@playwright/test";
import { capture, registerCapture } from "./capture.ts";
import fixtures from "../configuration-fixtures.json" with { type: "json" };
type Definition = {
  format: string;
  version: number;
  kind: string;
  key?: string;
  reserved?: boolean;
  spec: Record<string, unknown> | null;
};
const resources: Definition[] = Object.values(fixtures).map(
  (v) => JSON.parse(JSON.stringify(v)) as Definition,
);
resources.push({
  format: "agent-gateway-config",
  version: 2,
  kind: "git_routing",
  spec: { origins: [] },
});
resources.push({
  format: "agent-gateway-config",
  version: 2,
  kind: "mcp_server",
  key: "deleted_target",
  reserved: true,
  spec: null,
});
resources.push({
  ...resources[2]!,
  key: "expired_duplicate",
  spec: { ...resources[2]!.spec, expires_at: "2020-01-01T00:00:00.000000000Z" },
});
resources[4]!.spec!.agent = { kind: "agent", key: "portable" };
const bundle = (resources: Definition[]) =>
  JSON.stringify(
    { format: "agent-gateway-config", version: 2, resources },
    null,
    2,
  );
const raw = bundle(resources).replace(
  '"constraint": null',
  '"constraint": {"version":2,"equals":{"/integer":9007199254740993,"/fraction":1.0,"/exponent":1e+2}}',
);

test("config-system", async ({ page, context, frontend }) => {
  registerCapture(page, "config-system");
  let expectedRaw = raw;
  let exportedRaw = raw;
  let imports = 0,
    validations = 0,
    exports = 0;
  let mode: "success" | "conflict" | "uncertain" | "incomplete" = "success";
  let rejectedPreview = false;
  let lost = false;
  let release: (() => void) | undefined;
  let gate: Promise<void> | undefined;
  await page.route("**/api/v2/config/**", async (route) => {
    const request = route.request();
    if (request.method() === "GET") {
      exports++;
      return route.fulfill({
        contentType: "application/json",
        body: exportedRaw,
      });
    }
    expect(request.postData()).toBe(expectedRaw);
    expect((await request.allHeaders())["x-csrf-token"]).toBe("A".repeat(43));
    expect((await request.allHeaders()).origin).toBe(frontend.origin);
    if (gate) await gate;
    if (lost)
      return route.fulfill({
        status: 401,
        contentType: "application/problem+json",
        json: {
          status: 401,
          code: "authentication_required",
          title: "Sign in required",
        },
      });
    const submitted = (JSON.parse(expectedRaw) as { resources: Definition[] })
      .resources;
    if (request.url().endsWith("/preview")) {
      validations++;
      if (rejectedPreview)
        return route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          json: {
            status: 409,
            code: "conflict",
            title: "REJECTED_PAYLOAD_CANARY",
          },
        });
      return route.fulfill({
        json: {
          advisory: true,
          creations: submitted.map((v) => ({
            kind: v.kind,
            ...(v.key ? { key: v.key } : {}),
            reserved: v.reserved === true,
            dependencies: Object.values(v.spec ?? {}).filter(
              (ref): ref is Record<string, unknown> =>
                !!ref &&
                typeof ref === "object" &&
                "kind" in ref &&
                ["agent", "git_repository", "gateway_mcp"].includes(
                  String(ref.kind),
                ),
            ),
            requirements:
              v.kind === "agent"
                ? ["issue_agent_credential"]
                : v.kind === "mcp_server" && !v.reserved
                  ? ["supply_static_credentials", "activate_after_commit"]
                  : v.kind.includes("credential")
                    ? ["supply_credential_value"]
                    : v.kind === "git_routing"
                      ? ["initialize_untouched_routing_seed"]
                      : [],
          })),
        },
      });
    }
    imports++;
    if (mode === "conflict")
      return route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        json: {
          status: 409,
          code: "conflict",
          title: "REJECTED_PAYLOAD_CANARY",
        },
      });
    if (mode === "uncertain") return route.abort("failed");
    return route.fulfill({
      status: 201,
      json: {
        configuration: "committed",
        activation: "pending",
        creations: (mode === "incomplete" ? submitted.slice(1) : submitted).map(
          (v, i) => ({
            kind: v.kind,
            ...(v.key
              ? {
                  key: v.key,
                  id: `01ARZ3NDEKTSV4RRFFQ69G5F${String(i).padStart(2, "0")}`,
                }
              : {}),
            reserved: v.reserved === true,
          }),
        ),
      },
    });
  });
  const signIn = async () => {
    await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
    await page.getByTestId("sign-in-submit").click();
    await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
      "data-session-lifecycle",
      "authenticated",
    );
  };
  const open = async () => {
    await page.goto(`${frontend.origin}/#/system?tab=configuration`);
    await expect(
      page.getByRole("heading", { name: "Import configuration", exact: true }),
    ).toBeVisible();
  };
  await signIn();
  await page.getByRole("link", { name: "System", exact: true }).click();
  await page.getByRole("link", { name: "Configuration", exact: true }).click();
  await expect(page).toHaveURL(/#\/system\?tab=configuration$/);
  const input = page.getByLabel("Configuration JSON", { exact: true });
  const validate = async () => {
    await page.getByRole("button", { name: "Validate and preview" }).click();
    await expect(page.getByLabel("Import preview")).toBeVisible();
  };
  const confirm = async () => {
    await page.getByRole("button", { name: "Import", exact: true }).click();
    await page
      .getByRole("button", { name: "Confirm import", exact: true })
      .click();
  };
  await expect(
    page.getByRole("button", { name: "Copy config", exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Download config", exact: true }),
  ).toBeVisible();
  await expect(input).toHaveAttribute(
    "placeholder",
    "Paste a version 2 configuration bundle",
  );
  await capture(page, "input", true);
  await input.fill(raw.replace('"version": 2', '"version": 1'));
  await page.getByRole("button", { name: "Validate and preview" }).click();
  await expect(page.getByText(/Version 1 is not supported/)).toBeVisible();
  expect(validations).toBe(0);
  expect(imports).toBe(0);
  await capture(page, "version-refused");
  await input.fill(raw);
  await page.getByLabel("Upload JSON file", { exact: true }).setInputFiles({
    name: "large.json",
    mimeType: "application/json",
    buffer: Buffer.alloc(1024 * 1024 + 1, 32),
  });
  await expect(
    page.getByText("Configuration must be at most 1 MiB.", { exact: true }),
  ).toBeVisible();
  await expect(input).toHaveValue(raw);
  await capture(page, "file-refused");
  await page.getByLabel("Upload JSON file", { exact: true }).setInputFiles({
    name: "source.json",
    mimeType: "application/json",
    buffer: Buffer.from(raw),
  });
  await expect(input).toHaveValue(raw);
  gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.getByRole("button", { name: "Validate and preview" }).click();
  await expect(
    page.getByText("Validating configuration", { exact: true }),
  ).toBeVisible();
  await expect(input).toBeDisabled();
  await capture(page, "validating");
  release!();
  gate = undefined;
  await expect(page.getByLabel("Import preview")).toBeVisible();
  expect(imports).toBe(0);
  await expect(
    page.getByLabel("Import preview").locator("ol > li"),
  ).toHaveCount(resources.length);
  for (const phrase of [
    "Same submission",
    "Existing target definition",
    "Existing Gateway self-service",
    "Reserve deleted key permanently",
    "Supply static credentials",
    "Initialize untouched routing seed",
  ])
    await expect(page.getByLabel("Import preview")).toContainText(phrase);
  await page.getByText("Exact configuration JSON", { exact: true }).click();
  for (const token of ["9007199254740993", "1.0", "1e+2"])
    await expect(page.locator("pre.inert-json")).toContainText(token);
  await page.getByText("Exact configuration JSON", { exact: true }).click();
  await capture(page, "preview", true);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText(
    `${resources.length} definitions`,
  );
  await capture(page, "confirmation", true);
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(imports).toBe(0);
  mode = "conflict";
  await confirm();
  await expect(
    page.getByText(/Nothing was imported. Inspect the target/),
  ).toBeVisible();
  await expect(input).toHaveValue(raw);
  await expect(input).toBeEditable();
  await expect(page.getByLabel("Import preview")).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(
    "REJECTED_PAYLOAD_CANARY",
  );
  await capture(page, "conflict", true);
  rejectedPreview = true;
  await page.getByRole("button", { name: "Validate and preview" }).click();
  await expect(page.getByText(/A key, permanent reservation/)).toBeVisible();
  await capture(page, "validation-failed");
  rejectedPreview = false;
  // The complete bounded graph is rendered, including the last resource, not a first-page summary.
  const many = Array.from({ length: 500 }, (_, i) => ({
    ...resources[0]!,
    key: `large_${i}`,
  }));
  expectedRaw = bundle(many);
  await page.getByLabel("Upload JSON file", { exact: true }).setInputFiles({
    name: "large-bundle.json",
    mimeType: "application/json",
    buffer: Buffer.from(expectedRaw),
  });
  await expect(input).toHaveValue(expectedRaw);
  await validate();
  await expect(
    page.getByLabel("Import preview").locator("ol > li"),
  ).toHaveCount(500);
  await expect(
    page.getByLabel("Import preview").locator("ol > li").last(),
  ).toContainText("large_499");
  expectedRaw = raw;
  await input.fill(raw);
  await validate();
  mode = "success";
  gate = new Promise<void>((resolve) => {
    release = resolve;
  });
  await confirm();
  await expect(
    page.getByText("Creating configuration", { exact: true }),
  ).toBeVisible();
  await expect(input).toBeDisabled();
  await capture(page, "importing");
  release!();
  gate = undefined;
  await expect(
    page.getByText("Configuration creation committed", { exact: true }),
  ).toBeVisible();
  await expect(page.locator(".config-creation-list > li")).toHaveCount(
    resources.length,
  );
  await expect(
    page.getByText(/Enabled resources are retained even if startup fails/),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Import", exact: true }),
  ).toHaveCount(0);
  await capture(page, "committed", true);
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.getByRole("button", { name: "Copy config", exact: true }).click();
  await expect(page.getByText("Config copied.", { exact: true })).toBeVisible();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(raw);
  const download = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "Download config", exact: true })
    .click();
  const file = await download;
  expect(file.suggestedFilename()).toBe("gateway-config.json");
  expect(await readFile((await file.path())!, "utf8")).toBe(raw);
  expect(exports).toBe(2);
  // Whole exports are not truncated to the separate browser input bound.
  exportedRaw = bundle([
    {
      ...resources[0]!,
      spec: {
        ...resources[0]!.spec,
        display_name: "x".repeat(1024 * 1024 + 1),
      },
    },
    ...resources.slice(1),
  ]);
  const largeDownload = page.waitForEvent("download");
  await page
    .getByRole("button", { name: "Download config", exact: true })
    .click();
  expect(await readFile((await (await largeDownload).path())!, "utf8")).toBe(
    exportedRaw,
  );
  exportedRaw = raw;
  await capture(page, "export");
  await page.reload();
  await input.fill(raw);
  await validate();
  mode = "uncertain";
  await confirm();
  await expect(
    page.getByText("Creation outcome unknown", { exact: true }),
  ).toBeVisible();
  await expect(input).toBeDisabled();
  const attempts = imports;
  await expect(
    page.getByRole("button", { name: "Validate and preview" }),
  ).toBeDisabled();
  await expect(
    page.getByRole("button", { name: "Import", exact: true }),
  ).toBeDisabled();
  await page.keyboard.press("Escape");
  expect(imports).toBe(attempts);
  await capture(page, "uncertain", true);
  await page.reload();
  await input.fill(raw);
  await validate();
  mode = "incomplete";
  await confirm();
  await expect(
    page.getByText("Creation outcome unknown", { exact: true }),
  ).toBeVisible();
  await expect(input).toBeDisabled();
  await page.reload();
  await input.fill(raw);
  await page.route("**/api/v2/admin-sessions/current", (route) =>
    lost
      ? route.fulfill({
          status: 401,
          contentType: "application/problem+json",
          json: {
            status: 401,
            code: "authentication_required",
            title: "Sign in required",
          },
        })
      : route.fallback(),
  );
  lost = true;
  await page.getByRole("button", { name: "Validate and preview" }).click();
  await expect(page.getByTestId("admin-bearer-input")).toBeVisible();
  await expect(input).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText("9007199254740993");
  lost = false;
  await signIn();
  await open();
  await expect(input).toHaveValue("");
  await capture(page, "session-cleared");
});
