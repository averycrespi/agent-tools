//go:build e2e

package e2e

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type boundedEventReader struct{ reader io.Reader }

func newBoundedEventReader(reader io.Reader) *boundedEventReader {
	return &boundedEventReader{reader: reader}
}

func (reader *boundedEventReader) frame(t *testing.T) []byte {
	t.Helper()
	const maximum = 4096
	var frame []byte
	one := make([]byte, 1)
	for len(frame) < maximum {
		_, err := io.ReadFull(reader.reader, one)
		require.NoError(t, err)
		frame = append(frame, one[0])
		if bytes.HasSuffix(frame, []byte("\n\n")) {
			return frame
		}
	}
	t.Fatal("SSE frame exceeded evidence bound")
	return nil
}
