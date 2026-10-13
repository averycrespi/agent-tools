# Run Gateway in Docker

Audience: Operators creating a fresh Gateway installation on a Docker host

Purpose: Build a minimal image and run one persistent installation behind an existing HTTPS reverse proxy.

This is a **fresh-install** procedure for an operator-managed Debian Docker host with Docker Engine and the Compose plugin already installed. Network reachability, private access controls and the HTTPS reverse proxy already exist; this guide does not configure Tailscale or expose a service for you. One installation has one running owner, never multiple replicas.

The repository ships a [Dockerfile](../../Dockerfile), [context allowlist](../../.dockerignore) and [standalone Compose example](../../examples/docker/compose.yaml). It does not publish a registry image or bundle MCP servers, a reverse proxy, Node, a compiler or a Docker socket. The runtime is Alpine Linux, independent of the Debian host. CI's blocking **Gateway Docker smoke** builds and runs `linux/amd64`; other architectures, including arm64, are not yet qualified. Do not infer multi-architecture support from base-image availability.

## Build and configure

From a clean, reviewed checkout at the desired revision, on your Docker-capable build host:

```bash
docker build --platform linux/amd64 -t agent-gateway:reviewed ./agent-gateway
```

The staged build uses the module's exact Go version, checked-in `go.mod`/`go.sum`, read-only module resolution and the checked-in generated frontend embedded in the binary. No Node build runs in the image. The runtime copies standard outbound CA roots from the builder. Both base images are pinned by registry digest. Retain the built image ID/digest and source revision with deployment records. Update base digests deliberately for base/CA security fixes, rebuild and requalify rather than silently following mutable tags. Image registry publication is a separate operator decision, not part of this procedure.

Copy the Compose example into an operator-owned directory **outside the checkout/build context**. Set these nonsecret variables in that directory's `.env`, replacing the examples:

```dotenv
GATEWAY_IMAGE=agent-gateway:reviewed
GATEWAY_EXTERNAL_ORIGIN=https://gateway.example
GATEWAY_VOLUME=gateway-installation
GATEWAY_PROXY_NETWORK=existing-proxy-network
GATEWAY_PORT=8210
GATEWAY_PROXY_PORT=8212
GATEWAY_PROXY_BIND=127.0.0.1
GATEWAY_PROXY_HOST_PORT=8212
```

The external origin is the canonical HTTPS browser/MCP origin, with any nondefault port and no trailing slash. The Gateway backend listens on `0.0.0.0:GATEWAY_PORT` **inside** the container; it is not host-published. Point the existing reverse proxy to `gateway:GATEWAY_PORT` on the selected external Docker network (replace the variable with its value). Avoid service-name collisions on a shared network. All peers with access to this network are part of your trusted backend path.

The separate `GATEWAY_PROXY_PORT` carries HTTP/Git forwarding, not browser/MCP traffic. Its host mapping defaults to loopback. Change `GATEWAY_PROXY_BIND` to the host's already-controlled private IPv4 address and select the host port deliberately when remote agents need it. Direct HTTP forward-proxy transport is unencrypted on the **outer client-to-proxy hop**; your existing private network must protect credentials and traffic on that hop. The interception CA does not encrypt that outer connection. Do not put the forward proxy behind a website path or expose it publicly.

The example's `--allowed-host` entries are commented out. Add each needed internal alias explicitly as two command-list entries; aliases allow authenticated direct MCP/API access but do not grant browser Origin trust. Configure hostname/external origin through `serve` flags, never forwarding headers. Custom backend ports also change the Compose health probe and your local CLI `--address`.

## Initialize once, then serve

The image uses UID/GID **10001:10001** for both init and serve. The external volume declaration makes a missing/typoed volume fail instead of silently creating another installation. For this **new** installation only, create its named volume explicitly:

```bash
docker volume create gateway-installation
docker compose config --quiet
docker compose run --rm --no-deps gateway init --confirm
docker compose up -d --wait --wait-timeout 90
```

Use your configured volume name in the first command. A new empty named volume receives the image directory's UID/GID and `0700` mode through Docker's normal first-mount copy-up. Do not use `volume-nocopy`. Init writes protected files with `0600` custody. Init output reports paths and metadata, not a bearer to copy from logs. The complete installation lives at `/var/lib/agent-gateway`: identity/control and traffic databases, `master-key`, protected `admin-bearer`, encrypted upstream/CA material, public `http-ca.pem`, backups, sidecars and recovery/intent markers all remain together. Persist the **whole directory**, not selected SQLite or credential files. SQLite sidecars may have the narrowly allowed modes described in [installation safety](installation-safety.md#sqlite-sidecar-permissions).

For a bind mount instead, prepare a new, empty host directory with mapped UID/GID 10001 and mode `0700` before init, use an absolute bind source and disable automatic host-path creation. Rootless/user-namespace Docker maps these IDs differently; establish the mapping before use. Never run a root entrypoint that recursively chowns or chmods existing state. Wrong ownership, unsafe modes, missing custody or the wrong/uninitialized volume must fail; inspect the selected mount and diagnostic, not another init, repair or rotation. An empty mount is **not** an existing installation. A nonempty installation must never be replaced with a fresh one to bypass refusal.

Ordinary startup is only `serve`: no automatic initialization, repair, key/CA rotation or credential reset. The JSON exec entrypoint delivers SIGTERM directly to foreground Gateway. Compose gives it a **30-second** stop grace; readiness is `/readyz`, not mere process existence. Health failures do not authorize automatic recovery. The example intentionally has no restart policy: diagnose failed startup rather than loop it.

The root filesystem is read-only, capabilities are dropped, and a bounded writable `/tmp` supports child runtimes. Derived servers may need additional deliberate mounts/resources; do not weaken installation custody to satisfy them.

## Administer and hand off secrets safely

Use the same image, volume and UID. `docker compose exec` is the service-name form of `docker exec`:

```bash
docker compose exec -T gateway agent-gateway doctor --online \
  --address http://127.0.0.1:8210
docker compose exec -T gateway agent-gateway mcp server list \
  --address http://127.0.0.1:8210
```

Change the loopback port for a custom `GATEWAY_PORT`. The CLI reads the selected installation's protected administrator bearer directly. Remote HTTPS CLI is not supported by this packaging; use the local CLI through `docker exec`, or the HTTPS browser. Docker access is effectively host-level authority: restrict it to administrators, never agents.

For browser sign-in, deliberately retrieve the initial bearer into a **fresh owner-only file**, on a private, unrecorded administrator terminal on the Docker host:

```bash
(umask 077; set -C
 docker compose exec -T gateway cat /var/lib/agent-gateway/admin-bearer > ./initial-admin-bearer)
```

Check command success before using the file; a failed transfer can leave an empty/partial sink. Transfer through your established secure channel and paste only into the canonical HTTPS sign-in page. Do not echo it to terminal history, CI output, chat, argv, `.env`, Compose secrets/environment or image layers. Do not share it with agents. Protect/retire the handoff copy and clear clipboard history after use. [Administrator authentication](administration.md#administrator-authentication) owns rotation, separate administrator credentials and fresh one-time sinks; uncertain mutation outcomes require inspection, not replay.

Create agents and least-privilege protocol grants through the browser or [access-control CLI](access-control.md). Issue each agent credential to a fresh protected `--secret-output` path inside the private volume, then transfer it through a private file channel as above. Configure MCP at `https://gateway.example/mcp` using the client's supported token mechanism. Configure the **separate** HTTP/Git proxy endpoint and explicit protocol permissions using [HTTP setup](http-proxy.md) and [Git setup](git.md). No secret belongs in server arguments or Docker environment variables.

Distribute only `/var/lib/agent-gateway/http-ca.pem` to clients that need interception, verifying its fingerprint through your trusted channel. It is a public certificate; never export signing material or `master-key`. The Gateway interception CA is **not** the HTTPS reverse proxy's certificate: Caddy/another proxy owns browser-facing TLS, while Gateway's CA signs intercepted upstream TLS for explicitly trusting agent clients. Standard outbound CA roots in the image validate upstream HTTPS; they do not install Gateway trust into clients.

## Connect upstreams and extend the image

Use [upstream server configuration](upstream-servers.md) for stdio, Streamable HTTP, static credentials and OAuth. The browser/API stores upstream secrets under encrypted custody; never bake them into a Dockerfile or configuration document. Public remote upstreams use HTTPS. For an unauthenticated trusted private-network HTTP MCP service, configure its service-name URL (for example `http://notes-mcp:8080/mcp`) and explicitly set `allow_private_http: true` on that server. Gateway must share a suitable network with it. Default denial, private-address validation and credential/OAuth restrictions still apply; this is not an unrestricted HTTP or SSRF bypass.

Remote OAuth uses the exact `${GATEWAY_EXTERNAL_ORIGIN}/oauth/callback` URL (substitute the configured origin). Register those exact bytes with the provider, including a nondefault port. Route the callback through the same HTTPS proxy/backend, with no additional host-published callback port. Provider registration/authentication support still varies. No source/container smoke establishes live provider compatibility.

For stdio, derive a separate image containing only your approved executable and dependencies. For example, with an already-built compatible nonsecret executable in a separate build context:

```dockerfile
FROM agent-gateway:reviewed
USER root
COPY --chmod=0755 notes-mcp /opt/notes/bin/notes-mcp
RUN mkdir -p /opt/notes/work && chown 10001:10001 /opt/notes/work
USER 10001:10001
```

Install a pinned runtime in that derived image if needed, then return to the same non-root UID. Select the derived tag with `GATEWAY_IMAGE`. Configure the absolute executable `/opt/notes/bin/notes-mcp`, literal arguments and absolute working directory `/opt/notes/work`. The example read-only root means writable work needs a separately provisioned mount. Stdio receives a clean declared environment: nonsecret values go in `environment`; `secret_environment` maps names to separately provisioned encrypted credential slots, not Docker environment secrets. The Gateway runtime supervises children and owns bounded process-group cleanup. Do not mount `/var/run/docker.sock` or bundle an unrelated MCP catalog.

## Existing HTTPS proxy contract

Follow the complete [generic reverse-proxy contract](remote-serving.md#generic-proxy-contract): preserve public Host/Origin, authorization, cookies, CSRF and MCP headers; strip `Forwarded` and every `X-Forwarded-*`; disable caching, response buffering and mutation retries; permit long-lived streams and propagate cancellation. No proxy vendor is required by the image or Compose service.

A small **optional Caddy** example for an existing Caddy installation is below. Set `GATEWAY_HOST` and `GATEWAY_BACKEND` in that proxy's own nonsecret configuration (for example `gateway.example` and `gateway:8210`), not in Gateway:

```caddyfile
{$GATEWAY_HOST} {
    reverse_proxy {$GATEWAY_BACKEND} {
        header_up Host {http.request.host}
        header_up -Forwarded
        header_up -X-Forwarded-*
        flush_interval -1
        lb_try_duration 0
    }
}
```

Do not enable access logging for `/oauth/callback`: query strings contain one-time authorization material. Check any inherited proxy/global logging, caching, timeout and retry policies too. This snippet is not a Caddy app, container dependency, installed-proxy qualification or permission to change a live proxy.

## Backups, upgrades and stopped maintenance

Create and inspect control backups while running, for example `docker compose exec -T gateway agent-gateway backup create` (add the custom `--address` when needed). Backups omit traffic history and the master key. Safeguard a separate owner-only copy of the matching `master-key`, retain historical keys required by older backups, and follow [backup and recovery](backup-and-recovery.md). Key plus database theft defeats this file-custody protection. Full-volume preservation must retain ownership, permissions, sidecars, identities and markers; never make a second runnable clone or manually roll back a database to defeat encryption high-water accounting.

For an authorized upgrade: retain verified backups/key custody, review [compatibility](upgrade-compatibility.md), build the new reviewed image, stop the old owner cleanly, change only the image tag, then start against the **same complete volume**, flags and UID. Inspect health and authenticated status before resuming agents. Do not run init again. Do not downgrade blindly after a schema change; retained images are not proof of backward compatibility. `docker compose down` without `--volumes` does not remove this external volume; never delete it as upgrade cleanup.

Stopped maintenance uses the same service definition without starting a second server:

```bash
docker compose stop --timeout 30 gateway
# Verify the old container is stopped with exit 0; a timeout/forced kill is not a clean stop.
docker compose ps -a
# Inspect only; execute the corresponding --confirm after diagnosing and approving the plan.
docker compose run --rm --no-deps gateway maintenance verify-and-recover-storage --dry-run
```

Disable any separately configured launcher before offline work. If shutdown was unconfirmed, preserve `run.unclean`, WAL/journal and all other evidence and diagnose before maintenance. Reset, key/CA rotation and restore are explicit separate workflows, not entrypoint behavior. Recovery operates on the existing installation with its matching custody; it is **not fresh-install import**, migration or host relocation. Never copy an old database over the current one or remove markers to force startup.

## Evidence boundary

No Docker access exists in the coding environment; Docker build/runtime validation belongs to CI. `make test-ci` checks classifier/Required wiring and deterministic smoke-owner, packaging and documentation contracts without contacting Docker. CI builds one disposable image, checks uninitialized/unsafe-mount refusal, performs explicit non-root init, starts the example service, requires readiness and authenticated CLI access, stops within the grace period and removes its owned containers/volume/network/image with finite deadlines. Failed or missing Docker smoke blocks delivery.

This basic smoke is not full cross-protocol/lifecycle/security qualification, live Debian adoption, physical-durability evidence or a real proxy/provider test. Those require their own qualification and authorization; green source or container CI does not supply it.
