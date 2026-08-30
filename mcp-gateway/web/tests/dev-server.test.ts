import assert from "node:assert/strict";
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { createHash } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import test from "node:test";

import { parseDevelopmentConfig } from "../dev-server.ts";

const repositoryRoot = resolve(import.meta.dirname, "../../..");
const entryPoint = resolve(import.meta.dirname, "../dev-server.ts");

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
  });
}

function launch(
  listenAuthority: string,
  gateway = "http://127.0.0.1:8210",
  args: string[] = [],
): { child: ChildProcess; output: () => string } {
  const child = spawn(process.execPath, [entryPoint, ...args], {
    cwd: repositoryRoot,
    env: {
      ...process.env,
      MCP_GATEWAY_UI_LISTEN: listenAuthority,
      MCP_GATEWAY_UI_GATEWAY: gateway,
      MCP_GATEWAY_UI_SECRET_CANARY: "must-not-appear",
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

function waitForOutput(
  child: ChildProcess,
  output: () => string,
  expected: RegExp,
): Promise<void> {
  return new Promise((resolveWait, reject) => {
    const timeout = setTimeout(() => {
      cleanup();
      child.kill("SIGKILL");
      reject(new Error(`timed out waiting for ${expected}: ${output()}`));
    }, 5_000);
    const check = () => {
      if (expected.test(output())) {
        cleanup();
        resolveWait();
      }
    };
    const exited = () => {
      cleanup();
      reject(new Error(`process exited before ${expected}: ${output()}`));
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

async function developmentTemporaryRoots(): Promise<string[]> {
  return (await readdir(tmpdir(), { withFileTypes: true }))
    .filter(
      (entry) =>
        entry.isDirectory() &&
        entry.name.startsWith("mcp-gateway-ui-development-"),
    )
    .map((entry) => entry.name)
    .sort();
}

async function staticDigest(): Promise<string> {
  const staticRoot = resolve(repositoryRoot, "mcp-gateway/internal/api/static");
  const hash = createHash("sha256");
  for (const name of (await readdir(staticRoot)).sort()) {
    hash.update(name);
    hash.update(await readFile(resolve(staticRoot, name)));
  }
  return hash.digest("hex");
}

async function workspaceDigest(): Promise<string> {
  const files = spawnSync("git", ["ls-files", "-z"], {
    cwd: repositoryRoot,
    encoding: "utf8",
  });
  assert.equal(files.status, 0, files.stderr);
  const status = spawnSync(
    "git",
    ["status", "--porcelain=v1", "--untracked-files=all"],
    { cwd: repositoryRoot, encoding: "utf8" },
  );
  assert.equal(status.status, 0, status.stderr);

  const hash = createHash("sha256").update(status.stdout);
  for (const path of files.stdout.split("\0").filter(Boolean)) {
    hash.update(path);
    hash.update(await readFile(resolve(repositoryRoot, path)));
  }
  return hash.digest("hex");
}

function waitForHmr(frontendOrigin: string, token: string): Promise<void> {
  return new Promise((resolveHmr, reject) => {
    const socket = new WebSocket(
      `${frontendOrigin.replace("http://", "ws://")}?token=${token}`,
      "vite-hmr",
    );
    const timeout = setTimeout(() => {
      socket.close();
      reject(new Error("timed out connecting to Vite HMR"));
    }, 5_000);
    socket.addEventListener("open", () => {
      socket.close();
    });
    socket.addEventListener("close", () => {
      clearTimeout(timeout);
      resolveHmr();
    });
    socket.addEventListener("error", () => {
      clearTimeout(timeout);
      reject(new Error("Vite HMR connection failed"));
    });
  });
}

function waitForExit(
  child: ChildProcess,
): Promise<{ code: number | null; signal: NodeJS.Signals | null }> {
  return new Promise((resolveExit, reject) => {
    const timeout = setTimeout(() => {
      child.kill("SIGKILL");
      reject(new Error("timed out waiting for development server to exit"));
    }, 5_000);
    child.once("exit", (code, signal) => {
      clearTimeout(timeout);
      resolveExit({ code, signal });
    });
  });
}

test("selector defaults and canonical loopback overrides", () => {
  assert.deepEqual(parseDevelopmentConfig({}, []), {
    listen: { host: "127.0.0.1", port: 5173, authority: "127.0.0.1:5173" },
    gateway: {
      host: "127.0.0.1",
      port: 8210,
      authority: "127.0.0.1:8210",
      origin: "http://127.0.0.1:8210",
    },
    frontendOrigin: "http://127.0.0.1:5173",
  });
  assert.deepEqual(
    parseDevelopmentConfig(
      {
        MCP_GATEWAY_UI_LISTEN: "127.12.34.56:49152",
        MCP_GATEWAY_UI_GATEWAY: "http://127.255.0.1:65535",
      },
      [],
    ),
    {
      listen: {
        host: "127.12.34.56",
        port: 49152,
        authority: "127.12.34.56:49152",
      },
      gateway: {
        host: "127.255.0.1",
        port: 65535,
        authority: "127.255.0.1:65535",
        origin: "http://127.255.0.1:65535",
      },
      frontendOrigin: "http://127.12.34.56:49152",
    },
  );
});

for (const value of [
  "localhost:5173",
  "0.0.0.0:5173",
  "128.0.0.1:5173",
  "127.0.0.1",
  "127.0.0.1:0",
  "127.0.0.1:65536",
  "127.00.0.1:5173",
  "127.0.0.256:5173",
  "[::1]:5173",
  "http://127.0.0.1:5173",
  "127.0.0.1:5173/path",
]) {
  test(`selector rejects invalid frontend authority ${value}`, () => {
    assert.throws(
      () => parseDevelopmentConfig({ MCP_GATEWAY_UI_LISTEN: value }, []),
      /MCP_GATEWAY_UI_LISTEN/,
    );
  });
}

for (const value of [
  "127.0.0.1:8210",
  "https://127.0.0.1:8210",
  "http://localhost:8210",
  "http://0.0.0.0:8210",
  "http://127.0.0.1",
  "http://127.0.0.1:0",
  "http://127.0.0.1:8210/",
  "http://user@127.0.0.1:8210",
  "http://127.0.0.1:8210?target=x",
  "http://127.0.0.1:8210#fragment",
]) {
  test(`selector rejects invalid Gateway target ${value}`, () => {
    assert.throws(
      () => parseDevelopmentConfig({ MCP_GATEWAY_UI_GATEWAY: value }, []),
      /MCP_GATEWAY_UI_GATEWAY/,
    );
  });
}

test("startup rejects appended options", () => {
  assert.throws(
    () => parseDevelopmentConfig({}, ["--host", "0.0.0.0"]),
    /does not accept arguments/,
  );
});

test("startup is strict, emits safe bounded output, and settles on SIGTERM", async () => {
  const reservation = createServer();
  const port = await listen(reservation);
  await close(reservation);

  const process = launch(`127.0.0.1:${port}`);
  try {
    await waitForOutput(
      process.child,
      process.output,
      /Development only: trusted local proxy/,
    );
    const output = process.output();
    assert.match(
      output,
      new RegExp(`Frontend: http://127\\.0\\.0\\.1:${port} \\(ready\\)`),
    );
    assert.match(
      output,
      /Gateway: http:\/\/127\.0\.0\.1:8210 \(independent process\)/,
    );
    assert.match(
      output,
      /Development only: trusted local proxy handles administrator authentication and session traffic/,
    );
    assert.doesNotMatch(output, /must-not-appear/);

    process.child.kill("SIGTERM");
    assert.deepEqual(await waitForExit(process.child), {
      code: 0,
      signal: null,
    });
  } finally {
    if (process.child.exitCode === null) process.child.kill("SIGKILL");
  }
});

test("source server serves authored modules and owns HMR without workspace output", async () => {
  const workspaceBefore = await workspaceDigest();
  const staticBefore = await staticDigest();
  const temporaryBefore = await developmentTemporaryRoots();
  const reservation = createServer();
  const port = await listen(reservation);
  await close(reservation);

  const process = launch(`127.0.0.1:${port}`);
  const frontendOrigin = `http://127.0.0.1:${port}`;
  try {
    await waitForOutput(
      process.child,
      process.output,
      /Development only: trusted local proxy/,
    );
    const indexResponse = await fetch(`${frontendOrigin}/`, {
      signal: AbortSignal.timeout(5_000),
    });
    assert.equal(indexResponse.status, 200);
    const index = await indexResponse.text();
    assert.match(index, /\/@vite\/client/);
    assert.match(index, /\/src\/main\.tsx/);

    const clientResponse = await fetch(`${frontendOrigin}/@vite/client`, {
      signal: AbortSignal.timeout(5_000),
    });
    assert.equal(clientResponse.status, 200);
    const client = await clientResponse.text();
    const token = /const wsToken = \"([^\"]+)\"/.exec(client)?.[1];
    assert(token);

    const sourceResponse = await fetch(`${frontendOrigin}/src/main.tsx`, {
      signal: AbortSignal.timeout(5_000),
    });
    assert.equal(sourceResponse.status, 200);
    assert.match(await sourceResponse.text(), /import \"\/src\/styles\.css\"/);
    await waitForHmr(frontendOrigin, token);
    assert.doesNotMatch(process.output(), /GET \/|\/src\/main\.tsx/);

    process.child.kill("SIGTERM");
    assert.deepEqual(await waitForExit(process.child), {
      code: 0,
      signal: null,
    });
    assert.equal(await staticDigest(), staticBefore);
    assert.equal(await workspaceDigest(), workspaceBefore);
    assert.deepEqual(await developmentTemporaryRoots(), temporaryBefore);
  } finally {
    if (process.child.exitCode === null) process.child.kill("SIGKILL");
  }
});

test("startup fails clearly when the frontend port is occupied", async () => {
  const reservation = createServer();
  const port = await listen(reservation);
  const process = launch(`127.0.0.1:${port}`);
  try {
    const exit = await waitForExit(process.child);
    assert.equal(exit.code, 1);
    assert.match(
      process.output(),
      new RegExp(`127\\.0\\.0\\.1:${port} is already in use`),
    );
    assert.doesNotMatch(process.output(), /\(ready\)/);
  } finally {
    if (process.child.exitCode === null) process.child.kill("SIGKILL");
    await close(reservation);
  }
});
