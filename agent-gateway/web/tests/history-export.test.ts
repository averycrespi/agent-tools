import assert from "node:assert/strict";
import test from "node:test";
import { readHistoryExport } from "../src/history-export.ts";

const snapshot = {
  format: 1,
  generation: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
  installation_id: "01ARZ3NDEKTSV4RRFFQ69G5FAW",
  captured_at: "2026-10-05T00:00:00Z",
  high_water: "0",
  pruning: "0",
  after_sequence: "0",
  next_sequence: "0",
  retained: 0,
  truncated: false,
  complete_traffic_audit: false,
  absence: "Missing observations do not prove nonexecution.",
  records: [],
};

test("history export makes one session-bound bodyless read and retains coverage", async (t) => {
  const calls: RequestInit[] = [];
  t.mock.method(
    globalThis,
    "fetch",
    async (path: string, options: RequestInit) => {
      assert.equal(path, "/api/v2/history/export");
      calls.push(options);
      return new Response(JSON.stringify(snapshot), {
        headers: { "Content-Type": "application/json" },
      });
    },
  );
  const value = await readHistoryExport(
    "test-session-csrf",
    new AbortController().signal,
  );
  assert.deepEqual(JSON.parse(value.json), snapshot);
  assert.equal(value.records, 0);
  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.method, "GET");
  assert.equal(calls[0]?.body, undefined);
  assert.equal(calls[0]?.credentials, "same-origin");
  assert.equal(calls[0]?.cache, "no-store");
});

test("history export cancels oversized response instead of retaining unbounded data", async (t) => {
  let cancelled = false;
  t.mock.method(
    globalThis,
    "fetch",
    async () =>
      new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new Uint8Array(901 * 1024));
          },
          cancel() {
            cancelled = true;
          },
        }),
        { headers: { "Content-Type": "application/json" } },
      ),
  );
  await assert.rejects(
    readHistoryExport("csrf", new AbortController().signal),
    /exceeds bound/,
  );
  assert.equal(cancelled, true);
});

test("history export rejects unavailable or falsely complete snapshots without replay", async (t) => {
  let calls = 0;
  const responses = [
    new Response("unavailable", { status: 503 }),
    new Response(
      JSON.stringify({ ...snapshot, complete_traffic_audit: true }),
      { headers: { "Content-Type": "application/json" } },
    ),
  ];
  t.mock.method(globalThis, "fetch", async () => {
    calls++;
    return responses.shift()!;
  });
  await assert.rejects(readHistoryExport("csrf", new AbortController().signal));
  await assert.rejects(readHistoryExport("csrf", new AbortController().signal));
  assert.equal(calls, 2);
});
