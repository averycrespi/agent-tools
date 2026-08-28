import { render } from "preact";
import { useEffect, useState } from "preact/hooks";
import {
  synchronizeFragment,
  type Destination,
  type ResolvedLocation,
} from "./location";
import {
  applyTheme,
  observeSystemTheme,
  readThemePreference,
  writeThemePreference,
  type ThemePreference,
} from "./theme";
import "./styles.css";

const navigation: ReadonlyArray<{
  destination: Exclude<Destination, "sign-in">;
  label: string;
  href: string;
}> = [
  { destination: "overview", label: "Overview", href: "#/overview" },
  { destination: "servers", label: "Servers", href: "#/servers" },
  { destination: "catalog", label: "Catalog", href: "#/catalog" },
  {
    destination: "access",
    label: "Access",
    href: "#/access/principals",
  },
  { destination: "requests", label: "Requests", href: "#/requests" },
  {
    destination: "invocations",
    label: "Invocations",
    href: "#/invocations",
  },
  { destination: "system", label: "System", href: "#/system" },
];

const destinationLabels: Readonly<Record<Destination, string>> = {
  overview: "Overview",
  servers: "Servers",
  catalog: "Catalog",
  access: "Access",
  requests: "Requests",
  invocations: "Invocations",
  system: "System",
  "sign-in": "Sign in",
};

const initialLocation = synchronizeFragment(false);
const initialTheme = readThemePreference();
applyTheme(initialTheme);

function App() {
  const [resolved, setResolved] = useState<ResolvedLocation>(initialLocation);
  const [theme, setTheme] = useState<ThemePreference>(initialTheme);

  useEffect(() => {
    const synchronize = () => setResolved(synchronizeFragment(false));
    window.addEventListener("hashchange", synchronize);
    return () => window.removeEventListener("hashchange", synchronize);
  }, []);

  useEffect(() => {
    applyTheme(theme);
    return observeSystemTheme(theme, () => applyTheme(theme));
  }, [theme]);

  const chooseTheme = (value: ThemePreference) => {
    writeThemePreference(value);
    setTheme(value);
  };
  const destination = resolved.location.destination;
  const destinationLabel = destinationLabels[destination];

  return (
    <div class="shell" data-testid="gateway-shell">
      <header class="masthead">
        <a class="wordmark" href="#/overview" aria-label="MCP Gateway overview">
          <span aria-hidden="true" class="mark">
            MG
          </span>
          <span>MCP Gateway</span>
        </a>
        <div class="masthead-controls">
          <span class="environment">LOCAL CONTROL PLANE</span>
          <label class="theme-control">
            <span>Theme</span>
            <select
              data-testid="theme-preference"
              value={theme}
              onChange={(event) =>
                chooseTheme(event.currentTarget.value as ThemePreference)
              }
            >
              <option value="system">System</option>
              <option value="light">Light</option>
              <option value="dark">Dark</option>
            </select>
          </label>
        </div>
      </header>
      <aside class="rail" aria-label="Primary navigation">
        <nav>
          {navigation.map((item, index) => {
            const active = destination === item.destination;
            return (
              <a
                class={active ? "active" : undefined}
                href={item.href}
                aria-current={active ? "page" : undefined}
              >
                <span class="nav-index">
                  {String(index + 1).padStart(2, "0")}
                </span>
                {item.label}
              </a>
            );
          })}
        </nav>
      </aside>
      <main class="workspace">
        {resolved.invalid && (
          <p
            class="location-notice"
            role="status"
            data-testid="location-notice"
          >
            The requested location was invalid. A safe location was restored.
          </p>
        )}
        <div class="eyebrow">SYSTEM / {destinationLabel.toUpperCase()}</div>
        <section class="intro" aria-labelledby="page-title">
          <div>
            <h1 id="page-title">
              {destination === "sign-in"
                ? "Administrator session"
                : destinationLabel}
            </h1>
            <p>Authenticate to inspect and operate this local Gateway.</p>
          </div>
          <span class="status">
            <span aria-hidden="true" /> Ready for sign-in
          </span>
        </section>
        <section class="panel" aria-labelledby="sign-in-title">
          <div class="panel-heading">
            <div>
              <span class="panel-code">AUTH-01</span>
              <h2 id="sign-in-title">Administrator session</h2>
            </div>
            <span class="classification">LOCAL ONLY</span>
          </div>
          <p>
            Product workflows load from this embedded same-origin bundle.
            Browser locations contain only closed non-sensitive identifiers and
            filters.
          </p>
          <button type="button" disabled>
            Sign in
          </button>
        </section>
      </main>
      <footer>
        <span>Gateway authority stays in this process</span>
        <span>NO REMOTE ASSETS</span>
      </footer>
    </div>
  );
}

render(<App />, document.getElementById("app")!);
