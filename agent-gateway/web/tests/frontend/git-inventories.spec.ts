import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
const secondID = "01ARZ3NDEKTSV4RRFFQ69G5FA1";
const thirdID = "01ARZ3NDEKTSV4RRFFQ69G5FA2";
const timestamp = "2026-10-01T12:00:00.000000000Z";
const repo = {
  id,
  name: "Alpine repository with a long recognizable project name",
  url: "https://example.invalid:443/team/repo",
  aliases: ["https://example.invalid:443/team/repo.git"],
  credential_id: null,
  revision: "1",
  alias_revision: "1",
  created_at: timestamp,
  updated_at: timestamp,
};

test("git-inventory-routing", async ({ page, frontend }) => {
  registerCapture(page, "git-inventory-routing");
  let profileReads = 0;
  let coverage: "missing" | "covered" | "inactive" | "error" | "loading" =
    "missing";
  let release: (() => void) | undefined;
  let writes = 0;
  const queries: URLSearchParams[] = [];
  await page.route("**/api/v2/git/**", async (route) => {
    const request = route.request(),
      url = new URL(request.url());
    if (url.pathname === "/api/v2/git/routing-profile") {
      profileReads++;
      if (coverage === "loading")
        await new Promise<void>((resolve) => {
          release = resolve;
        });
      if (coverage === "error")
        return route.fulfill({
          status: 503,
          json: { status: 503, code: "unavailable", title: "Unavailable" },
        });
      return route.fulfill({
        json: {
          revision: "1",
          active: coverage !== "inactive",
          origins:
            coverage === "covered"
              ? ["https://example.invalid:443"]
              : ["https://example.invalid:8443"],
        },
        headers: { ETag: '"git-profile-routing-1"' },
      });
    }
    if (url.pathname === "/api/v2/git/credentials")
      return route.fulfill({
        json: { items: [], next_cursor: null, total_count: 0, offset: 0 },
      });
    if (request.method() === "POST") {
      writes++;
      expect(request.postDataJSON()).toMatchObject({
        name: "Unrouted creation",
        url: "https://example.invalid/team/new",
        aliases: ["https://example.invalid/team/new.git"],
        credential_id: null,
      });
      return route.fulfill({
        status: 201,
        json: {
          ...repo,
          name: "Unrouted creation",
          url: "https://example.invalid:443/team/new",
          aliases: ["https://example.invalid:443/team/new.git"],
        },
        headers: { ETag: `"git-repository-${id}-1"` },
      });
    }
    if (url.pathname === `/api/v2/git/repositories/${id}`)
      return route.fulfill({
        json: repo,
        headers: { ETag: `"git-repository-${id}-1"` },
      });
    if (url.pathname === "/api/v2/git/repositories") {
      queries.push(url.searchParams);
      const second = url.searchParams.has("cursor");
      return route.fulfill({
        json: {
          items: second
            ? [{ ...repo, id: thirdID, name: "Zulu repository" }]
            : [repo, { ...repo, id: secondID, name: "Beta repository" }],
          next_cursor: second ? null : "synthetic-page-two",
          total_count: 3,
          offset: second ? 2 : 0,
        },
      });
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
    await page.goto(`${frontend.origin}/#/git/repositories${suffix}`);
  };
  await nav();
  await expect(page.getByText("Not routed", { exact: true })).toHaveCount(2);
  // Activation/reconnect may produce multiple generations. Each complete view
  // read still shares one profile across both rows, not one read per row.
  expect(profileReads).toBe(queries.length);
  await capture(page, "inventory-not-routed", true);
  await page.getByRole("button", { name: "Next", exact: true }).first().click();
  await expect(page.getByRole("table")).toContainText("Zulu repository");
  expect(queries.at(-1)?.get("cursor")).toBe("synthetic-page-two");
  await page
    .getByRole("button", { name: "Previous", exact: true })
    .first()
    .click();
  await expect(page.getByRole("table")).toContainText(repo.name);
  await page.getByRole("link", { name: repo.name, exact: true }).click();
  await expect(
    page.getByRole("heading", { name: repo.name, exact: true }),
  ).toBeFocused();
  const originEnabled = page
    .locator(".detail-facts > div")
    .filter({ has: page.locator("dt", { hasText: /^Origin enabled$/ }) })
    .locator("dd");
  await expect(originEnabled).toHaveText("No");
  await expect(
    page.getByRole("link", { name: "Git routing", exact: true }),
  ).toHaveCount(0);
  await expect(page.locator("main")).not.toContainText(
    "Coverage does not establish",
  );
  await capture(page, "detail-not-routed", true);
  coverage = "covered";
  await page.getByTestId("manual-refresh").click();
  await expect(originEnabled).toHaveText("Yes");
  await expect(page.getByText("Not routed", { exact: true })).toHaveCount(0);
  await capture(page, "detail-covered");
  coverage = "error";
  await page.getByTestId("manual-refresh").click();
  await expect(originEnabled).toHaveText("Unavailable");
  await capture(page, "routing-stale");
  coverage = "inactive";
  await page.getByTestId("manual-refresh").click();
  await expect(originEnabled).toHaveText("No");
  await capture(page, "routing-inactive");
  coverage = "error";
  await page.goto("about:blank");
  await nav();
  await expect(page.getByText("Routing unknown", { exact: true })).toHaveCount(
    2,
  );
  await capture(page, "routing-unknown");
  coverage = "loading";
  await page.getByTestId("manual-refresh").click();
  await expect(page.getByText("Checking routing", { exact: true })).toHaveCount(
    2,
  );
  await expect.poll(() => release !== undefined).toBe(true);
  await capture(page, "routing-loading");
  coverage = "missing";
  release?.();
  await expect(page.getByText("Not routed", { exact: true })).toHaveCount(2);
  await page
    .getByRole("link", { name: "Create repository", exact: true })
    .click();
  await page.getByLabel("Name", { exact: true }).fill("Unrouted creation");
  await page
    .getByLabel("Canonical HTTPS destination", { exact: true })
    .fill("https://example.invalid/team/new");
  await page
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toContainText("Not routed");
  await expect(
    page
      .getByRole("dialog")
      .getByRole("link", { name: "Git routing", exact: true }),
  ).toBeVisible();
  await capture(page, "create-not-routed-review", true);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create Git repository", exact: true })
    .click();
  await expect.poll(() => writes).toBe(1);
  await expect(page).toHaveURL(new RegExp(`/git/repositories/${id}$`));
});
