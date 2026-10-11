// Equality uses submitted nonsecret values, never DOM material or stored secrets.
export function normalizedGitLocator(value: string): string {
  if (!/^https:\/\/[^/?#%@\\\s]+\/[A-Za-z0-9._~/-]+$/.test(value)) return value;
  const path = value.slice(value.indexOf("/", 8));
  if (
    path
      .slice(1)
      .split("/")
      .some((part) => !part || part === "." || part === "..")
  )
    return value;
  try {
    const url = new URL(value);
    if (url.port === "0") return value;
    return `https://${url.hostname}:${url.port || "443"}${path}`;
  } catch {
    return value;
  }
}

export function localExpiry(value: string | null | undefined): string {
  if (!value) return "";
  const date = new Date(value);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
}

// A minute-resolution control must not truncate an untouched stored instant.
export function submittedExpiry(
  value: string,
  baseline: string | null | undefined,
): string | null {
  if (value === localExpiry(baseline)) return baseline ?? null;
  if (!value) return null;
  const instant = Date.parse(value);
  return Number.isNaN(instant)
    ? value
    : new Date(instant).toISOString().replace(/\.000Z$/, "Z");
}
