import assert from "node:assert/strict";
import test from "node:test";
import { resourceUtilization } from "../src/resource-utilization.ts";

const maximum = Number.MAX_SAFE_INTEGER;
for (const [inUse, limit, expected] of [
  [0, 32, "0%"],
  [-0, 32, "0%"],
  [8, 32, "25%"],
  [27, 32, "84.4%"],
  [1, 16, "6.3%"],
  [1, 3, "33.3%"],
  [32, 32, "100%"],
  [9994, 10000, "99.9%"],
  [9995, 10000, "99.9%"],
  [9999, 10000, "99.9%"],
  [0, 0, "N/A"],
  [32, 0, "N/A"],
  [33, 32, "103.1%"],
  [64, 32, "200%"],
  [maximum - 1, maximum, "99.9%"],
  [maximum, maximum, "100%"],
  [1, maximum, "0%"],
  [maximum, 1, "900719925474099100%"],
  [maximum, 32, "28147497671065596.9%"],
] as const) {
  test(`resource utilization ${inUse}/${limit} = ${expected}`, () => {
    assert.equal(resourceUtilization(inUse, limit), expected);
  });
}
