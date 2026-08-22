//go:build darwin || linux

package paths

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOwnerIDMatchesEffectiveUser(t *testing.T) {
	assert.NoError(t, validateOwnerID(int64(os.Geteuid()), int64(os.Geteuid())))
	assert.ErrorIs(t, validateOwnerID(int64(os.Geteuid()+1), int64(os.Geteuid())), ErrUnsafePath)
}
