// Package inject provides template expansion and HTTP header injection for
// agent-gateway rules.
package inject

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/secrets"
)

// ErrSecretUnresolved is returned when a ${secrets.X} expression cannot be
// resolved (secret not found or store returns an error).
var ErrSecretUnresolved = errors.New("secret unresolved")

// ErrUnknownExpression is returned when a template contains a ${...} token
// that is not a recognised form (secrets.<ident> or agent.name / agent.id).
var ErrUnknownExpression = errors.New("unknown template expression")

// ErrSecretHostScopeViolation is returned when a ${secrets.X} expression
// resolves, but the request's host does not match any glob in the secret's
// allowed_hosts list. A scope violation is a policy error, not a transient
// lookup failure: the proxy pipeline reacts by synthesising a 403 instead
// of fail-softing to the original request.
var ErrSecretHostScopeViolation = errors.New("secret host scope violation")

// SecretsGetter resolves secret values for a given agent. The returned
// allowedHosts slice gates injection: callers must check the request host
// against it before expanding the secret into a header.
type SecretsGetter interface {
	Get(ctx context.Context, name, agent string) (value, scope string, allowedHosts []string, err error)
}

// tokenRE matches all ${...} expressions in a template string.
var tokenRE = regexp.MustCompile(`\$\{([^}]*)\}`)

// secretIdentRE validates the identifier portion of a secrets expression.
var secretIdentRE = regexp.MustCompile(`^secrets\.([A-Za-z_][A-Za-z0-9_]*)$`)

// Expand performs a single-pass substitution of template expressions in tmpl.
//
// Recognised forms:
//   - ${secrets.<ident>} — resolved via store.Get; requires store != nil.
//     The host argument is checked against the secret's allowed_hosts list;
//     a mismatch returns ErrSecretHostScopeViolation.
//   - ${agent.name}, ${agent.id} — replaced with agentName.
//
// Any other ${...} form returns ErrUnknownExpression.
// When a secret cannot be resolved (missing, store error), ErrSecretUnresolved
// is returned. host must already be normalised via hostnorm.Normalize.
//
// Substitution is strictly single-pass: values returned by the store are
// inserted verbatim and are never re-scanned for further ${...} tokens.
//
// The returned scope is the scope of the first secret resolved (empty when no
// secret tokens are present).
func Expand(ctx context.Context, tmpl, agentName, host string, store SecretsGetter) (string, string, error) {
	return expandInternal(ctx, tmpl, agentName, host, store)
}

// expandInternal is the implementation of Expand.
func expandInternal(ctx context.Context, tmpl, agentName, host string, store SecretsGetter) (string, string, error) {
	var firstScope string
	var prevScope string
	var buildErr error

	// Use ReplaceAllStringFunc to do a single left-to-right pass.
	// We can't return errors from within ReplaceAllStringFunc, so we capture
	// the first error and abort further substitution.
	result := tokenRE.ReplaceAllStringFunc(tmpl, func(token string) string {
		if buildErr != nil {
			// Earlier token already failed — return token unchanged.
			return token
		}
		// Extract the inner expression (strip ${ and }).
		inner := token[2 : len(token)-1]

		switch {
		case inner == "agent.name" || inner == "agent.id":
			return agentName

		case secretIdentRE.MatchString(inner):
			m := secretIdentRE.FindStringSubmatch(inner)
			secretName := m[1]
			if store == nil {
				buildErr = fmt.Errorf("%w: no store configured", ErrSecretUnresolved)
				return token
			}
			value, scope, allowedHosts, err := store.Get(ctx, secretName, agentName)
			if err != nil {
				buildErr = fmt.Errorf("%w: %s: %w", ErrSecretUnresolved, secretName, err)
				return token
			}
			// Validate the decrypted value before it can reach http.Header.Set.
			// This is the last defensive layer before credentials hit the
			// network: we do not trust net/http to reject CR/LF/control bytes
			// in header values (HTTP/2 framing has historically let some
			// CRLF-adjacent bytes through), so we scan bytes here ourselves.
			// Rule: reject DEL (0x7f) and any C0 control byte (< 0x20) except
			// TAB (0x09). Bytes >= 0x80 are allowed so UTF-8 secrets still
			// work; only CR/LF/NUL/DEL-class bytes are fatal for header safety.
			// The error names only the secret and bad byte offset — never the
			// secret value itself — to avoid leaking credentials into logs.
			if badIdx, ok := firstInvalidByte(value); !ok {
				buildErr = fmt.Errorf(
					"%w: secret %q contains disallowed byte 0x%02x at offset %d",
					ErrSecretInvalid, secretName, value[badIdx], badIdx,
				)
				return token
			}
			if !secrets.HostScopeAllows(allowedHosts, host) {
				buildErr = fmt.Errorf(
					"%w: secret %q is not bound to host %q (allowed: %v)",
					ErrSecretHostScopeViolation, secretName, host, allowedHosts,
				)
				return token
			}
			if firstScope == "" {
				firstScope = scope
			} else if scope != prevScope {
				slog.DebugContext(ctx, "inject: multiple credential scopes in template",
					"first_scope", firstScope, "current_scope", scope)
			}
			prevScope = scope
			return value

		default:
			buildErr = fmt.Errorf("%w: ${%s}", ErrUnknownExpression, inner)
			return token
		}
	})

	if buildErr != nil {
		return "", "", buildErr
	}
	return result, firstScope, nil
}

// firstInvalidByte scans v byte-by-byte and returns the index of the first
// byte that is not safe for an HTTP header value, together with ok=false.
// Safe bytes are: TAB (0x09) and anything >= 0x20 except DEL (0x7f). This
// accepts high-bit bytes (0x80–0xff) so UTF-8 secrets are not blocked. When
// no invalid byte is found, ok=true and the returned index is 0.
func firstInvalidByte(v string) (int, bool) {
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7f {
			return i, false
		}
	}
	return 0, true
}
