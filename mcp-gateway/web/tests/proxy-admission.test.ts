import assert from "node:assert/strict";
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { createServer, request, type IncomingHttpHeaders } from "node:http";
import { connect } from "node:net";
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

async function withDevelopmentServer(
  run: (context: {
    frontendPort: number;
    gatewayPort: number;
    frontendOrigin: string;
    observations: Observation[];
  }) => Promise<void>,
): Promise<void> {
  const observations: Observation[] = [];
  const backend = createServer((incoming, response) => {
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
