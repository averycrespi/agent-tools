import { expect, type Page } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
const agentID = "01ARZ3NDEKTSV4RRFFQ69G5FA1";
const timestamp = "2026-10-01T12:00:00.000000000Z";
const common = {
  id,
  revision: "1",
  created_at: timestamp,
  updated_at: timestamp,
};
const agent = {
  id: agentID,
  display_name: "Synthetic agent",
  state: "active",
  visibility: "all",
  http_default: "block",
  revision: "1",
  credential_revision: "0",
  credential: null,
  created_at: timestamp,
  updated_at: timestamp,
};
const entities = [
  {
    path: "git/repositories",
    singular: "Git repository",
    name: "Synthetic repository",
    record: {
      ...common,
      name: "Synthetic repository",
      url: "https://example.invalid/team/repo",
      aliases: [],
      credential_id: null,
      alias_revision: "1",
    },
  },
  {
    path: "git/credentials",
    singular: "Git credential",
    name: "Synthetic Git credential",
    record: {
      ...common,
      name: "Synthetic Git credential",
      origin: "https://example.invalid:443",
      recipe: { header: "Authorization", prefix: "Bearer " },
      available: true,
      referencing_repositories: [],
    },
  },
  {
    path: "git/grants",
    singular: "Git grant",
    name: "Synthetic Git grant",
    record: {
      ...common,
      principal_id: agentID,
      repository_id: id,
      description: "Synthetic Git grant",
      policy: {
        version: 1,
        read: true,
        refs: [
          {
            ref: { kind: "exact", value: "refs/heads/main" },
            actions: ["create", "update"],
          },
        ],
      },
      expires_at: null,
      state: "active",
    },
  },
  {
    path: "http/credentials",
    singular: "HTTP credential",
    name: "Synthetic HTTP credential",
    record: {
      ...common,
      name: "Synthetic HTTP credential",
      boundary: { host: "example.invalid", port: 443, allow_wildcard: false },
      recipe: { header: "Authorization", prefix: "Bearer " },
      available: true,
      referencing_grants: [],
    },
  },
  {
    path: "http/grants",
    singular: "HTTP grant",
    name: "Synthetic HTTP grant",
    record: {
      ...common,
      principal_id: agentID,
      description: "Synthetic HTTP grant",
      policy: {
        version: 1,
        type: "block_destination",
        destination: { host: "example.invalid", port: 443 },
      },
      expires_at: null,
      state: "active",
    },
  },
];

async function fillCreate(page: Page, path: string) {
  if (!path.endsWith("grants"))
    await page
      .getByLabel("Name", { exact: true })
      .fill("Synthetic created resource");
  if (path === "git/repositories") {
    await page
      .getByLabel("Canonical HTTPS destination", { exact: true })
      .fill("https://example.invalid/team/created");
    await expect(page.getByLabel("Alias 1", { exact: true })).toHaveValue(
      "https://example.invalid/team/created.git",
    );
    const canonical = page.getByLabel("Canonical HTTPS destination", {
      exact: true,
    });
    await canonical.fill("https://example.invalid/team/alternate.git");
    await expect(page.getByLabel("Alias 1", { exact: true })).toHaveValue(
      "https://example.invalid/team/alternate",
    );
    await canonical.fill("https://example.invalid/team/created");
  }
  if (path === "git/credentials")
    await page
      .getByLabel("HTTPS origin", { exact: true })
      .fill("https://example.invalid");
  if (path === "http/credentials")
    await page
      .getByLabel("HTTPS destination host", { exact: true })
      .fill("example.invalid");
  if (path.endsWith("credentials"))
    await page
      .getByLabel("Secret", { exact: true })
      .fill("SYNTHETIC_WRITE_ONLY_CANARY");
  if (path.endsWith("grants")) {
    await page.getByLabel("Agent", { exact: true }).selectOption(agentID);
    await page
      .getByLabel("Description (optional)", { exact: true })
      .fill("Synthetic created grant");
    if (path.startsWith("git")) {
      await page.getByLabel("Repository", { exact: true }).selectOption(id);
      await page
        .getByRole("button", { name: "Add push rule", exact: true })
        .click();
      await page
        .getByLabel("Ref selector", { exact: true })
        .fill("refs/heads/main");
    } else {
      await page
        .getByLabel("Grant type", { exact: true })
        .selectOption("block_destination");
      await page
        .getByLabel("Destination host", { exact: true })
        .fill("example.invalid");
    }
  }
}

for (const entity of entities) {
  test(entity.path.replaceAll("/", "-"), async ({ page, frontend }) => {
    registerCapture(page, entity.path.replaceAll("/", "-"));
    let mode: "populated" | "empty" | "error" | "loading" = "populated";
    let release: (() => void) | undefined;
    let writes = 0;
    const collectionQueries: URLSearchParams[] = [];
    let current = { ...entity.record };
    let etag = `"${entity.path
      .replace("/", "-")
      .replace(/repositories$/, "repository")
      .replace(/credentials$/, "credential")
      .replace(/grants$/, "grant")}-${id}-1"`;
    let mutationMode: "reject" | "uncertain" | "pending" = "reject";
    await page.route("**/api/v2/**", async (route) => {
      const req = route.request();
      const url = new URL(req.url());
      const path = url.pathname;
      const target = `/api/v2/${entity.path}`;
      if (req.method() === "GET") {
        if (path === "/api/v2/principals")
          return route.fulfill({
            json: {
              items: [agent],
              next_cursor: null,
              total_count: 1,
              offset: 0,
            },
          });
        if (path === `/api/v2/principals/${agentID}`)
          return route.fulfill({
            json: agent,
            headers: { ETag: `"principal-${agentID}-1"` },
          });
        if (path === target || path === `${target}/${id}`) {
          if (path === target)
            collectionQueries.push(new URL(req.url()).searchParams);
          if (mode === "loading")
            await new Promise<void>((resolve) => {
              release = resolve;
            });
          if (mode === "error")
            return route.fulfill({
              status: 503,
              json: {
                status: 503,
                code: "unavailable",
                title: "Synthetic unavailable",
              },
            });
          const value =
            entity.path === "http/grants"
              ? { grant: current, principal_display_name: agent.display_name }
              : current;
          return route.fulfill({
            json:
              path === target
                ? {
                    items: mode === "empty" ? [] : [value],
                    next_cursor: null,
                    total_count: mode === "empty" ? 0 : 1,
                    offset: 0,
                  }
                : current,
            headers: { ETag: etag },
          });
        }
        for (const choice of entities) {
          if (path === `/api/v2/${choice.path}`)
            return route.fulfill({
              json: {
                items: [choice.record],
                next_cursor: null,
                total_count: 1,
                offset: 0,
              },
            });
          if (path === `/api/v2/${choice.path}/${id}`)
            return route.fulfill({
              json: choice.record,
              headers: { ETag: `"git-repository-${id}-1"` },
            });
        }
      }
      if (path.startsWith(target) && req.method() !== "GET") {
        writes++;
        expect(["POST", "PATCH", "DELETE"]).toContain(req.method());
        expect(req.headers()["x-csrf-token"]).toBe("A".repeat(43));
        if (req.method() !== "POST")
          expect(req.headers()["if-match"]).toBe(etag);
        const payload = req.postDataJSON();
        expect(payload).toBeTruthy();
        if (req.method() === "PATCH") {
          expect(
            payload[entity.path.endsWith("grants") ? "description" : "name"],
          ).toBe(
            writes === 1
              ? "Retained synthetic draft"
              : "Pending synthetic draft",
          );
          expect(payload).not.toHaveProperty("secret");
        }
        if (mutationMode === "pending")
          await new Promise<void>((resolve) => {
            release = resolve;
          });
        if (mutationMode === "reject") {
          current = { ...current, revision: "2" };
          etag = etag.replace(/-1"$/, '-2"');
        }
        return mutationMode === "reject"
          ? route.fulfill({
              status: 412,
              contentType: "application/problem+json",
              json: {
                status: 412,
                code: "stale_revision",
                title: "Synthetic revision conflict",
              },
            })
          : route.fulfill({ status: 200, json: {} });
      }
      return route.fallback();
    });
    await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
    await page.getByTestId("sign-in-submit").click();
    await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
      "data-session-lifecycle",
      "authenticated",
    );
    const nav = async (suffix = "") => {
      await page.goto("about:blank");
      await page.goto(`${frontend.origin}/#/${entity.path}${suffix}`);
      await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
        "data-session-lifecycle",
        "authenticated",
      );
    };
    await nav();
    await expect(page.getByRole("table")).toContainText(entity.name);
    if (entity.path.startsWith("git/")) {
      const grants = entity.path === "git/grants";
      expect(collectionQueries.at(-1)?.get("sort")).toBe(
        grants ? "description" : "name",
      );
      expect(collectionQueries.at(-1)?.get("direction")).toBe("ascending");
      const fields =
        entity.path === "git/repositories"
          ? [
              ["Name or ID", "name"],
              ["Destination", "destination"],
              ["Git credential", "credential"],
            ]
          : grants
            ? [
                ["Description or ID", "identity"],
                ["Repository", "repository"],
                ["Agent", "principal"],
              ]
            : [
                ["Name or ID", "name"],
                ["HTTPS origin", "origin"],
              ];
      for (const [label, key] of fields) {
        await page
          .getByRole("searchbox", { name: label!, exact: true })
          .fill("Synthetic");
        await expect
          .poll(() => collectionQueries.at(-1)?.get(key!))
          .toBe("Synthetic");
      }
      if (entity.path !== "git/repositories") {
        await page
          .getByRole("combobox", { name: "Status", exact: true })
          .selectOption(grants ? "expired" : "unavailable");
        await expect
          .poll(() =>
            collectionQueries.at(-1)?.get(grants ? "state" : "status"),
          )
          .toBe(grants ? "expired" : "unavailable");
      }
      await page.getByRole("link", { name: entity.name, exact: true }).click();
      await expect(page).toHaveURL(/filter_/);
      await page
        .getByRole("link", {
          name: `Back to Git ${entity.path.split("/")[1]}`,
          exact: true,
        })
        .click();
      await expect(
        page.getByRole("searchbox", { name: fields[0]![0]!, exact: true }),
      ).toHaveValue("Synthetic");
      await capture(page, "collection-filtered", true);
      await page.getByRole("button", { name: "Reset", exact: true }).click();
      await expect
        .poll(() => collectionQueries.at(-1)?.has(fields[0]![1]!))
        .toBe(false);
      await page
        .getByRole("button", {
          name: grants
            ? "Grant"
            : entity.path === "git/repositories"
              ? "Repository"
              : "Credential",
          exact: true,
        })
        .focus();
      await page.keyboard.press("Enter");
      await expect
        .poll(() => collectionQueries.at(-1)?.get("direction"))
        .toBe("descending");
    }
    if (entity.path === "http/credentials") {
      expect(collectionQueries.at(-1)?.get("sort")).toBe("name");
      expect(collectionQueries.at(-1)?.get("direction")).toBe("ascending");
      await page
        .getByRole("button", { name: "Credential", exact: true })
        .click();
      await expect
        .poll(() => collectionQueries.at(-1)?.get("direction"))
        .toBe("descending");
      await page
        .getByRole("searchbox", { name: "Name or ID", exact: true })
        .fill("Synthetic");
      await expect
        .poll(() => collectionQueries.at(-1)?.get("name"))
        .toBe("Synthetic");
      await page.getByRole("link", { name: entity.name, exact: true }).click();
      await expect(page).toHaveURL(/filter_name=Synthetic/);
      await page
        .getByRole("link", { name: "Back to HTTP credentials", exact: true })
        .click();
      await expect(
        page.getByRole("searchbox", { name: "Name or ID", exact: true }),
      ).toHaveValue("Synthetic");
      await page.getByRole("button", { name: "Reset", exact: true }).click();
      await expect(page).toHaveURL(/sort=name&direction=descending$/);
      await expect
        .poll(() => collectionQueries.at(-1)?.has("name"))
        .toBe(false);
      expect(collectionQueries.at(-1)?.get("direction")).toBe("descending");
    }
    if (entity.path === "http/grants") {
      const agentCell = page.locator('[data-label="Agent"]');
      await expect(agentCell.locator(".table-primary a")).toHaveText(
        agent.display_name,
      );
      await expect(agentCell.locator(".table-identifier")).toHaveText(agentID);
      await expect(agentCell.locator(".table-identifier a")).toHaveCount(0);
    }
    await capture(page, "collection-populated", true);
    mode = "empty";
    await page.getByTestId("manual-refresh").click();
    await expect(page.getByRole("table").locator("tbody tr")).toHaveCount(0);
    await capture(page, "collection-empty");
    mode = "error";
    await nav();
    await expect(page.getByRole("alert").first()).toBeVisible();
    await capture(page, "collection-error");
    mode = "loading";
    await nav();
    await expect(page.getByText(/Loading/).first()).toBeVisible();
    await capture(page, "collection-loading");
    mode = "populated";
    release?.();
    await expect(page.getByRole("table")).toContainText(entity.name);
    mode = "error";
    await page.getByTestId("manual-refresh").click();
    await expect(page.getByRole("alert").first()).toBeVisible();
    await capture(page, "collection-stale");
    mode = "populated";
    await nav("/new");
    await expect(
      page.getByRole("button", { name: "Review and create", exact: true }),
    ).toBeVisible();
    await capture(page, "create-blank");
    await page
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    expect(await page.locator(":invalid").count()).toBeGreaterThan(0);
    expect(writes).toBe(0);
    await capture(page, "create-validation");
    await fillCreate(page, entity.path);
    if (entity.path === "http/grants") {
      for (const kind of ["allow_tunnel", "block_requests", "allow_requests"]) {
        await page.getByLabel("Grant type", { exact: true }).selectOption(kind);
        if (kind.endsWith("requests")) {
          await page
            .getByRole("button", { name: "Add method", exact: true })
            .click();
          await page.getByLabel("Method 1", { exact: true }).fill("GET");
          await page
            .getByLabel("Path match", { exact: true })
            .selectOption("exact");
          await page.getByLabel("Path", { exact: true }).fill("/synthetic");
        }
        if (kind === "allow_requests")
          await page
            .getByLabel("Credential (optional)", { exact: true })
            .selectOption(id);
        await capture(page, `create-${kind}`, true);
        await page
          .getByRole("button", { name: "Review and create", exact: true })
          .click();
        await expect(page.getByRole("dialog")).toBeVisible();
        await capture(page, `create-${kind}-confirmation`);
        await page
          .getByRole("dialog")
          .getByRole("button", { name: "Cancel", exact: true })
          .click();
        if (kind.endsWith("requests"))
          await page
            .getByRole("button", { name: "Remove method", exact: true })
            .click();
      }
      await page
        .getByLabel("Path match", { exact: true })
        .selectOption("segment_prefix");
      await capture(page, "create-descendant-path");
      await page.getByLabel("Scheme", { exact: true }).selectOption("http");
      await capture(page, "create-http-no-compatible-credential");
      await page
        .getByLabel("Grant type", { exact: true })
        .selectOption("block_destination");
    }
    if (entity.path === "git/repositories") {
      const canonical = page.getByLabel("Canonical HTTPS destination", {
        exact: true,
      });
      const alias = page.getByLabel("Alias 1", { exact: true });
      await alias.fill("https://example.invalid/team/deliberate");
      await canonical.fill("https://example.invalid/team/changed.git");
      await expect(alias).toHaveValue(
        "https://example.invalid/team/deliberate",
      );
      await capture(page, "create-alias-edited");
      await page
        .getByRole("button", { name: "Remove alias 1", exact: true })
        .click();
      await canonical.fill("https://example.invalid/team/created");
      await expect(alias).toHaveCount(0);
      await capture(page, "create-alias-removed");
      await page
        .getByRole("button", { name: "Add alias", exact: true })
        .click();
      await alias.fill("https://example.invalid/team/created");
      await page
        .getByRole("button", { name: "Review and create", exact: true })
        .click();
      await expect(
        page.getByText(
          "Aliases must be distinct from each other and the canonical destination.",
          { exact: true },
        ),
      ).toBeVisible();
      await expect(page.getByRole("dialog")).toHaveCount(0);
      await alias.fill("https://example.invalid/team/created.git");
      await page.getByLabel("Git credential", { exact: true }).selectOption(id);
      await capture(page, "create-selected-credential");
    }
    if (entity.path === "git/grants") {
      await page.getByLabel("Match", { exact: true }).selectOption("prefix");
      await page
        .getByLabel("Ref selector", { exact: true })
        .fill("refs/heads/");
      await page.getByLabel("Create", { exact: true }).click();
      await page.getByLabel("Delete", { exact: true }).click();
      const toggles = await page
        .locator(".push-permissions-row .form-field")
        .evaluateAll((fields) =>
          fields.map((field) => field.getBoundingClientRect().top),
        );
      expect(new Set(toggles).size).toBe(1);
      await expect(page.getByLabel("Create", { exact: true })).toBeChecked();
      await expect(page.getByLabel("Update", { exact: true })).toBeChecked();
      await expect(page.getByLabel("Delete", { exact: true })).toBeChecked();
      await capture(page, "create-push-prefix-actions", true);
    }
    await capture(page, "create-populated", true);
    await page
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await expect(page.getByRole("dialog")).not.toContainText(
      "SYNTHETIC_WRITE_ONLY_CANARY",
    );
    await capture(page, "create-confirmation");
    expect(writes).toBe(0);
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    if (entity.path.endsWith("credentials"))
      await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    await nav(`/${id}`);
    await expect(page.getByRole("heading", { level: 1 })).toContainText(
      entity.name,
    );
    const detailLabels: Record<string, string> = {
      "git/repositories": "Git Repository details",
      "git/credentials": "Git Credential details",
      "git/grants": "Git Grant details",
    };
    if (detailLabels[entity.path])
      await expect(page.locator("section.intro")).toHaveAccessibleName(
        detailLabels[entity.path]!,
      );
    await expect(
      page.getByTestId("detail-context").locator("h1"),
    ).toBeFocused();
    if (entity.path === "git/grants") {
      await expect(page.getByLabel("Agent", { exact: true })).toHaveAttribute(
        "readonly",
        "",
      );
      await expect(
        page.getByLabel("Repository", { exact: true }),
      ).toHaveAttribute("readonly", "");
      const toggles = await page
        .locator(".push-permissions-row .form-field")
        .evaluateAll((fields) =>
          fields.map((field) => field.getBoundingClientRect().top),
        );
      expect(new Set(toggles).size).toBe(1);
      await expect(page.getByLabel("Create", { exact: true })).toBeChecked();
      await expect(page.getByLabel("Update", { exact: true })).toBeChecked();
      await expect(
        page.getByLabel("Delete", { exact: true }),
      ).not.toBeChecked();
    }
    if (entity.path === "git/repositories")
      await expect(
        page.getByLabel("Canonical HTTPS destination"),
      ).toHaveAttribute("readonly", "");
    await capture(page, "detail-edit", true);
    if (entity.path.startsWith("git/")) {
      const input = page.getByLabel(
        entity.path.endsWith("grants") ? "Description (optional)" : "Name",
        { exact: true },
      );
      await input.fill("Temporary metadata");
      await input.fill(entity.name);
      if (entity.path.endsWith("grants")) {
        await page.getByLabel("Create", { exact: true }).click();
        await page.getByLabel("Create", { exact: true }).click();
      }
      await page
        .getByRole("link", {
          name: `Back to Git ${entity.path.split("/")[1]}`,
          exact: true,
        })
        .click();
      await expect(page.getByRole("table")).toContainText(entity.name);
      await nav(`/${id}`);
      await input.fill("Discarded metadata");
      await page
        .getByRole("button", { name: "Discard changes", exact: true })
        .click();
      await expect(input).toHaveValue(entity.name);
    }
    if (entity.path.endsWith("credentials")) {
      await page
        .getByLabel("Secret", { exact: true })
        .fill("SYNTHETIC_ROTATION_CANARY");
      await page
        .getByRole("button", { name: "Review rotation", exact: true })
        .click();
      await expect(page.getByRole("dialog")).toBeVisible();
      await expect(page.getByRole("dialog")).not.toContainText(
        "SYNTHETIC_ROTATION_CANARY",
      );
      await capture(page, "rotate-confirmation");
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Cancel", exact: true })
        .click();
      await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    }
    await expect(page.getByTestId("detail-context")).toContainText(id);
    await expect(
      page.locator(".detail-section dt").filter({ hasText: /^ID$/ }),
    ).toHaveCount(0);
    if (entity.path === "http/grants") {
      await expect(
        page.getByRole("heading", { name: "Edit grant", exact: true }),
      ).toBeVisible();
      await expect(page.getByLabel("Agent", { exact: true })).toHaveAttribute(
        "readonly",
        "",
      );
    }
    await page
      .getByRole("button", {
        name:
          entity.path === "http/grants" ? "Delete grant" : "Review deletion",
        exact: true,
      })
      .click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await capture(page, "delete-confirmation");
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Cancel", exact: true })
      .click();
    expect(writes).toBe(0);
    if (entity.path === "http/credentials") {
      for (const action of ["edit", "rotate", "delete"] as const) {
        mode = "populated";
        await nav(`/${id}`);
        if (action === "edit")
          await page
            .getByLabel("Name", { exact: true })
            .fill("Retained confirmation draft");
        if (action === "rotate")
          await page
            .getByLabel("Secret", { exact: true })
            .fill("SYNTHETIC_CONFIRMATION_CANARY");
        await page
          .getByRole("button", {
            name:
              action === "edit"
                ? "Review changes"
                : action === "rotate"
                  ? "Review rotation"
                  : "Review deletion",
            exact: true,
          })
          .click();
        await expect(page.getByRole("dialog")).toBeVisible();
        mode = "error";
        frontend.invalidate("http_credentials", id);
        await expect(
          page.getByText("Current HTTP credential data unavailable", {
            exact: true,
          }),
        ).toBeVisible();
        await capture(page, `${action}-confirmation-refresh-unavailable`);
        await page.getByRole("dialog").locator("button").last().click();
        await expect(page.getByRole("dialog")).toHaveCount(0);
        await expect(
          page.getByText(
            /Current credential state cannot authorize this change/,
          ),
        ).toBeVisible();
        expect(writes).toBe(0);
        if (action === "edit")
          await expect(page.getByLabel("Name", { exact: true })).toHaveValue(
            "Retained confirmation draft",
          );
        if (action === "rotate")
          await expect(page.getByLabel("Secret", { exact: true })).toHaveValue(
            "",
          );
        await capture(page, `${action}-confirmation-blocked`);
      }
      mode = "populated";
    }
    await nav(`/${id}`);
    const editField = page.getByLabel(
      entity.path.endsWith("grants") ? "Description (optional)" : "Name",
      { exact: true },
    );
    await editField.fill("Retained synthetic draft");
    await page
      .getByRole("button", { name: "Review changes", exact: true })
      .click();
    await expect(page.getByRole("dialog")).toBeVisible();
    await capture(page, "edit-confirmation");
    await page.getByRole("dialog").locator("button").last().click();
    await expect(
      page.getByText("Synthetic revision conflict", { exact: true }),
    ).toBeVisible();
    await expect(editField).toHaveValue("Retained synthetic draft");
    expect(writes).toBe(1);
    await capture(page, "edit-conflict");
    if (entity.path === "http/credentials") {
      await expect(
        page.getByRole("button", { name: "Review changes", exact: true }),
      ).toBeDisabled();
      mode = "error";
      await page.getByTestId("manual-refresh").click();
      await expect(
        page.getByText("Current HTTP credential data unavailable", {
          exact: true,
        }),
      ).toBeVisible();
      await expect(editField).toHaveValue("Retained synthetic draft");
      await expect(
        page.getByRole("button", {
          name: "Use reviewed revision; keep draft",
          exact: true,
        }),
      ).toBeDisabled();
      expect(writes).toBe(1);
      await capture(page, "conflict-refresh-unavailable");
      mode = "populated";
      await page.getByTestId("manual-refresh").click();
      await page
        .getByRole("button", {
          name: "Use reviewed revision; keep draft",
          exact: true,
        })
        .click();
      await expect(editField).toHaveValue("Retained synthetic draft");
      await expect(
        page.getByRole("button", { name: "Review changes", exact: true }),
      ).toBeEnabled();
    } else await nav(`/${id}`);
    await editField.fill("Pending synthetic draft");
    mutationMode = "pending";
    await page
      .getByRole("button", { name: "Review changes", exact: true })
      .click();
    await page.getByRole("dialog").locator("button").last().click();
    if (entity.path === "http/grants")
      await expect(
        page.getByRole("button", { name: "Applying grant…", exact: true }),
      ).toBeDisabled();
    else await expect(editField).toBeDisabled();
    await capture(page, "edit-pending");
    mutationMode = "uncertain";
    release?.();
    await expect(
      page.getByText(/outcome (?:(?:is )?unknown|uncertain)/i).first(),
    ).toBeVisible();
    await capture(page, "edit-uncertain");
    await page.getByTestId("manual-refresh").click();
    expect(writes).toBe(2);
    if (entity.path.endsWith("credentials")) {
      Object.assign(current, {
        available: false,
        [entity.path.startsWith("http")
          ? "referencing_grants"
          : "referencing_repositories"]: [{ id }],
      });
      await nav(`/${id}`);
      if (entity.path.startsWith("http"))
        await expect(
          page.getByRole("button", { name: "Review deletion", exact: true }),
        ).toBeDisabled();
      else
        await expect(
          page
            .getByRole("link", { name: "Synthetic repository", exact: true })
            .first(),
        ).toBeVisible();
      await capture(page, "detail-unavailable-material-referenced", true);
    }
    mode = "error";
    await nav(`/${id}`);
    await expect(page.getByRole("alert").first()).toBeVisible();
    await capture(page, "detail-error");
    mode = "loading";
    await nav(`/${id}`);
    await expect(page.getByText(/Loading/i).first()).toBeVisible();
    await capture(page, "detail-loading");
    mode = "populated";
    release?.();
    await expect(page.getByRole("heading", { level: 1 })).toContainText(
      entity.name,
    );
    await nav("/new");
    await fillCreate(page, entity.path);
    mutationMode = "pending";
    await page
      .getByRole("button", { name: "Review and create", exact: true })
      .click();
    await page.getByRole("dialog").locator("button").last().click();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    if (entity.path === "http/grants")
      await expect(
        page.getByRole("button", { name: "Applying grant…", exact: true }),
      ).toBeDisabled();
    else
      await expect(page.locator(".form-submit-action").first()).toBeDisabled();
    await capture(page, "create-pending");
    mutationMode = "uncertain";
    release?.();
    await expect(
      page.getByText(/outcome (?:(?:is )?unknown|uncertain)/i).first(),
    ).toBeVisible();
    if (entity.path.endsWith("credentials"))
      await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
    await capture(page, "create-uncertain");
    await page.getByTestId("manual-refresh").click();
    expect(writes).toBe(3);
  });
}
