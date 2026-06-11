module github.com/averycrespi/agent-tools/pi-dispatcher

go 1.25.9

require (
	github.com/ncruces/go-sqlite3 v0.32.0
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/kr/pretty v0.3.1 // indirect
	golang.org/x/text v0.36.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

require (
	github.com/averycrespi/agent-tools/sandbox-manager v0.0.0
	github.com/averycrespi/agent-tools/worktree-manager v0.0.0
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/ncruces/julianday v1.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tetratelabs/wazero v1.11.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/averycrespi/agent-tools/sandbox-manager => ../sandbox-manager

replace github.com/averycrespi/agent-tools/worktree-manager => ../worktree-manager
