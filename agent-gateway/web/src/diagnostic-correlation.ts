export interface DiagnosticCorrelation {
  processID: string;
  upstreamRef: string;
}

export function decodeDiagnosticCorrelation(
  value: unknown,
): DiagnosticCorrelation | null {
  if (value === undefined) return null;
  if (typeof value !== "object" || value === null || Array.isArray(value))
    throw new Error("invalid response");
  const item = value as Record<string, unknown>;
  if (
    Object.keys(item).sort().join(",") !== "process_id,upstream_ref" ||
    typeof item.process_id !== "string" ||
    !/^[0-9a-f]{32}$/.test(item.process_id) ||
    typeof item.upstream_ref !== "string" ||
    !/^[1-9]\d{0,19}$/.test(item.upstream_ref) ||
    BigInt(item.upstream_ref) > 18446744073709551615n
  )
    throw new Error("invalid response");
  return { processID: item.process_id, upstreamRef: item.upstream_ref };
}
