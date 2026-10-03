export interface CaptureManifest {
  matrix: Record<string, string[]>;
  themes: string[];
  scenarios: Record<string, Record<string, string>>;
}
export interface CaptureRecord {
  scenario: string;
  state: string;
  theme: string;
  viewport: string;
  image: string;
  run: string;
  supplementary?: string[];
}
export function captureImages(record: CaptureRecord): string[] {
  const files = record.supplementary ?? [];
  const prefix = `${record.theme}-${record.viewport}-dialog-`;
  if (
    !Array.isArray(files) ||
    files.some(
      (file) =>
        typeof file !== "string" ||
        !file.startsWith(prefix) ||
        !/^[a-z0-9-]+-dialog-[2-9]\.png$/.test(file),
    ) ||
    new Set(files).size !== files.length
  )
    throw new Error("Invalid dialog continuation filenames");
  const directory = record.image.slice(0, record.image.lastIndexOf("/") + 1);
  return [record.image, ...files.map((file) => `${directory}${file}`)];
}
export function qualifyInventory(
  manifest: CaptureManifest,
  selected: ReadonlySet<string>,
  records: CaptureRecord[],
  run: string,
) {
  const expected = new Set<string>();
  const unknownOwners = [...selected].filter(
    (name) => !(name in manifest.scenarios),
  );
  for (const [scenario, states] of Object.entries(manifest.scenarios)) {
    if (!selected.has(scenario)) continue;
    for (const [state, matrix] of Object.entries(states)) {
      const viewports = manifest.matrix[matrix];
      if (!viewports) throw new Error(`Unknown capture matrix: ${matrix}`);
      for (const theme of manifest.themes)
        for (const viewport of viewports)
          expected.add(`${scenario}/${state}/${theme}-${viewport}`);
    }
  }
  const invalid = records
    .filter((record) => record.run !== run)
    .map((record) => record.image);
  const captured = new Set(
    records
      .filter((record) => record.run === run)
      .map((item) =>
        item.image.replace(/^captures\//, "").replace(/\.png$/, ""),
      ),
  );
  return {
    missing: [...expected].filter((key) => !captured.has(key)),
    unlisted: [...captured].filter((key) => !expected.has(key)),
    invalid,
    unknownOwners,
  };
}
