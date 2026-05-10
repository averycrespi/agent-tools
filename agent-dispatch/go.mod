module github.com/averycrespi/agent-tools/agent-dispatch

go 1.25.9

require (
	github.com/ncruces/go-sqlite3 v0.30.0
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/averycrespi/agent-tools/sandbox-manager v0.0.0
	github.com/averycrespi/agent-tools/worktree-manager v0.0.0
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tetratelabs/wazero v1.9.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/averycrespi/agent-tools/sandbox-manager => ../sandbox-manager

replace github.com/averycrespi/agent-tools/worktree-manager => ../worktree-manager
