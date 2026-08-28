import { render } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import {
  synchronizeFragment,
  type Destination,
  type ResolvedLocation,
} from "./location";
import { MutationCoordinator, type MutationAvailability } from "./mutation";
import {
  SessionClient,
  type SessionLifecycle,
  type SessionSnapshot,
} from "./session";
import { ViewCoordinator, type ViewSnapshot } from "./view";
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
const sessionClient = new SessionClient();
const viewCoordinator = new ViewCoordinator(sessionClient);
const mutationCoordinator = new MutationCoordinator(sessionClient, {
  refreshCurrent: () => viewCoordinator.manualRefresh(),
});
applyTheme(initialTheme);

function SignInPanel({
  snapshot,
  onAuthenticated,
}: {
  snapshot: SessionSnapshot;
  onAuthenticated: () => void;
}) {
  const bearerInput = useRef<HTMLInputElement>(null);
  const [submitting, setSubmitting] = useState(false);

  const submit = async (event: SubmitEvent) => {
    event.preventDefault();
    const input = bearerInput.current;
    if (input === null || input.value.length === 0 || submitting) return;
    let candidate = input.value;
    input.value = "";
    setSubmitting(true);
    const exchange = sessionClient.exchange(candidate);
    candidate = "";
    const authenticated = await exchange;
    input.value = "";
    setSubmitting(false);
    if (authenticated) onAuthenticated();
    else input.focus();
  };

  return (
    <section class="panel sign-in-panel" aria-labelledby="sign-in-title">
      <div class="panel-heading">
        <div>
          <span class="panel-code">AUTH-01</span>
          <h2 id="sign-in-title">Administrator session</h2>
        </div>
        <span class="classification">LOCAL ONLY</span>
      </div>
      <p>
        Exchange a current administrator bearer for a local browser session. The
        bearer is cleared as soon as the request is handed off and is never
        saved by this application.
      </p>
      <form autocomplete="off" onSubmit={submit}>
        <label class="credential-field">
          <span>Administrator bearer</span>
          <input
            ref={bearerInput}
            data-testid="admin-bearer-input"
            type="password"
            autocomplete="off"
            autocapitalize="none"
            spellcheck={false}
            disabled={submitting || snapshot.lifecycle !== "signed_out"}
            required
          />
        </label>
        <button
          data-testid="sign-in-submit"
          type="submit"
          disabled={submitting || snapshot.lifecycle !== "signed_out"}
        >
          {submitting ? "Signing in…" : "Sign in"}
        </button>
      </form>
      {snapshot.message !== undefined && (
        <p class="session-message" role="alert" data-testid="session-message">
          {snapshot.message}
        </p>
      )}
    </section>
  );
}

function SessionTransition({ lifecycle }: { lifecycle: SessionLifecycle }) {
  return (
    <section class="panel session-transition" aria-live="polite">
      <span class="panel-code">AUTH-00</span>
      <h2>
        {lifecycle === "bootstrapping" ? "Restoring session" : "Session lost"}
      </h2>
      <p>
        {lifecycle === "bootstrapping"
          ? "Checking for current local browser authority."
          : "Discarding prior authority and checking once for a current session."}
      </p>
    </section>
  );
}

function App() {
  const [session, setSession] = useState<SessionSnapshot>(
    sessionClient.snapshot(),
  );
  const [resolved, setResolved] = useState<ResolvedLocation>(initialLocation);
  const [view, setView] = useState<ViewSnapshot>(viewCoordinator.snapshot());
  const [mutationAvailability, setMutationAvailability] =
    useState<MutationAvailability>(mutationCoordinator.snapshot());
  const [theme, setTheme] = useState<ThemePreference>(initialTheme);
  const priorLifecycle = useRef<SessionLifecycle>(session.lifecycle);

  useEffect(() => {
    const unsubscribe = sessionClient.subscribe(setSession);
    sessionClient.start();
    return unsubscribe;
  }, []);

  useEffect(() => viewCoordinator.subscribe(setView), []);
  useEffect(() => mutationCoordinator.subscribe(setMutationAvailability), []);

  useEffect(() => {
    const authenticated = session.lifecycle === "authenticated";
    const prior = priorLifecycle.current;
    if (
      authenticated &&
      (window.location.hash === "#/sign-in" || window.location.hash === "")
    ) {
      window.history.replaceState(null, "", "#/overview");
    } else if (
      session.lifecycle === "signed_out" &&
      (prior === "authenticated" ||
        prior === "reauthenticating" ||
        prior === "bootstrapping")
    ) {
      window.history.replaceState(null, "", "#/sign-in");
    }
    priorLifecycle.current = session.lifecycle;
    const nextLocation = synchronizeFragment(authenticated);
    setResolved(nextLocation);
    if (authenticated) viewCoordinator.activate(nextLocation.canonicalFragment);
  }, [session.lifecycle, session.epoch]);

  useEffect(() => {
    const synchronize = () => {
      const nextLocation = synchronizeFragment(
        session.lifecycle === "authenticated",
      );
      setResolved(nextLocation);
      if (session.lifecycle === "authenticated")
        viewCoordinator.navigate(nextLocation.canonicalFragment);
    };
    window.addEventListener("hashchange", synchronize);
    return () => window.removeEventListener("hashchange", synchronize);
  }, [session.lifecycle]);

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
  const authenticated = session.lifecycle === "authenticated";

  return (
    <div
      class={`shell ${authenticated ? "authenticated" : "signed-out"}`}
      data-testid="gateway-shell"
      data-session-lifecycle={session.lifecycle}
      data-view-generation={view.generation}
      data-freshness={view.freshness}
      data-mutation-availability={mutationAvailability}
    >
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
          {authenticated && (
            <button
              class="quiet-action"
              data-testid="logout"
              type="button"
              onClick={() => void sessionClient.logout()}
            >
              Sign out
            </button>
          )}
        </div>
      </header>
      {authenticated && (
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
      )}
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
              {authenticated ? destinationLabel : "Administrator session"}
            </h1>
            <p>
              Inspect and operate this local Gateway through its public API.
            </p>
          </div>
          <span class={`status ${authenticated ? "connected" : ""}`}>
            <span aria-hidden="true" />
            {authenticated ? "Authenticated" : "Authentication required"}
          </span>
        </section>
        {session.lifecycle === "bootstrapping" ||
        session.lifecycle === "reauthenticating" ? (
          <SessionTransition lifecycle={session.lifecycle} />
        ) : authenticated ? (
          <section class="panel" aria-labelledby="foundation-title">
            <div class="panel-heading">
              <div>
                <span class="panel-code">SESSION-01</span>
                <h2 id="foundation-title">Session established</h2>
              </div>
              <span class="classification">IN MEMORY</span>
            </div>
            <p>
              Protected views bind to this location and authentication epoch.
              Prior requests and scheduled work cannot update the current
              session.
            </p>
            <div class="refresh-controls">
              <span class={`freshness ${view.freshness}`} role="status">
                Data {view.freshness}
              </span>
              <button
                data-testid="manual-refresh"
                type="button"
                onClick={() => viewCoordinator.manualRefresh()}
              >
                Refresh visible data
              </button>
            </div>
          </section>
        ) : (
          <SignInPanel
            snapshot={session}
            onAuthenticated={() => setResolved(synchronizeFragment(true))}
          />
        )}
      </main>
      <footer>
        <span>Gateway authority stays in this process</span>
        <span>NO REMOTE ASSETS</span>
      </footer>
    </div>
  );
}

render(<App />, document.getElementById("app")!);
