package grants

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContextToken(t *testing.T) {
	ctx := context.Background()
	require.Empty(t, TokenFromContext(ctx))

	ctx = ContextWithToken(ctx, "gr_abc")
	require.Equal(t, "gr_abc", TokenFromContext(ctx))
}
