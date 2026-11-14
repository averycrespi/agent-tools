import { parseAuditJSON } from "./audit-contract.ts";
import { validResourceKey } from "./resource-key.ts";
import {
  configurationInputBytes,
  object,
  resourceKinds,
  decodePreview,
  decodeImport,
  type DefinitionIdentity,
  type Creation,
  type ImportResult,
} from "./config-contract.ts";

// Inspect identities only. The exact original bytes go to preview/import and sinks;
// parsing must never round authored numeric tokens through JSON.stringify.
export function bundleDefinitions(
  raw: string,
  maxBytes = configurationInputBytes,
): DefinitionIdentity[] {
  if (new TextEncoder().encode(raw).length > maxBytes)
    throw new Error("Configuration must be at most 1 MiB.");
  let root: Record<string, unknown>;
  try {
    root = object(parseAuditJSON(raw));
  } catch {
    throw new Error("Enter a valid JSON configuration bundle.");
  }
  if (root.version === 1)
    throw new Error(
      "Version 1 is not supported. Export version 2 from the source Gateway.",
    );
  if (
    root.format !== "agent-gateway-config" ||
    root.version !== 2 ||
    !Array.isArray(root.resources) ||
    Object.keys(root).sort().join() !== "format,resources,version"
  )
    throw new Error(
      "Enter a version 2 configuration bundle with a resources array.",
    );
  const seen = new Set<string>();
  return root.resources.map((value) => {
    const item = object(value);
    const routing = item.kind === "git_routing";
    if (
      item.format !== root.format ||
      item.version !== 2 ||
      (!routing &&
        !(resourceKinds as readonly unknown[]).includes(item.kind)) ||
      (routing
        ? item.key !== undefined || item.reserved === true
        : !validResourceKey(item.key)) ||
      (item.reserved !== undefined && typeof item.reserved !== "boolean")
    )
      throw new Error(
        "Every definition needs its explicit version 2 type and immutable key; Git routing is a keyless singleton.",
      );
    const identity = {
      kind: item.kind,
      key: item.key ?? "",
      reserved: item.reserved === true,
    } as DefinitionIdentity;
    const token = `${identity.kind}/${identity.key}`;
    if (seen.has(token))
      throw new Error(
        "Duplicate typed key. Keep exactly one definition per type and key.",
      );
    seen.add(token);
    return identity;
  });
}
export function decodeBundlePreview(
  value: unknown,
  definitions: DefinitionIdentity[],
): Creation[] {
  const root = object(value);
  if (
    Object.keys(root).sort().join() !== "advisory,creations" ||
    root.advisory !== true ||
    !Array.isArray(root.creations) ||
    root.creations.length !== definitions.length
  )
    throw new Error("Incomplete configuration preview.");
  const creations = root.creations;
  return definitions.map((d, index) =>
    decodePreview(
      { advisory: true, creations: [creations[index]] },
      d.kind,
      d.key,
      d.reserved,
    ),
  );
}
export function decodeBundleImport(
  value: unknown,
  definitions: DefinitionIdentity[],
): ImportResult {
  const root = object(value);
  if (
    root.configuration !== "committed" ||
    !["pending", "not_required"].includes(String(root.activation)) ||
    Object.keys(root).sort().join() !== "activation,configuration,creations" ||
    !Array.isArray(root.creations) ||
    root.creations.length !== definitions.length
  )
    throw new Error("Incomplete configuration result.");
  const creations = root.creations;
  definitions.forEach((d, index) =>
    decodeImport(
      { ...root, creations: [creations[index]] },
      d.kind,
      d.key,
      d.reserved,
    ),
  );
  return root as unknown as ImportResult;
}
