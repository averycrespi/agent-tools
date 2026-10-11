export interface InvocationRecognition {
  server: { serverID: string; displayName: string } | null;
  grant: { grantID: string; description: string } | null;
}

function record(value: unknown, keys: string[]): Record<string, unknown> {
  if (
    typeof value !== "object" ||
    value === null ||
    Array.isArray(value) ||
    Object.keys(value).sort().join(",") !== keys.sort().join(",")
  )
    throw new Error("invalid invocation recognition");
  return value as Record<string, unknown>;
}

function label(value: unknown, empty: boolean): string {
  if (
    typeof value !== "string" ||
    (!empty && value.length === 0) ||
    new TextEncoder().encode(value).byteLength > 256
  )
    throw new Error("invalid invocation recognition");
  return value;
}

function identity(value: unknown, expected: string | undefined): string {
  if (
    typeof value !== "string" ||
    !/^[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(value) ||
    value !== expected
  )
    throw new Error("invalid invocation recognition identity");
  return value;
}

// Older responses omit this sidecar. Null labels do not establish deletion.
export function decodeInvocationRecognition(
  value: unknown,
  serverID: string | undefined,
  grantID: string | undefined,
): InvocationRecognition | undefined {
  if (value === undefined) return undefined;
  const r = record(value, ["server", "grant"]);
  const server =
    r.server === null ? null : record(r.server, ["server_id", "display_name"]);
  const grant =
    r.grant === null ? null : record(r.grant, ["grant_id", "description"]);
  return {
    server:
      server === null
        ? null
        : {
            serverID: identity(server.server_id, serverID),
            displayName: label(server.display_name, false),
          },
    grant:
      grant === null
        ? null
        : {
            grantID: identity(grant.grant_id, grantID),
            description: label(grant.description, true),
          },
  };
}
