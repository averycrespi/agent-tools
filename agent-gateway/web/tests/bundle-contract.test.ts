import assert from "node:assert/strict";
import test from "node:test";
import {
  bundleDefinitions,
  decodeBundleImport,
  decodeBundlePreview,
} from "../src/bundle-contract.ts";
import {
  configurationInputBytes,
  singleDefinition,
} from "../src/config-contract.ts";
import { parseFragment } from "../src/location.ts";
const definition = {
  format: "agent-gateway-config",
  version: 2,
  kind: "agent",
  key: "builder",
  spec: {},
};
const routing = {
  format: "agent-gateway-config",
  version: 2,
  kind: "git_routing",
  spec: { origins: [] },
};
const bundle = (resources: unknown[]) =>
  JSON.stringify({ format: "agent-gateway-config", version: 2, resources });
test("System Configuration has a closed canonical route; individual import remains exact-one", () => {
  assert.ok(parseFragment("#/system?tab=configuration"));
  assert.equal(
    parseFragment("#/system?tab=configuration&mode=update"),
    undefined,
  );
  assert.throws(
    () => singleDefinition(bundle([definition]), "agent"),
    /System Configuration/,
  );
});
test("bundle identities are typed, explicit, duplicate-free and byte-bounded without numeric rewriting", () => {
  const raw = bundle([definition, routing]).replace(
    '"spec":{}',
    '"spec":{"integer":9007199254740993,"fraction":1.0,"exponent":1e+2}',
  );
  assert.deepEqual(bundleDefinitions(raw), [
    { kind: "agent", key: "builder", reserved: false },
    { kind: "git_routing", key: "", reserved: false },
  ]);
  for (const token of ["9007199254740993", "1.0", "1e+2"])
    assert.ok(raw.includes(token));
  assert.throws(
    () => bundleDefinitions(raw.replace('"version":2', '"version":1')),
    /Version 1/,
  );
  for (const invalid of [
    bundle([definition, definition]),
    bundle([{ ...definition, key: "" }]),
    bundle([{ ...routing, key: "bad" }]),
    bundle([{ ...routing, reserved: true }]),
    raw.replace('"version":2', '"version":2,"version":2'),
    JSON.stringify(definition),
    "é".repeat(configurationInputBytes),
  ])
    assert.throws(() => bundleDefinitions(invalid));
  assert.equal(
    bundleDefinitions(
      bundle([{ ...definition, reserved: true, spec: null }]),
    )[0]?.reserved,
    true,
  );
});
test("preview and result cover every exact submitted identity, reservation and routing singleton", () => {
  const definitions = bundleDefinitions(
    bundle([
      definition,
      routing,
      { ...definition, kind: "mcp_grant", reserved: true, spec: null },
    ]),
  );
  const creations = [
    {
      kind: "agent",
      key: "builder",
      reserved: false,
      dependencies: [],
      requirements: ["issue_agent_credential"],
    },
    {
      kind: "git_routing",
      reserved: false,
      dependencies: [],
      requirements: ["initialize_untouched_routing_seed"],
    },
    {
      kind: "mcp_grant",
      key: "builder",
      reserved: true,
      dependencies: [],
      requirements: [],
    },
  ];
  assert.equal(
    decodeBundlePreview({ advisory: true, creations }, definitions).length,
    3,
  );
  for (const invalid of [
    creations.slice(1),
    [...creations, creations[0]],
    [...creations].reverse(),
    [{ ...creations[0], key: "wrong" }, ...creations.slice(1)],
  ])
    assert.throws(() =>
      decodeBundlePreview({ advisory: true, creations: invalid }, definitions),
    );
  const result = {
    configuration: "committed",
    activation: "pending",
    creations: [
      {
        kind: "agent",
        key: "builder",
        reserved: false,
        id: "01ARZ3NDEKTSV4RRFFQ69G5FA0",
      },
      { kind: "git_routing", reserved: false },
      {
        kind: "mcp_grant",
        key: "builder",
        reserved: true,
        id: "01ARZ3NDEKTSV4RRFFQ69G5FA1",
      },
    ],
  };
  assert.deepEqual(decodeBundleImport(result, definitions), result);
  assert.throws(() =>
    decodeBundleImport({ ...result, activation: "ready" }, definitions),
  );
  assert.throws(() =>
    decodeBundleImport(
      { ...result, creations: result.creations.slice(1) },
      definitions,
    ),
  );
  assert.throws(() =>
    decodeBundleImport(
      { configuration: "wrong", activation: "ready", creations: [] },
      [],
    ),
  );
});
test("large bounded bundles never truncate identities or response coverage", () => {
  const resources = Array.from({ length: 1500 }, (_, i) => ({
    ...definition,
    key: `agent_${i}`,
  }));
  const definitions = bundleDefinitions(bundle(resources));
  assert.equal(definitions.length, 1500);
  const creations = resources.map((v) => ({
    kind: v.kind,
    key: v.key,
    reserved: false,
    dependencies: [],
    requirements: [],
  }));
  assert.equal(
    decodeBundlePreview({ advisory: true, creations }, definitions).at(-1)?.key,
    "agent_1499",
  );
});
