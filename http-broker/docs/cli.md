# CLI reference

All commands accept `--config <path>` to override `config.json`.

## `serve`

Runs the proxy and dashboard in the foreground. No pidfile. Supervise with
launchd; see [launchd.md](launchd.md).

| Flag        | Effect                                             |
| ----------- | -------------------------------------------------- |
| `--no-open` | Do not open the dashboard in a browser at startup. |

On startup the dashboard URL is printed with a `?token=` parameter and opened in
a browser. Both steps require stdout to be a terminal: under launchd stdout is a
log file, so printing would persist the token and opening a browser at login is
wrong. The token is exchanged for a cookie and redirected out of the URL on
first load, so it does not linger in browser history.

Signals: `SIGHUP` reloads `config.json`, `rules.json`, the auth token and the
CA, and clears the credential cache, keeping previous state on failure. The
listener addresses are the exception — moving a bound socket would point every
sandbox at a dead port, so a changed address is logged and ignored until a
restart. `SIGINT`/`SIGTERM` shut down gracefully with a 10-second drain window.

## `config`

| Command          | Effect                                                     |
| ---------------- | ---------------------------------------------------------- |
| `config path`    | Print the effective config path.                           |
| `config show`    | Print the effective configuration.                         |
| `config refresh` | Backfill newly added defaults, preserving existing values. |

## `rules`

| Command         | Effect                                                                                                                                            |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------- |
| `rules path`    | Print the effective rules path.                                                                                                                   |
| `rules show`    | Print the effective rules document.                                                                                                               |
| `rules check`   | Validate without restarting. **Run this before every `SIGHUP`** — an invalid file leaves the previous ruleset serving, so a typo is easy to miss. |
| `rules refresh` | Validate and rewrite in canonical form.                                                                                                           |

## `credential`

Values are never printed by any subcommand. `list` and `get` show a name, its
source, its bound hosts, and the byte count of the stored value.

```bash
printf %s "$TOKEN" | http-broker credential set gh_bot --host api.github.com
```

`--host` is repeatable and **at least one is required**. A host that matches
every host, or that reduces to a public suffix, is rejected.

The value is read from stdin and a terminal is refused: a secret typed at a
prompt lands in shell history and scrollback. `rebind` does not read stdin: it
keeps the stored value and changes only the hosts.

| Command                               | Effect                                               |
| ------------------------------------- | ---------------------------------------------------- |
| `credential set <name>`               | Store a credential and its bound hosts.              |
| `credential list`                     | List stored credentials, their sources, and hosts.   |
| `credential get <name>`               | Show one credential, re-registering it in the index. |
| `credential rebind <name> --host ...` | Change the hosts an existing credential may go to.   |
| `credential rm <name>`                | Remove a credential.                                 |

```bash
http-broker credential rebind gh_bot --host api.github.com --host uploads.github.com
```

A running proxy applies a rebind within 30s, the credential cache TTL. To apply
it immediately:

```bash
kill -HUP $(pgrep -f 'http-broker serve')
```

### The credential index

The OS keychain cannot be enumerated, so the names of stored credentials are
recorded in `~/.local/share/http-broker/credentials.json`. That file holds names
only — never a value, and never a host list, since the keychain envelope stays
the sole authority on scope.

It is derived state. Deleting it loses no secret, and `credential get <name>`
re-registers a name. Because an empty index does not mean nothing is stored,
`list` says so explicitly: look at the names `rules.json` references, and at the
dashboard's Credentials view, for what to re-register.

`list` prunes an index entry whose keychain item is gone and reports it on
stderr. It never prunes when the keychain cannot be reached — that failure exits
1 instead, because "could not ask" is not "not there". A name with an
`env_credentials` fallback may still be injecting in that case, so a failed
`list` is not evidence the proxy has stopped serving.

## `token`

| Command                  | Effect                                              |
| ------------------------ | --------------------------------------------------- |
| `token show`             | Print the token, generating one if absent.          |
| `token proxy-credential` | Print the `Proxy-Authorization` value clients send. |
| `token rotate`           | Generate a new token, invalidating the old one.     |

Values print to stdout, so `$(http-broker token show)` works. Advice that
follows a value — "re-run provisioning" after a rotate — goes to stderr, so it
never lands in the substitution.

## `ca`

| Command               | Effect                                                                                                                    |
| --------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `ca path`             | Print the CA certificate path.                                                                                            |
| `ca export [-o file]` | Write the CA certificate. The private key is never exported.                                                              |
| `ca rotate --yes`     | Generate a new CA. **Requires `--yes`**: there is no overlap window, and every provisioned sandbox needs re-provisioning. |

## Refusal reasons

Synthesized 4xx/5xx responses carry `X-Http-Broker-Reason`:

| Reason                  | Meaning                                                                                                         |
| ----------------------- | --------------------------------------------------------------------------------------------------------------- |
| `proxy-auth-required`   | Missing or wrong `Proxy-Authorization`.                                                                         |
| `rule-deny`             | A `deny` rule matched.                                                                                          |
| `fallthrough-deny`      | No rule matched and `fallthrough` is `deny`, or a fallthrough tunnel was CONNECTed on a non-443 port.           |
| `blocked-address`       | The upstream address is blocked by the SSRF guard.                                                              |
| `credential-unresolved` | A referenced credential could not be resolved.                                                                  |
| `credential-host-scope` | A referenced credential is not bound to this host.                                                              |
| `credential-invalid`    | A credential resolved but its value cannot go in a header.                                                      |
| `bad-request`           | Malformed CONNECT target or request URL, or an `https` absolute-form request line — send those through CONNECT. |
| `upstream-failure`      | The upstream request failed.                                                                                    |
