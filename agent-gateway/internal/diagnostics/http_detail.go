package diagnostics

import (
	"net/http"
	"strings"
)

// HTTPSecrets extracts credential values from known secret-bearing header
// sources, not arbitrary request bodies or URLs. Callers retain no headers.
func HTTPSecrets(header http.Header) []string {
	var values []string
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "Api-Key"} {
		for _, value := range header.Values(key) {
			if len(values) > 128 {
				return values
			}
			values = append(values, value)
			if key == "Authorization" || key == "Proxy-Authorization" {
				if _, token, ok := strings.Cut(value, " "); ok {
					values = append(values, token)
				}
			}
			if key == "Cookie" || key == "Set-Cookie" {
				for _, item := range strings.Split(value, ";") {
					if len(values) > 128 {
						return values
					}
					if _, token, ok := strings.Cut(item, "="); ok {
						values = append(values, strings.Trim(strings.TrimSpace(token), `"`))
					}
				}
			}
		}
	}
	return values
}
