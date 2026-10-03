import assert from "node:assert/strict";
import test from "node:test";
import {
  decodeGitRoutingProfile,
  decodeGitRoutingResponse,
  normalizeGitOrigins,
} from "../src/git-routing-contract.ts";
import { resolveFragment } from "../src/location.ts";

const profile = {
  origins: ["https://github.com:443"],
  revision: "2",
  active: true,
};
test("Git routing accepts active and inactive profiles with exact response revisions", async () => {
  for (const active of [true, false]) {
    const value = { ...profile, active };
    assert.deepEqual(decodeGitRoutingProfile(value), value);
    assert.deepEqual(
      await decodeGitRoutingResponse(
        new Response(JSON.stringify(value), {
          headers: {
            "Content-Type": "application/json",
            ETag: '"git-profile-routing-2"',
          },
        }),
      ),
      value,
    );
  }
  await assert.rejects(
    decodeGitRoutingResponse(
      new Response(JSON.stringify(profile), {
        headers: {
          "Content-Type": "application/json",
          ETag: '"git-profile-routing-1"',
        },
      }),
    ),
  );
  assert.deepEqual(
    decodeGitRoutingProfile({ ...profile, origins: [] }).origins,
    [],
  );
});
test("Git routing rejects malformed, unbounded and noncanonical response facts", () => {
  for (const value of [
    { ...profile, active: "true" },
    { origins: [], revision: "1" },
    { ...profile, revision: "0" },
    { ...profile, revision: "9223372036854775808" },
    { ...profile, extra: true },
    { ...profile, origins: null },
    { ...profile, origins: ["https://github.com"] },
    {
      ...profile,
      origins: ["https://github.com:443", "https://example.com:443"],
    },
    { ...profile, origins: Array(257).fill("https://github.com:443") },
  ])
    assert.throws(() => decodeGitRoutingProfile(value));
});
test("Git origin drafts normalize only exact HTTPS origins and reject duplicates", () => {
  assert.deepEqual(
    normalizeGitOrigins(["https://GitHub.com", "https://example.com:8443"]),
    ["https://example.com:8443", "https://github.com:443"],
  );
  assert.deepEqual(normalizeGitOrigins([]), []);
  for (const value of [
    "",
    "http://github.com",
    "https://github.com/",
    "https://github.com/repo",
    "https://github.com?",
    "https://github.com#",
    "https://user:secret@github.com",
    "https://*.github.com",
    "https://github.com:0",
    "https://github.com:65536",
    "https://github.com\\evil",
    " https://github.com",
  ])
    assert.throws(() => normalizeGitOrigins([value]), value);
  assert.throws(() =>
    normalizeGitOrigins(["https://github.com", "https://GITHUB.com:443"]),
  );
});
test("Git Routing is a singleton location without arbitrary queries or suffixes", () => {
  assert.equal(
    resolveFragment("#/git/routing", true).canonicalFragment,
    "#/git/routing",
  );
  for (const fragment of [
    "#/git/routing/new",
    "#/git/routing?origins=github.com",
    "#/git/routing/",
  ])
    assert.equal(resolveFragment(fragment, true).invalid, true);
});
