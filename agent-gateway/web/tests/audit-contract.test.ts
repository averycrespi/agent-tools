import assert from "node:assert/strict";
import { parseFragment, serializeLocation } from "../src/location.ts";
import test from "node:test";
import { readFileSync } from "node:fs";
import {
  auditActions,
  auditTargets,
  parseAuditJSON,
  decodeAuditItem,
  decodeAuditPage,
  validAuditQuery,
} from "../src/audit-contract.ts";
test("audit detail carries only a validated filter return query, never traversal state", () => {
  const id = "00000000000000000000000001";
  const fragment = `#/audit-log/${id}?filter_outcome=failed`;
  const location = parseFragment(fragment)!;
  assert.equal(serializeLocation(location), fragment);
  assert.equal(
    serializeLocation({ ...location, segments: ["audit"] }),
    "#/audit-log?filter_outcome=failed",
  );
  assert.equal(
    serializeLocation({
      ...parseFragment(`#/audit-log/${id}`)!,
      segments: ["audit"],
    }),
    "#/audit-log",
  );
  for (const query of [
    "cursor=opaque",
    "generation=" + "a".repeat(64),
    "filter_target_id=bad",
    "filter_from=2026-01-01T00%3A00%3A00.000000000Z",
  ])
    assert.equal(parseFragment(`#/audit-log/${id}?${query}`), undefined);
});
test("audit JSON rejects duplicate members, including escaped keys", () => {
  for (const source of [
    '{"a":1,"a":2}',
    '{"a":{"b":1,"b":2}}',
    '{"a":1,"\\u0061":2}',
  ])
    assert.throws(() => parseAuditJSON(source));
  const value = {
    note: 'quotes " and colons : and \\ escapes',
    nested: [{ id: "x:y" }],
  };
  assert.deepEqual(parseAuditJSON(JSON.stringify(value)), value);
});
test("browser audit vocabulary stays aligned with authoritative Go contract", () => {
  const source = readFileSync(
    new URL("../../internal/contract/audit.go", import.meta.url),
    "utf8",
  );
  const actions = Object.fromEntries(
    [...source.matchAll(/"([a-z_]+)":\s*\{([^}]+)\}/g)].map(
      ([, key, values]) => [
        key,
        [...values!.matchAll(/"([a-z_]+)"/g)].map((match) => match[1]),
      ],
    ),
  );
  assert.deepEqual(auditActions, actions);
  const targets = source.match(
    /func AuditTargetTypes\(\) \[\]string \{\s*return \[\]string\{([^}]+)\}/,
  );
  assert.ok(targets);
  assert.deepEqual(
    [...auditTargets].sort(),
    [...targets[1]!.matchAll(/"([a-z_]+)"/g)].map((match) => match[1]).sort(),
  );
  for (const [file, type, member] of [
    ["server_states.go", "PublicReason", "reason"],
    ["problems.go", "ProblemCode", "problem"],
  ]) {
    const code = readFileSync(
      new URL(`../../internal/contract/${file}`, import.meta.url),
      "utf8",
    );
    const values = [
      ...code.matchAll(new RegExp(`${type} = "([a-z_]+)"`, "g")),
    ].map((match) => match[1]);
    assert.ok(values.length > 20);
    for (const value of values)
      assert.doesNotThrow(() =>
        decodeAuditItem({
          event: {
            ...event,
            detail: { reason: null, problem: null, [member!]: value },
          },
          history,
        }),
      );
  }
});
const id = "00000000000000000000000001";
const event = {
  id,
  sequence: "2",
  timestamp: "2026-09-05T00:00:00.000000001Z",
  category: "server",
  action: "reconcile",
  phase: "outcome",
  outcome: "unknown",
  actor: { type: "system", credential: null },
  initiator: { id, fingerprint: "0123456789abcdef" },
  correlation_id: id,
  target: { type: "server", id },
};
const history = {
  generation: "a".repeat(64),
  oldest_retained: {
    id,
    sequence: "1",
    timestamp: "2026-09-04T00:00:00.000000000Z",
  },
  pruned: true,
};
test("audit current recognition stays separate from evidence and validates complete unique coverage", () => {
  const envelope = {
    items: [event],
    next_cursor: null,
    history,
    target_recognition: [
      { target: event.target, display_name: "Renamed server" },
    ],
  };
  assert.equal(
    decodeAuditPage(envelope).items[0]!.currentTargetName,
    "Renamed server",
  );
  assert.deepEqual(decodeAuditPage(envelope).items[0]!.target, event.target);
  assert.equal(
    decodeAuditPage({
      ...envelope,
      target_recognition: [{ target: event.target, display_name: null }],
    }).items[0]!.currentTargetName,
    undefined,
  );
  for (const target_recognition of [
    [],
    [...envelope.target_recognition, ...envelope.target_recognition],
    [
      {
        target: { ...event.target, id: "00000000000000000000000002" },
        display_name: "Unrelated",
      },
    ],
    [{ target: event.target, display_name: "x".repeat(257) }],
  ])
    assert.throws(() => decodeAuditPage({ ...envelope, target_recognition }));
  assert.equal("currentTargetName" in event, false);
});

test("audit strict projections preserve attribution, uncertain outcomes and retention", () => {
  const page = { items: [event], next_cursor: "opaque", history };
  assert.deepEqual(decodeAuditPage(page), page);
  const item = {
    event: { ...event, detail: { reason: "interrupted", problem: null } },
    history,
  };
  assert.deepEqual(decodeAuditItem(item), item);
  for (const bad of [
    { ...page, secret: "must not render" },
    { ...page, history: { ...history, generation: "bad" } },
    { ...page, items: null },
    { ...page, items: [event, event] },
    {
      ...page,
      items: [{ ...event, actor: { type: "operator", credential: null } }],
    },
    { ...page, items: [{ ...event, sequence: 2 }] },
    { ...page, items: [{ ...event, outcome: "success" }] },
    {
      ...page,
      items: [{ ...event, timestamp: "2026-02-30T00:00:00.000000000Z" }],
    },
    { ...page, next_cursor: "a".repeat(2049) },
  ])
    assert.throws(() => decodeAuditPage(bad));
  for (const detail of [
    { reason: "raw-secret", problem: null },
    { reason: null },
    { reason: null, problem: "raw_error" },
    { reason: null, problem: null, raw: "secret" },
  ])
    assert.throws(() =>
      decodeAuditItem({ ...item, event: { ...event, detail } }),
    );
  assert.throws(() =>
    decodeAuditItem({
      ...item,
      event: {
        ...event,
        phase: "attempt",
        outcome: "pending",
        detail: { reason: "interrupted", problem: null },
      },
    }),
  );
});
test("Git audit events and filters retain the closed shared audit contract", () => {
  for (const [category, targetType] of [
    ["git_repository", "git_repository"],
    ["git_grant", "git_grant"],
    ["git_credential", "git_credential"],
    ["git_profile", "installation"],
  ]) {
    for (const action of auditActions[category!]!) {
      const gitEvent = {
        ...event,
        category,
        action,
        phase: "outcome",
        outcome: "succeeded",
        target: { type: targetType, id },
      };
      const page = { items: [gitEvent], next_cursor: null, history };
      assert.deepEqual(decodeAuditPage(page), page);
      assert.equal(
        validAuditQuery({
          filter_category: category!,
          filter_action: action,
          filter_target_type: targetType!,
        }),
        true,
      );
      assert.throws(() =>
        decodeAuditPage({
          ...page,
          items: [{ ...gitEvent, action: "dispatch" }],
        }),
      );
      assert.throws(() =>
        decodeAuditPage({
          ...page,
          items: [{ ...gitEvent, secret: "private" }],
        }),
      );
    }
  }
});

test("audit filters validate the exact API grammar including nanosecond time boundaries", () => {
  const query = {
    filter_actor_type: "system",
    filter_credential_id: id,
    filter_category: "server",
    filter_action: "reconcile",
    filter_target_type: "server",
    filter_target_id: id,
    filter_outcome: "unknown",
    filter_correlation_id: id,
    filter_from: "2026-09-05T00:00:00.000000000Z",
    filter_until: "2026-09-05T00:00:00.000000001Z",
  };
  assert.equal(validAuditQuery(query), true);
  for (const bad of [
    { cursor: "opaque" },
    { generation: "a".repeat(64) },
    { filter_actor_type: "human" },
    { filter_credential_id: "bad" },
    { filter_from: query.filter_from },
    { ...query, filter_until: query.filter_from },
    { ...query, filter_until: "2027-09-07T00:00:00.000000000Z" },
    { ...query, filter_category: "principal" },
    { filter_anything: "x" },
    { filter_action: "" },
  ])
    assert.equal(validAuditQuery(bad), false);
});
