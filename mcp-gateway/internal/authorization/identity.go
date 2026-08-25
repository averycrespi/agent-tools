package authorization

import (
	"fmt"
	"io"
	"time"

	"github.com/averycrespi/agent-tools/mcp-gateway/internal/contract"
)

const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func (repository *Repository) newID(now time.Time) (string, error) {
	milliseconds := now.UnixMilli()
	if milliseconds < 0 || milliseconds > 1<<48-1 {
		return "", fmt.Errorf("authorization ULID timestamp is out of range")
	}
	value := make([]byte, 16)
	for index := 5; index >= 0; index-- {
		value[index] = byte(milliseconds) //nolint:gosec // The range check bounds this 48-bit extraction.
		milliseconds >>= 8
	}
	if _, err := io.ReadFull(repository.entropy, value[6:]); err != nil {
		return "", fmt.Errorf("generate authorization ULID entropy: %w", err)
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
	id := string(encoded)
	if id == contract.SyntheticServerID {
		return "", ErrIdentityUnavailable
	}
	return id, nil
}
