package service

import (
	"os"
	"runtime"
)

// LegacyHTTPDisabled preserves omitted HTTP selections in unchanged launchd
// definitions, including automatic restarts after binary replacement. The hint
// only suppresses an implicit listener; it is never process or credential authority.
func LegacyHTTPDisabled() bool {
	return legacyHTTPDisabled(runtime.GOOS, os.Getenv("XPC_SERVICE_NAME"))
}

func legacyHTTPDisabled(platform, label string) bool {
	return platform == "darwin" && label == Label
}
