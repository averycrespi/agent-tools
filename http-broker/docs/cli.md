# CLI reference

All commands accept `--config <path>` to override `config.json`.

## `serve`

Runs the proxy and dashboard in the foreground. No pidfile. Supervise with
launchd; see [launchd.md](launchd.md).

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

Values are never printed by any subcommand.

```bash
printf %s "$TOKEN" | http-broker credential set gh_bot --host api.github.com
```

`--host` is repeatable and **at least one is required**. A host that matches
every host, or that reduces to a public suffix, is rejected.

The value is read from stdin and a terminal is refused: a secret typed at a
prompt lands in shell history and scrollback.

| Command                    | Effect                                         |
| -------------------------- | ---------------------------------------------- |
| `credential set <name>`    | Store a credential and its bound hosts.        |
| `credential list [--json]` | Names, sources and bound hosts. Never a value. |
| `credential rm <name>`     | Remove a credential.                           |

## `token`

| Command                  | Effect                                              |
| ------------------------ | --------------------------------------------------- |
| `token show`             | Print the token, generating one if absent.          |
| `token proxy-credential` | Print the `Proxy-Authorization` value clients send. |
| `token rotate`           | Generate a new token, invalidating the old one.     |

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
