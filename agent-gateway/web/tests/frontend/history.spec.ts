import { test, syntheticBearer } from "./fixture.ts";
import { registerCapture } from "./capture.ts";
import { runAudit } from "../browser/audit-scenarios.ts";
import { runHTTPTraffic } from "../browser/http-traffic-scenarios.ts";

test("http-traffic", async ({ context, page, frontend }) => {
  registerCapture(page, "http-traffic");
  await runHTTPTraffic(
    context,
    page,
    frontend.origin,
    syntheticBearer,
    frontend.requests,
    "presentation",
  );
});
test.describe("audit presentation", () => {
  test.use({ timezoneId: "America/New_York" });
  test("audit", async ({ browser, context, page, frontend }) => {
    registerCapture(page, "audit");
    await runAudit(
      browser.version(),
      context,
      page,
      frontend.origin,
      syntheticBearer,
      frontend.requests,
      "presentation",
    );
  });
});
