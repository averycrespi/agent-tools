import { chromium } from "@playwright/test";

const chunks: Buffer[] = [];
let length = 0;
for await (const chunk of process.stdin) {
  const value = Buffer.from(chunk);
  length += value.length;
  if (length > 16 * 1024) process.exit(2);
  chunks.push(value);
}

let input: unknown;
try {
  input = JSON.parse(Buffer.concat(chunks).toString("utf8"));
} catch {
  process.exit(2);
}

if (
  typeof input !== "object" ||
  input === null ||
  Array.isArray(input) ||
  Object.keys(input).sort().join(",") !==
    "admin_bearer,base_url,scenario,version" ||
  !("version" in input) ||
  input.version !== 1 ||
  !("scenario" in input) ||
  input.scenario !== "shell-load" ||
  !("base_url" in input) ||
  typeof input.base_url !== "string" ||
  !/^http:\/\/127\.0\.0\.1:[1-9][0-9]{0,4}$/.test(input.base_url) ||
  !("admin_bearer" in input) ||
  typeof input.admin_bearer !== "string" ||
  input.admin_bearer.length === 0
) {
  process.exit(2);
}

const baseURL = input.base_url;
input = undefined;
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({
  baseURL,
  serviceWorkers: "block",
});
const page = await context.newPage();
const externalRequests: string[] = [];
page.on("request", (request) => {
  if (!request.url().startsWith(baseURL)) externalRequests.push(request.url());
});
await page.goto("/", { waitUntil: "networkidle" });
await page.locator('[data-testid="gateway-shell"]').waitFor();
if ((await page.title()) !== "MCP Gateway Control Plane") process.exit(3);
if (externalRequests.length !== 0) process.exit(3);

process.stdout.write('{"event":"shell_loaded"}\n');
process.on("SIGTERM", () => {});
setInterval(() => {}, 60 * 60 * 1000);
