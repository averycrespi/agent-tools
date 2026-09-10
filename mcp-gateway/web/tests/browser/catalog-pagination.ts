import { expect, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { descriptorReadFixture, serverReadFixture } from "./fixtures.ts";
import { parseFragment, serializeLocation } from "../../src/location.ts";

export async function exerciseCatalogPagination(page: Page): Promise<string[]> {
  const artifacts = await mkdtemp(join(tmpdir(), "tools8-visual-"));
  const screenshots: string[] = [];
  const capture = async (kind: string, state: string) => {
    for (const width of [1280, 390]) {
      await page.setViewportSize({ width, height: 900 });
      await page
        .locator(".collection-pagination")
        .first()
        .scrollIntoViewIfNeeded();
      const path = join(artifacts, `${kind}-${state}-${width}.png`);
      await page.screenshot({ path });
      screenshots.push(path);
      if (state === "populated") {
        await page
          .locator(".collection-pagination")
          .last()
          .scrollIntoViewIfNeeded();
        const bottom = join(artifacts, `${kind}-${state}-${width}-bottom.png`);
        await page.screenshot({ path: bottom });
        screenshots.push(bottom);
      }
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= window.innerWidth,
        ),
      ).toBe(true);
    }
    await page.setViewportSize({ width: 1280, height: 900 });
  };
  const id = (index: number) => String(index + 1000).padStart(26, "0");
  for (const path of ["servers", `servers/${id(0)}?tab=tools`, "catalog"]) {
    const separator = path.includes("?") ? "&" : "?";
    const filter = path === "servers" ? "name" : "tool";
    expect(
      parseFragment(
        `#/${path}${separator}filter_${filter}=${encodeURIComponent("\ufdfa".repeat(20))}`,
      ),
    ).toBeUndefined();
    const sorted = `#/${path}${separator}sort=${path === "servers" ? "namespace" : "tool"}&direction=descending`;
    const parsed = parseFragment(sorted);
    expect(parsed).toBeDefined();
    expect(parseFragment(serializeLocation(parsed!))).toEqual(parsed);
  }
  const servers = Array.from({ length: 60 }, (_, index) => ({
    ...serverReadFixture(id(index), {
      name: `Server ${String(59 - index).padStart(3, "0")}`,
      desired: "disabled",
      runtime: "inactive",
      credential: "not_required",
      durable: "current",
      active: "absent",
    }),
    namespace: `namespace_${index}`,
  }));
  const tools = Array.from({ length: 125 }, (_, index) =>
    descriptorReadFixture(
      id(index + 100),
      servers[0]!.id,
      `tool_${String(124 - index).padStart(3, "0")}`,
      index < 5,
    ),
  );
  const catalog = tools.map((item, index) => ({
    ...item,
    retired_at: null,
    server_display_name: index % 2 === 0 ? "A server" : "Z server",
    server_catalog_state: index % 2 === 0 ? "current" : "stale",
  }));
  const counts = { servers: 0, descriptors: 0, catalog: 0 };
  let stale = false;
  let failures = false;
  let staleRequests = 0;
  let release: (() => void) | undefined;
  let delayed = false;
  await page.unroute("**/api/v1/servers**");
  await page.unroute("**/api/v1/catalog**");
  const fulfill = async (route: import("@playwright/test").Route) => {
    const url = new URL(route.request().url());
    const query = url.searchParams;
    const isDescriptor = url.pathname.endsWith("/descriptors");
    const kind =
      url.pathname === "/api/v1/catalog"
        ? "catalog"
        : isDescriptor
          ? "descriptors"
          : "servers";
    if (url.pathname !== "/api/v1/servers" && kind === "servers") {
      await route.fulfill({
        status: 200,
        headers: {
          "Content-Type": "application/json",
          ETag: `"server-${servers[0]!.id}-7"`,
        },
        body: JSON.stringify(servers[0]),
      });
      return;
    }
    counts[kind]++;
    expect(query.get("limit")).toBe("50");
    expect(query.has("sort")).toBe(true);
    expect(query.has("retired")).toBe(false);
    const cursor = query.get("cursor");
    if (failures || (stale && cursor !== null)) {
      if (cursor !== null) staleRequests++;
      await route.fulfill({
        status: failures ? 503 : 409,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status: failures ? 503 : 409,
          code: failures ? "storage_unavailable" : "stale_cursor",
          title: "Unavailable",
        }),
      });
      return;
    }
    const search = query.get(kind === "servers" ? "name" : "tool") ?? "";
    if (search === "delayed") {
      delayed = true;
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    }
    const position = cursor === null ? 0 : Number(cursor);
    let items: Record<string, unknown>[] =
      kind === "servers" ? servers : kind === "descriptors" ? tools : catalog;
    items = items.filter((item) => {
      const name = String(
        item[kind === "servers" ? "display_name" : "external_name"],
      );
      if (!`${name} ${item.id}`.toLowerCase().includes(search.toLowerCase()))
        return false;
      if (
        query.has("namespace") &&
        !String(item.namespace).includes(query.get("namespace")!)
      )
        return false;
      if (
        query.has("server") &&
        !String(item.server_display_name).includes(query.get("server")!)
      )
        return false;
      const status =
        kind === "servers"
          ? "Disabled"
          : kind === "catalog"
            ? item.server_catalog_state === "current"
              ? "available"
              : "issue"
            : item.retired_at === null
              ? "available"
              : "retired";
      return !query.has("status") || query.get("status") === status;
    });
    const sort = query.get("sort");
    const field =
      sort === "name"
        ? "display_name"
        : sort === "tool"
          ? "external_name"
          : sort === "server"
            ? "server_display_name"
            : sort === "last-seen"
              ? "last_seen_at"
              : sort === "namespace"
                ? "namespace"
                : "id";
    items.sort((a, b) => {
      const order = String(a[field]).localeCompare(String(b[field]));
      return order === 0
        ? String(a.id).localeCompare(String(b.id))
        : query.get("direction") === "descending"
          ? -order
          : order;
    });
    const result = {
      items: items.slice(position, position + 50),
      next_cursor: position + 50 < items.length ? String(position + 50) : null,
    };
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(
        kind === "catalog"
          ? {
              ...result,
              catalog: {
                active_state: "degraded",
                active_generation: "process-8",
                changed_at: null,
                issue_count: 1,
              },
            }
          : result,
      ),
    });
  };
  await page.route("**/api/v1/servers**", fulfill);
  await page.route("**/api/v1/catalog**", fulfill);
  const next = page.getByRole("button", { name: "Next", exact: true }).last();
  const previous = page
    .getByRole("button", { name: "Previous", exact: true })
    .first();
  for (const kind of ["servers", "descriptors", "catalog"] as const) {
    const row = page.getByTestId(
      kind === "servers"
        ? "server-row"
        : kind === "descriptors"
          ? "descriptor-row"
          : "catalog-row",
    );
    const fragment =
      kind === "descriptors"
        ? `#/servers/${servers[0]!.id}?tab=tools`
        : `#/${kind}`;
    const before = counts[kind];
    await page.evaluate((hash) => {
      window.location.hash = hash;
    }, fragment);
    await expect(row).toHaveCount(50);
    await expect(page.locator(".collection-pagination")).toHaveCount(2);
    await expect(
      page.locator('.collection-pagination [aria-live="polite"]'),
    ).toHaveCount(1);
    await expect(next).toBeEnabled();
    await expect(previous).toBeDisabled();
    expect(counts[kind] - before).toBe(1);
    await capture(kind, "populated");
    const firstNames = await row.allTextContents();
    await next.click();
    await expect(row).toHaveCount(kind === "servers" ? 10 : 50);
    await expect(previous).toBeEnabled();
    if (kind !== "servers") {
      await next.click();
      await expect(row).toHaveCount(25);
    }
    await expect(next).toBeDisabled();
    await previous.click();
    await expect(row).toHaveCount(50);
    if (kind !== "servers") {
      await previous.click();
      await expect(previous).toBeDisabled();
    }
    await expect(row).toHaveText(firstNames);
    const field = page.getByLabel(kind === "servers" ? "Name or ID" : "Tool", {
      exact: true,
    });
    const validHash = await page.evaluate(() => window.location.hash);
    const validReads = counts[kind];
    await field.fill("\ufdfa".repeat(20));
    await expect(page.getByRole("alert")).toContainText("256 UTF-8 bytes");
    expect(await page.evaluate(() => window.location.hash)).toBe(validHash);
    expect(counts[kind]).toBe(validReads);
    await capture(kind, "invalid-filter");
    await field.fill("");
    await expect(row).toHaveCount(50);
    const sortKey =
      kind === "servers" ? "namespace" : kind === "catalog" ? "server" : "tool";
    const column = page.getByRole("columnheader", {
      name:
        kind === "servers"
          ? /^Namespace/
          : kind === "catalog"
            ? /^Server/
            : /^Tool/,
    });
    const source =
      kind === "servers" ? servers : kind === "catalog" ? catalog : tools;
    const expected = (direction: "ascending" | "descending") =>
      [...source]
        .sort((a, b) => {
          const value = (item: (typeof source)[number]) =>
            kind === "servers"
              ? String((item as (typeof servers)[number]).namespace)
              : kind === "catalog"
                ? String((item as (typeof catalog)[number]).server_display_name)
                : String((item as (typeof tools)[number]).external_name);
          const comparison = value(a).localeCompare(value(b));
          return comparison === 0
            ? a.id.localeCompare(b.id)
            : direction === "descending"
              ? -comparison
              : comparison;
        })
        .map((item) =>
          kind === "servers"
            ? (item as (typeof servers)[number]).display_name
            : (item as (typeof tools)[number]).external_name,
        );
    await column.getByRole("button").click();
    await expect(column).toHaveAttribute("aria-sort", "ascending");
    await expect(row.locator('th[scope="row"] .table-primary')).toHaveText(
      expected("ascending").slice(0, 50),
    );
    const ascendingHash = await page.evaluate(() => window.location.hash);
    expect(ascendingHash).toContain(`sort=${sortKey}`);
    expect(ascendingHash).toContain("direction=ascending");
    await next.click();
    await expect(row.locator('th[scope="row"] .table-primary')).toHaveText(
      expected("ascending").slice(50, 100),
    );
    await column.getByRole("button").click();
    await expect(previous).toBeDisabled();
    await expect(column).toHaveAttribute("aria-sort", "descending");
    await expect(row.locator('th[scope="row"] .table-primary')).toHaveText(
      expected("descending").slice(0, 50),
    );
    const descendingHash = await page.evaluate(() => window.location.hash);
    expect(descendingHash).toContain("direction=descending");
    await next.click();
    await expect(row.locator('th[scope="row"] .table-primary')).toHaveText(
      expected("descending").slice(50, 100),
    );
    await page.goBack();
    await expect(row.locator('th[scope="row"] .table-primary')).toHaveText(
      expected("ascending").slice(0, 50),
    );
    expect(await page.evaluate(() => window.location.hash)).toBe(ascendingHash);
    await expect(column).toHaveAttribute("aria-sort", "ascending");
    await page.goForward();
    await expect(row.locator('th[scope="row"] .table-primary')).toHaveText(
      expected("descending").slice(0, 50),
    );
    expect(await page.evaluate(() => window.location.hash)).toBe(
      descendingHash,
    );
    await expect(column).toHaveAttribute("aria-sort", "descending");
    await page.reload();
    await expect(row.locator('th[scope="row"] .table-primary')).toHaveText(
      expected("descending").slice(0, 50),
    );
    await expect(previous).toBeDisabled();
    expect(await page.evaluate(() => window.location.hash)).toBe(
      descendingHash,
    );
    await capture(kind, "sorted");
    await page.evaluate((hash) => {
      window.location.hash = hash;
    }, fragment);
    await expect(row).toHaveText(firstNames);
    const target =
      kind === "servers"
        ? "Server 059"
        : kind === "descriptors"
          ? "tool_000"
          : "tool_124";
    await field.fill(target);
    await expect(row).toHaveCount(1);
    await expect(previous).toBeDisabled();
    await expect(next).toBeDisabled();
    await expect(field).toBeFocused();
    const matchingHash = await page.evaluate(() => window.location.hash);
    expect(matchingHash).toContain(
      kind === "servers" ? "filter_name=" : "filter_tool=",
    );
    await field.fill("no matches");
    await expect(row).toHaveCount(0);
    await expect(field).toBeVisible();
    await capture(kind, "no-matches");
    await page.goBack();
    await expect(field).toHaveValue(target);
    await expect(row).toHaveCount(1);
    await page.goForward();
    await expect(field).toHaveValue("no matches");
    await expect(row).toHaveCount(0);
    await page.getByRole("button", { name: "Reset", exact: true }).click();
    await expect(row).toHaveCount(50);
    await next.click();
    await expect(previous).toBeEnabled();
    const shared = await page.evaluate(() => window.location.hash);
    expect(shared).not.toContain("cursor");
    await page.reload();
    await expect(row).toHaveCount(50);
    await expect(previous).toBeDisabled();
    stale = true;
    const staleBefore = staleRequests;
    const readsBefore = counts[kind];
    await next.click();
    await expect(
      page.getByText(
        "The previous page expired or changed. Restarted at the first page.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(row).toHaveCount(50);
    await expect(previous).toBeDisabled();
    expect(staleRequests - staleBefore).toBe(1);
    expect(counts[kind] - readsBefore).toBe(2);
    await capture(kind, "restarted");
    stale = false;
    failures = true;
    await page.getByTestId("manual-refresh").click();
    await expect(
      page.getByText(
        "Collection data is unavailable. Use Refresh to try again.",
        { exact: true },
      ),
    ).toBeVisible();
    await expect(field).toBeVisible();
    await capture(kind, "error");
    failures = false;
    await page.getByTestId("manual-refresh").click();
    await expect(row).toHaveCount(50);
    delayed = false;
    await field.fill("delayed");
    await expect.poll(() => delayed).toBe(true);
    await expect(
      page.getByText("Loading…", { exact: true }).first(),
    ).toBeVisible();
    await expect(
      page.locator(".collection-pagination .table-filter-summary"),
    ).toHaveText(["Loading…", "Loading…"]);
    for (const button of await page
      .locator(".collection-pagination button")
      .all())
      await expect(button).toBeDisabled();
    await capture(kind, "loading");
    await field.fill(target);
    await expect(row).toHaveCount(1);
    release?.();
    await expect(row).toHaveCount(1);
    await expect(field).toHaveValue(target);
    await page.getByRole("button", { name: "Reset", exact: true }).click();
    await expect(row).toHaveCount(50);
    await page
      .getByLabel("Status", { exact: true })
      .selectOption(
        kind === "servers"
          ? "Disabled"
          : kind === "descriptors"
            ? "retired"
            : "issue",
      );
    await expect(row).toHaveCount(kind === "descriptors" ? 5 : 50);
    await expect(page.locator("body")).not.toContainText("undefined");
  }
  await page.unroute("**/api/v1/servers**", fulfill);
  await page.unroute("**/api/v1/catalog**", fulfill);
  return screenshots;
}
