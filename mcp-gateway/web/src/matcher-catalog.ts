import { decodeDescriptorResource, type DescriptorView } from "./server-reads";
import type { SessionClient } from "./session";

const gatewayID = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
type JSONRecord = Record<string, unknown>;

export interface MatcherFieldSuggestion {
  pointer: string;
  type: "null" | "boolean" | "string" | "number";
  description: string | null;
  values: string[];
  regexAvailable: boolean;
}

export interface MatcherSchemaSuggestions {
  fields: MatcherFieldSuggestion[];
  unsupported: boolean;
}

function object(value: unknown): JSONRecord | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value)
    ? (value as JSONRecord)
    : undefined;
}

function pointerToken(value: string): string {
  return value.replaceAll("~", "~0").replaceAll("/", "~1");
}

const schemaMetadata = new Set([
  "$id",
  "$schema",
  "default",
  "deprecated",
  "description",
  "examples",
  "readOnly",
  "title",
  "writeOnly",
]);
function hasUnsupportedKeywords(
  schema: JSONRecord,
  supported: readonly string[],
): boolean {
  const allowed = new Set([...schemaMetadata, ...supported]);
  return Object.keys(schema).some((key) => !allowed.has(key));
}

export function matcherSchemaSuggestions(
  descriptor: unknown,
): MatcherSchemaSuggestions {
  const document = object(descriptor);
  const root = object(document?.inputSchema);
  const fields: MatcherFieldSuggestion[] = [];
  let unsupported = root === undefined;
  const visit = (schema: JSONRecord, segments: string[], depth: number) => {
    if (depth > 16 || fields.length >= 256) {
      unsupported = true;
      return;
    }
    const properties = object(schema.properties);
    if (
      schema.type !== "object" ||
      properties === undefined ||
      hasUnsupportedKeywords(schema, [
        "type",
        "properties",
        "required",
        "additionalProperties",
        "minProperties",
        "maxProperties",
      ]) ||
      schema.additionalProperties !== false
    )
      unsupported = true;
    if (schema.type !== "object" || properties === undefined) return;
    for (const [name, value] of Object.entries(properties)) {
      if (fields.length >= 256) {
        unsupported = true;
        break;
      }
      const property = object(value);
      if (property === undefined) {
        unsupported = true;
        continue;
      }
      const path = [...segments, name];
      if (property.type === "object") {
        visit(property, path, depth + 1);
        continue;
      }
      if (hasUnsupportedKeywords(property, ["type", "enum"]))
        unsupported = true;
      const type =
        property.type === "integer" || property.type === "number"
          ? "number"
          : property.type === "null" ||
              property.type === "boolean" ||
              property.type === "string"
            ? property.type
            : undefined;
      if (type === undefined) {
        unsupported = true;
        continue;
      }
      if (property.enum !== undefined && !Array.isArray(property.enum))
        unsupported = true;
      const enumValues = Array.isArray(property.enum) ? property.enum : [];
      const values = enumValues
        .filter((item) =>
          type === "null"
            ? item === null
            : type === "number"
              ? typeof item === "number"
              : typeof item === type,
        )
        .map((item) => (item === null ? "null" : String(item)));
      if (values.length !== enumValues.length) unsupported = true;
      const pointer = `/${path.map(pointerToken).join("/")}`;
      if (new TextEncoder().encode(pointer).length > 256) {
        unsupported = true;
        continue;
      }
      fields.push({
        pointer,
        type,
        description:
          typeof property.description === "string"
            ? property.description
            : null,
        values,
        regexAvailable: type === "string",
      });
    }
  };
  if (root !== undefined) visit(root, [], 0);
  return { fields, unsupported };
}

export interface MatcherDescriptorSummary {
  id: string;
  serverID: string;
  upstreamName: string;
  externalName: string;
  catalogRevision: string;
}

function text(value: unknown): string {
  if (typeof value !== "string") throw new Error("invalid response");
  return value;
}

function decodeSummaryPage(value: unknown): {
  items: MatcherDescriptorSummary[];
  nextCursor: string | null;
} {
  const page = object(value);
  if (
    page === undefined ||
    Object.keys(page).sort().join(",") !== "items,next_cursor" ||
    !Array.isArray(page.items) ||
    page.items.length > 100 ||
    (page.next_cursor !== null &&
      (typeof page.next_cursor !== "string" ||
        page.next_cursor === "" ||
        new TextEncoder().encode(page.next_cursor).length > 512))
  )
    throw new Error("invalid response");
  return {
    items: page.items.map((value) => {
      const item = object(value);
      if (
        item === undefined ||
        Object.keys(item).sort().join(",") !==
          "catalog_revision,external_name,id,server_id,upstream_name"
      )
        throw new Error("invalid response");
      const id = text(item.id);
      const serverID = text(item.server_id);
      if (!gatewayID.test(id) || !gatewayID.test(serverID))
        throw new Error("invalid response");
      return {
        id,
        serverID,
        upstreamName: text(item.upstream_name),
        externalName: text(item.external_name),
        catalogRevision: text(item.catalog_revision),
      };
    }),
    nextCursor: page.next_cursor,
  };
}

function requestSignal(
  context: AbortSignal,
  signal?: AbortSignal,
): AbortSignal {
  return signal === undefined ? context : AbortSignal.any([context, signal]);
}

export async function readMatcherDescriptors(
  session: SessionClient,
  serverID: string,
  signal?: AbortSignal,
): Promise<MatcherDescriptorSummary[] | undefined> {
  if (!gatewayID.test(serverID) || serverID === "00000000000000000000000000")
    return [];
  return session.runProtected(async (context) => {
    const items: MatcherDescriptorSummary[] = [];
    let cursor: string | null = null;
    let restarted = false;
    const seenCursors = new Set<string>();
    for (;;) {
      const query = new URLSearchParams({
        limit: "100",
        retired: "exclude",
        representation: "summary",
      });
      if (cursor !== null) query.set("cursor", cursor);
      const response = await fetch(
        `/api/v1/servers/${serverID}/descriptors?${query}`,
        {
          credentials: "same-origin",
          redirect: "error",
          signal: requestSignal(context.signal, signal),
          headers: {
            Accept: "application/json",
            "X-CSRF-Token": context.csrfToken,
          },
        },
      );
      if (await context.sessionLost(response)) return undefined;
      if (response.status === 409 && cursor !== null && !restarted) {
        items.length = 0;
        cursor = null;
        seenCursors.clear();
        restarted = true;
        continue;
      }
      if (
        !response.ok ||
        response.headers.get("Content-Type") !== "application/json"
      )
        throw new Error("Catalog tools are unavailable.");
      const page = decodeSummaryPage((await response.json()) as unknown);
      if (page.items.some((item) => item.serverID !== serverID))
        throw new Error("Catalog pagination returned the wrong server.");
      if (items.length + page.items.length > 256)
        throw new Error("Catalog tools exceed the supported limit.");
      items.push(...page.items);
      if (page.nextCursor === null) return items;
      if (seenCursors.has(page.nextCursor) || items.length >= 256)
        throw new Error("Catalog pagination is invalid.");
      seenCursors.add(page.nextCursor);
      cursor = page.nextCursor;
    }
  });
}

export async function readMatcherDescriptor(
  session: SessionClient,
  summary: MatcherDescriptorSummary,
  signal?: AbortSignal,
): Promise<DescriptorView | undefined> {
  if (!gatewayID.test(summary.serverID) || !gatewayID.test(summary.id))
    return undefined;
  return session.runProtected(async (context) => {
    const response = await fetch(
      `/api/v1/servers/${summary.serverID}/descriptors/${summary.id}`,
      {
        credentials: "same-origin",
        redirect: "error",
        signal: requestSignal(context.signal, signal),
        headers: {
          Accept: "application/json",
          "X-CSRF-Token": context.csrfToken,
        },
      },
    );
    if (await context.sessionLost(response)) return undefined;
    if (
      !response.ok ||
      response.headers.get("Content-Type") !== "application/json"
    )
      throw new Error("The selected tool schema is unavailable.");
    const descriptor = decodeDescriptorResource(
      (await response.json()) as unknown,
    );
    if (
      descriptor.id !== summary.id ||
      descriptor.serverID !== summary.serverID ||
      descriptor.upstreamName !== summary.upstreamName ||
      descriptor.externalName !== summary.externalName ||
      descriptor.retiredAt !== null
    )
      throw new Error("The selected tool schema is no longer current.");
    return descriptor;
  });
}
