# Enable HTTP proxying and configure fresh clients

Audience: Gateway administrators and client operators

Purpose: Enable proxying and configure fresh clients without migrating Broker state.

The proxy is opt-in and cooperative, not network-enforced egress containment.
MCP permissions never authorize HTTP. Existing and new principals default to
HTTP block; configure separate [HTTP grants](access-control.md#http-grants-and-test-access).
No HTTP self-service, automatic access request, retry or replay is provided.
Capacity remains unqualified; deterministic tests are not a throughput guarantee.

## Host setup

Retain the existing initialized installation and singular principal credential.
Stop the selected Gateway before explicitly creating its interception CA using
[stopped CA commands](backup-and-recovery.md#stopped-interception-ca-commands).
These commands require the exact installation ID; never export a private key.
For a new CA only:

```sh
agent-gateway http ca create --installation-id INSTALLATION_ID --confirm
mkdir -p "$HOME/.config/agent-gateway"
chmod 700 "$HOME/.config/agent-gateway"
umask 077
agent-gateway http ca export --installation-id INSTALLATION_ID > "$HOME/.config/agent-gateway/http-ca.pem"
agent-gateway serve --http-proxy-listen 127.0.0.1:8212
```

Supply the same explicit `--data-dir` to each command for a custom installation.
Export is public metadata only: it proves neither signing readiness nor client trust.
Inspect an export failure rather than trusting an empty output file. Create refuses
an existing CA; replacement is a distinct deliberate stopped operation.

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

`status` and **System → Status** report enablement, selected address, loaded CA,
proxy readiness, active request/stream and tunnel counts, connection/work occupancy,
and the shared traffic pressure, quota and fault facts. Loaded CA means current
process signing capability, not native-keyring health or installed client trust.
A traffic persistence fault blocks new MCP dispatch and HTTP forwarding while
healthy control administration remains available. Shutdown fences admissions and
settles connection/completion owners before closing CA material and shared storage.
Missing completion remains unknown; shutdown and restart never replay traffic.

## Client authentication and trust

Use the existing `mgw_agent_` credential, never an administrator bearer. Standard
proxy clients use Basic authentication with username `agent` and the agent token
as password. Explicit-header clients may instead use `Proxy-Authorization: Bearer`.
Gateway strips proxy authorization before forwarding. Rotation/revocation or
principal disablement affects subsequent admissions in both protocols, not already
admitted work; each intercepted request/stream revalidates the original credential.
Opaque tunnels expire one hour after admission, including after revocation.

For a fresh Linux sandbox use the explicit
[HTTP provisioning script](../../examples/provision/configure-agent-gateway-http-proxy.sh).
It is separate from MCP client provisioning and makes no host/service or system
trust changes. Before transferring, prepare the guest's owned nonsymlink
`~/.config/agent-gateway` directory with mode `0700`; `.config` must be owned and
not group/world-writable. Copy only the private `0600` (or `0400`) agent-token file
and exported public CA, never the Gateway data root or administrator credentials:

```json
{
  "copy_paths": [
    "~/.config/agent-gateway/agent-token",
    "~/.config/agent-gateway/http-ca.pem"
  ],
  "scripts": [
    "/path/to/agent-tools/agent-gateway/examples/provision/configure-agent-gateway-http-proxy.sh"
  ]
}
```

The guest requires Bash, coreutils, OpenSSL and the distribution public CA bundle
(`/etc/ssl/certs/ca-certificates.crt`). The script fails explicitly if prerequisites
are absent; install them through the separately authorized sandbox provisioning
owner. Run `sb provision`, open a fresh shell and restart clients.

The convergent managed block reads the current token at **every shell startup**,
not during file generation. It exports uppercase/lowercase `HTTP_PROXY` and
`HTTPS_PROXY` using `http://agent:<runtime token>@host.lima.internal:8212`.
Only these client environment exports contain the token; never copy them into
configuration, argv, logs, screenshots or tickets. Shell tracing is disabled before
reading credentials and must stay disabled. Environment inheritance exposes the
credential to child processes; this is not an OS security boundary.

The script creates an owner-private client CA bundle from distribution roots plus
the exported public CA, then sets `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, and
`SSL_CERT_FILE`. `NODE_EXTRA_CA_CERTS` selects the public CA separately. It does not
modify `/etc`, install system trust, or export protected keys. Clients must actually
support these selectors; Java, browser stores and other runtimes require explicit
client-specific trust setup. Certificate-pinned clients cannot use interception:
a narrowly scoped **Allow tunnel** grant is the explicit escape, bypassing inner
request restrictions and credential injection. There is no TLS-failure fallback.

`NO_PROXY`/`no_proxy` are empty by default. Any bypass skips all Gateway policy,
injection and evidence; clients may also implicitly bypass loopback regardless
of these variables. Validate each client's behavior with a disposable upstream,
not a production side effect. Do not broadly exempt private networks or wildcard
hosts to make requests succeed. Proxy environment variables do not enforce egress.

Token rotation requires refreshing `copy_paths` and restarting client processes;
existing environments retain old bytes. CA replacement, key loss and **every backup
restore** require explicit new CA replacement, export and client trust reprovisioning.
Ordinary restarts retain the selected CA; missing signing material never regenerates
or revives an old backup handle. A new public CA requires rerunning provisioning to
rebuild the client bundle. Retire old client trust explicitly; no automatic trust
removal is promised.

This is fresh setup, not HTTP Broker adoption. Conflicting proxy exports, Broker
managed blocks and malformed duplicate markers refuse before rewriting `.bashrc`.
No Broker importer, CA adoption, aliases, automatic shutdown, traffic import or
retirement is included.
