package proxy

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"
)

// ctxKey is the unexported typed context key used for request IDs.
type ctxKey int

const requestIDKey ctxKey = 1

// ulidEntropy is a package-level monotonic entropy source that is safe for
// concurrent use. Reusing a single source (rather than allocating one per
// call) ensures IDs are truly monotonic within the process and avoids
// repeated allocations on the hot path.
var (
	ulidMu      sync.Mutex
	ulidEntropy = ulid.Monotonic(rand.Reader, 0)
)

// NewULID generates a new ULID string using the package-level monotonic
// entropy source. It is safe for concurrent use from multiple goroutines.
func NewULID() string {
	ulidMu.Lock()
	id := ulid.MustNew(ulid.Timestamp(time.Now()), ulidEntropy)
	ulidMu.Unlock()
	return id.String()
}

// withRequestID returns a copy of ctx carrying id under the typed requestIDKey.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID stored in ctx, or "" if absent.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}
