import { validActivityRange } from "./protocol-summary.ts";
export const gitTrafficOptions: Readonly<Record<string, readonly string[]>> = {
  operation: [
    "read_discovery",
    "read",
    "push_discovery",
    "probe",
    "push",
    "invalid",
  ],
  admission: ["allowed", "blocked"],
  transport: [
    "not_dispatched",
    "unknown",
    "prestart_failure",
    "complete",
    "incomplete",
  ],
  report: [
    "not_a_push",
    "unknown",
    "reported_success",
    "reported_failure",
    "reported_partial",
  ],
};
export function validGitTrafficQuery(
  query: Readonly<Record<string, string>>,
): boolean {
  return (
    validActivityRange(query) &&
    Object.entries(query).every(([key, value]) =>
      key === "filter_from" || key === "filter_until"
        ? true
        : key === "filter_repository"
          ? value.length > 0 &&
            new TextEncoder().encode(value).length <= 256 &&
            !/[\p{Cc}\p{Cf}]/u.test(value)
          : key.startsWith("filter_") &&
            gitTrafficOptions[key.slice(7)]?.includes(value) === true,
    )
  );
}
