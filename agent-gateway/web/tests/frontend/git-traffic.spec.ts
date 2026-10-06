import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";
import type { GitTraffic } from "../../src/git-contract.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
const time = "2026-10-01T12:00:00.000000000Z";
const admission: GitTraffic["admission"] = {
  id,
  admitted_at: time,
  evaluated_at: time,
  principal: { id, revision: "1" },
  agent_credential: { id, revision: "1" },
  repository: { id, revision: "1" },
  alias_revision: "1",
  profile_revision: "1",
  authorization_revision: "1",
  operation: "push",
  commands: 1,
  allowed: true,
};
const complete: NonNullable<GitTraffic["completion"]> = {
  completed_at: time,
  outcome: "outcome_unknown",
  bytes_sent: 100,
  bytes_received: 200,
  duration_ms: 12,
  transfer_complete: true,
  status: 200,
};
const variants: Record<string, GitTraffic> = {
  "unknown-completion": { admission, completion: null },
  "reported-success": {
    admission,
    completion: { ...complete, reported_result: "reported_success" },
  },
  "reported-failure": {
    admission,
    completion: { ...complete, reported_result: "reported_failure" },
  },
  "reported-partial": {
    admission,
    completion: { ...complete, reported_result: "reported_partial" },
  },
  "incomplete-transfer": {
    admission,
    completion: { ...complete, transfer_complete: false },
  },
  "prestart-failure": {
    admission,
    completion: {
      completed_at: time,
      outcome: "prestart_failure",
      bytes_sent: 0,
      bytes_received: 0,
      duration_ms: 12,
      transfer_complete: false,
    },
  },
  blocked: { admission: { ...admission, allowed: false }, completion: null },
  read: {
    admission: { ...admission, operation: "read", commands: 0 },
    completion: { ...complete, outcome: "nonmutation" },
  },
  discovery: {
    admission: { ...admission, operation: "read_discovery", commands: 0 },
    completion: null,
  },
  probe: {
    admission: { ...admission, operation: "probe", commands: 0 },
    completion: null,
  },
  unsupported: {
    admission: {
      ...admission,
      operation: "invalid",
      commands: 0,
      allowed: false,
      rejection: "unsupported",
      alias_revision: "",
      repository: { id: "", revision: "" },
    },
    completion: null,
  },
  "policy-evidence": {
    admission: {
      ...admission,
      policy: {
        repository_name: "Synthetic policy repository",
        repository_url: "https://example.invalid/team/repo",
        grants: [{ id, revision: "1" }],
        grant_count: 1,
        creates: 0,
        updates: 1,
        deletes: 0,
      },
    },
    completion: null,
  },
};
const outcomes: Record<string, [string, string, string]> = {
  "unknown-completion": ["Push", "Unknown", "Unknown"],
  "reported-success": ["Push", "Complete", "Reported success"],
  "reported-failure": ["Push", "Complete", "Reported failure"],
  "reported-partial": ["Push", "Complete", "Reported partial success"],
  "incomplete-transfer": ["Push", "Incomplete", "Unknown"],
  "prestart-failure": ["Push", "Not started", "Unknown"],
  blocked: ["Push", "Not dispatched", "Unknown"],
  read: ["Read", "Complete", "Not a push"],
  discovery: ["Read discovery", "Unknown", "Not a push"],
  probe: ["Push probe", "Unknown", "Not a push"],
  unsupported: ["Unsupported exchange", "Not dispatched", "Not a push"],
  "policy-evidence": ["Push", "Unknown", "Unknown"],
};
test("git-traffic", async ({ page, frontend }) => {
  registerCapture(page, "git-traffic");
  let mode = "populated";
  let current = variants["unknown-completion"]!;
  let release: (() => void) | undefined;
  const queries: URLSearchParams[] = [];
  let revision = 1;
  let stale = false;
  let failOlder = false;
  await page.route("**/api/v2/git/traffic**", async (route) => {
    expect(route.request().method()).toBe("GET");
    if (mode === "loading")
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    if (mode === "error")
      return route.fulfill({ status: 503, json: { code: "unavailable" } });
    const url = new URL(route.request().url());
    const detail = url.pathname.endsWith(`/${id}`);
    if (!detail) queries.push(url.searchParams);
    if (mode === "history" && !detail) {
      const cursor = url.searchParams.get("cursor");
      if (failOlder && cursor)
        return route.fulfill({ status: 503, json: { code: "unavailable" } });
      if (stale && cursor)
        return route.fulfill({
          status: 409,
          contentType: "application/problem+json",
          json: { status: 409, code: "stale_cursor", title: "History changed" },
        });
      const item = structuredClone(variants["policy-evidence"]!);
      item.admission.id = cursor ? "01ARZ3NDEKTSV4RRFFQ69G5FA1" : id;
      if (revision === 2 && !cursor)
        item.completion = { ...complete, reported_result: "reported_success" };
      return route.fulfill({
        json: { items: [item], next_cursor: cursor ? null : "older" },
      });
    }
    return route.fulfill({
      json: detail
        ? current
        : { items: mode === "empty" ? [] : [current], next_cursor: null },
    });
  });
  await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
  await page.getByTestId("sign-in-submit").click();
  await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
    "data-session-lifecycle",
    "authenticated",
  );
  const nav = async (detail = false) => {
    await page.goto("about:blank");
    await page.goto(
      `${frontend.origin}/#/git/traffic${detail ? `/${id}` : ""}`,
    );
  };
  for (const state of ["populated", "empty", "error", "loading"]) {
    mode = state;
    await nav();
    if (state === "populated")
      await expect(page.getByRole("table")).toContainText(id);
    else if (state === "empty")
      await expect(page.getByText(/No Git traffic/)).toBeVisible();
    else if (state === "error")
      await expect(page.getByRole("alert")).toBeVisible();
    else await expect(page.getByText(/Loading git traffic/i)).toBeVisible();
    await capture(page, `collection-${state}`, state === "populated");
    if (state === "loading") {
      mode = "populated";
      release?.();
      await expect(page.getByRole("table")).toContainText(id);
    }
  }
  mode = "history";
  await nav();
  const live = page.getByRole("switch", { name: "Live mode", exact: true });
  await expect(live).toBeChecked();
  await expect(
    page.getByText("1 exchange loaded", { exact: true }),
  ).toBeVisible();
  await expect(page.getByRole("columnheader").getByRole("button")).toHaveCount(
    0,
  );
  await live.focus();
  await page.keyboard.press("Space");
  await expect(live).not.toBeChecked();
  const before = queries.length;
  revision = 2;
  frontend.invalidate("system_status", id);
  await page.waitForTimeout(200);
  expect(queries.length).toBe(before);
  await expect(page.getByRole("table")).not.toContainText("Reported success");
  await capture(page, "live-off", true);
  failOlder = true;
  await page
    .getByRole("button", { name: "Load older exchanges", exact: true })
    .click();
  await expect(
    page.getByText("Older exchanges unavailable", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText("1 exchange loaded", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Load older exchanges", exact: true }),
  ).toBeEnabled();
  await capture(page, "continuation-error", true);
  failOlder = false;
  await page
    .getByRole("button", { name: "Load older exchanges", exact: true })
    .click();
  await expect(
    page.getByText("2 exchanges loaded", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByRole("button", { name: "Return to newest", exact: true }),
  ).toBeVisible();
  await capture(page, "older-paused", true);
  await page
    .getByRole("table")
    .getByRole("link", { name: "Push", exact: true })
    .first()
    .click();
  await page
    .getByRole("link", { name: "Back to Git traffic", exact: true })
    .click();
  await expect(
    page.getByText("1 exchange loaded", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Showing the latest entries. Older results could not be continued.",
      { exact: true },
    ),
  ).toBeVisible();
  await capture(page, "detail-return", true);
  await page
    .getByRole("button", { name: "Return to newest", exact: true })
    .click();
  await expect(page.getByRole("table")).toContainText("Reported success");
  await expect(live).not.toBeChecked();
  await page
    .getByRole("table")
    .getByRole("link", { name: "Push", exact: true })
    .click();
  await page.goBack();
  await expect(
    page.getByText("1 exchange loaded", { exact: true }),
  ).toBeVisible();
  await expect(
    page.getByText(
      "Showing the latest entries. Older results could not be continued.",
      { exact: true },
    ),
  ).toHaveCount(0);
  await live.click();
  await page
    .getByRole("button", { name: "Load older exchanges", exact: true })
    .click();
  await expect(
    page.getByText("Live paused while viewing older results", { exact: true }),
  ).toBeVisible();
  await page.getByRole("button", { name: "Resume live", exact: true }).click();
  await expect(
    page.getByText("1 exchange loaded", { exact: true }),
  ).toBeVisible();
  await page
    .getByRole("searchbox", { name: "Recorded repository name or ID" })
    .fill("Recorded libray");
  await expect(page).toHaveURL(/filter_repository=Recorded%20libray/);
  await page
    .getByRole("combobox", { name: "Operation", exact: true })
    .selectOption("push");
  await expect.poll(() => queries.at(-1)?.get("operation")).toBe("push");
  expect(queries.at(-1)?.get("repository")).toBe("Recorded libray");
  expect(queries.at(-1)?.has("cursor")).toBe(false);
  await capture(page, "filtered", true);
  await page.getByRole("button", { name: "Reset", exact: true }).click();
  await expect(page).toHaveURL(/#\/git\/traffic$/);
  stale = true;
  await page
    .getByRole("button", { name: "Load older exchanges", exact: true })
    .click();
  await expect(
    page.getByText(/Older results could not be continued/),
  ).toBeVisible();
  await expect(page.getByRole("table").locator("tbody tr")).toHaveCount(1);
  await capture(page, "history-changed");
  stale = false;
  for (const [name, value] of Object.entries(variants)) {
    current = value;
    mode = "populated";
    await nav(true);
    await expect(
      page.getByRole("heading", {
        level: 1,
        name: /^(Push|Read|Unsupported exchange)/,
      }),
    ).toBeVisible();
    await expect(
      page.getByTestId("detail-context").locator("h1"),
    ).toBeFocused();
    await expect(
      page.getByTestId("detail-context").locator(".status-label"),
    ).toContainText("Transport:");
    await expect(page.locator("section.intro")).toHaveAccessibleName(
      "Git Traffic details",
    );
    await expect(page.locator("main")).toContainText(
      name === "unsupported"
        ? "Unsupported Git request"
        : value.admission.allowed
          ? "Allowed"
          : "Blocked",
    );
    const fact = (label: string) =>
      page
        .locator("dl > div")
        .filter({ has: page.getByText(label, { exact: true }) })
        .locator("dd");
    const [exchange, transport, report] = outcomes[name]!;
    await expect(fact("Exchange")).toHaveText(exchange);
    await expect(fact("Transport")).toHaveText(transport);
    await expect(fact("Upstream report")).toHaveText(report);
    await expect(page.locator("header.detail-context")).toContainText(
      transport,
    );
    await expect(fact("Command count")).toHaveText(
      String(value.admission.commands),
    );
    if (value.completion) {
      await expect(fact("HTTP status")).toHaveText(
        String(value.completion.status ?? "None"),
      );
      await expect(fact("Bytes sent / received")).toHaveText(
        `${value.completion.bytes_sent} / ${value.completion.bytes_received}`,
      );
    } else await expect(fact("HTTP status")).toHaveCount(0);
    await expect(page.locator("main")).toContainText(
      value.admission.operation === "push"
        ? "Upstream reports are not independently verified repository effects. Reconcile an uncertain push with the remote before deciding on another operation."
        : "Transport completion does not prove a valid local checkout. Discovery and probes are not completed pushes.",
    );
    if (name === "policy-evidence") {
      await page
        .getByText("Admission-time policy references", { exact: true })
        .click();
      await expect(fact("Canonical destination at admission")).toHaveText(
        "https://example.invalid/team/repo",
      );
      await expect(fact("Create / update / delete commands")).toHaveText(
        "0 / 1 / 0",
      );
      await expect(fact("Authorization revision")).toHaveText("1");
      await expect(fact("Repository")).toHaveText(`${id} · revision 1`);
    }
    await capture(page, `detail-${name}`, name === "policy-evidence");
  }
  mode = "error";
  await nav(true);
  await expect(page.getByRole("alert")).toBeVisible();
  await capture(page, "detail-unavailable");
});
