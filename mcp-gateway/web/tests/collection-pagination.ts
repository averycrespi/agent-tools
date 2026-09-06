import { expect, type Page, type Route } from "@playwright/test";

type Capture = (page: Page, state: string) => Promise<void>;

export async function exerciseCollectionPagination(
  page: Page,
  capture?: Capture,
  restoreSession?: () => Promise<void>,
): Promise<void> {
  const original = await page.evaluate(() => window.location.hash);
  const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  const id = (index: number) =>
    "01ARZ3NDEKTSV4RRFFQ69G5FA0".slice(0, 24) +
    alphabet[Math.floor(index / 32)]! +
    alphabet[index % 32]!;
  const principal = (index: number) => ({
    id: id(index),
    display_name: index === 127 ? "Zulu needle" : "Duplicate name",
    state: index === 127 ? "disabled" : "active",
    visibility: index === 127 ? "all" : "requestable",
    revision: "1",
    credential_revision: "0",
    credential: null,
    created_at: "2026-08-28T12:00:00Z",
    updated_at: "2026-08-28T12:00:00Z",
  });
  const grant = (index: number) => ({
    grant: {
      id: id(index + 200),
      description: index === 127 ? "Zulu needle" : "Duplicate policy",
      revision: "1",
      principal_id: id(index),
      effect: index === 127 ? "deny" : "allow",
      server_id: id(index === 127 ? 900 : 901),
      upstream_name: null,
      constraint: null,
      expires_at: index === 127 ? "2026-08-28T12:00:00Z" : null,
      state: index === 127 ? "expired" : "active",
      created_at: "2026-08-28T12:00:00Z",
    },
    principal_display_name: principal(index).display_name,
    server_display_name: index === 127 ? "Far target" : "Near target",
  });
  let collection: "principals" | "grants" = "principals";
  let mode: "normal" | "stale-next" | "stale-first" | "error" | "empty" =
    "normal";
  const requests: {
    collection: string;
    cursor: string | null;
    query: URLSearchParams;
  }[] = [];
  let referenceLookups = 0;
  let holdNextRead = false;
  let releaseLate: (() => void) | undefined;
  let signalLate: (() => void) | undefined;
  let signalLateFinished: (() => void) | undefined;
  const handler = async (route: Route) => {
    const url = new URL(route.request().url());
    const requested = url.pathname.split("/").pop()!;
    const query = url.searchParams;
    if (requested !== collection) referenceLookups++;
    expect(query.get("limit")).toBe("50");
    if (requested === "principals") expect(query.has("sort")).toBe(true);
    else expect(query.get("representation")).toBe("table");
    const cursor = query.get("cursor");
    requests.push({ collection: requested, cursor, query });
    const problem = async (status: number, code: string) =>
      route.fulfill({
        status,
        contentType: "application/problem+json",
        body: JSON.stringify({
          status,
          code,
          title: "Collection unavailable.",
        }),
      });
    if (mode === "stale-first" || (mode === "stale-next" && cursor !== null)) {
      if (mode === "stale-next") mode = "normal";
      await problem(409, "stale_cursor");
      return;
    }
    if (mode === "error") {
      await problem(503, "authorization_unavailable");
      return;
    }
    const search =
      query.get(requested === "principals" ? "name" : "identity") ?? "";
    if (search === "late" || holdNextRead) {
      holdNextRead = false;
      const held = new Promise<void>((resolve) => {
        releaseLate = resolve;
      });
      signalLate?.();
      await held;
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({
          items: [requested === "principals" ? principal(0) : grant(0)],
          next_cursor: null,
        }),
      });
      signalLateFinished?.();
      return;
    }
    let indices =
      mode === "empty" || search === "missing"
        ? []
        : Array.from({ length: 128 }, (_, index) => index);
    if (
      search === "needle" ||
      query.get("principal") === "needle" ||
      query.get("target") === "Far"
    )
      indices = indices.filter((index) => index === 127);
    if (query.get("state") !== null)
      indices = indices.filter(
        (index) =>
          (requested === "principals"
            ? principal(index).state
            : grant(index).grant.state) === query.get("state"),
      );
    if (query.get("visibility") !== null)
      indices = indices.filter(
        (index) => principal(index).visibility === query.get("visibility"),
      );
    if (query.get("effect") !== null)
      indices = indices.filter(
        (index) => grant(index).grant.effect === query.get("effect"),
      );
    if (query.get("direction") === "descending")
      indices.sort((a, b) => Number(b === 127) - Number(a === 127) || a - b);
    const start = cursor === null ? 0 : Number(cursor.split("-")[1]);
    expect(Number.isInteger(start)).toBe(true);
    const selected = indices.slice(start, start + 50);
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify({
        items: selected.map((index) =>
          requested === "principals" ? principal(index) : grant(index),
        ),
        next_cursor:
          start + 50 < indices.length ? `${requested}-${start + 50}` : null,
      }),
    });
  };
  const serverLookup = async (route: Route) => {
    referenceLookups++;
    await route.abort();
  };
  await page.route("**/api/v1/principals?*", handler);
  await page.route("**/api/v1/grants?*", handler);
  await page.route("**/api/v1/servers?*", serverLookup);
  try {
    for (const selected of ["principals", "grants"] as const) {
      collection = selected;
      mode = "normal";
      const kind = selected === "principals" ? "principal" : "grant";
      const root = page.locator(`[data-testid="${selected}-view"]`);
      const rows = root.locator(`[data-testid="${kind}-row"]`);
      const settled = async (count: number) => {
        await expect(rows).toHaveCount(count);
        await expect(root.locator(".collection-table")).not.toHaveAttribute(
          "aria-busy",
          "true",
        );
      };
      const links = () =>
        rows.evaluateAll((rows) =>
          rows.map((row) => row.querySelector("a")?.getAttribute("href")),
        );
      const previous = root.getByRole("button", {
        name: "Previous",
        exact: true,
      });
      const next = root.getByRole("button", { name: "Next", exact: true });
      const search = root.getByLabel(
        selected === "principals" ? "Name or ID" : "Description or ID",
        { exact: true },
      );
      const before = requests.length;
      await page.evaluate((selected) => {
        window.location.hash = `#/${selected}?sort=${selected === "principals" ? "name" : "description"}&direction=ascending`;
      }, selected);
      await settled(50);
      expect(requests.length - before).toBe(1);
      await expect(previous).toBeDisabled();
      await expect(next).toBeEnabled();
      const first = await links();
      await capture?.(page, `${selected}-populated`);
      await next.click();
      await expect(root.locator("output")).toHaveText("Showing 50 on page 2");
      await settled(50);
      const second = await links();
      expect(second.some((id) => first.includes(id))).toBe(false);
      await next.click();
      await settled(28);
      await expect(next).toBeDisabled();
      await previous.click();
      await expect(root.locator("output")).toHaveText("Showing 50 on page 2");
      await settled(50);
      expect(await links()).toEqual(second);
      await previous.click();
      await expect(root.locator("output")).toHaveText("Showing 50 on page 1");
      await settled(50);
      expect(await links()).toEqual(first);
      await next.click();
      await expect(root.locator("output")).toHaveText("Showing 50 on page 2");
      await search.fill("needle");
      await settled(1);
      await expect(rows).toContainText("Zulu needle");
      await expect(search).toBeFocused();
      expect(requests.at(-1)?.cursor).toBeNull();
      expect(await page.evaluate(() => window.location.hash)).toContain(
        "filter_",
      );
      await expect(previous).toBeDisabled();
      await page.goBack();
      await settled(50);
      await expect(search).toHaveValue("");
      await expect(previous).toBeDisabled();
      await page.goForward();
      await settled(1);
      await expect(search).toHaveValue("needle");
      await root.getByRole("button", { name: "Reset", exact: true }).click();
      await settled(50);
      await next.click();
      await expect(root.locator("output")).toHaveText("Showing 50 on page 2");
      await root
        .getByRole("button", {
          name: selected === "principals" ? "Name" : "Description",
          exact: true,
        })
        .click();
      await expect(root.locator("output")).toHaveText("Showing 50 on page 1");
      await settled(50);
      expect(requests.at(-1)?.cursor).toBeNull();
      expect(requests.at(-1)?.query.get("direction")).toBe("descending");
      const descending = await links();
      expect(descending[0]).toBe(
        `#/${selected}/${id(selected === "principals" ? 127 : 327)}`,
      );
      expect(descending.slice(1)).toEqual(first.slice(0, 49));
      await next.click();
      await expect(root.locator("output")).toHaveText("Showing 50 on page 2");
      const shared = await page.evaluate(() => window.location.hash);
      expect(shared).not.toContain("cursor");
      expect(await page.evaluate(() => Object.keys(sessionStorage))).toEqual(
        [],
      );
      await page.reload();
      await settled(50);
      expect(await page.evaluate(() => window.location.hash)).toBe(shared);
      expect(await links()).toEqual(descending);
      await expect(previous).toBeDisabled();
      const staleStart = requests.length;
      mode = "stale-next";
      await next.click();
      await expect(
        root.getByText(
          "The previous page expired or changed. Restarted at the first page.",
          { exact: true },
        ),
      ).toBeVisible();
      await settled(50);
      expect(requests.length - staleStart).toBe(2);
      await expect(previous).toBeDisabled();
      await capture?.(page, `${selected}-restarted`);
      mode = "stale-first";
      const firstStaleStart = requests.length;
      await search.fill("stale-first");
      await expect(root.getByRole("alert")).toBeVisible();
      expect(requests.length - firstStaleStart).toBe(1);
      mode = "error";
      const failedResponse = page.waitForResponse(
        (response) =>
          new URL(response.url()).pathname === `/api/v1/${selected}` &&
          response.status() === 503,
      );
      await root.getByRole("button", { name: "Reset", exact: true }).click();
      await failedResponse;
      await expect(root.getByRole("alert")).toBeVisible();
      await settled(0);
      await expect(search).toBeVisible();
      await expect(previous).toBeDisabled();
      await expect(next).toBeDisabled();
      await capture?.(page, `${selected}-error`);
      mode = "empty";
      await page.locator('[data-testid="manual-refresh"]').click();
      await expect(
        root.getByText(`No ${selected}`, { exact: true }),
      ).toBeVisible();
      await capture?.(page, `${selected}-empty`);
      mode = "normal";
      await search.fill("missing");
      await expect(root.getByText("No matches", { exact: true })).toBeVisible();
      await expect(search).toHaveValue("missing");
      await capture?.(page, `${selected}-no-matches`);
      const started = new Promise<void>((resolve) => {
        signalLate = resolve;
      });
      const finished = new Promise<void>((resolve) => {
        signalLateFinished = resolve;
      });
      await search.fill("late");
      await started;
      await expect(root.locator(".collection-table")).toHaveAttribute(
        "aria-busy",
        "true",
      );
      await capture?.(page, `${selected}-loading`);
      await search.fill("needle");
      await settled(1);
      releaseLate?.();
      await finished;
      await expect(rows).toContainText("Zulu needle");
      await expect(root.getByRole("alert")).toHaveCount(0);
      const selectFilter = async (
        label: string,
        value: string,
        parameter: string,
      ) => {
        const response = page.waitForResponse((response) => {
          const url = new URL(response.url());
          return (
            url.pathname === `/api/v1/${selected}` &&
            url.searchParams.get(parameter) === value
          );
        });
        await root.getByLabel(label, { exact: true }).selectOption(value);
        await response;
        await settled(1);
        expect(requests.at(-1)?.query.get(parameter)).toBe(value);
      };
      if (selected === "principals") {
        await selectFilter("Status", "disabled", "state");
        await selectFilter("Visibility", "all", "visibility");
      } else {
        await selectFilter("Effect", "deny", "effect");
        await selectFilter("State", "expired", "state");
        for (const [label, value, parameter] of [
          ["Principal", "needle", "principal"],
          ["Target", "Far", "target"],
        ]) {
          const response = page.waitForResponse((response) => {
            const url = new URL(response.url());
            return (
              url.pathname === "/api/v1/grants" &&
              url.searchParams.get(parameter!) === value
            );
          });
          await root.getByLabel(label!, { exact: true }).fill(value!);
          await response;
          await settled(1);
          expect(requests.at(-1)?.query.get(parameter!)).toBe(value);
        }
      }
      if (restoreSession !== undefined) {
        await root.getByRole("button", { name: "Reset", exact: true }).click();
        await settled(50);
        await next.click();
        await expect(root.locator("output")).toHaveText("Showing 50 on page 2");
        const fragment = await page.evaluate(() => window.location.hash);
        const oldStarted = new Promise<void>((resolve) => {
          signalLate = resolve;
        });
        const oldFinished = new Promise<void>((resolve) => {
          signalLateFinished = resolve;
        });
        holdNextRead = true;
        await page.locator('[data-testid="manual-refresh"]').click();
        await oldStarted;
        await page.locator('[data-testid="logout"]').click();
        await page
          .locator('[data-testid="logout-confirmation-submit"]')
          .click();
        await expect(
          page.locator('[data-testid="gateway-shell"]'),
        ).toHaveAttribute("data-session-lifecycle", "signed_out");
        await page.waitForFunction(() => window.location.hash === "#/sign-in");
        await page.evaluate((fragment) => {
          window.location.hash = fragment;
        }, fragment);
        await restoreSession();
        await settled(50);
        await expect(previous).toBeDisabled();
        expect(requests.at(-1)?.cursor).toBeNull();
        releaseLate?.();
        await oldFinished;
        await settled(50);
      }
      const validFragment = await page.evaluate(() => window.location.hash);
      const validRequestCount = requests.length;
      await search.fill("é".repeat(129));
      await expect(root.getByRole("alert")).toContainText("256 UTF-8 bytes");
      expect(await page.evaluate(() => window.location.hash)).toBe(
        validFragment,
      );
      expect(requests.length).toBe(validRequestCount);
      expect(referenceLookups).toBe(0);
    }
  } finally {
    releaseLate?.();
    await page.unroute("**/api/v1/principals?*", handler);
    await page.unroute("**/api/v1/grants?*", handler);
    await page.unroute("**/api/v1/servers?*", serverLookup);
    await page.evaluate((fragment) => {
      window.location.hash = fragment;
    }, original);
  }
}
