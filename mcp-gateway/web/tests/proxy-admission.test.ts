import assert from "node:assert/strict";
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { readdir, readFile } from "node:fs/promises";
import {
  createServer,
  request,
  type IncomingHttpHeaders,
  type IncomingMessage,
  type ServerResponse,
} from "node:http";
import { connect } from "node:net";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";

const repositoryRoot = resolve(import.meta.dirname, "../../..");
const entryPoint = resolve(import.meta.dirname, "../dev-server.ts");
const generatedContract = resolve(
  import.meta.dirname,
  "../generated/development-contract.ts",
);

interface Observation {
  method: string;
  url: string;
  headers: IncomingHttpHeaders;
  body: Buffer;
}

function listen(server = createServer()): Promise<number> {
  return new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      assert(address && typeof address === "object");
      resolveListen(address.port);
    });
  });
}

function close(server: ReturnType<typeof createServer>): Promise<void> {
  return new Promise((resolveClose, reject) => {
    server.close((error) => (error ? reject(error) : resolveClose()));
    server.closeAllConnections();
  });
}

function launch(
  frontendPort: number,
  gatewayPort: number,
): {
  child: ChildProcess;
  output: () => string;
} {
  const child = spawn(process.execPath, [entryPoint], {
    cwd: repositoryRoot,
    env: {
      ...process.env,
      MCP_GATEWAY_UI_LISTEN: `127.0.0.1:${frontendPort}`,
      MCP_GATEWAY_UI_GATEWAY: `http://127.0.0.1:${gatewayPort}`,
    },
    stdio: ["ignore", "pipe", "pipe"],
  });
  let output = "";
  child.stdout?.on("data", (chunk: Buffer) => {
    output += chunk.toString();
  });
  child.stderr?.on("data", (chunk: Buffer) => {
    output += chunk.toString();
  });
  return { child, output: () => output };
}

function waitForReady(
  child: ChildProcess,
  output: () => string,
): Promise<void> {
  return new Promise((resolveReady, reject) => {
    const timeout = setTimeout(() => {
      cleanup();
      child.kill("SIGKILL");
      reject(new Error(`timed out waiting for dev server: ${output()}`));
    }, 5_000);
    const check = () => {
      if (output().includes("Development only: trusted local proxy")) {
        cleanup();
        resolveReady();
      }
    };
    const exited = () => {
      cleanup();
      reject(new Error(`dev server exited before ready: ${output()}`));
    };
    const cleanup = () => {
      clearTimeout(timeout);
      child.stdout?.off("data", check);
      child.stderr?.off("data", check);
      child.off("exit", exited);
    };
    child.stdout?.on("data", check);
    child.stderr?.on("data", check);
    child.once("exit", exited);
    check();
  });
}

function waitForExit(child: ChildProcess): Promise<void> {
  return new Promise((resolveExit, reject) => {
    const timeout = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error("timed out waiting for dev server shutdown"));
    }, 5_000);
    child.once("exit", (code, signal) => {
      clearTimeout(timeout);
      if (code === 0 && signal === null) resolveExit();
      else
        reject(
          new Error(`dev server exited with code=${code} signal=${signal}`),
        );
    });
  });
}

function exchange(
  port: number,
  path: string,
  options: {
    method?: string;
    headers?: Record<string, string | string[]>;
    body?: Buffer | string;
  } = {},
): Promise<{ status: number; headers: IncomingHttpHeaders; body: Buffer }> {
  return new Promise((resolveExchange, reject) => {
    const outbound = request(
      {
        host: "127.0.0.1",
        port,
        method: options.method ?? "GET",
        path,
        headers: options.headers,
        agent: false,
      },
      (response) => {
        const chunks: Buffer[] = [];
        response.on("data", (chunk: Buffer) => chunks.push(chunk));
        response.on("end", () => {
          resolveExchange({
            status: response.statusCode ?? 0,
            headers: response.headers,
            body: Buffer.concat(chunks),
          });
        });
      },
    );
    outbound.once("error", reject);
    if (options.body !== undefined) outbound.write(options.body);
    outbound.end();
  });
}

function openHmr(frontendOrigin: string, token: string): Promise<void> {
  return new Promise((resolveHmr, reject) => {
    const socket = new WebSocket(
      `${frontendOrigin.replace("http://", "ws://")}?token=${token}`,
      "vite-hmr",
    );
    const timeout = setTimeout(() => {
      socket.close();
      reject(new Error("timed out connecting to HMR"));
    }, 5_000);
    socket.addEventListener("open", () => socket.close());
    socket.addEventListener("close", () => {
      clearTimeout(timeout);
      resolveHmr();
    });
    socket.addEventListener("error", () => {
      clearTimeout(timeout);
      reject(new Error("HMR connection failed"));
    });
  });
}

function rawUpgrade(port: number, path: string): Promise<string> {
  return new Promise((resolveUpgrade, reject) => {
    const socket = connect(port, "127.0.0.1");
    let response = "";
    const timeout = setTimeout(() => {
      socket.destroy();
      reject(new Error(`timed out waiting for upgrade rejection: ${response}`));
    }, 5_000);
    socket.setEncoding("utf8");
    socket.on("connect", () => {
      socket.write(
        `GET ${path} HTTP/1.1\r\nHost: 127.0.0.1:${port}\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n`,
      );
    });
    socket.on("data", (chunk) => {
      response += chunk;
    });
    socket.on("end", () => {
      clearTimeout(timeout);
      resolveUpgrade(response);
    });
    socket.on("error", (error) => {
      clearTimeout(timeout);
      reject(error);
    });
  });
}

type BackendHandler = (
  incoming: IncomingMessage,
  response: ServerResponse,
  observations: Observation[],
) => void;

const observingHandler: BackendHandler = (incoming, response, observations) => {
  const chunks: Buffer[] = [];
  incoming.on("data", (chunk: Buffer) => chunks.push(chunk));
  incoming.on("end", () => {
    observations.push({
      method: incoming.method ?? "",
      url: incoming.url ?? "",
      headers: incoming.headers,
      body: Buffer.concat(chunks),
    });
    response.writeHead(204, { "X-Observer": "reached" });
    response.end();
  });
};

async function withDevelopmentServer(
  run: (context: {
    frontendPort: number;
    gatewayPort: number;
    frontendOrigin: string;
    observations: Observation[];
    output: () => string;
  }) => Promise<void>,
  handler: BackendHandler = observingHandler,
): Promise<void> {
  const observations: Observation[] = [];
  const backend = createServer((incoming, response) => {
    handler(incoming, response, observations);
  });
  const gatewayPort = await listen(backend);
  const reservation = createServer();
  const frontendPort = await listen(reservation);
  await close(reservation);
  const development = launch(frontendPort, gatewayPort);
  try {
    await waitForReady(development.child, development.output);
    await run({
      frontendPort,
      gatewayPort,
      frontendOrigin: `http://127.0.0.1:${frontendPort}`,
      observations,
      output: development.output,
    });
  } finally {
    if (development.child.exitCode === null) {
      development.child.kill("SIGTERM");
      await waitForExit(development.child);
    }
    await close(backend);
  }
}

test("contract projection matches the authoritative API body bound", () => {
  const result = spawnSync(
    "go",
    [
      "run",
      "./web/scripts/generate-development-contract.go",
      "--check",
      generatedContract,
    ],
    { cwd: resolve(repositoryRoot, "mcp-gateway"), encoding: "utf8" },
  );
  assert.equal(result.status, 0, result.stderr);
});

test("proxy admission projects exact target, Origin, headers, and body once", async () => {
  await withDevelopmentServer(async (context) => {
    const body = '{"display_name":"observer"}';
    const response = await exchange(
      context.frontendPort,
      "/api/v1/servers?limit=1",
      {
        method: "POST",
        headers: {
          Origin: context.frontendOrigin,
          Authorization: "Bearer observer",
          Cookie: "mcp_gateway_session=observer",
          "X-CSRF-Token": "csrf-observer",
          "Content-Type": "application/json",
          "Content-Length": String(Buffer.byteLength(body)),
          Connection: "keep-alive, x-remove-me",
          "X-Remove-Me": "remove",
          Forwarded: "for=192.0.2.1",
          "Proxy-Connection": "keep-alive",
          "X-Forwarded-For": "192.0.2.2",
          "X-End-To-End": "preserve",
        },
        body,
      },
    );
    assert.equal(response.status, 204);
    assert.equal(context.observations.length, 1);
    const observed = context.observations[0];
    assert(observed);
    assert.equal(observed.method, "POST");
    assert.equal(observed.url, "/api/v1/servers?limit=1");
    assert.equal(observed.headers.host, `127.0.0.1:${context.gatewayPort}`);
    assert.equal(
      observed.headers.origin,
      `http://127.0.0.1:${context.gatewayPort}`,
    );
    assert.equal(observed.headers.authorization, "Bearer observer");
    assert.equal(observed.headers.cookie, "mcp_gateway_session=observer");
    assert.equal(observed.headers["x-csrf-token"], "csrf-observer");
    assert.equal(observed.headers["x-end-to-end"], "preserve");
    assert.equal(observed.headers["x-remove-me"], undefined);
    assert.equal(observed.headers.forwarded, undefined);
    assert.equal(observed.headers["proxy-connection"], undefined);
    assert.equal(observed.headers["x-forwarded-for"], undefined);
    assert.equal(observed.body.toString(), body);
  });
});

test("proxy admission preserves absent Origin and rejects forbidden Origin forms", async () => {
  await withDevelopmentServer(async (context) => {
    assert.equal(
      (await exchange(context.frontendPort, "/api/v1/system-status")).status,
      204,
    );
    assert.equal(context.observations[0]?.headers.origin, undefined);

    for (const origin of [
      ["null"],
      ["http://127.0.0.1:1"],
      [context.frontendOrigin, context.frontendOrigin],
    ]) {
      const before = context.observations.length;
      const response = await exchange(
        context.frontendPort,
        "/api/v1/system-status",
        { headers: { Origin: origin } },
      );
      assert.equal(response.status, 403);
      assert.equal(context.observations.length, before);
    }
  });
});

test("proxy admission rejects confusable paths and API upgrades without upstream contact", async () => {
  await withDevelopmentServer(async (context) => {
    for (const path of [
      "/api/v1",
      "/api/v10/status",
      "/api/v1%2fstatus",
      "/api/v1/%2e%2e/status",
      "http://127.0.0.1/api/v1/status",
      "/mcp",
      "/oauth/callback",
      "/assets/app.js",
    ]) {
      const response = await exchange(context.frontendPort, path);
      assert.equal(response.status, 404, path);
    }
    assert.equal(context.observations.length, 0);

    const upgradeResponse = await rawUpgrade(
      context.frontendPort,
      "/api/v1/events",
    );
    assert.match(upgradeResponse, /^HTTP\/1\.1 404 /);
    assert.equal(context.observations.length, 0);
  });
});

test("proxy response preserves safe headers, bodies, cookies, and redirects", async () => {
  const cookie =
    "mcp_gateway_session=response-cookie; Path=/; HttpOnly; SameSite=Strict";
  const clearing =
    "mcp_gateway_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict";
  await withDevelopmentServer(
    async (context) => {
      const problem = await exchange(context.frontendPort, "/api/v1/problem");
      assert.equal(problem.status, 409);
      assert.deepEqual(problem.headers["set-cookie"], [cookie, clearing]);
      assert.equal(problem.headers.etag, '\"observer-etag\"');
      assert.equal(problem.headers["cache-control"], "no-store");
      assert.equal(problem.headers["content-type"], "application/problem+json");
      assert.equal(problem.headers["x-safe-response"], "preserved");
      assert.equal(problem.headers["x-remove-response"], undefined);
      assert.equal(problem.headers.forwarded, undefined);
      assert.equal(problem.headers["x-forwarded-for"], undefined);
      assert.equal(problem.headers["access-control-allow-origin"], undefined);
      assert.equal(problem.body.toString(), '{"code":"observer_problem"}');

      const redirect = await exchange(context.frontendPort, "/api/v1/redirect");
      assert.equal(redirect.status, 302);
      assert.equal(redirect.headers.location, "http://127.0.0.1:9/fixed");
      assert.equal(redirect.body.toString(), "redirect-body");
    },
    (incoming, response) => {
      if (incoming.url === "/api/v1/redirect") {
        response.writeHead(302, { Location: "http://127.0.0.1:9/fixed" });
        response.end("redirect-body");
        return;
      }
      const body = '{"code":"observer_problem"}';
      response.writeHead(409, {
        "Set-Cookie": [cookie, clearing],
        ETag: '\"observer-etag\"',
        "Cache-Control": "no-store",
        "Content-Type": "application/problem+json",
        "Content-Length": String(Buffer.byteLength(body)),
        Connection: "x-remove-response",
        "X-Remove-Response": "remove",
        Forwarded: "for=192.0.2.1",
        "X-Forwarded-For": "192.0.2.2",
        "Access-Control-Allow-Origin": "*",
        "X-Safe-Response": "preserved",
      });
      response.end(body);
    },
  );
});

test("proxy streaming forwards an SSE chunk before completion", async () => {
  let releaseSecondChunk: (() => void) | undefined;
  const release = new Promise<void>((resolveRelease) => {
    releaseSecondChunk = resolveRelease;
  });
  await withDevelopmentServer(
    async (context) => {
      let received = "";
      await new Promise<void>((resolveComplete, reject) => {
        const outbound = request(
          {
            host: "127.0.0.1",
            port: context.frontendPort,
            method: "POST",
            path: "/api/v1/events",
            headers: { "Content-Length": "2" },
            agent: false,
          },
          (response) => {
            assert.equal(response.statusCode, 200);
            response.on("data", (chunk: Buffer) => {
              received += chunk.toString();
              if (received === "data: first\n\n") releaseSecondChunk?.();
            });
            response.on("end", resolveComplete);
          },
        );
        outbound.once("error", reject);
        outbound.end("{}");
      });
      assert.equal(received, "data: first\n\ndata: second\n\n");
    },
    (_incoming, response) => {
      response.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-store",
      });
      response.write("data: first\n\n");
      void release.then(() => response.end("data: second\n\n"));
    },
  );
});

test("proxy cancellation closes the streaming upstream", async () => {
  let resolveCancelled: (() => void) | undefined;
  const cancelled = new Promise<void>((resolveCancellation) => {
    resolveCancelled = resolveCancellation;
  });
  await withDevelopmentServer(
    async (context) => {
      await new Promise<void>((resolveClient, reject) => {
        const outbound = request(
          {
            host: "127.0.0.1",
            port: context.frontendPort,
            method: "POST",
            path: "/api/v1/events",
            headers: { "Content-Length": "2" },
            agent: false,
          },
          (response) => {
            response.once("data", () => {
              response.destroy();
              resolveClient();
            });
          },
        );
        outbound.once("error", reject);
        outbound.end("{}");
      });
      await new Promise<void>((resolveCancellation, reject) => {
        const timeout = setTimeout(
          () => reject(new Error("upstream stream was not cancelled")),
          5_000,
        );
        void cancelled.then(() => {
          clearTimeout(timeout);
          resolveCancellation();
        });
      });
    },
    (_incoming, response) => {
      response.writeHead(200, { "Content-Type": "text/event-stream" });
      response.write("data: first\n\n");
      response.once("close", () => resolveCancelled?.());
    },
  );
});

test("proxy never replays a mutation after an uncertain handoff", async () => {
  let attempts = 0;
  await withDevelopmentServer(
    async (context) => {
      const response = await exchange(
        context.frontendPort,
        "/api/v1/mutation",
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
            "Content-Length": "2",
          },
          body: "{}",
        },
      );
      assert.equal(response.status, 502);
      assert.equal(attempts, 1);
    },
    (incoming) => {
      incoming.resume();
      incoming.once("end", () => {
        attempts += 1;
        incoming.socket.destroy();
      });
    },
  );
});

async function developmentTempContents(): Promise<Buffer> {
  const chunks: Buffer[] = [];
  const visit = async (directory: string): Promise<void> => {
    let entries;
    try {
      entries = await readdir(directory, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      const path = resolve(directory, entry.name);
      if (entry.isDirectory()) await visit(path);
      else if (entry.isFile()) chunks.push(await readFile(path));
    }
  };
  for (const entry of await readdir(tmpdir(), { withFileTypes: true })) {
    if (
      entry.isDirectory() &&
      entry.name.startsWith("mcp-gateway-ui-development-")
    ) {
      await visit(resolve(tmpdir(), entry.name));
    }
  }
  return Buffer.concat(chunks);
}

test("proxy and asset traffic leave canaries out of logs and temp state", async () => {
  await withDevelopmentServer(
    async (context) => {
      const canaries = [
        "bearer-canary-t4",
        "cookie-canary-t4",
        "csrf-canary-t4",
        "request-body-canary-t4",
        "response-body-canary-t4",
        "one-time-canary-t4",
      ] as const;
      const response = await exchange(context.frontendPort, "/api/v1/canary", {
        method: "POST",
        headers: {
          Authorization: `Bearer ${canaries[0]}`,
          Cookie: `session=${canaries[1]}`,
          "X-CSRF-Token": canaries[2] ?? "",
          "X-One-Time-Value": canaries[5] ?? "",
          "Content-Length": String(Buffer.byteLength(canaries[3] ?? "")),
        },
        body: canaries[3],
      });
      assert.equal(response.body.toString(), canaries[4]);
      await exchange(context.frontendPort, "/", {
        headers: { Cookie: `session=${canaries[1]}` },
      });
      const hmrClient = await exchange(context.frontendPort, "/@vite/client", {
        headers: { Cookie: `session=${canaries[1]}` },
      });
      const token = /const wsToken = \"([^\"]+)\"/.exec(
        hmrClient.body.toString(),
      )?.[1];
      assert(token);
      await openHmr(context.frontendOrigin, token);
      assert.equal(context.observations.length, 1);
      const sinks = Buffer.concat([
        Buffer.from(context.output()),
        await developmentTempContents(),
      ]).toString();
      for (const canary of canaries)
        assert.doesNotMatch(sinks, new RegExp(canary));
    },
    (incoming, response, observations) => {
      const chunks: Buffer[] = [];
      incoming.on("data", (chunk: Buffer) => chunks.push(chunk));
      incoming.on("end", () => {
        observations.push({
          method: incoming.method ?? "",
          url: incoming.url ?? "",
          headers: incoming.headers,
          body: Buffer.concat(chunks),
        });
        response.end("response-body-canary-t4");
      });
    },
  );
});

test("proxy admission enforces the projected body bound before handoff", async () => {
  await withDevelopmentServer(async (context) => {
    const maximum = Buffer.alloc(1_048_576, 0x61);
    assert.equal(
      (
        await exchange(context.frontendPort, "/api/v1/observer", {
          method: "POST",
          headers: { "Content-Length": String(maximum.length) },
          body: maximum,
        })
      ).status,
      204,
    );
    assert.equal(context.observations.length, 1);
    assert.equal(context.observations[0]?.body.length, maximum.length);

    const over = Buffer.alloc(maximum.length + 1, 0x62);
    assert.equal(
      (
        await exchange(context.frontendPort, "/api/v1/observer", {
          method: "POST",
          headers: { "Content-Length": String(over.length) },
          body: over,
        })
      ).status,
      413,
    );
    assert.equal(context.observations.length, 1);
  });
});
