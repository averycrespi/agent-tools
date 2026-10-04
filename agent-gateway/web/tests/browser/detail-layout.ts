import { capture, hasCaptureOwner } from "../frontend/capture.ts";
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
  if (hasCaptureOwner(page)) return async (_name: string) => {};
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
      // Titles are part of the comparison, not the identity fence: a readable-title
      // change must still be comparable with the prior ID-based heading.
      expect(baseline.url()).toBe(page.url());
      await expect(baseline.locator("h1")).toBeVisible();
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
  await capture(page, `table-${state}`);
  if (process.env.AGENT_GATEWAY_TABLE_ARTIFACT_DIR !== undefined)
    await captureDetailLayout(page, `table-${state}`);
}

export async function captureDetailLayout(
  page: Page,
  state: string,
  candidate = true,
): Promise<void> {
  await capture(page, state);
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
            [960, 900, "intermediate"],
            [390, 844, "narrow"],
            [320, 800, "320"],
            [640, 450, "200-reflow"],
          ] as const);
    for (const color of colors) {
      if (directory !== undefined) {
        await theme.selectOption(color);
        await expect(page.locator("html")).toHaveAttribute("data-theme", color);
      }
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
                  boundaries: (() => {
                    let count = 0;
                    for (
                      let parent = row.parentElement;
                      parent;
                      parent = parent.parentElement
                    )
                      if (parent.matches(".detail-section, .panel, dialog")) {
                        count++;
                        // A modal's top layer is independent of its JSX mount point.
                        if (parent.matches("dialog")) break;
                      }
                    return count;
                  })(),
                  overflow: value.scrollWidth > value.clientWidth + 1,
                };
              }),
          );
        const surfaces = await page
          .locator(".detail-section")
          .evaluateAll((nodes) =>
            nodes
              .filter((node) => node.checkVisibility())
              .map((node) => {
                const style = getComputedStyle(node);
                return {
                  name:
                    node.getAttribute("aria-labelledby") ??
                    node.getAttribute("aria-label") ??
                    node.querySelector("h2, summary")?.textContent,
                  background: style.backgroundColor,
                  canvas: getComputedStyle(document.body).backgroundColor,
                  border: parseFloat(style.borderTopWidth),
                  radius: parseFloat(style.borderTopLeftRadius),
                  padding: parseFloat(style.paddingLeft),
                  nested:
                    node.parentElement?.closest(
                      ".detail-section, .panel, dialog",
                    ) !== null,
                  pageTitle:
                    node.querySelector(
                      "h1, .detail-navigation, .server-tabs",
                    ) !== null,
                };
              }),
          );
        const innerGroups = await page
          .locator(
            ".detail-group, .detail-section .operator-status-section, .detail-section .operator-status-details",
          )
          .evaluateAll((nodes) =>
            nodes
              .filter((node) => node.checkVisibility())
              .map((node) => ({
                background: getComputedStyle(node).backgroundColor,
                radius: parseFloat(getComputedStyle(node).borderTopLeftRadius),
                padding: parseFloat(getComputedStyle(node).paddingLeft),
              })),
          );
        if (candidate) {
          for (const badge of await page
            .locator(".detail-context-heading > .status-label")
            .all()) {
            const lines = await badge.evaluate((node) => {
              const range = document.createRange();
              range.selectNodeContents(node);
              return [...range.getClientRects()].length;
            });
            expect(
              lines,
              "primary status must not be squeezed by a long title",
            ).toBe(1);
          }
          for (const surface of surfaces) {
            expect(
              surface.background,
              `${state}: surfaced ${surface.name}`,
            ).not.toBe("rgba(0, 0, 0, 0)");
            expect(surface.background).not.toBe(surface.canvas);
            expect(surface.border).toBeGreaterThan(0);
            expect(surface.radius).toBeGreaterThan(0);
            expect(surface.padding).toBeGreaterThan(0);
            expect(
              surface.nested,
              `${state}: no nested generic card ${surface.name}`,
            ).toBe(false);
            expect(
              surface.pageTitle,
              "resource context stays outside cards",
            ).toBe(false);
          }
          for (const group of innerGroups) {
            expect(group.background).toBe("rgba(0, 0, 0, 0)");
            expect(group.radius).toBe(0);
            expect(group.padding).toBe(0);
          }
          const valueOffset =
            facts[0] === undefined ? 0 : facts[0].valueX - facts[0].labelX;
          for (const fact of facts) {
            if (width > 600)
              expect(
                fact.valueX - fact.labelX,
                `aligned value column: ${fact.label}`,
              ).toBeCloseTo(valueOffset, 0);
            expect(fact.background).toBe("rgba(0, 0, 0, 0)");
            expect(
              fact.boundaries,
              `${state}: one outer boundary for ${fact.label}`,
            ).toBe(1);
            expect(fact.overflow, `${state}: ${fact.label} overflow`).toBe(
              false,
            );
            if (width <= 600)
              expect(fact.valueTop).toBeGreaterThanOrEqual(fact.labelBottom);
          }
          if (directory !== undefined && width === 320 && color === "light") {
            if (!hasCaptureOwner(page)) {
              const scan = await new AxeBuilder({ page }).analyze();
              expect(
                scan.violations.filter(
                  (v) => v.impact === "serious" || v.impact === "critical",
                ),
              ).toEqual([]);
            }
          }
        }
        if (directory === undefined) continue;
        const contrast = await page.evaluate(() => {
          const rgb = (value: string) =>
            value.match(/[\d.]+/g)?.map(Number) ?? [];
          const luminance = (color: number[]) =>
            color
              .slice(0, 3)
              .map((v) => {
                const s = v / 255;
                return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
              })
              .reduce((sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i]!, 0);
          const background = (node: Element): number[] => {
            const c = rgb(getComputedStyle(node).backgroundColor);
            if (c.length === 3 || c[3] === 1) return c;
            const behind = node.parentElement
              ? background(node.parentElement)
              : [255, 255, 255];
            const alpha = c[3] ?? 0;
            return behind
              .slice(0, 3)
              .map((v, i) => (c[i] ?? 0) * alpha + v * (1 - alpha));
          };
          const root = document.querySelector("dialog[open]");
          const nodes = root
            ? [...root.querySelectorAll("*")]
            : [
                ...document.querySelectorAll(
                  ".detail-context *, .detail-section *, .detail-group *, .audit-history *",
                ),
              ];
          return nodes
            .filter(
              (node) =>
                node.checkVisibility({
                  checkVisibilityCSS: true,
                  checkOpacity: true,
                }) &&
                !node.matches(":disabled") &&
                [...node.childNodes].some(
                  (child) =>
                    child.nodeType === Node.TEXT_NODE &&
                    child.textContent?.trim(),
                ),
            )
            .map((node) => {
              const style = getComputedStyle(node);
              const a = luminance(rgb(style.color)),
                b = luminance(background(node));
              const size = parseFloat(style.fontSize);
              return {
                text: node.textContent?.slice(0, 80),
                foreground: style.color,
                background: background(node),
                ratio: (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05),
                minimum:
                  size >= 24 ||
                  (size >= 18.66 && Number(style.fontWeight) >= 700)
                    ? 3
                    : 4.5,
              };
            });
        });
        if (candidate)
          for (const item of contrast)
            expect(
              item.ratio,
              `rendered ${state}/${color}/${viewport} contrast: ${JSON.stringify(item)}`,
            ).toBeGreaterThanOrEqual(item.minimum);
        const indicators = await page.evaluate(() => {
          const luminance = (color: string) =>
            (color.match(/[\d.]+/g) ?? [])
              .slice(0, 3)
              .map(Number)
              .map((value) => {
                const s = value / 255;
                return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
              })
              .reduce(
                (sum, value, i) => sum + value * [0.2126, 0.7152, 0.0722][i]!,
                0,
              );
          // Status labels carry text; generic card/notice borders are decorative.
          // The keyboard focus outline is the meaningful non-text indicator.
          return [...document.querySelectorAll(":focus-visible")]
            .filter((node) => node.checkVisibility())
            .flatMap((node) => {
              const style = getComputedStyle(node);
              if (parseFloat(style.outlineWidth) <= 0) return [];
              let parent: Element | null = node.parentElement;
              while (
                parent &&
                getComputedStyle(parent).backgroundColor === "rgba(0, 0, 0, 0)"
              )
                parent = parent.parentElement;
              const background = parent
                ? getComputedStyle(parent).backgroundColor
                : "rgb(255, 255, 255)";
              const foreground = style.outlineColor;
              const a = luminance(foreground),
                b = luminance(background);
              return [
                {
                  kind: "focus",
                  foreground,
                  background,
                  ratio: (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05),
                },
              ];
            });
        });
        if (candidate)
          for (const indicator of indicators)
            expect(
              indicator.ratio,
              `rendered ${state}/${color}/${viewport} indicator contrast: ${JSON.stringify(indicator)}`,
            ).toBeGreaterThanOrEqual(3);
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
          surfaces,
          innerGroups,
          contrast,
          indicators,
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
