import { render } from "preact";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "preact/hooks";
import {
  replaceForLifecycle,
  synchronizeFragment,
  type Destination,
  type ResolvedLocation,
} from "./location";
import { Grants } from "./grants";
import { Invocations, InvocationsController } from "./invocations";
import { MutationCoordinator, type MutationAvailability } from "./mutation";
import { configureNavigationGuard, type NavigationGuard } from "./navigation";
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
import { ToastCoordinator, ToastHost } from "./toast";
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
  { destination: "catalog", label: "Catalog", href: "#/catalog" },
  { destination: "servers", label: "Servers", href: "#/servers" },
  {
    destination: "principals",
    label: "Principals",
    href: "#/principals",
  },
  { destination: "grants", label: "Grants", href: "#/grants" },
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
  principals: "Principals",
  grants: "Grants",
  requests: "Requests",
  invocations: "Invocations",
  system: "System",
  "sign-in": "Sign in",
};

const initialLocation = synchronizeFragment(false);
const initialTheme = readThemePreference();
const sessionClient = new SessionClient();
const sensitiveSinkCoordinator = new SensitiveSinkCoordinator(sessionClient);
const toastCoordinator = new ToastCoordinator();
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
  (key) => /^#\/principals(?:\/|$)/.test(key),
  ["authorization"],
);
registerInvalidationTrigger(
  "grant-invalidation",
  (key) => /^#\/grants(?:\/|$)/.test(key),
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
        <h2 id="sign-in-title">Administrator access</h2>
      </div>
      <form autocomplete="off" onSubmit={submit}>
        <FormField
          id="admin-bearer"
          label="Administrator bearer"
          hint="Cleared after handoff and never saved."
          required
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
          class="primary-action"
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
  const [pendingNavigation, setPendingNavigation] = useState<string>();
  const priorLifecycle = useRef<SessionLifecycle>(session.lifecycle);
  const acceptedFragment = useRef(initialLocation.canonicalFragment);
  const bypassHashGuard = useRef(false);
  const dirtyOwners = useRef(new Set<symbol>());
  const pageTitle = useRef<HTMLElement>(null);
  const navigationToggle = useRef<HTMLButtonElement>(null);
  const logoutButton = useRef<HTMLButtonElement>(null);
  const navigationReturnFocus = useRef<HTMLElement>(null);
  const focusAfterLogout = useRef(false);
  const focusedLocationOwner = useRef<string>();

  const setDirty = useCallback((owner: symbol, dirty: boolean) => {
    const changed = dirty
      ? !dirtyOwners.current.has(owner)
      : dirtyOwners.current.has(owner);
    if (!changed) return;
    if (dirty) dirtyOwners.current.add(owner);
    else dirtyOwners.current.delete(owner);
  }, []);
  const navigate = useCallback((fragment: string, discard = false) => {
    if (fragment === window.location.hash) return;
    if (!discard && dirtyOwners.current.size !== 0) {
      setPendingNavigation(fragment);
      return;
    }
    bypassHashGuard.current = discard;
    window.location.hash = fragment;
  }, []);
  const navigationGuard = useMemo<NavigationGuard>(
    () => ({ setDirty, navigate }),
    [navigate, setDirty],
  );
  configureNavigationGuard(navigationGuard);

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
    acceptedFragment.current = nextLocation.canonicalFragment;
    setResolved(nextLocation);
    if (authenticated) viewCoordinator.activate(nextLocation.canonicalFragment);
  }, [session.lifecycle, session.epoch]);

  useEffect(() => {
    const synchronize = () => {
      if (bypassHashGuard.current) {
        bypassHashGuard.current = false;
      } else if (
        dirtyOwners.current.size !== 0 &&
        window.location.hash !== acceptedFragment.current &&
        !window.confirm("Leave this page? Unsaved changes will be discarded.")
      ) {
        window.history.pushState(null, "", acceptedFragment.current);
        return;
      }
      sensitiveSinkCoordinator.clearForNavigation();
      const nextLocation = synchronizeFragment(
        session.lifecycle === "authenticated",
      );
      acceptedFragment.current = nextLocation.canonicalFragment;
      setResolved(nextLocation);
      if (session.lifecycle === "authenticated")
        viewCoordinator.navigate(nextLocation.canonicalFragment);
    };
    window.addEventListener("hashchange", synchronize);
    return () => window.removeEventListener("hashchange", synchronize);
  }, [session.lifecycle]);

  useEffect(() => {
    const preventUnload = (event: BeforeUnloadEvent) => {
      if (dirtyOwners.current.size === 0) return;
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("beforeunload", preventUnload);
    return () => window.removeEventListener("beforeunload", preventUnload);
  }, []);

  useEffect(() => {
    applyTheme(theme);
    return observeSystemTheme(theme, () => applyTheme(theme));
  }, [theme]);

  useEffect(() => {
    const serverID =
      resolved.location.destination === "servers" &&
      resolved.location.segments[1] !== undefined &&
      resolved.location.segments[1] !== "new"
        ? resolved.location.segments[1]
        : undefined;
    const principalID =
      resolved.location.destination === "principals" &&
      resolved.location.segments[1] !== undefined &&
      resolved.location.segments[1] !== "new"
        ? resolved.location.segments[1]
        : undefined;
    const invocationID =
      resolved.location.destination === "invocations"
        ? resolved.location.segments[1]
        : undefined;
    const owner =
      serverID !== undefined
        ? `server:${serverID}`
        : principalID !== undefined
          ? `principal:${principalID}`
          : invocationID !== undefined
            ? `invocation:${invocationID}`
            : resolved.location.destination;
    let observer: MutationObserver | undefined;
    if (
      session.lifecycle === "authenticated" &&
      focusedLocationOwner.current !== owner
    ) {
      focusedLocationOwner.current = owner;
      if (
        serverID === undefined &&
        principalID === undefined &&
        invocationID === undefined
      ) {
        pageTitle.current?.focus();
      } else {
        const focusContextTitle = () => {
          const heading = document.querySelector<HTMLElement>(
            serverID !== undefined
              ? '[data-testid="server-context"] h2'
              : principalID !== undefined
                ? '[data-testid="principal-context"] h1'
                : '[data-testid="invocation-detail"] h1',
          );
          heading?.focus();
          return heading !== null;
        };
        if (!focusContextTitle()) {
          observer = new MutationObserver(() => {
            if (focusContextTitle()) observer?.disconnect();
          });
          observer.observe(document.body, { childList: true, subtree: true });
        }
      }
    } else if (session.lifecycle === "signed_out" && focusAfterLogout.current) {
      focusAfterLogout.current = false;
      focusedLocationOwner.current = undefined;
      pageTitle.current?.focus();
    }
    if (session.lifecycle !== "authenticated") {
      focusedLocationOwner.current = undefined;
      setNavigationOpen(false);
      setLogoutConfirmationOpen(false);
      toastCoordinator.clear();
    }
    return () => observer?.disconnect();
  }, [resolved.canonicalFragment, session.lifecycle]);

  const chooseTheme = (value: ThemePreference) => {
    writeThemePreference(value);
    setTheme(value);
  };
  const destination = resolved.location.destination;
  const isServerDetail =
    destination === "servers" &&
    resolved.canonicalFragment !== "#/servers" &&
    resolved.canonicalFragment !== "#/servers/new";
  const isPrincipalDetail =
    destination === "principals" &&
    resolved.canonicalFragment !== "#/principals" &&
    resolved.canonicalFragment !== "#/principals/new";
  const isInvocationDetail =
    destination === "invocations" &&
    resolved.location.segments[1] !== undefined;
  const destinationLabel =
    destination === "servers" && resolved.canonicalFragment !== "#/servers"
      ? resolved.canonicalFragment === "#/servers/new"
        ? "Create server"
        : "Server details"
      : destination === "principals" &&
          resolved.canonicalFragment === "#/principals/new"
        ? "Create principal"
        : destination === "grants" &&
            resolved.canonicalFragment.startsWith("#/grants/new")
          ? "Create grant"
          : resolved.canonicalFragment === "#/system/backups/new"
            ? "Create backup"
            : resolved.canonicalFragment === "#/system/admin-credentials/new"
              ? "Create admin credential"
              : destinationLabels[destination];
  const authenticated = session.lifecycle === "authenticated";

  return (
    <div
      class={`shell ${authenticated ? "authenticated" : "signed-out"}`}
      data-testid="gateway-shell"
      data-session-lifecycle={session.lifecycle}
      data-view-generation={view.generation}
      data-freshness={view.freshness}
      data-mutation-availability={mutationAvailability}
      onClickCapture={(event) => {
        if (
          event.button !== 0 ||
          event.metaKey ||
          event.ctrlKey ||
          event.shiftKey ||
          event.altKey
        )
          return;
        const target = event.target;
        if (!(target instanceof Element)) return;
        const link = target.closest("a");
        const fragment = link?.getAttribute("href");
        if (
          link === null ||
          fragment === null ||
          fragment === undefined ||
          !fragment.startsWith("#/") ||
          link.target !== ""
        )
          return;
        event.preventDefault();
        navigationReturnFocus.current = link;
        navigate(fragment);
      }}
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
            G
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
          {authenticated && (
            <div class="freshness-control">
              {view.freshness !== "current" && (
                <StatusLabel state={view.freshness}>
                  {view.freshness === "stale" ? "Stale" : "Reconnecting"}
                </StatusLabel>
              )}
              <button
                class="quiet-action"
                data-testid="manual-refresh"
                type="button"
                aria-label="Refresh current view"
                onClick={() => viewCoordinator.manualRefresh()}
              >
                <span aria-hidden="true">↻</span>
                <span class="refresh-label">Refresh</span>
              </button>
            </div>
          )}
          <label class="theme-control">
            <span class="visually-hidden">Theme</span>
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
            {navigation.map((item) => {
              const active = destination === item.destination;
              return (
                <a
                  key={item.destination}
                  class={active ? "active" : undefined}
                  href={item.href}
                  aria-current={active ? "page" : undefined}
                  onClick={() => setNavigationOpen(false)}
                >
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
        <section class="intro" aria-labelledby="page-title">
          {authenticated && (isPrincipalDetail || isInvocationDetail) ? (
            <span
              ref={(element) => {
                pageTitle.current = element;
              }}
              id="page-title"
              class="visually-hidden"
              tabindex={-1}
            >
              {isPrincipalDetail ? "Principal details" : "Invocation details"}
            </span>
          ) : (
            <h1
              ref={(element) => {
                pageTitle.current = element;
              }}
              id="page-title"
              class={
                authenticated && isServerDetail ? "visually-hidden" : undefined
              }
              tabindex={-1}
            >
              {authenticated ? destinationLabel : "Sign in"}
            </h1>
          )}
          <div class="visually-hidden">
            <StatusLabel
              state={authenticated ? "current" : "unavailable"}
              testID="authentication-status"
            >
              {authenticated ? "Authenticated" : "Authentication required"}
            </StatusLabel>
          </div>
        </section>
        {session.lifecycle === "bootstrapping" ||
        session.lifecycle === "reauthenticating" ? (
          <SessionTransition lifecycle={session.lifecycle} />
        ) : authenticated ? (
          destination === "overview" ? (
            <Overview controller={overviewController} view={view} />
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
          ) : destination === "principals" ? (
            <Principals
              session={sessionClient}
              mutations={mutationCoordinator}
              sinks={sensitiveSinkCoordinator}
              resolved={resolved}
              view={view}
              onRefresh={() => viewCoordinator.manualRefresh()}
            />
          ) : destination === "grants" ? (
            <Grants
              key={resolved.canonicalFragment}
              session={sessionClient}
              mutations={mutationCoordinator}
              resolved={resolved}
              view={view}
            />
          ) : destination === "servers" || destination === "catalog" ? (
            <ServerReads
              controller={serverReadsController}
              view={view}
              destination={destination}
              mutations={mutationCoordinator}
              sinks={sensitiveSinkCoordinator}
              onRefresh={() => viewCoordinator.manualRefresh()}
              notify={(message) => toastCoordinator.show(message)}
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
      <ToastHost coordinator={toastCoordinator} />
      <ConfirmationDialog
        id="unsaved-changes"
        open={pendingNavigation !== undefined}
        title="Discard unsaved changes?"
        consequence="Your changes on this page have not been saved."
        confirmLabel="Discard and leave"
        destructive
        returnFocus={navigationReturnFocus}
        onCancel={() => setPendingNavigation(undefined)}
        onConfirm={() => {
          const destination = pendingNavigation;
          setPendingNavigation(undefined);
          if (destination !== undefined) navigate(destination, true);
        }}
      />
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
    </div>
  );
}

render(<App />, document.getElementById("app")!);
