# Enable HTTP proxying and configure fresh clients

Audience: Gateway administrators and client operators

Purpose: Enable proxying and configure fresh clients without migrating Broker state.

The proxy is opt-in and cooperative, not network-enforced egress containment.
MCP permissions never authorize HTTP. New agents, and agents backfilled
when HTTP defaults were introduced, start at block. Existing agents retain
their stored `http_default` (`allow` or `block`); configure separate
[HTTP defaults and grants](access-control.md#http-grants-and-test-access).
No HTTP self-service, automatic access request, retry or replay is provided.
Capacity remains unqualified; deterministic tests are not a throughput guarantee.

## Host setup

Retain the existing initialized installation and singular agent credential.
Stop the selected Gateway before completing missing CA setup with `init` or explicitly replacing its CA using
[stopped CA commands](backup-and-recovery.md#stopped-interception-ca-commands).
Installation-ID assertions are optional for these operations; never export a private key.
For missing setup:

```sh
agent-gateway init --confirm
mkdir -p "$HOME/.config/agent-gateway"
chmod 700 "$HOME/.config/agent-gateway"
umask 077
agent-gateway http ca export --output "$HOME/.config/agent-gateway/http-ca.pem"
agent-gateway serve --http-proxy-listen 127.0.0.1:8212
```

Supply the same explicit `--data-dir` to each command for a custom installation.
Export is public metadata only: it proves neither signing readiness nor client trust.
Inspect an export failure rather than trusting an incomplete output file. Init preserves
an existing CA; replacement is a distinct deliberate stopped operation. Public output defaults to `<data-dir>/http-ca.pem`; `--stdout` explicitly streams PEM.

Administration/MCP stays at `127.0.0.1:8210`; `8212` is the recommended separate
proxy port. Both listeners accept only canonical numeric IPv4 loopback addresses.
The proxy has no administrative routes. Gateway-owned destinations, including
its temporary OAuth callbacks, are forbidden even with private-network permission.
A trusted VM forwarding path must be supplied separately; do not expose either
listener to an untrusted network. Plain proxy authentication is not encrypted on
the client-to-proxy hop.

Omitting `--http-proxy-listen` keeps MCP-only startup independent of CA availability.
Explicit selection requires usable signing material and both listener binds;
any failure prevents successful startup acknowledgement and cleans up the partial
start. No CA is generated implicitly. For installed macOS management, persist
`service install --http-proxy-listen 127.0.0.1:8212` or
`service update --http-proxy-listen 127.0.0.1:8212`. Omitted updates preserve the
selection; `service update --clear-http-proxy-listen` disables it. Restart preserves
installed values. See [launchd management](launchd.md); do not run these mutations
as a smoke test against a live installation.

`doctor --online` and **System → Status** report enablement, selected address, loaded CA,
proxy readiness, active request/stream and tunnel counts, connection/work occupancy,
and the shared traffic pressure, quota and fault facts. Loaded CA means current
process signing capability, not native-keyring health or installed client trust.
A traffic persistence fault blocks new MCP dispatch and HTTP forwarding while
healthy control administration remains available. Shutdown fences admissions and
settles connection/completion owners before closing CA material and shared storage.
Missing completion remains unknown; shutdown and restart never replay traffic.

## Request-target compatibility

Parsing support does not grant forwarding permission. Accepted targets still need
HTTP authorization, safe resolved addresses and (for HTTPS) verified upstream TLS.
The [canonical selector contract](../design/identity-and-authorization.md#canonical-selectors-and-forwarding)
owns normalization and policy semantics.

| Request form                                                                               | Status                | Behavior or limit                                                                                                              |
| ------------------------------------------------------------------------------------------ | --------------------- | ------------------------------------------------------------------------------------------------------------------------------ |
| Absolute-form `http://host/path`                                                           | Supported             | Mandatory Host must agree, including the effective port                                                                        |
| CONNECT `host:443` or `[IPv6]:443`                                                         | Supported             | Explicit canonical port and matching Host; interception is not request permission                                              |
| Origin-form `/path` inside intercepted H1/H2                                               | Supported             | HTTPS authority comes only from CONNECT; Host and supplied SNI must agree                                                      |
| Absolute HTTPS on the plain proxy listener; origin-form outside CONNECT; `OPTIONS *`       | Unsupported           | No inferred authority or alternate ingress form                                                                                |
| `/word-wrap/-/word-wrap-1.2.5.tgz` and `/@anthropic-ai/sdk/-/sdk-0.124.0.tgz`              | Supported             | Literal `@` is path data, not userinfo                                                                                         |
| `/@scope%2fpkg` scoped npm metadata; escaped `%40`                                         | Deliberately rejected | Reserved escaping can change upstream resource/segment interpretation; tarball support does not qualify a complete npm install |
| Unreserved escapes such as `/%61pi/~user`                                                  | Supported             | Normalize once to `/api/~user`; grants and forwarding use the same path                                                        |
| Other reserved path characters, such as `:`, `;`, `+`, and their escapes                   | Unsupported           | Conservative path subset; no general URI-path compatibility claim                                                              |
| Unicode path bytes, literal or escaped                                                     | Unsupported           | ASCII path subset; Unicode host support is separate                                                                            |
| Unicode/IDNA DNS hosts, mixed-case DNS, canonical IPv4/IPv6                                | Supported             | Lowercase IDNA A-labels and normalized IPs; mapped IPv6 becomes IPv4                                                           |
| Trailing-dot hosts, IPv6 zones, alternate numeric IPs, userinfo                            | Deliberately rejected | Avoid authority and resolver interpretation differences                                                                        |
| Omitted HTTP/HTTPS port; explicit decimal 1–65535                                          | Supported             | Effective 80/443 when omitted; leading zeros, zero and empty ports reject                                                      |
| Empty URL path; query including duplicate keys, escaped reserved data and empty `?`        | Supported             | Path becomes `/`; query stays opaque and is forwarded unchanged, never matched by policy                                       |
| Dot segments, repeated slashes, encoded separators, percent/double escaping, path controls | Deliberately rejected | No traversal, second decoding or alternate segment interpretation                                                              |
| Fragments, malformed escaping, Host/CONNECT/SNI disagreement                               | Deliberately rejected | Fail before upstream dispatch                                                                                                  |
| WebSockets/upgrades and HTTP/3                                                             | Unsupported           | No automatic opaque-tunnel or TLS-error fallback                                                                               |

Targets are bounded to 8,192 bytes and paths to 4,096 bytes. A supported syntax
row is not a live-client qualification: deterministic controlled-upstream tests
do not prove an installed Gateway or an entire package-manager workflow. Do not
weaken grants, disable TLS verification or switch to a broad tunnel as an automatic
workaround for a rejected target.

## Client authentication and trust

Use the existing `mgw_agent_` credential, never an administrator bearer. Standard
proxy clients use Basic authentication with username `agent` and the agent token
as password. Explicit-header clients may instead use `Proxy-Authorization: Bearer`.
Gateway strips proxy authorization before forwarding. Rotation/revocation or
agent disablement affects subsequent admissions in both protocols, not already
admitted work; each intercepted request/stream revalidates the original credential.
Opaque tunnels expire one hour after admission, including after revocation.

Repository-owned guest provisioning is retired. Configure HTTP clients manually,
independently of [MCP client configuration](access-control.md#configure-an-agent-client-manually).
Before transferring, prepare the client's owned nonsymlink
`~/.config/agent-gateway` directory with mode `0700`; `.config` must be owned and
not group/world-writable. Through an authenticated, confidential channel, copy only
`agent-token` and the exported public `http-ca.pem` into that directory, with owned
nonsymlink files at mode `0600` (or `0400`). Never transfer the Gateway data root,
administrator credentials or CA private key. Validate the current `mgw_agent_`
credential and public certificate before use; no retired script enforces these checks.

Configure the client's supported uppercase/lowercase `HTTP_PROXY` and
`HTTPS_PROXY` selectors using `http://agent:<runtime token>@host.lima.internal:8212`
when a trusted Lima forwarding path is deliberately selected. Other environments
must use their explicitly selected trusted proxy endpoint. Read the current token
from the private file at client launch rather than baking it into a shell profile.
Only these client environment exports contain the token; never copy them into
configuration, argv, logs, screenshots or tickets. Shell tracing is disabled before
reading credentials and must stay disabled. Environment inheritance exposes the
credential to child processes; this is not an OS security boundary.

For clients that replace rather than extend their trust store, prepare an
owner-private client CA bundle containing distribution roots plus the validated
exported public CA, then select it with `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, and
`SSL_CERT_FILE` as supported. `NODE_EXTRA_CA_CERTS` selects the public CA separately.
Keep trust client-scoped: this procedure does not authorize modifying `/etc`,
installing system trust, or exporting protected keys. Clients must actually
support these selectors; Java, browser stores and other runtimes require explicit
client-specific trust setup. Certificate-pinned clients cannot use interception:
a narrowly scoped **Allow tunnel** grant is the explicit escape, bypassing inner
request restrictions and credential injection. There is no TLS-failure fallback.

Explicitly reconcile inherited proxy variables, including `ALL_PROXY`/`all_proxy`,
and set `NO_PROXY`/`no_proxy` empty unless a bypass is deliberately required. Any bypass skips all Gateway policy,
injection and evidence; clients may also implicitly bypass loopback regardless
of these variables. Validate each client's behavior with a disposable upstream,
not a production side effect. Do not broadly exempt private networks or wildcard
hosts to make requests succeed. Proxy environment variables do not enforce egress.

Token rotation requires securely refreshing the private client file and restarting
client processes; existing environments retain old bytes. CA replacement, key loss
and **every maintenance restore-backup** require explicit new CA replacement, public export and
manual client trust refresh. Ordinary restarts retain the selected CA; missing
signing material never regenerates or revives an old backup handle. Rebuild the
client bundle when the public CA changes and retire old client trust explicitly;
no automatic trust removal is promised.

This is fresh setup, not HTTP Broker adoption. Operators must inspect and reconcile
conflicting proxy exports, historical Broker/Gateway managed blocks and malformed
or duplicate markers before changing a profile. The retired scripts no longer
refuse conflicts or rewrite `.bashrc`; do not leave an old block overriding the
selected endpoint, token or trust. No Broker importer, CA adoption, aliases,
automatic shutdown, traffic import or retirement is included. Source tests do not
qualify live client adoption.
