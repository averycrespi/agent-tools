package proxy

import (
	"crypto/rand"
	"net/url"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// redactedPlaceholder replaces a credential-shaped query value.
const redactedPlaceholder = "REDACTED"

// maxQueryBytes truncates a stored query string. A query is metadata for the
// audit log, not a record of the request.
const maxQueryBytes = 2048

// credentialParams are query parameter names whose values must never be
// stored (D14).
//
// The presigned-URL names matter as much as the obvious ones: agents
// legitimately fetch presigned S3 and GCS URLs, and those carry a working
// signature in the query string. Without these, the audit database and the
// dashboard would become the leak channel AC-10 exists to close.
var credentialParams = map[string]struct{}{
	"key":                  {},
	"token":                {},
	"signature":            {},
	"sig":                  {},
	"auth":                 {},
	"api_key":              {},
	"apikey":               {},
	"access_token":         {},
	"refresh_token":        {},
	"password":             {},
	"secret":               {},
	"client_secret":        {},
	"x-amz-signature":      {},
	"x-amz-credential":     {},
	"x-amz-security-token": {},
	"x-goog-signature":     {},
	"x-goog-credential":    {},
}

// redactQuery replaces credential-shaped parameter values, then truncates.
//
// Matching is case-insensitive: "X-Amz-Signature", "x-amz-signature" and
// "API_KEY" are the same parameter as far as leaking goes.
//
// A query that cannot be parsed is dropped entirely rather than stored raw.
// An unparseable query is exactly the case where a naive scan could miss a
// credential, and the audit value of keeping it is far lower than the risk.
func redactQuery(raw string) string {
	if raw == "" {
		return ""
	}

	values, err := url.ParseQuery(raw)
	if err != nil {
		return "unparseable-query-redacted"
	}

	for name, vals := range values {
		if _, sensitive := credentialParams[strings.ToLower(name)]; !sensitive {
			continue
		}
		for i := range vals {
			vals[i] = redactedPlaceholder
		}
		values[name] = vals
	}

	out := values.Encode()
	if len(out) > maxQueryBytes {
		out = out[:maxQueryBytes]
	}
	return out
}

// newRequestID returns a ULID for one request, assigned at decode so the audit
// row and any log line about the same request can be correlated.
//
// A ULID rather than a random hex string: the leading timestamp makes IDs sort
// in creation order, so audit rows can be ordered by ID alone when timestamps
// tie, and a row's approximate age is readable from the identifier itself.
func newRequestID() string {
	id, err := ulid.New(ulid.Timestamp(time.Now()), rand.Reader)
	if err != nil {
		// A request without an identifier is still auditable, and the store
		// assigns a fallback rather than dropping the row.
		return ""
	}
	return id.String()
}
