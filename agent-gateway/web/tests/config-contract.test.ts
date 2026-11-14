import assert from "node:assert/strict";
import test from "node:test";
import {
  configurationInputBytes,
  decodeImport,
  decodePreview,
  resourceKinds,
  singleDefinition,
} from "../src/config-contract.ts";
import { parseFragment } from "../src/location.ts";
import { resourceDestinations } from "../src/config-contract.ts";
for (const kind of resourceKinds) {
  test(`${kind}: resource-only input and closed route`, () => {
    const raw = JSON.stringify({
      format: "agent-gateway-config",
      version: 2,
      kind,
      key: "example",
      spec: {},
    });
    assert.equal(singleDefinition(raw, kind).key, "example");
    assert.ok(parseFragment(`#/${resourceDestinations[kind]}/import`));
    assert.equal(
      parseFragment(`#/${resourceDestinations[kind]}/import?tab=status`),
      undefined,
    );
    assert.throws(() =>
      singleDefinition(raw.replace('"version":2', '"version":1'), kind),
    );
    assert.throws(() =>
      singleDefinition(
        raw.replace('"key":"example"', '"key":"example","key":"other"'),
        kind,
      ),
    );
    assert.throws(
      () =>
        singleDefinition(
          JSON.stringify({
            format: "agent-gateway-config",
            version: 2,
            resources: [JSON.parse(raw)],
          }),
          kind,
        ),
      /System Configuration/,
    );
    assert.throws(() =>
      singleDefinition(
        raw.replace(`"kind":"${kind}"`, '"kind":"git_routing"'),
        kind,
      ),
    );
  });
}
test("input failures do not disclose payloads and byte limits include UTF-8", () => {
  for (const raw of [
    '{"secret":"REJECTED_CANARY"',
    '"REJECTED_CANARY"',
    "é".repeat(configurationInputBytes),
  ]) {
    assert.throws(
      () => singleDefinition(raw, "agent"),
      (e) => e instanceof Error && !e.message.includes("REJECTED_CANARY"),
    );
  }
});
test("online preview and committed result are distinct closed facts", () => {
  const creation = {
    kind: "agent",
    key: "example",
    reserved: false,
    dependencies: [],
    requirements: ["issue_agent_credential"],
  };
  assert.deepEqual(
    decodePreview(
      { advisory: true, creations: [creation] },
      "agent",
      "example",
    ),
    creation,
  );
  for (const bad of [
    { advisory: false, creations: [creation] },
    { advisory: true, creations: [creation, creation] },
    {
      advisory: true,
      creations: [{ ...creation, requirements: ["invented"] }],
    },
    { advisory: true, creations: [{ ...creation, key: "wrong" }] },
  ])
    assert.throws(() => decodePreview(bad, "agent", "example"));
  const result = {
    configuration: "committed",
    creations: [
      {
        kind: "agent",
        key: "example",
        id: "01ARZ3NDEKTSV4RRFFQ69G5FA0",
        reserved: false,
      },
    ],
    activation: "not_required",
  };
  assert.deepEqual(decodeImport(result, "agent", "example"), result);
  assert.throws(() => decodeImport(result, "agent", "example", true));
  const reservation = {
    ...result,
    creations: [{ ...result.creations[0], reserved: true }],
  };
  assert.deepEqual(
    decodeImport(reservation, "agent", "example", true),
    reservation,
  );
  assert.throws(() =>
    decodePreview(
      {
        advisory: true,
        creations: [
          {
            ...creation,
            dependencies: [{ kind: "gateway_mcp", key: "unexpected" }],
          },
        ],
      },
      "agent",
      "example",
    ),
  );
  assert.throws(() =>
    decodePreview(
      { advisory: true, creations: [{ ...creation, reserved: true }] },
      "agent",
      "example",
    ),
  );
  assert.throws(() =>
    decodeImport({ ...result, activation: "ready" }, "agent", "example"),
  );
  assert.throws(() =>
    decodeImport({ ...result, configuration: "preview" }, "agent", "example"),
  );
});
