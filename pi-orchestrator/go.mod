module github.com/averycrespi/agent-tools/pi-orchestrator

go 1.25.9

require (
	github.com/averycrespi/agent-tools/pi-dispatcher v0.0.0
	github.com/averycrespi/agent-tools/sandbox-manager v0.0.0
	github.com/averycrespi/agent-tools/worktree-manager v0.0.0
	github.com/ncruces/go-sqlite3 v0.35.0
	github.com/spf13/cobra v1.10.2
	gopkg.in/yaml.v3 v3.0.1
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/ncruces/go-sqlite3-wasm/v3 v3.1.35302 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/averycrespi/agent-tools/pi-dispatcher => ../pi-dispatcher

replace github.com/averycrespi/agent-tools/sandbox-manager => ../sandbox-manager

replace github.com/averycrespi/agent-tools/worktree-manager => ../worktree-manager
