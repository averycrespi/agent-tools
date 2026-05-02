# Hindsight Auth Hardening

## Goal

Harden the local Hindsight Docker Compose stack so the HTTP API is not public by default and the Control Plane can still communicate with it.

## Constraints

- Use Hindsight's built-in API-key tenant extension rather than adding a reverse proxy.
- Keep the setup Docker Compose based.
- Keep local development convenient.
- Do not commit real secrets.
- Keep all Hindsight-specific files under `hindsight/`.

## Relevant Findings

- Hindsight supports built-in bearer API-key authentication with:
  - `HINDSIGHT_API_TENANT_EXTENSION=hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension`
  - `HINDSIGHT_API_TENANT_API_KEY=<secret>`
- The built-in `ApiKeyTenantExtension` uses the `public` schema for authenticated requests.
- The Control Plane supports passing an API key to the dataplane with:
  - `HINDSIGHT_CP_DATAPLANE_API_KEY=<secret>`
- The current Compose file publishes both ports on all host interfaces:
  - `8888:8888`
  - `9999:9999`
- Hindsight API-key auth protects API requests, but it is not a browser login for the Control Plane UI. Binding both ports to `127.0.0.1` keeps the local UI and API off the LAN by default.

## Acceptance Criteria

1. `hindsight/docker-compose.yml` publishes Hindsight ports only on `127.0.0.1`.
2. `hindsight/docker-compose.yml` enables `ApiKeyTenantExtension` for the API.
3. `hindsight/docker-compose.yml` configures the Control Plane dataplane API key to the same secret.
4. `hindsight/.env.example` documents a placeholder `HINDSIGHT_API_KEY` or equivalent secret without committing a real value.
5. `hindsight/README.md` explains how to set the key, call authenticated API endpoints, and configure local clients with `HINDSIGHT_API_KEY`.
6. `docker compose --env-file hindsight/.env.example -f hindsight/docker-compose.yml config --quiet` passes.
7. `git diff --check` passes.

## Chosen Approach

Use Hindsight's native bearer API-key authentication and bind published ports to localhost:

- Add `HINDSIGHT_API_KEY=change-me` to `hindsight/.env.example`.
- In the `hindsight` service environment:
  - Set `HINDSIGHT_API_TENANT_EXTENSION` to `hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension`.
  - Set `HINDSIGHT_API_TENANT_API_KEY` from `${HINDSIGHT_API_KEY}`.
  - Set `HINDSIGHT_CP_DATAPLANE_API_KEY` from `${HINDSIGHT_API_KEY}` so the bundled Control Plane can call the protected API.
- Change port mappings to:
  - `127.0.0.1:8888:8888`
  - `127.0.0.1:9999:9999`
- Update README setup and verification examples to use `Authorization: Bearer $HINDSIGHT_API_KEY` for API calls.

## Assumptions / Open Questions

- Assumption: The combined Hindsight image's `start-all.sh` starts both API and Control Plane in the same container environment, so `HINDSIGHT_CP_DATAPLANE_API_KEY` is visible to the Control Plane process.
- Assumption: Localhost-only binding is sufficient for the Control Plane UI safety requirement in this local stack.
- Open question for future hardening: If the Control Plane must be accessible from other devices, add a reverse proxy with Basic Auth or another browser-facing auth layer.

## Ordered Tasks

1. Update `hindsight/.env.example` with a non-secret `HINDSIGHT_API_KEY=change-me` placeholder and comments instructing users to change it.
2. Update `hindsight/docker-compose.yml`:
   - Bind API and Control Plane ports to `127.0.0.1`.
   - Add Hindsight tenant extension auth environment variables.
   - Add Control Plane dataplane API-key environment variable.
3. Update `hindsight/README.md`:
   - Tell users to change both `HINDSIGHT_DB_PASSWORD` and `HINDSIGHT_API_KEY`.
   - Explain that API requests require `Authorization: Bearer ...`.
   - Show authenticated health/API verification examples where applicable.
   - Explain ports are localhost-only by default.
4. Run deterministic checks:
   - `docker compose --env-file hindsight/.env.example -f hindsight/docker-compose.yml config --quiet`
   - `git diff --check`
5. Review diff for accidental secret exposure or broken env interpolation.
6. Commit with a concise conventional commit message.

## Verification Checklist

- Compose config renders without errors using `.env.example`.
- Rendered config shows `127.0.0.1` host bindings for ports 8888 and 9999.
- Rendered config includes `HINDSIGHT_API_TENANT_EXTENSION`.
- Rendered config includes `HINDSIGHT_API_TENANT_API_KEY` and `HINDSIGHT_CP_DATAPLANE_API_KEY` sourced from the placeholder value only.
- README examples load `.env` before using `HINDSIGHT_API_KEY`.
- No real API key or password is committed.

## Known Issues / Follow-ups

- This does not add browser login to the Control Plane UI. It relies on localhost binding for UI safety.
- If remote access is needed later, add a reverse proxy with Basic Auth or an identity-aware proxy in front of both ports.
