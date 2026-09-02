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
}: {
  value: string | null | undefined;
  fallback?: string;
}) {
  if (value === null || value === undefined) return <>{fallback}</>;
  return (
    <time dateTime={value} title={value}>
      {formatUserTime(value)}
    </time>
  );
}
