package grants

import "context"

type ctxKey struct{}

// ContextWithToken returns ctx annotated with the raw grant token string.
func ContextWithToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxKey{}, token)
}

// TokenFromContext returns the raw grant token set by ContextWithToken, or
// the empty string if none was set.
func TokenFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKey{}).(string)
	return v
}
