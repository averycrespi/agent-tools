import assert from "node:assert/strict";
import test from "node:test";
import { attentionItems, serverAttention } from "../src/attention.ts";
import type { OverviewSnapshot, ServerView } from "../src/overview.tsx";
import type { ViewSnapshot } from "../src/view.ts";

const server = (patch: Partial<ServerView> = {}): ServerView => ({
  id: "server-a",
  name: "Example",
  desired: "enabled",
  runtime: "active",
  reason: null,
  credential: "ready",
  catalog: "current",
  activeToolCount: 1,
  attention: false,
  saturated: false,
  ...patch,
});
const view = {
  panels: Object.fromEntries(
    ["status", "servers", "requests"].map((s) => [
      `overview-${s}`,
      { status: "current", hasValue: true },
    ]),
  ),
} as ViewSnapshot;
const snapshot = (): OverviewSnapshot => ({
  status: {
    latched: false,
    traffic: { recovery: { health: "healthy" } },
  } as NonNullable<OverviewSnapshot["status"]>,
  servers: { items: [], complete: true, restarted: false },
  requests: { items: [], total: 0, complete: true },
});

test("only confirmed actionable server faults qualify, one reason per server", () => {
  for (const runtime of [
    "inactive",
    "activating",
    "retry_wait",
    "stopping",
    "active",
  ])
    assert.equal(
      serverAttention(
        server({
          runtime,
          saturated: true,
          catalog: "unavailable",
          reason: "connectivity",
        }),
      ),
      undefined,
    );
  for (const reason of [
    "resource_limit",
    "connectivity",
    "tls_failed",
    "protocol_invalid",
    "cancelled",
    null,
  ])
    assert.equal(
      serverAttention(server({ runtime: "degraded", reason })),
      undefined,
    );
  for (const reason of [
    "configuration_invalid",
    "protocol_unsupported",
    "stop_unconfirmed",
  ])
    assert.ok(serverAttention(server({ runtime: "degraded", reason })));
  for (const credential of [
    "absent",
    "locked",
    "interaction_required",
    "unavailable",
    "unsupported",
    "reauthentication_required",
    "cleanup_pending",
  ])
    assert.ok(serverAttention(server({ credential })));
  for (const credential of [
    "ready",
    "not_required",
    "refreshing",
    "disconnecting",
  ])
    assert.equal(serverAttention(server({ credential })), undefined);
  assert.equal(
    serverAttention(
      server({ desired: "disabled", runtime: "authentication_required" }),
    ),
    undefined,
  );
  assert.equal(
    serverAttention(
      server({ runtime: "retry_wait", credential: "unavailable" }),
    ),
    undefined,
  );
});

test("attention uses stable severity order, deduplicates servers, and counts the queue as one item", () => {
  const s = snapshot();
  s.status!.latched = true;
  s.status!.traffic!.recovery!.health = "operator_action_required";
  s.servers!.items = [
    server({ id: "z", runtime: "authentication_required" }),
    server({ id: "a", credential: "absent" }),
    server({ id: "a", credential: "absent" }),
  ];
  s.requests!.total = 999;
  s.requests!.complete = false; // One authoritative total does not require fetching every request.
  const result = attentionItems(s, view);
  assert.equal(result.complete, true);
  assert.deepEqual(
    result.items.map((i) => i.id),
    ["control-storage", "traffic-recording", "a", "z", "pending-requests"],
  );
  assert.equal(result.items.at(-1)!.label, "999 pending MCP requests");
  assert.equal(result.items.at(-1)!.href, "#/mcp/access-requests");
});

test("empty is healthy only with complete current evidence; stale faults are not current", () => {
  assert.equal(attentionItems(snapshot(), view).complete, true);
  for (const health of [
    "healthy",
    "degraded",
    "recovering",
    "recovered",
    "opening",
    "disabled",
    "unavailable",
  ]) {
    const s = snapshot();
    s.status!.traffic!.recovery!.health = health;
    assert.deepEqual(attentionItems(s, view).items, []);
  }
  for (const source of ["status", "servers", "requests"]) {
    for (const status of ["loading", "stale", "error"]) {
      const v = {
        ...view,
        panels: {
          ...view.panels,
          [`overview-${source}`]: { status, hasValue: true },
        },
      } as ViewSnapshot;
      const result = attentionItems(snapshot(), v);
      assert.equal(result.complete, false);
      assert.ok(result.notice);
    }
  }
  const partial = snapshot();
  partial.servers!.complete = false;
  assert.equal(attentionItems(partial, view).complete, false);
  delete partial.status!.traffic!.recovery;
  assert.equal(attentionItems(partial, view).complete, false);
});
