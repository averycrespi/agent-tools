export function formatUserTime(value: string): string {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "medium",
  }).format(date);
}

export function UserTime({
  value,
  fallback = "—",
  compact = false,
}: {
  value: string | null | undefined;
  fallback?: string;
  compact?: boolean;
}) {
  if (value === null || value === undefined) return <>{fallback}</>;
  const date = new Date(value);
  if (!Number.isFinite(date.getTime()))
    return <time dateTime={value}>{value}</time>;
  if (compact)
    return (
      <time
        dateTime={value}
        title={value}
        aria-label={`${formatUserTime(value)} (${value})`}
      >
        {new Intl.DateTimeFormat(undefined, {
          dateStyle: "short",
          timeStyle: "short",
        }).format(date)}
      </time>
    );
  const parts = new Intl.DateTimeFormat(undefined, {
    dateStyle: "medium",
    timeStyle: "medium",
  }).formatToParts(date);
  const timeStart = parts.findIndex(
    (part) => part.type === "hour" || part.type === "dayPeriod",
  );
  // Preserve the localized separator inline; tables split only at the date/time boundary.
  const separatorStart =
    parts[timeStart - 1]?.type === "literal" ? timeStart - 1 : timeStart;
  return (
    <time dateTime={value} title={value} class="user-time">
      <span class="user-time-date">
        {parts
          .slice(0, separatorStart)
          .map((part) => part.value)
          .join("")}
      </span>
      <span class="user-time-separator">
        {parts
          .slice(separatorStart, timeStart)
          .map((part) => part.value)
          .join("")}
      </span>
      <span class="user-time-clock">
        {parts
          .slice(timeStart)
          .map((part) => part.value)
          .join("")}
      </span>
    </time>
  );
}
