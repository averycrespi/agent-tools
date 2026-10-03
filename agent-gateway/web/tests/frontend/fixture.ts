import { createServer, type ServerResponse } from "node:http";
import { readFile } from "node:fs/promises";
import { resolve } from "node:path";
import { test as base, expect } from "@playwright/test";
import { overviewStatusFixture } from "../browser/fixtures.ts";
import { activityFixture } from "../recorded-activity-fixture.ts";

export const syntheticBearer = "SYNTHETIC_FRONTEND_ONLY_ADMIN_CANARY";
const assets = resolve(import.meta.dirname, "../../../internal/api/static");
const files = new Map([
  ["/", ["index.html", "text/html"]],
  ["/assets/app.js", ["app.js", "text/javascript"]],
  ["/assets/app.css", ["app.css", "text/css"]],
  ["/assets/favicon.svg", ["favicon.svg", "image/svg+xml"]],
]);

// Static frontend only: the sole HTTP mock is a held synthetic invalidation stream.
// All control API responses are explicit browser routes; nothing proxies to Gateway.
export const test = base.extend<{
  frontend: {
    origin: string;
    requests: () => number;
    invalidate: (kind: string, resourceID: string) => void;
  };
}>({
  frontend: async ({ context, page }, use) => {
    const failures: string[] = [];
    const streams = new Set<ServerResponse>();
    const server = createServer((req, res) => {
      const asset = files.get(req.url ?? "");
      if (req.url === "/api/v2/events" && req.method === "POST") {
        streams.add(res);
        res.writeHead(200, {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-store",
        });
        res.write(": synthetic keepalive\n\n");
        res.on("close", () => streams.delete(res));
      } else if (asset && req.method === "GET") {
        void readFile(resolve(assets, asset[0]!))
          .then((bytes) => {
            res.writeHead(200, {
              "Content-Type": asset[1]!,
              "Content-Security-Policy":
                "default-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
            });
            res.end(bytes);
          })
          .catch(() => {
            failures.push("static asset unavailable");
            res.writeHead(500);
            res.end();
          });
      } else {
        failures.push(`Unmocked HTTP ${req.method} ${req.url?.split("?")[0]}`);
        res.writeHead(500);
        res.end();
      }
    });
    await new Promise<void>((resolve) =>
      server.listen(0, "127.0.0.1", resolve),
    );
    const address = server.address();
    if (!address || typeof address === "string")
      throw new Error("Missing frontend listener");
    const origin = `http://127.0.0.1:${address.port}`;
    let signedIn = false;
    let requests = 0;
    context.on("request", () => requests++);
    page.on("pageerror", (error) =>
      failures.push(`Browser error: ${error.name}`),
    );
    await context.route("**/*", async (route) => {
      const request = route.request();
      const url = new URL(request.url());
      const path = url.pathname;
      const method = request.method();
      if (url.origin !== origin) {
        failures.push("Unexpected external request");
        return route.abort("blockedbyclient");
      }
      if (
        (files.has(path) && method === "GET") ||
        (path === "/api/v2/events" && method === "POST")
      )
        return route.continue();
      const json = (
        value: unknown,
        status = 200,
        headers: Record<string, string> = {},
      ) =>
        route.fulfill({
          status,
          contentType: "application/json",
          headers,
          json: value,
        });
      if (path === "/api/v2/admin-sessions" && method === "POST") {
        expect(request.headers().authorization).toBe(
          `Bearer ${syntheticBearer}`,
        );
        expect(request.postDataJSON()).toEqual({});
        signedIn = true;
        return json(session(), 201, {
          "Set-Cookie":
            "agent_gateway_session=synthetic-only; HttpOnly; SameSite=Strict; Path=/",
        });
      }
      if (path === "/api/v2/admin-sessions/current" && method === "POST")
        return signedIn
          ? json(session())
          : route.fulfill({
              status: 401,
              contentType: "application/problem+json",
              json: {
                status: 401,
                code: "authentication_required",
                title: "Sign in required",
              },
            });
      if (path === "/api/v2/admin-sessions/current" && method === "DELETE") {
        signedIn = false;
        return route.fulfill({
          status: 204,
          headers: {
            "Set-Cookie":
              "agent_gateway_session=; Max-Age=0; HttpOnly; SameSite=Strict; Path=/",
          },
        });
      }
      if (
        path === "/api/v2/mcp/grant-constraints/validate" &&
        method === "POST"
      ) {
        const body = request.postDataJSON() as {
          constraint: {
            version: number;
            equals?: Record<string, unknown>;
            regex?: Record<string, string>;
          };
        };
        expect(Object.keys(body)).toEqual(["constraint"]);
        expect([1, 2]).toContain(body.constraint.version);
        const invalid = Object.entries(body.constraint.regex ?? {}).find(
          ([, value]) => value === "[",
        );
        return json(
          invalid
            ? {
                valid: false,
                diagnostics: [
                  {
                    field: `/regex/${invalid[0].replaceAll("~", "~0").replaceAll("/", "~1")}`,
                    message: "pattern is not valid RE2",
                  },
                ],
              }
            : { valid: true, diagnostics: [] },
        );
      }
      if (method === "GET") {
        if (path === "/api/v2/audit-events")
          return json({
            items: [],
            next_cursor: null,
            history: {
              generation: "a".repeat(64),
              oldest_retained: null,
              pruned: false,
            },
          });
        if (path === "/api/v2/system-status")
          return json({
            ...overviewStatusFixture(),
            process: {
              state: "ready",
              ready: true,
              started_at: "2026-08-28T00:00:00Z",
            },
            sqlite: {
              state: "ready",
              schema_version: "18",
              revision: "1",
              latched: false,
            },
            keyring: { capability: "ready" },
          });
        if (path === "/api/v2/recorded-activity")
          return json(activityFixture());
        if (
          [
            "/api/v2/mcp/servers",
            "/api/v2/mcp/catalog",
            "/api/v2/mcp/grant-requests",
            "/api/v2/principals",
            "/api/v2/mcp/grants",
            "/api/v2/backups",
            "/api/v2/admin-credentials",
          ].includes(path)
        )
          return json({
            items: [],
            next_cursor: null,
            total_count: 0,
            offset: 0,
          });
      }
      failures.push(`Unmocked API ${method} ${path}`);
      return route.fulfill({ status: 500, json: { code: "unmocked_fixture" } });
    });
    await page.clock.setFixedTime(new Date("2026-10-03T12:34:56Z"));
    try {
      await page.goto(`${origin}/#/overview`);
      await expect(page.getByTestId("gateway-shell")).toHaveAttribute(
        "data-session-lifecycle",
        "signed_out",
      );
      await expect(page).toHaveURL(`${origin}/#/sign-in`);
      await use({
        origin,
        requests: () => requests,
        invalidate: (kind, resourceID) => {
          expect(
            streams.size,
            "Connected synthetic invalidation stream",
          ).toBeGreaterThan(0);
          for (const stream of streams)
            stream.write(
              `event: invalidate\ndata: ${JSON.stringify({ kind, resource_id: resourceID })}\n\n`,
            );
        },
      });
    } finally {
      await context.close();
      for (const stream of streams) stream.end();
      server.closeAllConnections();
      await new Promise<void>((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve())),
      );
      expect(failures, "Closed fixture request inventory").toEqual([]);
    }
  },
});
function session() {
  return {
    csrf_token: "A".repeat(43),
    idle_expires_at: "2099-01-01T00:00:00Z",
    absolute_expires_at: "2099-01-02T00:00:00Z",
  };
}
