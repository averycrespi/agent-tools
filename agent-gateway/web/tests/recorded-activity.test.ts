import assert from "node:assert/strict";
import test from "node:test";
import {
  activityTotals,
  decodeRecordedActivity,
  type RecordedActivity,
} from "../src/recorded-activity.ts";
import { activityFixture } from "./recorded-activity-fixture.ts";

test("activity decoder preserves observed coverage and unavailable nonnumbers", () => {
  for (const state of ["complete", "partial", "unavailable"] as const) {
    const value = decodeRecordedActivity(activityFixture(state));
    assert.equal(value.coverage, state);
    assert.equal(
      activityTotals(value.buckets, "mcp")?.admissions,
      state === "unavailable" ? undefined : 0n,
    );
  }
  const reset = activityFixture("unavailable");
  reset.epoch_reason = "clock_reset";
  reset.collection_start = "2026-09-30T12:16:00Z";
  assert.equal(decodeRecordedActivity(reset).epoch_reason, "clock_reset");
});

test("activity coverage preserves sub-millisecond collection boundaries", () => {
  const value = activityFixture();
  value.collection_start = "2026-09-30T12:00:00.000000001Z";
  value.coverage = "partial";
  value.buckets[0]!.coverage = "partial";
  value.buckets[0]!.observed_start = value.collection_start;
  const decoded = decodeRecordedActivity(value);
  assert.equal(decoded.buckets[0]!.coverage, "partial");
  assert.equal(activityTotals(decoded.buckets, "mcp")?.admissions, 0n);
});

test("activity minute bounds remain exact before the Unix epoch", () => {
  const value = activityFixture();
  const shift =
    Date.parse("1969-12-31T23:44:00Z") - Date.parse(value.window_start);
  const move = (text: string) =>
    new Date(Date.parse(text) + shift).toISOString();
  value.collection_start = move(value.collection_start);
  value.as_of = move(value.as_of);
  value.window_start = move(value.window_start);
  value.window_end = move(value.window_end);
  for (const bucket of value.buckets) {
    bucket.start = move(bucket.start);
    bucket.end = move(bucket.end);
    bucket.observed_start = move(bucket.observed_start!);
  }
  assert.equal(decodeRecordedActivity(value).coverage, "complete");
});

test("activity decoder rejects open schemas and contradictory dimensions or bounds", () => {
  const mutations: ((value: RecordedActivity) => void)[] = [
    (v) => Object.assign(v, { secret: "never render" }),
    (v) => {
      v.bucket_seconds = 30 as 60;
    },
    (v) => {
      v.epoch = "arbitrary-label";
    },
    (v) => {
      v.buckets.pop();
    },
    (v) => {
      v.coverage = "partial";
    },
    (v) => {
      v.buckets[0]!.end = v.buckets[0]!.start;
    },
    (v) => {
      v.buckets[0]!.counts = null;
    },
    (v) => {
      v.buckets[0]!.observed_start = null;
    },
    (v) => {
      v.buckets[0]!.counts!.mcp.admissions.allow = -1;
    },
    (v) => {
      v.buckets[0]!.counts!.mcp.admissions.allow = 2 ** 53;
    },
    (v) => {
      v.buckets[0]!.counts!.mcp.admissions.allow = 0.5;
    },
    (v) => {
      Object.assign(v.buckets[0]!.counts!.mcp.admissions, { destination: 1 });
    },
    (v) => {
      v.window_end = "2026-09-30T12:15:01Z";
    },
  ];
  for (const mutate of mutations) {
    const value = activityFixture();
    mutate(value);
    assert.throws(() => decodeRecordedActivity(value));
  }
  const unavailable = activityFixture("unavailable");
  unavailable.buckets[0]!.counts = activityFixture().buckets[0]!.counts;
  assert.throws(() => decodeRecordedActivity(unavailable));
});

test("activity counts keep independent populations and integer-safe large sums", () => {
  const value = activityFixture();
  value.buckets[0]!.counts!.mcp.admissions.allow = Number.MAX_SAFE_INTEGER;
  value.buckets[1]!.counts!.mcp.admissions.deny = Number.MAX_SAFE_INTEGER;
  value.buckets[1]!.counts!.mcp.completions.downstream_failure = 3;
  value.buckets[1]!.counts!.mcp.completions.outcome_unknown = 7;
  const totals = activityTotals(decodeRecordedActivity(value).buckets, "mcp")!;
  assert.equal(totals.admissions, 2n * BigInt(Number.MAX_SAFE_INTEGER));
  assert.equal(totals.refusals, BigInt(Number.MAX_SAFE_INTEGER));
  assert.equal(totals.failures, 3n);
  assert.equal(totals.unknown, 7n);
  assert.equal(totals.succeeded, 0n);
});
