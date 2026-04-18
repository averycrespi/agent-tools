package private

import (
	"errors"
	"fmt"
	"strings"
)

// ErrModuleNotFound signals that the Go toolchain reported the module or
// version does not exist (as opposed to a transient or auth failure). Callers
// use errors.Is to map to HTTP 404, which the Go module proxy protocol
// requires so clients fall through cleanly instead of retrying.
var ErrModuleNotFound = errors.New("module not found")

// notFoundPatterns are substrings in `go mod download` / `go list` output that
// we treat as authoritative "module or version does not exist" signals.
//
// Sources:
//   - go/src/cmd/go/internal/modfetch/codehost — UnknownRevisionError
//   - go/src/cmd/go/internal/modfetch/coderepo.go — InvalidVersionError
//   - proxy.golang.org returns 404 / 410 for missing artifacts
//
// Caveat (golang/go#42751): GitHub returns 404 for inaccessible private
// repos, so "unknown revision" can mask an auth failure. The `go` tool's
// stderr does not let us disambiguate.
var notFoundPatterns = []string{
	"unknown revision",
	"invalid version",
	"repository does not exist",
	"repository not found",
	"no matching versions",
	"404 Not Found",
	"410 Gone",
}

// classifyError wraps err with ErrModuleNotFound when msg contains a known
// not-found signal. Otherwise it returns err unchanged. msg is the string
// most likely to carry the toolchain's reason (typically the JSON Error
// field, falling back to raw combined output).
func classifyError(err error, msg string) error {
	for _, p := range notFoundPatterns {
		if strings.Contains(msg, p) {
			return fmt.Errorf("%w: %s", ErrModuleNotFound, strings.TrimSpace(msg))
		}
	}
	return err
}
