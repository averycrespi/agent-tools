export const historyDownloadLimits = {
  bytes: 64 * 1024 * 1024,
  milliseconds: 120_000,
  pages: 4096,
} as const;

export type HistoryProgress = { records: number; pages: number; bytes: number };
export type HistoryExport = HistoryProgress & {
  blob: Blob;
  newRecords: boolean;
};

export class HistoryExportError extends Error {}

const invalid = () =>
  new HistoryExportError(
    "History coverage could not be verified. No file was downloaded.",
  );
const decimal = /^(0|[1-9][0-9]{0,18})$/;
const id = /^[0-7][0-9A-HJKMNP-TV-Z]{25}$/;
function sequence(value: unknown): bigint {
  if (typeof value !== "string" || !decimal.test(value)) throw invalid();
  const result = BigInt(value);
  if (result > 9223372036854775807n) throw invalid();
  return result;
}

type Page = {
  generation: string;
  installation: string;
  high: bigint;
  pruning: bigint;
  retained: number;
  next: bigint;
  truncated: boolean;
  count: number;
  captured: string;
};

function validatePage(raw: string, after: bigint, through?: bigint): Page {
  const value = JSON.parse(raw) as Record<string, unknown>;
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
    typeof value.absence !== "string" ||
    typeof value.captured_at !== "string" ||
    value.captured_at.length > 64 ||
    !Number.isFinite(Date.parse(value.captured_at)) ||
    sequence(value.after_sequence) !== after
  )
    throw invalid();
  const high = sequence(value.high_water),
    pruning = sequence(value.pruning),
    next = sequence(value.next_sequence);
  if (high - pruning !== BigInt(value.retained)) throw invalid();
  let previous = after;
  for (const record of value.records) {
    if (record === null || typeof record !== "object" || Array.isArray(record))
      throw invalid();
    const current = sequence(record.sequence);
    const protocol = record.protocol;
    if (
      current <= previous ||
      current > (through ?? high) ||
      !["mcp", "http", "git"].includes(protocol) ||
      record[protocol] === null ||
      typeof record[protocol] !== "object" ||
      Array.isArray(record[protocol]) ||
      ["mcp", "http", "git"].some((key) => key !== protocol && key in record)
    )
      throw invalid();
    previous = current;
  }
  if (next !== previous || (value.truncated && next === after)) throw invalid();
  return {
    generation: value.generation,
    installation: value.installation_id,
    high,
    pruning,
    retained: value.retained,
    next,
    truncated: value.truncated,
    count: value.records.length,
    captured: value.captured_at,
  };
}

async function readPage(
  csrfToken: string,
  signal: AbortSignal,
  after: bigint,
  through: bigint | undefined,
  sessionLost?: (response: Response) => Promise<boolean>,
): Promise<{ raw: string; bytes: number }> {
  const query =
    through === undefined
      ? ""
      : `?after_sequence=${after}&through_sequence=${through}&limit=256`;
  const response = await fetch(`/api/v2/history/export${query}`, {
    method: "GET",
    credentials: "same-origin",
    cache: "no-store",
    redirect: "error",
    headers: { "X-CSRF-Token": csrfToken },
    signal: AbortSignal.any([signal, AbortSignal.timeout(5000)]),
  });
  if (await sessionLost?.(response)) throw invalid();
  if (
    !response.ok ||
    response.headers.get("Content-Type") !== "application/json" ||
    response.body === null
  )
    throw new HistoryExportError(
      "Traffic history is unavailable. No file was downloaded. Backup controls remain independent.",
    );
  const reader = response.body.getReader();
  const decoder = new TextDecoder("utf-8", { fatal: true });
  let bytes = 0,
    raw = "";
  try {
    for (;;) {
      signal.throwIfAborted();
      const chunk = await reader.read();
      if (chunk.done) break;
      bytes += chunk.value.byteLength;
      if (bytes > 900 * 1024)
        throw new HistoryExportError(
          "A history response exceeds its safe bound. No file was downloaded.",
        );
      raw += decoder.decode(chunk.value, { stream: true });
    }
    raw += decoder.decode();
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
  return { raw, bytes };
}

// Keep original response bytes in the file: parsing is only for coverage checks,
// never reserialization of protocol evidence or potentially large JSON numbers.
export async function readHistoryExport(
  csrfToken: string,
  signal: AbortSignal,
  onProgress: (progress: HistoryProgress) => void = () => {},
  sessionLost?: (response: Response) => Promise<boolean>,
): Promise<HistoryExport> {
  const bounded = AbortSignal.any([
    signal,
    AbortSignal.timeout(historyDownloadLimits.milliseconds),
  ]);
  const pages: string[] = [];
  let initial: Page | undefined,
    after = 0n,
    records = 0,
    bytes = 0,
    latestHigh = 0n;
  try {
    for (let index = 0; index < historyDownloadLimits.pages; index++) {
      bounded.throwIfAborted();
      const response = await readPage(
        csrfToken,
        bounded,
        after,
        initial?.high,
        sessionLost,
      );
      bounded.throwIfAborted();
      const page = validatePage(response.raw, after, initial?.high);
      initial ??= page;
      if (
        page.generation !== initial.generation ||
        page.installation !== initial.installation
      )
        throw new HistoryExportError(
          "Traffic history was replaced during export. No file was downloaded. Start a new export.",
        );
      if (page.pruning !== initial.pruning)
        throw new HistoryExportError(
          "Traffic history was pruned during export. No file was downloaded. Start a new export.",
        );
      if (page.high < latestHigh || page.high < initial.high) throw invalid();
      latestHigh = page.high;
      bytes += response.bytes;
      if (bytes > historyDownloadLimits.bytes)
        throw new HistoryExportError(
          "Traffic history exceeds the 64 MiB browser limit. No file was downloaded. Use the documented bounded CLI export procedure.",
        );
      records += page.count;
      if (records > initial.retained) throw invalid();
      pages.push(response.raw);
      onProgress({ records, pages: pages.length, bytes });
      after = page.next;
      if (!page.truncated) {
        if (
          records !== initial.retained ||
          (records > 0 && after !== initial.high)
        )
          throw invalid();
        const coverage = JSON.stringify({
          initial_high_water: String(initial.high),
          final_high_water: String(latestHigh),
          initial_pruning: String(initial.pruning),
          initial_retained: initial.retained,
          returned_records: records,
          page_count: pages.length,
          started_at: initial.captured,
          finished_at: page.captured,
          retained_boundary_traversed: true,
          atomic_snapshot: false,
          new_records_excluded: latestHigh > initial.high,
          completion_changes:
            "Each page observes its own read transaction; late completions on earlier pages are not refreshed. Missing completion remains unknown.",
        });
        bounded.throwIfAborted();
        const blob = new Blob(
          [
            `{"format":"agent-gateway-retained-traffic-v1","complete_traffic_audit":false,"coverage":${coverage},"pages":[`,
            ...pages.flatMap((raw, i) => (i === 0 ? [raw] : [",", raw])),
            "]}\n",
          ],
          { type: "application/json" },
        );
        return {
          blob,
          records,
          pages: pages.length,
          bytes,
          newRecords: latestHigh > initial.high,
        };
      }
      if (after >= initial.high) throw invalid();
    }
    throw new HistoryExportError(
      "Traffic history exceeds the browser page limit. No file was downloaded. Use the documented bounded CLI export procedure.",
    );
  } finally {
    pages.length = 0;
  }
}
