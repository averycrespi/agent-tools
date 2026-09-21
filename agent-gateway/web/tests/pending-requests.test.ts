import assert from "node:assert/strict";
import test from "node:test";
import { PendingRequestsController } from "../src/pending-requests.ts";
import { SessionClient } from "../src/session.ts";
import { ViewCoordinator } from "../src/view.ts";

async function eventually(predicate: () => boolean) {
  const deadline = performance.now() + 3000;
  while (!predicate()) {
    assert.ok(performance.now() < deadline, "pending count did not settle");
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

test("global pending count follows shared lifecycle and fences session reads", async (t) => {
  const session = new SessionClient(async (input, init) => {
    if (String(input).endsWith("/current"))
      return init?.method === "DELETE"
        ? new Response(null, { status: 204 })
        : new Response(
            JSON.stringify({
              status: 401,
              code: "authentication_required",
              title: "Authentication is required.",
            }),
            {
              status: 401,
              headers: { "Content-Type": "application/problem+json" },
            },
          );
    return new Response(
      JSON.stringify({
        csrf_token: "A".repeat(43),
        idle_expires_at: "2026-09-20T18:30:00Z",
        absolute_expires_at: "2026-09-21T18:00:00Z",
      }),
      { status: 201, headers: { "Content-Type": "application/json" } },
    );
  });
  session.start();
  await eventually(() => session.snapshot().lifecycle === "signed_out");
  assert.equal(await session.exchange("test-pending-count"), true);
  let stream: ReadableStreamDefaultController<Uint8Array> | undefined;
  let visible = true;
  let visibilityChanged = () => {};
  const views = new ViewCoordinator(session, {
    visibility: {
      isVisible: () => visible,
      subscribe: (listener) => {
        visibilityChanged = listener;
        return () => {};
      },
    },
    request: async () =>
      new Response(
        new ReadableStream({
          start(controller) {
            stream = controller;
          },
        }),
        { headers: { "Content-Type": "text/event-stream" } },
      ),
    reconnectMilliseconds: 10,
  });
  t.after(() => views.close());
  let total = 125;
  let failure = false;
  let release: (() => void) | undefined;
  let delayed = false;
  let reads = 0;
  t.mock.method(globalThis, "fetch", async (input: string) => {
    assert.equal(input, "/api/v2/mcp/grant-requests?state=pending&limit=1");
    reads++;
    const captured = total;
    if (delayed)
      await new Promise<void>((resolve) => {
        release = resolve;
      });
    if (failure) throw new Error("unavailable");
    return new Response(
      JSON.stringify({
        items: captured === 0 ? [] : [{}],
        next_cursor: captured > 1 ? "more" : null,
        total_count: captured,
        offset: 0,
      }),
      { headers: { "Content-Type": "application/json" } },
    );
  });
  const count = new PendingRequestsController(session, views);
  const presentation = () => count.presentation(views.snapshot());
  assert.deepEqual(presentation(), {
    label: "Requests, pending count loading",
    badge: "?",
  });
  views.activate("#/principals");
  await eventually(() => presentation().label === "Requests, 125 pending");
  assert.equal(reads, 1, "must not traverse pages to count");
  delayed = true;
  views.navigate("#/mcp/access-requests?queue=all&filter_state=approved");
  assert.equal(
    presentation().badge,
    "125",
    "navigation must not clear the count",
  );
  assert.match(presentation().label, /refreshing/);
  delayed = false;
  release?.();
  await eventually(() => presentation().label === "Requests, 125 pending");
  total = 0;
  views.manualRefresh();
  await eventually(() => presentation().badge === null);
  assert.equal(presentation().label, "Requests, 0 pending");
  views.navigate("#/system");
  total = 3;
  stream?.enqueue(
    new TextEncoder().encode(
      'event: invalidate\ndata: {"kind":"grant_requests","resource_id":null}\n\n',
    ),
  );
  await eventually(() => presentation().label === "Requests, 3 pending");
  total = 2;
  views.invalidate({ kind: "grant_requests", resourceID: null });
  await eventually(() => presentation().label === "Requests, 2 pending");
  failure = true;
  views.manualRefresh();
  await eventually(() => presentation().label.includes("unavailable"));
  assert.equal(presentation().badge, "?");
  assert.match(presentation().label, /last known 2 pending/);
  failure = false;
  total = 1;
  stream?.close();
  await eventually(() => presentation().label === "Requests, 1 pending");
  visible = false;
  visibilityChanged();
  t.mock.timers.enable({ apis: ["setTimeout"] });
  total = 6;
  t.mock.timers.tick(60_000);
  for (let i = 0; i < 20; i++) await Promise.resolve();
  assert.equal(presentation().label, "Requests, 1 pending");
  visible = true;
  visibilityChanged();
  t.mock.timers.tick(30_000);
  for (let i = 0; i < 20; i++) await Promise.resolve();
  assert.equal(presentation().label, "Requests, 6 pending");
  visible = false;
  visibilityChanged();
  t.mock.timers.reset();
  delayed = true;
  const readsBeforeLate = reads;
  total = 99;
  views.manualRefresh();
  await eventually(() => reads > readsBeforeLate);
  await session.logout();
  assert.equal(presentation().badge, "?");
  assert.doesNotMatch(presentation().label, /99|1 pending/);
  delayed = false;
  release?.();
  assert.equal(await session.exchange("test-next-session"), true);
  total = 0;
  views.activate("#/overview");
  await eventually(() => presentation().label === "Requests, 0 pending");
});
