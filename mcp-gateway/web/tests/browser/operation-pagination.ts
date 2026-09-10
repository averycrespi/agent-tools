import { expect, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { parseFragment, serializeLocation } from "../../src/location.ts";

type OperationFixture = {
  id: string;
  kind: string;
  state: string;
  created_at: string;
  started_at: string | null;
  reason: string | null;
};

export async function exerciseOperationPagination(
  page: Page,
  baseURL: string,
  serverID: string,
  server: object,
  operation: (
    id: string,
    kind: string,
    state: string,
    reason?: string | null,
  ) => OperationFixture,
): Promise<string[]> {
  const artifacts = await mkdtemp(join(tmpdir(), "tools23-visual-"));
  const screenshots: string[] = [];
  const capture = async (state: string) => {
    for (const width of [1280, 390, 320]) {
      await page.setViewportSize({ width, height: 900 });
      await page
        .locator(".collection-pagination")
        .first()
        .scrollIntoViewIfNeeded();
      const path = join(artifacts, `${state}-${width}.png`);
      await page.screenshot({ path });
      screenshots.push(path);
      if (state === "off-page-blocker") {
        await page
          .locator(".collection-pagination")
          .last()
          .scrollIntoViewIfNeeded();
        const bottom = join(artifacts, `${state}-${width}-bottom.png`);
        await page.screenshot({ path: bottom });
        screenshots.push(bottom);
      }
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
    }
    await page.setViewportSize({ width: 1280, height: 900 });
  };
  const make = (index: number, kind: string, state: string) => ({
    ...operation(String(index + 100).padStart(26, "0"), kind, state),
    created_at: new Date(Date.UTC(2026, 8, 1, 0, index)).toISOString(),
    started_at: null,
  });
  const old = make(0, "retry", "running"),
    newest = make(61, "reload", "scheduled");
  const history = [
    old,
    ...Array.from({ length: 60 }, (_, i) =>
      make(i + 1, "disable", "interrupted"),
    ),
    newest,
  ];
  let active: OperationFixture[] = [old, newest];
  let unavailable = false,
    starts = 0,
    activeReads = 0,
    conflictSettles = false;
  let delay: Promise<void> | undefined;
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations?*`,
    async (route) => {
      const query = new URL(route.request().url()).searchParams;
      if (query.get("projection") === "active") {
        activeReads++;
        if (delay !== undefined) await delay;
        await route.fulfill(
          unavailable
            ? {
                status: 503,
                contentType: "application/problem+json",
                body: JSON.stringify({
                  status: 503,
                  code: "storage_unavailable",
                  title: "Storage unavailable",
                }),
              }
            : {
                status: 200,
                contentType: "application/json",
                body: JSON.stringify({ items: active, has_more: false }),
              },
        );
        return;
      }
      expect(query.get("limit")).toBe("50");
      expect(query.has("sort")).toBe(true);
      let items = history.filter(
        (item) =>
          (!query.has("action") || item.kind === query.get("action")) &&
          (!query.has("status") || item.state === query.get("status")),
      );
      const sort = query.get("sort"),
        direction = query.get("direction") === "descending" ? -1 : 1;
      const key = (item: OperationFixture) =>
        sort === "action"
          ? item.kind
          : sort === "status"
            ? item.state
            : sort === "outcome"
              ? (item.reason ?? "")
              : (item.started_at ?? item.created_at);
      items.sort(
        (a, b) =>
          direction * key(a).localeCompare(key(b)) || a.id.localeCompare(b.id),
      );
      const offset = query.has("cursor") ? 50 : 0;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: items.slice(offset, offset + 50),
          next_cursor: offset + 50 < items.length ? "page-two" : null,
          total_count: items.length,
          offset,
        }),
      });
    },
  );
  await page.route(
    `${baseURL}/api/v1/servers/${serverID}/operations`,
    async (route) => {
      expect(route.request().method()).toBe("POST");
      starts++;
      active = conflictSettles ? [] : [old];
      await route.fulfill({
        status: 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: 409,
          code: "operation_conflict",
          title: "The server has conflicting work.",
        }),
      });
    },
  );
  await page.route(`${baseURL}/api/v1/servers/${serverID}`, async (route) => {
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: {
        ETag: `"server-${serverID}-${(server as { desired_revision: string }).desired_revision}"`,
      },
      body: JSON.stringify(server),
    });
  });
  const navigate = async (suffix = "") => {
    const fragment = `#/servers/${serverID}?tab=activity${suffix}`;
    expect(serializeLocation(parseFragment(fragment)!)).toBe(fragment);
    await page.evaluate((fragment) => {
      location.hash = fragment;
    }, fragment);
    await page.locator('[data-testid="manual-refresh"]').click();
  };
  const table = page.locator('[data-testid="operation-list"]');
  await navigate();
  await expect(
    table.getByText("Showing 1–50 of 62 operations", { exact: true }).first(),
  ).toBeVisible();
  await expect(
    page.locator('[data-testid="active-operation-link"]'),
  ).toHaveAttribute("href", `#/servers/${serverID}/operations/${old.id}`);
  await expect(table.locator(`a[href$="/${old.id}"]`)).toHaveCount(0);
  await expect(page.locator('[data-testid^="start-operation-"]')).toHaveCount(
    0,
  );
  await capture("off-page-blocker");
  await expect(table.locator(".collection-pagination")).toHaveCount(2);
  await table.getByRole("button", { name: "Next", exact: true }).last().click();
  await expect(
    table.getByText("Showing 51–62 of 62 operations", { exact: true }).first(),
  ).toBeVisible();
  await expect(table.locator(`a[href$="/${old.id}"]`)).toBeVisible();
  await table.locator('select[aria-label="Action"]').selectOption("retry");
  await expect(
    table
      .getByText("Showing 1–1 of 1 matching operation", { exact: true })
      .first(),
  ).toBeVisible();
  await expect(
    table.getByRole("button", { name: "Previous", exact: true }).first(),
  ).toBeDisabled();
  await table
    .locator('select[aria-label="Status"]')
    .selectOption("interrupted");
  await expect(
    table.getByText("No matching operations", { exact: true }).first(),
  ).toBeVisible();
  await expect(
    page.locator('[data-testid="active-operation-link"]'),
  ).toBeVisible();
  await capture("filtered-empty-blocker");
  active = [];
  await navigate("&filter_status=interrupted");
  await expect(
    table
      .getByText("Showing 1–50 of 60 matching operations", { exact: true })
      .first(),
  ).toBeVisible();
  await expect(
    page.locator('[data-testid="start-operation-refresh_catalog"]'),
  ).toBeEnabled();
  await expect(
    page.locator('[data-testid="active-operation-link"]'),
  ).toHaveCount(0);
  await capture("terminal-history-eligible");
  // A conflict after the eligible snapshot must refresh, not repeat the POST.
  const before = activeReads;
  await page.locator('[data-testid="start-operation-refresh_catalog"]').click();
  await expect(
    page.locator('[data-testid="active-operation-link"]'),
  ).toBeVisible();
  expect(activeReads).toBeGreaterThan(before);
  expect(starts).toBe(1);
  await capture("conflict-refreshed");
  active = [];
  conflictSettles = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await expect(
    page.locator('[data-testid="start-operation-refresh_catalog"]'),
  ).toBeEnabled();
  const beforeSettled = activeReads;
  await page.locator('[data-testid="start-operation-refresh_catalog"]').click();
  await expect.poll(() => activeReads).toBeGreaterThan(beforeSettled);
  await expect(
    page.locator('[data-testid="start-operation-refresh_catalog"]'),
  ).toBeEnabled();
  await expect(
    page.locator('[data-testid="active-operation-link"]'),
  ).toHaveCount(0);
  expect(starts).toBe(2);
  await expect(
    page.getByText(
      "Refreshed state: no active operation remains. Review available actions before starting a new intent.",
      { exact: true },
    ),
  ).toBeVisible();
  active = [make(62, "refresh_catalog", "running")];
  await page.locator('[data-testid="manual-refresh"]').click();
  await expect(
    page.locator('[data-testid="active-operation-link"]'),
  ).toBeVisible();
  await expect(
    page.locator('[data-testid="start-operation-refresh_catalog"]'),
  ).toBeEnabled();
  await expect(page.locator('[data-testid^="start-operation-"]')).toHaveCount(
    1,
  );
  active = [];
  const historyBeforeRefresh = await table.boundingBox();
  let release!: () => void;
  delay = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.locator('[data-testid="manual-refresh"]').click();
  try {
    await expect(
      page.locator('[data-testid="start-operation-refresh_catalog"]'),
    ).toBeDisabled();
    expect((await table.boundingBox())?.y).toBe(historyBeforeRefresh?.y);
    await capture("active-loading");
  } finally {
    delay = undefined;
    release();
  }
  await expect(
    page.locator('[data-testid="start-operation-refresh_catalog"]'),
  ).toBeEnabled();
  unavailable = true;
  await page.locator('[data-testid="manual-refresh"]').click();
  await expect(
    page.locator('[data-testid="start-operation-refresh_catalog"]'),
  ).toBeDisabled();
  await expect(
    page.getByText("Active-work status is not current", { exact: true }),
  ).toBeVisible();
  await capture("active-unavailable");
  expect(starts).toBe(2);
  unavailable = false;
  return screenshots;
}
