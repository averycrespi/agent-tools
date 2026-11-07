import { parseAuditJSON } from "./audit-contract.ts";
import { validResourceKey } from "./resource-key.ts";

export const configurationInputBytes = 1024 * 1024;
export const resourceKinds = [
  "agent",
  "mcp_server",
  "mcp_grant",
  "http_credential",
  "http_grant",
  "git_credential",
  "git_repository",
  "git_grant",
] as const;
export type ResourceKind = (typeof resourceKinds)[number];
export const resourceDestinations: Record<ResourceKind, string> = {
  agent: "agents",
  mcp_server: "mcp/servers",
  mcp_grant: "mcp/grants",
  http_credential: "http/credentials",
  http_grant: "http/grants",
  git_credential: "git/credentials",
  git_repository: "git/repositories",
  git_grant: "git/grants",
};
export function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value))
    throw new Error("Invalid configuration response.");
  return value as Record<string, unknown>;
}
export function singleDefinition(
  raw: string,
  kind: ResourceKind,
): Record<string, unknown> {
  if (new TextEncoder().encode(raw).length > configurationInputBytes)
    throw new Error("Configuration must be at most 1 MiB.");
  let value: Record<string, unknown>;
  try {
    value = object(parseAuditJSON(raw));
  } catch {
    throw new Error("Enter a valid JSON resource definition.");
  }
  if ("resources" in value)
    throw new Error(
      "Bundles belong in System Configuration. Import exactly one matching resource here.",
    );
  if (
    value.format !== "agent-gateway-config" ||
    value.version !== 2 ||
    value.kind !== kind ||
    !validResourceKey(value.key)
  )
    throw new Error("Enter one matching version 2 resource definition.");
  return value;
}
export interface Creation {
  kind: ResourceKind;
  key: string;
  reserved: boolean;
  dependencies: { kind: string; key?: string }[];
  requirements: string[];
}
export function decodePreview(
  value: unknown,
  kind: ResourceKind,
  key: string,
  reserved = false,
): Creation {
  const root = object(value);
  if (
    root.advisory !== true ||
    !Array.isArray(root.creations) ||
    root.creations.length !== 1
  )
    throw new Error("Invalid configuration preview.");
  const item = object(root.creations[0]);
  if (
    item.kind !== kind ||
    item.key !== key ||
    item.reserved !== reserved ||
    !Array.isArray(item.dependencies) ||
    !Array.isArray(item.requirements) ||
    item.requirements.some(
      (v) =>
        ![
          "issue_agent_credential",
          "supply_credential_value",
          "supply_static_credentials",
          "supply_oauth_client_secret",
          "authorize_oauth_explicitly",
          "activate_after_commit",
        ].includes(String(v)),
    )
  )
    throw new Error("Invalid configuration preview.");
  for (const value of item.dependencies) {
    const ref = object(value);
    if (
      (!(resourceKinds as readonly unknown[]).includes(ref.kind) &&
        ref.kind !== "gateway_mcp") ||
      (ref.kind !== "gateway_mcp" && !validResourceKey(ref.key)) ||
      Object.keys(ref).sort().join() !==
        (ref.kind === "gateway_mcp" ? "kind" : "key,kind")
    )
      throw new Error("Invalid configuration reference.");
  }
  if (
    Object.keys(root).sort().join() !== "advisory,creations" ||
    Object.keys(item).sort().join() !==
      "dependencies,key,kind,requirements,reserved" ||
    item.dependencies.length > 3 ||
    item.requirements.length > 6
  )
    throw new Error("Invalid configuration preview.");
  return item as unknown as Creation;
}
export interface ImportResult {
  configuration: "committed";
  creations: {
    kind: ResourceKind;
    key: string;
    id: string;
    reserved: boolean;
  }[];
  activation: "pending" | "not_required";
}
export function decodeImport(
  value: unknown,
  kind: ResourceKind,
  key: string,
  reserved = false,
): ImportResult {
  const root = object(value);
  if (
    root.configuration !== "committed" ||
    !["pending", "not_required"].includes(String(root.activation)) ||
    !Array.isArray(root.creations) ||
    root.creations.length !== 1
  )
    throw new Error("Invalid configuration result.");
  const item = object(root.creations[0]);
  if (
    item.kind !== kind ||
    item.key !== key ||
    item.reserved !== reserved ||
    typeof item.id !== "string" ||
    !/^[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(item.id)
  )
    throw new Error("Invalid configuration result.");
  if (
    Object.keys(root).sort().join() !== "activation,configuration,creations" ||
    Object.keys(item).sort().join() !== "id,key,kind,reserved"
  )
    throw new Error("Invalid configuration result.");
  return root as unknown as ImportResult;
}
