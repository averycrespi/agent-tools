# Security audit remediation

**Date:** 2026-04-23
**Audit source:** `agent-gateway/SECURITY_AUDIT.md` (41 findings across 4 parts, commit on branch `agent-gateway`, 2026-04-20)

## Methodology and scope

The audit surfaced 41 findings plus two design-level recommendations from the agent-vault prior-art comparison. We walked through each one, decided case-by-case, and landed on **28 tasks to do** (27 from the walkthrough plus a cross-cutting comment sweep) and **13 deliberately skipped**. This doc is the record of those decisions, specs for the work with real design content, and the skipped-with-reason list so the trail is durable.

### Deployment assumptions

agent-gateway is a **single-user tool** running on the operator's own host:

- Listeners are loopback-only (enforced per T#13).
- The operator is trusted — we do not defend against a malicious operator authoring rules, secret names, or config.
- Agents are **semi-trusted** — they hold a valid auth token but may be buggy, compromised, or runaway.
- Sandboxes reach the host via Lima's `host.lima.internal` user-mode forwarding (non-cooperative sandbox hardening is a documented opt-in per T#27).

Several audit findings were skipped because they assume a multi-operator or network-reachable threat model that doesn't match this deployment. Those are listed in the "Deliberately skipped" section with rationale.

### Out of scope for this doc

- Per-task implementation details for P2 cleanups (listed terse; any of them could land as a same-day change without further design).
- Backwards-compatibility shims — there are no deployed users besides the author, so migrations can be single-path and one-shot.

---

## Convention: WHY-comments on security code

All code touched by this remediation that enforces a security property must carry a short comment explaining **why** the code is shaped this way. The comment names the property being enforced and the consequence of getting it wrong — not what the code does (the code already shows that).

**Good:**

```go
// Always do the Argon2 work against a dummy entry on miss so that
// valid-prefix-wrong-suffix and no-such-prefix have matching latency.
// Without this, an attacker who can submit auth attempts can enumerate
// live prefixes via timing and narrow brute-force to the 8-char suffix.
```

**Bad (says what, not why):**

```go
// Compute the hash and compare.
```

**Bad (references the ticket, not the property):**

```go
// Fix for finding #14.
```

### Scope: new and existing code

This convention applies to **both** new code written for this remediation and existing security-relevant code that's currently under-commented. When touching a file that already enforces a security property without explaining why, add the comment in the same change. **T#28** covers the files we don't touch otherwise:

- `internal/proxy/decide.go` (intercept/tunnel/reject decision)
- `internal/proxy/pipeline.go` secret-scope-violation 403 path
- `internal/secrets/masterkey.go` key rotation ordering (new key before re-encrypt commit)
- `internal/ca/leaf.go` leaf validity / refresh buffer
- `internal/agents/registry.go` constant-time compare
- `internal/dashboard/auth.go` bearer token comparison

Comments explain invariants, not what the code does, not ticket numbers, not design history. Don't write "added for the rotation migration" or "see commit abc123" — those rot.

---

## P0 — before the next release

### Filesystem and OS-level hardening

- **T#3** — Change every `os.MkdirAll` in agent-gateway's XDG paths from `0o750` to `0o700` (`cmd/agent-gateway/serve.go:118-126`, `internal/config/config.go:404-410`, `internal/secrets/masterkey.go:203`, `internal/dashboard/auth.go:38,56`, `internal/store/store.go:17`). Add a startup self-check that `os.Stat`s each critical dir and refuses to start if the mode is more permissive than `0o700` or the owner is not the current UID — `MkdirAll` does not narrow permissions on an existing directory, so without the check, upgraded installs stay wide.
- **T#4** — After `sql.Open`, chmod `state.db`, `state.db-wal`, `state.db-shm` to `0o600`. WAL/SHM files are created lazily on first write, so the sequence is: open → `PRAGMA journal_mode=WAL` → warm-up write → chmod all three. Also set process `umask(0o077)` at startup in `cmd/agent-gateway` so any future file creation defaults owner-only.

### Network boundary

- **T#13** — Port `ValidateLoopbackAddr` from `mcp-broker/internal/server/addr.go` verbatim. Call it on both `proxy.listen` and `dashboard.listen` in `internal/config/validate.go`. Reject non-loopback binds at startup. No opt-out knob — matches mcp-broker's stance that the network boundary is load-bearing, and Lima's `host.lima.internal` forwarding already covers sandbox→host.
- **T#5** — In `internal/proxy/connect.go:23-34`, remove the silent fallback to raw host on `hostnorm.Normalize` error. Return a hard CONNECT reject instead. Also fix the same pattern at `decide.go:144-146`. No config knob for an escape-hatch.

### Auth and config bounds

- **T#2** — Raise Argon2id parameters in `internal/agents/registry.go:26-32` to `time=2, memory=19*1024, threads=1, keyLen=32` (OWASP 2023 floor). Keep as constants; no config knob for now. Existing hashes keep verifying (argon2id is parameter-aware in its encoded form) — implement re-hash-on-next-successful-auth so upgraded installs silently migrate. WHY-comment: the parameters are deliberately tuned above the OWASP floor because a compromised DB snapshot enables offline brute-force; dropping below the floor re-introduces that attack.
- **T#12** — In `internal/config/validate.go`, add bounds: `audit.retention_days ∈ [0, 3650]`, `proxy_behavior.max_body_buffer ∈ [0, 100 MiB]`, `approval.max_pending ∈ [0, 10000]`. Refuse to start on out-of-range with an error that names the field and the cap. Ships in the same validator as T#13.
- **T#6** — In `internal/dashboard/auth.go:116-126` and `dashboard.go:193-197`, set cookie `Secure` dynamically: `r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"`. Drop both `TODO(TLS)` comments.

### HTTP server hardening

- **T#19** — In `internal/proxy/connect.go:294-302` set `http2.Server{ MaxConcurrentStreams: 100, MaxReadFrameSize: 16<<10, MaxDecoderHeaderTableSize: 4096, MaxEncoderHeaderTableSize: 4096 }` alongside existing `IdleTimeout`. On the HTTP/1 `http.Server` in `serveH1`, set `MaxHeaderBytes: 64<<10`. Hardcoded. WHY-comment: these caps mitigate Rapid Reset (CVE-2023-44487) and CONTINUATION flood (CVE-2024-27316); the stdlib's defaults assume a public-facing server tuned for max throughput, which is the wrong shape for a sandbox proxy.
- **T#20** — In `serveConn` (`internal/proxy/connect.go:39-53`), set `conn.SetReadDeadline(time.Now().Add(p.readHeaderTimeout))` before `http.ReadRequest`, then clear with `conn.SetDeadline(time.Time{})` before the MITM/tunnel handoff. Prevents Slowloris goroutine leak from clients that open TCP and never send a full request line.

---

## P1 — Crypto: AES-GCM AAD + DEK/KEK split (T#1)

**T#1** bundles finding #1 (AEAD without AAD) with the agent-vault prior-art recommendation (DEK/KEK split). Both touch the `secrets` table; combining them is one migration instead of two.

### Key hierarchy (new)

```
keychain master key (per-install, rotatable)
       │
       ▼
  KEK (derived, Argon2id or HKDF over master-key)
       │
       ▼
  DEK (random 32 bytes per install, wrapped by KEK, stored in meta table)
       │
       ▼
  row ciphertext (AES-256-GCM with AAD = name || 0x00 || scope)
```

**Why the split matters.** Today, rotating the keychain master key forces a transactional re-encryption of every secret row. With DEK/KEK, rotation rewraps a single ~60-byte blob in the `meta` table. Row ciphertexts are untouched — orders of magnitude less write amplification, and rotation becomes safe to do opportunistically.

**Why AAD.** Without AAD, `(ciphertext, nonce)` is not bound to the row's identity. An attacker (or a buggy migration) with DB write access can swap ciphertext/nonce between rows. The master key still verifies integrity, and the proxy injects the wrong credential into requests scoped to a different secret. AAD = `name || 0x00 || scope` binds the ciphertext to the row; a swap produces a decrypt failure.

### Schema change

New `meta` table rows (or new columns if a meta table already exists):

```
dek_wrapped    BLOB    -- DEK wrapped by KEK, 32 bytes plaintext + GCM overhead
dek_nonce      BLOB    -- 12-byte nonce for the wrap
kek_kdf_salt   BLOB    -- salt for Argon2id-over-masterkey
```

`secrets` table unchanged in shape; row ciphertexts are just re-encrypted with the new DEK + AAD.

### Migration

Single one-shot migration triggered on startup when schema version < N:

1. Generate random DEK (32 bytes).
2. Derive KEK from active keychain master key + fresh salt.
3. Wrap DEK with KEK, write to `meta`.
4. For each row in `secrets`: decrypt-without-AAD with old master-key path; re-encrypt with DEK + AAD `(name || 0x00 || scope)`; update row.
5. Bump schema version, commit.

The migration runs in a single transaction. On crash mid-migration, restart retries (old path still works because schema version didn't bump). No backwards-compat shim is needed once the bump lands — the no-AAD code path is deleted in the same change.

### Master-key rotation path (simplified)

Before: decrypt every row with old master key, re-encrypt with new master key, commit.
After: derive new KEK from new master key, rewrap DEK, update `meta`. Done. Row count irrelevant.

---

## P1 — Dashboard security overhaul (T#7)

**Order matters within T#7:** the CSP directive `script-src 'self'` blocks inline `onclick='…'` handlers, which the current dashboard uses. Rewriting the handlers must land together with the CSP middleware, or the dashboard breaks on deploy.

- Replace every `innerHTML` concatenation in `internal/dashboard/app.js:259-264` (and any similar spots) with `document.createElement` + `addEventListener` closures. No user-derived value touches HTML event attributes. WHY-comment near the button-build site: "DOM APIs + addEventListener instead of innerHTML+onclick because the CSP forbids inline handlers, and string-concatenating user-derived IDs into attribute context is the class of bug that produced XSS-via-onclick in similar dashboards."
- Add middleware around the dashboard mux in `internal/dashboard/dashboard.go` setting: `Content-Security-Policy: default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'`, plus `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: no-referrer`. Exclude `/ca.pem` or scope its content-type separately.

---

## P1 — Pipeline hardening

- **T#9** — In `internal/inject/template.go:85-111`, at the secret return point, reject values containing `\r`, `\n`, or any byte `< 0x20` other than `\t`. New error `ErrSecretInvalid` (distinct from `ErrSecretUnresolved`) synthesizes a 403 in the pipeline. Log which secret was rejected. WHY-comment: last defensive layer before credentials hit the network; assumes nothing about upstream Transport validation because HTTP/2 has historically let CRLF-adjacent bytes through.
- **T#10** — In `internal/proxy/buffer.go:85-89` + `pipeline.go:236-240`, when `bufferBody` hits a hard I/O error AND `NeedsBodyBuffer` was true, synthesize a 403 with `X-Request-ID` instead of warning-and-proceeding. Align with the existing fail-closed path at `pipeline.go:252-259`.
- **T#22** — In `internal/rules/match.go:152-161`, lowercase `req.Path` before path-glob evaluation and uppercase `req.Method` before method compare. Update `compileGlob` in `parse.go:545` if the compiled pattern needs to be lowercased at compile time for consistency. Header values stay case-sensitive; document `(?i)` inline flag.

---

## P1 — Rules and secret scoping

- **T#24** — Apply existing `warnIfPublicSuffix` from `config/validate.go:95-108` to each `allowed_hosts` entry in `internal/secrets/store.go:345-389`. **Reject** rather than warn — this is the credential-scoping layer; fail loud. Share the check via a helper so both sites stay consistent.
- **T#18** — In `internal/approval/broker.go`, track pending count per agent name alongside the global `MaxPending`. Reject with `ErrQueueFull` when a single agent exceeds the per-agent cap. New config field `approval.max_pending_per_agent` (default 10, below global 50).

---

## P1 — New features

- **T#8** — Replace the `sync.Map` leaf-cert cache in `internal/ca/leaf.go:31-59` with `hashicorp/golang-lru/v2`. Hardcoded cap of 10 000 entries; evict oldest on insert when full. Keep existing `sweepExpired` for near-expiry eviction. While in the file, add a clock-skew buffer to cert validity checks (agent-vault precedent). WHY-comment: the cache is both a perf optimization and a DoS target — unbounded growth is exploitable by an authenticated-but-hostile agent opening CONNECT to many unique hosts.
- **T#26** — Port `internal/netguard/netguard.go` from agent-vault. Block `169.254.169.254` and `fd00:ec2::254` **unconditionally**. Block RFC1918 / loopback / link-local by default with opt-out `proxy_behavior.allow_private_upstream = false`. Resolve DNS **before** the block check so public hostnames pointing at private ranges still get caught. Wire into the upstream dialer, not the CONNECT-time check (CONNECT host is user-supplied; DNS happens at dial).
- **T#21** — Typed `EventKind` in `internal/dashboard/sse.go`. Exported constants for known kinds; `Broadcast` takes `EventKind` instead of `string`. Removes the untrusted-string-in-SSE-header class at compile time.

---

## P2 — Cleanups

Terse list. Each is a same-day change with no design surface.

- **T#14** — `MinVersion: tls.VersionTLS13` in `internal/ca/leaf.go:191` (leaf) and `cmd/agent-gateway/serve.go:228` (upstream Transport). No legacy-compat knob.
- **T#15** — Add `MaxPathLen: 0, MaxPathLenZero: true` to the root CA template in `internal/ca/root.go:165-175`. Both flags are required because `0` alone is ambiguous with "unset" in Go's x509. WHY-comment: the gateway only ever signs leaves; the constraint makes that explicit so a leaked `ca.key` can't mint intermediates.
- **T#16** — Convert PID-file acquire in `internal/daemon/pidfile.go:50-69` to `O_CREATE|O_EXCL|O_WRONLY, 0o600`. On `EEXIST`, do the existing liveness + comm check; if stale, `os.Remove` and retry the exclusive create once.
- **T#17** — In `internal/audit/audit.go:240-248`, switch `LIMIT`/`OFFSET` from `fmt.Sprintf` to placeholder `?` + args. In `internal/dashboard/dashboard.go:305-311`, clamp parsed limit at 10000 rather than error.
- **T#25** — Reorder SIGHUP handler in `cmd/agent-gateway/serve.go:373-394` so agent registry reloads **before** rules (current order rejects newly-added agents in a millisecond window).

---

## Documentation

- **T#11** — `docs/security-model.md` additions:
  - **CA trust-store scope** — intended for sandbox trust stores only; importing into host trust turns a `ca.key` leak into arbitrary MITM against the user's browser/host traffic.
  - **Tunnel-mode XFF** — raw TCP relay means agents can prepend `X-Forwarded-For` / `True-Client-IP` / `Forwarded`; upstream services treating those as authoritative must not trust tunnel-routed traffic.
  - **Operational security** section: audit-log sensitivity (file perms, retention, what not to share); compromise-response playbook for admin token, `ca.key`, agent token, master key. Skip rotation cadence (opted against forced rotation). Skip detailed backup guidance until asked.
- **T#23** — Add a "What SIGHUP reloads vs what requires restart" table to `docs/cli.md` (or wherever SIGHUP is already referenced). Lift the table from `SECURITY_AUDIT.md` lines 1082-1097. CLAUDE.md is already correct on this.
- **T#27** — Add a "non-cooperative sandbox" recipe to `docs/sandbox-manager.md` showing how to pin Lima iptables so only `host.lima.internal:8220` is reachable from the sandbox. Doc-only; no shipped tooling. Reference agent-vault as prior art.

---

## Deliberately skipped

Thirteen findings the audit raised that we decided against, with rationale.

### Skipped: not in scope (single-user tool, trusted operator)

- **#6** — Rule regex complexity bound. A hostile operator authoring ReDoS regexes is out of scope. RE2's linear guarantee already covers accidental pathology.
- **#22 (dashboard half)** — Dashboard rate limiting. Loopback-only means an attacker capable of reaching the dashboard already owns the host.
- **#35** — `$EDITOR` trust-surface doc note. Standard Unix semantics; documenting dilutes real security notes.
- **#39** — Agent/secret name validation (control chars, length). Operator is the only one naming things.

### Skipped: out of threat model

- **#12** — Query-string redaction in audit log. Injection is header-only; agents only ever send dummy credentials in request URLs.
- **#14** — Constant-time prefix lookup. Post-T#13 (loopback enforced), the attacker is already on the host and can read the SQLite hashes offline; timing oracle adds nothing. Also introduces a CPU-DoS vector (forced Argon2 work per auth miss) without T#22-style rate limiting.
- **#28** — Sandbox env token exposure. The sandbox **is** the security boundary; the agent is expected to hold this token. `/proc/<pid>/environ` reads from inside a compromised sandbox are post-boundary.

### Skipped: cost > benefit at this scale

- **#13 (lifetime cut)** — 10yr → 1yr CA lifetime + expiry warning. Limited blast radius in intended deployment (CA trusted only by sandbox; host MITM requires host-local access that already yields other paths). Keeping 10yr; doc note covers the misuse case.
- **#18** — Admin-token expiry. Doesn't reduce blast radius in loopback/single-user (anyone with local read of `admin-token` also has `ca.key`, master-key fallback file, and audit DB).
- **#25** — Keychain instance-scoping via config-dir hash. Not running multi-instance; hash would break on `XDG_CONFIG_HOME` rename.
- **#26** — SSE drop-signal event. Dashboard reconnect already refetches pending approvals; paginated `/api/audit` covers missed rows.
- **#27** — Stdout token leak. Deferred (not rejected). Revisit if launchd/journald logging becomes an issue in practice.
- **#40** — `singleConnListener.Accept` nil-conn block. Current behavior correct under `http.Server.Serve` contract; adding context plumbing to prove something already true isn't worth the code cost.

---

## Dependencies and sequencing

Most tasks are independent. The hard dependencies:

- **T#7 (CSP headers and DOM rewrite)** — single change. The CSP's `script-src 'self'` blocks inline `onclick` handlers; deploying CSP without the rewrite breaks the dashboard.
- **T#3 + T#4** — filesystem perm changes touch the same startup path. The startup self-check from T#3 also validates T#4's chmod took effect.
- **T#13 + T#12** — both land in `internal/config/validate.go`.
- **T#19 + T#20** — both touch `internal/proxy/connect.go`.

Soft ordering:

- **T#11 (security-model.md ops runbook)** is most accurate after T#13 (loopback enforcement) so the doc describes enforced behavior, not aspirational.
- **T#28 (existing-code comment sweep)** can land first or last — it changes comments only, no behavior. Landing first gives new work a comment baseline to match.

Beyond those, ordering is flexible.
