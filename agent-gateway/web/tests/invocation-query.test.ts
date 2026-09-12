import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { parseFragment, serializeLocation } from "../src/location.ts";
import { invocationOptions } from "../src/invocation-query.ts";
import { SessionClient } from "../src/session.ts";
import { ViewCoordinator } from "../src/view.ts";

test("invocation list/detail share closed applied filters, never live authority or cursors", () => {
  for (const [key, options] of Object.entries(invocationOptions)) {
    for (const [value] of options) {
      const fragment = `#/invocations/00000000000000000000000001?filter_${key}=${value}`;
      const location = parseFragment(fragment)!;
      assert.ok(location);
      assert.equal(serializeLocation(location), fragment);
      assert.equal(
        serializeLocation({ ...location, segments: ["invocations"] }),
        `#/invocations?filter_${key}=${value}`,
      );
    }
  }
  for (const query of [
    "cursor=opaque",
    "live=true",
    "paused=true",
    "filter_unknown=x",
    "filter_decision=other",
    "filter_outcome=other",
    "filter_tool=" + "x".repeat(257),
    "filter_principal=%00",
  ]) {
    assert.equal(parseFragment(`#/invocations?${query}`), undefined);
    assert.equal(
      parseFragment(`#/invocations/00000000000000000000000001?${query}`),
      undefined,
    );
  }
  assert.ok(
    parseFragment(
      "#/invocations?filter_tool=historical%20lokoup&filter_principal=caf%C3%A9",
    ),
  );
});

test("browser invocation choices cover the canonical API vocabularies", () => {
  const outcomes = readFileSync(
    new URL("../../internal/contract/s6_invocations.go", import.meta.url),
    "utf8",
  );
  const decisions = readFileSync(
    new URL("../../internal/contract/s3_states.go", import.meta.url),
    "utf8",
  );
  assert.deepEqual(
    invocationOptions.outcome.map(([value]) => value).sort(),
    [
      ...outcomes.matchAll(
        /InvocationOutcome\w+\s+InvocationOutcomeClass\s*=\s*"([^"]+)"/g,
      ),
    ]
      .map((match) => match[1])
      .sort(),
  );
  assert.deepEqual(
    invocationOptions.decision.map(([value]) => value).sort(),
    [
      ...decisions.matchAll(
        /Decision\w+\s+AuthorizationDecision\s*=\s*"([^"]+)"/g,
      ),
    ]
      .map((match) => match[1])
      .concat("not_evaluated")
      .sort(),
  );
});

test("shared coalescer rechecks automatic eligibility without blocking explicit reads", async (t) => {
  const session = new SessionClient(
    async () =>
      new Response(
        JSON.stringify({
          csrf_token: "A".repeat(43),
          idle_expires_at: "2026-08-28T18:30:00Z",
          absolute_expires_at: "2026-08-29T18:00:00Z",
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
  );
  const authenticated = new Promise<void>((resolve) =>
    session.subscribe((state) => {
      if (state.lifecycle === "authenticated") resolve();
    }),
  );
  session.start();
  await authenticated;
  const views = new ViewCoordinator(session, {
    request: async () =>
      new Response(
        new ReadableStream({
          start(controller) {
            controller.enqueue(new TextEncoder().encode(": ready\n\n"));
          },
        }),
        { headers: { "Content-Type": "text/event-stream" } },
      ),
  });
  t.after(() => views.close());
  let automatic = true;
  const reasons: string[] = [];
  let published = 0;
  let notify: (() => void) | undefined;
  views.registerPanel({
    id: "history",
    matches: () => true,
    invalidations: ["invocations"],
    shouldRefresh: (reason) =>
      !["invalidation", "reconnect", "poll"].includes(reason) || automatic,
    read: async (context) => {
      reasons.push(context.reason!);
      return reasons.length;
    },
    publish: () => {
      published++;
      notify?.();
    },
  });
  const initial = new Promise<void>((resolve) => {
    notify = resolve;
  });
  views.activate("#/invocations");
  await initial;
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const before = published;
  views.invalidate({ kind: "invocations", resourceID: null });
  automatic = false;
  t.mock.timers.tick(250);
  await Promise.resolve();
  assert.equal(
    published,
    before,
    "queued work must not replace older/off reading",
  );
  const explicit = new Promise<void>((resolve) => {
    notify = resolve;
  });
  views.manualRefresh();
  await explicit;
  assert.equal(reasons.at(-1), "manual");
  automatic = true;
  const live = new Promise<void>((resolve) => {
    notify = resolve;
  });
  views.invalidate({ kind: "invocations", resourceID: null });
  views.invalidate({ kind: "invocations", resourceID: null });
  t.mock.timers.tick(250);
  await live;
  assert.equal(
    published,
    before + 2,
    "matching invalidations coalesce into one fresh read",
  );
  assert.equal(reasons.at(-1), "invalidation");
});
