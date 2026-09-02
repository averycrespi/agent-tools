import {
  request as requestUpstream,
  type IncomingMessage,
  type Server,
  type ServerResponse,
} from "node:http";
import type { Duplex } from "node:stream";

import { API_JSON_BODY_MAX_BYTES } from "./generated/development-contract.ts";

interface ProxyConfig {
  frontendOrigin: string;
  gateway: {
    host: string;
    port: number;
    authority: string;
    origin: string;
  };
}

const HOP_BY_HOP_HEADERS = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "proxy-connection",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
]);

function rawPath(requestTarget: string): string {
  const query = requestTarget.indexOf("?");
  return query === -1 ? requestTarget : requestTarget.slice(0, query);
}

function hasUnsafePathEncoding(path: string): boolean {
  return /%(?:2e|2f|5c)/i.test(path) || /(?:^|\/)\.{1,2}(?:\/|$)/.test(path);
}

function classifyRequestTarget(
  requestTarget: string,
): "proxy" | "reject" | "vite" {
  if (!requestTarget.startsWith("/") || requestTarget.startsWith("//")) {
    return "reject";
  }
  const path = rawPath(requestTarget);
  if (path.startsWith("/api/v1/")) {
    return hasUnsafePathEncoding(path) ? "reject" : "proxy";
  }
  if (
    path.startsWith("/api") ||
    path.startsWith("/mcp") ||
    path.startsWith("/oauth/callback") ||
    path === "/assets" ||
    path.startsWith("/assets/")
  ) {
    return "reject";
  }
  return "vite";
}

function reject(response: ServerResponse, status: number): void {
  response.writeHead(status, {
    "Cache-Control": "no-store",
    "Content-Length": "0",
  });
  response.end();
}

function connectionNominatedHeaders(values: string[] | undefined): Set<string> {
  const nominated = new Set<string>();
  for (const value of values ?? []) {
    for (const name of value.split(",")) {
      const normalized = name.trim().toLowerCase();
      if (normalized) nominated.add(normalized);
    }
  }
  return nominated;
}

function projectRequestHeaders(
  incoming: IncomingMessage,
  config: ProxyConfig,
  bodyLength: number,
): Record<string, string | string[]> | undefined {
  const origins = incoming.headersDistinct.origin ?? [];
  if (origins.length > 1) return undefined;
  if (origins.length === 1 && origins[0] !== config.frontendOrigin) {
    return undefined;
  }

  const nominated = connectionNominatedHeaders(
    incoming.headersDistinct.connection,
  );
  const projected: Record<string, string | string[]> = {
    host: config.gateway.authority,
  };
  for (const [name, values] of Object.entries(incoming.headersDistinct)) {
    const normalized = name.toLowerCase();
    if (
      normalized === "host" ||
      normalized === "origin" ||
      normalized === "content-length" ||
      HOP_BY_HOP_HEADERS.has(normalized) ||
      nominated.has(normalized) ||
      normalized === "forwarded" ||
      normalized.startsWith("x-forwarded-")
    ) {
      continue;
    }
    if (values && values.length > 0) projected[normalized] = values;
  }
  if (origins.length === 1) projected.origin = config.gateway.origin;
  if (bodyLength > 0 || incoming.headers["content-length"] !== undefined) {
    projected["content-length"] = String(bodyLength);
  }
  return projected;
}

function projectResponseHeaders(
  upstream: IncomingMessage,
): Record<string, string | string[]> {
  const nominated = connectionNominatedHeaders(
    upstream.headersDistinct.connection,
  );
  const projected: Record<string, string | string[]> = {};
  for (const [name, values] of Object.entries(upstream.headersDistinct)) {
    const normalized = name.toLowerCase();
    if (
      HOP_BY_HOP_HEADERS.has(normalized) ||
      nominated.has(normalized) ||
      normalized === "forwarded" ||
      normalized.startsWith("x-forwarded-") ||
      normalized.startsWith("access-control-")
    ) {
      continue;
    }
    if (values && values.length > 0) projected[normalized] = values;
  }
  return projected;
}

function readBoundedBody(
  incoming: IncomingMessage,
): Promise<Buffer | undefined> {
  const declared = incoming.headers["content-length"];
  if (declared !== undefined) {
    const length = Number(declared);
    if (
      !Number.isSafeInteger(length) ||
      length < 0 ||
      length > API_JSON_BODY_MAX_BYTES
    ) {
      incoming.resume();
      return Promise.resolve(undefined);
    }
  }

  return new Promise((resolveBody, rejectBody) => {
    const chunks: Buffer[] = [];
    let length = 0;
    let exceeded = false;
    incoming.on("data", (chunk: Buffer) => {
      if (exceeded) return;
      length += chunk.length;
      if (length > API_JSON_BODY_MAX_BYTES) {
        exceeded = true;
        chunks.length = 0;
        return;
      }
      chunks.push(chunk);
    });
    incoming.on("end", () => {
      resolveBody(exceeded ? undefined : Buffer.concat(chunks, length));
    });
    incoming.on("aborted", () => rejectBody(new Error("request body aborted")));
    incoming.on("error", rejectBody);
  });
}

async function proxy(
  incoming: IncomingMessage,
  response: ServerResponse,
  config: ProxyConfig,
): Promise<void> {
  let body: Buffer | undefined;
  try {
    body = await readBoundedBody(incoming);
  } catch {
    reject(response, 400);
    return;
  }
  if (body === undefined) {
    reject(response, 413);
    return;
  }

  const headers = projectRequestHeaders(incoming, config, body.length);
  if (!headers) {
    reject(response, 403);
    return;
  }

  await new Promise<void>((resolveProxy) => {
    let settled = false;
    const settle = () => {
      if (settled) return;
      settled = true;
      resolveProxy();
    };
    const upstream = requestUpstream(
      {
        protocol: "http:",
        host: config.gateway.host,
        port: config.gateway.port,
        method: incoming.method,
        path: incoming.url,
        headers,
        agent: false,
      },
      (upstreamResponse) => {
        response.writeHead(
          upstreamResponse.statusCode ?? 502,
          projectResponseHeaders(upstreamResponse),
        );
        upstreamResponse.pipe(response);
        upstreamResponse.once("end", settle);
        upstreamResponse.once("error", () => {
          response.destroy();
          settle();
        });
      },
    );
    response.once("close", () => {
      if (!response.writableEnded) upstream.destroy();
      settle();
    });
    upstream.once("error", () => {
      if (!response.headersSent) reject(response, 502);
      else response.destroy();
      settle();
    });
    if (body.length > 0) upstream.write(body);
    upstream.end();
  });
}

export async function handleDevelopmentRequest(
  incoming: IncomingMessage,
  response: ServerResponse,
  config: ProxyConfig,
): Promise<boolean> {
  const classification = classifyRequestTarget(incoming.url ?? "");
  if (classification === "vite") return false;
  if (classification === "reject") {
    incoming.resume();
    reject(response, 404);
    return true;
  }
  await proxy(incoming, response, config);
  return true;
}

function rejectUpgrade(socket: Duplex): void {
  socket.end(
    "HTTP/1.1 404 Not Found\r\nCache-Control: no-store\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
  );
}

export function installUpgradeAdmission(server: Server): void {
  const viteListeners = server.listeners("upgrade");
  server.removeAllListeners("upgrade");
  server.on("upgrade", (request, socket, head) => {
    if (classifyRequestTarget(request.url ?? "") !== "vite") {
      rejectUpgrade(socket);
      return;
    }
    for (const listener of viteListeners) {
      listener.call(server, request, socket, head);
    }
  });
}
