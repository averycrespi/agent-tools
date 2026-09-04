import { decodeDescriptorPage, type DescriptorView } from "./server-reads";
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

export async function readMatcherDescriptors(
  session: SessionClient,
  serverID: string,
  signal?: AbortSignal,
): Promise<DescriptorView[] | undefined> {
  if (!gatewayID.test(serverID) || serverID === "00000000000000000000000000")
    return [];
  return session.runProtected(async (context) => {
    const items: DescriptorView[] = [];
    let cursor: string | null = null;
    let restarted = false;
    for (;;) {
      const query = new URLSearchParams({ limit: "100", retired: "exclude" });
      if (cursor !== null) query.set("cursor", cursor);
      const response = await fetch(
        `/api/v1/servers/${serverID}/descriptors?${query}`,
        {
          credentials: "same-origin",
          redirect: "error",
          signal:
            signal === undefined
              ? context.signal
              : AbortSignal.any([context.signal, signal]),
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
        restarted = true;
        continue;
      }
      if (
        !response.ok ||
        response.headers.get("Content-Type") !== "application/json"
      )
        throw new Error("Catalog tools are unavailable.");
      const page = decodeDescriptorPage((await response.json()) as unknown);
      items.push(...page.items.filter((item) => item.retiredAt === null));
      if (page.nextCursor === null) return items;
      cursor = page.nextCursor;
    }
  });
}
