package admin

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewIDEncodesULIDBounds(t *testing.T) {
	minimum, err := NewID(time.UnixMilli(0), bytes.NewReader(make([]byte, ulidEntropyBytes)))
	require.NoError(t, err)
	assert.Equal(t, "00000000000000000000000000", minimum)

	maximum, err := NewID(
		time.UnixMilli(1<<48-1),
		bytes.NewReader(bytes.Repeat([]byte{0xff}, ulidEntropyBytes)),
	)
	require.NoError(t, err)
	assert.Equal(t, "7ZZZZZZZZZZZZZZZZZZZZZZZZZ", maximum)
}
