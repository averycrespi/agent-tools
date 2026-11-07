package configimport

import (
	"os"
	"testing"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/configdefinition"
	"github.com/stretchr/testify/require"
)

func TestStoppedInitExplicitlyRejectsV2UntilCoordinatedCutover(t *testing.T) {
	raw, err := os.ReadFile("../configdefinition/testdata/v2-bundle.json")
	require.NoError(t, err)
	_, err = configdefinition.DecodeBundle(raw)
	require.NoError(t, err)
	_, err = Decode(raw)
	require.ErrorIs(t, err, ErrInvalid)
	// The paired transitional v1 route/init behavior remains coherent, not a
	// translator from or unsupported promise about backend v2 creation input.
	_, err = Decode([]byte(portableFixture()))
	require.NoError(t, err)
}
