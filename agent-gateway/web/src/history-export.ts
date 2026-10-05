export type HistoryExport = {
  generation: string;
  records: number;
  retained: number;
  truncated: boolean;
  nextSequence: string;
  json: string;
};

export async function readHistoryExport(
  csrfToken: string,
  signal: AbortSignal,
): Promise<HistoryExport> {
  const response = await fetch("/api/v2/history/export", {
    method: "GET",
    credentials: "same-origin",
    cache: "no-store",
    redirect: "error",
    headers: { "X-CSRF-Token": csrfToken },
    signal: AbortSignal.any([signal, AbortSignal.timeout(5000)]),
  });
  if (
    !response.ok ||
    response.headers.get("Content-Type") !== "application/json"
  )
    throw new Error("history export unavailable");
  if (response.body === null) throw new Error("history export unavailable");
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let bytes = 0;
  let raw = "";
  try {
    for (;;) {
      const chunk = await reader.read();
      if (chunk.done) break;
      bytes += chunk.value.byteLength;
      if (bytes > 900 * 1024) throw new Error("history export exceeds bound");
      raw += decoder.decode(chunk.value, { stream: true });
    }
    raw += decoder.decode();
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
  const value = JSON.parse(raw) as Record<string, unknown>;
  const id = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
  const decimal = /^(0|[1-9][0-9]*)$/;
  if (
    value === null ||
    typeof value !== "object" ||
    Array.isArray(value) ||
    value.format !== 1 ||
    typeof value.generation !== "string" ||
    !id.test(value.generation) ||
    typeof value.installation_id !== "string" ||
    !id.test(value.installation_id) ||
    value.complete_traffic_audit !== false ||
    !Array.isArray(value.records) ||
    value.records.length > 256 ||
    typeof value.retained !== "number" ||
    !Number.isSafeInteger(value.retained) ||
    value.retained < value.records.length ||
    typeof value.truncated !== "boolean" ||
    typeof value.next_sequence !== "string" ||
    !decimal.test(value.next_sequence) ||
    typeof value.absence !== "string"
  )
    throw new Error("invalid history export");
  return {
    generation: value.generation,
    records: value.records.length,
    retained: value.retained,
    truncated: value.truncated,
    nextSequence: value.next_sequence,
    json: JSON.stringify(value, null, 2),
  };
}
