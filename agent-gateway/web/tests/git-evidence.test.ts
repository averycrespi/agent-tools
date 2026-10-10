import assert from "node:assert/strict";
import test from "node:test";
import {
  decodeGitTraffic,
  gitOutcome,
  type GitTraffic,
} from "../src/git-contract.ts";
import { oppositeGitAlias, validGitAliases } from "../src/git-alias.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  time = "2026-09-30T12:00:00.000000000Z";
function record(): GitTraffic {
  return {
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
      commands: 2,
      allowed: true,
      ref_evidence: {
        state: "complete",
        refs: [
          { name: "refs/heads/main", action: "update" },
          { name: "refs/tags/old", action: "delete" },
        ],
      },
    },
    completion: {
      completed_at: time,
      outcome: "outcome_unknown",
      status: 200,
      bytes_sent: 1,
      bytes_received: 1,
      duration_ms: 1,
      transfer_complete: true,
      reported_result: "reported_partial",
      ref_outcomes: ["ok", "ng"],
    },
  };
}
test("request outcomes do not confuse admission, HTTP, and push claims", () => {
  const r = record();
  assert.equal(gitOutcome(decodeGitTraffic(r)), "Reported partial success");
  delete r.completion!.reported_result;
  delete r.completion!.ref_outcomes;
  assert.equal(gitOutcome(decodeGitTraffic(r)), "Unknown");
  for (const status of [401, 403, 500]) {
    r.completion!.status = status;
    assert.equal(gitOutcome(decodeGitTraffic(r)), "Failed");
  }
  r.completion = null;
  assert.equal(gitOutcome(decodeGitTraffic(r)), "Unknown");
  r.admission.allowed = false;
  assert.equal(gitOutcome(decodeGitTraffic(r)), "Denied");
  const read = record();
  read.admission.operation = "read";
  read.admission.commands = 0;
  delete read.admission.ref_evidence;
  delete read.completion!.ref_outcomes;
  delete read.completion!.reported_result;
  read.completion!.outcome = "nonmutation";
  assert.equal(gitOutcome(decodeGitTraffic(read)), "HTTP success");
  read.admission.operation = "push_discovery";
  assert.equal(gitOutcome(decodeGitTraffic(read)), "HTTP success");
});
test("ref evidence is bounded, strict, positional and legacy-safe", () => {
  const r = record();
  assert.deepEqual(decodeGitTraffic(r), r);
  for (const mutate of [
    (x: GitTraffic) => {
      x.admission.ref_evidence!.refs[0]!.name = "refs/heads/bad\nsecret";
    },
    (x: GitTraffic) => {
      x.admission.ref_evidence!.refs[0]!.name = "refs/heads/bad\\name";
    },
    (x: GitTraffic) => {
      x.admission.ref_evidence!.refs[0]!.name =
        "refs/heads/" + "a".repeat(1024);
    },
    (x: GitTraffic) => {
      x.admission.ref_evidence!.refs[1] = x.admission.ref_evidence!.refs[0]!;
    },
    (x: GitTraffic) => {
      x.completion!.ref_outcomes = ["ok"];
    },
    (x: GitTraffic) => {
      x.completion!.reported_result = "reported_success";
    },
    (x: GitTraffic) => {
      x.completion!.transfer_complete = false;
    },
    (x: GitTraffic) => {
      x.admission.commands = 3;
    },
  ]) {
    const x = record();
    mutate(x);
    assert.throws(() => decodeGitTraffic(x));
  }
  r.admission.commands = 128;
  r.admission.ref_evidence!.state = "truncated";
  assert.deepEqual(decodeGitTraffic(r), r);
  delete r.admission.ref_evidence;
  delete r.completion!.ref_outcomes;
  assert.equal(decodeGitTraffic(r).admission.ref_evidence, undefined);
  const oversized = record();
  oversized.admission.ref_evidence!.refs[0]!.name =
    "refs/heads/" + "&".repeat(260);
  assert.throws(() => decodeGitTraffic(oversized));
});
test("opposite explicit alias generation is bidirectional and refuses unsafe URLs", () => {
  assert.equal(
    oppositeGitAlias("https://example.com/team/testing"),
    "https://example.com/team/testing.git",
  );
  assert.equal(
    oppositeGitAlias("https://example.com/team/testing.git"),
    "https://example.com/team/testing",
  );
  for (const url of [
    "",
    "http://example.com/a",
    "https://u:p@example.com/a",
    "https://example.com/",
    "https://example.com/a?x=y",
    "https://example.com/a#b",
    "https://example.com/.git",
  ])
    assert.equal(oppositeGitAlias(url), null);
  assert.equal(
    validGitAliases("https://example.com/a", ["https://example.com/a.git"]),
    true,
  );
  assert.equal(
    validGitAliases("https://example.com/a", ["https://example.com:443/a"]),
    false,
  );
  assert.equal(
    validGitAliases("https://example.com/a", [
      "https://example.com/b",
      "https://example.com/b",
    ]),
    false,
  );
});
