import AxeBuilder from "@axe-core/playwright";
import { expect, type BrowserContext, type Page } from "@playwright/test";
import { mkdtemp } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { assertSecretAbsent, waitForLifecycle } from "./shared.ts";
import { captureDetailLayout } from "./detail-layout.ts";

export async function runGit(
  browserVersion: string,
  context: BrowserContext,
  page: Page,
  baseURL: string,
  bearer: string,
  requestCount: () => number,
): Promise<number[]> {
  const expectedFailures: number[] = [];
  const deletingPaths = new Set<string>();
  let conflictPath: string | undefined;
  page.on("response", (response) => {
    const path = new URL(response.url()).pathname;
    if (
      (response.status() === 412 &&
        path === conflictPath &&
        response.request().method() === "PATCH") ||
      (response.status() === 404 &&
        deletingPaths.has(path) &&
        response.request().method() === "GET")
    )
      expectedFailures.push(response.status());
  });
  const screenshots = await mkdtemp(join(tmpdir(), "gateway-git-"));
  const capture = async (name: string) => {
    await captureDetailLayout(page, `git-${name}`);
    for (const theme of ["light", "dark"] as const) {
      await page.emulateMedia({ colorScheme: theme });
      for (const [suffix, width, height] of [
        ["desktop", 1280, 900],
        ["narrow", 390, 844],
        ["320", 320, 800],
      ] as const) {
        await page.setViewportSize({ width, height });
        await page.screenshot({
          path: join(screenshots, `${name}-${theme}-${suffix}.png`),
          fullPage: true,
        });
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= window.innerWidth,
          ),
        ).toBe(true);
      }
    }
    await page.emulateMedia({ colorScheme: "light" });
    const axe = await new AxeBuilder({ page })
      .withTags(["wcag2a", "wcag2aa", "wcag21aa", "wcag22aa"])
      .analyze();
    expect(
      axe.violations
        .filter((v) => v.impact === "serious" || v.impact === "critical")
        .map((v) => ({ id: v.id, nodes: v.nodes.map((n) => n.target) })),
    ).toEqual([]);
    await page.setViewportSize({ width: 1280, height: 900 });
  };
  const nav = async (path: string) => {
    await page.evaluate((fragment) => {
      window.location.hash = fragment;
    }, path);
  };
  await waitForLifecycle(page, "signed_out");
  await page.locator('[data-testid="admin-bearer-input"]').fill(bearer);
  await page.locator('[data-testid="sign-in-submit"]').click();
  await waitForLifecycle(page, "authenticated");
  await page
    .locator('#primary-navigation a[href="#/git/repositories"]')
    .click();
  await expect(
    page.getByText("No Git repositories", { exact: true }),
  ).toBeVisible();
  await capture("repositories-empty");
  await page
    .getByRole("link", { name: "Create repository", exact: true })
    .click();
  await page
    .getByLabel("Name", { exact: true })
    .fill("Canonical workshop repository");
  await page
    .getByLabel("Canonical HTTPS destination", { exact: true })
    .fill("https://example.com/team/workshop");
  await expect(page.getByLabel("Alias 1", { exact: true })).toHaveValue(
    "https://example.com/team/workshop.git",
  );
  await capture("repository-alias-added");
  await page
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await capture("repository-review");
  const repositoryResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v2/git/repositories") &&
      r.request().method() === "POST",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create Git repository", exact: true })
    .click();
  const repository = await (await repositoryResponse).json();
  await expect(
    page.getByRole("heading", {
      name: "Canonical workshop repository",
      exact: true,
    }),
  ).toBeVisible();
  await expect(
    page.getByLabel("Canonical HTTPS destination", { exact: true }),
  ).toHaveAttribute("readonly", "");
  await capture("repository-saved");
  await nav("#/git/credentials/new");
  await page
    .getByLabel("Name", { exact: true })
    .fill("Workshop host credential");
  await page
    .getByLabel("HTTPS origin", { exact: true })
    .fill("https://example.com");
  await page
    .getByLabel("Secret", { exact: true })
    .fill("git-browser-secret-canary");
  await page
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  // Outside controls remain inert until the dialog close effect settles.
  await expect(page.getByRole("dialog")).not.toBeVisible();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  await page
    .getByLabel("Secret", { exact: true })
    .fill("git-browser-secret-canary");
  await page
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  await expect(page.getByRole("dialog")).toBeVisible();
  const credentialResponse = page
    .waitForResponse(
      (r) =>
        r.url().endsWith("/api/v2/git/credentials") &&
        r.request().method() === "POST",
    )
    .catch(() => undefined);
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create Git credential", exact: true })
    .click();
  const credentialReply = await credentialResponse;
  if (credentialReply === undefined)
    throw new Error(
      `Git credential submission did not reach the API: ${(await page.getByRole("alert").allTextContents()).join("; ")}`,
    );
  const credential = await credentialReply.json();
  await expect(
    page.getByRole("heading", {
      name: "Workshop host credential",
      exact: true,
    }),
  ).toBeVisible();
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    ["git-browser-secret-canary"],
    true,
  );
  await page.route(
    `**/api/v2/git/credentials/${credential.id}`,
    async (route) => {
      if (route.request().method() !== "GET") return route.fallback();
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        headers: {
          ETag: `"git-credential-${credential.id}-${credential.revision}"`,
        },
        json: { ...credential, available: false },
      });
    },
  );
  await page
    .getByRole("button", { name: "Refresh current view", exact: true })
    .click();
  await expect(page.getByText("Unavailable", { exact: true })).toBeVisible();
  await capture("credential-unavailable");
  const principalResponse = await context.request.post(
    `${baseURL}/api/v2/principals`,
    {
      headers: {
        Authorization: `Bearer ${bearer}`,
        Cookie: "",
        "Content-Type": "application/json",
      },
      data: { display_name: "Git workshop agent", visibility: "all" },
    },
  );
  expect(principalResponse.status()).toBe(201);
  const principal = await principalResponse.json();
  await nav("#/git/grants/new");
  await page
    .getByLabel("Agent", { exact: true })
    .selectOption(principal.principal.id);
  await page
    .getByLabel("Repository", { exact: true })
    .selectOption(repository.id);
  await page
    .getByLabel("Description (optional)", { exact: true })
    .fill("Read and update workshop");
  await page
    .getByRole("switch", { name: "Read repository", exact: true })
    .uncheck();
  await page
    .getByRole("button", { name: "Add push rule", exact: true })
    .click();
  await expect(
    page.getByRole("switch", { name: "Read repository", exact: true }),
  ).toBeChecked();
  await page
    .getByLabel("Ref selector", { exact: true })
    .fill("refs/heads/main");
  await capture("grant-authoring");
  await page
    .getByRole("button", { name: "Review and create", exact: true })
    .click();
  const grantResponse = page.waitForResponse(
    (r) =>
      r.url().endsWith("/api/v2/git/grants") && r.request().method() === "POST",
  );
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Create Git grant", exact: true })
    .click();
  const grant = await (await grantResponse).json();
  expect(grant.policy.read).toBe(true);
  expect(grant.policy.refs[0].actions).toEqual(["update"]);
  await expect(
    page.getByRole("heading", {
      name: "Read and update workshop",
      exact: true,
    }),
  ).toBeVisible();
  await page.route(`**/api/v2/git/grants/${grant.id}`, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      headers: { ETag: `"git-grant-${grant.id}-${grant.revision}"` },
      json: { ...grant, state: "expired", expires_at: "2020-01-01T00:00:00Z" },
    });
  });
  await page
    .getByRole("button", { name: "Refresh current view", exact: true })
    .click();
  await expect(page.getByText("Expired", { exact: true })).toBeVisible();
  await capture("grant-expired");
  const trafficID = "01ARZ3NDEKTSV4RRFFQ69G5FAV",
    now = "2026-09-30T12:00:00.000000000Z";
  const traffic = {
    admission: {
      id: trafficID,
      admitted_at: now,
      evaluated_at: now,
      principal: { id: principal.principal.id, revision: "1" },
      agent_credential: { id: trafficID, revision: "1" },
      repository: { id: repository.id, revision: repository.revision },
      alias_revision: "1",
      profile_revision: "1",
      authorization_revision: "1",
      operation: "push",
      commands: 1,
      allowed: true,
      policy: {
        repository_name: "Historical repository label",
        repository_url: repository.url,
        grants: [{ id: grant.id, revision: grant.revision }],
        grant_count: 1,
        creates: 0,
        updates: 1,
        deletes: 0,
      },
    },
    completion: null,
  };
  await page.route("**/api/v2/git/traffic?*", (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      json: { items: [traffic], next_cursor: null },
    }),
  );
  await page.route(`**/api/v2/git/traffic/${trafficID}`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "application/json",
      json: traffic,
    }),
  );
  await nav("#/git/traffic");
  await expect(
    page.getByRole("link", { name: "Push", exact: true }),
  ).toBeVisible();
  await capture("traffic-incomplete");
  await page.getByRole("link", { name: "Push", exact: true }).click();
  await expect(
    page.getByText("Reconcile an uncertain push", { exact: false }),
  ).toBeVisible();
  await capture("traffic-detail");
  await page
    .getByText("Admission-time policy references", { exact: true })
    .click();
  await expect(
    page.getByText("Authorization revision", { exact: true }),
  ).toBeVisible();
  await capture("traffic-policy-references");
  await page.unroute("**/api/v2/git/traffic?*");
  await page.route("**/api/v2/git/traffic?*", (route) =>
    route.fulfill({ status: 200, contentType: "application/json", json: {} }),
  );
  await nav("#/git/traffic");
  await expect(page.getByText(/unavailable/i).first()).toBeVisible();
  await capture("traffic-error");

  await page.unroute(`**/api/v2/git/credentials/${credential.id}`);
  await page.unroute(`**/api/v2/git/grants/${grant.id}`);
  const apiHeaders = { Authorization: `Bearer ${bearer}`, Cookie: "" };
  const submitReview = async (label: string) => {
    await page
      .getByRole("dialog")
      .getByRole("button", { name: label, exact: true })
      .click();
    await expect(page.getByRole("dialog")).not.toBeVisible();
  };

  // Cause a real stale-ETag rejection after the edited request is dispatched.
  conflictPath = `/api/v2/git/repositories/${repository.id}`;
  await nav(`#/git/repositories/${repository.id}`);
  await page
    .getByLabel("Name", { exact: true })
    .fill("Retained repository draft");
  await page.route(
    `**/api/v2/git/repositories/${repository.id}`,
    async (route) => {
      if (route.request().method() !== "PATCH") return route.fallback();
      const concurrent = await context.request.patch(
        `${baseURL}/api/v2/git/repositories/${repository.id}`,
        {
          headers: {
            ...apiHeaders,
            "If-Match": `"git-repository-${repository.id}-${repository.revision}"`,
          },
          data: {
            name: "Concurrent repository edit",
            url: repository.url,
            aliases: repository.aliases,
            credential_id: null,
          },
        },
      );
      expect(concurrent.status()).toBe(200);
      await route.continue();
    },
  );
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await capture("repository-edit-review");
  const conflictResponse = page.waitForResponse(
    (r) =>
      r.request().method() === "PATCH" &&
      r.url().endsWith(`/git/repositories/${repository.id}`),
  );
  await submitReview("Edit repository");
  expect((await conflictResponse).status()).toBe(412);
  await page.unroute(`**/api/v2/git/repositories/${repository.id}`);
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue(
    "Retained repository draft",
  );
  const editRepository = page.locator("section.panel").filter({
    has: page.getByRole("heading", {
      name: "Edit repository",
      exact: true,
    }),
  });
  await expect(
    editRepository.getByText("Revision changed", { exact: true }),
  ).toBeVisible();
  await capture("repository-conflict");
  await page
    .getByRole("link", { name: "Back to Git repositories", exact: true })
    .click();
  const protectedNavigation = page.getByRole("dialog", {
    name: "Discard unsaved changes?",
    exact: true,
  });
  await expect(protectedNavigation).toBeVisible();
  await capture("repository-conflict-navigation");
  await protectedNavigation
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(protectedNavigation).not.toBeVisible();
  await expect(page).toHaveURL(
    new RegExp(`#/git/repositories/${repository.id}$`),
  );
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue(
    "Retained repository draft",
  );
  await editRepository
    .getByRole("button", { name: "Use reviewed revision", exact: true })
    .click();
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await submitReview("Edit repository");
  await expect(
    page.getByRole("heading", {
      name: "Retained repository draft",
      exact: true,
    }),
  ).toBeVisible();
  const savedRepository = await context.request.get(
    `${baseURL}/api/v2/git/repositories/${repository.id}`,
    { headers: apiHeaders },
  );
  expect((await savedRepository.json()).name).toBe("Retained repository draft");

  let releaseSave!: () => void;
  const saveHeld = new Promise<void>((resolve) => {
    releaseSave = resolve;
  });
  await page.route(
    `**/api/v2/git/repositories/${repository.id}`,
    async (route) => {
      if (route.request().method() !== "PATCH") return route.fallback();
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      await saveHeld;
      await route.fulfill({ response });
    },
  );
  await page
    .getByLabel("Name", { exact: true })
    .fill("Submitted repository name");
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  const requestDispatched = page.waitForRequest(
    (r) =>
      r.method() === "PATCH" &&
      r.url().endsWith(`/git/repositories/${repository.id}`),
  );
  const heldResponse = page.waitForResponse(
    (r) =>
      r.request().method() === "PATCH" &&
      r.url().endsWith(`/git/repositories/${repository.id}`),
  );
  try {
    await submitReview("Edit repository");
    await requestDispatched;
    await expect(page.getByLabel("Name", { exact: true })).toBeDisabled();
    await expect(page.getByLabel("Alias 1", { exact: true })).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "Add alias", exact: true }),
    ).toBeDisabled();
    await expect(
      page.getByRole("button", { name: "Remove alias 1", exact: true }),
    ).toBeDisabled();
    // Native controls cannot accept a newer draft before this acknowledgement.
    await page.keyboard.insertText("must-not-change-the-pending-draft");
    await expect(page.getByLabel("Name", { exact: true })).toHaveValue(
      "Submitted repository name",
    );
    await capture("repository-save-inflight");
  } finally {
    releaseSave();
  }
  expect((await heldResponse).status()).toBe(200);
  await page.unroute(`**/api/v2/git/repositories/${repository.id}`);
  await expect(
    page.getByRole("heading", {
      name: "Submitted repository name",
      exact: true,
    }),
  ).toBeVisible();
  await expect(page.getByLabel("Name", { exact: true })).toBeEnabled();
  await page
    .getByLabel("Name", { exact: true })
    .fill("Post-acknowledgement draft");
  await page
    .getByRole("link", { name: "Back to Git repositories", exact: true })
    .click();
  await expect(protectedNavigation).toBeVisible();
  await protectedNavigation
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(protectedNavigation).not.toBeVisible();
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue(
    "Post-acknowledgement draft",
  );
  const acknowledgedRepository = await context.request.get(
    `${baseURL}/api/v2/git/repositories/${repository.id}`,
    { headers: apiHeaders },
  );
  expect((await acknowledgedRepository.json()).name).toBe(
    "Submitted repository name",
  );
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await submitReview("Edit repository");
  await expect(
    page.getByRole("heading", {
      name: "Post-acknowledgement draft",
      exact: true,
    }),
  ).toBeVisible();

  await nav(`#/git/grants/${grant.id}`);
  await page
    .getByLabel("Description (optional)", { exact: true })
    .fill("Updated Git grant");
  await page.getByRole("switch", { name: "Create", exact: true }).check();
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await capture("grant-edit-review");
  await submitReview("Edit grant");
  await expect(
    page.getByRole("heading", { name: "Updated Git grant", exact: true }),
  ).toBeVisible();
  const savedGrant = await context.request.get(
    `${baseURL}/api/v2/git/grants/${grant.id}`,
    { headers: apiHeaders },
  );
  expect((await savedGrant.json()).policy.refs[0].actions).toContain("create");

  await nav(`#/git/credentials/${credential.id}`);
  await page.getByLabel("Name", { exact: true }).fill("Updated Git credential");
  await page
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await capture("credential-edit-review");
  await submitReview("Edit credential");
  await expect(
    page.getByRole("heading", { name: "Updated Git credential", exact: true }),
  ).toBeVisible();
  const rotateEditor = page.locator("section.panel").filter({
    has: page.getByRole("heading", {
      name: "Rotate secret",
      exact: true,
    }),
  });
  await expect(
    rotateEditor.getByRole("button", { name: "Review rotation", exact: true }),
  ).toBeEnabled();
  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    0,
  );
  await page
    .getByLabel("Secret", { exact: true })
    .fill("git-rotation-secret-canary");
  await page
    .getByRole("button", { name: "Review rotation", exact: true })
    .click();
  await capture("credential-rotation-review");
  const rotationResponse = page.waitForResponse(
    (r) =>
      r.request().method() === "POST" &&
      r.url().endsWith(`/git/credentials/${credential.id}/rotate`),
  );
  await submitReview("Rotate secret");
  const rotated = await (await rotationResponse).json();
  expect(rotated.available).toBe(true);
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  await expect(
    page.getByRole("button", { name: "Review rotation", exact: true }),
  ).toBeEnabled();

  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    0,
  );
  await page
    .getByRole("link", { name: "Back to Git credentials", exact: true })
    .click();
  await expect(page.getByRole("table")).toContainText("Updated Git credential");
  await expect(protectedNavigation).not.toBeVisible();
  await nav(`#/git/credentials/${credential.id}`);

  // The server performs one cutover, but its malformed acknowledgement cannot
  // qualify a known result. The UI must block replay and clear material.
  let uncertainRequests = 0;
  await page.route(
    `**/api/v2/git/credentials/${credential.id}/rotate`,
    async (route) => {
      uncertainRequests++;
      const response = await route.fetch();
      expect(response.status()).toBe(200);
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        json: {},
      });
    },
  );
  await page
    .getByLabel("Secret", { exact: true })
    .fill("git-uncertain-secret-canary");
  await page
    .getByRole("button", { name: "Review rotation", exact: true })
    .click();
  await submitReview("Rotate secret");
  await expect(
    page.getByText("Change outcome unknown", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Review rotation", exact: true }),
  ).toBeDisabled();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  await capture("credential-uncertain");
  expect(uncertainRequests).toBe(1);
  const reconciled = await context.request.get(
    `${baseURL}/api/v2/git/credentials/${credential.id}`,
    { headers: apiHeaders },
  );
  expect(BigInt((await reconciled.json()).revision)).toBe(
    BigInt(rotated.revision) + 1n,
  );
  await page.unroute(`**/api/v2/git/credentials/${credential.id}/rotate`);
  // Explicit fresh inspection after reconciliation, never an automatic retry.
  await page.reload();
  await waitForLifecycle(page, "authenticated");
  await expect(
    page.getByRole("heading", { name: "Updated Git credential", exact: true }),
  ).toBeVisible();
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    ["git-rotation-secret-canary", "git-uncertain-secret-canary"],
    true,
  );

  for (const [kind, id, singularName, empty] of [
    ["grants", grant.id, "grant", "No Git grants"],
    ["repositories", repository.id, "repository", "No Git repositories"],
    ["credentials", credential.id, "credential", "No Git credentials"],
  ] as const) {
    await nav(`#/git/${kind}/${id}`);
    await page
      .getByRole("button", { name: "Review deletion", exact: true })
      .click();
    await capture(`${singularName}-deletion-review`);
    deletingPaths.add(`/api/v2/git/${kind}/${id}`);
    const deletionResponse = page.waitForResponse(
      (r) =>
        r.request().method() === "DELETE" &&
        r.url().endsWith(`/git/${kind}/${id}`),
    );
    await submitReview(`Delete ${singularName}`);
    expect((await deletionResponse).status()).toBe(204);
    await expect(page.getByText(empty, { exact: true })).toBeVisible();
    const deleted = await context.request.get(
      `${baseURL}/api/v2/git/${kind}/${id}`,
      { headers: apiHeaders },
    );
    expect(deleted.status()).toBe(404);
  }
  await assertSecretAbsent(
    page,
    context,
    baseURL,
    [bearer, "git-browser-secret-canary"],
    true,
  );
  // Choice traversal must include page two and preserve a selected identity through failed refresh.
  const fixtureID = (n: number) => n.toString().padStart(26, "0");
  const agents = Array.from({ length: 51 }, (_, i) => ({
    ...principal.principal,
    id: fixtureID(i + 1),
    display_name: i >= 49 ? "Duplicate agent" : `Agent ${i}`,
  }));
  const repositories = Array.from({ length: 51 }, (_, i) => ({
    ...repository,
    id: fixtureID(i + 101),
    name: i >= 49 ? "Duplicate repository" : `Repository ${i}`,
  }));
  const credentials = Array.from({ length: 51 }, (_, i) => ({
    ...credential,
    id: fixtureID(i + 201),
    name: i >= 49 ? "Duplicate credential" : `Credential ${i}`,
    available: true,
  }));
  let choicesUnavailable = false;
  let missingCredential = false;
  let secondPages = 0;
  const choicesRoute =
    /\/api\/v2\/(principals|git\/(repositories|credentials))\?/;
  await page.route(choicesRoute, async (route) => {
    if (choicesUnavailable) {
      expectedFailures.push(503);
      return route.fulfill({
        status: 503,
        contentType: "application/json",
        json: {},
      });
    }
    const url = new URL(route.request().url());
    const values = url.pathname.endsWith("principals")
      ? agents
      : url.pathname.endsWith("repositories")
        ? repositories
        : credentials.filter(
            (c) => !missingCredential || c.id !== fixtureID(251),
          );
    const offset = url.searchParams.has("cursor") ? 50 : 0;
    if (offset) secondPages++;
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      json: {
        items: values.slice(offset, offset + 50),
        total_count: values.length,
        offset,
        next_cursor: offset + 50 < values.length ? "choice-page-two" : null,
      },
    });
  });
  await nav("#/git/grants/new");
  await page.getByLabel("Agent", { exact: true }).selectOption(fixtureID(51));
  await page
    .getByLabel("Repository", { exact: true })
    .selectOption(fixtureID(151));
  await expect(
    page.getByLabel("Agent", { exact: true }).locator("option:checked"),
  ).toHaveText(`Duplicate agent · ${fixtureID(51)}`);
  await expect(
    page.getByLabel("Repository", { exact: true }).locator("option:checked"),
  ).toHaveText(`Duplicate repository · ${fixtureID(151)}`);
  expect(secondPages).toBeGreaterThanOrEqual(2);
  await expect(page.locator("#git-principal-create-hint")).toHaveText(
    fixtureID(51),
  );
  await expect(page.locator("#git-repository-create-hint")).toHaveText(
    fixtureID(151),
  );
  await capture("duplicate-paginated-grant-choices");
  choicesUnavailable = true;
  await page
    .getByRole("button", { name: "Refresh current view", exact: true })
    .click();
  await expect(
    page.getByText("Resource choices unavailable", { exact: true }),
  ).toBeVisible();
  await expect(page.getByLabel("Agent", { exact: true })).toHaveValue(
    fixtureID(51),
  );
  await capture("choices-unavailable-retained-draft");
  choicesUnavailable = false;
  await page
    .locator('#primary-navigation a[href="#/git/repositories"]')
    .click();
  await protectedNavigation
    .getByRole("button", { name: "Discard and leave", exact: true })
    .click();
  await page
    .getByRole("link", { name: "Create repository", exact: true })
    .click();
  await page
    .getByLabel("Canonical HTTPS destination", { exact: true })
    .fill("https://example.com/another/repository");
  await page
    .getByLabel("Git credential", { exact: true })
    .selectOption(fixtureID(251));
  await expect(
    page
      .getByLabel("Git credential", { exact: true })
      .locator("option:checked"),
  ).toHaveText(`Duplicate credential · ${fixtureID(251)}`);
  missingCredential = true;
  await page
    .getByRole("button", { name: "Refresh current view", exact: true })
    .click();
  await expect(
    page
      .getByLabel("Git credential", { exact: true })
      .locator("option:checked"),
  ).toContainText("Unavailable or incompatible");
  await expect(page.getByLabel("Git credential", { exact: true })).toHaveValue(
    fixtureID(251),
  );
  await capture("missing-selected-credential");
  await page.unroute(choicesRoute);
  process.stdout.write(
    JSON.stringify({
      event: "git_complete",
      chromium_version: browserVersion,
      requests: requestCount(),
      screenshots,
    }) + "\n",
  );
  return expectedFailures;
}
