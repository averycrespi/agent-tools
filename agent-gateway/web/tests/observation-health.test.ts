import test from "node:test";
import assert from "node:assert/strict";
import {
  decodeHistoryHealth,
  decodeTrafficRecovery,
  trafficRecoveryAction,
  decodeDiagnosticHealth,
  decodeObservations,
  measurementText,
} from "../src/observation-health.ts";

import {
  trafficRecoveryFixture,
  recoveryStates,
} from "./browser/traffic-recovery-fixture.ts";

test("traffic recovery retains initiating facts and rejects unsafe incident fields", () => {
  for (const state of recoveryStates) {
    const input = trafficRecoveryFixture(state);
    const decoded = decodeTrafficRecovery(input)!;
    assert.equal(decoded.health, state);
    if (state !== "healthy") {
      assert.equal(decoded.incident!.discarded, 3);
      assert.notEqual(trafficRecoveryAction(decoded), "");
      for (const [key, value] of Object.entries({
        cause: "SECRET",
        stage: "raw SQL",
        settlement: "assumed",
        first_failure: "invalid",
        sqlite_code: 65536,
        discarded: -1,
        recovery_cause: "secret",
        recovery_stage: "secret",
      })) {
        assert.throws(() =>
          decodeTrafficRecovery({
            ...input,
            incident: { ...input.incident, [key]: value },
          }),
        );
      }
      assert.throws(() =>
        decodeTrafficRecovery({
          ...input,
          incident: { ...input.incident, extra: "secret" },
        }),
      );
    }
  }
  assert.equal(decodeTrafficRecovery({}), undefined);
  assert.throws(() => decodeTrafficRecovery({ health: "healthy" }));
});

const history = {
  delivery: {
    accepted: 9,
    acknowledged: 7,
    discarded: 3,
    queue_records: 1,
    queue_bytes: 1024,
    completion_records: 1,
    queue_record_limit: 128,
    queue_byte_limit: 2097152,
  },
  database_measurement: { state: "unavailable", bytes: null },
  wal_measurement: { state: "absent", bytes: null },
  free_space_measurement: { state: "available", bytes: 0 },
  pressure_reason: "low_space",
  accounting_available: false,
};
test("history measurements preserve absence and unavailable independently of zero", () => {
  const value = decodeHistoryHealth(history)!;
  assert.equal(measurementText(value.database_measurement), "Unavailable");
  assert.equal(measurementText(value.wal_measurement), "Absent");
  assert.equal(measurementText(value.free_space_measurement), "0 bytes");
  assert.equal(value.delivery.discarded, 3);
  assert.equal(decodeHistoryHealth({}), undefined);
  assert.throws(() =>
    decodeHistoryHealth({
      ...history,
      database_measurement: { state: "unavailable", bytes: 0 },
    }),
  );
  assert.throws(() =>
    decodeHistoryHealth({ ...history, pressure_reason: "secret-canary" }),
  );
});
test("diagnostic health retains cumulative loss and pending writes", () => {
  const value = {
    state: "failed",
    epoch: "process",
    accepted: 4,
    written: 0,
    dropped: 7,
    invalid: 1,
    write_failures: 1,
    queue_records: 3,
    queue_bytes: 12288,
    queue_limit: 255,
    writing: true,
    last_successful_write: null,
    overflow: false,
  };
  assert.deepEqual(decodeDiagnosticHealth(value), value);
  assert.equal(decodeDiagnosticHealth(undefined), undefined);
  assert.throws(() => decodeDiagnosticHealth({ ...value, extra: "secret" }));
  assert.throws(() =>
    decodeDiagnosticHealth({ ...value, dropped: Number.MAX_SAFE_INTEGER + 1 }),
  );
});
test("execution observations have closed protocol and bucket cardinality", () => {
  const value = {
    epoch: "process",
    started_at: "2026-07-28T12:00:00Z",
    coverage: "owner_boundaries",
    overflow: false,
    protocols: ["mcp", "http", "connect", "git"].map((protocol) => ({
      protocol,
      requests: 0,
      executions: 0,
      results: [0, 0, 0, 0, 0],
      git_reports: [0, 0, 0],
      latency: Array.from({ length: 3 }, () => [0, 0, 0, 0, 0, 0]),
    })),
  };
  assert.deepEqual(decodeObservations(value), value);
  assert.equal(decodeObservations(undefined), undefined);
  assert.throws(() =>
    decodeObservations({
      ...value,
      protocols: [...value.protocols, ...value.protocols],
    }),
  );
  assert.throws(() =>
    decodeObservations({
      ...value,
      protocols: value.protocols.map((p) => ({
        ...p,
        protocol: "secret-host",
      })),
    }),
  );
});
