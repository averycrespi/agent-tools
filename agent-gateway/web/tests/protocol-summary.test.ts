import assert from "node:assert/strict";
import test from "node:test";
import {
  decodeProtocolActivity,
  decodeInventoryTotal,
  activityHref,
  validActivityRange,
} from "../src/protocol-summary.ts";
import { parseFragment, serializeLocation } from "../src/location.ts";
import { protocolSummaryFixture } from "./protocol-summary-fixture.ts";

test("protocol summaries preserve explicit outcomes, unavailable and partial coverage", () => {
  const fixture = protocolSummaryFixture();
  fixture.counts!.git = {
    ...fixture.counts!.git,
    total: 5,
    reported_success: 1,
    reported_partial: 1,
    denied: 1,
    unknown: 1,
    incomplete: 1,
  };
  assert.deepEqual(decodeProtocolActivity(fixture), fixture);
  fixture.coverage = "partial";
  assert.equal(decodeProtocolActivity(fixture).coverage, "partial");
  fixture.coverage = "unavailable";
  assert.throws(() => decodeProtocolActivity(fixture));
  fixture.counts = null;
  assert.equal(decodeProtocolActivity(fixture).counts, null);
  for (const change of [
    (v: any) => (v.counts.mcp.total = 1),
    (v: any) => (v.counts.http.failed = -1),
    (v: any) => (v.counts.http.extra = 0),
    (v: any) => (v.window = "7d"),
    (v: any) => (v.until = v.from),
    (v: any) => (v.coverage = "complete"),
  ]) {
    const v = protocolSummaryFixture();
    change(v);
    assert.throws(() => decodeProtocolActivity(v));
  }
});
test("inventory totals do not count loaded rows or accept incomplete envelopes", () => {
  assert.equal(
    decodeInventoryTotal({
      items: [{}],
      offset: 0,
      total_count: 150,
      next_cursor: "next",
    }),
    150,
  );
  assert.equal(
    decodeInventoryTotal({
      items: [],
      offset: 0,
      total_count: 0,
      next_cursor: null,
    }),
    0,
  );
  for (const page of [
    { items: [{}], offset: 0, total_count: 150, next_cursor: null },
    { items: [], offset: 0, total_count: 2, next_cursor: "next" },
    { items: [], offset: 1, total_count: 0, next_cursor: null },
    { items: [], offset: 0, total_count: null, next_cursor: null },
  ])
    assert.throws(() => decodeInventoryTotal(page));
});
test("shared window links preserve admission bounds through all three history routes", () => {
  for (const window of ["15m", "1h", "24h"] as const)
    for (const protocol of ["http", "git", "mcp"] as const) {
      const summary = protocolSummaryFixture(window),
        href = activityHref(protocol, summary),
        parsed = parseFragment(href);
      assert.ok(parsed);
      assert.equal(parsed.query.filter_from, summary.from);
      assert.equal(parsed.query.filter_until, summary.until);
      if (protocol === "http")
        assert.equal(parsed.query.filter_type, "request");
      assert.ok(parseFragment(serializeLocation(parsed)));
    }
  const s = protocolSummaryFixture();
  assert.equal(
    validActivityRange({
      filter_from: s.from,
      filter_until: s.from.replace("000000000Z", "000000001Z"),
    }),
    true,
  );
  assert.equal(
    validActivityRange({
      filter_from: protocolSummaryFixture("24h").from,
      filter_until: s.until.replace("000000000Z", "000000001Z"),
    }),
    false,
  );
  const mismatched = protocolSummaryFixture("15m");
  mismatched.window = "1h";
  assert.throws(() => decodeProtocolActivity(mismatched));
  for (const query of [
    { filter_from: s.from },
    { filter_until: s.until },
    { filter_from: s.until, filter_until: s.from },
    { filter_from: "2026-02-30T00:00:00.000000000Z", filter_until: s.until },
  ])
    assert.equal(validActivityRange(query), false);
  for (const path of ["http/traffic", "git/traffic", "mcp/invocations"])
    assert.equal(
      parseFragment(`#/${path}?filter_from=${encodeURIComponent(s.from)}`),
      undefined,
    );
});
