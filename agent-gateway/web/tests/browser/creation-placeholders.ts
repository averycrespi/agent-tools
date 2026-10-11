import { expect, type Page } from "@playwright/test";

// This is an explicit rendered-field inventory, not a label-derived fallback.
// Shared sites and added rows are checked on every exercised creation branch.
const copy: Record<string, string> = {
  "http-name-create": "Example API credential",
  "http-host-create": "api.example.com",
  "http-port-create": "443",
  "http-header-create": "Authorization",
  "http-prefix-create": "Bearer ",
  "http-credential-secret-create": "Enter secret value",
  "http-grant-description": "Example API policy",
  "http-grant-host": "api.example.com",
  "http-grant-port": "443",
  "http-grant-path": "/v1/resources",
  "git-url-create": "https://git.example.com/team/project",
  "git-origin-create": "https://git.example.com",
  "git-header-create": "Authorization",
  "git-prefix-create": "Bearer ",
  "git-secret-create": "Enter secret value",
  "git-description-create": "Project repository access policy",
  "server-namespace": "example_tools",
  "server-display-name": "Example Tools",
  "server-executable": "/usr/local/bin/example-mcp-server",
  "server-working-directory": "/opt/example-mcp-server",
  "server-url": "https://mcp.example.invalid/mcp",
  "server-client-id": "example-gateway-client",
  "server-issuer": "https://auth.example.invalid",
  "server-auth-metadata-url":
    "https://auth.example.invalid/.well-known/oauth-authorization-server",
  "server-callback-uri": "http://localhost:3118/callback",
  "grant-description": "Example tool access policy",
  "grant-upstream": "lookup",
  "approval-description": "Allow requested tool access",
  "principal-display-name": "Research assistant",
};
const rowCopy: Record<string, string> = {
  "server-argument": "--verbose",
  "server-initial-scopes": "read:tools",
  "server-oauth-origin": "https://auth.example.invalid",
  // Approved retained placeholders; these pair rows are not secret inputs.
  "server-environment-name": "Variable name",
  "server-environment-value": "Value",
  "server-secret-environment-name": "Environment variable",
  "server-secret-environment-value": "Credential slot",
  "server-header-name": "Header name",
  "server-header-value": "Header value",
};

export async function exerciseMatcherPlaceholders(page: Page, prefix: string) {
  const operator = page.getByTestId(`${prefix}-operator`).last();
  const type = page.getByTestId(`${prefix}-type`).last();
  const value = page.getByTestId(`${prefix}-value`).last();
  await expect(value).toHaveValue("");
  for (const [scalar, example] of [
    ["string", "example-project"],
    ["number", "42"],
  ]) {
    await type.selectOption(scalar!);
    await expect(value).toHaveAttribute("placeholder", example!);
    await assertCreationPlaceholders(page);
    await value.fill(example!);
    await assertCreationPlaceholders(page);
    await value.fill("");
  }
  await operator.selectOption("regex");
  await expect(value).toHaveAttribute("placeholder", "example-[a-z0-9-]+");
  await value.fill("example-[a-z0-9-]+");
  await assertCreationPlaceholders(page);
  await operator.selectOption("equals");
  await type.selectOption("boolean");
  await assertCreationPlaceholders(page);
  await type.selectOption("null");
  await assertCreationPlaceholders(page);
  await type.selectOption("string");
  await expect(value).toHaveValue("");
}

export async function assertCreationPlaceholders(page: Page) {
  const path = new URL(page.url()).hash.split("?")[0]!;
  const creation =
    /^#\/(?:agents|http\/(?:credentials|grants)|git\/(?:repositories|credentials|grants)|mcp\/(?:servers|grants)|system\/(?:admin-credentials|backups))\/new$/.test(
      path,
    );
  const scope = creation
    ? page.locator("main")
    : page.locator(
        '[data-testid="request-actions"], [data-testid="credential-replacement-form"]',
      );
  for (const control of await scope
    .locator("input:visible, textarea:visible")
    .all()) {
    const id = (await control.getAttribute("id")) ?? "";
    const type = (await control.getAttribute("type")) ?? "text";
    const testID = (await control.getAttribute("data-testid")) ?? "";
    if (id === "rejection-reason") continue; // Rejection creates no resource.
    // Native unsupported controls and immutable/generated evidence are explicit exclusions.
    if (
      ["checkbox", "radio", "datetime-local", "date", "time"].includes(type) ||
      (await control.getAttribute("readonly")) !== null
    )
      continue;
    if (
      (id === "approval-target" && (await control.isDisabled())) ||
      (id.match(/-value-\d+$/) &&
        (await control.isDisabled()) &&
        (await control.inputValue()) === "null")
    )
      continue;
    let expected: string | undefined = copy[id] ?? rowCopy[testID];
    if (id === "git-name-create")
      expected = path.includes("repositories")
        ? "Project repository"
        : "Project Git credential";
    if (/^git-alias-\d+$/.test(id))
      expected = "Same destination with .git added or removed";
    if (/^git-rule-ref-\d+$/.test(id))
      expected =
        (await page
          .locator(`#${id.replace("-ref-", "-kind-")}`)
          .inputValue()) === "exact"
          ? "refs/heads/main"
          : "refs/heads/";
    if (/^Method \d+$/.test((await control.getAttribute("aria-label")) ?? ""))
      expected = "GET";
    if (/-pointer-\d+$/.test(id)) expected = "/repository";
    if (/-value-\d+$/.test(id)) {
      const operator = await page
        .locator(`#${id.replace("-value-", "-operator-")}`)
        .inputValue();
      const scalar = await page
        .locator(`#${id.replace("-value-", "-type-")}`)
        .inputValue();
      expected =
        operator === "regex"
          ? "example-[a-z0-9-]+"
          : scalar === "number"
            ? "42"
            : "example-project";
    }
    if (id.startsWith("credential-slot-")) {
      const label = await control.evaluate(
        (node: HTMLInputElement) => node.labels?.[0]?.textContent ?? "",
      );
      expected = label.includes("OAuth client secret")
        ? "Enter OAuth client secret"
        : label.includes("Bearer token")
          ? "Enter bearer token"
          : "Enter secret value";
    }
    // Approval target and bounded duration are asserted with fixture-owned values below.
    if (id === "approval-target") {
      expect(await control.getAttribute("placeholder")).toMatch(
        /^[^.]+\.lookup$/,
      );
    } else if (id === "approval-duration") {
      const unit = await page
        .getByTestId("approval-duration-unit")
        .inputValue();
      const placeholder = await control.getAttribute("placeholder");
      if (!placeholder)
        await expect(page.locator("#approval-duration-hint")).toContainText(
          "Choose a smaller unit",
        );
      else expect(placeholder).toBe(unit === "seconds" ? "60" : "1");
    } else {
      expect(
        expected,
        `Unclassified creation control: ${path} ${id || testID || type}`,
      ).toBeDefined();
      await expect(control).toHaveAttribute("placeholder", expected!);
    }
    await expect(control).toHaveAccessibleName(/\S/);
    const guidance = await control.evaluate((node: HTMLInputElement) => ({
      label:
        node.getAttribute("aria-label") ||
        [...(node.labels ?? [])].map((label) => label.textContent).join(" "),
      help: (node.getAttribute("aria-describedby") ?? "")
        .split(/\s+/)
        .filter(Boolean)
        .map((id) => ({ id, text: document.getElementById(id)?.textContent })),
    }));
    expect(guidance.label, `Persistent label for ${id}`).toMatch(/\S/);
    for (const help of guidance.help) {
      expect(help.text, `Persistent help ${help.id}`).toMatch(/\S/);
      await expect(page.locator(`[id="${help.id}"]`)).toBeVisible();
    }
    if (/^(http|git)-prefix-create$/.test(id))
      await expect(control).toHaveAccessibleDescription(
        "Include any space needed between the prefix and secret. Leave blank for no prefix.",
      );
  }
}
