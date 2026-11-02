export type CredentialMaterialState =
  | "not_configured"
  | "ready"
  | "unavailable";

export function validCredentialMaterial(
  state: unknown,
  available: unknown,
): boolean {
  return available === true
    ? state === "ready"
    : available === false &&
        (state === "not_configured" || state === "unavailable");
}

export function credentialMaterialLabel(value: {
  material_state?: CredentialMaterialState;
  available?: boolean;
}): string {
  return value.material_state === "not_configured"
    ? "Not configured"
    : value.available
      ? "Configured"
      : "Unavailable";
}
