# Configure smart-HTTPS Git access

Audience: Gateway administrators and native Git client operators

Purpose: Configure Git routing, repository permissions and protected client handoff, then verify a read-only exchange.

Gateway's Git proxy replaces the supported HTTPS access path of the retired Local Git MCP; it is not a drop-in replacement for its MCP commands, SSH transport or host credential helpers. Existing remotes and installed configurations are not rewritten automatically. It uses the HTTP proxy listener and client CA trust, but has **independent Git repositories, grants and upstream credentials**. HTTP defaults, request grants and HTTP credentials never grant Git access. Ordinary non-Git web requests still use HTTP policy.

## Configure a repository and read grant

Start the initialized Gateway and [create an agent and issue its credential](access-control.md#create-and-inspect-principals). Keep its HTTP default blocked unless separate HTTP access is intended. Use the same singular agent token for MCP and proxy authentication, never an administrator bearer.

In **Git → Repositories**, create the intended smart-HTTPS repository. The equivalent strict file is:

```json
{
  "name": "Team repository",
  "url": "https://github.com/team/repository",
  "aliases": ["https://github.com/team/repository.git"],
  "credential_id": null
}
```

This example deliberately selects public upstream access; it does not name a real fixture or guarantee that repository exists. For a private repository, [create Git-only credentials](#git-only-upstream-credentials) first and select that ID. Save the file, then:

```bash
agent-gateway git repository create --file /private/repository.json --yes
agent-gateway git repository list
agent-gateway git repository get REPOSITORY_ID
```

The canonical destination is immutable. Aliases may only add/remove terminal `.git` spelling at the same origin/base; there is no implicit URL equivalence. Review any alias suggested by the browser. A different destination needs a new repository identity and grants.

Create a complete read-only grant file, replacing the illustrative IDs with the returned agent and repository IDs:

```json
{
  "principal_id": "PRINCIPAL_ID",
  "repository_id": "REPOSITORY_ID",
  "description": "Read team repository",
  "expires_at": null,
  "policy": { "version": 1, "read": true, "refs": [] }
}
```

```bash
agent-gateway git grant create --file /private/grant.json --yes
agent-gateway git grant list
```

For deliberate branch creation/update, `refs` can include `{"ref":{"kind":"prefix","value":"refs/heads/team/"},"actions":["create","update"]}`; deletion stays denied. Exact selectors use `kind:"exact"` with a fully qualified ref. Write-only grants reject: push permission includes read. Granting a ref action is not permission for every remote ref.

## Enable the origin's Git routing

Inspect **Git → Routing** or `agent-gateway git routing-profile get`. A profile file contains the **complete proposed list**, for example:

```json
{ "origins": ["https://github.com"] }
```

```bash
agent-gateway git routing-profile get
agent-gateway git routing-profile update --file /private/profile.json --yes
```

Merge intended existing origins into that file rather than replacing them accidentally. Origins normalize to explicit effective ports; read-only `active:true` reports the enforcement gate. Enabling an origin refuses while a matching opaque tunnel is still settling; inspect and let that owner finish before a new conditional update. Existing work is not canceled.

Enabled origins refuse opaque CONNECT **even after the last repository is removed**. Removing an origin returns it to ordinary HTTP policy, not merely a TLS workaround. Routing neither creates grants nor installs client proxy/CA settings.

All updates/deletes/rotations use exact ETags. Omission performs one validated preflight; explicit `--etag` is never refreshed. Authority changes fence pending admissions and retain atomic administrative audit. On a conflict, read and review current state. On uncertainty, inspect before new intent; never replay automatically.

## Git-only upstream credentials

Create files require `name`, an exact HTTPS `origin`, safe `recipe:{header,prefix}` and write-only `secret`. This complete example is synthetic:

```json
{
  "name": "Team Git bearer",
  "origin": "https://github.com",
  "recipe": { "header": "Authorization", "prefix": "Bearer " },
  "secret": "synthetic-example-not-a-real-token"
}
```

Confirm that the real Git provider supports the chosen HTTPS authentication recipe; this example is not live GitHub interoperability qualification.

```bash
agent-gateway git credential create --file /private/credential.json --yes
agent-gateway git credential get CREDENTIAL_ID
agent-gateway git credential rotate CREDENTIAL_ID --file /private/rotation.json --yes
```

Metadata updates contain complete metadata without `secret`; rotation files contain only `secret`. Supply real material through owner-only private files or protected stdin, never argv/environment, host helper execution or credential-store scanning. Do not save plaintext secrets in source, ordinary backups or browser storage. Gateway does not remove your source files.

Selected unavailable material never falls back to public access or an old generation. Every retained repository reference, including deleted configuration, blocks credential deletion or incompatible origin/recipe changes. Format-4 restore recovers encrypted upstream material **as of backup time**, including subsequently rotated/deleted secrets, without proving upstream validity. Legacy recovery does not recover native secrets. Every restore invalidates administrator and agent access tokens; follow [recovery](backup-and-recovery.md#restore-a-backup).

## Client handoff and first read-only check

1. Complete [proxy authentication and client-scoped CA trust](http-proxy.md#client-authentication-and-trust). Securely transfer only the agent token and exported **public** CA. Never transfer upstream Git credentials, the Gateway root, master key or administrator bearer.
2. Reconcile existing `http.proxy`, URL-specific proxy/trust settings, credential helpers, `ALL_PROXY` and bypass variables privately. Do not print token-bearing Git configuration. Do not put a proxy URL containing the token into `git config`, argv or a profile. Never disable TLS verification.
3. Run from a trusted directory outside a repository, with a validated owner-private token file and public CA. The shell example below uses a same-host endpoint and a synthetic repository URL: substitute the configured repository, and use a separately trusted forwarding endpoint only when needed.

```bash
(
  set +x
  set -eu
  token=$(cat "$HOME/.config/agent-gateway/agent-token")
  # Files/parents must already have passed the ownership and symlink checks above.
  case "$token" in mgw_agent_*) ;; *) exit 1 ;; esac
  suffix=${token#mgw_agent_}
  case "$suffix" in ''|*[!A-Za-z0-9_-]*) exit 1 ;; esac
  [ "${#suffix}" -eq 43 ] || exit 1
  unset HTTP_PROXY HTTPS_PROXY ALL_PROXY all_proxy GIT_CONFIG_PARAMETERS GIT_CONFIG_COUNT
  unset GIT_TRACE GIT_TRACE_CURL GIT_CURL_VERBOSE GIT_SSL_NO_VERIFY
  export http_proxy="http://agent:${token}@127.0.0.1:8212"
  export https_proxy="$http_proxy" NO_PROXY= no_proxy=
  export GIT_SSL_CAINFO="$HOME/.config/agent-gateway/http-ca.pem"
  export GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null GIT_TERMINAL_PROMPT=0
  unset token suffix
  git -c credential.helper= -c http.version=HTTP/1.1 \
    ls-remote https://github.com/team/repository.git
)
```

This read-only discovery check neither clones nor pushes. It deliberately ignores system/global Git configuration for this invocation, disables helpers/prompts, and keeps the token only in the client environment; inherited child environments still expose it. Inspect other inherited Git overrides and tracing before use. A protected token file is a prerequisite, not something this snippet repairs. For distributions needing additional public roots, use the private combined CA bundle described in the proxy guide instead of weakening TLS.

Read the native Git exit/result and retained **Git → Traffic**. A successful ref listing establishes this discovery exchange only, not clone/fetch/push completeness, production interoperability or enforced egress isolation. Rotation requires replacing the private token file and restarting clients holding old environments.

## Git traffic history

![Git read-discovery detail for Demo Notes showing allowed admission, complete transport and HTTP success, not a push](../images/git-traffic.png)

_Synthetic example aligned with the disposable demo's read-only discovery: **Git → Traffic** distinguishes admission, transport and upstream report. This illustration does not qualify a real Git exchange, clone or push._

```bash
agent-gateway git traffic list --limit 50
agent-gateway git traffic get TRAFFIC_ID
```

**Git → Traffic** and `/api/v2/git/traffic` expose bounded newest-first admission and optional completion evidence. Use explicit `--cursor` continuation; a stale cursor requires a new read, not another Git operation. Recognized Git exchanges are not duplicated as ordinary HTTP traffic. Discovery and flush-only probes are distinct from pushes; missing rows never prove nonexecution.

**Admission**, **Transport**, and **Upstream report** answer different questions. Allowed does not mean dispatched; HTTP 200 and clean transfer do not mean push success. HTTP 401/403 are failures even with completed transport. A supported receive-pack report can claim success, failure or partial success, but Gateway does not independently verify repository effects. Missing, malformed, interrupted, oversized or unsupported reports remain unknown, including a partial report missing a requested command. An unexpectedly encoded push response is relayed but not interpreted; ordinary HTTP encoding is unchanged.

Evidence pins configured destination, revisions, bounded grant references and operation counts at admission. Push details retain at most eight targeted refs and 1,536 encoded bytes; omitted refs/outcomes are unavailable, and known per-ref results remain upstream claims. Later edits never rewrite history. OIDs, packs, headers, command fingerprints, arbitrary upstream messages and secrets are not persisted. Current control backups omit Git history; use [separate export](backup-and-recovery.md#export-optional-history).

For an uncertain push, inspect the native result and independently read the relevant authoritative remote refs before deciding on a new operation. Missing completion or a persistence failure never proves rollback or safe retry.

## Supported boundary

Receive-pack discovery and large-push flush probes require read plus a push-capable grant, but authorize no later push. Unsupported signed pushes, push options, SHA-256, HTTP-compressed push bodies, dumb HTTP, LFS and SSH fail closed. Host Git helpers and alternate transports are outside this proxy integration. Controlled fixtures do not qualify live GitHub, installed services, native custody or actual sandbox egress.

Use `agent-gateway git --help` for command syntax and the [Git public contract](../design/public-contract.md#git-configuration-resources) for exact shapes and bounds. Return to the [documentation map](../README.md).
