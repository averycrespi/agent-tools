export type ThemePreference = "system" | "light" | "dark";
export type ResolvedTheme = "light" | "dark";

export const THEME_STORAGE_KEY = "mcp_gateway_theme";
const darkPreference = "(prefers-color-scheme: dark)";

function isThemePreference(value: string | null): value is ThemePreference {
  return value === "system" || value === "light" || value === "dark";
}

export function readThemePreference(): ThemePreference {
  try {
    const value = window.localStorage.getItem(THEME_STORAGE_KEY);
    if (isThemePreference(value)) return value;
    if (value !== null) window.localStorage.removeItem(THEME_STORAGE_KEY);
  } catch {
    return "system";
  }
  return "system";
}

export function writeThemePreference(value: ThemePreference): void {
  try {
    window.localStorage.setItem(THEME_STORAGE_KEY, value);
  } catch {
    // Presentation remains usable when browser persistence is unavailable.
  }
}

export function resolveTheme(value: ThemePreference): ResolvedTheme {
  if (value !== "system") return value;
  return window.matchMedia(darkPreference).matches ? "dark" : "light";
}

export function applyTheme(value: ThemePreference): void {
  document.documentElement.dataset.theme = resolveTheme(value);
  document.documentElement.dataset.themePreference = value;
}

export function observeSystemTheme(
  preference: ThemePreference,
  listener: () => void,
): () => void {
  if (preference !== "system") return () => {};
  const media = window.matchMedia(darkPreference);
  media.addEventListener("change", listener);
  return () => media.removeEventListener("change", listener);
}
