import assert from "node:assert/strict";
import test from "node:test";
import {
  credentialMaterialLabel,
  validCredentialMaterial,
} from "../src/credential-material.ts";

test("credential setup labels distinguish unset, configured and failed material", () => {
  for (const [state, available, label] of [
    ["not_configured", false, "Not configured"],
    ["ready", true, "Configured"],
    ["unavailable", false, "Unavailable"],
  ] as const) {
    assert.equal(validCredentialMaterial(state, available), true);
    assert.equal(
      credentialMaterialLabel({ material_state: state, available }),
      label,
    );
  }
  for (const [state, available] of [
    ["ready", false],
    ["not_configured", true],
    [undefined, false],
    ["other", false],
  ])
    assert.equal(validCredentialMaterial(state, available), false);
});
