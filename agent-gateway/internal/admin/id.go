package admin

import (
	"fmt"
	"io"
	"time"
)

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func NewID(now time.Time, entropy io.Reader) (string, error) {
	milliseconds := now.UnixMilli()
	if milliseconds < 0 || milliseconds > 1<<48-1 {
		return "", fmt.Errorf("ULID timestamp is out of range")
	}
	value := make([]byte, 16)
	for index := 5; index >= 0; index-- {
		value[index] = byte(milliseconds) //nolint:gosec // The range check above bounds this 48-bit timestamp extraction.
		milliseconds >>= 8
	}
	if _, err := io.ReadFull(entropy, value[6:]); err != nil {
		return "", fmt.Errorf("generate ULID entropy: %w", err)
	}

	encoded := make([]byte, 26)
	for character := range encoded {
		var bits byte
		for bit := range 5 {
			sourceBit := character*5 + bit - 2
			bits <<= 1
			if sourceBit >= 0 {
				bits |= (value[sourceBit/8] >> (7 - sourceBit%8)) & 1
			}
		}
		encoded[character] = crockfordAlphabet[bits]
	}
	return string(encoded), nil
}
