import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/frontend",
  globalSetup: "./tests/frontend/setup.ts",
  testMatch: "**/*.spec.ts",
  timeout: 300_000,
  globalTimeout: 1_200_000,
  expect: { timeout: 10_000 },
  // Each test owns its ephemeral static listener, API routes and browser context.
  fullyParallel: true,
  workers: 2,
  retries: 0,
  forbidOnly: true,
  outputDir: "../../.frontend-browser/test-results",
  reporter: [["list"], ["./tests/frontend/reporter.ts"]],
  use: {
    browserName: "chromium",
    headless: true,
    viewport: { width: 1280, height: 900 },
    serviceWorkers: "block",
    locale: "en-US",
    timezoneId: "UTC",
    reducedMotion: "reduce",
    actionTimeout: 10_000,
    navigationTimeout: 10_000,
    trace: "off",
    video: "off",
  },
});
