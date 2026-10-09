package main

// legacyHTTPDisabled preserves omitted HTTP selections in existing launchd
// definitions, including automatic restarts after binary replacement. The hint
// only suppresses an implicit listener; it is never process or credential authority.
func legacyHTTPDisabled(platform, label string) bool {
	return platform == "darwin" && label == "dev.agent-tools.agent-gateway"
}
