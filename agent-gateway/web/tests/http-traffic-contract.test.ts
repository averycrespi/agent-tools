import assert from "node:assert/strict";
import test from "node:test";
import {
  decodeTrafficItem,
  decodeTrafficPage,
  validHTTPTrafficQuery,
} from "../src/http-traffic-contract.ts";
import { parseFragment, serializeLocation } from "../src/location.ts";
const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
const at = "2026-09-21T00:00:00.000000000Z";
function item() {
  return {
    admission: {
      id,
      admitted_at: at,
      principal: { id, revision: 1 },
      agent_credential: { id, revision: 1 },
      credential_fingerprint: "0123456789abcdef",
      class: "evaluated",
      default: "allow",
      target: {
        host: "example.com",
        port: 443,
        scheme: "https",
        method: "GET",
      },
      evaluated_at: at,
      decision: {
        version: 1,
        principal: { id, revision: 1 },
        policy_revision: 1,
        default_revision: 1,
        transport: "request",
        allowed: true,
        reason: "principal_default",
      },
      grants: [],
      material: null,
    },
    completion: null,
  };
}
test("HTTP history deep links retain closed exact filters separately from MCP", () => {
  const location = {
    destination: "http-traffic",
    segments: ["http-traffic", id],
    query: { filter_destination: "example.com", filter_type: "request" },
  } as const;
  const parsed = parseFragment(
    serializeLocation({ ...location, segments: [...location.segments] }),
  );
  assert.deepEqual({ ...parsed, query: { ...parsed?.query } }, location);
  for (const query of [
    { filter_destination: "*.example.com" },
    { filter_destination: "https://example.com/path?token=1" },
    { filter_type: "mcp" },
    { filter_outcome: "complete" },
    { filter_principal_id: "name" },
    { unknown: "value" },
  ])
    assert.equal(validHTTPTrafficQuery(query), false);
  assert.equal(
    parseFragment("#/http/traffic?filter_destination=a&filter_destination=b"),
    undefined,
  );
});
test("HTTP history rejects unknown fields and contradictory evidence", () => {
  assert.deepEqual(decodeTrafficItem(item()), item());
  const wrong = { ...item(), secret: "do not render" };
  assert.throws(() => decodeTrafficItem(wrong));
  const target = item();
  Object.assign(target.admission.target, { path: "/private" });
  assert.throws(() => decodeTrafficItem(target));
  const bad = item();
  bad.admission.default = "block";
  assert.throws(() => decodeTrafficItem(bad));
  const policy = item();
  Object.assign(policy.admission.decision, { grant: { id, revision: 1 } });
  assert.throws(() => decodeTrafficItem(policy));
  const completed = {
    ...item(),
    completion: {
      completed_at: at,
      outcome: "succeeded",
      status: 200,
      bytes_sent: 0,
      bytes_received: 1,
      duration_ms: 0,
    },
  };
  assert.deepEqual(decodeTrafficItem(completed), completed);
  Object.assign(completed.completion, { upstream_error: "private" });
  assert.throws(() => decodeTrafficItem(completed));
});
test("HTTP summary pages remain bounded without policy snapshots", () => {
  const summary = {
    id,
    admitted_at: at,
    principal_id: id,
    target: item().admission.target,
    type: "request",
    decision: "allow",
    outcome: "outcome_unknown",
  };
  assert.equal(
    decodeTrafficPage({ items: [summary], next_cursor: null }).items.length,
    1,
  );
  assert.throws(() =>
    decodeTrafficPage({ items: [summary, summary], next_cursor: null }),
  );
  assert.throws(() =>
    decodeTrafficPage({
      items: [{ ...summary, grants: [] }],
      next_cursor: null,
    }),
  );
  assert.throws(() =>
    decodeTrafficPage({ items: [], next_cursor: "x".repeat(513) }),
  );
});
