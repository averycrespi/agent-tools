import assert from "node:assert/strict";
import test from "node:test";
import {
  decodeGitResource,
  decodeGitTraffic,
  decodeGitTrafficPage,
  gitFacts,
} from "../src/git-contract.ts";
const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  time = "2026-09-30T12:00:00.000000000Z";
const record = {
  admission: {
    id,
    admitted_at: time,
    evaluated_at: time,
    principal: { id, revision: "1" },
    agent_credential: { id, revision: "1" },
    repository: { id, revision: "1" },
    alias_revision: "1",
    profile_revision: "1",
    authorization_revision: "1",
    operation: "push",
    commands: 1,
    allowed: true,
  },
  completion: null,
};
test("Git traffic keeps HTTP completion separate from upstream reports", () => {
  assert.equal(gitFacts(decodeGitTraffic(record)).report, "Unknown");
  const complete = {
    ...record,
    completion: {
      completed_at: time,
      outcome: "outcome_unknown",
      bytes_sent: 10,
      bytes_received: 20,
      duration_ms: 1,
      transfer_complete: true,
      status: 200,
    },
  };
  assert.deepEqual(gitFacts(decodeGitTraffic(complete)), {
    admission: "Allowed",
    transport: "Complete",
    report: "Unknown",
  });
  assert.equal(
    gitFacts(
      decodeGitTraffic({
        ...complete,
        completion: {
          ...complete.completion,
          reported_result: "reported_partial",
        },
      }),
    ).report,
    "Reported partial success",
  );
  assert.throws(() =>
    decodeGitTraffic({
      ...complete,
      completion: {
        ...complete.completion,
        transfer_complete: false,
        reported_result: "reported_success",
      },
    }),
  );
  assert.throws(() =>
    decodeGitTraffic({
      ...record,
      admission: { ...record.admission, ref: "secret" },
    }),
  );
  assert.throws(() =>
    decodeGitTraffic({
      ...complete,
      completion: { ...complete.completion, message: "secret" },
    }),
  );
});
test("Git traffic pages use bounded cursors without fabricated totals", () => {
  const page = decodeGitTrafficPage({
    items: [record],
    next_cursor: "cursor_1",
  });
  assert.equal(page.items[0]?.admission.id, id);
  assert.equal(page.nextCursor, "cursor_1");
  assert.equal(page.totalCount, undefined);
  assert.equal(
    decodeGitTrafficPage({ items: [], next_cursor: null }).items.length,
    0,
  );
  for (const value of [
    { items: [record, record], next_cursor: null },
    { items: [], next_cursor: "cursor" },
    { items: [record], next_cursor: "" },
    { items: [record], next_cursor: "secret/invalid" },
    { items: [record], next_cursor: null, total_count: 1 },
  ])
    assert.throws(() => decodeGitTrafficPage(value));
});
test("Git browser rejects write-only grant representations", () => {
  const grant = {
    id,
    revision: "1",
    created_at: time,
    updated_at: time,
    principal_id: id,
    repository_id: id,
    description: null,
    expires_at: null,
    state: "active",
    policy: {
      version: 1,
      read: true,
      refs: [
        {
          ref: { kind: "exact", value: "refs/heads/main" },
          actions: ["update"],
        },
      ],
    },
  };
  assert.equal(decodeGitResource("grants", grant).policy?.read, true);
  assert.throws(() =>
    decodeGitResource("grants", {
      ...grant,
      policy: { ...grant.policy, read: false },
    }),
  );
});
