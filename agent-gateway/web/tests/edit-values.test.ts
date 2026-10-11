import assert from "node:assert/strict";
import test from "node:test";
import {
  localExpiry,
  submittedExpiry,
  normalizedGitLocator,
} from "../src/edit-values.ts";

test("edit expiry preserves precise baseline through unchanged and reverted controls", () => {
  const baseline = "2030-10-01T12:00:37.123456789Z";
  assert.equal(submittedExpiry(localExpiry(baseline), baseline), baseline);
  assert.notEqual(submittedExpiry("2031-01-01T12:00", baseline), baseline);
  assert.equal(submittedExpiry("", baseline), null);
  assert.equal(submittedExpiry("", null), null);
  assert.equal(submittedExpiry("invalid", baseline), "invalid");
});

test("Git locator comparison normalizes only permitted host and port equivalence", () => {
  assert.equal(
    normalizedGitLocator("https://EXAMPLE.invalid/team/repo"),
    "https://example.invalid:443/team/repo",
  );
  assert.equal(
    normalizedGitLocator("https://example.invalid:443/team/repo"),
    "https://example.invalid:443/team/repo",
  );
  for (const invalid of [
    "https://example.invalid/team/../repo",
    "https://example.invalid/team/%72epo",
    "https://example.invalid/team//repo",
    "https://example.invalid:0/team/repo",
    "https://user@example.invalid/team/repo",
  ])
    assert.equal(normalizedGitLocator(invalid), invalid);
  assert.notEqual(
    normalizedGitLocator("https://example.invalid/team/repo.git"),
    normalizedGitLocator("https://example.invalid/team/repo"),
  );
  assert.notEqual(
    normalizedGitLocator("https://example.invalid/team/Repo"),
    normalizedGitLocator("https://example.invalid/team/repo"),
  );
});
