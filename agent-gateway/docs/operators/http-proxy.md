# Enable HTTP proxying and configure fresh clients

Audience: Gateway administrators and client operators

Purpose: Enable proxying and configure fresh clients without migrating Broker state.

The listener is enabled by default for bare `serve` and new managed installs.
Client proxy use remains cooperative, not network-enforced egress containment.
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
agent-gateway serve
```

Supply the same explicit `--data-dir` to each command for a custom installation.
Export is public metadata only: it proves neither signing readiness nor client trust.
Inspect an export failure rather than trusting an incomplete output file. Init preserves
an existing CA; replacement is a distinct deliberate stopped operation. Public output defaults to `<data-dir>/http-ca.pem`; `--stdout` explicitly streams PEM.

Administration/MCP stays at `127.0.0.1:8210`; HTTP defaults separately to
`127.0.0.1:8212`. Use `--http-proxy-listen 127.0.0.1:8213` for a custom address. Both listeners accept only canonical numeric IPv4 loopback addresses.
The proxy has no administrative routes. Gateway-owned destinations, including
its temporary OAuth callbacks, are forbidden even with private-network permission.
A trusted VM forwarding path must be supplied separately; do not expose either
listener to an untrusted network. Plain proxy authentication is not encrypted on
the client-to-proxy hop.

Use `serve --clear-http-proxy-listen` for MCP-only startup independent of CA
availability. Opt-out cannot accompany an explicit proxy address; an empty address
is rejected. Default and custom enabled selections require usable signing material
and both listener binds; any failure prevents startup acknowledgement and cleans
up the partial start. Occupied ports do not select another port or silently disable
HTTP. No CA is generated during serve and no TLS verification is bypassed.

New macOS `service install` persists the default address; use
`service install --clear-http-proxy-listen` to opt out. Omitted updates and restart
preserve existing settings, including legacy missing flags meaning disabled.
`service update --http-proxy-listen 127.0.0.1:8212` deliberately enables it;
`service update --clear-http-proxy-listen` disables it. Canonical launchd context
preserves legacy omission without rewriting old plists; it is not authentication.
See [launchd management](launchd.md) and [upgrade guidance](upgrade-compatibility.md#http-listener-default-cutover);
do not run these mutations as a smoke test against a live installation.

`doctor --online` and **System → Status** report enablement, selected address, loaded CA,
proxy readiness, active request/stream and tunnel counts, connection/work occupancy,
and the shared traffic pressure, quota and fault facts. Loaded CA means current
process signing capability, not native-keyring health or installed client trust.
A traffic persistence fault blocks new MCP dispatch and HTTP forwarding while
healthy control administration remains available. Shutdown fences admissions and
settles connection/completion owners before closing CA material and shared storage.
Missing completion remains unknown; shutdown and restart never replay traffic.

## Interpret CONNECT evidence

**Interception selected** means Gateway selected local interception, not denial or
successful connection establishment. Inner requests are authorized separately.
Selection proves neither CONNECT acceptance, TLS establishment, upstream dispatch,
request completion nor connection closure. The retained `allowed` bit describes
upstream-dispatch permission; `false` alone does not mean CONNECT failed.
**CONNECT denied** is a policy rejection. **Opaque tunnel allowed** permits opaque
forwarding, not inspection of inner requests; missing completion remains unknown.

Use the Interception selected decision/outcome filters to distinguish it from
Not dispatched denials. Detail embeds **Requests on this connection**, selected
only by recorded CONNECT ID, with manual refresh and Load more. Inner detail links back to that recorded parent. The normal traffic filter bar has no CONNECT-ID input; existing bookmarked filters remain visible and removable.
The CLI equivalent is `agent-gateway http traffic list --connect-id ID`.
Retention may remove either side, and older records lack correlation; an empty
list or unavailable parent never proves no execution or safe replay. Historical
interception decisions receive the same label without inventing lifecycle events.
Upgrade the bundled CLI/browser with the service for the new
`interception_selected` summary outcome; stored admission/completion is unchanged.

## Inspect rejected requests

Open **HTTP → Traffic** to inspect retained request evidence. **Reason** explains
Gateway pre-dispatch rejection; detail includes the stable stage/reason codes.
Header failures distinguish invalid/oversized headers, unsupported trailers or
upgrades, and proxy credentials sent inside CONNECT. Request-form failures
identify nested/body-bearing CONNECT, missing origin form inside CONNECT, or
missing absolute HTTP form outside it. Target failures identify request versus
CONNECT validation. New request diagnostics distinguish `invalid_target_syntax`
(including malformed escapes), `target_too_long`, `forbidden_path` and
`authority_mismatch`; historical generic reasons remain readable. Consult the
compatibility table below rather than assuming an upstream service rejected it.

**Response source** separates Gateway-generated responses from upstream responses.
Rejection admission records Gateway validation, not the final live status or
confirmed delivery to the client. A persistence failure can require a different
Gateway error. Completion `status` is an upstream status; `gateway_status` records
a separately generated Gateway error. Neither a response source nor a status
makes an unknown outcome safe to retry.

**CONNECT context** is the actual enclosing connection's admission ID and inherited
canonical destination, shared only by that connection's H1 requests/H2 streams.
It is not validation of the inner target. Retention can remove the parent record;
missing historical context or rejection details remain unavailable, with no
backfill or time-based matching. No raw errors, authorization headers, bodies,
queries, URLs or path fragments are captured to explain a rejection. Parser-level
framing errors before authentication have no authenticated traffic record.

## Request-target compatibility

Parsing support does not grant forwarding permission. Accepted targets still need
HTTP authorization, safe resolved addresses and (for HTTPS) verified upstream TLS.
The [canonical selector contract](../design/identity-and-authorization.md#canonical-selectors-and-forwarding)
owns normalization and policy semantics.

| Request form                                                                         | Status                | Behavior or limit                                                                                                        |
| ------------------------------------------------------------------------------------ | --------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| Absolute-form `http://host/path`                                                     | Supported             | URL target authority wins over conflicting raw Host; forwarded Host comes from the validated target                      |
| CONNECT `host:443` or `[IPv6]:443`                                                   | Supported             | Explicit canonical port; CONNECT target authority wins over conflicting raw Host; interception is not request permission |
| Origin-form `/path` inside intercepted H1/H2                                         | Supported             | HTTPS authority comes only from CONNECT; Host and supplied SNI must agree                                                |
| Absolute HTTPS on the plain proxy listener; origin-form outside CONNECT; `OPTIONS *` | Unsupported           | No inferred authority or alternate ingress form                                                                          |
| `/word-wrap/-/word-wrap-1.2.5.tgz` and `/@anthropic-ai/sdk/-/sdk-0.124.0.tgz`        | Supported             | Literal `@` is path data, not userinfo                                                                                   |
| `/@scope%2fpkg` scoped npm metadata; escaped `%40`                                   | Supported             | Escaped spelling is preserved; escaped slash is not a policy segment boundary                                            |
| Unreserved escapes such as `/%61pi/~user`                                            | Supported             | Compare as `/api/~user`, but preserve `/%61pi/~user` on the wire                                                         |
| Reserved path characters such as `:`, `;`, `+`, and their escapes                    | Supported             | Literal and escaped reserved bytes remain distinct in matching; escape hex-case differs only on the wire                 |
| `/https://example.com` and `//example.com/x`                                         | Supported             | Remain path data under the original destination, never another authority                                                 |
| Percent-encoded Unicode; valid Unicode URL input                                     | Supported             | Input text becomes UTF-8 URI escapes without normalization; literal non-ASCII wire syntax is not supported               |
| Unicode/IDNA DNS hosts, mixed-case DNS, canonical IPv4/IPv6                          | Supported             | Lowercase IDNA A-labels and normalized IPs; mapped IPv6 becomes IPv4                                                     |
| Trailing-dot hosts, IPv6 zones, alternate numeric IPs, userinfo                      | Deliberately rejected | Avoid authority and resolver interpretation differences                                                                  |
| Omitted HTTP/HTTPS port; explicit decimal 1–65535                                    | Supported             | Effective 80/443 when omitted; leading zeros, zero and empty ports reject                                                |
| Empty URL path; query including duplicate keys, escaped reserved data and empty `?`  | Supported             | Path becomes `/`; query stays opaque and is forwarded unchanged, never matched by policy                                 |
| Repeated slashes, encoded separators and double-encoded data                         | Supported             | No cleaning or recursive decoding; `/a/b`, `/a%2Fb` and `/a%252Fb` are distinct                                          |
| Literal/unreserved-decoded dot segments, backslashes, encoded path controls          | Deliberately rejected | Path exclusions do not apply to opaque encoded query data                                                                |
| Fragments, malformed escaping, intercepted Host/H2 authority/SNI disagreement        | Deliberately rejected | Fail before upstream dispatch                                                                                            |
| WebSockets/upgrades and HTTP/3                                                       | Unsupported           | No automatic opaque-tunnel or TLS-error fallback                                                                         |

Targets are bounded to 8,192 bytes both before conversion and as serialized absolute
URIs (including explicit effective port and query). The 4,096-byte path bound
applies after Unicode escaping and to the no-larger comparison path. Intercepted
origin-form includes the bound CONNECT HTTPS authority in the target limit.
These are byte limits, not character counts.

Outer absolute-form HTTP follows RFC 9112 section 3.2.2: ignore conflicting raw
Host and derive authority from the absolute URL. Outer CONNECT explicitly uses
its request-target authority even if raw Host conflicts. Neither raw value can
change routing, policy, private-network permission, credential choice or forwarded
Host. This does not relax intercepted HTTP/1 Host, HTTP/2 authority or TLS SNI
agreement with the CONNECT destination. Malformed headers/framing still reject;
CONNECT acceptance alone proves neither upstream dispatch nor injection.

The accepted profile uses RFC 3986 path/query syntax with the exclusions above.
Queries retain duplicates/order, `+`, escape casing and empty `?` versus no query.
Unchanged default-allow, any-path, root/ancestor-prefix and credential-bearing
grants now reach newly accepted targets and can inject credentials there. Review
existing policy and credential scopes before live adoption; no migration or
automatic grant edit occurs. V1 exact selectors retain their narrower grammar;
not every accepted request can be expressed as an exact selector. Gateway does
not infer arbitrary upstream decoding or slash merging. Especially with default
allow and narrow blocks, review upstream routing rather than assume all equivalent
upstream resources are blocked.

A supported syntax row is not a live-client qualification: controlled-upstream tests
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
