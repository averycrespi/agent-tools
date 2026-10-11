import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
const timestamp = "2026-10-01T12:00:00.000000000Z";

test("git-rotation-reconciliation", async ({ page, frontend }) => {
  registerCapture(page, "git-rotation-reconciliation");
  let current = {
    id,
    revision: "1",
    name: "Synthetic credential",
    origin: "https://example.invalid:443",
    recipe: { header: "Authorization", prefix: "Bearer " },
    available: true,
    referencing_repositories: [],
    created_at: timestamp,
    updated_at: timestamp,
  };
  let writes = 0;
  let mode: "success" | "reject" | "uncertain" | "pending" = "success";
  let release: (() => void) | undefined;
  let readFailure = false;
  let externalAfterWrite = false;
  const nativeWarnings: string[] = [];
  page.on("dialog", async (dialog) => {
    nativeWarnings.push(dialog.message());
    await dialog.dismiss();
  });
  const etag = () => `"git-credential-${id}-${current.revision}"`;
  await page.route("**/api/v2/git/credentials**", async (route) => {
    const req = route.request();
    if (req.method() === "GET" && readFailure)
      return route.fulfill({ status: 503, json: {} });
    if (req.method() === "GET")
      return route.fulfill({
        json: new URL(req.url()).pathname.endsWith(id)
          ? current
          : {
              items: [current],
              next_cursor: null,
              total_count: 1,
              offset: 0,
            },
        headers: { ETag: etag() },
      });
    writes++;
    expect(req.headers()["if-match"]).toBe(etag());
    if (mode === "pending")
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    if (mode === "reject")
      return route.fulfill({
        status: 400,
        contentType: "application/problem+json",
        json: {
          status: 400,
          code: "invalid_request",
          title: "Synthetic rejection",
        },
      });
    if (mode === "uncertain") return route.fulfill({ status: 200, json: {} });
    current = {
      ...current,
      revision: String(Number(current.revision) + 1),
      ...(req.method() === "PATCH" ? req.postDataJSON() : {}),
    };
    const response = { json: current, headers: { ETag: etag() } };
    if (externalAfterWrite)
      current = { ...current, revision: String(Number(current.revision) + 1) };
    return route.fulfill(response);
  });
  await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
  await page.getByTestId("sign-in-submit").click();
  await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
    "data-session-lifecycle",
    "authenticated",
  );
  const open = async () => {
    await page.goto("about:blank");
    await page.goto(`${frontend.origin}/#/git/credentials/${id}`);
    await expect(page.getByLabel("Name", { exact: true })).toHaveValue(
      current.name,
    );
  };
  const rotate = async () => {
    await page
      .getByLabel("Secret", { exact: true })
      .fill("SYNTHETIC_ROTATION_CANARY");
    await page
      .getByRole("button", { name: "Review rotation", exact: true })
      .click();
    await page
      .getByRole("dialog")
      .getByRole("button", { name: "Rotate secret", exact: true })
      .click();
    await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  };
  await open();
  await rotate();
  await expect(page.locator(".detail-section")).toContainText("2");
  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    0,
  );
  await capture(page, "rotation-clean");
  await page
    .getByRole("link", { name: "Back to Git credentials", exact: true })
    .click();
  await expect(page.getByRole("table")).toContainText(current.name);
  expect(nativeWarnings).toEqual([]);
  expect(writes).toBe(1);

  await open();
  const name = page.getByLabel("Name", { exact: true });
  const back = page.getByRole("link", {
    name: "Back to Git credentials",
    exact: true,
  });
  const edit = page.locator("section.panel").filter({
    has: page.getByRole("heading", { name: "Edit credential", exact: true }),
  });
  const deletion = page.locator("section.panel").filter({
    has: page.getByRole("heading", {
      name: "Delete credential",
      exact: true,
    }),
  });
  await name.fill("Retained metadata draft");
  await rotate();
  await expect(
    edit.getByText("Revision changed", { exact: true }),
  ).toBeVisible();
  await expect(
    deletion.getByText("Revision changed", { exact: true }),
  ).toHaveCount(0);
  await expect(name).toHaveValue("Retained metadata draft");
  await capture(page, "rotation-retained-draft");
  await back.click();
  await expect(
    page.getByRole("heading", {
      name: "Discard unsaved changes?",
      exact: true,
    }),
  ).toBeVisible();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(back).toBeFocused();
  await expect(name).toHaveValue("Retained metadata draft");
  // Acknowledgment adopts only the concurrency precondition; it sends no write.
  const priorWrites = writes;
  await edit
    .getByRole("button", { name: "Use reviewed revision", exact: true })
    .focus();
  await page.keyboard.press("Enter");
  await expect(edit.getByText("Revision changed", { exact: true })).toHaveCount(
    0,
  );
  expect(writes).toBe(priorWrites);
  await expect(name).toHaveValue("Retained metadata draft");
  await name.fill(current.name);
  await back.click();
  await expect(page.getByRole("table")).toBeVisible();
  expect(nativeWarnings).toHaveLength(0);

  await open();
  await name.fill("Discard this draft");
  await edit
    .getByRole("button", { name: "Discard changes", exact: true })
    .click();
  await expect(name).toHaveValue(current.name);
  await page.getByLabel("Secret", { exact: true }).fill("SYNTHETIC_CANCELED");
  await page
    .getByRole("button", { name: "Review rotation", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(
    page.getByRole("button", { name: "Review rotation", exact: true }),
  ).toBeFocused();
  await expect(page.getByLabel("Secret", { exact: true })).toHaveValue("");
  await back.click();
  await expect(page.getByRole("table")).toBeVisible();
  expect(nativeWarnings).toHaveLength(0);
  expect(writes).toBe(priorWrites);

  await open();
  // An external revision is not a local acknowledgment, nor an unsaved edit.
  current = { ...current, revision: String(Number(current.revision) + 1) };
  await page.getByTestId("manual-refresh").click();
  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    3,
  );
  await capture(page, "external-revision");
  await back.click();
  await expect(page.getByRole("table")).toBeVisible();
  expect(nativeWarnings).toHaveLength(0);

  await open();
  // A confirmation already under review must not hand off against changed facts.
  await page
    .getByRole("button", { name: "Review deletion", exact: true })
    .click();
  current = { ...current, revision: String(Number(current.revision) + 1) };
  await page.evaluate(() =>
    document
      .querySelector<HTMLButtonElement>('[data-testid="manual-refresh"]')!
      .click(),
  );
  await expect(
    deletion.getByText("Revision changed", { exact: true }),
  ).toBeAttached();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Delete credential", exact: true })
    .click();
  await expect(
    deletion.getByText(
      "Current facts changed or are unavailable. Review them before confirming a new action.",
      { exact: true },
    ),
  ).toBeVisible();
  expect(writes).toBe(priorWrites);

  await open();
  mode = "reject";
  await name.fill("Draft survives rejection");
  await rotate();
  await expect(
    page.getByText("Synthetic rejection", { exact: true }),
  ).toBeVisible();
  await expect(name).toHaveValue("Draft survives rejection");
  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    0,
  );
  await capture(page, "rotation-rejected");
  await edit
    .getByRole("button", { name: "Discard changes", exact: true })
    .click();
  await back.click();
  await expect(page.getByRole("table")).toBeVisible();
  expect(nativeWarnings).toHaveLength(0);

  await open();
  mode = "pending";
  await name.fill("Draft survives uncertainty");
  await rotate();
  await expect.poll(() => release !== undefined).toBe(true);
  await expect(
    page.getByRole("button", { name: "Review rotation", exact: true }),
  ).toBeDisabled();
  await expect(name).toHaveValue("Draft survives uncertainty");
  await capture(page, "rotation-pending");
  mode = "uncertain";
  release!();
  await expect(
    page.getByText("Change outcome unknown", { exact: true }),
  ).toBeVisible();
  await page.getByTestId("manual-refresh").click();
  await expect(
    page.getByRole("button", { name: "Review rotation", exact: true }),
  ).toBeDisabled();
  await expect(name).toHaveValue("Draft survives uncertainty");
  await capture(page, "rotation-uncertain");
  expect(writes).toBe(priorWrites + 2);

  await edit
    .getByRole("button", { name: "Discard changes", exact: true })
    .click();
  await back.click();
  await expect(page.getByRole("table")).toBeVisible();
  // Confirmed success with an unavailable subsequent read does not rebase neighbors.
  await open();
  mode = "success";
  await page
    .getByLabel("Secret", { exact: true })
    .fill("SYNTHETIC_REFRESH_FAILURE");
  await page
    .getByRole("button", { name: "Review rotation", exact: true })
    .click();
  readFailure = true;
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Rotate secret", exact: true })
    .click();
  await expect(
    page.getByText("Git resource refresh unavailable", { exact: true }),
  ).toBeVisible();
  await expect(
    edit.getByRole("button", { name: "Review changes", exact: true }),
  ).toBeDisabled();
  readFailure = false;
  await page.getByTestId("manual-refresh").click();
  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    0,
  );
  await expect(
    edit.getByRole("button", { name: "Review changes", exact: true }),
  ).toBeEnabled();
  // Neighbor submission actually uses the acknowledged current revision.
  await name.fill("Saved metadata");
  await edit
    .getByRole("button", { name: "Review changes", exact: true })
    .click();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Edit credential", exact: true })
    .click();
  await expect(page.getByTestId("detail-context")).toContainText(
    "Saved metadata",
  );
  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    0,
  );
  await back.click();
  await expect(page.getByRole("table")).toContainText("Saved metadata");
  expect(nativeWarnings).toHaveLength(0);
  expect(writes).toBe(priorWrites + 4);

  await open();
  mode = "pending";
  release = undefined;
  await rotate();
  await expect.poll(() => release !== undefined).toBe(true);
  await page
    .getByRole("button", { name: "Review deletion", exact: true })
    .click();
  mode = "success";
  release!();
  await expect(
    deletion.getByText("Revision changed", { exact: true }),
  ).toBeAttached();
  await expect(page.getByRole("dialog")).toBeVisible();
  await page
    .getByRole("dialog")
    .getByRole("button", { name: "Delete credential", exact: true })
    .click();
  await expect(
    deletion.getByText(
      "Current facts changed or are unavailable. Review them before confirming a new action.",
      { exact: true },
    ),
  ).toBeVisible();
  expect(writes).toBe(priorWrites + 5);

  await open();
  externalAfterWrite = true;
  await rotate();
  await expect(page.getByText("Revision changed", { exact: true })).toHaveCount(
    3,
  );
  await expect(
    edit.getByRole("button", { name: "Review changes", exact: true }),
  ).toBeDisabled();
  expect(writes).toBe(priorWrites + 6);
  await back.click();
  await expect(page.getByRole("table")).toBeVisible();
  expect(nativeWarnings).toEqual([]);
});
