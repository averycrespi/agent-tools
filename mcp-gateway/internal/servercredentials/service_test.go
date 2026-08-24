package servercredentials

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticGenerationCodecIsVersionedCompleteAndStrict(t *testing.T) {
	encoded, err := EncodeStaticGeneration(map[string]string{"z": "last", "a": "first"})
	require.NoError(t, err)
	assert.Equal(t, `{"version":1,"values":{"a":"first","z":"last"}}`, string(encoded))
	decoded, err := DecodeStaticGeneration(encoded)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"a": "first", "z": "last"}, decoded.Values)
	_, err = DecodeStaticGeneration([]byte(`{"version":1,"values":{"a":"secret"},"unknown":true}`))
	assert.ErrorIs(t, err, ErrInvalidSecret)
	_, err = EncodeStaticGeneration(map[string]string{"a": ""})
	assert.ErrorIs(t, err, ErrInvalidSecret)
}
