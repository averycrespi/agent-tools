//go:build dependencies

// Package dependencies keeps S1's pinned platform dependencies visible to
// module tooling before their owning packages are implemented.
package dependencies

import (
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/zalando/go-keyring"
)
