import assert from "node:assert/strict";
import test from "node:test";
import {
  RelatedHistory,
  RelatedHistoryChanged,
} from "../src/related-history.ts";
import type { SessionClient } from "../src/session.ts";
import type {
  ViewCoordinator,
  ViewPanel,
  ViewReadContext,
} from "../src/view.ts";

test("related evidence pages, isolates failures and discards replaced history", async () => {
  let panel: ViewPanel<unknown>;
  let clear = () => {};
  let key = "#/audit-log/01ARZ3NDEKTSV4RRFFQ69G5FAV";
  let fail = false;
  let changed = false;
  let bulk = false;
  let bulkReads = 0;
  let pending = Promise.resolve();
  const read = async (reason: "navigation" | "panel") => {
    const result = await panel.read({
      viewKey: key,
      reason,
      signal: new AbortController().signal,
    } as ViewReadContext);
    panel.publish(result);
  };
  const session = {
    registerProtectedState: (fn: () => void) => {
      clear = fn;
    },
  } as unknown as SessionClient;
  const views = {
    registerPanel: (value: ViewPanel<unknown>) => {
      panel = value;
    },
    refreshPanel: () => {
      pending = read("panel");
      return pending;
    },
  } as unknown as ViewCoordinator;
  const history = new RelatedHistory(
    session,
    views,
    "related-test",
    "audit",
    async (_context, cursor) => {
      if (changed) throw new RelatedHistoryChanged();
      if (fail) throw new Error("read failed");
      if (bulk) {
        bulkReads++;
        return {
          items: Array.from(
            { length: cursor ? 50 : 49 },
            (_, i) => `page${bulkReads}-${i}`,
          ),
          next: `page${bulkReads}`,
          generation: "first",
        };
      }
      return {
        items: cursor ? ["older"] : ["newest"],
        next: cursor ? null : "next",
        generation: "first",
      };
    },
  );
  await read("navigation");
  assert.deepEqual(history.snapshot().items, ["newest"]);
  history.more();
  await pending;
  assert.deepEqual(history.snapshot().items, ["newest", "older"]);
  fail = true;
  history.refresh();
  await pending;
  assert.equal(history.snapshot().error, true);
  assert.deepEqual(history.snapshot().items, ["newest", "older"]);
  fail = false;
  history.refresh();
  await pending;
  assert.equal(history.snapshot().error, false);
  assert.deepEqual(history.snapshot().items, ["newest"]);
  changed = true;
  history.more();
  await pending;
  assert.deepEqual(history.snapshot().items, []);
  assert.equal(history.snapshot().next, null);
  assert.match(history.snapshot().notice!, /History changed/);
  changed = false;
  key = "#/audit-log/01ARZ3NDEKTSV4RRFFQ69G5FAW";
  await read("navigation");
  assert.equal(history.snapshot().key, key);
  clear();
  assert.equal(history.snapshot().loaded, false);
  assert.deepEqual(history.snapshot().items, []);
  bulk = true;
  await read("navigation");
  for (let i = 0; i < 9; i++) {
    history.more();
    await pending;
  }
  assert.equal(history.snapshot().items.length, 499);
  history.more();
  await pending;
  assert.equal(history.snapshot().items.length, 500);
  assert.equal(history.snapshot().items.at(-1), "page11-0");
  assert.equal(history.snapshot().next, null);
  assert.match(history.snapshot().notice!, /first 500/);
  history.more();
  await pending;
  assert.equal(bulkReads, 11);
  bulk = false;
  history.refresh();
  await pending;
  assert.deepEqual(history.snapshot().items, ["newest"]);
  assert.equal(history.snapshot().notice, undefined);
});
