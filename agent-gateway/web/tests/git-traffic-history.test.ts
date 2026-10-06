import assert from "node:assert/strict";
import test from "node:test";
import { GitTrafficController } from "../src/git-traffic-history.ts";
import { validGitTrafficQuery } from "../src/git-traffic-query.ts";
import { parseFragment } from "../src/location.ts";
import type { SessionClient } from "../src/session.ts";
import type {
  ViewCoordinator,
  ViewPanel,
  ViewReadContext,
  ViewRefreshReason,
} from "../src/view.ts";

const id = (n: number) => `${"0".repeat(22)}${String(n).padStart(4, "0")}`;
const row = (n: number) => ({
  admission: {
    id: id(n),
    admitted_at: "2026-10-01T12:00:00.000000000Z",
    evaluated_at: "2026-10-01T12:00:00.000000000Z",
    principal: { id: id(0), revision: "1" },
    agent_credential: { id: id(0), revision: "1" },
    repository: { id: id(0), revision: "1" },
    alias_revision: "1",
    profile_revision: "1",
    authorization_revision: "1",
    operation: "push",
    commands: 1,
    allowed: true,
  },
  completion: null,
});
test("Git history query is closed, bounded and query-preserving", () => {
  const query = {
    filter_repository: "Recorded libray",
    filter_operation: "push",
    filter_report: "unknown",
  };
  assert.equal(validGitTrafficQuery(query), true);
  assert.deepEqual(
    {
      ...parseFragment(
        "#/git/traffic?filter_repository=Recorded%20libray&filter_operation=push&filter_report=unknown",
      )?.query,
    },
    query,
  );
  for (const invalid of [
    { sort: "admitted" },
    { filter_report: "complete" },
    { filter_repository: "x".repeat(257) },
    { filter_repository: "\u0000" },
  ])
    assert.equal(validGitTrafficQuery(invalid), false);
});
test("Git history owns live/pause, bounded continuation, resets and late read fencing", async () => {
  let panel!: ViewPanel<any>;
  let clear!: () => void;
  let key = "#/git/traffic";
  let signal = new AbortController();
  let count = 0;
  let mode = "normal";
  let hold: (() => void) | undefined;
  let entered: (() => void) | undefined;
  const requests: string[] = [];
  const original = globalThis.fetch;
  globalThis.fetch = async (input) => {
    const url = String(input);
    requests.push(url);
    count++;
    if (mode === "late") {
      entered?.();
      await new Promise<void>((resolve) => {
        hold = resolve;
      });
    }
    if (mode === "error") throw new Error("unavailable");
    if (mode === "stale" && url.includes("cursor="))
      return new Response(
        JSON.stringify({ status: 409, code: "stale_cursor", title: "Changed" }),
        {
          status: 409,
          headers: { "Content-Type": "application/problem+json" },
        },
      );
    return new Response(
      JSON.stringify({
        items:
          mode === "bulk"
            ? Array.from({ length: 50 }, (_, index) => row(count * 50 + index))
            : [row(url.includes("cursor=") ? 1 : 2)],
        next_cursor:
          mode === "bulk"
            ? `older${count}`
            : url.includes("cursor=")
              ? null
              : "older",
      }),
      { headers: { "Content-Type": "application/json" } },
    );
  };
  const read = async (reason: ViewRefreshReason) => {
    if (panel.shouldRefresh?.(reason) === false) return;
    signal = new AbortController();
    const result = await panel.read({
      viewKey: key,
      reason,
      generation: 1,
      epoch: 1,
      isCurrent: () => !signal.signal.aborted,
      signal: signal.signal,
      csrfToken: "synthetic",
      sessionLost: async () => false,
    } as ViewReadContext);
    panel.publish(result);
  };
  const views = {
    registerPanel: (p: ViewPanel<any>) => {
      panel = p;
    },
    snapshot: () => ({ viewKey: key, panels: {} }),
    refreshPanel: () => read("panel"),
    cancelPanelRead: () => signal.abort(),
  } as unknown as ViewCoordinator;
  const controller = new GitTrafficController(
    {
      registerProtectedState: (fn: () => void) => {
        clear = fn;
      },
    } as unknown as SessionClient,
    views,
  );
  try {
    await read("navigation");
    assert.equal(controller.snapshot().items.length, 1);
    controller.setLive(false);
    const offCount = count;
    await read("invalidation");
    assert.equal(count, offCount);
    mode = "error";
    await controller.older();
    assert.equal(controller.snapshot().error, false);
    assert.equal(controller.snapshot().olderError, true);
    assert.equal(controller.snapshot().next, "older");
    assert.equal(controller.snapshot().items.length, 1);
    mode = "normal";
    await controller.older();
    assert.equal(controller.snapshot().olderError, false);
    assert.equal(controller.snapshot().items.length, 2);
    assert.equal(controller.snapshot().paused, true);
    assert.equal(controller.snapshot().next, null);
    key = `#/git/traffic/${id(2)}`;
    assert.equal(panel.matches(key), false);
    key = "#/git/traffic";
    await read("navigation");
    assert.equal(controller.snapshot().items.length, 1);
    assert.match(
      controller.snapshot().notice,
      /Older results could not be continued/,
    );
    controller.resume();
    await read("navigation");
    assert.equal(controller.snapshot().notice, "");
    key = "#/git/traffic?filter_operation=push";
    await read("navigation");
    assert.equal(controller.snapshot().items.length, 1);
    assert.equal(controller.snapshot().live, false);
    assert.ok(requests.at(-1)?.includes("operation=push"));
    assert.ok(!requests.at(-1)?.includes("cursor="));
    mode = "stale";
    await controller.older();
    assert.equal(controller.snapshot().items.length, 1);
    assert.match(
      controller.snapshot().notice,
      /Older results could not be continued/,
    );
    mode = "error";
    await read("manual");
    assert.equal(controller.snapshot().error, true);
    assert.equal(controller.snapshot().items.length, 1);
    mode = "late";
    const reached = new Promise<void>((resolve) => {
      entered = resolve;
    });
    const pending = read("manual");
    await reached;
    controller.setLive(false);
    hold?.();
    await assert.rejects(pending);
    assert.equal(controller.snapshot().error, true);
    clear();
    assert.equal(controller.snapshot().live, true);
    assert.equal(controller.snapshot().items.length, 0);
    mode = "bulk";
    await read("navigation");
    for (let page = 1; page < 10; page++) await controller.older();
    assert.equal(controller.snapshot().items.length, 500);
    const boundedReads = count;
    await controller.older();
    assert.equal(count, boundedReads);
  } finally {
    globalThis.fetch = original;
  }
});
