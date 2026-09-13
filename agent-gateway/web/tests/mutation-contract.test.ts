import assert from "node:assert/strict";
import test from "node:test";
import { MutationCoordinator, type MutationSpec } from "../src/mutation.ts";
import { SessionClient } from "../src/session.ts";

const id = "01ARZ3NDEKTSV4RRFFQ69G5FAV";
const routes: Array<
  [
    string,
    MutationSpec<void>["method"],
    boolean,
    MutationSpec<void>["idempotency"],
  ]
> = [
  ["/api/v2/admin-credentials", "POST", false, "none"],
  ["/api/v2/backups", "POST", false, "backup_create"],
  ["/api/v2/mcp/servers", "POST", false, "server_create"],
  [`/api/v2/mcp/servers/${id}`, "PATCH", true, "none"],
  [`/api/v2/mcp/servers/${id}/operations`, "POST", true, "operation_start"],
  [`/api/v2/mcp/servers/${id}/oauth-flows`, "POST", true, "none"],
  [`/api/v2/mcp/servers/${id}/credential-replacements`, "POST", true, "none"],
  ["/api/v2/principals", "POST", false, "none"],
  [`/api/v2/principals/${id}/credential`, "POST", true, "none"],
  ["/api/v2/grants", "POST", false, "none"],
  [`/api/v2/grants/${id}`, "PATCH", true, "none"],
  [`/api/v2/grant-requests/${id}/approve`, "POST", true, "none"],
];

for (const [route, method, requiresPrecondition, idempotency] of routes) {
  test(`v2 mutation can reach confirmation without sending: ${method} ${route}`, () => {
    let requests = 0;
    const request: typeof fetch = async () => {
      requests++;
      throw new Error("unexpected network work");
    };
    const coordinator = new MutationCoordinator(new SessionClient(request), {
      request,
      key: () => "fixture-key",
    });
    try {
      const spec: MutationSpec<void> = {
        route,
        method,
        body: "{}",
        precondition: requiresPrecondition ? '"fixture-revision"' : null,
        requiresPrecondition,
        idempotency,
        successStatuses: [200],
        decode: async () => {},
      };
      const mutation = coordinator.create<void>();
      mutation.begin(spec);
      mutation.confirm();
      assert.equal(mutation.snapshot().state, "confirming");
      assert.throws(
        () =>
          coordinator.create<void>().begin({
            ...spec,
            route: route
              .replace("/api/v2/mcp/", "/api/v1/")
              .replace("/api/v2/", "/api/v1/"),
          }),
        /invalid mutation specification/,
      );
      assert.equal(requests, 0);
    } finally {
      coordinator.close();
    }
  });
}
