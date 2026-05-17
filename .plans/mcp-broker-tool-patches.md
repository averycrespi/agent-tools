# MCP Broker Tool Patches Plan

## Goal

Add load-time `tool_patches` configuration to let mcp-broker hide discovered tools and repair MCP tool annotations before tools are exposed to clients.

## Constraints

- Keep rules focused on call-time policy; `tool_patches` are load-time tool catalog transforms.
- Match patch `tool` patterns against broker-prefixed tool names such as `github.search_code`.
- Reuse the same glob behavior as rules (`filepath.Match`) and use ordered first-match-wins semantics.
- Preserve existing behavior when `tool_patches` is absent or empty.
- Do not add `_meta` patching in v1; this feature targets MCP `annotations` specifically.
- Apply patches before MCP tool registration, dashboard listing, rule debugging views, and call routing see the tool registry.
- If a tool is disabled, do not expose it or route calls to it.

## Acceptance Criteria

- AC-1: A config with no `tool_patches` loads and behaves exactly like the current broker for discovered tools, annotations, and calls.
- AC-2: A patch with `disable: true` matching a prefixed tool name prevents that tool from appearing in MCP `tools/list` and dashboard `/api/tools` output.
- AC-3: A disabled tool cannot be called through the broker and returns the existing unknown-tool failure behavior.
- AC-4: A patch with `annotations` matching a prefixed tool name merges field-by-field with backend annotations: present patch fields override, omitted patch fields remain unchanged, and missing backend annotations are created.
- AC-5: When multiple patches could match a tool, only the first matching patch applies.
- AC-6: Documentation shows the `tool_patches` config shape, matching semantics, disable behavior, and annotation merge behavior.

## Chosen Approach

Add a top-level ordered `tool_patches` array to config and apply it inside `internal/server.Manager.discover` immediately after prefixing each backend tool name and before inserting it into `m.tools`.

Example config:

```json
{
  "tool_patches": [
    {
      "tool": "github.search_*",
      "annotations": {
        "readOnlyHint": true,
        "destructiveHint": false,
        "idempotentHint": true,
        "openWorldHint": true
      }
    },
    {
      "tool": "github.delete_*",
      "disable": true
    }
  ]
}
```

This keeps the transform close to the tool registry, so one decision affects MCP exposure, dashboard display, rule debugging, Telegram descriptions, and routing consistently. The main trade-off is that patching is startup/discovery-time only; dynamic backend tool changes would require rediscovery/restart, matching the current broker architecture.

## Documentation Impact

- Update `mcp-broker/README.md`:
  - Add `tool_patches` to the configuration example.
  - Add a section describing load-time tool patches, first-match-wins matching, `disable`, and annotation merge semantics.
- Update `mcp-broker/DESIGN.md`:
  - Extend Config and Server manager sections to describe tool patching during discovery.
  - Clarify that disabled tools are removed from the broker registry and therefore hidden/unroutable.
- No changelog exists in this repo/tool, so no changelog update is required.

## Assumptions / Open Questions

- Q1: Config field name is `tool_patches`. Status: confirmed.
- Q2: Annotation patching should merge field-by-field rather than replace the whole annotation object. Status: confirmed.
- Q3: The config key should be `annotations`, not `metadata`, because MCP tool annotations are the exact field being repaired. Status: confirmed.
- Q4: `_meta` patching is out of scope for v1. Status: assumed from validation; revisit only if requested.

## Ordered Tasks

### T1: Add config model for tool patches

Covers: AC-1, AC-4, AC-5

- Extend `mcp-broker/internal/config/config.go` with `ToolPatches []ToolPatchConfig` on `Config`.
- Add `ToolPatchConfig` with:
  - `Tool string \`json:"tool"\``
  - `Disable bool \`json:"disable,omitempty"\``
  - `Annotations *ToolAnnotationsPatch \`json:"annotations,omitempty"\``
- Represent annotation patch fields as pointers so omitted fields are distinguishable from explicit `false`, and give each field an explicit lower-camel JSON tag matching MCP annotation keys:
  ```go
  Title           *string `json:"title,omitempty"`
  ReadOnlyHint    *bool   `json:"readOnlyHint,omitempty"`
  DestructiveHint *bool   `json:"destructiveHint,omitempty"`
  IdempotentHint  *bool   `json:"idempotentHint,omitempty"`
  OpenWorldHint   *bool   `json:"openWorldHint,omitempty"`
  ```
- Add config tests for load/save round-trip, omitted annotations, explicit false boolean values, and raw saved JSON key casing.

### T2: Implement patch matching and application

Covers: AC-1, AC-2, AC-4, AC-5

- Add a small internal helper in `mcp-broker/internal/server` to evaluate patches against a prefixed tool name using `filepath.Match`.
- Preserve rules-like behavior for malformed globs by skipping invalid patterns rather than failing startup, unless implementation discovers rules actually require stricter parity.
- Implement first-match-wins: stop scanning patches after the first pattern that matches the prefixed tool name.
- Implement annotation merge:
  - If the tool has no annotations and patch annotations exist, create a new `mcp.ToolAnnotation`.
  - Copy existing annotations before mutating to avoid aliasing surprises.
  - For each non-nil patch field, set the corresponding annotation field.
  - Leave omitted fields unchanged.
- Treat `disable: true` as removal from the registry; if a patch has both `disable` and `annotations`, disabled behavior wins because the tool is skipped.

### T3: Wire patches into manager construction and discovery

Covers: AC-1, AC-2, AC-3, AC-4

- Change `server.NewManager` to accept tool patches in addition to servers, or add an equivalent options struct if that is cleaner.
- Update `cmd/mcp-broker/serve.go` to pass `cfg.ToolPatches` when constructing the manager.
- Update tests and any direct `Manager` construction as needed.
- In `discover`, apply patches after computing `prefixed := name + "." + tool.Name` and before inserting `m.tools[prefixed]`.
- Ensure disabled tools are absent from `m.tools`, so `Tools()`, `ToolDescription`, and `Call` naturally follow existing behavior.

### T4: Add unit coverage

Covers: AC-1, AC-2, AC-3, AC-4, AC-5

- Add/extend `mcp-broker/internal/server/manager_test.go` tests for:
  - no patches preserves existing discovered tools and annotations;
  - disable patch hides a matched tool and leaves unmatched tools visible;
  - call to disabled tool returns `unknown tool`;
  - annotation patch merges into existing annotations and preserves omitted fields;
  - annotation patch creates annotations when backend supplied none;
  - first matching patch wins.
- Add/extend `mcp-broker/internal/config/config_test.go` tests for JSON parsing/round-trip of `tool_patches`, especially explicit `false` annotation values.

### T5: Add e2e coverage

Covers: AC-2, AC-4

- Extend `mcp-broker/test/e2e/teststack_test.go` config/test helper types to include `tool_patches`.
- Add e2e test confirming a disabled backend tool is absent from MCP `ListTools` and dashboard `/api/tools`.
- Add e2e test that attempts to call a disabled tool through the MCP client and asserts the observable client failure shape; also ensure the backend handler is not called if the test helper can track call counts without overcomplicating the fixture.
- Add e2e test confirming annotation patch merge is visible through MCP `ListTools`.

### T6: Update documentation

Covers: AC-6

- Update `mcp-broker/README.md` configuration docs with a concise `tool_patches` example and semantics.
- Update `mcp-broker/DESIGN.md` to document where patches apply in discovery and how disable/annotation merge affect the registry.

## Verification Checklist

- [ ] V1: Run `make test` from `mcp-broker/` and confirm all race-enabled unit/integration-default tests pass.
- [ ] V2: Run `make test-e2e` from `mcp-broker/` and confirm e2e tests pass.
- [ ] V3: Run `make lint` from `mcp-broker/` and confirm lint passes.
- [ ] V4: Confirm `README.md` and `DESIGN.md` document the implemented `tool_patches` behavior.
- [ ] V5: Confirm Documentation Impact was followed, or update this plan if documentation needs changed during execution.

## Known Issues / Follow-ups

- `_meta` patching is intentionally out of scope for v1.
- Patch application is startup/discovery-time only; dynamic rediscovery is not addressed.
