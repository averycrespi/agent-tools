import { render } from "preact";
import { useEffect, useRef, useState } from "preact/hooks";
import {
  replaceForLifecycle,
  synchronizeFragment,
  type Destination,
  type ResolvedLocation,
} from "./location";
import { Grants } from "./grants";
import { Invocations, InvocationsController } from "./invocations";
import { MutationCoordinator, type MutationAvailability } from "./mutation";
import { Overview, OverviewController } from "./overview";
import { Principals } from "./principals";
import { Requests } from "./requests";
import {
  ConfirmationDialog,
  FormField,
  StateNotice,
  StatusLabel,
} from "./primitives";
import { ServerReads, ServerReadsController } from "./server-reads";
import { SensitiveSinkHost } from "./sinks-ui";
import { SensitiveSinkCoordinator } from "./sinks";
import { System, SystemController } from "./system";
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
const sensitiveSinkCoordinator = new SensitiveSinkCoordinator(sessionClient);
const viewCoordinator = new ViewCoordinator(sessionClient);
const mutationCoordinator = new MutationCoordinator(sessionClient, {
  refreshCurrent: () => viewCoordinator.manualRefresh(),
});
const overviewController = new OverviewController(
  sessionClient,
  viewCoordinator,
  (latched) => mutationCoordinator.setStorageLatched(latched),
);
const invocationsController = new InvocationsController(
  sessionClient,
  viewCoordinator,
);
const systemController = new SystemController(
  sessionClient,
  viewCoordinator,
  (latched) => mutationCoordinator.setStorageLatched(latched),
);
const serverReadsController = new ServerReadsController(
  sessionClient,
  viewCoordinator,
);
const registerInvalidationTrigger = (
  id: string,
  matches: (viewKey: string) => boolean,
  invalidations: ReadonlyArray<
    "authorization" | "grant_requests" | "servers" | "catalog"
  >,
) =>
  viewCoordinator.registerPanel({
    id,
    matches,
    invalidations,
    read: async () => null,
    publish: () => undefined,
  });
registerInvalidationTrigger(
  "principal-invalidation",
  (key) => /^#\/access\/principals(?:\/|$)/.test(key),
  ["authorization"],
);
registerInvalidationTrigger(
  "grant-invalidation",
  (key) => /^#\/access\/grants(?:\/|$)/.test(key),
  ["authorization"],
);
registerInvalidationTrigger(
  "request-list-invalidation",
  (key) => key === "#/requests" || key.startsWith("#/requests?"),
  ["grant_requests"],
);
registerInvalidationTrigger(
  "request-detail-invalidation",
  (key) => /^#\/requests\/[0-7][0-9A-HJKMNP-TV-Z]{25}$/.test(key),
  ["grant_requests", "servers", "catalog"],
);
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
        <FormField
          id="admin-bearer"
          label="Administrator bearer"
          hint="The value is cleared immediately after request handoff."
        >
          {(attributes) => (
            <input
              {...attributes}
              ref={bearerInput}
              data-testid="admin-bearer-input"
              type="password"
              autocomplete="off"
              autocapitalize="none"
              spellcheck={false}
              disabled={submitting || snapshot.lifecycle !== "signed_out"}
              required
            />
          )}
        </FormField>
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
  const bootstrapping = lifecycle === "bootstrapping";
  return (
    <StateNotice
      state={bootstrapping ? "loading" : "unavailable"}
      title={bootstrapping ? "Restoring session" : "Session lost"}
    >
      <p>
        {bootstrapping
          ? "Checking for current local browser authority."
          : "Discarding prior authority and checking once for a current session."}
      </p>
    </StateNotice>
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
  const [navigationOpen, setNavigationOpen] = useState(false);
  const [logoutConfirmationOpen, setLogoutConfirmationOpen] = useState(false);
  const priorLifecycle = useRef<SessionLifecycle>(session.lifecycle);
  const pageTitle = useRef<HTMLHeadingElement>(null);
  const navigationToggle = useRef<HTMLButtonElement>(null);
  const logoutButton = useRef<HTMLButtonElement>(null);
  const focusAfterLogout = useRef(false);

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
      replaceForLifecycle(true);
    } else if (
      session.lifecycle === "signed_out" &&
      (prior === "authenticated" ||
        prior === "reauthenticating" ||
        prior === "bootstrapping")
    ) {
      replaceForLifecycle(false);
    }
    priorLifecycle.current = session.lifecycle;
    const nextLocation = synchronizeFragment(authenticated);
    setResolved(nextLocation);
    if (authenticated) viewCoordinator.activate(nextLocation.canonicalFragment);
  }, [session.lifecycle, session.epoch]);

  useEffect(() => {
    const synchronize = () => {
      sensitiveSinkCoordinator.clearForNavigation();
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

  useEffect(() => {
    if (session.lifecycle === "authenticated") {
      pageTitle.current?.focus();
    } else if (session.lifecycle === "signed_out" && focusAfterLogout.current) {
      focusAfterLogout.current = false;
      pageTitle.current?.focus();
    }
    if (session.lifecycle !== "authenticated") {
      setNavigationOpen(false);
      setLogoutConfirmationOpen(false);
    }
  }, [resolved.canonicalFragment, session.lifecycle]);

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
      onKeyDown={(event) => {
        if (!navigationOpen || event.key !== "Escape") return;
        event.preventDefault();
        setNavigationOpen(false);
        navigationToggle.current?.focus();
      }}
    >
      <a
        class="skip-link"
        href="#main-content"
        onClick={(event) => {
          event.preventDefault();
          pageTitle.current?.focus();
        }}
      >
        Skip to main content
      </a>
      <div
        class="visually-hidden"
        role="status"
        aria-live="polite"
        aria-atomic="true"
        data-testid="shell-announcement"
      >
        {authenticated
          ? `${destinationLabel}. Data ${view.freshness}.`
          : `${destinationLabel}. Authentication required.`}
      </div>
      <header class="masthead">
        <a class="wordmark" href="#/overview" aria-label="MCP Gateway overview">
          <span aria-hidden="true" class="mark">
            MG
          </span>
          <span>MCP Gateway</span>
        </a>
        <div class="masthead-controls">
          {authenticated && (
            <button
              ref={navigationToggle}
              class="navigation-toggle"
              data-testid="navigation-toggle"
              type="button"
              aria-expanded={navigationOpen}
              aria-controls="primary-navigation"
              onClick={() => setNavigationOpen((value) => !value)}
            >
              <span aria-hidden="true">≡</span>
              Menu
            </button>
          )}
          <span class="environment">LOCAL CONTROL PLANE</span>
          <label class="theme-control">
            <span>Theme</span>
            <select
              data-testid="theme-preference"
              aria-label="Theme preference"
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
              ref={logoutButton}
              class="quiet-action"
              data-testid="logout"
              type="button"
              onClick={() => setLogoutConfirmationOpen(true)}
            >
              Sign out
            </button>
          )}
        </div>
      </header>
      {authenticated && (
        <aside
          id="primary-navigation"
          class={`rail ${navigationOpen ? "open" : ""}`}
          aria-label="Primary navigation"
        >
          <nav>
            {navigation.map((item, index) => {
              const active = destination === item.destination;
              return (
                <a
                  key={item.destination}
                  class={active ? "active" : undefined}
                  href={item.href}
                  aria-current={active ? "page" : undefined}
                  onClick={() => setNavigationOpen(false)}
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
      <main id="main-content" class="workspace" tabindex={-1}>
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
            <h1 ref={pageTitle} id="page-title" tabindex={-1}>
              {authenticated ? destinationLabel : "Administrator session"}
            </h1>
            <p>
              Inspect and operate this local Gateway through its public API.
            </p>
          </div>
          <StatusLabel
            state={authenticated ? "current" : "unavailable"}
            testID="authentication-status"
          >
            {authenticated ? "Authenticated" : "Authentication required"}
          </StatusLabel>
        </section>
        {session.lifecycle === "bootstrapping" ||
        session.lifecycle === "reauthenticating" ? (
          <SessionTransition lifecycle={session.lifecycle} />
        ) : authenticated ? (
          destination === "overview" ? (
            <Overview
              controller={overviewController}
              view={view}
              onRefresh={() => viewCoordinator.manualRefresh()}
            />
          ) : destination === "invocations" ? (
            <Invocations controller={invocationsController} view={view} />
          ) : destination === "system" ? (
            <System
              controller={systemController}
              session={sessionClient}
              mutations={mutationCoordinator}
              sinks={sensitiveSinkCoordinator}
              view={view}
              onRefresh={() => viewCoordinator.manualRefresh()}
            />
          ) : destination === "requests" ? (
            <Requests
              key={resolved.canonicalFragment}
              session={sessionClient}
              mutations={mutationCoordinator}
              resolved={resolved}
              view={view}
              onRefresh={() => viewCoordinator.manualRefresh()}
            />
          ) : destination === "access" ? (
            resolved.location.segments[1] === "grants" ? (
              <Grants
                key={resolved.canonicalFragment}
                session={sessionClient}
                mutations={mutationCoordinator}
                resolved={resolved}
                view={view}
              />
            ) : (
              <Principals
                session={sessionClient}
                mutations={mutationCoordinator}
                sinks={sensitiveSinkCoordinator}
                resolved={resolved}
                view={view}
                onRefresh={() => viewCoordinator.manualRefresh()}
              />
            )
          ) : destination === "servers" || destination === "catalog" ? (
            <ServerReads
              controller={serverReadsController}
              view={view}
              destination={destination}
              mutations={mutationCoordinator}
              sinks={sensitiveSinkCoordinator}
              onRefresh={() => viewCoordinator.manualRefresh()}
            />
          ) : (
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
                <StatusLabel state={view.freshness}>
                  Data {view.freshness}
                </StatusLabel>
                <button
                  data-testid="manual-refresh"
                  type="button"
                  onClick={() => viewCoordinator.manualRefresh()}
                >
                  Refresh visible data
                </button>
              </div>
            </section>
          )
        ) : (
          <SignInPanel
            snapshot={session}
            onAuthenticated={() => setResolved(synchronizeFragment(true))}
          />
        )}
      </main>
      <SensitiveSinkHost coordinator={sensitiveSinkCoordinator} />
      <ConfirmationDialog
        id="logout-confirmation"
        open={logoutConfirmationOpen}
        title="Sign out of this browser session?"
        consequence={
          <p>
            The current local session will end. No Gateway configuration or
            other administrator session is changed.
          </p>
        }
        confirmLabel="Sign out"
        returnFocus={logoutButton}
        onCancel={() => setLogoutConfirmationOpen(false)}
        onConfirm={() => {
          focusAfterLogout.current = true;
          setLogoutConfirmationOpen(false);
          void sessionClient.logout();
        }}
      />
      <footer>
        <span>Gateway authority stays in this process</span>
        <span>NO REMOTE ASSETS</span>
      </footer>
    </div>
  );
}

render(<App />, document.getElementById("app")!);
