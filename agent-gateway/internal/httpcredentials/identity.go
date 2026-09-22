package httpcredentials

import (
	"io"
	"time"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

func (r *Repository) newID(now time.Time) (string, error) {
	milliseconds := now.UnixMilli()
	if milliseconds < 0 || milliseconds > 1<<48-1 {
		return "", ErrUnavailable
	}
	value := make([]byte, 16)
	for index := 5; index >= 0; index-- {
		value[index] = byte(milliseconds) //nolint:gosec // Range checked 48-bit timestamp extraction.
		milliseconds >>= 8
	}
	if _, err := io.ReadFull(r.entropy, value[6:]); err != nil {
		return "", ErrUnavailable
	}
	encoded := make([]byte, 26)
	for character := range encoded {
		var bits byte
		for bit := range 5 {
			source := character*5 + bit - 2
			bits <<= 1
			if source >= 0 {
				bits |= (value[source/8] >> (7 - source%8)) & 1
			}
		}
		encoded[character] = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"[bits]
	}
	id := string(encoded)
	if id == contract.SyntheticServerID {
		return "", ErrUnavailable
	}
	return id, nil
}
