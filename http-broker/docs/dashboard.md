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
open "http://127.0.0.1:8221/?token=$(http-broker token show)"
```

The token is exchanged for an `http-broker-auth` cookie and dropped from the URL,
so it does not linger in browser history.

Requests may also authenticate with `Authorization: Bearer <token>`.

## Routes

This table is the source of truth for the non-mutation sweep in
`test/e2e/dashboard_test.go`. That test parses this file, drives every route
listed here with every HTTP method and adversarial query parameters, and
asserts that no on-disk state changed. **A route added to the code must be
added here, or it will never be swept.** A companion test fails if the code
serves a route this table omits.

| Route                  | Auth  | Returns                                                                                                     |
| ---------------------- | ----- | ----------------------------------------------------------------------------------------------------------- |
| `GET /healthz`         | none  | `ok`. The liveness probe an external monitor uses to detect a wedged-but-listening proxy.                   |
| `GET /ca.pem`          | none  | The CA certificate. Unauthenticated because provisioning fetches it before any token exists in the sandbox. |
| `GET /`                | token | The dashboard page.                                                                                         |
| `GET /app.js`          | token | Dashboard script.                                                                                           |
| `GET /styles.css`      | token | Dashboard styles.                                                                                           |
| `GET /favicon.svg`     | token | Dashboard icon.                                                                                             |
| `GET /api/audit`       | token | Audit history. Filters: `host`, `outcome`, `rule`, `limit`, `offset`.                                       |
| `GET /api/rules`       | token | The active ruleset and fallthrough policy.                                                                  |
| `GET /api/credentials` | token | Credential **names, sources and bound hosts**. Never a value.                                               |
| `GET /api/events`      | token | Server-sent events: one `audit` event per new request.                                                      |

Every route is registered with an explicit `GET` method pattern, so any other
method returns 405 from the mux rather than relying on a handler to reject it.

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

`/api/events` sends one event per audit record. Each client gets a buffered
channel and the broadcast is non-blocking: a browser tab that stops reading
loses events rather than applying back-pressure to the proxy's request path.

A keepalive comment is sent every 15 seconds so an idle stream is not closed.
