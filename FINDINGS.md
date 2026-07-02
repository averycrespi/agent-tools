# agent-tools — Deep Review Findings

Read-only staff-level assessment. Scope: all six Go modules + the auxiliary `hindsight/` stack, reviewed against each tool's `DESIGN.md` (treated as intent). Every finding cites `file:line`. Where I'm inferring intent or couldn't fully verify, I say so.

Bottom line up front: **the design is genuinely strong and ahead of the field on the hard parts** (host-side credential custody, interactive human-in-the-loop approval, fail-closed policy, VM-grade isolation). The gaps are concentrated in three places: (1) the sandbox↔host boundary has two holes that let an untrusted agent reach past the broker, (2) there is no enforcement layer (no CI) behind an otherwise excellent `make audit` culture, and (3) a handful of code/DESIGN divergences where the code is the bug.

---

## 1. Executive summary

1. **The sandbox has unrestricted network egress by default.** The embedded Lima template (`sandbox-manager/internal/sandbox/files/lima.yaml`) declares no `networks`/egress restriction, so the guest gets full outbound internet. This undercuts the entire premise — the broker and proxies exist so the sandbox needs neither credentials nor network, but a prompt-injected/compromised agent can exfiltrate directly and pull arbitrary code, bypassing every audited path. **This is the single highest-leverage gap.** Current SOTA (Claude Code, Codex) treats default-deny egress as mandatory, not optional.

2. **`local-git-mcp` lets the agent point git at an arbitrary transport URL.** `remote`/`refspec` are passed to git verbatim after `--`; git accepts a URL, not just a configured remote name. Concrete, no-misconfig-needed impact: SSRF to any http/https/ssh host using the _host's_ credentials and SSH keys, and cross-repo exfiltration via `file://`. Latent RCE via `ext::` (blocked only by git's default `protocol.ext.allow=never`). The `--` separator the CLAUDE.md relies on stops flag injection but is irrelevant to this class.

3. **The Telegram approval prompt is HTML-injectable by the untrusted agent** (`mcp-broker/internal/telegram/telegram.go:71`). Agent-controlled argument values are interpolated into an `HTML` parse-mode message without escaping. An agent can forge/spoof what the human approver sees — directly attacking the human-in-the-loop control that is the product's reason to exist. (The dashboard, by contrast, escapes correctly.)

4. **No CI exists at all** despite a strong `make audit` culture. Integration/e2e tests exist but are wired to no aggregate target, so they effectively never run; `govulncheck` is soft-failed via `|| echo` in two Makefiles. Every quality gate depends on a human remembering to run it in the dirs they touched.

5. **A worktree branch-name collision silently targets the wrong worktree on a destructive path.** `feat/thing`, `feat.thing`, `feat-thing` all sanitize to one identity, so `wt rm` can remove a different worktree than the branch it deletes (`worktree-manager/internal/config/config.go:105`).

6. **Two broker concurrency bugs bite under normal agent behavior** (parallel tool calls): stdio backend stderr is never drained (a chatty backend deadlocks the pipeline), and concurrent Telegram approvals collide on one `getUpdates` stream, silently losing human decisions.

7. **Several code/DESIGN divergences where the code is the bug:** audit never records tool results (DESIGN promises it); `::1` is documented-valid but can't actually bind; `config.Image` is a dead field with a test that gives false confidence; Telegram's "4096 char" limit counts runes, not Telegram's UTF-16 units.

8. **`local-gomod-proxy` is the model tool** — locked reverse-proxy target, argv-only shell-out, correct `GOMODCACHE` containment, constant-time auth, sound shutdown/cancellation. Findings there are informational/low. Dependency hygiene across all six modules is unusually clean (aligned, pinned versions; no `replace`/pseudo-versions).

9. **Missing capabilities the stated purpose implies:** default-deny egress (see #1); `tools/list` pinning/diff to defeat tool-poisoning/rug-pulls (now the biggest unaddressed MCP attack class); outbound secret redaction in the proxy pipeline (gateway table-stakes); `--version`/release/tagging.

---

## 2. Architecture assessment

**Mental model.** The system is a set of host-resident capability brokers around one untrusted compute environment:

```
        HOST (trusted, holds all secrets)                 SANDBOX (untrusted-ish)
  ┌───────────────────────────────────────┐          ┌──────────────────────────┐
  │ mcp-broker  ── rules→approval→proxy ──▶│ backends │  agent (Claude/etc.)     │
  │   (holds OAuth tokens, audit SQLite)   │◀─ /mcp ──│   only MCP server = broker│
  │ local-git-mcp   (host git creds)       │◀─ stdio ─│                          │
  │ local-gomod-proxy (host git creds)     │◀─ HTTPS ─│  go build                │
  │ telegram-mcp    (bot token)            │          │                          │
  └───────────────────────────────────────┘          └──────────────────────────┘
        ▲ sb (Lima VM lifecycle)  ▲ wt (worktrees, host-side)   shared writable mounts
```

The **core decoupling insight is correct and well-executed**: agent autonomy is separated from system privilege. Secrets never enter the sandbox; the agent only makes tool calls; a trusted host layer holds credentials and applies policy. The broker's fail-closed default (`RequireApproval`), loopback-only enforcement, constant-time token comparison, and interactive approval put it ahead of most published MCP gateways.

**Where the boundary is load-bearing and correctly placed:**

- Loopback binding enforced before listen in broker and gomod-proxy (`ValidateLoopbackAddr`); the bearer/basic-auth token is explicitly defense-in-depth. Good, and matches SOTA guidance that host-only binding is _not_ an auth boundary on its own.
- Path allowlists in both `local-git-mcp` and `local-gomod-proxy` use the correct `filepath.Rel` idiom, not `strings.HasPrefix` — `/repo-evil` and `/repo2` are correctly rejected (`local-git-mcp/internal/git/git.go:227`, tested at `git_test.go:271`). This is the thing most likely to be a naive-prefix bug, and it isn't.
- Host-side credential custody with no token passthrough is exactly the pattern current MCP-security guidance prescribes.

**Where it's fragile:**

- **The boundary is porous in two ways that let the agent reach past the broker entirely:** open network egress from the VM (§Finding H1) and arbitrary-URL git transport (§Finding H2). Both defeat the "agent has no network/credentials" guarantee that everything else is built on. The broker can audit and gate _credentialed MCP calls_; it cannot see raw guest egress or a git push to `https://attacker/`.
- **The writable-mount + shared-path model couples the tools in a way that isn't documented as a risk.** Lima mounts are unconditionally `writable: true` at `mountPoint == location` (host path == guest path). That's why `local-git-mcp` paths line up host/guest — but it also means the agent can plant a symlink in the shared mount that the host-side `local-git-mcp` will follow (§Finding H3), and can write to any mounted host dir as the host UID.
- **The human-in-the-loop channel is trusted to render faithfully, but the Telegram path doesn't** (§Finding H4).

**On the "copy small helpers, don't share a module" convention:** it's paying off, not drifting. The six `exec.Runner` copies are genuinely small and appropriately specialized (gomod-proxy's adds context for subprocess cancellation; sandbox-manager's adds `StartPiped`). The one real cost of the copy convention showing up as a bug: `sandbox-manager`'s runner never adopted the context-aware pattern the root CLAUDE.md mandates (§Finding M9).

---

## 3. Prioritized findings

Ranked by leverage. Tags: **[FIX]** something is wrong · **[HARDEN]** security/robustness · **[ADD]** missing capability · **[SIMPLIFY]** remove complexity. Confidence noted where it's not "confirmed bug."

### H1 — [HARDEN/ADD] Sandbox has unrestricted default egress (breaks the isolation premise)

- **Severity: High · Effort: M · Impact: very high**
- **Evidence:** `sandbox-manager/internal/sandbox/files/lima.yaml` — no `networks:` block, no egress policy; Lima defaults to full user-mode outbound networking. The design docs sell the broker/proxies as the reason the sandbox needs no direct network, but nothing stops direct egress.
- **Why it matters:** A prompt-injected or misbehaving agent can exfiltrate data and fetch arbitrary code straight out of the VM, never touching the audited broker. The "lethal trifecta" (private data + untrusted content + external comms) is fully present. Every other control in the repo is downstream of this assumption.
- **Direction:** Default-deny egress with an allowlist enforced _outside_ the guest, and route allowed traffic through the existing host-side proxies. Block IMDS (169.254.169.254) and filter DNS. Anthropic's own `@anthropic-ai/sandbox-runtime` and Codex's fail-closed network model are the reference designs. **Critical implementation note:** the hostname-parser is the vulnerability class here — validate on the _resolved IP_, reject non-DNS characters, and never use naive `endsWith`/suffix matching (Claude Code's allowlist was bypassed twice via SOCKS5 null-byte host injection). At minimum, document that egress is currently open so users don't over-trust the sandbox.

### H2 — [HARDEN/FIX] `local-git-mcp` accepts arbitrary git transport URLs in `remote`/`refspec`

- **Severity: High (latent Critical) · Effort: M · Impact: very high**
- **Evidence:** `local-git-mcp/internal/git/git.go:52-101` (push/pull/fetch/ls-remote build `git … -- <remote> [refspec]`); `remote` comes verbatim from `tools.go:196-226` with no check that it names a configured remote. `--` blocks flag injection but a git remote positional may be a full URL.
- **Why it matters:** No host misconfiguration required: the agent can `fetch`/`push`/`ls-remote` to any `http(s)://`/`ssh://` host using the host's SSH keys and credential helpers (SSRF + credential use), and `file://` pulls objects from _any other git repo on the host_ into a repo the sandbox reads — cross-repo exfiltration that defeats the allowlist's whole intent. RCE via `ext::sh -c …` is latent, gated only by git's default `protocol.ext.allow=never`. The CLAUDE.md line "always use `--` … to prevent argument injection" gives false confidence about this class.
- **Direction (defense in depth, do more than one):** (a) validate `remote` against actually-configured remotes (`git -C <repo> remote get-url -- <name>`; reject on failure) or reject any value containing `://` or `::`; (b) set `GIT_PROTOCOL_FROM_USER=0` and/or an explicit `-c protocol.allow`/`GIT_ALLOW_PROTOCOL=ssh:https:http:git` on every shell-out; (c) reject URL-shaped `refspec`. Option (a) matches DESIGN's own framing of `remote` as a name defaulting to `origin` and is the tightest fit.

### H3 — [HARDEN] `local-git-mcp` allowlist is not symlink-resolved

- **Severity: Medium/High (scales with how writable the allowed prefix is) · Effort: S/M · Impact: high**
- **Evidence:** `local-git-mcp/internal/git/git.go:167-180` (`ValidateRepo`) does `filepath.Clean` + `isAllowedPath` but never `filepath.EvalSymlinks`; `normalizeAllowedPaths` (`:202`) also only cleans.
- **Why it matters:** The canonical deployment shares a worktree the agent can write to. The agent creates `/<allowed>/escape → /home/user/other-repo`; `Clean` doesn't resolve it, `isAllowedPath` accepts it, and `git -C` follows the symlink to an out-of-allowlist host repo. DESIGN §Security explicitly claims this can't happen.
- **Direction:** `EvalSymlinks` both the allowed prefixes and the cleaned `repo_path` before the prefix check; accept residual same-user TOCTOU as out of scope and document it.

### H4 — [FIX] Telegram approval prompt is HTML-injectable (approval-prompt spoofing)

- **Severity: High · Effort: S · Impact: high**
- **Evidence:** `mcp-broker/internal/telegram/telegram.go:71` (and `:332`) interpolate agent-controlled `argsStr`/`tool`/`desc` into an `HTML` parse-mode message with no escaping; sent with `parse_mode:"HTML"` at `:189`. Dashboard escapes via `esc()` (`index.html:1315`); Telegram path does not.
- **Why it matters:** Two impacts — (1) a malicious arg value with markup can forge what the human approver sees (`</pre>…✅ looks safe…`), subverting human-in-the-loop; (2) a bare `<` yields invalid Telegram HTML → `sendMessage` returns `ok:false` → `Review` errors → that call can't be approved via Telegram (denial-of-approval).
- **Direction:** HTML-escape `&`,`<`,`>` in `tool`, `desc`, and `argsStr` before interpolation; add a test with those characters in args.

### H5 — [FIX] Worktree branch-name collision → wrong target on a destructive path

- **Severity: High · Effort: M · Impact: high (data loss)**
- **Evidence:** `worktree-manager/internal/config/config.go:105-107` (`SanitizeBranch` maps every non-`[A-Za-z0-9-]` to `-`); worktree dir + tmux window key off the _sanitized_ name while git ops use the _raw_ branch (`internal/workspace/workspace.go:155`, `internal/git/git.go:97`). `feat/thing`, `feat.thing`, `feat-thing` collapse to one identity.
- **Why it matters:** `wt add feat-thing` after `wt add feat/thing` sees the dir exists, says "already exists," and drops you in the wrong worktree; `wt rm feat-thing -d` then removes `feat/thing`'s worktree while deleting the `feat-thing` branch — removal and deletion diverge, silently.
- **Direction:** Record the real branch per worktree (metadata file or parse `git worktree list --porcelain`) and refuse when an existing worktree's branch differs from the requested one. Add the missing collision test.

### H6 — [FIX] Stdio backend stderr is never drained → deadlock wedges the broker

- **Severity: High · Effort: S · Impact: high**
- **Evidence:** `mcp-broker/internal/server/stdio.go:27` uses mcp-go's stdio client, which pipes child stderr but never reads it; the broker never calls `GetStderr` either (grep: no `Stderr` reader in `internal`/`cmd`).
- **Why it matters:** Most stdio MCP servers log to stderr. After ~64 KiB of unread stderr the child blocks on write; if it blocks mid-request, `CallTool` hangs until the agent's context dies and the backend is wedged for the broker's lifetime (no runtime reconnect by design). Backend errors are also silently swallowed.
- **Direction:** After spawn, `io.Copy` `GetStderr(c)` into `logger.Debug/Warn` per line until EOF.

### H7 — [FIX] Concurrent Telegram approvals collide on one `getUpdates` stream → lost decisions

- **Severity: High · Effort: M · Impact: high**
- **Evidence:** `mcp-broker/internal/telegram/telegram.go:93-133` — each `Review` runs its own `pollForDecision` with local `offset:=0` calling `getUpdates` (`:223`). Telegram permits only one outstanding `getUpdates` per bot.
- **Why it matters:** Agents routinely issue parallel tool calls. Two pending approvals ⇒ two concurrent long-polls ⇒ 409/`ok:false` thrash every 2 s; worse, whichever poller advances the offset _consumes_ the other's callback update and discards it (`MessageID != messageID`), so the human's tap on the other request is silently lost and it times out.
- **Direction:** Single owner for the update stream — one long-poll goroutine per approver, dispatching callbacks to waiting Reviews by `message_id` over channels; or serialize Reviews.

### M1 — [HARDEN/FIX] Malformed tool-name globs silently fail-open

- **Severity: Medium (borders High) · Effort: S**
- **Evidence:** `mcp-broker/internal/rules/rules.go:97-99` — `filepath.Match` error ⇒ `continue`. Arg paths/regexes are validated at `rules.New`, but tool globs are not.
- **Why it matters:** A malformed _deny_ pattern (e.g. `github.[delete`) is silently skipped; if a broad `allow *` follows, the intended deny evaporates and the call is allowed. Inconsistent with the args-validated-at-startup posture, with no operator feedback. (This is documented as intentional at `rules.go:60`, but the intent predates arg validation and should be revisited.)
- **Direction:** Validate each `Tool` glob in `rules.New` (`filepath.Match(pat, "x")`, reject `ErrBadPattern`), failing startup like bad paths/regexes.

### M2 — [HARDEN] Deny rules with `args` constraints are evadable

- **Severity: Medium · Effort: S (doc/validate) / M (semantic split)**
- **Evidence:** `mcp-broker/internal/rules/rules.go:95-107` + `match.go:141` — arg-resolution failure ⇒ rule skipped, evaluation continues; first-match at `broker.go`. DESIGN.md:119 frames fail-closed as unambiguously safe.
- **Why it matters:** Fail-closed is correct for `allow` (can't over-allow) but is effectively _fail-open for `deny`_: an agent that renames/omits/nests/retypes the constrained argument makes the deny miss and falls through to a following `allow`. Combined with the documented non-anchored-regex footgun (`match.go:29`, substring `MatchString`), arg-based denies give false confidence.
- **Direction:** Either make arg-resolution failure on a `deny` rule _match_ (deny fail-closed), or validate/document that `deny` verdicts are tool-name-only. Prefer the latter for predictability; call it out in DESIGN. Consider a startup warning for non-anchored regexes.

### M3 — [FIX] Audit never records the tool result (contradicts DESIGN)

- **Severity: Medium · Effort: M (implement) / S (doc)**
- **Evidence:** DESIGN.md:62/§Audit promise result is recorded. `mcp-broker/internal/audit/audit.go:16-25` has no result field/column; `broker.go:151-163` stores only `rec.Error` (and content only when `IsError`).
- **Why it matters:** DESIGN is the stated source of truth and sells "maximum observability," but the audit log can't answer "what did the tool return." Decide which is the bug.
- **Direction:** Add a size-capped JSON result column, or strike "result" from DESIGN.

### M4 — [HARDEN] Audit DB is created world-readable and stores args verbatim

- **Severity: Medium · Effort: S**
- **Evidence:** `mcp-broker/internal/audit/audit.go:71-100` creates the dir `0o750` but the SQLite file (+ `-wal`/`-shm`) inherits the driver default (typically `0644`); `Record` stores full arg JSON (`:104`).
- **Why it matters:** Config and token files are deliberately `0600`, but the most sensitive data — every tool call's arguments, which can carry secrets the agent passes through — is left readable by any local user. Inconsistent with the rest of the trust model.
- **Direction:** `OpenFile(path, …, 0o600)` (or chmod) before `sql.Open`; document that args may contain secrets.

### M5 — [FIX] `::1` host is documented-valid but can't bind

- **Severity: Medium · Effort: S**
- **Evidence:** `ValidateLoopbackAddr` accepts `::1` (asserted in `addr_test`), but `cmd/mcp-broker/serve.go:171` uses `fmt.Sprintf("%s:%d", host, port)` ⇒ `"::1:8200"`, which `net.SplitHostPort` rejects ⇒ startup fails. The IPv6 loopback DESIGN/CLAUDE list as allowed is unusable. Fails closed, but contradicts docs.
- **Direction:** `net.JoinHostPort(host, strconv.Itoa(port))`.

### M6 — [FIX] Telegram 4096 limit counts runes, not Telegram's UTF-16 units

- **Severity: Medium · Effort: S**
- **Evidence:** `telegram-mcp/internal/telegram/client.go:148` uses `utf8.RuneCountInString`; Telegram enforces 4096 UTF-16 code units. Every astral char (most emoji) is 2 units but 1 rune, so the local check is too permissive. DESIGN.md:63 makes "reject over-limit rather than split" an explicit guarantee — it leaks exactly for emoji-heavy agent output. The boundary test uses 4097 NUL bytes, where bytes=runes=UTF-16 coincide, so it can't catch this.
- **Direction:** `len(utf16.Encode([]rune(s)))`; add an astral-emoji boundary test.

### M7 — [FIX] `sandbox-manager` `config.Image` is a dead field with a misleading test

- **Severity: Medium · Effort: S**
- **Evidence:** `Image` is defined (`internal/config/config.go:14`), defaulted, assigned to `params.Image` (`internal/sandbox/sandbox.go:74`), but `lima.yaml` never references `{{.Image}}` — it hardcodes two Ubuntu 24.04 URLs. `template_test.go:26` "validates" it via `assert.Contains(out,"ubuntu-24.04")`, which passes only because that string is in the hardcoded URL. Setting `image: ubuntu-22.04` silently gets 24.04.
- **Direction:** Template the image block and honor the field, or remove `Image` and fix the test. (DESIGN.md:55 correctly lists only CPUs/memory/disk/mounts, so the code carries a trap the design never intended.)

### M8 — [HARDEN] Sandbox writable-mount + secret copy-in isolation caveat is undocumented; no read-only mounts

- **Severity: Medium · Effort: S (docs) / M (RO mounts)**
- **Evidence:** `lima.yaml:25` hardcodes `writable: true`; `internal/lima/lima.go:107` hardcodes `mount+":w"` — no way to declare a read-only mount. README seeds `~/.gitconfig` via `copy_paths`; users will predictably add tokens/keys, which the untrusted agent can then read and exfiltrate (compounded by H1).
- **Why it matters:** The default posture is sound (mounts empty by default, containerd off, loopback SSH), but the moment a user mounts `~` or copies in a credential, an untrusted agent gets host-side write as the host UID / secret read — and nothing in DESIGN/README flags this.
- **Direction:** Offer a read-only mount option; document the mount/copy-in boundary as a security caveat.

### M9 — [FIX] `sandbox-manager` runner lacks context/deadline (violates repo convention)

- **Severity: Medium · Effort: M**
- **Evidence:** `sandbox-manager/internal/exec/exec.go` uses bare `os/exec` with no `context.Context`/deadline across `Run`/`RunInteractive`/`StartPiped`. Root CLAUDE.md mandates "a context-aware runner with finite deadlines." A hung `limactl start/stop/edit` hangs `sb` indefinitely.
- **Direction:** Mirror worktree-manager's context-aware runner.

### M10 — [FIX] Worktree file-copy widens permissions on secrets

- **Severity: Medium · Effort: S**
- **Evidence:** `worktree-manager/internal/workspace/workspace.go:341` uses `os.Create(dst)` ⇒ `0644 & ~umask`, never mirroring source mode. The documented use copies `.env.local` / `.claude/settings.local.json` (secrets): a `0600` source lands `0644` (group/world-readable); executables lose `+x`. Test at `workspace_test.go:411` checks content only.
- **Direction:** `Stat` the source and open dst with `srcInfo.Mode().Perm()` (or chmod after); assert mode in the test.

### M11 — [HARDEN] SSE backends have no finite per-call HTTP timeout

- **Severity: Medium · Effort: S/M**
- **Evidence:** `mcp-broker/internal/server/http.go:41-46` applies `WithHTTPTimeout` for streamable-http only; `newSSEBackend` (`:72`) sets only a startup-derived `WithEndpointTimeout`. DESIGN.md:164's hung-backend protection silently doesn't cover SSE at runtime.
- **Direction:** Apply an equivalent per-call timeout to SSE, or document the asymmetry.

### M12 — [HARDEN] OAuth callback never validates returned `state`

- **Severity: Medium (Low given loopback+PKCE) · Effort: S**
- **Evidence:** `mcp-broker/internal/server/oauth.go:397` passes the broker's own generated `state` into `ProcessAuthorizationResponse`; the callback handler (`:413`) reads only `code`/`error`, never comparing `r.URL.Query().Get("state")`. Callback port is deterministic (`FNV32a(serverName)`, `:253`), so it's derivable.
- **Why it matters:** During the interactive OAuth window a malicious local origin/process can hit `localhost:<port>/callback?code=…` and inject a foreign authorization code (authz-code injection / account confusion). PKCE defends interception, not injection-when-state-unchecked. DESIGN/CLAUDE describe `state` "for CSRF protection" — code contradicts intent.
- **Direction:** Compare returned `state` before accepting the callback (verify whether mcp-go already does; belt-and-suspenders is cheap).

### M13 — [HARDEN] Bot token can leak into logs on Telegram network errors

- **Severity: Medium · Effort: S**
- **Evidence:** `mcp-broker/internal/telegram/telegram.go:136` builds `…/bot<token>/<method>`; on transport failure Go's `*url.Error.Error()` includes the full URL, and that error is logged at `:106`. (`telegram-mcp` already sanitizes this via `sanitizeError` — the broker's copy doesn't.)
- **Direction:** Strip the `/bot<token>/` segment before logging/wrapping; reuse the sibling's `sanitizeError` approach.

### M14 — [HARDEN] Config validation gaps (silent verdict coercion, dropped unknown fields, dotted server names)

- **Severity: Medium/Low · Effort: S**
- **Evidence:** `mcp-broker/internal/rules/rules.go:33` — any verdict typo (`"denny"`) silently becomes `RequireApproval`; `config.go:191` unmarshals without `DisallowUnknownFields`, and `Refresh` (`:242`) rewrites the file _dropping_ the unknown key (destroys a typo'd `tool_patches` silently); `manager.go:176` — servers `a`(tool `b.c`) and `a.b`(tool `c`) both register `a.b.c` and shadow each other nondeterministically by map order.
- **Why it matters:** This is the _policy_ file; every failure is silent and fail-closed-but-surprising (a typo'd `deny` quietly becomes approvable).
- **Direction:** Validate verdicts/types at load, reject `.` in server names, warn on unknown fields.

### Low / Simplify (grouped)

- **[FIX]** `dashboard.go:458` `generateID` ignores `crypto/rand` error (all-zero ID on failure) and uses 8 bytes; propagate the error and widen to 16. Theoretical but silent.
- **[SIMPLIFY]** Audit `Logger.mu` is held across `Record` _and_ the whole `Query` (`audit.go:119,175`), serializing reads against writes and negating the WAL concurrency DESIGN.md:228 claims; `database/sql` is already safe. Drop/narrow the mutex or correct the doc.
- **[SIMPLIFY]** Dashboard `decided` slice grows unbounded and is never served (no `/api/decided`), so DESIGN's "decided history" is lost on reload (`dashboard.go:67,189`); Telegram-decided requests appear as `removed`, not `decided`. Ring-buffer + serve it, or derive from audit and delete the field.
- **[FIX]** Client-cancel (agent disconnect / shutdown) is audited as `denied by timeout` in `multi.go:51` and `dashboard.go:132` and `telegram.go:75`; branch on `DeadlineExceeded` vs `Canceled`.
- **[HARDEN]** `ValidateLoopbackAddr` returns early for the literal string `localhost` without resolving it (`addr.go:22`); if `/etc/hosts` maps it off-loopback the hard guarantee is defeated. Resolve and assert `IsLoopback`, or bind explicit IPs.
- **[FIX]** Dashboard decision-vs-timeout TOCTOU (`dashboard.go:132`/`152`): a human click in the window after `Review` returned timeout still shows the call "approved" though it was denied and never executed. Resolve atomically.
- **[HARDEN]** `local-git-mcp`/`worktree-manager` don't set `GIT_TERMINAL_PROMPT=0`/SSH `BatchMode` — missing creds block on a prompt until timeout instead of failing fast.
- **[HARDEN]** `worktree-manager` config `copy_files`/setup-script paths are `filepath.Join`ed with no `..` containment (`workspace.go:297,317`); leading-dash branch becomes a git flag (`git.go:99`); all-digit sanitized branch aliases a tmux window _index_ (`tmux.go:72`). Config is trusted, so low, but cheap to guard given secrets are copied.
- **[SIMPLIFY]** `gomod-proxy` `New`/`NewWithTimeout` leave `gomodcache==""`, which disables the containment check (`fetcher.go:195`); only tests use them. Remove the non-GOMODCACHE constructors or make empty-cache a hard error so a future caller can't silently lose containment.

### Operational / repo-wide

- **O1 — [ADD] No CI. Severity: High · Effort: M.** No `.github/`, no CI config anywhere. Add a per-module matrix (mirrors the root Makefile forwarding) running `make -C <mod> audit` **plus** the integration/e2e targets, with a blocking `govulncheck`. Pin the toolchain via `go-version-file: go.work`.
- **O2 — [FIX] Integration/e2e tests never run in aggregate. Severity: High · Effort: S.** Real tagged tests exist (`mcp-broker/test/e2e`, `local-gomod-proxy/test/e2e`, `internal/**/integration_test.go`, etc.) but the root Makefile forwards only `test`/`audit` (which run `go test ./...`, skipping build-tagged files), and there's no root `test-integration`/`test-e2e`. Meanwhile `local-git-mcp`'s DESIGN.md:124 promises integration tests that **don't exist** (grep finds zero build-tagged files in that module — `make test-integration` passes vacuously). Add root aggregate targets and wire them into CI; write the missing `local-git-mcp` integration tests (they'd regression-guard H2/H3).
- **O3 — [FIX] `govulncheck` is soft-failed. Severity: Medium · Effort: S.** `mcp-broker/Makefile` and `local-gomod-proxy/Makefile` run `govulncheck ./... || echo …`, swallowing the non-zero exit for _all_ vulns, not just the stdlib advisory it was presumably added for. Remove the swallow in CI; scope any needed suppression to a specific advisory ID. (Watch the govulncheck-action SARIF gotcha — it exits success with findings unless you add a gating step.)
- **O4 — [ADD] No versioning/release/`--version`. Severity: Medium · Effort: M.** No git tags, no ldflags, none of the six binaries support `--version`. For agent-facing daemons run in sandboxes, there's no way to know which build is deployed. Add a `version` var via `-ldflags -X`, a `--version` flag (one line in Cobra), and start tagging. Consider GoReleaser + cosign v3 bundles + SLSA provenance later.
- **O5 — [HARDEN] gitleaks declared but dead. Severity: Medium · Effort: S.** `.tool-versions` pins `gitleaks 8.30.1` but nothing invokes it (`.husky/pre-commit` runs only lint/format). Wire it into the hook and/or CI, or drop it so it doesn't imply coverage.
- **O6 — [SIMPLIFY] Tooling drift. Severity: Low · Effort: S.** Lint command differs across Makefiles (`go tool golangci-lint run` vs `… run ./...`); `mcp-broker/.golangci.yml` diverges from the other five byte-identical configs in more than its (justified) G304 exclusion — the `_test\.go` vs `_test\\.go$` regex looks accidental. Normalize.
- **O7 — [SIMPLIFY] `fable-deep-exploration-prompt.md` is untracked and unignored** (the prompt for this review). Delete or gitignore. `go.work` is gitignored yet instruction-managed (root CLAUDE.md step 6) — for a repo organized around the workspace, committing `go.work` is the more consistent choice.

### Missing capabilities (ADD) implied by the stated purpose

- **Default-deny egress** (H1) — the biggest.
- **`tools/list` snapshot + diff / pinning.** The broker is the natural chokepoint and already has `tool_patches`; extend it to hash tool definitions at first sight (TOFU) and alert on drift. This defeats tool-poisoning, "line jumping," and rug-pulls — currently the single biggest unaddressed MCP attack class, and the broker sees every `tools/list`.
- **Outbound secret/PII redaction in the proxy pipeline.** Gateway table-stakes now (Docker `--block-secrets`, Lasso masking). The broker only relies on sandbox-layer scanning; a redaction pass on tool _results_ would catch tokens/PII flowing back to the agent.
- **Optional OTel/SIEM audit export.** Audit is SQLite-local; structured export is increasingly expected.

---

## 4. State-of-the-art notes (cited)

Only external context that changes a recommendation here.

**MCP authorization & attack classes.** The current spec (rev. 2025-06-18) recasts MCP servers as OAuth 2.1 Resource Servers: PKCE, RFC 8707 resource indicators, RFC 9728 protected-resource metadata, audience-bound tokens, and an explicit ban on token passthrough ([spec/authorization](https://modelcontextprotocol.io/specification/2025-06-18/basic/authorization), [security best practices](https://modelcontextprotocol.io/specification/2025-06-18/basic/security_best_practices)). The 2025 attack classes that matter for a _proxy_: tool poisoning and "line jumping" — malicious instructions in tool _descriptions_ delivered at `tools/list`, before any call ([Trail of Bits, 2025-04-21](https://blog.trailofbits.com/2025/04/21/jumping-the-line-how-mcp-servers-can-attack-you-before-you-ever-use-them/)); and the "lethal trifecta" ([Willison, 2025-06-16](https://simonwillison.net/series/prompt-injection/)). → Motivates the `tools/list` pinning/diff ADD and confirms the broker's host-side credential custody (no passthrough) is the right call.

**MCP gateway table-stakes.** Across Docker MCP Gateway, IBM ContextForge, Lasso, Pomerium, Solo agentgateway: proxy/aggregation + namespacing, client auth, upstream credential custody, tool allowlisting (unauthorized tools hidden from `tools/list`), policy (OPA/CEL emerging), audit-to-SIEM/OTel, and **in-flight secret/PII redaction** ([Docker MCP Gateway](https://github.com/docker/mcp-gateway) interceptors `--block-secrets`/`--verify-signatures`/`--log-calls`; [IBM ContextForge](https://github.com/IBM/mcp-context-forge)). Interactive per-call HITL approval is _rare_ — only MCP Guardian and a Windows preview have it. → `mcp-broker` is ahead on the rarest feature (interactive approval) and behind on redaction + OTel export.

**Sandboxing untrusted agents.** Isolation strength: containers < gVisor < microVMs/full VMs. Anthropic layers gVisor (claude.ai), Bubblewrap/Seatbelt (Claude Code), and full VMs via Virtualization.framework (Cowork) ([Willison summary, 2026-05-30](https://simonwillison.net/2026/May/)). Claude Code requires _both_ filesystem and network isolation, forcing all traffic through an out-of-sandbox allowlist proxy with the network namespace removed ([Anthropic, 2025-10-20](https://www.anthropic.com/engineering/claude-code-sandboxing), open-sourced as `@anthropic-ai/sandbox-runtime`); Codex disables network by default and fails closed ([OpenAI](https://developers.openai.com/codex/)). **The Lima full-VM choice is near best-in-class** — stronger than the Bubblewrap/Seatbelt/gVisor tier the coding agents themselves use. The gap is purely egress control (H1). Critical implementation lesson: Claude Code's egress allowlist was bypassed _twice_ via SOCKS5 hostname null-byte injection (`attacker.com\x00.google.com`) — a JS-`endsWith`-vs-libc-`getaddrinfo` parser differential ([writeup](https://oddguan.com/blog/)). → If you build egress allowlisting, validate on the resolved IP and reject non-DNS characters; don't suffix-match hostnames.

**Go supply chain.** `golang/govulncheck-action` gives per-PR reachability scanning, but with `output-format: json/sarif` it **exits success even with findings** — add a gating step ([action repo](https://github.com/golang/govulncheck-action)). For a `go.work` monorepo there's no single transitive scan; a per-module matrix (which your root Makefile already models) is the pattern; pin the toolchain via `go-version-file: go.work`. Release integrity: SLSA L3 via `slsa-github-generator`, GoReleaser + cosign **v3** keyless bundles (`*.sigstore.json`) ([cosign v3](https://goreleaser.com/blog/cosign-v3/)). → O1–O4.

**Host↔VM transport (better than a self-signed cert in the trust store).** Loopback binding is not an auth boundary; the load-bearing part is _client authentication_, not encryption. Best fit for one local VM, in order: (1) **Unix-domain socket forwarded over Lima's SSH channel** — drops TLS entirely (SSH gives authenticated+encrypted transport, `0600` socket perms give access control, no TCP listener at all, which kills the browser/DNS-rebinding surface) ([Lima port config](https://lima-vm.io/docs/config/port/)); (2) if TCP is required, **mTLS with short-lived certs** (step-ca + passive revocation); (3) minimum viable: keep the self-signed server cert but add a static client cert or bearer token — the _client_ auth is what protects the proxy's capabilities. Avoid SPIFFE/SPIRE/Tailscale/WireGuard (fleet problems you don't have). → Reframes `local-gomod-proxy`'s cert-in-trust-store approach: consider UDS-over-SSH for the host↔VM link; the current self-signed + basic-auth is the "minimum viable" tier and is acceptable, but the SSH-forwarded socket is strictly simpler and removes the TCP attack surface.

---

## 5. Open questions

1. **Egress posture (H1):** is the open guest network a deliberate accepted risk (you trust the model + rely on the sandbox only for filesystem/host-integrity), or an oversight? The answer changes whether H1 is "document it" or "build allowlisting." Everything else in the repo reads as if the sandbox is _not_ trusted with network.
2. **`local-git-mcp` threat model (H2/H3):** how untrusted is the agent in practice? If the broker is _always_ in front and you rely on its arg-matching rules to constrain `remote`, that mitigates H2 in deployment — but the tool ships insecure-by-default and the standalone DESIGN claims safety it doesn't have. Is standalone use (no broker) a supported mode?
3. **Audit "result" (M3):** did the intent change (results deliberately not stored, e.g. size/secret concerns) or is the code behind DESIGN?
4. **Telegram vs dashboard approver parity:** the dashboard is hardened (escaping, XSS) but the Telegram path lags (H4, M13, and the concurrency bug H7). Is Telegram approval considered a first-class control or a convenience? That sets how much to invest in hardening it.
5. **`hindsight/` scope:** I treated it as out-of-scope auxiliary per the docs. It mounts Codex OAuth provider credentials and runs Postgres — worth a note whether its localhost-binding + bearer-auth posture has had the same scrutiny as the Go tools (not reviewed here).
6. **Is a shared `go.work`/committed workspace + CI the intended direction,** or is the "six independent modules, build each alone" model deliberate enough that per-module CI is all you want? Affects O1/O7.
