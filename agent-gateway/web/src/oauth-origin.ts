// Match internal/remote.ParseOrigin without WHATWG URL host/port normalization.
export function validOAuthOrigin(origin: string): boolean {
  if (origin.length > 8192) return false;
  const match = /^https:\/\/([a-z0-9.-]+)(?::([0-9]+))?$/.exec(origin);
  if (match === null || match[0] !== origin) return false;
  const host = match[1]!;
  const port = match[2];
  if (
    host.length > 253 ||
    host
      .split(".")
      .some(
        (label) =>
          label.length > 63 || !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label),
      ) ||
    (port !== undefined &&
      (Number(port) < 1 || Number(port) > 65535 || Number(port) === 443))
  )
    return false;
  const labels = host.split(".");
  return !(
    labels.length === 4 &&
    labels.every(
      (label) => /^(0|[1-9][0-9]{0,2})$/.test(label) && Number(label) <= 255,
    )
  );
}
