# Dashboard

A read-only view of policy and traffic, served on the dashboard listener
(`:8221` by default).

Read-only is a design constraint, not an omission. No endpoint writes config,
rules, or credentials. Policy is authored by editing `rules.json` and sending
`SIGHUP`, so the dashboard cannot become a second, weaker path to changing what
the proxy allows.

## Access

`serve` prints this URL and opens it in a browser at startup, unless
`--no-open` is passed or stdout is not a terminal (as under launchd). To open it
by hand:

```bash
open "http://127.0.0.1:8221/dashboard/?token=$(http-broker token show admin)"
```

Only `admin-token` works in the query, Bearer header, or `http-broker-auth` cookie; an agent credential receives 401. The bootstrap redirect removes the query from the current URL, without promising removal from browser history. The cookie is scoped to `/dashboard/`, `HttpOnly`, and `SameSite=Strict`. Tokenized URLs are printed/opened only from an interactive terminal.

The listener root redirects to `/dashboard/`, carrying a `?token=` parameter
over if one was given, so the bare address is not a dead end.

Requests may also authenticate with `Authorization: Bearer <admin-token>`.

Everything the dashboard serves lives under `/dashboard/`, matching
`mcp-broker`. That tool shares one port between `/mcp` and its dashboard, so the
prefix disambiguates; here the dashboard has its own listener and the prefix
buys consistency instead. `/healthz` and `/ca.pem` stay at the root: they are
consumed by monitors and provisioning scripts, not by the UI.

## Routes

This table is the source of truth for the non-mutation sweep in
`test/e2e/dashboard_test.go`. That test parses this file, drives every route
listed here with every HTTP method and adversarial query parameters, and
asserts that no on-disk state changed. **A route added to the code must be
added here, or it will never be swept.** A companion test fails if the code
serves a route this table omits.

| Route                            | Auth        | Returns                                                                                                     |
| -------------------------------- | ----------- | ----------------------------------------------------------------------------------------------------------- |
| `GET /`                          | none        | 302 to `/dashboard/`, carrying `?token=` over if present. Exposes nothing itself.                           |
| `GET /healthz`                   | none        | `ok`. The liveness probe an external monitor uses to detect a wedged-but-listening proxy.                   |
| `GET /ca.pem`                    | none        | The CA certificate. Unauthenticated because provisioning fetches it before any token exists in the sandbox. |
| `GET /dashboard/`                | admin token | The dashboard page.                                                                                         |
| `GET /dashboard/app.js`          | admin token | Dashboard script.                                                                                           |
| `GET /dashboard/styles.css`      | admin token | Dashboard styles.                                                                                           |
| `GET /dashboard/favicon.svg`     | admin token | Dashboard icon.                                                                                             |
| `GET /dashboard/api/audit`       | admin token | Audit history. Filters: `host`, `outcome`, `source`, `mode`, `rule`, `limit`, `offset`.                     |
| `GET /dashboard/api/rules`       | admin token | The active ruleset and fallthrough policy.                                                                  |
| `GET /dashboard/api/credentials` | admin token | Credential **names, sources and bound hosts**, plus `referenced` and `index_error`. Never a value.          |
| `GET /dashboard/api/events`      | admin token | Server-sent events: one `audit` event per new request.                                                      |

Every route is registered with an explicit `GET` method pattern, so any other method returns 405 from the mux rather than relying on a handler to reject it. After admin rotation and `SIGHUP`, old credentials and cookies fail on new requests, while an already-authenticated SSE feed may remain open.

`host` is a substring match, so `github` finds `api.github.com`. Wildcard
characters are escaped, so `%` matches a literal percent sign. `outcome`,
`source`, `mode` and `rule` are exact: the UI drives them from dropdowns over a
fixed set of values.

`source` and `mode` are separate axes. `source` is what decided — `rule`,
`fallthrough`, or `implicit-allow` — and `matched_rule` names the rule when the
source is `rule`. `mode` is what the proxy then did with the bytes:
`intercept`, `tunnel` or `deny`. Both are always set on any row that reached a
policy decision; a refusal before policy (a failed proxy auth, a malformed
request line) has neither.

Rows written before the two were split carry the old merged value: a NULL
`source` and a `mode` holding `fallthrough` or `implicit-allow`.

`/dashboard/api/credentials` unions three name sets: the credential index, the
names `rules.json` references, and the `env_credentials` keys. Each row carries
`referenced`, which is true when a rule injects that name — a stored credential
no rule references is dead weight, and a referenced one that is missing is what
produces a 403. A name held by both the keychain and `env_credentials` reports
the keychain, because that is the source the proxy resolves through.

`index_error` is present only when the credential index could not be read. The
listing still returns the referenced and `env_credentials` rows, and the page
shows the error above the table: a credentials table that looks complete while
missing entries is worse than one that says what it could not read.

## What the API deliberately does not expose

- **Credential values.** The type carrying credentials to the dashboard has no
  value field, so no code path can leak one by accident.
- **Request or response bodies.** They are never buffered, never stored, and
  never available to query.
- **Header values.** The audit schema has no column for one.

Query strings are stored with credential-shaped parameters redacted before they
reach the database, so they are already redacted by the time the dashboard can
read them.

## The live feed

`/dashboard/api/events` sends one event per audit record. Each client gets a buffered
channel and the broadcast is non-blocking: a browser tab that stops reading
loses events rather than applying back-pressure to the proxy's request path.

A keepalive comment is sent every 15 seconds so an idle stream is not closed.

The traffic view prepends a streamed record only when it is on the first page,
unpaused, and the record matches the active filters — so a live event can never
appear to contradict a filter. Anything else is counted and reported in the
strip above the table as new records waiting.

Expanding a row pauses the feed, because a prepend would otherwise shift the
row out from under the reader. Collapsing it resumes, unless the feed was
already paused with the Pause button.
