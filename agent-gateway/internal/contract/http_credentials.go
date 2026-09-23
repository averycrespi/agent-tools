package contract

import "strings"

const (
	HTTPCredentialNameBytes   = 256
	HTTPCredentialHeaderBytes = 128
	HTTPCredentialPrefixBytes = 128
	HTTPCredentialValueBytes  = 4096
)

type HTTPCredentialBoundary struct {
	Host          string `json:"host"`
	Port          uint16 `json:"port"`
	AllowWildcard bool   `json:"allow_wildcard"`
}

type HTTPCredentialDefinition struct {
	Name     string                 `json:"name"`
	Boundary HTTPCredentialBoundary `json:"boundary"`
	Recipe   HTTPCredentialRecipe   `json:"recipe"`
}

type HTTPCredentialReference struct {
	ID string `json:"id"`
}

type HTTPCredential struct {
	ID string `json:"id"`
	HTTPCredentialDefinition
	Revision   string                    `json:"revision"`
	Available  bool                      `json:"available"`
	References []HTTPCredentialReference `json:"referencing_grants"`
	CreatedAt  string                    `json:"created_at"`
	UpdatedAt  string                    `json:"updated_at"`
}

// HTTPCredentialRecipe is safe configuration, never a secret template.
type HTTPCredentialRecipe struct {
	Header string `json:"header"`
	Prefix string `json:"prefix"`
}

// ValidHTTPCredentialRecipe permits one end-to-end authentication field. It
// deliberately does not reuse the MCP plaintext-header allowlist, which forbids
// authentication fields and has a different trust boundary.
func ValidHTTPCredentialRecipe(recipe HTTPCredentialRecipe) bool {
	if len(recipe.Header) == 0 || len(recipe.Header) > HTTPCredentialHeaderBytes || len(recipe.Prefix) > HTTPCredentialPrefixBytes {
		return false
	}
	for _, b := range []byte(recipe.Header) {
		if (b < 'a' || b > 'z') && (b < 'A' || b > 'Z') && (b < '0' || b > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(b)) {
			return false
		}
	}
	name := strings.ToLower(recipe.Header)
	for _, prefix := range []string{"proxy-", "sec-", "x-forwarded-", "content-", "accept-", "if-", "access-control-"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	switch name {
	case "host", "connection", "keep-alive", "te", "trailer", "transfer-encoding", "upgrade", "expect",
		"forwarded", "via", "x-real-ip", "x-original-url", "x-rewrite-url", "x-http-method-override",
		"origin", "referer", "accept", "user-agent", "range", "cache-control", "pragma", "max-forwards", "date",
		"http2-settings", "alt-svc", "alt-used", "early-data", "priority", "x-http-method", "x-method-override",
		"authentication-info", "www-authenticate", "set-cookie", "set-cookie2", "cookie", "cookie2":
		return false
	}
	// A trailing space is useful for "Bearer "; leading whitespace would be
	// normalized by HTTP and would make configured and emitted bytes disagree.
	if strings.HasPrefix(recipe.Prefix, " ") {
		return false
	}
	for _, b := range []byte(recipe.Prefix) {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

func ValidHTTPCredentialSecret(recipe HTTPCredentialRecipe, secret []byte) bool {
	if !ValidHTTPCredentialRecipe(recipe) || len(secret) == 0 || len(recipe.Prefix)+len(secret) > HTTPCredentialValueBytes {
		return false
	}
	if secret[0] == ' ' || secret[len(secret)-1] == ' ' {
		return false
	}
	for _, b := range secret {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}
