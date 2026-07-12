import test from "node:test";
import assert from "node:assert/strict";
import { bucketLabel, rateLabel, unwrapResponse } from "../assets/view-model.js";

test("truncation wrappers are explicit", () => {
  assert.deepEqual(unwrapResponse({ truncated: true, value: { rows: [] } }), { value: { rows: [] }, truncated: true });
  assert.deepEqual(unwrapResponse({ rows: [] }), { value: { rows: [] }, truncated: false });
  assert.deepEqual(unwrapResponse({ truncated: true }), { value: null, truncated: true });
});

test("rates include nullable coverage and confirmed inferred split", () => {
  const totals = { calls: 10, classifiable: 4, confirmed_errors: 1, inferred_errors: 1 };
  assert.match(rateLabel(null, totals), /^Unknown · 4\/10 classifiable$/);
  assert.match(rateLabel(0.5, totals), /50.0% errors · 4\/10 classifiable · 1 confirmed \/ 1 inferred/);
});

test("bucket labels are usable without color", () => {
  const label = bucketLabel({ key: "2026-07-12", sessions: 2, cost_as_logged: 1.5, tool_calls: 3 }, "UTC");
  assert.match(label, /2 sessions/);
  assert.match(label, /\$1.50 logged cost/);
  assert.match(label, /UTC/);
});
