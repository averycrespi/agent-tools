package httppolicy

import (
	"errors"
	"strconv"
	"strings"

	"github.com/averycrespi/agent-tools/agent-gateway/internal/contract"
)

type targetError string

func (e targetError) Error() string { return string(e) }
func (e targetError) Unwrap() error { return ErrInvalid }

// RejectionReason projects only closed parser facts, never the rejected input.
func RejectionReason(err error) string {
	var reason targetError
	if errors.As(err, &reason) {
		switch reason {
		case "invalid_target_syntax", "target_too_long", "forbidden_path", "authority_mismatch":
			return string(reason)
		}
	}
	return "invalid_request_target"
}

const upperHex = "0123456789ABCDEF"

// Unicode URL input becomes UTF-8 URI bytes without normalization. Existing
// escapes and ASCII query data are opaque and retain their original spelling.
func encodeUnicode(raw string) string {
	var out strings.Builder
	for _, b := range []byte(raw) {
		if b >= 0x80 {
			out.WriteByte('%')
			out.WriteByte(upperHex[b>>4])
			out.WriteByte(upperHex[b&15])
		} else {
			out.WriteByte(b)
		}
	}
	return out.String()
}

func uriComponent(raw string, query bool) bool {
	for _, b := range []byte(raw) {
		if b >= 0x80 || unreserved(b) || strings.ContainsRune("/%!$&'()*+,;=:@", rune(b)) || query && b == '?' {
			continue
		}
		return false
	}
	return true
}

func requestPath(raw string) (string, error) {
	if len(raw) > contract.HTTPPathBytes {
		return "", targetError("target_too_long")
	}
	if raw == "" || raw[0] != '/' {
		return "", targetError("invalid_target_syntax")
	}
	var comparison strings.Builder
	for i := 0; i < len(raw); i++ {
		b := raw[i]
		if b == '%' {
			if i+2 >= len(raw) {
				return "", targetError("invalid_target_syntax")
			}
			n, err := strconv.ParseUint(raw[i+1:i+3], 16, 8)
			if err != nil {
				return "", targetError("invalid_target_syntax")
			}
			b = byte(n)
			if b < 0x20 || b == 0x7f || b == '\\' {
				return "", targetError("forbidden_path")
			}
			i += 2
			if !unreserved(b) {
				comparison.WriteByte('%')
				comparison.WriteByte(upperHex[b>>4])
				comparison.WriteByte(upperHex[b&15])
				continue
			}
		} else if !unreserved(b) && !strings.ContainsRune("/!$&'()*+,;=:@", rune(b)) {
			return "", targetError("invalid_target_syntax")
		}
		comparison.WriteByte(b)
	}
	p := comparison.String()
	for _, segment := range strings.Split(p, "/") {
		if segment == "." || segment == ".." {
			return "", targetError("forbidden_path")
		}
	}
	return p, nil
}
