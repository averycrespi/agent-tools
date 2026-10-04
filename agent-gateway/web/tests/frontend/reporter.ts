import type {
  FullConfig,
  FullResult,
  Reporter,
  Suite,
} from "@playwright/test/reporter";
import { mkdir, cp, readdir, readFile, writeFile } from "node:fs/promises";
import { join, relative } from "node:path";
import { artifactBase, artifactRoot as getArtifactRoot } from "./capture.ts";
import manifest from "./inventory.json" with { type: "json" };
import {
  qualifyInventory,
  captureImages,
  type CaptureRecord,
} from "./inventory.ts";

const escape = (text: string) =>
  text
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll('"', "&quot;");
async function walk(path: string): Promise<string[]> {
  const entries = await readdir(path, { withFileTypes: true }).catch(
    (error: NodeJS.ErrnoException) => {
      if (error.code === "ENOENT") return [];
      throw error;
    },
  );
  return (
    await Promise.all(
      entries.map((entry) =>
        entry.isDirectory()
          ? walk(join(path, entry.name))
          : Promise.resolve([join(path, entry.name)]),
      ),
    )
  ).flat();
}
export default class GalleryReporter implements Reporter {
  private selected = new Set<string>();
  onBegin(_config: FullConfig, suite: Suite) {
    this.selected = new Set(suite.allTests().map((test) => test.title));
  }
  async onEnd(result: FullResult): Promise<{ status: FullResult["status"] }> {
    const artifactRoot = getArtifactRoot();
    await mkdir(artifactRoot, { recursive: true });
    await cp(
      join(artifactBase, "test-results"),
      join(artifactRoot, "test-results"),
      { recursive: true },
    ).catch((error: NodeJS.ErrnoException) => {
      if (error.code !== "ENOENT") throw error;
    });
    const records: CaptureRecord[] = [];
    const invalid: string[] = [];
    for (const file of await walk(join(artifactRoot, "captures"))) {
      if (!file.endsWith(".json")) continue;
      const item = JSON.parse(await readFile(file, "utf8"));
      const image = relative(artifactRoot, file).replace(/\.json$/, ".png");
      const record: CaptureRecord = { ...item, image };
      let images: string[];
      try {
        images = captureImages(record);
      } catch {
        invalid.push(image);
        continue;
      }
      if (item.run !== process.env.FRONTEND_BROWSER_RUN) {
        invalid.push(image);
        continue;
      }
      for (const candidate of images) {
        const png = await readFile(join(artifactRoot, candidate)).catch(() =>
          Buffer.alloc(0),
        );
        if (
          png.length < 8 ||
          png.subarray(0, 8).toString("hex") !== "89504e470d0a1a0a"
        )
          invalid.push(candidate);
      }
      records.push(record);
    }
    records.sort((a, b) => a.image.localeCompare(b.image));
    const qualification = qualifyInventory(
      manifest,
      this.selected,
      records,
      process.env.FRONTEND_BROWSER_RUN ?? "unknown",
    );
    const { missing, unlisted, unknownOwners } = qualification;
    invalid.push(...qualification.invalid);
    const incomplete =
      missing.length + unlisted.length + invalid.length + unknownOwners.length >
      0;
    const status =
      incomplete && result.status === "passed" ? "failed" : result.status;
    const inventory = {
      run: process.env.FRONTEND_BROWSER_RUN,
      status,
      selected: [...this.selected],
      notSelected: Object.keys(manifest.scenarios).filter(
        (name) => !this.selected.has(name),
      ),
      captured: records,
      supplementaryImages: records.reduce(
        (count, record) => count + (record.supplementary?.length ?? 0),
        0,
      ),
      missing,
      unlisted,
      invalid,
      unknownOwners,
      note: "Synthetic fixtures; source surface mapping is in frontend-browser-tests.md. Assertions and captures are not visual approval. No prior-run files qualify this run.",
    };
    await writeFile(
      join(artifactRoot, "inventory.json"),
      JSON.stringify(inventory, null, 2),
    );
    await writeFile(
      join(artifactRoot, "index.html"),
      `<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Frontend browser gallery</title><style>body{font:16px system-ui;margin:2rem;background:#eee;color:#222}main{display:grid;grid-template-columns:repeat(auto-fit,minmax(320px,1fr));gap:1rem}figure{margin:0;padding:1rem;background:white}img{width:100%;height:360px;object-fit:contain;object-position:top}a{color:#153a90}pre{white-space:pre-wrap}</style><h1>Frontend browser gallery</h1><p>Run: ${escape(status)}. ${records.length} captures; ${missing.length} missing; ${unlisted.length} unlisted; ${invalid.length} invalid. Synthetic fixtures only. <a href="inventory.json">Captured/missing inventory</a>. No pixel baselines; inspect full images before approval.</p><details><summary>Incomplete coverage</summary><pre>${escape(JSON.stringify({ missing, unlisted, invalid, unknownOwners }, null, 2))}</pre></details><main>${records.flatMap((item) => captureImages(item).map((image, index) => `<figure><a href="${escape(image)}"><img loading="lazy" src="${escape(image)}" alt="${escape(`${item.scenario} ${item.state} ${item.theme} ${item.viewport}`)}"></a><figcaption>${escape(`${item.scenario} / ${item.state} / ${item.theme} / ${item.viewport}${index ? ` / continued ${index + 1}` : ""}`)}</figcaption></figure>`)).join("\n")}</main></html>`,
    );
    const runPath = relative(artifactBase, artifactRoot);
    await writeFile(
      join(artifactBase, "index.html"),
      `<!doctype html><html lang="en"><meta charset="utf-8"><title>Latest frontend run</title><h1>Latest frontend browser run</h1><p><a href="${escape(runPath)}/index.html">${escape(status)}: ${escape(process.env.FRONTEND_BROWSER_RUN ?? "unknown")}</a></p></html>`,
    );
    if (incomplete)
      console.error(
        `Frontend inventory incomplete: ${missing.length} missing, ${unlisted.length} unlisted, ${invalid.length} invalid captures, ${unknownOwners.length} unknown owners. See ${join(artifactRoot, "inventory.json")}`,
      );
    return { status };
  }
}
