import { expect, type Page } from "@playwright/test";
import AxeBuilder from "@axe-core/playwright";
import { mkdir, writeFile } from "node:fs/promises";
import { resolve, join, basename } from "node:path";

export const artifactBase = resolve(
  import.meta.dirname,
  "../../../../.frontend-browser",
);
export const artifactRoot = () =>
  join(
    artifactBase,
    "runs",
    process.env.FRONTEND_BROWSER_RUN ?? "unregistered",
  );
const owners = new WeakMap<Page, string>();
export function registerCapture(page: Page, scenario: string) {
  owners.set(page, scenario);
}
export function hasCaptureOwner(page: Page) {
  return owners.has(page);
}
const safe = (value: string) =>
  value.replace(/[^a-zA-Z0-9-]/g, "-").toLowerCase();
const captured = new WeakMap<Page, Set<string>>();

// Retain legacy real-browser artifacts while promoting their explicitly named,
// asserted checkpoints to the frontend matrix when a frontend owner is present.
export async function captureScreenshot(
  page: Page,
  options: NonNullable<Parameters<Page["screenshot"]>[0]>,
) {
  if (options.path)
    await capture(
      page,
      basename(options.path, ".png").replace(
        /-(?:(?:light|dark)-)?(?:1440|1280|640|390|320)$/,
        "",
      ),
    );
  return page.screenshot(options);
}

// Explicit settled checkpoints only, never a timer/DOM observer or pixel baseline.
export async function capture(
  page: Page,
  state: string,
  reflow = false,
): Promise<void> {
  const scenario = owners.get(page);
  if (!scenario) return;
  const key = `${state}/${reflow}`;
  const seen = captured.get(page) ?? new Set<string>();
  if (seen.has(key)) return;
  captured.set(page, seen);
  const directory = join(
    artifactRoot(),
    "captures",
    safe(scenario),
    safe(state),
  );
  await mkdir(directory, { recursive: true });
  const previous = page.viewportSize();
  const preference = await page
    .getByLabel("Theme preference", { exact: true })
    .inputValue();
  const storedTheme = await page.evaluate(() =>
    localStorage.getItem("agent_gateway_theme"),
  );
  const focused = await page.evaluateHandle(() => document.activeElement);
  const scroll = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  const dialog = page.locator("dialog[open]").first();
  const initialDialogScroll = (await dialog.count())
    ? await dialog.evaluate((element) => element.scrollTop)
    : null;
  try {
    for (const theme of ["light", "dark"] as const) {
      await page
        .getByLabel("Theme preference", { exact: true })
        .selectOption(theme, { force: true });
      await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
      for (const [viewport, width, height] of [
        ["desktop", 1440, 900],
        ["mobile", 390, 844],
        ...(reflow
          ? [
              ["320px", 320, 844],
              ["reflow", 640, 450],
            ]
          : []),
      ] as [string, number, number][]) {
        await page.setViewportSize({ width, height });
        await focused.evaluate((element) => {
          if (element instanceof HTMLElement && element.isConnected)
            element.focus({ preventScroll: true });
        });
        // Full-page screenshots taken while scrolled can relocate offscreen
        // fixed controls into the image. Preserve the original scroll in finally.
        await page.evaluate(() => window.scrollTo(0, 0));
        await expect(page.locator("main")).toBeVisible();
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
          `${state} page overflow at ${width}`,
        ).toBe(true);
        const file = `${theme}-${viewport}.png`;
        const hasDialog = (await dialog.count()) > 0;
        if (hasDialog)
          await dialog.evaluate((element) => {
            element.scrollTop = 0;
          });
        // Screenshots contain only synthetic fixtures. Password fields remain masked by Chromium.
        await page.screenshot({
          path: join(directory, file),
          fullPage: !hasDialog,
          animations: "disabled",
        });
        const supplementary: string[] = [];
        if (hasDialog) {
          const geometry = await dialog.evaluate((element) => ({
            max: element.scrollHeight - element.clientHeight,
            step: Math.max(1, element.clientHeight - 40),
          }));
          const pages = Math.ceil(geometry.max / geometry.step);
          expect(
            pages,
            `${scenario}/${state} bounded dialog capture`,
          ).toBeLessThanOrEqual(8);
          for (let index = 1; index <= pages; index++) {
            await dialog.evaluate(
              (element, top) => {
                element.scrollTop = top;
              },
              Math.min(index * geometry.step, geometry.max),
            );
            const continuation = `${theme}-${viewport}-dialog-${index + 1}.png`;
            await page.screenshot({
              path: join(directory, continuation),
              animations: "disabled",
            });
            supplementary.push(continuation);
          }
          await dialog.evaluate((element) => {
            element.scrollTop = 0;
          });
        }
        const scan = await new AxeBuilder({ page })
          .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
          .analyze();
        expect(
          scan.violations.filter(
            (v) => v.impact === "serious" || v.impact === "critical",
          ),
          `${scenario}/${state}/${theme}/${viewport} accessibility`,
        ).toEqual([]);
        await writeFile(
          join(directory, `${theme}-${viewport}.json`),
          JSON.stringify({
            scenario,
            state,
            theme,
            viewport,
            width,
            height,
            file,
            supplementary,
            route: new URL(page.url()).hash,
            browser: page.context().browser()?.version(),
            run: process.env.FRONTEND_BROWSER_RUN,
          }),
        );
      }
    }
    seen.add(key);
  } finally {
    await page
      .getByLabel("Theme preference", { exact: true })
      .selectOption(preference, { force: true });
    await page.evaluate((value) => {
      if (value === null) localStorage.removeItem("agent_gateway_theme");
      else localStorage.setItem("agent_gateway_theme", value);
    }, storedTheme);
    if (previous) await page.setViewportSize(previous);
    if (initialDialogScroll !== null && (await dialog.count()))
      await dialog.evaluate((element, top) => {
        element.scrollTop = top;
      }, initialDialogScroll);
    await focused.evaluate((element) => {
      if (element instanceof HTMLElement && element.isConnected)
        element.focus();
    });
    await page.evaluate(
      (position) => window.scrollTo(position.x, position.y),
      scroll,
    );
    await focused.dispose();
  }
}
