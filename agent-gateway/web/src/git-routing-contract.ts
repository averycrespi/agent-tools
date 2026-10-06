import { object } from "./git-contract.ts";

export interface GitRoutingProfile {
  origins: string[];
  revision: string;
  active: boolean;
}

export function normalizeGitOrigin(value: string): string {
  if (
    value.length > 4096 ||
    !/^[\x21-\x7e]+$/.test(value) ||
    !/^https:\/\/[^/?#%@\\\s]+$/.test(value)
  )
    throw new Error(
      "Enter an HTTPS origin without a path, credentials, query or fragment.",
    );
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new Error("Enter an exact HTTPS host and a valid port.");
  }
  if (!url.hostname || url.port === "0" || url.hostname.includes("*"))
    throw new Error("Enter an exact HTTPS host and a valid port.");
  return `https://${url.hostname}:${url.port || "443"}`;
}

// Valid aliases share the canonical origin; a .git suffix cannot change coverage.
export function gitOriginCovered(
  destination: string,
  profile: GitRoutingProfile,
): boolean | undefined {
  try {
    const match = /^https:\/\/([^/?#@%\\\s]+)(\/[A-Za-z0-9._~/-]+)$/.exec(
      destination,
    );
    if (
      destination.length > 4096 ||
      !match ||
      match[2]!
        .slice(1)
        .split("/")
        .some((part) => part === "" || part === "." || part === "..")
    )
      return undefined;
    const url = new URL(destination);
    return profile.origins.includes(normalizeGitOrigin(`https://${url.host}`));
  } catch {
    return undefined;
  }
}

export function normalizeGitOrigins(values: string[]): string[] {
  if (values.length > 256) throw new Error("Use at most 256 origins.");
  const origins = values.map(normalizeGitOrigin).sort();
  if (new Set(origins).size !== origins.length)
    throw new Error("Each HTTPS origin must be unique.");
  return origins;
}

export function decodeGitRoutingProfile(value: unknown): GitRoutingProfile {
  const r = object(value, ["origins", "revision", "active"]);
  if (
    typeof r.active !== "boolean" ||
    typeof r.revision !== "string" ||
    !/^[1-9][0-9]*$/.test(r.revision) ||
    BigInt(r.revision) > 9223372036854775807n ||
    !Array.isArray(r.origins) ||
    r.origins.some((v) => typeof v !== "string") ||
    JSON.stringify(normalizeGitOrigins(r.origins as string[])) !==
      JSON.stringify(r.origins)
  )
    throw new Error("Git routing profile is unavailable.");
  return value as GitRoutingProfile;
}

export function gitRoutingETag(profile: GitRoutingProfile): string {
  return `"git-profile-routing-${profile.revision}"`;
}

export async function decodeGitRoutingResponse(
  response: Response,
): Promise<GitRoutingProfile> {
  if (response.headers.get("Content-Type") !== "application/json")
    throw new Error("Git routing profile is unavailable.");
  const body = await response.text();
  if (body.length > 1024 * 1024)
    throw new Error("Git routing profile is unavailable.");
  const profile = decodeGitRoutingProfile(JSON.parse(body));
  if (response.headers.get("ETag") !== gitRoutingETag(profile))
    throw new Error("Git routing revision is unavailable.");
  return profile;
}
