export const invocationOptions = {
  decision: [
    ["allow", "Allow"],
    ["deny", "Deny"],
    ["block", "Block"],
    ["not_evaluated", "Not evaluated"],
  ],
  outcome: [
    ["succeeded", "Succeeded"],
    ["downstream_failure", "Downstream failure"],
    ["outcome_unknown", "Outcome unknown"],
    ["deny", "Deny"],
    ["block", "Block"],
    ["invalid_params", "Invalid parameters"],
    ["unknown_tool", "Unknown tool"],
    ["invalid_arguments", "Invalid arguments"],
    ["authorization_unavailable", "Authorization unavailable"],
    ["prestart_failure", "Prestart failure"],
  ],
} as const;

export function validInvocationQuery(
  query: Readonly<Record<string, string>>,
): boolean {
  return Object.entries(query).every(([key, value]) => {
    if (["filter_tool", "filter_principal"].includes(key))
      return (
        new TextEncoder().encode(value).byteLength <= 256 &&
        !/[\p{Cc}\p{Cf}]/u.test(value)
      );
    if (key === "filter_decision" || key === "filter_outcome")
      return invocationOptions[
        key === "filter_decision" ? "decision" : "outcome"
      ].some(([choice]) => choice === value);
    return false;
  });
}
