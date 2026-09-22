// Inputs are the nonnegative safe integers accepted by decodeStatus.
export function resourceUtilization(inUse: number, limit: number): string {
  if (limit === 0) return "N/A";
  // Scale and round exactly even when the percentage exceeds safe-integer range.
  const numerator = BigInt(inUse) * 1000n;
  const denominator = BigInt(limit);
  let tenths = numerator / denominator;
  if ((numerator % denominator) * 2n >= denominator) tenths += 1n;
  if (inUse < limit && tenths >= 1000n) tenths = 999n;
  const fraction = tenths % 10n;
  return `${tenths / 10n}${fraction === 0n ? "" : `.${fraction}`}%`;
}
