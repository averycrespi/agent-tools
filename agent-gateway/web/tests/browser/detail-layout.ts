import AxeBuilder from "@axe-core/playwright";
import { expect, type Page } from "@playwright/test";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

export async function assertDetailComparison(
  page: Page,
  label: string,
  values: readonly {
    field: string;
    before: string;
    after: string;
    changed: boolean;
  }[],
) {
  const comparison = page.locator(`dl[aria-label="${label}"]`);
  for (const value of values) {
    const row = comparison.locator(".comparison-field").filter({
      has: page.locator("dt", {
        hasText: new RegExp(`^${value.field}(Changed)?$`),
      }),
    });
    await expect(row.locator(".comparison-value > div")).toHaveText([
      value.before,
      value.after,
    ]);
    await expect(row.locator(".comparison-change")).toHaveCount(
      value.changed ? 1 : 0,
    );
  }
}

// Public read-only fixture responses stay in memory, never in evidence artifacts.
export function prepareDetailBaseline(page: Page) {
  const reads = new Map<
    string,
    { body: Buffer; headers: Record<string, string>; status: number }
  >();
  const pending = new Set<Promise<void>>();
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (
      response.request().method() !== "GET" ||
      !path.startsWith("/api/v2/") ||
      path.startsWith("/api/v2/admin-sessions") ||
      !response.headers()["content-type"]?.startsWith("application/json")
    )
      return;
    const task = (async () => {
      try {
        reads.set(response.url(), {
          body: await response.body(),
          headers: response.headers(),
          status: response.status(),
        });
      } catch {
        /* A superseded read is not a baseline. */
      }
    })();
    pending.add(task);
    void task.finally(() => pending.delete(task));
  });
  return async (name: string) => {
    const assets = process.env.AGENT_GATEWAY_DETAIL_BASELINE_DIR;
    if (assets === undefined) return;
    await Promise.all([...pending]);
    const baseline = await page.context().newPage();
    const absent: string[] = [];
    try {
      await baseline.route("**/app.{js,css}", async (route) => {
        const file = new URL(route.request().url()).pathname.endsWith(".js")
          ? "app.js"
          : "app.css";
        await route.fulfill({
          status: 200,
          contentType: file.endsWith(".js") ? "text/javascript" : "text/css",
          body: await readFile(join(assets, file)),
        });
      });
      await baseline.route("**/api/v2/**", async (route) => {
        const path = new URL(route.request().url()).pathname;
        if (
          path.startsWith("/api/v2/admin-sessions") ||
          path === "/api/v2/events"
        )
          return route.continue();
        // The baseline reuses the normal session/view owners, never mutations.
        if (route.request().method() !== "GET")
          return route.fulfill({ status: 204 });
        const response = reads.get(route.request().url());
        if (response === undefined) {
          absent.push(path);
          return route.fulfill({ status: 503, body: "" });
        }
        await route.fulfill(response);
      });
      await baseline.goto(page.url());
      await expect(baseline.getByTestId("gateway-shell")).toHaveAttribute(
        "data-session-lifecycle",
        "authenticated",
      );
      await expect(baseline.locator("h1")).toHaveText(
        await page.locator("h1").innerText(),
      );
      await expect(baseline.locator("dl").first()).toBeVisible();
      await expect(baseline.getByTestId("gateway-shell")).toHaveAttribute(
        "data-freshness",
        "current",
      );
      expect(
        absent,
        "baseline requires identical captured public read data",
      ).toEqual([]);
      await captureDetailLayout(baseline, `${name}-before`, false);
      await captureDetailLayout(page, `${name}-after`);
    } finally {
      await baseline.close();
    }
  };
}

export async function captureTableState(
  page: Page,
  state: string,
): Promise<void> {
  if (process.env.AGENT_GATEWAY_TABLE_ARTIFACT_DIR !== undefined)
    await captureDetailLayout(page, `table-${state}`);
}

export async function captureDetailLayout(
  page: Page,
  state: string,
  candidate = true,
): Promise<void> {
  await expect(page.locator("dialog.sensitive-dialog[open]")).toHaveCount(0);
  const artifactRoot = state.startsWith("table-")
    ? process.env.AGENT_GATEWAY_TABLE_ARTIFACT_DIR
    : process.env.AGENT_GATEWAY_DETAIL_ARTIFACT_DIR;
  const directory =
    artifactRoot === undefined
      ? undefined
      : await mkdtemp(join(artifactRoot, `gateway-detail-${state}-`));
  const previous = page.viewportSize();
  const theme = page.locator('[data-testid="theme-preference"]');
  const previousTheme = await theme.inputValue();
  const storedTheme = await page.evaluate(() =>
    localStorage.getItem("agent_gateway_theme"),
  );
  const evidence: unknown[] = [];
  try {
    // Normal regressions assert desktop/narrow geometry without duplicating the
    // domain axe owners or an expensive artifact sweep in the workflow deadline.
    const colors =
      directory === undefined ? [previousTheme] : ["light", "dark"];
    const viewports =
      directory === undefined
        ? ([
            [1280, 900, "desktop"],
            [320, 800, "320"],
          ] as const)
        : ([
            [1280, 900, "desktop"],
            [390, 844, "narrow"],
            [320, 800, "320"],
            [640, 450, "200-reflow"],
          ] as const);
    for (const color of colors) {
      if (directory !== undefined) await theme.selectOption(color);
      for (const [width, height, viewport] of viewports) {
        await page.setViewportSize({ width, height });
        const dialog = page.locator("dialog[open]");
        const modal = (await dialog.count()) > 0;
        if (modal)
          await dialog.evaluate((node) => {
            node.scrollTop = 0;
          });
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        ).toBe(true);
        const facts = await page
          .locator(
            ".detail-facts > div, .detail-list > div, .review-list > div, .tool-metadata > div, .operator-status-grid > div, .technical-details-grid > div",
          )
          .evaluateAll((rows) =>
            rows
              .filter((row) => row.getBoundingClientRect().height > 0)
              .map((row) => {
                const label = row.querySelector("dt")!,
                  value = row.querySelector("dd")!;
                const l = label.getBoundingClientRect(),
                  v = value.getBoundingClientRect();
                return {
                  label: label.textContent,
                  value: value.textContent,
                  labelX: l.x,
                  valueX: v.x,
                  labelBottom: l.bottom,
                  valueTop: v.top,
                  background: getComputedStyle(row).backgroundColor,
                  overflow: value.scrollWidth > value.clientWidth + 1,
                };
              }),
          );
        if (candidate) {
          const valueOffset =
            facts[0] === undefined ? 0 : facts[0].valueX - facts[0].labelX;
          for (const fact of facts) {
            if (width > 600)
              expect(
                fact.valueX - fact.labelX,
                `aligned value column: ${fact.label}`,
              ).toBeCloseTo(valueOffset, 0);
            expect(fact.background).toBe("rgba(0, 0, 0, 0)");
            expect(fact.overflow, `${state}: ${fact.label} overflow`).toBe(
              false,
            );
            if (width <= 600)
              expect(fact.valueTop).toBeGreaterThanOrEqual(fact.labelBottom);
          }
          if (directory !== undefined && width === 320 && color === "light") {
            const scan = await new AxeBuilder({ page }).analyze();
            expect(
              scan.violations.filter(
                (v) => v.impact === "serious" || v.impact === "critical",
              ),
            ).toEqual([]);
          }
        }
        if (directory === undefined) continue;
        const path = join(directory, `${color}-${viewport}.png`);
        await page.screenshot({ path, fullPage: !modal });
        await page.screenshot({
          path: join(directory, `${color}-${viewport}-viewport.png`),
        });
        evidence.push({
          state,
          url: new URL(page.url()).hash,
          color,
          width,
          height,
          viewport,
          path,
          facts,
          focus: await page.evaluate(() => ({
            tag: document.activeElement?.tagName,
            id: document.activeElement?.id,
          })),
        });
        if (modal) {
          await dialog.evaluate((node) => {
            node.scrollTop = node.scrollHeight;
          });
          for (const action of await dialog.getByRole("button").all()) {
            const bounds = (await action.boundingBox())!;
            expect(bounds.y).toBeGreaterThanOrEqual(0);
            expect(bounds.y + bounds.height).toBeLessThanOrEqual(height);
          }
          await page.screenshot({
            path: join(directory, `${color}-${viewport}-actions.png`),
          });
        }
      }
    }
    if (directory !== undefined)
      await writeFile(
        join(directory, "manifest.json"),
        JSON.stringify(evidence, null, 2),
      );
  } finally {
    if (directory !== undefined) await theme.selectOption(previousTheme);
    // Undo only the theme persistence this capture changed in its owned fixture.
    await page.evaluate((value) => {
      if (value === null) localStorage.removeItem("agent_gateway_theme");
      else localStorage.setItem("agent_gateway_theme", value);
    }, storedTheme);
    if (previous !== null) await page.setViewportSize(previous);
  }
}
