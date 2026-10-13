import { chromium } from "@playwright/test";
import { X509Certificate, createHash } from "node:crypto";
import { readFileSync } from "node:fs";
import assert from "node:assert/strict";

let input = "";
for await (const chunk of process.stdin) input += chunk;
const { origin, admin } = JSON.parse(input);
// Trust exactly the disposable TLS peer key, not arbitrary invalid certificates.
const certificate = new X509Certificate(
  readFileSync(process.env.NODE_EXTRA_CA_CERTS),
);
const pin = createHash("sha256")
  .update(certificate.publicKey.export({ type: "spki", format: "der" }))
  .digest("base64");
const browser = await chromium.launch({
  headless: true,
  args: [`--ignore-certificate-errors-spki-list=${pin}`],
});
try {
  const context = await browser.newContext();
  const page = await context.newPage();
  page.setDefaultTimeout(15000);
  await page.goto(origin);
  await page.getByTestId("admin-bearer-input").fill(admin);
  await page.getByTestId("sign-in-submit").click();
  await page.waitForURL("**/#/overview");
  const cookies = await context.cookies();
  const session = cookies.find(
    (cookie) => cookie.name === "agent_gateway_session",
  );
  assert.ok(
    session?.secure && session.httpOnly && session.sameSite === "Strict",
  );
  const evidence = await page.evaluate(async () => {
    const bootstrap = await fetch("/api/v2/admin-sessions/current", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const { csrf_token: csrf } = await bootstrap.json();
    const missing = await fetch("/api/v2/admin-sessions/current", {
      method: "DELETE",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    const wrong = await fetch("/api/v2/admin-sessions/current", {
      method: "DELETE",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": "wrong" },
      body: "{}",
    });
    const logout = await fetch("/api/v2/admin-sessions/current", {
      method: "DELETE",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf },
      body: "{}",
    });
    const after = await fetch("/api/v2/admin-sessions/current", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "{}",
    });
    return [
      bootstrap.status,
      missing.status,
      wrong.status,
      logout.status,
      after.status,
    ];
  });
  assert.deepEqual(evidence, [200, 403, 403, 204, 401]);
  await page.reload();
  await page.getByTestId("admin-bearer-input").waitFor();
  assert.equal(await page.getByTestId("admin-bearer-input").inputValue(), "");
  await context.close();
  console.log(
    JSON.stringify({
      chromium: browser.version(),
      tls: "fixture-SPKI-only",
      checks: "sign-in,secure-cookie,bootstrap,csrf,logout",
    }),
  );
} catch {
  // Playwright exceptions can contain DOM/requests. Never retain them as CI artifacts.
  console.error(
    "Container browser qualification failed; private details withheld",
  );
  process.exitCode = 1;
} finally {
  await browser.close();
}
