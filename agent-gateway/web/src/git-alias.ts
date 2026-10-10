// Only the create form generates aliases; the backend keeps exact URL identity.
export function oppositeGitAlias(value: string): string | null {
  try {
    const url = new URL(value);
    if (
      url.protocol !== "https:" ||
      url.username ||
      url.password ||
      url.search ||
      url.hash ||
      url.pathname === "/" ||
      url.pathname.endsWith("/") ||
      /[%?#\\\\]/.test(value) ||
      !url.pathname
        .slice(1)
        .split("/")
        .every((segment) => /^[A-Za-z0-9._~-]+$/.test(segment))
    )
      return null;
    const path = url.pathname.endsWith(".git")
      ? url.pathname.slice(0, -4)
      : `${url.pathname}.git`;
    if (!path || path.endsWith("/")) return null;
    url.pathname = path;
    return url.href;
  } catch {
    return null;
  }
}

export function validGitAliases(url: string, aliases: string[]): boolean {
  const canonical = (value: string) => {
    try {
      return new URL(value).href;
    } catch {
      return value;
    }
  };
  const values = aliases.map(canonical);
  return (
    !values.includes(canonical(url)) && new Set(values).size === values.length
  );
}
