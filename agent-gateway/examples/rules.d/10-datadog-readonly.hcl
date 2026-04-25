# Read-only Datadog API access via the `pup` CLI (https://github.com/datadog-labs/pup)
# or any client that honours DD_API_KEY / DD_APP_KEY / DD_SITE.
#
# Only GETs under /api/v1/ and /api/v2/ are allowed. Writes (POST/PUT/PATCH/
# DELETE) and any other path fall through: the sandbox's dummy credentials
# reach Datadog and the request fails 4xx upstream. Drop this file in
# ~/.config/agent-gateway/rules.d/ and run `agent-gateway reload`.
#
# ---- One-time setup on the host --------------------------------------------
#
#   echo -n "<real-api-key>" | agent-gateway secret add dd_api_key --host api.datadoghq.com
#   echo -n "<real-app-key>" | agent-gateway secret add dd_app_key --host api.datadoghq.com
#
# Without these, requests matched by these rules will fail with HTTP 403 and
# header `X-Agent-Gateway-Reason: secret-unresolved` — the rule matched but
# the gateway had no `${secrets.dd_api_key}` / `${secrets.dd_app_key}` to inject.
#
# ---- Inside the sandbox ----------------------------------------------------
#
#   export DD_API_KEY=dummy DD_APP_KEY=dummy DD_SITE=datadoghq.com
#   pup monitors list                       # swapped at the gateway, real call
#
# ---- Changing Datadog region -----------------------------------------------
#
# The host is always `api.<DD_SITE>`. To target a non-default region, edit the
# `host` line below and rebind both secrets:
#
#   agent-gateway secret bind   dd_api_key --host api.datadoghq.eu
#   agent-gateway secret unbind dd_api_key --host api.datadoghq.com
#   # (repeat for dd_app_key, then `agent-gateway reload`)
#
# Known DD_SITE values: datadoghq.com (US1, default), us3.datadoghq.com,
# us5.datadoghq.com, datadoghq.eu (EU1), ap1.datadoghq.com, ap2.datadoghq.com,
# ddog-gov.com.
#
# ---- Minimum APP key scopes ------------------------------------------------
#
# Create the APP key as a *scoped* key (Datadog UI: Organization Settings ->
# Application Keys -> New Key -> Scopes) to cap blast radius. Pick only the
# reads the agent actually needs. Common pup-reading scopes:
#
#   apm_read, apm_service_catalog_read, dashboards_read, error_tracking_read,
#   events_read, hosts_read, incident_read, metrics_read, monitors_read,
#   slos_read, synthetics_read, teams_read, user_access_read
#
# Full list of authorization scopes: https://docs.datadoghq.com/api/latest/scopes/
#
# Log data is not covered by an OAuth scope; a scoped APP key with the
# `logs_read_data` role permission is required if the agent needs logs.
# ----------------------------------------------------------------------------

rule "datadog-readonly" {
  match {
    host   = "api.datadoghq.com"
    method = "GET"
    path   = "/api/v*/**"
  }

  verdict = "allow"

  inject {
    replace_header = {
      "DD-API-KEY"         = "${secrets.dd_api_key}"
      "DD-APPLICATION-KEY" = "${secrets.dd_app_key}"
    }
  }
}
