import test from "node:test";
import assert from "node:assert/strict";
import { matrixSearch, overviewSearch, parseState, stateSearch, withBucket } from "../assets/state.js";

test("URL state round trips only non-sensitive navigation state", () => {
  const state = parseState("?range=7d&bucket=week&timezone=America%2FLos_Angeles&cwd=%2Fwork&cwd=%2Fhome&session=abc&from=1&to=2", "UTC");
  assert.equal(state.range, "7d");
  assert.equal(state.timezone, "America/Los_Angeles");
  assert.deepEqual(state.cwds, ["/work", "/home"]);
  assert.deepEqual(parseState(stateSearch(state), "UTC"), state);
  assert.equal(stateSearch({ ...state, transcript: "private" }).includes("private"), false);
});

test("matrix sort round trips and reaches the matrix query", () => {
  const state = parseState("?sort=cost&direction=asc", "UTC");
  assert.equal(state.sort, "cost");
  assert.deepEqual(parseState(stateSearch(state), "UTC"), state);
  const query = new URLSearchParams(matrixSearch(state, { buckets: [{ start_unix: 1, end_unix: 2 }] }));
  assert.equal(query.get("sort"), "cost");
  assert.equal(query.get("direction"), "asc");
  assert.equal(parseState("?sort=records", "UTC").sort, "start");
  assert.equal(new URLSearchParams(matrixSearch(parseState("", "UTC"), { buckets: [] })).get("sort"), null);
});

test("cwd filters repeat in matrix queries and are capped", () => {
  const state = parseState(`?${Array.from({ length: 20 }, (_, i) => `cwd=%2Fp${i}`).join("&")}`, "UTC");
  assert.equal(state.cwds.length, 16);
  const query = new URLSearchParams(matrixSearch({ ...state, cwds: ["/a", "/b"] }, { buckets: [{ start_unix: 1, end_unix: 2 }] }));
  assert.deepEqual(query.getAll("cwd"), ["/a", "/b"]);
});

test("invalid URL choices fall back and bucket selection clears session", () => {
  const state = parseState("?range=forever&bucket=hour&untimed=true", "UTC");
  assert.equal(state.range, "30d");
  assert.equal(state.bucket, "auto");
  const selected = withBucket({ ...state, session: "old" }, { start_unix: 10, end_unix: 20 });
  assert.equal(selected.session, "");
  assert.equal(selected.untimed, false);
  assert.match(matrixSearch(selected), /from=10/);
});

test("explicit dates are represented in local state and overview requests", () => {
  const state = parseState("?date_from=2026-01-01&date_to=2026-02-01", "UTC");
  const query = new URLSearchParams(overviewSearch(state));
  assert.equal(query.get("from"), "2026-01-01");
  assert.equal(query.get("to"), "2026-02-01");
});

test("matrix query derives its half-open range from overview", () => {
  const query = matrixSearch(parseState("", "UTC"), { buckets: [{ start_unix: 10 }, { end_unix: 30 }] });
  assert.equal(new URLSearchParams(query).get("from"), "10");
  assert.equal(new URLSearchParams(query).get("to"), "30");
});
