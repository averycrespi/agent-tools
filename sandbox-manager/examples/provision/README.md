# Example provisioning scripts

Drop-in provisioning scripts for common sandbox setups. Most scripts are **self-contained** — they work on a bare sandbox and install whatever they need (including prerequisites like `curl`). A small number require another tool to be installed first; those scripts fail fast with a clear error message that names the missing prerequisite. Requirements are noted in the catalog below. Scripts can be used individually or listed together in your `~/.config/sb/config.json`.

## Using them

Reference any script by path in the `scripts` field of your config. Paths starting with `~/` are expanded to your home directory:

```json
{
  "scripts": [
    "~/Workspace/agent-tools/sandbox-manager/examples/provision/docker.sh",
    "~/Workspace/agent-tools/sandbox-manager/examples/provision/claude-code.sh"
  ]
}
```

Scripts run in the listed order.

## Catalog

| Script            | What it does                                                | Requires         |
| ----------------- | ----------------------------------------------------------- | ---------------- |
| `apt-packages.sh` | Template for installing a user-defined list of apt packages | —                |
| `docker.sh`       | Install Docker Engine and enable the daemon                 | —                |
| `asdf.sh`         | Install asdf as a prebuilt binary and wire up `$PATH`       | —                |
| `asdf-nodejs.sh`  | Install the latest Node.js via asdf                         | `asdf.sh`        |
| `asdf-golang.sh`  | Install the latest Go via asdf                              | `asdf.sh`        |
| `claude-code.sh`  | Install Claude Code (native binary)                         | —                |
| `pi-agent.sh`     | Install the Pi coding agent via npm                         | `asdf-nodejs.sh` |

## Convention for new examples

- Prefer self-containment: assume a bare Ubuntu sandbox with `sudo`, `apt-get`, and `bash` available, and install anything else you need.
- If a script depends on a non-trivial tool that has its own example script (e.g. asdf), it's okay to require that tool to be installed first — but the script must check for it upfront and fail fast with an error that names the missing prerequisite.
- Be idempotent: guard installs with `command_exists`, `dpkg -s`, or equivalent; print a "skipping" message when the work is already done.
- Pin versions for bootstrap infrastructure (installer URLs, prebuilt binaries). Use `latest` for tooling that a version manager (e.g. asdf) is meant to track.
- Use `set -euo pipefail` and quote variables.
- Keep scripts single-purpose — one script per tool or concern.
