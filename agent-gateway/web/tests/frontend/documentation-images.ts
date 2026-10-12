import {
  cp,
  lstat,
  mkdir,
  mkdtemp,
  readFile,
  readdir,
  rename,
  rm,
  writeFile,
} from "node:fs/promises";
import { dirname, join, relative, resolve } from "node:path";
import { crc32, deflateSync, inflateSync } from "node:zlib";
import type { CaptureManifest, CaptureRecord } from "./inventory.ts";

export interface DocumentationImage {
  file: string;
  scenario: string;
  state: string;
  theme: string;
  viewport: string;
  width: number;
  height: number;
  document: string;
}
export const maxImageBytes = 200 * 1024;
export function validateDocumentationManifest(
  value: unknown,
  inventory: CaptureManifest,
): DocumentationImage[] {
  const manifest = value as {
    version?: unknown;
    images?: DocumentationImage[];
  };
  if (
    manifest?.version !== 1 ||
    !Array.isArray(manifest.images) ||
    !manifest.images.length
  )
    throw new Error("Documentation manifest requires version 1 and images");
  const files = new Set<string>();
  const mappings = new Set<string>();
  for (const image of manifest.images) {
    if (
      !image ||
      !/^[a-z][a-z0-9-]*\.png$/.test(image.file) ||
      files.has(image.file)
    )
      throw new Error("Missing, unsafe or duplicate documentation filename");
    const matrix = inventory.scenarios[image.scenario]?.[image.state];
    const mapping = `${image.scenario}/${image.state}/${image.theme}-${image.viewport}`;
    if (
      !matrix ||
      !inventory.matrix[matrix]?.includes(image.viewport) ||
      !inventory.themes.includes(image.theme) ||
      mappings.has(mapping)
    )
      throw new Error(
        `Missing or ambiguous documentation mapping: ${image.file}`,
      );
    // Publication is deliberately narrower than the test gallery. Secret/login
    // checkpoints are never candidates; changing this policy requires review.
    if (
      image.theme !== "light" ||
      image.viewport !== "desktop" ||
      image.width !== 1440 ||
      !Number.isInteger(image.height) ||
      image.height < 900 ||
      image.height > 1100 ||
      /secret|bearer|sign-in|login|one-time|auth-flow/.test(mapping)
    )
      throw new Error(`Unsafe or unbounded documentation view: ${image.file}`);
    if (
      typeof image.document !== "string" ||
      !/^(README\.md|docs\/operators\/[a-z-]+\.md)$/.test(image.document)
    )
      throw new Error(`Unsafe documentation reference: ${image.file}`);
    files.add(image.file);
    mappings.add(mapping);
  }
  return manifest.images;
}
export function selectDocumentationImages(
  images: DocumentationImage[],
  names: string[],
) {
  if (!names.length) return images;
  if (
    new Set(names).size !== names.length ||
    names.some((name) => !images.some((image) => image.file === name))
  )
    throw new Error(
      "Select unique published filenames from documentation-images.json",
    );
  return images.filter((image) => names.includes(image.file));
}

// Chromium emits lossless, noninterlaced RGB/RGBA PNGs. Recompress only IDAT;
// never resize text, crop content, quantize colors or introduce another renderer.
export function optimizePNG(bytes: Buffer, image: DocumentationImage): Buffer {
  if (
    bytes.length > 4 * maxImageBytes ||
    bytes.subarray(0, 8).toString("hex") !== "89504e470d0a1a0a"
  )
    throw new Error(`Invalid PNG: ${image.file}`);
  const chunks: { type: string; data: Buffer }[] = [];
  for (let offset = 8; offset < bytes.length; ) {
    if (offset + 12 > bytes.length)
      throw new Error(`Truncated PNG: ${image.file}`);
    const length = bytes.readUInt32BE(offset);
    const end = offset + length + 12;
    if (end > bytes.length) throw new Error(`Truncated PNG: ${image.file}`);
    const type = bytes.toString("ascii", offset + 4, offset + 8);
    if (
      crc32(bytes.subarray(offset + 4, end - 4)) !== bytes.readUInt32BE(end - 4)
    )
      throw new Error(`PNG checksum mismatch: ${image.file}`);
    chunks.push({ type, data: bytes.subarray(offset + 8, end - 4) });
    offset = end;
  }
  const header = chunks[0];
  if (
    header?.type !== "IHDR" ||
    header.data.length !== 13 ||
    header.data.readUInt32BE(0) !== image.width ||
    header.data.readUInt32BE(4) !== image.height ||
    header.data[8] !== 8 ||
    ![2, 6].includes(header.data[9]!) ||
    header.data[10] !== 0 ||
    header.data[11] !== 0 ||
    header.data[12] !== 0 ||
    chunks.at(-1)?.type !== "IEND" ||
    chunks.at(-1)?.data.length !== 0 ||
    chunks.filter((chunk) => chunk.type === "IHDR" || chunk.type === "IEND")
      .length !== 2 ||
    chunks.some((chunk) => !["IHDR", "IDAT", "IEND"].includes(chunk.type))
  )
    throw new Error(`Unexpected PNG format or framing: ${image.file}`);
  const rawLength =
    image.height * (1 + image.width * (header.data[9] === 6 ? 4 : 3));
  const raw = inflateSync(
    Buffer.concat(
      chunks
        .filter((chunk) => chunk.type === "IDAT")
        .map((chunk) => chunk.data),
    ),
    { maxOutputLength: rawLength },
  );
  if (raw.length !== rawLength)
    throw new Error(`Invalid PNG rows: ${image.file}`);
  const chunk = (type: string, data: Buffer) => {
    const result = Buffer.alloc(data.length + 12);
    result.writeUInt32BE(data.length);
    result.write(type, 4);
    data.copy(result, 8);
    result.writeUInt32BE(crc32(result.subarray(4, -4)), result.length - 4);
    return result;
  };
  const output = Buffer.concat([
    bytes.subarray(0, 8),
    chunk("IHDR", header.data),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
  if (output.length > maxImageBytes)
    throw new Error(`Documentation PNG exceeds 200 KiB: ${image.file}`);
  return output;
}

export interface DocumentationRun {
  run: string;
  status: string;
  selected: string[];
  captured: CaptureRecord[];
  missing: string[];
  unlisted: string[];
  invalid: string[];
  unknownOwners: string[];
}
export async function prepareDocumentationImages(
  images: DocumentationImage[],
  run: DocumentationRun,
  currentRun: string,
  root: string,
): Promise<Map<string, Buffer>> {
  if (
    !currentRun ||
    run.run !== currentRun ||
    run.status !== "passed" ||
    [run.missing, run.unlisted, run.invalid, run.unknownOwners].some(
      (items) => !Array.isArray(items) || items.length,
    )
  )
    throw new Error(
      "Documentation export requires the successful current capture run",
    );
  const owners = [...new Set(images.map((image) => image.scenario))].sort();
  if (JSON.stringify([...run.selected].sort()) !== JSON.stringify(owners))
    throw new Error(
      "Documentation capture selection does not match this invocation",
    );
  const result = new Map<string, Buffer>();
  for (const image of images) {
    const matches = run.captured.filter(
      (record) =>
        record.scenario === image.scenario &&
        record.state === image.state &&
        record.theme === image.theme &&
        record.viewport === image.viewport,
    );
    const record = matches[0];
    const path = `captures/${image.scenario}/${image.state}/${image.theme}-${image.viewport}.png`;
    if (
      matches.length !== 1 ||
      record?.run !== currentRun ||
      record.image !== path ||
      (record.supplementary?.length ?? 0) !== 0
    )
      throw new Error(
        `Missing, ambiguous or incomplete documentation capture: ${image.file}`,
      );
    const source = join(root, path);
    const metadata = JSON.parse(
      await readFile(source.replace(/\.png$/, ".json"), "utf8"),
    );
    if (
      metadata.run !== currentRun ||
      metadata.scenario !== image.scenario ||
      metadata.state !== image.state ||
      metadata.theme !== image.theme ||
      metadata.viewport !== image.viewport ||
      metadata.width !== image.width ||
      metadata.height !== 900
    )
      throw new Error(`Capture metadata mismatch: ${image.file}`);
    if (!(await lstat(source)).isFile())
      throw new Error(`Capture must be a regular PNG: ${image.file}`);
    result.set(image.file, optimizePNG(await readFile(source), image));
  }
  return result;
}

// Prepare every selected byte before touching the published directory. Directory
// replacement keeps unselected files intact and rolls back a failed installation.
export async function publishDocumentationImages(
  output: string,
  images: Map<string, Buffer>,
  move = rename,
) {
  const parent = dirname(output);
  await mkdir(parent, { recursive: true });
  if (
    (await readdir(parent)).some((name) =>
      name.startsWith(".documentation-images-"),
    )
  )
    throw new Error(
      "Interrupted documentation publication: preserve and reconcile .documentation-images-* before exporting",
    );
  const stage = await mkdtemp(join(parent, ".documentation-images-"));
  const next = join(stage, "next");
  const previous = join(stage, "previous");
  let hadPrevious = false;
  let preserveRecovery = false;
  let installed = false;
  try {
    const existing = await lstat(output).catch(
      (error: NodeJS.ErrnoException) => {
        if (error.code !== "ENOENT") throw error;
        return null;
      },
    );
    if (existing && !existing.isDirectory())
      throw new Error(
        "Documentation output must be a directory, not a symlink",
      );
    if (existing)
      await cp(output, next, { recursive: true, verbatimSymlinks: true });
    else await mkdir(next);
    for (const [name, bytes] of images) {
      if (!/^[a-z][a-z0-9-]*\.png$/.test(name))
        throw new Error("Unsafe publication filename");
      // Remove only the staged entry, so even a prior symlink is never followed.
      await rm(join(next, name), { force: true });
      await writeFile(join(next, name), bytes, { flag: "wx" });
    }
    if (existing) {
      await move(output, previous);
      hadPrevious = true;
    }
    try {
      await move(next, output);
      installed = true;
    } catch (error) {
      if (hadPrevious) {
        try {
          await rename(previous, output);
        } catch (rollback) {
          preserveRecovery = true;
          throw new AggregateError(
            [error, rollback],
            `Publication failed; prior images retained at ${previous}`,
          );
        }
      }
      throw error;
    }
  } finally {
    if (!preserveRecovery) {
      try {
        await rm(stage, { recursive: true, force: true });
      } catch (error) {
        if (!installed) throw error;
        console.warn(
          `Images published; staging cleanup failed at ${stage}. Preserve and reconcile before another export.`,
        );
      }
    }
  }
}

export async function validateDocumentationReferences(
  images: DocumentationImage[],
  gatewayRoot: string,
) {
  for (const image of images) {
    const document = resolve(gatewayRoot, image.document);
    const target = resolve(gatewayRoot, "docs/images", image.file);
    const link = relative(dirname(document), target).replaceAll("\\", "/");
    const markdown = await readFile(document, "utf8");
    const escaped = link.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    if (!new RegExp(`!\\[[^\\]\\n]{15,}\\]\\(${escaped}\\)`).test(markdown))
      throw new Error(
        `Missing image reference or meaningful alt text: ${image.document} → ${image.file}`,
      );
    optimizePNG(await readFile(target), image);
  }
}
