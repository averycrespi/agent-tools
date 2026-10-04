import { capture, hasCaptureOwner } from "../frontend/capture.ts";
import AxeBuilder from "@axe-core/playwright";
import { expect, type Page } from "@playwright/test";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

// Only public GET projections are replayed; authentication stays with the fixture.
export function prepareOverviewBaseline(page: Page) {
  if (hasCaptureOwner(page)) return async (_state: string) => {};
  const reads = new Map<string, { body: Buffer; contentType: string }>();
  const pending = new Set<Promise<void>>();
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (
      response.request().method() !== "GET" ||
      !path.startsWith("/api/v2/") ||
      path.startsWith("/api/v2/admin-sessions") ||
      response.status() !== 200 ||
      response.headers()["content-type"] !== "application/json"
    )
      return;
    const task = (async () => {
      try {
        reads.set(response.url(), {
          body: await response.body(),
          contentType: "application/json",
        });
      } catch {
        /* Superseded reads cannot qualify a baseline. */
      }
    })();
    pending.add(task);
    void task.finally(() => pending.delete(task));
  });
  return async (state: string) => {
    const assets = process.env.AGENT_GATEWAY_OVERVIEW_BASELINE_DIR;
    if (!assets || !process.env.AGENT_GATEWAY_OVERVIEW_ARTIFACT_DIR) return;
    await Promise.all([...pending]);
    const capturedReads = new Map(reads);
    const baseline = await page.context().newPage();
    const missing: string[] = [];
    try {
      await baseline.route("**/app.{js,css}", async (route) => {
        const file = new URL(route.request().url()).pathname.endsWith(".js")
          ? "app.js"
          : "app.css";
        await route.fulfill({
          contentType: file === "app.js" ? "text/javascript" : "text/css",
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
        expect(route.request().method(), "baseline must remain read-only").toBe(
          "GET",
        );
        const data = capturedReads.get(route.request().url());
        if (!data) {
          missing.push(path);
          await route.fulfill({ status: 503, body: "" });
          return;
        }
        await route.fulfill(data);
      });
      await baseline.goto(page.url());
      for (const source of ["status", "servers", "requests"])
        await expect(
          baseline.getByTestId(`overview-${source}`),
        ).toHaveAttribute("data-panel-status", "current");
      expect(missing, "identical baseline public data").toEqual([]);
      await captureOverviewLayout(baseline, `${state}-before`, false);
      await captureOverviewLayout(page, `${state}-after`);
    } finally {
      await baseline.close();
    }
  };
}

export async function captureOverviewLayout(
  page: Page,
  state: string,
  candidate = true,
) {
  await capture(page, `overview-${state}`, true);
  const root = process.env.AGENT_GATEWAY_OVERVIEW_ARTIFACT_DIR;
  if (!root) return;
  const directory = join(root, state);
  await mkdir(directory, { recursive: true });
  const previous = page.viewportSize();
  const theme = page.getByTestId("theme-preference");
  const previousTheme = await theme.inputValue();
  const previousStoredTheme = await page.evaluate(() =>
    localStorage.getItem("agent_gateway_theme"),
  );
  const evidence: unknown[] = [];
  const emulation = await page.context().newCDPSession(page);
  try {
    await page.emulateMedia({ reducedMotion: "reduce" });
    for (const color of ["light", "dark"]) {
      await theme.selectOption(color);
      await expect(page.locator("html")).toHaveAttribute("data-theme", color);
      for (const [width, height, name] of [
        [1440, 1000, "desktop"],
        [960, 900, "intermediate"],
        [390, 844, "narrow"],
        [320, 800, "320"],
        [720, 500, "200-reflow"],
      ] as const) {
        await page.setViewportSize({ width, height });
        // Render 720 CSS px at 2x density, not a 720-pixel image relabeled as zoom.
        const deviceScaleFactor = name === "200-reflow" ? 2 : 1;
        await emulation.send("Emulation.setDeviceMetricsOverride", {
          width,
          height,
          deviceScaleFactor,
          mobile: false,
        });
        expect(
          await page.evaluate(() => {
            // Full-page capture must not relocate offscreen fixed controls into the image.
            window.scrollTo(0, 0);
            return document.documentElement.scrollWidth <= innerWidth;
          }),
        ).toBe(true);
        const screenshotPath = join(directory, `${color}-${name}.png`);
        if (name === "200-reflow") {
          // Playwright's full-page resize restores the context's original DPR.
          // Capture directly so Chromium retains the actual 2x render scale.
          const metrics = await emulation.send("Page.getLayoutMetrics");
          const image = await emulation.send("Page.captureScreenshot", {
            format: "png",
            captureBeyondViewport: true,
            clip: {
              x: 0,
              y: 0,
              width,
              height: metrics.cssContentSize.height,
              scale: 1,
            },
          });
          const png = Buffer.from(image.data, "base64");
          expect(png.readUInt32BE(16), "actual 2x rendered PNG width").toBe(
            width * 2,
          );
          await writeFile(screenshotPath, png);
        } else {
          await page.screenshot({ path: screenshotPath, fullPage: true });
        }
        const contrast = await page
          .getByTestId("overview-grid")
          .evaluate((grid) => {
            const rgb = (value: string) =>
              value.match(/[\d.]+/g)?.map(Number) ?? [];
            const luminance = (c: number[]) =>
              c
                .slice(0, 3)
                .map((v) => {
                  const s = v / 255;
                  return s <= 0.04045
                    ? s / 12.92
                    : ((s + 0.055) / 1.055) ** 2.4;
                })
                .reduce(
                  (sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i]!,
                  0,
                );
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
            return [...grid.querySelectorAll("*")]
              .filter(
                (node) =>
                  [...node.childNodes].some(
                    (child) =>
                      child.nodeType === Node.TEXT_NODE &&
                      child.textContent?.trim(),
                  ) &&
                  node.checkVisibility({
                    checkVisibilityCSS: true,
                    checkOpacity: true,
                  }),
              )
              .map((node) => {
                const style = getComputedStyle(node),
                  size = parseFloat(style.fontSize);
                const a = luminance(rgb(style.color)),
                  b = luminance(background(node));
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
        if (candidate) {
          for (const item of contrast)
            expect(
              item.ratio,
              `rendered contrast ${state}/${color}/${name}: ${JSON.stringify(item)}`,
            ).toBeGreaterThanOrEqual(item.minimum);
          if (width === 320) {
            if (!hasCaptureOwner(page)) {
              const scan = await new AxeBuilder({ page })
                .include('[data-testid="overview-grid"]')
                .analyze();
              expect(
                scan.violations.filter(
                  (item) =>
                    item.impact === "serious" || item.impact === "critical",
                ),
              ).toEqual([]);
            }
          }
        }
        const indicators = await page
          .getByTestId("overview-grid")
          .evaluate((grid) => {
            const luminance = (value: string) =>
              (value.match(/[\d.]+/g) ?? [])
                .slice(0, 3)
                .map(Number)
                .map((v) => {
                  const s = v / 255;
                  return s <= 0.04045
                    ? s / 12.92
                    : ((s + 0.055) / 1.055) ** 2.4;
                })
                .reduce(
                  (sum, v, i) => sum + v * [0.2126, 0.7152, 0.0722][i]!,
                  0,
                );
            return [
              ...grid.querySelectorAll(
                ".overview-capacity, .overview-evidence, .activity-line",
              ),
            ]
              .filter(
                (node) =>
                  node.getBoundingClientRect().height > 0 ||
                  (node.classList.contains("activity-line") &&
                    node.getBoundingClientRect().width > 0),
              )
              .map((node) => {
                let parent: Element | null = node;
                while (
                  parent &&
                  getComputedStyle(parent).backgroundColor ===
                    "rgba(0, 0, 0, 0)"
                )
                  parent = parent.parentElement;
                const foreground = node.classList.contains("activity-line")
                  ? getComputedStyle(node).stroke
                  : getComputedStyle(node).borderLeftColor;
                const background = parent
                  ? getComputedStyle(parent).backgroundColor
                  : "rgb(255, 255, 255)";
                const a = luminance(foreground),
                  b = luminance(background);
                return {
                  foreground,
                  background,
                  ratio: (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05),
                };
              });
          });
        if (candidate)
          for (const indicator of indicators)
            expect(
              indicator.ratio,
              "meaningful state boundary contrast",
            ).toBeGreaterThanOrEqual(3);
        evidence.push({
          state,
          candidate,
          color,
          width,
          height,
          name,
          deviceScaleFactor,
          contrast,
          indicators,
        });
      }
    }
    await writeFile(
      join(directory, "manifest.json"),
      JSON.stringify(evidence, null, 2),
    );
  } finally {
    await emulation.send("Emulation.clearDeviceMetricsOverride");
    await emulation.detach();
    await theme.selectOption(previousTheme);
    // Restore only presentation persistence changed by this fixture capture.
    await page.evaluate((value) => {
      if (value === null) localStorage.removeItem("agent_gateway_theme");
      else localStorage.setItem("agent_gateway_theme", value);
    }, previousStoredTheme);
    if (previous) await page.setViewportSize(previous);
  }
}
