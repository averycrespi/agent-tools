import { execFileSync } from "node:child_process";
import {
  chmodSync,
  mkdtempSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
} from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const repositoryRoot = resolve(
  fileURLToPath(new URL("../../..", import.meta.url)),
);
const tracked = join(repositoryRoot, "mcp-gateway/internal/api/static");
const temporaryRoot = mkdtempSync(join(tmpdir(), "mcp-gateway-ui-"));

function snapshot(directory) {
  const result = new Map();
  for (const name of readdirSync(directory).sort()) {
    const path = join(directory, name);
    const info = statSync(path);
    if (!info.isFile())
      throw new Error(`generated asset is not a regular file: ${name}`);
    result.set(name, { contents: readFileSync(path), mode: info.mode & 0o777 });
  }
  return result;
}

function assertEqual(left, right, label) {
  const leftNames = [...left.keys()];
  const rightNames = [...right.keys()];
  if (JSON.stringify(leftNames) !== JSON.stringify(rightNames)) {
    throw new Error(
      `${label} inventory differs: ${leftNames} != ${rightNames}`,
    );
  }
  for (const name of leftNames) {
    const leftFile = left.get(name);
    const rightFile = right.get(name);
    if (
      !leftFile.contents.equals(rightFile.contents) ||
      leftFile.mode !== rightFile.mode
    ) {
      throw new Error(`${label} differs for ${name}`);
    }
  }
}

try {
  const trackedBefore = snapshot(tracked);
  const expected = ["app.css", "app.js", "favicon.svg", "index.html"];
  if (JSON.stringify([...trackedBefore.keys()]) !== JSON.stringify(expected)) {
    throw new Error(
      `tracked static allowlist differs: ${[...trackedBefore.keys()]}`,
    );
  }
  const builds = [];
  for (const name of ["first", "second"]) {
    const outDir = join(temporaryRoot, name);
    execFileSync(
      "npm",
      [
        "exec",
        "vite",
        "--",
        "build",
        "--config",
        "mcp-gateway/web/vite.config.ts",
        "--outDir",
        outDir,
        "--emptyOutDir",
      ],
      {
        cwd: repositoryRoot,
        stdio: "pipe",
        env: { ...process.env, NO_COLOR: "1" },
      },
    );
    for (const generated of readdirSync(outDir))
      chmodSync(join(outDir, generated), 0o644);
    builds.push(snapshot(outDir));
  }
  assertEqual(builds[0], builds[1], "normalized builds");
  assertEqual(builds[0], trackedBefore, "tracked generated assets");
  assertEqual(snapshot(tracked), trackedBefore, "nonmutating verification");
  for (const [name, file] of trackedBefore) {
    if (name.endsWith(".map") || name.startsWith("forbidden-"))
      throw new Error(`forbidden generated asset: ${name}`);
    if (file.contents.includes(Buffer.from("sourceMappingURL=")))
      throw new Error(`source map reference in ${name}`);
  }
} finally {
  rmSync(temporaryRoot, { recursive: true, force: true });
}
