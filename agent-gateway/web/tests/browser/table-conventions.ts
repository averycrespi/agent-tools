import { expect, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

// Called by each existing domain owner; no separate scenario or fixture lifecycle.
export async function assertTableConventions(
  page: Page,
  caption: string,
  columns: readonly string[],
  rowHeading: string,
  comparison = false,
): Promise<void> {
  const originalViewport = page.viewportSize();
  const region = page
    .getByRole("region", { name: caption, exact: true })
    .and(page.locator(".table-region"));
  const row = region.locator("tbody tr").first();
  await expect(row).toBeVisible();
  const headings = await region.locator("thead th").allInnerTexts();
  expect(headings.map((text) => text.replace(/[↑↓↕]/g, "").trim())).toEqual(
    columns,
  );
  await expect(row.locator('th[scope="row"]')).toHaveCount(1);
  if (!comparison) {
    await expect(row.locator('th[scope="row"]')).toHaveAttribute(
      "data-label",
      rowHeading,
    );
  }
  expect(
    await region.locator("time").evaluateAll(
      (times) =>
        times.filter((node) => {
          const value = node.getAttribute("datetime")!;
          const formatted = new Intl.DateTimeFormat(undefined, {
            dateStyle: "medium",
            timeStyle: "medium",
          }).format(new Date(value));
          return (
            node.textContent !== formatted ||
            node.getAttribute("title") !== value ||
            !node.querySelector(".user-time-clock")
          );
        }).length,
    ),
  ).toBe(0);
  const completeText = await row.textContent();
  const artifacts = await mkdtemp(
    join(
      tmpdir(),
      `gateway-table-${caption.toLowerCase().replace(/[^a-z]+/g, "-")}-`,
    ),
  );
  try {
    for (const width of [1440, 720, 390, 320]) {
      await page.setViewportSize({ width, height: 1000 });
      await row.scrollIntoViewIfNeeded();
      await expect(row).toHaveText(completeText!);
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth <= innerWidth,
        ),
      ).toBe(true);
      const layout = await row.evaluate((node) => {
        const region = node.closest(".table-region")!;
        const bounds = region.getBoundingClientRect();
        return {
          display: getComputedStyle(node).display,
          regionOverflow: region.scrollWidth > region.clientWidth,
          clippedControls: [...node.querySelectorAll("a,button")].filter(
            (control) => {
              const rect = control.getBoundingClientRect();
              return rect.left < bounds.left || rect.right > bounds.right;
            },
          ).length,
          actionsReachable: [
            ...node.querySelectorAll(
              ".column-actions a,.column-actions button",
            ),
          ].every((control) => {
            const rect = control.getBoundingClientRect();
            return rect.left >= bounds.left && rect.right <= bounds.right;
          }),
          numericAlignment: [
            ...node.querySelectorAll(".column-count,.column-measure"),
          ].map((cell) => getComputedStyle(cell).textAlign),
        };
      });
      expect(layout.actionsReachable).toBe(true);
      await expect(region.getByRole("table")).toHaveCount(1);
      if (!comparison) {
        await expect(
          region.getByRole("columnheader", {
            name: rowHeading,
            exact: true,
          }),
        ).toHaveCount(1);
        await expect(row.getByRole("rowheader")).toHaveCount(1);
      }
      if (width < 700 && !comparison) {
        expect(layout.display).toBe("grid");
        expect(layout.regionOverflow).toBe(false);
        expect(layout.clippedControls).toBe(0);
        expect(layout.numericAlignment.every((value) => value === "left")).toBe(
          true,
        );
        const collection = region.locator("..");
        const sort = collection.locator(".table-sort-controls");
        if (await sort.count()) {
          await expect(sort).toBeVisible();
          await sort.locator("select").focus();
          await expect(sort.locator("select")).toBeFocused();
        }
        await expect(region.locator("thead button:visible")).toHaveCount(0);
      } else {
        await expect(
          region.locator("..").locator(".table-sort-controls"),
        ).not.toBeVisible();
        if (caption === "Grant request summaries") {
          expect(
            await row
              .locator(".column-actions")
              .evaluate((cell) => cell.getBoundingClientRect().width),
          ).toBeLessThan(160);
        }
        if (caption === "Grant policy records") {
          expect(
            await row
              .locator('[data-label="Effect"]')
              .evaluate((cell) => cell.getBoundingClientRect().width),
          ).toBeLessThan(112);
          expect(
            await row
              .locator('[data-label="Status"]')
              .evaluate((cell) => cell.getBoundingClientRect().width),
          ).toBeLessThan(160);
        }
        expect(
          await row.locator(".table-identifier").evaluateAll((identifiers) =>
            identifiers.every((identifier) => {
              const range = document.createRange();
              range.selectNodeContents(identifier);
              const style = getComputedStyle(identifier);
              const lineHeight =
                parseFloat(style.lineHeight) ||
                parseFloat(style.fontSize) * 1.5;
              return range.getBoundingClientRect().height <= lineHeight + 1;
            }),
          ),
          `${caption}: desktop IDs must stay on one line at ${width}px`,
        ).toBe(true);
        expect(layout.display).toBe("table-row");
        expect(
          layout.numericAlignment.every((value) => value === "right"),
        ).toBe(true);
      }
      if (width >= 700 && (await row.locator(".column-actions").count())) {
        for (const control of await row
          .locator("a,button:not(:disabled)")
          .all()) {
          await control.focus();
          await expect(control).toBeFocused();
          expect(
            await control.evaluate((element) => {
              const rect = element.getClientRects()[0]!;
              return element.contains(
                document.elementFromPoint(
                  rect.left + rect.width / 2,
                  rect.top + rect.height / 2,
                ),
              );
            }),
            `${caption}: pinned actions must not obscure keyboard focus at ${width}px`,
          ).toBe(true);
        }
      }
      await region.evaluate((element) => {
        element.scrollLeft = 0;
      });
      await row.scrollIntoViewIfNeeded();
      await page.screenshot({ path: join(artifacts, `${width}.png`) });
    }
  } finally {
    if (originalViewport) await page.setViewportSize(originalViewport);
  }
}
