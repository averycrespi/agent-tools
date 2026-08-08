# agent-tools

Monorepo of tools for working with AI coding agents.

## Structure

```
mcp-broker/          MCP proxy for sandboxed agents — see mcp-broker/CLAUDE.md
sandbox-manager/     Lima VM sandbox manager for isolated agent environments — see sandbox-manager/CLAUDE.md
local-git-mcp/       Stdio MCP server for authenticated git remote operations — see local-git-mcp/CLAUDE.md
local-gomod-proxy/  Host-side Go module proxy for sandboxed agents — see local-gomod-proxy/CLAUDE.md
telegram-mcp/       Minimal stdio MCP server for sending Telegram notifications — see telegram-mcp/CLAUDE.md
http-broker/         MITM HTTP/HTTPS forward proxy that injects credentials for sandboxed agents — see http-broker/CLAUDE.md
```

Each Go tool has its own `CLAUDE.md` with tool-specific instructions.

## Development

```bash
npm install    # install Husky/Prettier deps and Git hooks
make install   # install all Go tool binaries
make build     # build all Go tools
make test      # test all Go tools
make audit     # tidy + fmt + lint + test + govulncheck for all Go tools
```

Targets are forwarded to each tool's Makefile. Run from any subdirectory for a single tool. The pre-commit hook runs `lint-staged` for Go and docs formatting, then `make lint` for Go linting.

## Service Layout

Each Go tool is a separate Go module under `go.work` and follows the same baseline so structure is predictable. When adding a new Go tool or looking for the expected shape, mirror an existing tool (e.g. `sandbox-manager/`) as a template — file layout, Makefile targets, and package organization should match.

## Go Conventions

Keep each tool as an independent Go module. Prefer copying small local interfaces and helpers, such as `exec.Runner`, over introducing a shared internal module. When adding subprocess execution, use a context-aware runner with finite deadlines; copy the nearest tool's established pattern and keep timeout behavior documented in that tool's `DESIGN.md` or `CLAUDE.md`.

## CLI Conventions

For Cobra commands, avoid exposing generic argument validation errors such as `accepts 2 arg(s), received 0`.
Prefer command-specific `Args` functions that name missing arguments and include the command usage or a short example when quoting/order matters.
Let Cobra print execution errors once; don't wrap `Execute()` with a second stderr print unless `SilenceErrors` is enabled.

## Provisioning Scripts

Example provisioning scripts live in each tool's `examples/provision/` and run under `sb provision`, which re-runs every script on each invocation. Write them to converge, not to skip.

A script that edits a shell startup file (`~/.bashrc`) must fence its block between `# >>> <name> >>>` and `# <<< <name> <<<` markers and **replace that block wholesale on every run**:

```bash
touch "$BASHRC"
if grep -qF "$MARKER_START" "$BASHRC"; then
	sed -i "/^${MARKER_START}$/,/^${MARKER_END}$/d" "$BASHRC"
fi
if [[ -s "$BASHRC" && -n "$(tail -c 1 "$BASHRC")" ]]; then
	printf '\n' >>"$BASHRC"
fi
cat >>"$BASHRC" <<EOF
$MARKER_START
...
$MARKER_END
EOF
```

The conditional `printf` keeps the marker on its own line when an existing file lacks a final newline without adding blank lines on repeated runs. Do not guard the write with `if ! grep -qF "$MARKER_START"`, which makes the block write-once. The contents change between versions — hostnames, ports, added exports — and a write-once block leaves an already-provisioned sandbox on the old ones forever, with no error to reveal it. `configure-http-broker.sh` is the reference implementation.

Two things follow from this shape:

- **Interpolate paths, read secrets at runtime.** Export a token as `$(cat "$TOKEN_FILE")` so shell startup reads the current file, rather than baking the value in. Rotation on the host then needs only the `copy_paths` refresh, and the block never holds a secret.
- **Replacing moves the block to the end of the file.** Content outside the markers is preserved but ordering relative to it is not, so a block must not depend on a later line in `~/.bashrc`.

Installs and system-level steps stay guarded, since they are expensive and not version-sensitive in the same way: check `command_exists`/`dpkg -s` before installing, and compare before rewriting a trust-store cert. Prefer scripts that are self-contained (work on a bare sandbox) and single-purpose; a script depending on another must check for the prerequisite upfront and fail fast.

## Doc Purposes

Each doc has a distinct audience and scope — don't duplicate content between them.

- **`README.md`** — user-facing. What the tool does, how to install it, how to use it (Quick Start, Commands, security notes). Audience: anyone consuming the tool.
- **`DESIGN.md`** — source of truth for what the system should be and do. Motivation, intended behavior, architecture, key design decisions. When code and `DESIGN.md` disagree, the code is the bug. Update `DESIGN.md` deliberately when the intended design changes. Audience: anyone deciding what the tool should do.
- **`CLAUDE.md`** — conventions for Claude sessions inside the tool. Development commands, package layout, dependency flow, tool-specific gotchas (intentional `//nolint` directives, error-wrapping patterns, invariants that aren't obvious from the code). Audience: Claude and humans editing the tool. Each `CLAUDE.md` has a sibling `AGENTS.md` symlink pointing to it, so non-Claude agents that look for `AGENTS.md` get the same content.
- **`docs/*.md`** — standalone topic guides (e.g., `docs/launchd.md`). Use when a topic is too detailed for the README but isn't design-level context.
- **`examples/`** — copy-pasteable artifacts referenced from `docs/` or the README.

## Diagrams and SVGs

When editing hand-authored SVG diagrams, verify both structure and appearance before reporting success:

1. Validate the SVG as XML, for example with `python3 - <<'PY'` and `xml.etree.ElementTree.parse(...)`.
2. Render the SVG to a temporary PNG and inspect it visually. If no renderer is installed, use `npx -y @resvg/resvg-js-cli path/to/input.svg /tmp/output.png`.
3. Read the rendered PNG with the image reader and look for visual defects: overlapping labels, arrowheads landing inside text, lines crossing nodes unnecessarily, cramped spacing, unclear grouping, and inaccessible color contrast.
4. Prefer direct horizontal or vertical arrows when boxes can be aligned. Avoid elbow/kinked arrows unless they route around another element.
5. Leave enough whitespace that every arrow has a visible tail segment and arrowhead; widen the canvas rather than squeezing right-side or bottom-row nodes together.
6. Align related source/target boxes on the same axis when the relationship is conceptually direct, such as host worktree to mounted worktree or proxy to external service.
7. Keep edge labels off strokes and borders; add a small background rectangle when a label must sit near a line.
8. Iterate on the SVG and re-render until the visual output matches the intent.

Prefer SVG for diagrams that need precise layout or styling. Mermaid is fine for simple diagrams, but avoid claiming visual quality from source inspection alone.

## Changing the Go Tool Set

When adding or removing a Go tool, update every repo-level index that describes or operates on the tool set:

1. Create or remove `<name>/` with `go.mod` (`module github.com/averycrespi/agent-tools/<name>`)
2. For additions, copy `Makefile` and `.golangci.yml` from an existing tool and update the binary name
3. For additions, scaffold `cmd/<binary>/main.go` + `root.go` and `internal/` packages
4. For additions, write `README.md`, `DESIGN.md`, `CLAUDE.md` (see purposes above), and add an `AGENTS.md` symlink to `CLAUDE.md` (`ln -s CLAUDE.md AGENTS.md`)
5. Update the root `TOOLS` list in `Makefile`
6. Update `go.work`
7. Update the root `README.md` overview, install examples, and tool section
8. Update the root `CLAUDE.md` / `AGENTS.md` structure list
9. Update `assets/tool-relationships.svg` so the architecture diagram matches the current tool set, then validate and render it using the Diagrams and SVGs checklist above
10. Run `go mod tidy` for affected modules
