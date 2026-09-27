import assert from "node:assert/strict";
import test from "node:test";
import { validOAuthOrigin } from "../src/oauth-origin.ts";

test("OAuth origins follow remote.ParseOrigin without URL normalization", () => {
  for (const value of [
    "https://example.com",
    "https://localhost",
    "https://xn--bcher-kva.example",
    "https://example.com:8443",
    "https://example.com:08443",
    "https://example.com:65535",
    "https://123",
    "https://127.01.0.1",
  ])
    assert.equal(validOAuthOrigin(value), true, value);
  for (const value of [
    "",
    "http://example.com",
    "https://127.0.0.1",
    "https://[::1]",
    "https://Example.com",
    "HTTPS://example.com",
    "https://example.com.",
    "https://example.com:443",
    "https://example.com:0443",
    "https://example.com:0",
    "https://example.com:65536",
    "https://example.com:",
    "https://example.com/",
    "https://example.com?",
    "https://example.com#",
    "https://a@b.example",
    "https://a_b.example",
    "https://-a.example",
    "https://a-.example",
    "https://a..example",
    "https://bücher.example",
    " https://example.com",
    "https://example.com\n",
    `https://${"a".repeat(64)}.example`,
    `https://${Array(5).fill("a".repeat(63)).join(".")}`,
  ])
    assert.equal(validOAuthOrigin(value), false, value);
});
