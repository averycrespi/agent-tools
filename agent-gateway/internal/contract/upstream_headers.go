package contract

import (
	"maps"
	"slices"
	"strings"
)

const (
	UpstreamHeaderCount      = 16
	UpstreamHeaderNameBytes  = 128
	UpstreamHeaderValueBytes = 4096
	UpstreamHeaderBytes      = 8192
)

func ValidateUpstreamHeaders(headers map[string]string) ServerConfigurationRule {
	if len(headers) > UpstreamHeaderCount {
		return ServerConfigurationRuleMaximum
	}
	seen := make(map[string]struct{}, len(headers))
	total := 0
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		value := headers[name]
		if len(name) > UpstreamHeaderNameBytes || len(value) > UpstreamHeaderValueBytes {
			return ServerConfigurationRuleMaximum
		}
		if name == "" {
			return ServerConfigurationRuleInvalid
		}
		for _, character := range []byte(name) {
			if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
				return ServerConfigurationRuleInvalid
			}
		}
		folded := strings.ToLower(name)
		if _, duplicate := seen[folded]; duplicate {
			return ServerConfigurationRuleUnique
		}
		seen[folded] = struct{}{}
		if ReservedUpstreamHeader(folded) {
			return ServerConfigurationRuleDisjoint
		}
		// HTTP trims surrounding whitespace; reject it rather than change configured bytes.
		if strings.TrimSpace(value) != value {
			return ServerConfigurationRuleInvalid
		}
		for _, character := range []byte(value) {
			if character < ' ' || character > '~' {
				return ServerConfigurationRuleInvalid
			}
		}
		total += len(name) + len(value)
	}
	if total > UpstreamHeaderBytes {
		return ServerConfigurationRuleMaximum
	}
	return ""
}

func ReservedUpstreamHeader(name string) bool {
	name = strings.ToLower(name)
	for _, prefix := range []string{"mcp-", "proxy-", "sec-", "x-forwarded-", "content-", "accept-", "if-"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	switch name {
	case "authorization", "authentication-info", "www-authenticate", "cookie", "cookie2", "set-cookie", "set-cookie2",
		"api-key", "x-api-key", "x-auth-token", "x-access-token", "x-authorization",
		"host", "connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade", "expect",
		"forwarded", "via", "x-real-ip", "x-original-url", "x-rewrite-url", "x-http-method-override",
		"origin", "referer", "accept", "user-agent", "range", "cache-control", "pragma", "max-forwards", "date":
		return true
	default:
		return false
	}
}
