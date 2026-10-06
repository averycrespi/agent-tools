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
  records: [] as Record<string, unknown>[],
};
function page(after: number, count: number, high: number) {
  return {
    ...snapshot,
    after_sequence: String(after),
    next_sequence: String(after + count),
    high_water: String(high),
    retained: high,
    truncated: after + count < high,
    records: Array.from({ length: count }, (_, i) => {
      const protocol = ["mcp", "http", "git"][(after + i) % 3]!;
      return {
        sequence: String(after + i + 1),
        protocol,
        [protocol]: { completion: null },
      };
    }),
  };
}
function response(value: unknown) {
  return new Response(JSON.stringify(value), {
    headers: { "Content-Type": "application/json" },
  });
}

test("empty export uses one protected bodyless read and preserves page coverage", async (t) => {
  const calls: RequestInit[] = [];
  t.mock.method(
    globalThis,
    "fetch",
    async (path: string, options: RequestInit) => {
      assert.equal(path, "/api/v2/history/export");
      calls.push(options);
      return response(snapshot);
    },
  );
  const value = await readHistoryExport(
    "test-session-csrf",
    new AbortController().signal,
  );
  const file = JSON.parse(await value.blob.text());
  assert.deepEqual(file.pages, [snapshot]);
  assert.equal(file.complete_traffic_audit, false);
  assert.equal(file.coverage.atomic_snapshot, false);
  assert.equal(file.coverage.retained_boundary_traversed, true);
  assert.equal(value.records, 0);
  assert.equal(calls.length, 1);
  assert.equal(calls[0]?.method, "GET");
  assert.equal(calls[0]?.body, undefined);
  assert.equal(calls[0]?.credentials, "same-origin");
  assert.equal(calls[0]?.cache, "no-store");
});

test("traverses >256 mixed records with byte-limited pages, finite initial boundary and unchanged raw number tokens", async (t) => {
  const first = page(0, 160, 300),
    last = { ...page(160, 140, 301), truncated: false };
  let calls = 0;
  const progress: number[] = [];
  t.mock.method(globalThis, "fetch", async (path: string) => {
    if (calls++ === 0)
      return new Response(
        JSON.stringify(first).replace(
          '"completion":null',
          '"completion":null,"redacted_arguments":{"precise":900719925474099312345,"scale":1.2300}',
        ),
        { headers: { "Content-Type": "application/json" } },
      );
    assert.equal(
      path,
      "/api/v2/history/export?after_sequence=160&through_sequence=300&limit=256",
    );
    (last.records[0] as Record<string, unknown>).http = {
      completion: { outcome: "succeeded" },
    };
    return response(last);
  });
  const value = await readHistoryExport(
    "csrf",
    new AbortController().signal,
    (p) => progress.push(p.records),
  );
  const text = await value.blob.text(),
    file = JSON.parse(text);
  assert.match(text, /900719925474099312345/);
  assert.match(text, /1\.2300/);
  assert.equal(calls, 2);
  assert.deepEqual(progress, [160, 300]);
  assert.equal(value.records, 300);
  assert.equal(value.newRecords, true);
  assert.equal(file.pages[0].records[0].mcp.completion, null);
  assert.equal(file.pages[1].records[0].http.completion.outcome, "succeeded");
  assert.equal(file.coverage.initial_high_water, "300");
  assert.equal(file.coverage.final_high_water, "301");
  assert.equal(file.coverage.new_records_excluded, true);
  assert.match(file.coverage.completion_changes, /not refreshed/);
});

test("cancels oversized responses instead of retaining unbounded data", async (t) => {
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
    /exceeds.*bound/,
  );
  assert.equal(cancelled, true);
});

for (const [name, change] of [
  ["generation replacement", { generation: "01ARZ3NDEKTSV4RRFFQ69G5FAX" }],
  [
    "installation replacement",
    { installation_id: "01ARZ3NDEKTSV4RRFFQ69G5FAX" },
  ],
  ["pruning", { pruning: "1", retained: 2 }],
  ["stall", { records: [], next_sequence: "2", truncated: true }],
  ["missing range", { records: [], next_sequence: "2", truncated: false }],
  ["duplicate", { records: page(1, 1, 3).records, next_sequence: "2" }],
  [
    "past boundary",
    {
      records: page(3, 1, 4).records,
      next_sequence: "4",
      high_water: "4",
      retained: 4,
    },
  ],
  ["false complete audit", { complete_traffic_audit: true }],
] as const) {
  test(`rejects ${name} without returning a partial file or retrying`, async (t) => {
    let calls = 0;
    t.mock.method(globalThis, "fetch", async () =>
      response(calls++ === 0 ? page(0, 2, 3) : { ...page(2, 1, 3), ...change }),
    );
    await assert.rejects(
      readHistoryExport("csrf", new AbortController().signal),
    );
    assert.equal(calls, 2);
  });
}

test("cancellation after a page stops continuation and publication", async (t) => {
  let calls = 0;
  const controller = new AbortController();
  t.mock.method(globalThis, "fetch", async () => {
    calls++;
    return response(page(0, 1, 2));
  });
  await assert.rejects(
    readHistoryExport("csrf", controller.signal, () => controller.abort()),
  );
  assert.equal(calls, 1);
});

test("cumulative byte ceiling refuses a file despite individually bounded responses", async (t) => {
  let calls = 0;
  t.mock.method(globalThis, "fetch", async () => {
    const value = page(calls++, 1, 100);
    (value.records[0] as Record<string, unknown>).mcp = {
      payload: "x".repeat(850 * 1024),
    };
    value.records[0]!.protocol = "mcp";
    delete (value.records[0] as Record<string, unknown>).http;
    delete (value.records[0] as Record<string, unknown>).git;
    return response(value);
  });
  await assert.rejects(
    readHistoryExport("csrf", new AbortController().signal),
    /64 MiB browser limit/,
  );
  assert.ok(calls > 70 && calls < 80);
});

test("finite page ceiling stops forward-progress traversal without a partial file", async (t) => {
  let calls = 0;
  t.mock.method(globalThis, "fetch", async () =>
    response(page(calls++, 1, 4097)),
  );
  await assert.rejects(
    readHistoryExport("csrf", new AbortController().signal),
    /browser page limit/,
  );
  assert.equal(calls, 4096);
});

test("whole traversal deadline aborts an in-flight read without retry", async (t) => {
  const deadline = new AbortController();
  t.mock.method(AbortSignal, "timeout", () => deadline.signal);
  let calls = 0;
  t.mock.method(
    globalThis,
    "fetch",
    async (_url: string, options: RequestInit) => {
      calls++;
      return await new Promise<Response>((_resolve, reject) => {
        options.signal!.addEventListener(
          "abort",
          () => reject(options.signal!.reason),
          { once: true },
        );
        deadline.abort(new DOMException("Timeout", "TimeoutError"));
      });
    },
  );
  await assert.rejects(
    readHistoryExport("csrf", new AbortController().signal),
    { name: "TimeoutError" },
  );
  assert.equal(calls, 1);
});

test("failed continuation and session loss are not retried", async (t) => {
  let calls = 0;
  t.mock.method(globalThis, "fetch", async () =>
    calls++ === 0
      ? response(page(0, 1, 2))
      : new Response(null, { status: 401 }),
  );
  let loss = 0;
  await assert.rejects(
    readHistoryExport(
      "csrf",
      new AbortController().signal,
      undefined,
      async (r) => {
        if (r.status === 401) {
          loss++;
          return true;
        }
        return false;
      },
    ),
  );
  assert.equal(calls, 2);
  assert.equal(loss, 1);
});
