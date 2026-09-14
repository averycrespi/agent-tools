import assert from "node:assert/strict";
import test from "node:test";
import {
  parseFragment,
  resolveFragment,
  serializeLocation,
} from "../src/location.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
const other = "01ARZ3NDEKTSV4RRFFQ69G5FAW";
const collections = [
  "mcp/servers",
  "mcp/tools",
  "access/principals",
  "mcp/grants",
  "mcp/access-requests",
  "mcp/invocations",
  "activity/audit",
  "system",
  "overview",
  "sign-in",
];
const details = [
  "mcp/servers",
  "access/principals",
  "mcp/grants",
  "mcp/access-requests",
  "mcp/invocations",
  "activity/audit",
].map((path) => `${path}/${id}`);
const creates = [
  "mcp/servers/new",
  "access/principals/new",
  "mcp/grants/new",
  "system/backups/new",
  "system/admin-credentials/new",
];
const owned = ["operations", "auth-flows", "descriptors"].map(
  (kind) => `mcp/servers/${id}/${kind}/${other}`,
);
const routes = [...collections, ...details, ...creates, ...owned];

test("every canonical collection, detail, create and server-owned route round trips", () => {
  for (const path of routes) {
    const fragment = `#/${path}`;
    const parsed = parseFragment(fragment);
    assert.ok(parsed, fragment);
    assert.equal(serializeLocation(parsed), fragment);
    assert.deepEqual(parseFragment(serializeLocation(parsed)), parsed);
  }
});

test("old flat paths and undeclared members never resolve as resources", () => {
  const old = [
    "servers",
    "catalog",
    "principals",
    "grants",
    "requests",
    "access/grants",
    "access/requests",
    "invocations",
    "activity/invocations",
    "audit",
  ];
  for (const path of old.flatMap((path) => [
    path,
    `${path}/${id}`,
    `${path}/new`,
  ])) {
    assert.equal(parseFragment(`#/${path}`), undefined, path);
    for (const authenticated of [false, true]) {
      const resolved = resolveFragment(`#/${path}`, authenticated);
      assert.equal(resolved.invalid, true);
      assert.equal(
        resolved.canonicalFragment,
        authenticated ? "#/overview" : "#/sign-in",
      );
    }
  }
  for (const path of routes) {
    for (const query of [
      "filter_unknown=x",
      "cursor=opaque",
      "__proto__=x",
      "constructor=x",
      "toString=x",
      "tab=x",
      "filter_name=%",
      "filter_name=%00",
      "sort=name&sort=id",
      "secret=x",
    ]) {
      assert.equal(
        parseFragment(`#/${path}?${query}`),
        undefined,
        `${path}?${query}`,
      );
    }
  }
});

test("server projections are closed and status is the elided default", () => {
  const base = `#/mcp/servers/${id}`;
  assert.equal(serializeLocation(parseFragment(`${base}?tab=status`)!), base);
  for (const tab of ["tools", "operations", "authentication", "settings"]) {
    assert.equal(
      serializeLocation(parseFragment(`${base}?tab=${tab}`)!),
      `${base}?tab=${tab}`,
    );
  }
  for (const query of [
    "tab=activity",
    "tab=overview",
    "filter_tool=x",
    "tab=status&filter_name=x",
    "tab=settings&filter_name=x",
    "tab=authentication&sort=name",
    "tab=tools&filter_action=reload",
    "tab=operations&filter_tool=x",
    "tab=operations&direction=ascending",
  ]) {
    assert.equal(parseFragment(`${base}?${query}`), undefined, query);
  }
  const fragment = `${base}?tab=operations&sort=created&direction=descending&filter_action=reload&filter_status=failed`;
  assert.equal(serializeLocation(parseFragment(fragment)!), fragment);
});

test("destination queries have deterministic ordering and preserve valid context", () => {
  const cases: Array<[string, string]> = [
    [
      "mcp/servers?filter_namespace=demo&direction=descending&sort=name&filter_name=Caf%C3%A9",
      "mcp/servers?sort=name&direction=descending&filter_name=Caf%C3%A9&filter_namespace=demo",
    ],
    [
      "mcp/tools?filter_server=demo&sort=tool",
      "mcp/tools?sort=tool&filter_server=demo",
    ],
    [
      `mcp/servers/${id}?filter_tool=echo&tab=tools&sort=tool`,
      `mcp/servers/${id}?tab=tools&sort=tool&filter_tool=echo`,
    ],
    [
      "access/principals?filter_visibility=all&sort=name",
      "access/principals?sort=name&filter_visibility=all",
    ],
    [
      "mcp/grants?filter_effect=deny&sort=target",
      "mcp/grants?sort=target&filter_effect=deny",
    ],
    [
      `mcp/grants/new?server_id=${other}&principal_id=${id}`,
      `mcp/grants/new?principal_id=${id}&server_id=${other}`,
    ],
    [
      "mcp/access-requests?filter_state=approved&queue=all",
      "mcp/access-requests?queue=all&filter_state=approved",
    ],
    [
      `mcp/invocations/${id}?filter_tool=echo&filter_decision=allow`,
      `mcp/invocations/${id}?filter_decision=allow&filter_tool=echo`,
    ],
    [
      `activity/audit/${id}?filter_outcome=succeeded&filter_category=server`,
      `activity/audit/${id}?filter_category=server&filter_outcome=succeeded`,
    ],
    ["system?tab=status", "system"],
  ];
  for (const [input, expected] of cases) {
    const parsed = parseFragment(`#/${input}`);
    assert.ok(parsed, input);
    const serialized = serializeLocation(parsed);
    assert.equal(serialized, `#/${expected}`);
    assert.equal(serializeLocation(parseFragment(serialized)!), serialized);
  }
  for (const path of [
    "mcp/servers?filter_status=unknown",
    "mcp/tools?filter_status=retired",
    "mcp/access-requests?filter_state=pending",
    "access/principals?direction=ascending",
    `mcp/grants/new?filter_name=x`,
    "system?filter_name=x",
  ]) {
    assert.equal(parseFragment(`#/${path}`), undefined, path);
  }
});
