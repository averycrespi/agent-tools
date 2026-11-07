package contract

// Resource keys are public identifiers, never credentials. MCP servers retain
// their existing namespace grammar and identity instead of a second key.
const ResourceKeyBytes = 64

func ValidResourceKey(key string) bool {
	if len(key) == 0 || len(key) > ResourceKeyBytes || key[0] < 'a' || key[0] > 'z' {
		return false
	}
	for _, c := range []byte(key) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}
