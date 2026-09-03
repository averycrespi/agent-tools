import type { ProtectedContext, SessionClient } from "./session";

type JSONRecord = Record<string, unknown>;

function record(value: unknown, keys: readonly string[]): JSONRecord {
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("Matcher validation returned an invalid response.");
  const result = value as JSONRecord;
  if (Object.keys(result).sort().join(",") !== [...keys].sort().join(","))
    throw new Error("Matcher validation returned an invalid response.");
  return result;
}

function text(value: unknown): string {
  if (typeof value !== "string")
    throw new Error("Matcher validation returned an invalid response.");
  return value;
}

function headers(context: ProtectedContext): HeadersInit {
  return {
    Accept: "application/json",
    "Content-Type": "application/json",
    "X-CSRF-Token": context.csrfToken,
  };
}

export async function validateMatcherConstraint(
  session: SessionClient,
  constraint: string,
): Promise<string | null | undefined> {
  return session.runProtected(async (context) => {
    const response = await fetch("/api/v1/grant-constraints/validate", {
      method: "POST",
      credentials: "same-origin",
      redirect: "error",
      signal: context.signal,
      headers: headers(context),
      body: `{"constraint":${constraint}}`,
    });
    if (await context.sessionLost(response)) return undefined;
    if (
      response.status !== 200 ||
      response.headers.get("Content-Type") !== "application/json"
    )
      throw new Error("Matcher validation is unavailable.");
    const result = record((await response.json()) as unknown, [
      "valid",
      "diagnostics",
    ]);
    if (typeof result.valid !== "boolean" || !Array.isArray(result.diagnostics))
      throw new Error("Matcher validation returned an invalid response.");
    if (result.valid) {
      if (result.diagnostics.length !== 0)
        throw new Error("Matcher validation returned an invalid response.");
      return null;
    }
    if (result.diagnostics.length !== 1)
      throw new Error("Matcher validation returned an invalid response.");
    const diagnostic = record(result.diagnostics[0], ["field", "message"]);
    return `${text(diagnostic.field)}: ${text(diagnostic.message)}`;
  });
}
