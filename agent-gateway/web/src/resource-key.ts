export function validResourceKey(value: unknown): value is string {
  return typeof value === "string" && /^[a-z][a-z0-9_-]{0,63}$/.test(value);
}
