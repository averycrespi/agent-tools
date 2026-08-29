export const visualDestinations = [
  {
    id: "overview",
    route: "#/overview",
    selector: '[data-testid="overview-view"]',
  },
  {
    id: "servers",
    route: "#/servers",
    selector: '[data-testid="servers-view"]',
  },
  {
    id: "catalog",
    route: "#/catalog",
    selector: '[data-testid="catalog-view"]',
  },
  {
    id: "access",
    route: "#/access/principals",
    selector: '[data-testid="principals-view"]',
  },
  {
    id: "requests",
    route: "#/requests",
    selector: '[data-testid="requests-view"]',
  },
  {
    id: "invocations",
    route: "#/invocations",
    selector: '[data-testid="invocations-view"]',
  },
  { id: "system", route: "#/system", selector: '[data-testid="system-view"]' },
] as const;

export const visualStates = [
  { id: "signed-out", owner: "overview", secretBearing: false },
  { id: "loading", owner: "overview", secretBearing: false },
  { id: "empty", owner: "requests", secretBearing: false },
  { id: "error", owner: "system", secretBearing: false },
  { id: "populated", owner: "servers", secretBearing: false },
  { id: "stale", owner: "invocations", secretBearing: false },
  { id: "uncertain", owner: "system", secretBearing: false },
  { id: "confirmation", owner: "access", secretBearing: false },
  { id: "one-time-secret", owner: "access", secretBearing: true },
  { id: "long-content", owner: "catalog", secretBearing: false },
] as const;

export const visualViewports = [
  { id: "desktop", width: 1440, height: 900, zoom: 1 },
  { id: "mobile", width: 390, height: 844, zoom: 1 },
] as const;

export const visualThemes = ["light", "dark"] as const;

export const visualArtifactInventory = [
  ...visualDestinations.flatMap((destination) =>
    visualThemes.flatMap((theme) =>
      visualViewports.map((viewport) => ({
        id: `${destination.id}-${theme}-${viewport.id}`,
        destination: destination.id,
        route: destination.route,
        selector: destination.selector,
        theme,
        viewport,
        secretBearing: false,
      })),
    ),
  ),
  ...visualDestinations.map((destination) => ({
    id: `${destination.id}-light-320px`,
    destination: destination.id,
    route: destination.route,
    selector: destination.selector,
    theme: "light" as const,
    viewport: { id: "narrow", width: 320, height: 800, zoom: 1 },
    secretBearing: false,
  })),
  ...visualDestinations.map((destination) => ({
    id: `${destination.id}-dark-200pct`,
    destination: destination.id,
    route: destination.route,
    selector: destination.selector,
    theme: "dark" as const,
    viewport: { id: "zoom", width: 720, height: 450, zoom: 2 },
    secretBearing: false,
  })),
] as const;

export const visualRubric = [
  "comparison hierarchy remains explicit",
  "primary and destructive actions remain distinguishable",
  "status is conveyed by text and symbol, not color alone",
  "tables scroll inside labeled regions rather than the page",
  "long identifiers and evidence wrap without covering controls",
  "focus, dialog, and live-region states remain visible",
] as const;
