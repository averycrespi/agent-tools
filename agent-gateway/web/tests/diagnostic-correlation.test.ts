import test from "node:test";
import assert from "node:assert/strict";
import { decodeDiagnosticCorrelation } from "../src/diagnostic-correlation.ts";

const processID = "0123456789abcdef0123456789abcdef";

test("diagnostic correlation preserves uint64 precision and optional absence", () => {
  assert.equal(decodeDiagnosticCorrelation(undefined), null);
  assert.deepEqual(
    decodeDiagnosticCorrelation({
      process_id: processID,
      upstream_ref: "18446744073709551615",
    }),
    { processID, upstreamRef: "18446744073709551615" },
  );
  for (const ref of [0, "0", "01", "18446744073709551616", "private-input"]) {
    assert.throws(() =>
      decodeDiagnosticCorrelation({ process_id: processID, upstream_ref: ref }),
    );
  }
  for (const value of [
    null,
    {},
    { process_id: "private-input", upstream_ref: "1" },
    { process_id: "20260920T000000.000000001", upstream_ref: "1" },
    { process_id: processID, upstream_ref: "1", extra: true },
  ])
    assert.throws(() => decodeDiagnosticCorrelation(value));
});
