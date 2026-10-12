import { spawnSync } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { pathToFileURL } from "node:url";
import manifest from "../tests/frontend/documentation-images.json" with { type: "json" };
import inventory from "../tests/frontend/inventory.json" with { type: "json" };
import {
  prepareDocumentationImages,
  publishDocumentationImages,
  selectDocumentationImages,
  validateDocumentationManifest,
  validateDocumentationReferences,
} from "../tests/frontend/documentation-images.ts";

const root = resolve(import.meta.dirname, "../../..");
export async function generateDocumentationImages(
  names: string[],
  run = (receipt: string, owners: string[]) => {
    const result = spawnSync(
      process.execPath,
      [
        join(root, "node_modules/@playwright/test/cli.js"),
        "test",
        "--config",
        "agent-gateway/web/playwright.config.ts",
        "--workers=1",
        "--grep",
        `(?:^| )(${owners.join("|")})$`,
      ],
      {
        cwd: root,
        env: { ...process.env, FRONTEND_DOCUMENTATION_RECEIPT: receipt },
        stdio: "inherit",
        timeout: 1_260_000,
        killSignal: "SIGTERM",
      },
    );
    if (result.error || result.status !== 0)
      throw new Error("Documentation capture failed; prior images unchanged", {
        cause: result.error,
      });
  },
) {
  const images = selectDocumentationImages(
    validateDocumentationManifest(manifest, inventory),
    names,
  );
  const artifacts = join(root, ".frontend-browser");
  await mkdir(artifacts, { recursive: true });
  const lock = join(artifacts, "documentation-export.lock");
  await mkdir(lock).catch((error) => {
    throw new Error(
      `Documentation export already owned or lock unavailable: ${lock}`,
      { cause: error },
    );
  });
  try {
    const invocation = await mkdtemp(join(artifacts, "documentation-export-"));
    const receipt = join(invocation, "receipt.json");
    const owners = [...new Set(images.map((image) => image.scenario))];
    await run(receipt, owners);
    const current = JSON.parse(await readFile(receipt, "utf8"));
    if (!/^[a-f0-9-]{36}$/.test(current.run) || current.status !== "passed")
      throw new Error(
        "Missing successful current-run receipt; prior images unchanged",
      );
    const runRoot = join(artifacts, "runs", current.run);
    const report = JSON.parse(
      await readFile(join(runRoot, "inventory.json"), "utf8"),
    );
    const prepared = await prepareDocumentationImages(
      images,
      report,
      current.run,
      runRoot,
    );
    await publishDocumentationImages(
      join(root, "agent-gateway/docs/images"),
      prepared,
    );
    console.log(
      `Exported ${images.map((image) => image.file).join(", ")} from ${runRoot}. Inspect every PNG and rendered Markdown; this is not visual approval.`,
    );
  } finally {
    await rm(lock, { recursive: true });
  }
}
if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(resolve(process.argv[1])).href
) {
  try {
    const names = process.argv.slice(2);
    if (names.length === 1 && names[0] === "--check") {
      await validateDocumentationReferences(
        validateDocumentationManifest(manifest, inventory),
        join(root, "agent-gateway"),
      );
      console.log(
        "Documentation image mappings, PNG bounds and references passed (not visual approval)",
      );
    } else await generateDocumentationImages(names);
  } catch (error) {
    console.error(
      error instanceof Error
        ? error.message
        : "Documentation image export failed",
    );
    process.exitCode = 1;
  }
}
