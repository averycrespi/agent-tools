export const readOnlyExplanation =
  "Includes current and future tools marked readOnlyHint=true. The Gateway does not verify that these tools are read-only; they may still change data.";

export function readOnlyKeys(value: unknown): string[] {
  return typeof value === "object" &&
    value !== null &&
    Object.prototype.hasOwnProperty.call(value, "read_only")
    ? ["read_only"]
    : [];
}

export function decodeReadOnly(item: Record<string, unknown>): boolean {
  if ("read_only" in item && typeof item.read_only !== "boolean")
    throw new Error("invalid response");
  return item.read_only === true;
}

export function readOnlyMember(readOnly: boolean): string {
  return readOnly ? ',"read_only":true' : "";
}
