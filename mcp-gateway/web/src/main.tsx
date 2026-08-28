import { render } from "preact";
import "./styles.css";

const destinations = [
  "Overview",
  "Servers",
  "Catalog",
  "Access",
  "Requests",
  "Invocations",
  "System",
] as const;

function App() {
  return (
    <div class="shell" data-testid="gateway-shell">
      <header class="masthead">
        <a class="wordmark" href="#/overview" aria-label="MCP Gateway overview">
          <span aria-hidden="true" class="mark">
            MG
          </span>
          <span>MCP Gateway</span>
        </a>
        <span class="environment">LOCAL CONTROL PLANE</span>
      </header>
      <aside class="rail" aria-label="Primary navigation">
        <nav>
          {destinations.map((destination, index) => (
            <a
              class={index === 0 ? "active" : undefined}
              href={`#/${destination.toLowerCase()}`}
              aria-current={index === 0 ? "page" : undefined}
            >
              <span class="nav-index">
                {String(index + 1).padStart(2, "0")}
              </span>
              {destination}
            </a>
          ))}
        </nav>
      </aside>
      <main class="workspace">
        <div class="eyebrow">SYSTEM / OVERVIEW</div>
        <section class="intro" aria-labelledby="page-title">
          <div>
            <h1 id="page-title">Gateway posture</h1>
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
            The product workflows are loading from the embedded, same-origin
            bundle.
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
