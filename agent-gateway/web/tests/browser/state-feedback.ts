import { capture } from "../frontend/capture.ts";
import AxeBuilder from "@axe-core/playwright";
import { captureDetailLayout } from "./detail-layout.ts";
import { expect, type Page } from "@playwright/test";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

export async function captureStateFeedback(
  page: Page,
  state: string,
): Promise<void> {
  await capture(page, state);
  if (
    [
      "tool-schema-summary",
      "system-stale",
      "request-reload-failed",
      "agent-reload-failed",
      "http-credential-invalid",
    ].includes(state)
  )
    await captureDetailLayout(page, state);
  const directory = await mkdtemp(join(tmpdir(), `gateway-feedback-${state}-`));
  const previous = page.viewportSize();
  for (const width of [1280, 390]) {
    await page.setViewportSize({ width, height: 900 });
    expect(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    ).toBe(true);
    const scan = await new AxeBuilder({ page }).analyze();
    expect(
      scan.violations.filter(
        (item) => item.impact === "serious" || item.impact === "critical",
      ),
    ).toEqual([]);
    await page.screenshot({
      path: join(directory, `${width}.png`),
      fullPage: (await page.locator("dialog[open]").count()) === 0,
    });
    await page.screenshot({ path: join(directory, `${width}-viewport.png`) });
    await writeFile(
      join(directory, `${width}-focus.json`),
      JSON.stringify(
        await page.evaluate(() => {
          const skip = document.querySelector<HTMLElement>(".skip-link");
          return {
            activeTag: document.activeElement?.tagName,
            activeID: document.activeElement?.id,
            skipTop: skip?.getBoundingClientRect().top,
            skipPosition: skip ? getComputedStyle(skip).position : null,
          };
        }),
      ),
    );
  }
  if (previous !== null) await page.setViewportSize(previous);
}
