import assert from "node:assert/strict";
import test from "node:test";
import {
  mkdtemp,
  mkdir,
  readFile,
  readdir,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { crc32, deflateSync, inflateSync } from "node:zlib";
import manifest from "./frontend/documentation-images.json" with { type: "json" };
import inventory from "./frontend/inventory.json" with { type: "json" };
import {
  maxImageBytes,
  optimizePNG,
  prepareDocumentationImages,
  publishDocumentationImages,
  selectDocumentationImages,
  validateDocumentationManifest,
  validateDocumentationReferences,
  type DocumentationRun,
} from "./frontend/documentation-images.ts";
import { generateDocumentationImages } from "../scripts/documentation-images.ts";

const images = validateDocumentationManifest(manifest, inventory);
const image = images[0]!;
function png() {
  const chunk = (type: string, data: Buffer) => {
    const bytes = Buffer.alloc(data.length + 12);
    bytes.writeUInt32BE(data.length);
    bytes.write(type, 4);
    data.copy(bytes, 8);
    bytes.writeUInt32BE(crc32(bytes.subarray(4, -4)), bytes.length - 4);
    return bytes;
  };
  const header = Buffer.alloc(13);
  header.writeUInt32BE(1440);
  header.writeUInt32BE(900, 4);
  header[8] = 8;
  header[9] = 2;
  return Buffer.concat([
    Buffer.from("89504e470d0a1a0a", "hex"),
    chunk("IHDR", header),
    chunk("IDAT", deflateSync(Buffer.alloc(900 * (1440 * 3 + 1)))),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}
const path = `captures/${image.scenario}/${image.state}/light-desktop.png`;
function report(): DocumentationRun {
  return {
    run: "current",
    status: "passed",
    selected: [image.scenario],
    captured: [{ ...image, image: path, run: "current", supplementary: [] }],
    missing: [],
    unlisted: [],
    invalid: [],
    unknownOwners: [],
  };
}
async function fixture(fn: (root: string) => Promise<void>) {
  const root = await mkdtemp(join(tmpdir(), "documentation-images-"));
  try {
    await mkdir(join(root, "captures", image.scenario, image.state), {
      recursive: true,
    });
    await writeFile(join(root, path), png());
    await writeFile(
      join(root, path.replace(/\.png$/, ".json")),
      JSON.stringify({ ...image, run: "current" }),
    );
    await fn(root);
  } finally {
    await rm(root, { recursive: true, force: true });
  }
}

test("documentation manifest maps to existing gallery states and referenced bounded PNGs", async () => {
  await validateDocumentationReferences(
    images,
    resolve(import.meta.dirname, "../.."),
  );
  const published = (
    await readdir(resolve(import.meta.dirname, "../../docs/images"))
  ).sort();
  assert.deepEqual(published, images.map((image) => image.file).sort());
});
test("manifest rejects missing, ambiguous, unsafe and secret mappings", () => {
  for (const patch of [
    { file: "../escape.png" },
    { state: "missing" },
    { theme: "dark" },
    { viewport: "mobile" },
    { height: 2000 },
    { document: "../README.md" },
  ])
    assert.throws(() =>
      validateDocumentationManifest(
        { version: 1, images: [{ ...image, ...patch }] },
        inventory,
      ),
    );
  assert.throws(() =>
    validateDocumentationManifest(
      { version: 1, images: [image, image] },
      inventory,
    ),
  );
  assert.throws(
    () =>
      validateDocumentationManifest(
        { version: 1, images: [image, { ...image, file: "duplicate.png" }] },
        inventory,
      ),
    /ambiguous/,
  );
  assert.throws(() => selectDocumentationImages(images, ["missing.png"]));
  assert.throws(() =>
    selectDocumentationImages(images, [image.file, image.file]),
  );
  assert.deepEqual(selectDocumentationImages(images, [image.file]), [image]);
});
test("PNG optimization is lossless, bounded and rejects corrupt or unexpected framing", () => {
  const bytes = png();
  const optimized = optimizePNG(bytes, image);
  assert.ok(optimized.length < maxImageBytes);
  const pixels = (png: Buffer) =>
    inflateSync(png.subarray(41, 41 + png.readUInt32BE(33)));
  assert.deepEqual(pixels(optimized), pixels(bytes));
  const corrupt = Buffer.from(bytes);
  corrupt[45] = corrupt[45]! ^ 1;
  for (const bad of [Buffer.from("not png"), bytes.subarray(0, -1), corrupt])
    assert.throws(() => optimizePNG(bad, image));
  assert.throws(() => optimizePNG(bytes, { ...image, height: 901 }), /framing/);
});
test("export accepts only one complete mapping from the successful current selected run", async () => {
  await fixture(async (root) => {
    const prepared = await prepareDocumentationImages(
      [image],
      report(),
      "current",
      root,
    );
    assert.deepEqual([...prepared.keys()], [image.file]);
    for (const alter of [
      (r: DocumentationRun) => {
        r.status = "failed";
      },
      (r: DocumentationRun) => {
        r.run = "old";
      },
      (r: DocumentationRun) => {
        r.missing = ["other-state"];
      },
      (r: DocumentationRun) => {
        r.invalid = ["bad.png"];
      },
      (r: DocumentationRun) => {
        r.selected.push("unrequested-owner");
      },
      (r: DocumentationRun) => {
        r.captured = [];
      },
      (r: DocumentationRun) => {
        r.captured.push(r.captured[0]!);
      },
      (r: DocumentationRun) => {
        r.captured[0]!.run = "old";
      },
      (r: DocumentationRun) => {
        r.captured[0]!.image = "../old.png";
      },
      (r: DocumentationRun) => {
        r.captured[0]!.supplementary = ["light-desktop-dialog-2.png"];
      },
    ]) {
      const bad = report();
      alter(bad);
      await assert.rejects(
        prepareDocumentationImages([image], bad, "current", root),
      );
    }
    await writeFile(
      join(root, path.replace(/\.png$/, ".json")),
      JSON.stringify({ ...image, run: "old" }),
    );
    await assert.rejects(
      prepareDocumentationImages([image], report(), "current", root),
      /metadata/,
    );
    await writeFile(
      join(root, path.replace(/\.png$/, ".json")),
      JSON.stringify({ ...image, run: "current" }),
    );
    await rm(join(root, path));
    await assert.rejects(
      prepareDocumentationImages([image], report(), "current", root),
      /ENOENT/,
    );
  });
});
test("a failed replacement restores every previous image; successful selection preserves unselected bytes", async () => {
  await fixture(async (root) => {
    const output = join(root, "images");
    await mkdir(output);
    await writeFile(join(output, "overview.png"), "prior selected");
    await writeFile(join(output, "other.png"), "prior unselected");
    const next = new Map([["overview.png", png()]]);
    let moves = 0;
    await assert.rejects(
      publishDocumentationImages(output, next, async (from, to) => {
        if (++moves === 2) throw new Error("injected replacement failure");
        await rename(from, to);
      }),
      /injected/,
    );
    assert.equal(
      await readFile(join(output, "overview.png"), "utf8"),
      "prior selected",
    );
    assert.equal(
      await readFile(join(output, "other.png"), "utf8"),
      "prior unselected",
    );
    await publishDocumentationImages(output, next);
    assert.deepEqual(await readFile(join(output, "overview.png")), png());
    assert.equal(
      await readFile(join(output, "other.png"), "utf8"),
      "prior unselected",
    );
  });
});
test("interrupted publication refuses without deleting retained recovery images", async () => {
  await fixture(async (root) => {
    const recovery = join(root, ".documentation-images-interrupted");
    await mkdir(recovery);
    await writeFile(join(recovery, "previous.png"), "prior image");
    await assert.rejects(
      publishDocumentationImages(
        join(root, "images"),
        new Map([[image.file, png()]]),
      ),
      /Interrupted/,
    );
    assert.equal(
      await readFile(join(recovery, "previous.png"), "utf8"),
      "prior image",
    );
  });
});

test("capture failure or missing receipt never falls back to a previous gallery or touches published images", async () => {
  const output = resolve(import.meta.dirname, "../../docs/images");
  const before = await Promise.all(
    images.map((image) => readFile(join(output, image.file))),
  );
  await assert.rejects(
    generateDocumentationImages([image.file], () => {
      throw new Error("injected capture failure");
    }),
    /capture failure/,
  );
  await assert.rejects(
    generateDocumentationImages([image.file], () => {}),
    /ENOENT/,
  );
  assert.deepEqual(
    await Promise.all(
      images.map((image) => readFile(join(output, image.file))),
    ),
    before,
  );
});
