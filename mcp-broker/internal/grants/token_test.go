package grants

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewCredential(t *testing.T) {
	c1, err := NewCredential()
	require.NoError(t, err)
	c2, err := NewCredential()
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(c1.ID, "grt_"), "id must have grt_ prefix")
	require.True(t, strings.HasPrefix(c1.Token, "gr_"), "token must have gr_ prefix")
	require.Len(t, c1.ID, 4+12, "id = grt_ + 12 hex chars")
	require.NotEqual(t, c1.ID, c2.ID, "ids must be unique")
	require.NotEqual(t, c1.Token, c2.Token, "tokens must be unique")

	sum := sha256.Sum256([]byte(c1.Token))
	require.Equal(t, hex.EncodeToString(sum[:]), c1.TokenHash,
		"TokenHash must be the hex sha256 of Token")
}

func TestHashToken(t *testing.T) {
	got := HashToken("gr_known")
	sum := sha256.Sum256([]byte("gr_known"))
	require.Equal(t, hex.EncodeToString(sum[:]), got)
}
