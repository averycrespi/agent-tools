import { mkdtemp, rm } from "node:fs/promises";
import { createServer, type Server } from "node:http";
import { tmpdir } from "node:os";
import { resolve } from "node:path";
import { createServer as createViteServer, type ViteDevServer } from "vite";

import {
  handleDevelopmentRequest,
  installUpgradeAdmission,
} from "./dev-proxy.ts";

const DEFAULT_LISTEN = "127.0.0.1:5173";
const DEFAULT_GATEWAY = "http://127.0.0.1:8210";
const AUTHORITY_PATTERN =
  /^(127\.(?:0|[1-9]\d{0,2})\.(?:0|[1-9]\d{0,2})\.(?:0|[1-9]\d{0,2})):((?:0|[1-9]\d{0,4}))$/;

interface Authority {
  host: string;
  port: number;
  authority: string;
}

export interface DevelopmentConfig {
  listen: Authority;
  gateway: Authority & { origin: string };
  frontendOrigin: string;
}

function parseAuthority(value: string, selector: string): Authority {
  const match = AUTHORITY_PATTERN.exec(value);
  if (!match) {
    throw new Error(
      `${selector} must be a canonical numeric IPv4 127/8 authority with an explicit port`,
    );
  }
  const host = match[1];
  const portText = match[2];
  if (!host || !portText) throw new Error(`${selector} is invalid`);

  const octets = host.split(".").map(Number);
  const port = Number(portText);
  if (octets.some((octet) => octet > 255) || port < 1 || port > 65_535) {
    throw new Error(
      `${selector} must use a valid 127/8 address and a port from 1 to 65535`,
    );
  }
  return { host, port, authority: `${host}:${port}` };
}

export function parseDevelopmentConfig(
  environment: Record<string, string | undefined>,
  args: readonly string[],
): DevelopmentConfig {
  if (args.length !== 0) {
    throw new Error(
      "ui:dev does not accept arguments or pass-through Vite options",
    );
  }

  const listen = parseAuthority(
    environment.MCP_GATEWAY_UI_LISTEN ?? DEFAULT_LISTEN,
    "MCP_GATEWAY_UI_LISTEN",
  );
  const gatewayValue = environment.MCP_GATEWAY_UI_GATEWAY ?? DEFAULT_GATEWAY;
  if (!gatewayValue.startsWith("http://")) {
    throw new Error(
      "MCP_GATEWAY_UI_GATEWAY must be a canonical http://127/8 URL with an explicit port",
    );
  }
  const gatewayAuthority = parseAuthority(
    gatewayValue.slice("http://".length),
    "MCP_GATEWAY_UI_GATEWAY",
  );
  const gateway = {
    ...gatewayAuthority,
    origin: `http://${gatewayAuthority.authority}`,
  };

  return {
    listen,
    gateway,
    frontendOrigin: `http://${listen.authority}`,
  };
}

function listen(server: Server, config: DevelopmentConfig): Promise<void> {
  return new Promise((resolveListen, reject) => {
    const onError = (error: NodeJS.ErrnoException) => {
      if (error.code === "EADDRINUSE") {
        reject(new Error(`${config.listen.authority} is already in use`));
        return;
      }
      reject(
        new Error(
          `could not listen on ${config.listen.authority}: ${error.message}`,
        ),
      );
    };
    server.once("error", onError);
    server.listen(
      { host: config.listen.host, port: config.listen.port, exclusive: true },
      () => {
        server.off("error", onError);
        resolveListen();
      },
    );
  });
}

function close(server: Server): Promise<void> {
  return new Promise((resolveClose, reject) => {
    server.close((error) => (error ? reject(error) : resolveClose()));
    server.closeAllConnections();
  });
}

export async function runDevelopmentServer(
  environment: Record<string, string | undefined> = process.env,
  args: readonly string[] = process.argv.slice(2),
): Promise<void> {
  const config = parseDevelopmentConfig(environment, args);
  const developmentRoot = await mkdtemp(
    resolve(tmpdir(), "mcp-gateway-ui-development-"),
  );
  const server = createServer();
  let vite: ViteDevServer | undefined;

  try {
    vite = await createViteServer({
      configFile: false,
      root: import.meta.dirname,
      base: "/",
      publicDir: false,
      cacheDir: resolve(developmentRoot, "cache"),
      appType: "spa",
      clearScreen: false,
      logLevel: "silent",
      optimizeDeps: {
        noDiscovery: true,
        include: [],
      },
      build: {
        outDir: resolve(developmentRoot, "output"),
        emptyOutDir: true,
      },
      server: {
        middlewareMode: { server },
        cors: false,
        ws: {
          server,
          host: config.listen.host,
          clientPort: config.listen.port,
        },
      },
    });
    const activeVite = vite;
    installUpgradeAdmission(server);
    server.on("request", (incoming, response) => {
      void handleDevelopmentRequest(incoming, response, config).then(
        (handled) => {
          if (!handled) activeVite.middlewares(incoming, response);
        },
        () => {
          if (!response.headersSent) {
            response.writeHead(500, {
              "Cache-Control": "no-store",
              "Content-Length": "0",
            });
            response.end();
          } else {
            response.destroy();
          }
        },
      );
    });

    await listen(server, config);
    process.stdout.write(`Frontend: ${config.frontendOrigin} (ready)\n`);
    process.stdout.write(
      `Gateway: ${config.gateway.origin} (independent process)\n`,
    );
    process.stdout.write(
      "Development only: trusted local proxy handles administrator authentication and session traffic\n",
    );

    await new Promise<void>((resolveShutdown) => {
      const shutdown = () => resolveShutdown();
      process.once("SIGINT", shutdown);
      process.once("SIGTERM", shutdown);
    });
    await close(server);
    await activeVite.waitForRequestsIdle();
    vite = undefined;
    await activeVite.close();
  } finally {
    if (vite) await vite.close();
    await rm(developmentRoot, { recursive: true, force: true });
  }
}

if (process.argv[1] && import.meta.filename === resolve(process.argv[1])) {
  runDevelopmentServer().catch((error: unknown) => {
    const message =
      error instanceof Error ? error.message : "development server failed";
    process.stderr.write(`ui:dev: ${message}\n`);
    process.exitCode = 1;
  });
}
