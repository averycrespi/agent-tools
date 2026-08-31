import { execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(
  fileURLToPath(new URL("../../..", import.meta.url)),
);
const packageJSON = JSON.parse(
  readFileSync(join(repositoryRoot, "package.json"), "utf8"),
);
const lockContents = readFileSync(join(repositoryRoot, "package-lock.json"));
const lock = JSON.parse(lockContents.toString("utf8"));
const lockDigest = createHash("sha256").update(lockContents).digest("hex");
if (
  lockDigest !==
  "3179b01fceb75d1df80217849de915f024e98ee0ab1b821639fbf04b33c5a8e0"
)
  throw new Error(`frontend lockfile digest changed: ${lockDigest}`);
const allowedLicenses = new Set([
  "Apache-2.0",
  "BSD-3-Clause",
  "ISC",
  "MIT",
  "MPL-2.0",
]);

if (lock.lockfileVersion !== 3 || lock.requires !== true)
  throw new Error("frontend lockfile contract changed");
const root = lock.packages?.[""];
if (
  root?.name !== packageJSON.name ||
  JSON.stringify(root.devDependencies) !==
    JSON.stringify(packageJSON.devDependencies)
)
  throw new Error("package manifest and lock root differ");
for (const [path, value] of Object.entries(lock.packages)) {
  if (path === "") continue;
  if (
    typeof value.version !== "string" ||
    typeof value.resolved !== "string" ||
    !value.resolved.startsWith("https://registry.npmjs.org/") ||
    typeof value.integrity !== "string" ||
    !value.integrity.startsWith("sha512-")
  )
    throw new Error(`dependency provenance is incomplete: ${path}`);
  if (!allowedLicenses.has(value.license))
    throw new Error(`dependency license is not approved: ${path}`);
}
if (
  packageJSON.scripts?.["ui:audit"] !== "npm audit --audit-level=high" ||
  packageJSON.scripts?.["ui:verify-generated"] !==
    "node mcp-gateway/web/scripts/verify-generated.mjs"
)
  throw new Error("frontend qualification command changed");

execFileSync(
  process.execPath,
  [join(dirname(fileURLToPath(import.meta.url)), "verify-generated.mjs")],
  {
    cwd: repositoryRoot,
    stdio: "inherit",
    env: { ...process.env, NO_COLOR: "1" },
  },
);

const webRoot = join(repositoryRoot, "mcp-gateway/web");
const productionSources = new Map([
  ["index.html", readFileSync(join(webRoot, "index.html"), "utf8")],
  [
    "public/favicon.svg",
    readFileSync(join(webRoot, "public/favicon.svg"), "utf8"),
  ],
  ["vite.config.ts", readFileSync(join(webRoot, "vite.config.ts"), "utf8")],
]);
const sourceRoot = join(webRoot, "src");
for (const entry of readdirSync(sourceRoot, { recursive: true })) {
  const path = join(sourceRoot, entry);
  try {
    productionSources.set(`src/${entry}`, readFileSync(path, "utf8"));
  } catch (error) {
    if (error?.code !== "EISDIR") throw error;
  }
}
if (
  !productionSources
    .get("vite.config.ts")
    .includes('input: resolve(webRoot, "index.html")')
)
  throw new Error("production build input changed");
for (const [path, contents] of productionSources) {
  for (const forbidden of [
    "dev-server",
    "dev-proxy",
    "development-contract",
    "@vite/client",
    "vite/client",
    "mcp-gateway-ui-development",
  ])
    if (contents.includes(forbidden))
      throw new Error(
        `production frontend ${path} contains development reference: ${forbidden}`,
      );
}

const staticRoot = join(repositoryRoot, "mcp-gateway/internal/api/static");
const html = readFileSync(join(staticRoot, "index.html"), "utf8");
const script = readFileSync(join(staticRoot, "app.js"), "utf8");
const style = readFileSync(join(staticRoot, "app.css"), "utf8");
const favicon = readFileSync(join(staticRoot, "favicon.svg"), "utf8");
if (
  (html.match(/<script\b/g) ?? []).length !== 1 ||
  !html.includes(
    '<script type="module" crossorigin src="/assets/app.js"></script>',
  ) ||
  (html.match(/<link\b/g) ?? []).length !== 2 ||
  !html.includes(
    '<link rel="icon" type="image/svg+xml" href="/assets/favicon.svg" />',
  ) ||
  !html.includes('rel="stylesheet" crossorigin href="/assets/app.css"') ||
  /<(?:script|style)[^>]*>\s*[^<]/i.test(html) ||
  /(?:https?:|data:|blob:|\/\/)/i.test(html)
)
  throw new Error("static shell active-content allowlist changed");
if (
  !favicon.includes('<svg xmlns="http://www.w3.org/2000/svg"') ||
  /<(?:script|foreignObject)\b|\bon\w+=|\bhref=|url\(/i.test(favicon)
)
  throw new Error("favicon active-content boundary changed");
for (const [name, contents] of [
  ["app.js", script],
  ["app.css", style],
]) {
  for (const forbidden of [
    "sourceMappingURL=",
    "navigator.serviceWorker",
    "serviceWorker.register",
    "new Function(",
    "eval(",
    "@vite/client",
    "vite/client",
    "mcp-gateway-ui-development",
  ])
    if (contents.includes(forbidden))
      throw new Error(
        `${name} contains forbidden active content: ${forbidden}`,
      );
}
