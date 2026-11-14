import { readFile } from "node:fs/promises";
import { test, syntheticBearer } from "./fixture.ts";
import { expect } from "@playwright/test";
import { capture, registerCapture } from "./capture.ts";
import {
  resourceKinds,
  resourceDestinations,
} from "../../src/config-contract.ts";
import { serverReadFixture } from "../browser/fixtures.ts";
import definitions from "../configuration-fixtures.json" with { type: "json" };

const id = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
const readerID = "01ARZ3NDEKTSV4RRFFQ69G5FA1";
const projectID = "01ARZ3NDEKTSV4RRFFQ69G5FA2";
const agent = {
  key: "portable",
  id,
  display_name: "Imported agent",
  state: "active",
  visibility: "all",
  http_default: "block",
  revision: "1",
  credential_revision: "0",
  credential: null,
  created_at: "2026-10-01T12:00:00Z",
  updated_at: "2026-10-01T12:00:00Z",
};
for (const kind of resourceKinds) {
  test(`config-${kind.replaceAll("_", "-")}`, async ({
    page,
    context,
    frontend,
  }) => {
    registerCapture(page, `config-${kind.replaceAll("_", "-")}`);
    let imports = 0;
    let validations = 0;
    let responseMode: "success" | "conflict" | "uncertain" = "success";
    let previewGate: Promise<void> | undefined;
    let importGate: Promise<void> | undefined;
    let releasePreview: (() => void) | undefined;
    let releaseImport: (() => void) | undefined;
    let rejectPreview = false;
    try {
      const definition = definitions[kind];
      const raw = JSON.stringify(definition, null, 2);
      await page.route("**/api/v2/principals?**", (route) =>
        route.fulfill({
          json: {
            items: [
              {
                ...agent,
                id: readerID,
                key: "reader",
                display_name: "Existing reader",
              },
            ],
            next_cursor: null,
            total_count: 1,
            offset: 0,
          },
        }),
      );
      await page.route("**/api/v2/git/repositories?**", (route) =>
        route.fulfill({
          json: {
            items: [
              {
                id: projectID,
                key: "project",
                name: "Existing project",
                revision: "1",
                created_at: "2026-10-01T12:00:00.000000000Z",
                updated_at: "2026-10-01T12:00:00.000000000Z",
                url: "https://example.invalid:443/team/project",
                aliases: [],
                credential_id: null,
                alias_revision: "1",
              },
            ],
            next_cursor: null,
            total_count: 1,
            offset: 0,
          },
        }),
      );
      await page.route("**/api/v2/config/**", async (route) => {
        const request = route.request();
        if (request.method() === "GET")
          return route.fulfill({ contentType: "application/json", body: raw });
        expect(request.postData()).toBe(raw);
        expect((await request.allHeaders())["x-csrf-token"]).toBe(
          "A".repeat(43),
        );
        expect((await request.allHeaders()).origin).toBe(frontend.origin);
        if (request.url().endsWith("/preview")) {
          validations++;
          if (previewGate) await previewGate;
          if (rejectPreview)
            return route.fulfill({
              status: 503,
              contentType: "application/problem+json",
              json: {
                status: 503,
                code: "storage_unavailable",
                title: "REJECTED_SECRET_CANARY",
              },
            });
          return route.fulfill({
            json: {
              advisory: true,
              creations: [
                {
                  kind,
                  key: "portable",
                  reserved: false,
                  dependencies:
                    kind === "mcp_grant"
                      ? [
                          { kind: "agent", key: "reader" },
                          { kind: "gateway_mcp" },
                        ]
                      : kind === "git_grant"
                        ? [
                            { kind: "agent", key: "reader" },
                            { kind: "git_repository", key: "project" },
                          ]
                        : kind === "http_grant"
                          ? [{ kind: "agent", key: "reader" }]
                          : [],
                  requirements:
                    kind === "mcp_server"
                      ? ["supply_static_credentials", "activate_after_commit"]
                      : kind.includes("credential")
                        ? ["supply_credential_value"]
                        : kind === "agent"
                          ? ["issue_agent_credential"]
                          : [],
                },
              ],
            },
          });
        }
        imports++;
        if (importGate) await importGate;
        if (responseMode === "conflict")
          return route.fulfill({
            status: 409,
            contentType: "application/problem+json",
            json: {
              status: 409,
              code: "conflict",
              title: "REJECTED_SECRET_CANARY",
            },
          });
        if (responseMode === "uncertain") return route.abort("failed");
        return route.fulfill({
          status: 201,
          json: {
            configuration: "committed",
            creations: [{ kind, key: "portable", id, reserved: false }],
            activation: kind === "mcp_server" ? "pending" : "not_required",
          },
        });
      });
      await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
      await page.getByTestId("sign-in-submit").click();
      await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
        "data-session-lifecycle",
        "authenticated",
      );
      const path = resourceDestinations[kind];
      await page.goto(`${frontend.origin}/#/${path}`);
      await expect(page.getByRole("link", { name: /^Create / })).toBeVisible();
      await page.getByRole("link", { name: /^Import / }).click();
      const input = page.getByLabel("Configuration JSON", { exact: true });
      await expect(input).toBeVisible();
      await capture(page, "input", true);
      if (kind === "agent") {
        await input.fill(JSON.stringify({ ...definition, kind: "git_grant" }));
        await page
          .getByRole("button", { name: "Validate and preview" })
          .click();
        await expect(
          page.getByText("Enter one matching version 2 resource definition.", {
            exact: true,
          }),
        ).toBeVisible();
        await expect(input).toBeEditable();
        await input.fill(
          JSON.stringify({
            format: "agent-gateway-config",
            version: 2,
            resources: [definition],
          }),
        );
        await page
          .getByRole("button", { name: "Validate and preview" })
          .click();
        await expect(
          page.getByText(
            /Bundles belong in System Configuration\. Import exactly/,
          ),
        ).toBeVisible();
        expect(validations).toBe(0);
        await capture(page, "bundle-refused", true);
        await input.fill('{"secret":"REJECTED_SECRET_CANARY"');
        await page
          .getByRole("button", { name: "Validate and preview" })
          .click();
        await expect(
          page.getByText("Enter a valid JSON resource definition."),
        ).toBeVisible();
        await expect(page.locator('[role="alert"]')).not.toContainText(
          "REJECTED_SECRET_CANARY",
        );
      }
      if (kind === "agent") {
        const previous = await input.inputValue();
        await page.getByLabel("Upload JSON file").setInputFiles({
          name: "invalid.json",
          mimeType: "application/json",
          buffer: Buffer.from([0xff]),
        });
        await expect(
          page.getByText("The JSON file could not be read as UTF-8.", {
            exact: true,
          }),
        ).toBeVisible();
        await expect(input).toHaveValue(previous);
        await expect(input).toBeEditable();
      }
      await page.getByLabel("Upload JSON file").setInputFiles({
        name: "config.json",
        mimeType: "application/json",
        buffer: Buffer.from(raw),
      });
      await expect(input).toHaveValue(raw);
      if (kind === "agent") {
        rejectPreview = true;
        await page
          .getByRole("button", { name: "Validate and preview" })
          .click();
        await expect(
          page.getByText(
            "Online validation failed. Check the definition and its dependencies, then validate again.",
            { exact: true },
          ),
        ).toBeVisible();
        await expect(input).toHaveValue(raw);
        await expect(input).toBeEditable();
        await expect(page.locator('[role="alert"]')).not.toContainText(
          "REJECTED_SECRET_CANARY",
        );
        await capture(page, "validation-failed", true);
        rejectPreview = false;
        previewGate = new Promise<void>((resolve) => {
          releasePreview = resolve;
        });
      }
      await page.getByRole("button", { name: "Validate and preview" }).click();
      if (kind === "agent") {
        await expect(
          page.getByText("Validating configuration", { exact: true }),
        ).toBeVisible();
        await expect(input).toBeDisabled();
        await expect.poll(() => validations).toBe(2);
        await capture(page, "validating", true);
        releasePreview?.();
        previewGate = undefined;
      }
      await expect(
        page.getByRole("heading", { name: "Creation preview" }),
      ).toBeVisible();
      if (kind.includes("grant"))
        await expect(page.getByLabel("Import preview")).toContainText(
          "agent / reader",
        );
      await page.getByText("Exact configuration JSON", { exact: true }).click();
      await expect(page.locator("pre")).toHaveText(raw);
      await page.setViewportSize({ width: 320, height: 844 });
      expect(
        await page
          .locator("pre")
          .evaluate((element) => element.scrollWidth <= element.clientWidth),
      ).toBe(true);
      await capture(page, "preview", true);
      if (kind === "agent") {
        await page.getByLabel("Upload JSON file").setInputFiles({
          name: "oversize.json",
          mimeType: "application/json",
          buffer: Buffer.alloc(1024 * 1024 + 1, 32),
        });
        await expect(
          page.getByText("Configuration must be at most 1 MiB.", {
            exact: true,
          }),
        ).toBeVisible();
        await expect(input).toHaveValue(raw);
        await expect(input).toBeEditable();
        await expect(
          page.getByRole("button", { name: "Import", exact: true }),
        ).toHaveCount(0);
        await capture(page, "file-refused", true);
        await page
          .getByRole("button", { name: "Validate and preview" })
          .click();
      }
      await page.getByRole("button", { name: "Import", exact: true }).click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await expect(page.getByRole("dialog")).toContainText('"portable"');
      await capture(page, "confirmation", true);
      await page.getByRole("button", { name: "Cancel", exact: true }).click();
      await expect(
        page.getByRole("button", { name: "Import", exact: true }),
      ).toBeFocused();
      expect(imports).toBe(0);
      if (kind === "agent") {
        await page.getByRole("button", { name: "Import", exact: true }).click();
        await page
          .getByRole("button", { name: "Import", exact: true })
          .evaluate((button) =>
            button.addEventListener(
              "focus",
              () =>
                queueMicrotask(() =>
                  document.getElementById("configuration-json")?.focus(),
                ),
              { once: true },
            ),
          );
        await page.getByRole("dialog").evaluate((dialog) =>
          dialog.addEventListener(
            "close",
            () => {
              document.documentElement.dataset.configDialogClosed = "settled";
            },
            { once: true },
          ),
        );
        await page.getByRole("button", { name: "Cancel", exact: true }).click();
        await expect(page.locator("html")).toHaveAttribute(
          "data-config-dialog-closed",
          "settled",
        );
        await expect(input).toBeFocused();
      }
      if (kind === "agent") {
        responseMode = "conflict";
        await page.getByRole("button", { name: "Import", exact: true }).click();
        await page
          .getByRole("button", { name: "Confirm import", exact: true })
          .click();
        await expect(
          page.getByText(/a key or dependency changed since preview/),
        ).toBeVisible();
        await expect(input).toHaveValue(raw);
        await expect(input).toBeEditable();
        await expect(page.locator("main")).not.toContainText(
          "REJECTED_SECRET_CANARY",
        );
        await capture(page, "conflict", true);
        expect(imports).toBe(1);
        responseMode = "success";
        await page
          .getByRole("button", { name: "Validate and preview" })
          .click();
      }
      if (kind === "agent")
        importGate = new Promise<void>((resolve) => {
          releaseImport = resolve;
        });
      await page.getByRole("button", { name: "Import", exact: true }).click();
      await page
        .getByRole("button", { name: "Confirm import", exact: true })
        .click();
      if (kind === "agent") {
        await expect(
          page.getByText("Creating resource", { exact: true }),
        ).toBeVisible();
        await expect(input).toBeDisabled();
        await expect(
          page.getByRole("button", { name: "Import", exact: true }),
        ).toBeDisabled();
        await expect.poll(() => imports).toBe(2);
        await capture(page, "importing", true);
        releaseImport?.();
        importGate = undefined;
      }
      await expect(
        page.getByText("Resource creation committed", { exact: true }),
      ).toBeVisible();
      if (kind === "mcp_server")
        await expect(page.getByText(/Activation pending/)).toBeVisible();
      if (kind.includes("credential"))
        await expect(
          page.getByText(/Required next: Supply credential value/),
        ).toBeVisible();
      if (kind === "agent")
        await expect(
          page.getByText(/Required next: Issue agent credential/),
        ).toBeVisible();
      await capture(page, "committed", true);
      const createdURL = `#/${path}/${id}`;
      await expect(
        page.getByRole("button", { name: "View created resource" }),
      ).toBeVisible();
      // Dedicated live detail owners below cover the existing ID route and exports.
      {
        const api = kind === "agent" ? "principals" : path;
        const common = {
          key: "portable",
          id,
          revision: "1",
          created_at: "2026-10-01T12:00:00.000000000Z",
          updated_at: "2026-10-01T12:00:00.000000000Z",
        };
        const values: Record<string, unknown> = {
          agent,
          mcp_server: {
            ...serverReadFixture(id, {
              name: "Imported server",
              desired: "enabled",
              runtime: "inactive",
              credential: "absent",
              durable: "current",
              active: "absent",
            }),
            namespace: "portable",
            credential_revisions: {
              static_credential: "0",
              oauth_client: "0",
              oauth_tokens: "0",
            },
          },
          mcp_grant: {
            key: "portable",
            id,
            revision: "1",
            created_at: common.created_at,
            description: "Imported grant",
            principal_id: readerID,
            effect: "allow",
            server_id: "00000000000000000000000000",
            upstream_name: null,
            constraint: null,
            expires_at: null,
            state: "active",
          },
          http_credential: {
            ...common,
            name: "Imported HTTP credential",
            boundary: {
              host: "example.invalid",
              port: 443,
              allow_wildcard: false,
            },
            recipe: { header: "Authorization", prefix: "Bearer " },
            available: false,
            material_state: "not_configured",
            referencing_grants: [],
          },
          http_grant: {
            ...common,
            principal_id: readerID,
            description: "Imported HTTP grant",
            policy: {
              version: 1,
              type: "block_destination",
              destination: { host: "example.invalid", port: 443 },
            },
            expires_at: null,
            state: "active",
          },
          git_credential: {
            ...common,
            name: "Imported Git credential",
            origin: "https://example.invalid:443",
            recipe: { header: "Authorization", prefix: "Bearer " },
            available: false,
            material_state: "not_configured",
            referencing_repositories: [],
          },
          git_repository: {
            ...common,
            name: "Imported repository",
            url: "https://example.invalid:443/team/repo",
            aliases: [],
            credential_id: null,
            alias_revision: "1",
          },
          git_grant: {
            ...common,
            description: "Imported Git grant",
            principal_id: readerID,
            repository_id: projectID,
            policy: { version: 1, read: true, refs: [] },
            expires_at: null,
            state: "active",
          },
        };
        const etag =
          kind === "agent"
            ? `"principal-${id}-1"`
            : kind === "mcp_server"
              ? `"server-${id}-7"`
              : kind === "mcp_grant"
                ? `"grant-${id}-1"`
                : `"${kind.replaceAll("_", "-")}-${id}-1"`;
        if (kind === "mcp_grant")
          await page.route("**/api/v2/mcp/servers?limit=50", (route) =>
            route.fulfill({ json: { items: [], next_cursor: null } }),
          );
        await page.route(`**/api/v2/${api}/${id}`, (route) =>
          route.fulfill({ json: values[kind], headers: { ETag: etag } }),
        );
        await page
          .getByRole("button", { name: "View created resource" })
          .click();
        await expect(page).toHaveURL(new RegExp(createdURL));
        await expect(
          page.getByText("Immutable key", { exact: true }),
        ).toBeVisible();
        await context.grantPermissions(["clipboard-read", "clipboard-write"]);
        await page
          .getByRole("button", { name: "Copy config", exact: true })
          .click();
        await expect(
          page.getByText("Config copied.", { exact: true }),
        ).toBeVisible();
        expect(await page.evaluate(() => navigator.clipboard.readText())).toBe(
          raw,
        );
        const download = page.waitForEvent("download");
        await page
          .getByRole("button", { name: "Download config", exact: true })
          .click();
        const file = await download;
        expect(file.suggestedFilename()).toBe(`${kind}-portable.json`);
        expect(await readFile((await file.path())!, "utf8")).toBe(raw);
        await expect(
          page.getByText("Config downloaded.", { exact: true }),
        ).toBeVisible();
        await capture(page, "export", true);
        if (kind === "mcp_server") {
          let release!: () => void;
          let started!: () => void;
          const barrier = new Promise<void>((resolve) => {
            release = resolve;
          });
          const intercepted = new Promise<void>((resolve) => {
            started = resolve;
          });
          await page.evaluate(() => {
            Object.defineProperty(navigator, "clipboard", {
              configurable: true,
              value: {
                writeText: async () => {
                  document.documentElement.dataset.staleCopy = "published";
                },
              },
            });
          });
          await page.route(
            "**/api/v2/config/resources/mcp_server/portable",
            async (route) => {
              started();
              await barrier;
              await route.fulfill({
                contentType: "application/json",
                body: raw,
              });
            },
          );
          const response = page.waitForResponse((value) =>
            value
              .url()
              .endsWith("/api/v2/config/resources/mcp_server/portable"),
          );
          await page
            .getByRole("button", { name: "Copy config", exact: true })
            .click();
          await intercepted;
          await page
            .getByRole("link", { name: "Settings", exact: true })
            .click();
          await expect(
            page.getByRole("heading", { name: "Edit server", exact: true }),
          ).toBeVisible();
          release();
          await (await response).finished();
          await page.evaluate(
            () =>
              new Promise<void>((resolve) =>
                requestAnimationFrame(() => resolve()),
              ),
          );
          await expect(page.locator("html")).not.toHaveAttribute(
            "data-stale-copy",
            "published",
          );
          await expect(
            page.getByText("Config copied.", { exact: true }),
          ).toHaveCount(0);
        }
        if (kind === "agent") {
          await page.evaluate(() => {
            Object.defineProperty(navigator, "clipboard", {
              configurable: true,
              value: {
                writeText: async () => {
                  throw Error("Denied");
                },
              },
            });
          });
          await page
            .getByRole("button", { name: "Copy config", exact: true })
            .click();
          await expect(
            page.getByText(
              "Config could not be published. Try the action again.",
              { exact: true },
            ),
          ).toBeVisible();
          await capture(page, "copy-failed", true);
          await page.goto(`${frontend.origin}/#/${path}/import`);
          await input.fill(raw);
          await page
            .getByRole("button", { name: "Validate and preview" })
            .click();
          await page
            .getByRole("button", { name: "Import", exact: true })
            .click();
          responseMode = "uncertain";
          await page
            .getByRole("button", { name: "Confirm import", exact: true })
            .click();
          await expect(
            page.getByText("Creation outcome unknown", { exact: true }),
          ).toBeVisible();
          await expect(input).toBeDisabled();
          const count = imports;
          await page
            .getByRole("button", { name: "Refresh current view" })
            .click();
          await expect(
            page.getByText("Creation outcome unknown", { exact: true }),
          ).toBeVisible();
          expect(imports).toBe(count);
          await capture(page, "uncertain", true);
        }
      }
    } finally {
      releasePreview?.();
      releaseImport?.();
    }
  });
}
