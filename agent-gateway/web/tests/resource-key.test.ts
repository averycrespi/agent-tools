import assert from "node:assert/strict";
import test from "node:test";
import { validResourceKey } from "../src/resource-key.ts";

test("resource keys use bounded ASCII identifier grammar, not display names", () => {
  for (const key of ["a", "build-agent_1", "a".repeat(64)])
    assert.equal(validResourceKey(key), true, key);
  for (const key of [
    undefined,
    null,
    1,
    "",
    "Upper",
    "0agent",
    "with space",
    "a.b",
    "é",
    "a".repeat(65),
  ])
    assert.equal(validResourceKey(key), false, String(key));
});
