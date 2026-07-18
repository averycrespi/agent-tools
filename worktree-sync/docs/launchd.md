# LaunchAgent

`wts daemon install` installs the per-user `dev.agent-tools.worktree-sync` LaunchAgent. The generated plist embeds the absolute sibling `wtsd` path, an explicit minimal `PATH`, `RunAtLoad`, `KeepAlive`, and separate files under `~/Library/Logs`. launchd does not load shell profiles or rotate these logs.

Use `wts daemon start|status|stop|uninstall`. These commands affect only this label; uninstall does not modify Git or tmux. On Linux, run `wtsd` directly in the foreground; launchd commands return an unsupported-platform error.
