package contract

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDesignDocumentsTheExecutableContract(t *testing.T) {
	t.Parallel()

	document, err := os.ReadFile("../../DESIGN.md")
	require.NoError(t, err)
	text := string(document)

	for _, route := range Routes() {
		require.Contains(t, text, "`"+route.Pattern+"`", route.Pattern)
		require.Contains(t, text, "`"+route.Allow()+"`", route.Pattern)
	}
	for _, problem := range Problems() {
		require.Contains(t, text, "`"+string(problem.Code)+"`", problem.Code)
		require.Contains(t, text, problem.Title, problem.Code)
	}
	for _, limit := range FixedLimits() {
		require.Contains(t, text, "`"+limit.Name+"`", limit.Name)
		require.Contains(t, text, strconv.FormatInt(limit.Maximum, 10), limit.Name)
	}
	for _, value := range []string{
		DefaultAuthority,
		CanonicalOrigin,
		ModernProtocolVersion,
		LegacyProtocolVersion,
		MediaTypeJSON,
		MediaTypeProblemJSON,
		MediaTypeEventStream,
		string(SecretSinkControllingTerminal),
		string(SecretSinkOwnerOnlyFile),
	} {
		require.True(t, strings.Contains(text, "`"+value+"`") || strings.Contains(text, value), value)
	}
}
