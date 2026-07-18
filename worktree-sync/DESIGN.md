# Worktree Sync Design

`worktree-sync` treats a complete `git worktree list --porcelain -z` result as desired state and a complete metadata-bearing tmux snapshot on the dedicated `wts` socket as actual state. It creates or repairs owned projections before deleting stale ones and performs no destructive apply when either snapshot is incomplete.

The primary worktree maps to one base window in `wts-<repo-id>`. Canonical existing linked worktrees contained component-wise by an allowed root map to managed windows. Names are readable projections; stable Git administrative identities and tmux user-option metadata determine ownership.

Automatic behavior never removes Git worktrees, branches, or directories and never prunes. Setup and launch are trusted host configuration with separate explicit/passive policies and durable once-per-definition attempts. Event watching only nudges a full reconciliation; startup and periodic scans guarantee recovery.

The core supports macOS and Linux and has no Lima dependency. A sandbox requires no special integration when registered paths are exposed transparently and consistently to the host.
