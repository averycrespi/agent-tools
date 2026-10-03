import { expect } from "@playwright/test";
import { test, syntheticBearer } from "./fixture.ts";
import { capture, registerCapture } from "./capture.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FA0";
const ref = (suffix = "0") => ({ id: id.slice(0, -1) + suffix, revision: 1 });
const cases: Record<
  string,
  {
    connect?: boolean;
    allowed?: boolean;
    transport?: string;
    reason: string;
    [key: string]: unknown;
  }
> = {
  "default-block": { reason: "principal_default" },
  "default-allow": { reason: "principal_default", allowed: true },
  "forbidden-address": { reason: "address_forbidden" },
  "private-permission": { reason: "private_permission_required" },
  "destination-block": { reason: "destination_block", grant: ref("1") },
  "request-block": { reason: "request_block", grant: ref("1") },
  "request-allow": {
    reason: "request_allow",
    allowed: true,
    grant: ref("1"),
    private_grant: ref("2"),
  },
  "tunnel-allow": {
    reason: "tunnel_allow",
    connect: true,
    transport: "tunnel",
    allowed: true,
    grant: ref("1"),
    private_grant: ref("2"),
  },
  intercept: {
    reason: "intercept_required",
    connect: true,
    transport: "intercept",
  },
  "credential-unavailable": {
    reason: "credential_unavailable",
    grant: ref("1"),
    credential: ref("2"),
    credential_grant: ref("1"),
  },
  "credential-conflict": {
    reason: "credential_conflict",
    grant: ref("1"),
    credential: ref("2"),
    credential_grant: ref("1"),
    conflict_credential: ref("3"),
    conflict_grant: ref("4"),
  },
};
const headings: Record<string, string> = {
  "default-block": "Agent default: Block requests",
  "default-allow": "Agent default: Allow requests",
  "forbidden-address": "Blocked: destination address is forbidden",
  "private-permission": "Blocked: a matching local/private allow is required",
  "destination-block": "Blocked by a destination grant",
  "request-block": "Blocked by a request grant",
  "request-allow": "Allowed by request policy",
  "tunnel-allow":
    "Opaque tunnel allowed; request policy and injection bypassed",
  intercept:
    "Interception required; each decrypted request needs its own evaluation",
  "credential-unavailable":
    "Blocked: selected credential has no available material binding",
  "credential-conflict":
    "Blocked: matching grants require different credentials",
};
test("http-preview", async ({ page, frontend }) => {
  expect(frontend.origin).toMatch(/^http:\/\/127\.0\.0\.1:/);
  registerCapture(page, "http-preview");
  let selection = "default-block";
  let pending = false;
  let release: (() => void) | undefined;
  let calls = 0;
  let expectedMethod = "GET";
  await page.route("**/api/v2/principals?*", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id,
            display_name: "Synthetic preview agent",
            state: "active",
            visibility: "all",
            http_default: "block",
            revision: "1",
            credential_revision: "0",
            credential: null,
            created_at: "2026-10-01T12:00:00Z",
            updated_at: "2026-10-01T12:00:00Z",
          },
        ],
        next_cursor: null,
        total_count: 1,
        offset: 0,
      },
    }),
  );
  await page.route("**/api/v2/http/access-preview", async (route) => {
    calls++;
    expect(route.request().method()).toBe("POST");
    expect(route.request().headers()["x-csrf-token"]).toBe("A".repeat(43));
    expect(route.request().postDataJSON()).toEqual(
      cases[selection]?.connect
        ? { principal_id: id, connect: { host: "example.invalid", port: 443 } }
        : {
            principal_id: id,
            method: expectedMethod,
            url: "https://example.invalid/path",
          },
    );
    if (pending)
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    if (selection === "error")
      return route.fulfill({ status: 503, json: { code: "unavailable" } });
    const { connect, ...decision } = cases[selection]!;
    expect(Object.keys(route.request().postDataJSON()).sort()).toEqual(
      connect ? ["connect", "principal_id"] : ["method", "principal_id", "url"],
    );
    return route.fulfill({
      json: {
        decision: {
          version: 1,
          principal: ref(),
          policy_revision: 5,
          default_revision: 2,
          allowed: false,
          transport: "request",
          ...decision,
        },
        default:
          selection === "default-allow" ||
          selection === "forbidden-address" ||
          selection === "private-permission"
            ? "allow"
            : "block",
        policy_only: true,
        network_verified: false,
        tls_verified: false,
        material_verified: false,
        admission_authority: false,
      },
    });
  });
  await page.getByTestId("admin-bearer-input").fill(syntheticBearer);
  await page.getByTestId("sign-in-submit").click();
  await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
    "data-session-lifecycle",
    "authenticated",
  );
  await page.evaluate(() => {
    location.hash = "#/http/grants/test-access";
  });
  const submit = page.getByRole("button", { name: "Test access", exact: true });
  await expect(submit).toBeVisible();
  await capture(page, "blank");
  await submit.click();
  expect(calls).toBe(0);
  expect(await page.locator(":invalid").count()).toBeGreaterThan(0);
  await capture(page, "validation");
  await page.getByLabel("Agent", { exact: true }).selectOption(id);
  for (const [name, value] of Object.entries(cases)) {
    selection = name;
    await page
      .getByLabel("Access type", { exact: true })
      .selectOption(value.connect ? "connect" : "request");
    if (value.connect)
      await page.getByLabel("Host", { exact: true }).fill("example.invalid");
    else
      await page
        .getByLabel("URL", { exact: true })
        .fill("https://example.invalid/path");
    await submit.click();
    await expect(page.getByRole("heading", { level: 3 })).toHaveText(
      headings[name]!,
    );
    const result = page.getByRole("heading", { level: 3 }).locator("..");
    const fact = (label: string) =>
      result
        .locator("dl > div")
        .filter({ has: page.getByText(label, { exact: true }) })
        .locator("dd");
    await expect(fact("Policy revision")).toHaveText("5");
    await expect(fact("Default revision")).toHaveText("2");
    await expect(fact("Transport")).toHaveText(value.transport ?? "request");
    await expect(fact("Local/private permission")).toHaveText(
      value.private_grant
        ? "Matching permission present"
        : "Explicit matching allow required",
    );
    await expect(fact("Agent").getByRole("link")).toHaveAttribute(
      "href",
      `#/agents/${id}`,
    );
    for (const [key, label] of Object.entries({
      grant: "Deciding grant",
      private_grant: "Local/private grant",
      credential: "Credential",
      credential_grant: "Credential grant",
      conflict_credential: "Conflicting credential",
      conflict_grant: "Conflicting grant",
    })) {
      const reference = value[key] as ReturnType<typeof ref> | undefined;
      if (reference) {
        await expect(fact(label)).toContainText(
          `${reference.id} · revision ${reference.revision}`,
        );
        await expect(fact(label).getByRole("link")).toHaveAttribute(
          "href",
          `#/http/${key.includes("credential") && !key.includes("grant") ? "credentials" : "grants"}/${reference.id}`,
        );
      } else await expect(fact(label)).toHaveCount(0);
    }
    await expect(page.locator("main")).toContainText(
      "Policy only. No request is sent. Network, TLS and secret material are unverified; this result is not future admission authority.",
    );
    await capture(page, name, name === "credential-conflict");
  }
  await page.getByLabel("Method", { exact: true }).selectOption("custom");
  await page.getByLabel("Custom method", { exact: true }).fill("PROPFIND");
  expectedMethod = "PROPFIND";
  await capture(page, "custom-method");
  pending = true;
  await submit.click();
  await expect(submit).toBeDisabled();
  await capture(page, "pending");
  selection = "error";
  pending = false;
  release?.();
  await expect(page.getByRole("alert")).toBeVisible();
  await capture(page, "error");
});
