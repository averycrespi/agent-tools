import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(
  fileURLToPath(new URL("../../..", import.meta.url)),
);
const packageJSON = JSON.parse(
  readFileSync(join(repositoryRoot, "package.json"), "utf8"),
);
const lock = JSON.parse(
  readFileSync(join(repositoryRoot, "package-lock.json"), "utf8"),
);
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

const staticRoot = join(repositoryRoot, "mcp-gateway/internal/api/static");
const html = readFileSync(join(staticRoot, "index.html"), "utf8");
const script = readFileSync(join(staticRoot, "app.js"), "utf8");
const style = readFileSync(join(staticRoot, "app.css"), "utf8");
if (
  (html.match(/<script\b/g) ?? []).length !== 1 ||
  !html.includes(
    '<script type="module" crossorigin src="/assets/app.js"></script>',
  ) ||
  (html.match(/<link\b/g) ?? []).length !== 1 ||
  !html.includes('rel="stylesheet" crossorigin href="/assets/app.css"') ||
  /<(?:script|style)[^>]*>\s*[^<]/i.test(html) ||
  /(?:https?:|data:|blob:|\/\/)/i.test(html)
)
  throw new Error("static shell active-content allowlist changed");
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
  ])
    if (contents.includes(forbidden))
      throw new Error(
        `${name} contains forbidden active content: ${forbidden}`,
      );
}
