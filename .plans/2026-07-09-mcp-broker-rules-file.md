# MCP Broker Separate Rules File Plan

## Goal

Move mcp-broker base policy rules out of the sensitive `config.json` into a separate, shareable-by-intent `rules.json` file whose path is configured by `rules_path`. Add a `rules` CLI command with `path`, `refresh`, and `edit` subcommands parallel to the existing `config` command, while preserving existing users' embedded-rule policies through automatic migration.

## Background / Repo Context

- `mcp-broker` currently stores base policy rules in the top-level `rules` field of `config.json`; `internal/config/config.go` defines `Config.Rules`, seeds it in `DefaultConfig`, and `Save` writes it back.
- `cmd/mcp-broker/serve.go` currently compiles startup rules from `cfg.Rules` and handles SIGHUP by calling `config.LoadRules(configPath)`, which extracts `rules` from `config.json`.
- `cmd/mcp-broker/config.go` already implements the CLI pattern to mirror: `config path`, `config refresh`, and `config edit`.
- Tests currently assume embedded rules in several areas: `internal/config/config_test.go`, `cmd/mcp-broker/serve_test.go`, and `test/e2e/teststack_test.go`.
- User-facing docs currently describe embedded config rules in `mcp-broker/README.md`, `mcp-broker/DESIGN.md`, and `mcp-broker/docs/launchd.md`.
- Grant rules are separate from this work: `grant mint --rules-file` remains a per-grant rule overlay feature and should not be conflated with the broker's base rules file.

## Acceptance Criteria

- AC-1: New/default mcp-broker configuration includes `rules_path` and omits the legacy embedded top-level `rules` array from saved `config.json`.
- AC-2: The default effective rules path is `rules.json` alongside the effective config file path, including when `--config /custom/dir/config.json` is used and `rules_path` is omitted.
- AC-3: `rules.json` uses the canonical object shape `{ "rules": [...] }`, is created with mode `0600`, and defaults to the catch-all `require-approval` rule.
- AC-4: `serve` loads startup base rules from the effective rules file path, and SIGHUP reloads the same source without reconnecting backends.
- AC-5: Legacy embedded `config.json.rules` auto-migrates into `rules.json` when the effective rules file is missing, preserving the user's existing policy.
- AC-6: If both legacy embedded `config.json.rules` and the effective `rules.json` exist, `rules.json` is authoritative; legacy rules are ignored and a warning is emitted where the caller has a logger or stderr.
- AC-7: A malformed legacy embedded `config.json.rules` must not break startup when an authoritative `rules.json` exists.
- AC-8: Invalid `rules.json` content or invalid compiled rule patterns fails closed on startup; invalid SIGHUP reload logs/returns an error and preserves the prior active rules snapshot.
- AC-9: `mcp-broker rules path`, `mcp-broker rules refresh`, and `mcp-broker rules edit` exist and behave analogously to `config path`, `config refresh`, and `config edit`, scoped only to the rules file.
- AC-10: README, DESIGN, launchd docs, and CLI references describe `rules.json`, `rules_path`, migration/precedence, SIGHUP reload behavior, and the new `rules` CLI.

## Non-Goals / Out of Scope

- Do not add a `--rules` CLI flag; the rules file path is configured only through `rules_path` in `config.json`.
- Do not change rule evaluation semantics, verdict names, argument matching behavior, grant overlay behavior, dashboard approval behavior, or audit semantics except for rule source loading.
- Do not create dashboard editing controls for base rules.
- Do not change the grant `--rules-file` format or grant storage behavior.

## Constraints

- Preserve existing mcp-broker security posture: config files remain private, created files use conservative permissions, and listener behavior is unchanged.
- `rules.json` creation must use file mode `0600`; parent directories should follow existing config directory permissions (`0750`).
- `rules.json` canonical save format is an object with a top-level `rules` array, not a bare array.
- `rules_path` is the top-level config field name.
- `rules.json` wins over legacy embedded rules when both are present.
- Hot reload must remain rules-only and must not restart backend MCP servers or rebuild the HTTP listener.

## Chosen Approach

Introduce rules-file management in `internal/config` as a first-class companion to config management. The main config model should carry `RulesPath string `json:"rules_path"``but should not treat legacy top-level`rules` as a normal persisted field. A shared resolver should load config, compute the effective rules path, detect legacy embedded rules from raw JSON for migration/warnings, and load or create the external rules document.

The default rules path should be derived from the selected config path, not hard-coded only to XDG: if `--config` points at `/tmp/profile/config.json`, omitted `rules_path` should resolve to `/tmp/profile/rules.json`. If `rules_path` is explicitly set, use that path as configured.

Use one shared rules-source path for startup, SIGHUP reload, CLI refresh, and tests so precedence and migration behavior cannot diverge.

## Design Decisions

- D1: Rules path is configured by top-level `rules_path`; no separate `--rules` flag. Rationale: user selected persisted config-only path control.
- D2: Canonical rules file shape is `{ "rules": [...] }`. Rationale: user selected an extensible object shape.
- D3: Legacy embedded rules auto-migrate only when the effective rules file is missing. Rationale: preserves existing policies without overwriting an already-separated rules file.
- D4: When both sources exist, `rules.json` wins and legacy rules are ignored with a warning. Rationale: supports the new split and avoids surprising users who edit the rules file.
- D5: Created rules files use `0600`. Rationale: user selected conservative permissions even though the file is intended to be shareable.
- D6: Config loading must not unmarshal legacy `rules` into the main `Config` in a way that can fail when `rules.json` exists. Rationale: authoritative `rules.json` must win even if stale embedded rules are malformed.

## Implementation Notes

- `mcp-broker/internal/config/config.go`
  - Replace or deprecate `Config.Rules` as a normal persisted field; add `RulesPath string `json:"rules_path"``.
  - Ensure `DefaultConfig` can seed `RulesPath`, but also ensure omitted paths are resolved relative to the actual config path passed to `Load`/`Refresh`.
  - Avoid writing embedded `rules` when saving config. Consider using a separate raw-load helper to detect legacy `rules` without keeping it in the saved struct.
  - Keep existing config validation for grants, servers, OAuth, and startup retry settings.
- `mcp-broker/internal/config/rules.go`
  - Rework `LoadRules` from "extract rules from config" into rules-document loading from the effective rules path, or introduce clearer helpers such as `RulesPath(configPath, cfg)`, `LoadRulesFile(path)`, `SaveRulesFile(path, rules)`, `RefreshRules(configPath)`, and `LoadConfigAndRules(configPath)`.
  - Read/write canonical `{ "rules": [...] }` documents.
  - Migration helper should detect legacy embedded `rules` using raw JSON so malformed legacy rules can be ignored when `rules.json` exists, but parsed and migrated when `rules.json` is missing.
  - Ensure `null`, missing `rules`, non-array `rules`, malformed arg patterns, and malformed JSON produce useful errors for actual `rules.json` loads.
- `mcp-broker/cmd/mcp-broker/serve.go`
  - Load config and rules through the shared resolver; compile startup rules from the external rules document.
  - Log both config path and effective rules path.
  - SIGHUP reload should reload from the effective rules path. If `rules_path` changes in `config.json`, prefer re-reading config to discover the new path only if that can be done without expanding scope; otherwise document/restrict that changing `rules_path` requires restart. The simpler and safer target is: rules content hot-reloads, path changes require restart.
  - Preserve current invalid-reload behavior: warn and keep prior active rules.
- `mcp-broker/cmd/mcp-broker/config.go`
  - `config refresh` should backfill config defaults, write/migrate rules as needed, and leave `config.json` without embedded rules.
  - `config edit` should open only `config.json`; it may ensure config/rules defaults first to avoid missing-file surprises.
- Add `mcp-broker/cmd/mcp-broker/rules.go`
  - `rules path`: load/resolve config and print the effective rules file path.
  - `rules refresh`: create or rewrite the rules file in canonical object format after loading/migrating current effective rules; do not rewrite unrelated config beyond any necessary first-run/default config creation.
  - `rules edit`: ensure/refresh the rules file, then open it in `$EDITOR`, defaulting to `vi`, following the existing `config edit` pattern.
- Tests to update/add:
  - Config default/save tests proving `rules_path` is present and top-level `rules` is absent after save/refresh.
  - Rules file load/save tests for canonical object shape, missing file default creation, invalid JSON, `null`, non-array `rules`, and file mode `0600` where portable.
  - Migration tests: embedded rules migrate when rules file missing; both-exist uses rules file and warns; malformed legacy embedded rules is ignored when rules file exists.
  - Serve reload tests in `cmd/mcp-broker/serve_test.go` should write/edit `rules.json` instead of embedded config rules.
  - CLI tests for `rules path`, `rules refresh`, and `rules edit` if the command package already has command-level tests; otherwise cover through focused helpers plus at least command construction/registration.
  - E2E fixtures in `test/e2e/teststack_test.go` should create/use separate `rules.json` and config `rules_path`.
- Documentation to update:
  - `mcp-broker/README.md`: configuration example, top-level settings table, rules section, reload section, migration/precedence note, CLI command list.
  - `mcp-broker/DESIGN.md`: config section, rules reload section, CLI section, design rationale.
  - `mcp-broker/docs/launchd.md`: state/config table and reload instructions.
  - Any examples that embed rules in config should be updated or explicitly labeled as legacy migration examples.

## Documentation Impact

Documentation updates are required because the user-facing config file layout, reload instructions, and CLI surface change. Update `README.md`, `DESIGN.md`, and `docs/launchd.md` at minimum. If any examples or tests include sample `config.json` with embedded `rules`, update them to `rules_path` plus separate `rules.json`, except for deliberate legacy migration fixtures.

## Testing / Verification

- V1: Run `go test -race ./...` from `mcp-broker/`; expected result: all package tests pass.
- V2: Run `go test -race -tags=e2e -timeout=60s ./test/e2e/...` from `mcp-broker/`; expected result: e2e tests pass with separate `rules.json` fixtures.
- V3: Run focused CLI/manual checks with a temp config directory:
  - `mcp-broker --config <tmp>/config.json config refresh` creates/updates config and migrates/creates rules.
  - `mcp-broker --config <tmp>/config.json rules path` prints `<tmp>/rules.json` when `rules_path` is omitted.
  - `mcp-broker --config <tmp>/config.json rules refresh` writes canonical `{ "rules": [...] }` JSON.
  - Inspect file modes where supported: `config.json` and `rules.json` should be `0600`.
- V4: Manually or via tests verify SIGHUP reload reads changes from `rules.json`, invalid reload preserves previous rules, and changing embedded legacy `config.json.rules` after `rules.json` exists has no effect.
- V5: Run `make fmt` and `make lint` in `mcp-broker/` after code changes; expected result: no formatting or lint failures.

## Risks and Mitigations

- Risk: Legacy embedded rules accidentally remain in saved config because `Config.Rules` is still part of the main struct. Mitigation: remove it from normal save path or mark it ignored, and detect legacy `rules` through raw JSON helper code.
- Risk: Startup and SIGHUP use different precedence/migration paths. Mitigation: route both through shared config/rules resolver helpers and test both.
- Risk: Malformed stale embedded `rules` breaks startup despite `rules.json` existing. Mitigation: do not unmarshal legacy rules during main config load; parse legacy rules only when needed for migration and only when rules file is absent.
- Risk: `rules path` is ambiguous when `--config` points to a non-default location. Mitigation: define and test default derivation as sibling `rules.json` next to the effective config path.
- Risk: `config refresh` unexpectedly overwrites user-edited `rules.json`. Mitigation: migration writes rules only when the rules file is missing; refresh canonicalizes only when the user runs `rules refresh`.
- Risk: Docs confuse base rules file with grant `--rules-file`. Mitigation: explicitly distinguish base policy rules from temporary grant overlay rules in README and CLI docs.

## Assumptions

- Relative `rules_path` values, if allowed by the existing JSON path style, may be resolved by normal OS behavior relative to the current working directory unless the implementer finds an established path-normalization convention in this repo. This is not central to the requested behavior; absolute and default paths must work.
- Changing `rules_path` itself is a restart-level config change unless the implementer can support path hot-reload cleanly without expanding complexity. Rule content changes remain SIGHUP-reloadable.

## Handoff Summary

Implement `.plans/2026-07-09-mcp-broker-rules-file.md` by moving base policy rules into a configurable external `rules.json`, adding the `rules` CLI, preserving legacy embedded rules through migration, and updating tests/docs. Complete only after every acceptance criterion is verified with concrete evidence, especially migration, precedence, invalid reload preservation, file permissions, and docs updates.

Suggested `/goal` prompt:

```text
/goal Implement .plans/2026-07-09-mcp-broker-rules-file.md. Complete only after every acceptance criterion is satisfied with concrete evidence from tests, file inspections, and documentation updates.
```
