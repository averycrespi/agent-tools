import assert from "node:assert/strict";
import test from "node:test";
import { createHash } from "node:crypto";
import { readFile, readdir } from "node:fs/promises";
import { resolve } from "node:path";
import surfaceInventory from "./frontend/inventory.json" with { type: "json" };
import setup from "./frontend/setup.ts";
import {
  qualifyInventory,
  captureImages,
  type CaptureManifest,
  type CaptureRecord,
} from "./frontend/inventory.ts";

test("new or changed rendered surfaces require an explicit inventory review", async () => {
  const source = resolve(import.meta.dirname, "../src");
  const files = (await readdir(source))
    .filter(
      (name) =>
        name.endsWith(".tsx") ||
        name === "location.ts" ||
        name === "styles.css",
    )
    .sort();
  assert.deepEqual(
    Object.keys(surfaceInventory.sourceDigests).sort(),
    files,
    "New/removed production surface: update the source/state mapping and capture inventory",
  );
  for (const [name, expected] of Object.entries(
    surfaceInventory.sourceDigests,
  )) {
    const actual = createHash("sha256")
      .update(await readFile(resolve(source, name)))
      .digest("hex");
    assert.equal(
      actual,
      expected,
      `${name}: review affected page/modal/conditional-state coverage before updating its inventory fingerprint`,
    );
  }
});

test("each execution replaces any caller-supplied artifact identity", () => {
  const previous = process.env.FRONTEND_BROWSER_RUN;
  try {
    process.env.FRONTEND_BROWSER_RUN = "old-run";
    setup();
    const first = process.env.FRONTEND_BROWSER_RUN;
    assert.notEqual(first, "old-run");
    setup();
    assert.notEqual(process.env.FRONTEND_BROWSER_RUN, first);
  } finally {
    if (previous === undefined) delete process.env.FRONTEND_BROWSER_RUN;
    else process.env.FRONTEND_BROWSER_RUN = previous;
  }
});

const manifest: CaptureManifest = {
  matrix: { standard: ["desktop", "mobile"] },
  themes: ["light", "dark"],
  scenarios: { shell: { ready: "standard" }, other: { empty: "standard" } },
};
const selected = new Set(["shell"]);
const records: CaptureRecord[] = ["light", "dark"].flatMap((theme) =>
  ["desktop", "mobile"].map((viewport) => ({
    scenario: "shell",
    state: "ready",
    theme,
    viewport,
    run: "current",
    image: `captures/shell/ready/${theme}-${viewport}.png`,
  })),
);

test("dialog continuations stay inside their state and viewport image group", () => {
  assert.ok(records[0]);
  const record = {
    ...records[0],
    supplementary: ["light-desktop-dialog-2.png", "light-desktop-dialog-3.png"],
  };
  assert.deepEqual(captureImages(record), [
    record.image,
    "captures/shell/ready/light-desktop-dialog-2.png",
    "captures/shell/ready/light-desktop-dialog-3.png",
  ]);
  for (const file of [
    "../../outside.png",
    "dark-mobile-dialog-2.png",
    "light-desktop-dialog-10.png",
  ])
    assert.throws(
      () => captureImages({ ...record, supplementary: [file] }),
      /Invalid dialog/,
    );
  assert.throws(
    () =>
      captureImages({
        ...record,
        supplementary: [
          "light-desktop-dialog-2.png",
          "light-desktop-dialog-2.png",
        ],
      }),
    /Invalid dialog/,
  );
});

test("capture inventory requires every declared theme/viewport and only selected owners", () => {
  assert.deepEqual(qualifyInventory(manifest, selected, records, "current"), {
    missing: [],
    unlisted: [],
    invalid: [],
    unknownOwners: [],
  });
  const partial = qualifyInventory(
    manifest,
    selected,
    records.slice(1),
    "current",
  );
  assert.deepEqual(partial.missing, ["shell/ready/light-desktop"]);
  assert.equal(
    qualifyInventory(manifest, new Set(["shell", "other"]), records, "current")
      .missing.length,
    4,
  );
});
test("prior-run artifacts cannot hide a missing capture", () => {
  const result = qualifyInventory(manifest, selected, records, "different-run");
  assert.equal(result.missing.length, 4);
  assert.equal(result.invalid.length, 4);
});
test("new owners and checkpoints require deliberate inventory updates", () => {
  assert.deepEqual(
    qualifyInventory(manifest, new Set(["new-owner"]), [], "current")
      .unknownOwners,
    ["new-owner"],
  );
  const result = qualifyInventory(
    manifest,
    selected,
    [
      ...records,
      { ...records[0]!, image: "captures/shell/new-state/light-desktop.png" },
    ],
    "current",
  );
  assert.deepEqual(result.unlisted, ["shell/new-state/light-desktop"]);
  assert.throws(
    () =>
      qualifyInventory({ ...manifest, matrix: {} }, selected, [], "current"),
    /Unknown capture matrix/,
  );
});
