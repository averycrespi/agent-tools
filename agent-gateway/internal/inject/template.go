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
