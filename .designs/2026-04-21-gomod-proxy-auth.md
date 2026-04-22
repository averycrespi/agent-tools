# local-gomod-proxy: TLS + basic auth

Status: design, not yet implemented.
Date: 2026-04-21.

## Motivation

The proxy today binds to `127.0.0.1:7070` with no application-level auth. Its security model is "only processes that can reach loopback can use it." In practice that is **every process running as the host user** — browsers, extensions, random CLIs, anything. A compromised browser extension or curious tool that probes `localhost:7070` can resolve arbitrary modules against the host's git credentials and exfiltrate private source.

We accept that a truly native attacker running as the user can always steal any secret we store on disk. The goal is defense-in-depth against the broader set of "other processes of mine that happen to poke at localhost ports": they should not be able to use this proxy without first reading a specific file they have no reason to look at.

## Decision summary

- Proxy listens on **HTTPS** with a self-signed cert.
- Every request must carry **HTTP basic auth** with credentials the proxy generates at first launch.
- Cert, key, and credentials persist under **`$XDG_STATE_HOME/local-gomod-proxy/`** (default `~/.local/state/local-gomod-proxy/`).
- Sandbox provisioning reads the credentials file via Lima's `$HOME` mount and sets `GOPROXY=https://x:<token>@host.lima.internal:7070/` plus `GOINSECURE=host.lima.internal`.
- Plain-HTTP mode goes away. Single deployment shape.

Rationale for TLS over the simpler URL-path-token alternative: equivalent effective security here, but basic-auth-over-TLS is the canonical Go mechanism — familiar to anyone reviewing the code, and future-proofs for any non-loopback deployment.

## Cert & credential lifecycle

### State directory

```
$XDG_STATE_HOME/local-gomod-proxy/     (mode 0700)
├── cert.pem                           (mode 0644 — public)
├── key.pem                            (mode 0600)
└── credentials                        (mode 0600 — "x:<token>\n")
```

XDG choice: cache is wrong (these aren't regenerable without breaking consumers), config is wrong (user doesn't author them). State fits: generated, per-install, persistent, not worth backing up.

Override via `--state-dir` flag (useful for tests and non-standard launchd setups).

### Cert

- ECDSA P-256, self-signed.
- SANs: `localhost`, `127.0.0.1`, `host.lima.internal`.
- Subject CN: `local-gomod-proxy`.
- Validity: 1 year.
- Load-or-generate: reuse if `cert.pem`+`key.pem` both present, parseable, and >30 days to expiry. Otherwise regenerate silently and log at Info.

### Credentials

- Username fixed at `x` (Git/npm convention for "username slot unused, token in password").
- Password is 32 random bytes, base64url-encoded (~43 chars).
- File format: single line `x:<token>\n`.
- Load-or-generate on first launch; **never** auto-regenerate. A missing file regenerates; a malformed file fails startup (the user may have hand-edited it, and silent clobber would invalidate every provisioned sandbox).

### Rotation

Manual: `rm -rf $state_dir && restart proxy && re-provision sandboxes`. No automatic rotation. Document this.

## Server wiring

### Flags

One addition:

| Flag          | Default                                                                           | Purpose                         |
| ------------- | --------------------------------------------------------------------------------- | ------------------------------- |
| `--state-dir` | `$XDG_STATE_HOME/local-gomod-proxy` (fallback `~/.local/state/local-gomod-proxy`) | Location of cert + credentials. |

Existing `--addr`, `--private`, `--upstream` unchanged. No `--tls-cert` / `--tls-key` / `--credentials-file` — the state dir is the single knob.

### New internal packages

```
internal/
  state/
    dir.go          # XDG resolution, ensures 0700 dir
    dir_test.go
    cert.go         # load-or-generate ECDSA P-256 self-signed cert
    cert_test.go
    creds.go        # load-or-generate "x:<random>" credentials
    creds_test.go
  auth/
    auth.go         # basic-auth middleware
    auth_test.go
```

### Auth middleware

Wraps the server handler. Extracts Basic auth via `r.BasicAuth()`. Compares with `subtle.ConstantTimeCompare` on both user and password bytes. On mismatch or missing header: `401` with `WWW-Authenticate: Basic realm="local-gomod-proxy"`. Warn-level log with remote addr only. The `Authorization` header is never logged.

Missing vs. bad auth responses are identical — no enumeration help for an attacker.

### Listener

Replace `http.ListenAndServe` with `Server.ListenAndServeTLS(certPath, keyPath)`. `TLSConfig.MinVersion = tls.VersionTLS12`, stdlib default cipher suites. Existing graceful-shutdown path (`Server.Shutdown` with 5s drain, context-cancel of in-flight subprocesses) unchanged.

### Startup logging

- Resolved state dir.
- Cert SHA-256 fingerprint (first 16 hex chars).
- Listen address.
- **Never** the password.

Lets a user verify "did the proxy pick up the cert I expect" without exposing secrets.

## Sandbox provisioning

Updated `examples/provision/gomod-proxy.sh` flow:

1. Resolve host state dir via Lima's `$HOME` mount (same path inside the VM as on the host).
2. Read `credentials` file → one line `x:<token>`.
3. Export:
   ```sh
   GOPROXY="https://${creds}@host.lima.internal:7070/"
   GOINSECURE="host.lima.internal"
   GOSUMDB=off
   unset GOPRIVATE
   go env -u GOPRIVATE
   ```
4. If the credentials file is missing or unreadable, **fail loudly** with a message pointing at the host-side start command. No silent fallback to an unauthenticated `GOPROXY`.

### Why `GOINSECURE` over installing the cert into the sandbox trust store

- Trust-store install needs `sudo update-ca-certificates` — extra provisioning complexity; cert rotation becomes a sudo event.
- `GOINSECURE` is scoped: relaxes cert verification for `host.lima.internal` only, and only for the Go toolchain. `curl`, `git`, etc. inside the sandbox are unaffected.
- The MITM threat "opened up" is the Lima host-local bridge — an attacker positioned to MITM it already owns the host and can read the cert file.

## Error handling

| Condition               | Behavior                                                 |
| ----------------------- | -------------------------------------------------------- |
| State dir not creatable | Fail startup with path + errno                           |
| Cert unparseable        | Fail startup, point at file                              |
| Cert expired / <30 days | Regenerate silently, log at Info                         |
| Credentials malformed   | Fail startup (do not regenerate)                         |
| Bad basic auth          | 401 + `WWW-Authenticate`, Warn log with remote addr only |
| Missing Authorization   | Same as bad (no enumeration)                             |

Mid-stream errors (after response headers written) keep existing `ErrResponseCommitted` handling.

## Testing

| Layer                             | What's new                                                                                                                                                                |
| --------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Unit                              | `internal/state`: tempdir round-trip for cert + creds; regen-on-expiry; 0700/0600/0644 perm bits verified on written files                                                |
| Unit                              | `internal/auth`: valid / invalid / missing / malformed Authorization; constant-time path; 401 shape with `WWW-Authenticate`                                               |
| Unit                              | Log-scrubbing: assert `Authorization` header value never appears in captured log lines                                                                                    |
| Integration (`-tags=integration`) | Start server with a tempdir state dir; exercise TLS + basic auth happy path, wrong creds → 401, no creds → 401                                                            |
| E2E (`-tags=e2e`)                 | Update existing E2E: generate+trust cert, plumb `GOPROXY=https://x:…@…` and `GOINSECURE` into the `go mod download` subprocess env, exercise a real resolution end-to-end |

## Documentation updates

Per the doc-sync rule in `local-gomod-proxy/CLAUDE.md`:

- **`DESIGN.md`** — replace the "No application-level auth" decision with this design. Update the ASCII diagram to show HTTPS + auth middleware. Update `Security` section.
- **`README.md`** — new `--state-dir` flag in `## Run`; new sandbox env in `## How the sandbox consumes it`; rewrite `## Security` (drop "unauthenticated" warnings, state the new posture and its limits).
- **`CLAUDE.md`** — replace the "No application-level auth" convention bullet with "Auth: basic auth over TLS; credentials + cert live in `$XDG_STATE_HOME/local-gomod-proxy/`."
- **`docs/launchd.md`** — note that the state dir must be readable by the launchd-run process (it is by default; same user). No plist changes.
- **`examples/provision/gomod-proxy.sh`** — updated script.

## Honest limits (to surface in README Security section)

- Any process running as the same user can read `$XDG_STATE_HOME/local-gomod-proxy/credentials`. `0600` stops _other users_, not other _processes of yours_.
- Native malware running as you can still steal the token. Defeating that requires OS-level process isolation (Keychain + entitlements, Linux keyring, etc.) — explicitly out of scope.
- What this does block: browser JS (no filesystem read, no way to guess the token), scripts that probe `localhost:7070` without knowing to look for a credentials file, and anyone who learns the port exists but not the secret.

## Alternatives considered

- **URL-path shared secret (plain HTTP).** Token as a GOPROXY URL prefix. Equivalent effective security, simpler server (no TLS code), simpler sandbox config (no `GOINSECURE`). Rejected in favor of the canonical HTTP auth mechanism — easier to review, future-proofs for non-loopback deployments.
- **TLS + `GOAUTH` bearer script.** Canonical but strictly heavier than URL-embedded basic auth (extra helper script inside the sandbox, no security gain at this boundary).
- **Unix domain socket with peer-cred filtering.** Would need a TCP bridge for Lima anyway; peer-cred doesn't help against same-user processes (the actual threat).
- **Module-path allowlist.** Orthogonal blast-radius reduction. Deferred — current `GOPRIVATE` matching is sufficient for now.
- **Per-launch credential regeneration.** Rejected because sandbox provisioning runs at _sandbox_ boot, not _proxy_ restart; rotating on every proxy launch would break running sandboxes on any launchd-triggered restart.
